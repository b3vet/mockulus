// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"testing"
)

// Two stubs that can both answer one request, in one payload, at the same
// priority. Which of them serves is decided by insertion sequence, so it is
// decided by the order the importer applies the array in — and a mappings file
// listing overlapping stubs is the ordinary shape of one. The first element is
// the one that wins, which is what a file written for WireMock expects; the
// last winning would mean the same file serves a different stub here and says
// nothing about it.
func TestImportLetsTheFirstElementOfThePayloadWinTheTie(t *testing.T) {
	h := newTestHandler(t)
	h.importMappings(t, `{"mappings": [
	  {"id": "c2000001-0000-4000-8000-000000000001",
	   "request": {"method": "GET", "urlPath": "/unit/import/lane"},
	   "response": {"status": 200, "body": "first"}},
	  {"id": "c2000001-0000-4000-8000-000000000002",
	   "request": {"method": "GET", "urlPath": "/unit/import/lane"},
	   "response": {"status": 200, "body": "last"}}
	]}`)

	if body := h.serve(t, "/unit/import/lane"); body != "first" {
		t.Fatalf("the payload's %s element serves, want the first", body)
	}
	// And it wins for the stated reason rather than by accident of the index
	// merge: the tie-break is insertion sequence descending, so the first
	// element must hold the higher one.
	first := h.storedStub(t, "c2000001-0000-4000-8000-000000000001")
	last := h.storedStub(t, "c2000001-0000-4000-8000-000000000002")
	if first.Seq <= last.Seq {
		t.Fatalf("the first element got seq %d and the last %d, want the first higher",
			first.Seq, last.Seq)
	}
}

// The control on the reversal. Applying the array back to front must not turn
// into "the first element always wins": priority is compared before insertion
// sequence, so a later element that outranks an earlier one still serves. A
// reversal that swallowed priority would silently promote whichever stub a
// fixture file happened to list first over the one it deliberately ranked.
func TestImportOrderDoesNotOverridePriority(t *testing.T) {
	h := newTestHandler(t)
	h.importMappings(t, `{"mappings": [
	  {"id": "c2000002-0000-4000-8000-000000000001",
	   "request": {"method": "GET", "urlPath": "/unit/import/ranked"},
	   "response": {"status": 200, "body": "first at the default priority"}},
	  {"id": "c2000002-0000-4000-8000-000000000002",
	   "priority": 1,
	   "request": {"method": "GET", "urlPath": "/unit/import/ranked"},
	   "response": {"status": 200, "body": "last, ranked"}}
	]}`)

	if body := h.serve(t, "/unit/import/ranked"); body != "last, ranked" {
		t.Fatalf("%q serves, want the ranked last element", body)
	}
}

// An id repeated inside one payload under OVERWRITE. The occurrence applied
// last is the one that survives, and applying back to front makes that the
// first — the same end of the array that wins a precedence tie, so one rule
// explains both. One stub comes out of it, not two: the repetition is a
// collision, not two registrations that happen to share a name.
func TestImportOverwriteKeepsTheFirstOccurrenceOfARepeatedID(t *testing.T) {
	h := newTestHandler(t)
	h.importMappings(t, `{"mappings": [
	  {"id": "c2000003-0000-4000-8000-000000000001",
	   "name": "the first occurrence",
	   "request": {"method": "GET", "urlPath": "/unit/import/repeated"},
	   "response": {"status": 200, "body": "first"}},
	  {"id": "c2000003-0000-4000-8000-000000000001",
	   "name": "the last occurrence",
	   "request": {"method": "GET", "urlPath": "/unit/import/repeated"},
	   "response": {"status": 200, "body": "last"}}
	],
	"importOptions": {"duplicatePolicy": "OVERWRITE"}}`)

	if body := h.serve(t, "/unit/import/repeated"); body != "first" {
		t.Fatalf("the stub serves %q, want the first occurrence's document", body)
	}
	if n := h.countStubs(t); n != 1 {
		t.Fatalf("the deployment holds %d stubs, want the one id the payload named", n)
	}
	rec := h.admin(t, "GET", "/__admin/mappings/c2000003-0000-4000-8000-000000000001", "")
	assertJSONField(t, rec.Body.Bytes(), "name", "the first occurrence")
}

