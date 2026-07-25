// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/b3vet/mockulus/internal/match"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// The near-miss endpoints answer "why did nothing match?" on demand. Computing
// them here rather than on the request path is the whole point: the cost lands
// on someone waiting for a diagnostic, not on traffic (SPEC §5.4, §6.8).

// nearMissRequest is the request description these endpoints accept.
type nearMissRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Cookies map[string]string `json:"cookies"`
	Body    string            `json:"body"`
}

// nearMissesForRequest scores a supplied request against the current snapshot.
func (h *Handler) nearMissesForRequest(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var desc nearMissRequest
	if err := json.Unmarshal(raw, &desc); err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed,
			"the body must describe a request: "+err.Error()))
		return
	}

	misses := h.scoreDescribedRequest(desc)
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"nearMisses": misses})
}

// nearMissesForPattern is the request-pattern form. Both endpoints score
// against the same snapshot; the pattern form simply describes the request
// differently, and is accepted for compatibility.
func (h *Handler) nearMissesForPattern(w http.ResponseWriter, r *http.Request) {
	h.nearMissesForRequest(w, r)
}

// unmatchedNearMisses scores every unmatched journal entry.
func (h *Handler) unmatchedNearMisses(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}

	events, err := h.loadEvents(r, h.cfg.JournalQueryScanLimit)
	if err != nil {
		h.storeError(w, "query_journal", err)
		return
	}

	out := make([]map[string]any, 0)
	for _, e := range events {
		if e.Matched {
			continue
		}
		desc := nearMissRequest{
			Method: e.Request.Method,
			URL:    e.Request.URL,
			Body:   e.Request.Body,
		}
		out = append(out, map[string]any{
			"request":    json.RawMessage(e.Raw),
			"nearMisses": h.scoreDescribedRequest(desc),
		})
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"nearMisses": out})
}

// scoreDescribedRequest builds a request from the description and scores it.
//
// A synthetic http.Request is constructed rather than a parallel scoring path,
// so the near-miss answer is computed by exactly the machinery that decides a
// real match — a diagnostic that disagreed with the matcher would be worse than
// none.
func (h *Handler) scoreDescribedRequest(desc nearMissRequest) []wmcompat.NearMiss {
	method := desc.Method
	if method == "" {
		method = http.MethodGet
	}
	url := desc.URL
	if url == "" {
		url = "/"
	}

	req := httptest.NewRequest(method, url, strings.NewReader(desc.Body))
	for name, value := range desc.Headers {
		req.Header.Set(name, value)
	}
	for name, value := range desc.Cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	pr := match.AcquireRequest(req, []byte(desc.Body))
	defer match.ReleaseRequest(pr)

	return h.engine.Snapshot().NearMisses(pr, wmcompat.NearMissCount)
}
