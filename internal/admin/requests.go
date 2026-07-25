// SPDX-License-Identifier: Apache-2.0

package admin

import (
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

// serveEvent is a decoded journal entry, kept as raw JSON plus the fields
// criteria are evaluated against.
type serveEvent struct {
	ID      string
	Raw     json.RawMessage
	Request eventRequest
	Matched bool
}

type eventRequest struct {
	Method  string         `json:"method"`
	URL     string         `json:"url"`
	Headers map[string]any `json:"headers"`
	Cookies map[string]any `json:"cookies"`
	Query   map[string]any `json:"queryParams"`
	Body    string         `json:"body"`
}

// listRequests returns journal entries newest-first.
func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}

	events, err := h.loadEvents(r, queryInt(r, "limit", h.cfg.JournalQueryScanLimit))
	if err != nil {
		h.storeError(w, "query_journal", err)
		return
	}

	raws := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		raws = append(raws, e.Raw)
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"requests": raws,
		"meta":     map[string]any{"total": len(raws)},
	})
}

// getRequest returns one entry.
func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	entry, err := h.journal.GetJournalEntry(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		wmcompat.WriteErrors(w, http.StatusNotFound,
			wmcompat.NewError(wmcompat.CodeMalformed, "no journal entry with that id"))
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
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"count": len(matched)})
}

// findRequests returns the recorded requests satisfying the criteria.
func (h *Handler) findRequests(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.matchRequests(w, r)
	if !ok {
		return
	}
	raws := make([]json.RawMessage, 0, len(matched))
	for _, e := range matched {
		raws = append(raws, e.Raw)
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"requests": raws})
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
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"requests": raws})
}

// unmatchedRequests returns the entries that matched no stub, which is the
// first thing anyone looks at when a test fails.
func (h *Handler) unmatchedRequests(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}
	events, err := h.loadEvents(r, h.cfg.JournalQueryScanLimit)
	if err != nil {
		h.storeError(w, "query_journal", err)
		return
	}

	raws := make([]json.RawMessage, 0)
	for _, e := range events {
		if !e.Matched {
			raws = append(raws, e.Raw)
		}
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"requests": raws,
		"meta":     map[string]any{"total": len(raws)},
	})
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

	events, err := h.loadEvents(r, h.cfg.JournalQueryScanLimit)
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

// loadEvents reads and decodes the newest entries within the scan window.
//
// The window is a guard rail, not a limitation of the query: the journal serves
// functional tests, not analytics, and a count beyond it under-reports by
// design (deviation #16).
func (h *Handler) loadEvents(r *http.Request, limit int) ([]serveEvent, error) {
	q := store.JournalQuery{Limit: limit}
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			q.Since = t
		}
	}

	entries, err := h.journal.QueryJournal(r.Context(), q)
	if err != nil {
		return nil, err
	}

	out := make([]serveEvent, 0, len(entries))
	for _, entry := range entries {
		var decoded struct {
			Request    eventRequest `json:"request"`
			WasMatched bool         `json:"wasMatched"`
		}
		if err := json.Unmarshal(entry.Data, &decoded); err != nil {
			continue
		}
		out = append(out, serveEvent{
			ID:      entry.ID,
			Raw:     entry.Data,
			Request: decoded.Request,
			Matched: decoded.WasMatched,
		})
	}
	return out, nil
}
