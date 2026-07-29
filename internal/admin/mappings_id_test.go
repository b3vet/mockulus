// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"net/http"
	"strings"
	"testing"
)

// TestASegmentThatIsNotAUUIDIsRefusedRatherThanNotFound separates the two ways
// an id in a path can fail to name a stub.
//
// A well-formed UUID that was never registered is a 404: it could have named a
// stub and does not. A segment that is not a UUID at all could never have named
// one, and answering 404 there invites a caller to re-derive the id and ask
// again for something that was never addressable. WireMock answers it 400 with
// code 10, and so does this.
func TestASegmentThatIsNotAUUIDIsRefusedRatherThanNotFound(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, idCaseStub)

	// Both of these fail to parse as a UUID, so both take the same branch.
	//
	// The short group is the interesting one. Java's parser reads `-ab-` as a
	// padded `-00ab-` and WireMock therefore resolves the stub; Go's rejects it,
	// which makes it a non-UUID here by exactly the rule this server states.
	// mockulus differs from WireMock on this segment whichever answer it gives —
	// it will not resolve a spelling it refuses to store (deviation 24) — and of
	// the two answers available, "this is not a valid UUID" is the true one and
	// "no such stub" is not.
	segments := []string{"not-a-uuid", "c1000001-ab-4000-8000-0000000000cd"}
	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete}

	for _, segment := range segments {
		for _, method := range methods {
			t.Run(method+"/"+segment, func(t *testing.T) {
				got := h.admin(t, method, "/__admin/mappings/"+segment, "{}")
				if got.Code != http.StatusBadRequest {
					t.Fatalf("%s answered %d, want 400", method, got.Code)
				}
				if !strings.Contains(got.Body.String(), "not a valid UUID") {
					t.Errorf("the body does not say why: %s", got.Body.String())
				}
				if !strings.Contains(got.Body.String(), `"code":10`) {
					t.Errorf("want the malformed-request code 10: %s", got.Body.String())
				}
			})
		}
	}

	// The control, and the whole point of the split: an id that is a UUID and
	// simply is not there keeps its 404, so this did not turn every miss into a
	// 400. And the stub itself is untouched by any of it.
	unregistered := "aaaaaaaa-0000-4000-8000-000000000001"
	if got := h.admin(t, http.MethodGet, "/__admin/mappings/"+unregistered, ""); got.Code != http.StatusNotFound {
		t.Errorf("an unregistered UUID answered %d, want 404", got.Code)
	}
	if body := h.serve(t, "/unit/id-case"); body != "registered" {
		t.Errorf("the stub now serves %q", body)
	}
}

// The stub every case here addresses. It is registered in lower case, which is
// the only spelling the store ever holds: registration canonicalises (SPEC
// §5.2), so an id in any other case reaching the path is a spelling of an id
// that is already there rather than a different one.
const idCaseStub = `{"id": "c1000001-00ab-4000-8000-0000000000cd",
  "request": {"method": "GET", "urlPath": "/unit/id-case"},
  "response": {"status": 200, "body": "registered"}}`

const (
	idCaseLower = "c1000001-00ab-4000-8000-0000000000cd"
	idCaseUpper = "C1000001-00AB-4000-8000-0000000000CD"
)

// A path id differing from the registered one only by case must resolve, on
// every verb that takes one. Comparing the segment as text made the admin API
// disagree with itself: the create canonicalises, so the id a client holds is
// not necessarily the id it sent, and every later call with its own spelling
// answered 404 for a stub sitting in the store.
func TestMappingPathIDIsCaseInsensitive(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, idCaseStub)

	got := h.admin(t, http.MethodGet, "/__admin/mappings/"+idCaseUpper, "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET through the upper-case id answered %d, want 200", got.Code)
	}
	// The document names the identity it was stored under, not the one the path
	// spelled: resolving the two spellings to one stub is only worth anything if
	// the answer then reports one identity.
	assertJSONField(t, got.Body.Bytes(), "id", idCaseLower)

	put := h.admin(t, http.MethodPut, "/__admin/mappings/"+idCaseUpper,
		`{"request": {"method": "GET", "urlPath": "/unit/id-case"},
		  "response": {"status": 200, "body": "replaced"}}`)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT through the upper-case id answered %d, want 200", put.Code)
	}
	assertJSONField(t, put.Body.Bytes(), "id", idCaseLower)
	if body := h.serve(t, "/unit/id-case"); body != "replaced" {
		t.Fatalf("the stub serves %q after the PUT, want %q", body, "replaced")
	}

	if del := h.admin(t, http.MethodDelete, "/__admin/mappings/"+idCaseUpper, ""); del.Code != http.StatusOK {
		t.Fatalf("DELETE through the upper-case id answered %d, want 200", del.Code)
	}
	if get := h.admin(t, http.MethodGet, "/__admin/mappings/"+idCaseLower, ""); get.Code != http.StatusNotFound {
		t.Fatalf("the stub reads back %d after the delete, want 404", get.Code)
	}
}

// The control on the case above. Case-folding an id must not turn the lookup
// into one that finds something for any well-formed UUID: an id nothing was
// registered under is still absent, and a client that mistypes one hears so.
func TestMappingPathIDStillMissesAnUnregisteredUUID(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, idCaseStub)

	for _, spelling := range []string{
		"C1000001-00AB-4000-8000-0000000000FF",
		"c1000001-00ab-4000-8000-0000000000ff",
	} {
		if got := h.admin(t, http.MethodGet, "/__admin/mappings/"+spelling, ""); got.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404", spelling, got.Code)
		}
	}
}

// The other control, and the one that costs something to get wrong. Go's UUID
// parser accepts three spellings WireMock and this server both refuse to
// register — dashless, `urn:uuid:`-prefixed and brace-wrapped — so a helper
// that only parsed would let a client read and *write* a stub through an
// identity it could never have created. The write half is the damage: a PUT
// through such a segment that resolved would replace a stub whose id the caller
// never spelled correctly.
func TestMappingPathIDRefusesSpellingsRegistrationRefuses(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, idCaseStub)

	// Every one of these is a UUID value in a spelling a stub cannot be
	// registered under, so each names no stub — the same answer an id that was
	// never registered gets. A segment that is not a UUID at all is a different
	// question with a different answer; see the test below.
	segments := map[string]string{
		"brace-wrapped": "%7B" + idCaseLower + "%7D",
		"urn-prefixed":  "urn:uuid:" + idCaseLower,
		"dashless":      "c100000100ab400080000000000000cd",
	}
	for name, segment := range segments {
		t.Run(name, func(t *testing.T) {
			if got := h.admin(t, http.MethodGet, "/__admin/mappings/"+segment, ""); got.Code != http.StatusNotFound {
				t.Errorf("GET answered %d, want 404", got.Code)
			}
			if got := h.admin(t, http.MethodDelete, "/__admin/mappings/"+segment, ""); got.Code != http.StatusNotFound {
				t.Errorf("DELETE answered %d, want 404", got.Code)
			}
			put := h.admin(t, http.MethodPut, "/__admin/mappings/"+segment,
				`{"request": {"method": "GET", "urlPath": "/unit/id-case"},
				  "response": {"status": 200, "body": "written through `+name+`"}}`)
			if put.Code != http.StatusNotFound {
				t.Errorf("PUT answered %d, want 404", put.Code)
			}
			if body := h.serve(t, "/unit/id-case"); body != "registered" {
				t.Errorf("the stub serves %q, so the refused PUT wrote through anyway", body)
			}
		})
	}
}