// The same payload under IGNORE, and the reason the apply order and the
// duplicate lookup have to move together. IGNORE drops an occurrence that
// collides with something already there — which, walking back to front, is
// every occurrence except the last one to be applied, so the document that
// survives is the payload's last. Deciding duplicates against the store as it
// was before the batch would make all three of them look new and leave the
// first, changing a behaviour that agrees today.
func TestImportIgnoreKeepsTheLastOccurrenceOfARepeatedID(t *testing.T) {
	h := newTestHandler(t)
	h.importMappings(t, `{"mappings": [
	  {"id": "c2000004-0000-4000-8000-000000000001",
	   "name": "the first occurrence",
	   "request": {"method": "GET", "urlPath": "/unit/import/repeated-ignore"},
	   "response": {"status": 200, "body": "first"}},
	  {"id": "c2000004-0000-4000-8000-000000000001",
	   "name": "the middle occurrence",
	   "request": {"method": "GET", "urlPath": "/unit/import/repeated-ignore"},
	   "response": {"status": 200, "body": "middle"}},
	  {"id": "c2000004-0000-4000-8000-000000000001",
	   "name": "the last occurrence",
	   "request": {"method": "GET", "urlPath": "/unit/import/repeated-ignore"},
	   "response": {"status": 200, "body": "last"}}
	],
	"importOptions": {"duplicatePolicy": "IGNORE"}}`)

	if body := h.serve(t, "/unit/import/repeated-ignore"); body != "last" {
		t.Fatalf("the stub serves %q, want the last occurrence's document", body)
	}
	if n := h.countStubs(t); n != 1 {
		t.Fatalf("the deployment holds %d stubs, want the one id the payload named", n)
	}
	rec := h.admin(t, "GET", "/__admin/mappings/c2000004-0000-4000-8000-000000000001", "")
	assertJSONField(t, rec.Body.Bytes(), "name", "the last occurrence")
}

// A payload that repeats an id the deployment already holds. Both occurrences
// are overwrites of the same stub, so the first is applied last and its
// document is the one left standing — and the stub keeps the sequence it was
// registered with, because an edit must not change which stub answers a
// request it was not competing for.
func TestImportOverwriteOfAStoredIDKeepsTheFirstOccurrenceAndTheSequence(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, `{"id": "c2000005-0000-4000-8000-000000000001",
	  "request": {"method": "GET", "urlPath": "/unit/import/boundary"},
	  "response": {"status": 200, "body": "registered"}}`)
	registered := h.storedStub(t, "c2000005-0000-4000-8000-000000000001")

	h.importMappings(t, `{"mappings": [
	  {"id": "c2000005-0000-4000-8000-000000000001",
	   "request": {"method": "GET", "urlPath": "/unit/import/boundary"},
	   "response": {"status": 200, "body": "first"}},
	  {"id": "c2000005-0000-4000-8000-000000000001",
	   "request": {"method": "GET", "urlPath": "/unit/import/boundary"},
	   "response": {"status": 200, "body": "last"}}
	],
	"importOptions": {"duplicatePolicy": "OVERWRITE"}}`)

	if body := h.serve(t, "/unit/import/boundary"); body != "first" {
		t.Fatalf("the stub serves %q, want the first occurrence's document", body)
	}
	if got := h.storedStub(t, "c2000005-0000-4000-8000-000000000001"); got.Seq != registered.Seq {
		t.Fatalf("the overwritten stub moved from seq %d to %d", registered.Seq, got.Seq)
	}
}

// The control on the boundary case. An id the deployment already holds is a
// collision whichever occurrence of it the batch reaches first, so under IGNORE
// nothing in the payload is applied and the incumbent is untouched — the
// property a suite relies on when it imports a fixture file to seed only what
// is missing.
func TestImportIgnoreLeavesAStoredStubUntouched(t *testing.T) {
	h := newTestHandler(t)
	h.createStub(t, `{"id": "c2000006-0000-4000-8000-000000000001",
	  "name": "the incumbent",
	  "request": {"method": "GET", "urlPath": "/unit/import/incumbent"},
	  "response": {"status": 200, "body": "registered"}}`)

	h.importMappings(t, `{"mappings": [
	  {"id": "c2000006-0000-4000-8000-000000000001",
	   "name": "the challenger",
	   "request": {"method": "GET", "urlPath": "/unit/import/incumbent"},
	   "response": {"status": 200, "body": "first"}},
	  {"id": "c2000006-0000-4000-8000-000000000001",
	   "name": "the challenger",
	   "request": {"method": "GET", "urlPath": "/unit/import/incumbent"},
	   "response": {"status": 200, "body": "last"}}
	],
	"importOptions": {"duplicatePolicy": "IGNORE"}}`)

	if body := h.serve(t, "/unit/import/incumbent"); body != "registered" {
		t.Fatalf("the stub serves %q, want the incumbent's document", body)
	}
	rec := h.admin(t, "GET", "/__admin/mappings/c2000006-0000-4000-8000-000000000001", "")
	assertJSONField(t, rec.Body.Bytes(), "name", "the incumbent")
}

// A payload whose mappings carry no id at all. The server names them, so
// nothing in the batch can collide — and the tie between two of them is still
// decided by where they sat in the array, which is the case that would pass
// whether or not ids were involved in the reversal.
func TestImportWithoutIDsStillLetsTheFirstElementWin(t *testing.T) {
	h := newTestHandler(t)
	h.importMappings(t, `{"mappings": [
	  {"request": {"method": "GET", "urlPath": "/unit/import/anonymous"},
	   "response": {"status": 200, "body": "first"}},
	  {"request": {"method": "GET", "urlPath": "/unit/import/anonymous"},
	   "response": {"status": 200, "body": "last"}}
	]}`)

	if body := h.serve(t, "/unit/import/anonymous"); body != "first" {
		t.Fatalf("the payload's %s element serves, want the first", body)
	}
	if n := h.countStubs(t); n != 2 {
		t.Fatalf("the deployment holds %d stubs, want both anonymous mappings", n)
	}
}
