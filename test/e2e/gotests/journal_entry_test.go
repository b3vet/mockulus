// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// The per-entry routes are the only part of the verification API whose input is
// a value the server invented. A client reads an id out of a listing and then
// asks for or deletes that entry by it, so every call after the first is built
// from the answer to the one before — which a corpus case cannot do, its steps
// being fixed text.
//
// Owning the process buys the rest of the claim too: the journal here holds
// only what this test put in it, so "the entry is gone" is read off the whole
// collection rather than inferred from one query that stopped returning it.
//
// Statuses are spelt as numbers throughout, because the catalog's evidence
// contract for these two routes is the status itself and it has to be visible
// in the code that asserts it rather than in the sentence above it.

// journalEntryStub is what the recorded call hits, so the entry fetched by id
// carries a matched serve event rather than an unmatched one.
const journalEntryStub = `{
  "id": "51000010-0000-4000-8000-000000000001",
  "request": {"method": "GET", "urlPath": "/e2e/journal-entry/order"},
  "response": {"status": 200, "body": "order"}}`

// unknownEntryID is a well-formed key nothing was ever written under, so the
// 404 below is about the entry being absent rather than the id being unusable.
const unknownEntryID = "2ZzzzzzzzzzzzzzzzzzzzzzzzzZ"

func TestJournalEntryIsAddressableByTheIdItWasListedUnder(t *testing.T) {
	m := start(t, map[string]string{"MOCKULUS_JOURNAL_ENABLED": "true"})
	m.registerStub(t, journalEntryStub)

	if body := m.get(t, "/e2e/journal-entry/order?ref=7", 200); body != "order" {
		t.Fatalf("served body = %q, want %q", body, "order")
	}

	// The journal is eventually consistent (deviation #10), so the listing is
	// polled rather than read once.
	listed := m.awaitOneServeEvent(t)
	if listed.ID == "" {
		t.Fatal("the listed serve event carries no id, so nothing can address it")
	}
	if listed.Request.URL != "/e2e/journal-entry/order?ref=7" {
		t.Fatalf("listed url = %q, want the url that was served", listed.Request.URL)
	}

	// The id in the listing is the id the entry answers to. Minting one for the
	// document and another for the store key would leave every follow-up call a
	// 404 against an entry that is plainly in the listing.
	fetched := m.serveEvent(t, "/__admin/requests/"+listed.ID, 200)
	if fetched.ID != listed.ID || fetched.Request.URL != listed.Request.URL {
		t.Fatalf("GET /__admin/requests/{id} returned %+v, want the listed entry %+v",
			fetched, listed)
	}

	// An id nothing was written under is a bare 404 — the not-found a WireMock
	// client library already handles, the same one an unknown mapping id gets.
	m.statusOf(t, http.MethodGet, "/__admin/requests/"+unknownEntryID, 404)

	m.statusOf(t, http.MethodDelete, "/__admin/requests/"+listed.ID, 200)

	// Gone from the collection, not merely from the route that named it: a
	// delete that only unlinked the entry from its id would leave a later
	// verification counting a call the caller believes it erased.
	if events := m.serveEvents(t); len(events) != 0 {
		t.Fatalf("the journal holds %d entries after the delete, want none", len(events))
	}
	m.statusOf(t, http.MethodGet, "/__admin/requests/"+listed.ID, 404)
}

// serveEventDoc is the part of a serve event these assertions read.
type serveEventDoc struct {
	ID      string `json:"id"`
	Request struct {
		URL string `json:"url"`
	} `json:"request"`
}

// awaitOneServeEvent polls the listing until the one call this test made has
// been recorded. More than one would mean the process is not this test's alone,
// which every assertion below depends on.
func (m *mockulus) awaitOneServeEvent(t *testing.T) serveEventDoc {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		events := m.serveEvents(t)
		if len(events) == 1 {
			return events[0]
		}
		if len(events) > 1 {
			t.Fatalf("the journal holds %d entries, want the one call this test made",
				len(events))
		}
		if time.Now().After(deadline) {
			t.Fatal("no journal entry appeared within 10s")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// serveEvents reads the whole journal.
func (m *mockulus) serveEvents(t *testing.T) []serveEventDoc {
	t.Helper()

	var listing struct {
		Requests []serveEventDoc `json:"requests"`
	}
	m.admin(t, http.MethodGet, "/__admin/requests", 200, &listing)
	return listing.Requests
}

// serveEvent reads one entry.
func (m *mockulus) serveEvent(t *testing.T, path string, want int) serveEventDoc {
	t.Helper()

	var event serveEventDoc
	m.admin(t, http.MethodGet, path, want, &event)
	return event
}

// statusOf issues an admin call for its status alone.
func (m *mockulus) statusOf(t *testing.T, method, path string, want int) {
	t.Helper()
	m.admin(t, method, path, want, nil)
}

// admin issues an admin call, checks the status, and decodes the body when the
// caller asked for it.
func (m *mockulus) admin(t *testing.T, method, path string, want int, into any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, m.adminURL(path), nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp, err := harnessClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d, body %s", method, path, resp.StatusCode, want, body)
	}
	if into == nil {
		return
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("%s %s: body %s is not the expected document: %v", method, path, body, err)
	}
}
