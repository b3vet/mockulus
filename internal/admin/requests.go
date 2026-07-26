// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Verification reads the journal. Criteria are evaluated **in process** using
// the same compiled-matcher machinery that selects stubs, rather than being
// translated into store queries — so there is one definition of what a request
// pattern means, and no second matching implementation to keep in step
// (SPEC §11.3).
//
// The store's job is narrow: hand back the newest N entries in a time window.
// Everything else happens here.

// journalDisabled reports the error every journal-dependent endpoint returns
// when the journal is off, which is the default (deviation #1).
func (h *Handler) journalDisabled(w http.ResponseWriter) {
	wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeJournalDisabled,
		"the request journal is disabled; set journal_enabled to record and verify requests"))
}

// journalReportedDisabled is the `requestJournalDisabled` flag WireMock puts on
// every verification envelope, and a client library reads it before trusting a
// count: a verify() against a deployment with no journal must fail loudly
// rather than report zero calls. Reaching any of these writers means the
// journal is on, so it is always false — the disabled path answers with an
// error instead of an envelope (deviation #1).
const journalReportedDisabled = false

// serveEvent is a decoded journal entry, kept as raw JSON plus the fields
// criteria are evaluated against.
type serveEvent struct {
	ID  string
	Raw json.RawMessage
	// RequestRaw is the entry's `request` sub-document, untouched. Half the
	// verification endpoints return serve events and half return the bare
	// logged requests inside them, so both forms have to survive decoding.
	RequestRaw json.RawMessage
	Request    eventRequest
	Matched    bool
}

type eventRequest struct {
	Method  string         `json:"method"`
	URL     string         `json:"url"`
	Headers map[string]any `json:"headers"`
	Cookies map[string]any `json:"cookies"`
	Query   map[string]any `json:"queryParams"`
	Body    string         `json:"body"`
}

// listRequests returns journal entries newest-first, as serve events.
//
// `limit` is applied here rather than pushed into the store query, because
// `meta.total` counts the window the query considered rather than the page it
// returned — measured on pinned WireMock 3.13.2, where `?limit=1` against a
// four-entry journal answers one request and `"total": 4`. A client reads that
// field to decide whether it has seen everything, so a total that only ever
// equalled the page size would tell it the journal is exhausted every time.
func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}

	q, ok := h.journalBounds(w, r)
	if !ok {
		return
	}
	events, err := h.loadEvents(r.Context(), q)
	if err != nil {
		h.storeError(w, "query_journal", err)
		return
	}
	total := len(events)

	if limit := queryInt(r, "limit", total); limit < total {
		events = events[:limit]
	}
	raws := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		raws = append(raws, e.Raw)
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"requests":               raws,
		"meta":                   map[string]any{"total": total},
		"requestJournalDisabled": journalReportedDisabled,
	})
}

// getRequest returns one entry.
//
// An unknown id is a bare 404 with no body, which is the not-found a WireMock
// client library already handles — the same reasoning as an unknown mapping id
// (§5.1), and measured the same way on the pinned oracle.
func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	entry, err := h.journal.GetJournalEntry(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		h.storeError(w, "get_journal_entry", err)
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, json.RawMessage(entry.Data))
}

// deleteRequest removes one entry.
func (h *Handler) deleteRequest(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	if err := h.journal.DeleteJournalEntry(r.Context(), r.PathValue("id")); err != nil {
		h.storeError(w, "delete_journal_entry", err)
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
}

// clearRequests empties the journal.
func (h *Handler) clearRequests(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	if err := h.journal.ClearJournal(r.Context()); err != nil {
		h.storeError(w, "clear_journal", err)
		return
	}
	h.log.Info("journal cleared")
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
}

// countRequests reports how many recorded requests satisfy the criteria.
func (h *Handler) countRequests(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.matchRequests(w, r)
	if !ok {
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"count":                  len(matched),
		"requestJournalDisabled": journalReportedDisabled,
	})
}

// findRequests returns the recorded requests satisfying the criteria.
//
// The elements are the logged *requests*, not the serve events that hold them —
// measured against pinned WireMock 3.13.2, where this endpoint and
// `/requests/unmatched` both answer with bare request documents while
// `GET /__admin/requests` answers with serve events. A client deserializes this
// into typed LoggedRequests, so handing it a serve event would leave every
// field it reads — url, method, headers — null.
func (h *Handler) findRequests(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.matchRequests(w, r)
	if !ok {
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"requests":               loggedRequests(matched),
		"requestJournalDisabled": journalReportedDisabled,
	})
}

// removeRequests deletes the entries satisfying the criteria.
func (h *Handler) removeRequests(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.matchRequests(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	for _, e := range matched {
		if err := h.journal.DeleteJournalEntry(ctx, e.ID); err != nil {
			h.storeError(w, "delete_journal_entry", err)
			return
		}
	}
	raws := make([]json.RawMessage, 0, len(matched))
	for _, e := range matched {
		raws = append(raws, e.Raw)
	}
	// `serveEvents`, not `requests`: what was removed is reported in full,
	// which is the one place WireMock answers a criteria query with the whole
	// event rather than the request inside it.
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"serveEvents": raws})
}

// unmatchedRequests returns the entries that matched no stub, which is the
// first thing anyone looks at when a test fails.
func (h *Handler) unmatchedRequests(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	q, ok := h.journalBounds(w, r)
	if !ok {
		return
	}
	events, err := h.loadEvents(r.Context(), q)
	if err != nil {
		h.storeError(w, "query_journal", err)
		return
	}

	unmatched := make([]serveEvent, 0, len(events))
	for _, e := range events {
		if !e.Matched {
			unmatched = append(unmatched, e)
		}
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"requests":               loggedRequests(unmatched),
		"requestJournalDisabled": journalReportedDisabled,
	})
}

// loggedRequests projects serve events down to the request documents inside
// them, which is what the criteria endpoints return.
func loggedRequests(events []serveEvent) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		out = append(out, e.RequestRaw)
	}
	return out
}

// matchRequests loads the scan window and applies the criteria in the body.
func (h *Handler) matchRequests(w http.ResponseWriter, r *http.Request) ([]serveEvent, bool) {
	if h.journal == nil {
		h.journalDisabled(w)
		return nil, false
	}

	raw, ok := h.readBody(w, r)
	if !ok {
		return nil, false
	}

	// The criteria are a request pattern — the same model a stub's `request` is
	// — so compiling them through the stub compiler reuses every matcher and
	// every validation rule.
	criteria, errs := compileCriteria(raw, h.stubOpts)
	if errs != nil {
		errs.WriteList(w)
		return nil, false
	}

	q, ok := h.journalBounds(w, r)
	if !ok {
		return nil, false
	}
	events, err := h.loadEvents(r.Context(), q)
	if err != nil {
		h.storeError(w, "query_journal", err)
		return nil, false
	}

	var matched []serveEvent
	for _, e := range events {
		if criteria.matches(&e) {
			matched = append(matched, e)
		}
	}
	return matched, true
}

// journalBounds reads the window a journal query runs over.
//
// A `since` that is not a timestamp is rejected rather than dropped. Ignoring it
// would answer a windowed verification with the entire journal, so a
// `verify(exactly(1))` written to look at the last minute would silently count
// every call the deployment has ever served — the accept-and-behave-differently
// P3 exists to prevent. WireMock rejects it too, naming the same parameter.
func (h *Handler) journalBounds(w http.ResponseWriter, r *http.Request) (store.JournalQuery, bool) {
	// The scan limit is a guard rail, not a limitation of the query: the journal
	// serves functional tests, not analytics, and a count beyond it under-reports
	// by design (deviation #16).
	q := store.JournalQuery{Limit: h.cfg.JournalQueryScanLimit}

	since := r.URL.Query().Get("since")
	if since == "" {
		return q, true
	}
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeMalformed, "since",
			since+" is not an ISO-8601 timestamp"))
		return q, false
	}
	q.Since = t
	return q, true
}

// loadEvents reads and decodes the entries the query selects.
func (h *Handler) loadEvents(ctx context.Context, q store.JournalQuery) ([]serveEvent, error) {
	entries, err := h.journal.QueryJournal(ctx, q)
	if err != nil {
		return nil, err
	}

	out := make([]serveEvent, 0, len(entries))
	for _, entry := range entries {
		// The `request` sub-document is captured raw as well as decoded: the
		// endpoints answering with logged requests hand it straight back, and
		// re-serialising the decoded struct instead would drop every field
		// criteria never look at — absoluteUrl, clientIp, the timestamps — from
		// a document a client deserializes whole.
		var decoded struct {
			Request    json.RawMessage `json:"request"`
			WasMatched bool            `json:"wasMatched"`
		}
		if err := json.Unmarshal(entry.Data, &decoded); err != nil {
			continue
		}
		var request eventRequest
		if err := json.Unmarshal(decoded.Request, &request); err != nil {
			continue
		}
		out = append(out, serveEvent{
			ID:         entry.ID,
			Raw:        entry.Data,
			RequestRaw: decoded.Request,
			Request:    request,
			Matched:    decoded.WasMatched,
		})
	}
	return out, nil
}
