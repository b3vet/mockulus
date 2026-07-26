// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"github.com/b3vet/mockulus/internal/match"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// The near-miss endpoints answer "why did nothing match?" on demand. Computing
// them here rather than on the request path is the whole point: the cost lands
// on someone waiting for a diagnostic, not on traffic (SPEC §5.4, §6.8).
//
// The two POST endpoints rank in opposite directions, and pinned WireMock
// 3.13.2 keeps them apart. `/near-misses/request` is handed a request and ranks
// the *stub mappings* against it — no journal involved, so it answers on a
// deployment that never turned journaling on, which is the deployment anyone
// debugging a stub that will not match is standing in front of.
// `/near-misses/request-pattern` is handed a pattern and ranks the *recorded
// requests* against it, so it needs the journal and reports the disabled error
// without one. Serving one endpoint from the other answers a different question
// than the caller asked, in a body shaped like the one they wanted.

// nearMissRequest is the logged-request description `/near-misses/request`
// accepts, and the projection of a recorded entry the pattern endpoint scores.
type nearMissRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Cookies map[string]string `json:"cookies"`
	Body    string            `json:"body"`
}

// nearMissesForRequest ranks the current snapshot's stubs against a request.
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

	// One snapshot for the scoring and for the mapping lookups behind it: a
	// second read could land after a concurrent registration and report a
	// distance for a stub the other half of the answer no longer knows.
	snap := h.engine.Snapshot()
	echoed := describedRequestDoc(desc)

	out := make([]map[string]any, 0, wmcompat.NearMissCount)
	for _, miss := range scoreAgainstSnapshot(snap, desc) {
		out = append(out, map[string]any{
			"request":     echoed,
			"stubMapping": stubDocument(snap, miss.StubID),
			"matchResult": matchResult(miss),
		})
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"nearMisses": out})
}

// nearMissesForPattern ranks the recorded requests against a supplied pattern,
// which is how a team asks "what did arrive, and how close was it to what I
// expected?" after a verification came back empty.
func (h *Handler) nearMissesForPattern(w http.ResponseWriter, r *http.Request) {
	if h.journal == nil {
		h.journalDisabled(w)
		return
	}

	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	// Compiled through the stub compiler for the reason the verification
	// criteria are (§11.3): the pattern this ranks against has to mean exactly
	// what it would have meant written on a stub.
	pattern, errs := compileCriteria(raw, h.stubOpts)
	if errs != nil {
		errs.WriteList(w)
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

	scored := make([]patternMiss, 0, len(events))
	for i := range events {
		e := &events[i]
		scored = append(scored, patternMiss{
			event: e,
			miss:  scoreOne(pattern.stub, recordedRequestDesc(e)),
		})
	}
	rankPatternMisses(scored)

	echoed := json.RawMessage(raw)
	out := make([]map[string]any, 0, wmcompat.NearMissCount)
	for _, p := range scored[:min(len(scored), wmcompat.NearMissCount)] {
		out = append(out, map[string]any{
			"request":        p.event.RequestRaw,
			"requestPattern": echoed,
			"matchResult":    matchResult(p.miss),
		})
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"nearMisses": out})
}

// unmatchedNearMisses scores every unmatched journal entry against the stubs.
//
// The answer is one flat list of request-and-stub pairings rather than a list
// grouped by request, which is the shape WireMock's clients deserialize.
func (h *Handler) unmatchedNearMisses(w http.ResponseWriter, r *http.Request) {
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

	snap := h.engine.Snapshot()
	out := make([]map[string]any, 0)
	for i := range events {
		e := &events[i]
		if e.Matched {
			continue
		}
		for _, miss := range scoreAgainstSnapshot(snap, recordedRequestDesc(e)) {
			out = append(out, map[string]any{
				"request":     e.RequestRaw,
				"stubMapping": stubDocument(snap, miss.StubID),
				"matchResult": matchResult(miss),
			})
		}
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"nearMisses": out})
}

// patternMiss is one recorded request measured against the supplied pattern.
type patternMiss struct {
	event *serveEvent
	miss  wmcompat.NearMiss
}

// rankPatternMisses orders recorded requests closest-first.
//
// Ties break on the entry id, which is time-ordered, so the same journal and
// the same pattern produce the same answer every time — a diagnostic that
// reorders between calls is one nobody can paste into a bug report.
func rankPatternMisses(all []patternMiss) {
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].miss.Distance != all[j].miss.Distance {
			return all[i].miss.Distance < all[j].miss.Distance
		}
		return all[i].event.ID < all[j].event.ID
	})
}

// matchResult renders one score in WireMock's `matchResult` envelope.
//
// The per-criterion `differences` are ours. WireMock reports its own diff as
// rendered text on the serve event, and a caller who has to parse prose to
// learn which header was wrong is being told less than the scorer already knew.
func matchResult(miss wmcompat.NearMiss) map[string]any {
	return map[string]any{
		"distance":    miss.Distance,
		"differences": miss.Differences,
	}
}

// stubDocument returns the mapping a near miss names, verbatim.
//
// The whole mapping rather than its id, because a WireMock client deserializes
// this field into a StubMapping and prints it: an id alone would leave the
// diagnostic saying only that *something* was close.
func stubDocument(snap *match.Snapshot, id string) json.RawMessage {
	if cs, ok := snap.ByID(id); ok {
		return cs.Raw
	}
	// Reaching here needs the stub to have been deleted between scoring and
	// this lookup. The id still identifies what was measured.
	return json.RawMessage(`{"id":` + quoteJSON(id) + `}`)
}

// describedRequestDoc echoes a supplied request back in the logged-request
// shape, which is the field WireMock fills in on every near miss so a caller
// can see what the server understood their description to be.
func describedRequestDoc(desc nearMissRequest) json.RawMessage {
	url := defaultURL(desc.URL)
	raw, err := json.Marshal(map[string]any{
		"method":      defaultMethod(desc.Method),
		"url":         url,
		"headers":     orEmpty(desc.Headers),
		"cookies":     orEmpty(desc.Cookies),
		"body":        desc.Body,
		"queryParams": queryParamsOf(url),
	})
	if err != nil {
		// Marshalling a map of strings cannot fail; an empty document is still
		// a near miss rather than a 500 on a diagnostic call.
		return json.RawMessage(`{}`)
	}
	return raw
}

// recordedRequestDesc projects a journal entry down to the fields scoring reads.
func recordedRequestDesc(e *serveEvent) nearMissRequest {
	return nearMissRequest{
		Method:  e.Request.Method,
		URL:     e.Request.URL,
		Headers: flattenRecorded(e.Request.Headers),
		Cookies: flattenRecorded(e.Request.Cookies),
		Body:    e.Request.Body,
	}
}

// flattenRecorded takes the first value of each recorded multi-value entry.
//
// Scoring is a diagnostic: the distance to the first value of a repeated header
// is more useful than no distance at all, and matching itself — any-of over
// every value (§5.2) — happens elsewhere and is unaffected.
func flattenRecorded(source map[string]any) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for name, raw := range source {
		switch v := raw.(type) {
		case string:
			out[name] = v
		case []any:
			if len(v) > 0 {
				if s, ok := v[0].(string); ok {
					out[name] = s
				}
			}
		}
	}
	return out
}

// scoreAgainstSnapshot ranks the snapshot's stubs against a described request.
func scoreAgainstSnapshot(snap *match.Snapshot, desc nearMissRequest) []wmcompat.NearMiss {
	pr, release := parseDescribed(desc)
	defer release()
	return snap.NearMisses(pr, wmcompat.NearMissCount)
}

// scoreOne measures a single compiled pattern against a described request.
func scoreOne(cs *stub.CompiledStub, desc nearMissRequest) wmcompat.NearMiss {
	pr, release := parseDescribed(desc)
	defer release()
	return match.ScoreRequest(cs, pr)
}

// parseDescribed builds the engine's request from a description.
//
// A synthetic http.Request is constructed rather than a parallel parsing path,
// so the near-miss answer is computed by exactly the machinery that decides a
// real match — a diagnostic that disagreed with the matcher would be worse than
// none.
func parseDescribed(desc nearMissRequest) (*match.ParsedRequest, func()) {
	req := httptest.NewRequest(defaultMethod(desc.Method), defaultURL(desc.URL),
		strings.NewReader(desc.Body))
	for name, value := range desc.Headers {
		req.Header.Set(name, value)
	}
	for name, value := range desc.Cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	pr := match.AcquireRequest(req, []byte(desc.Body))
	return pr, func() { match.ReleaseRequest(pr) }
}

func defaultMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

func defaultURL(url string) string {
	if url == "" {
		return "/"
	}
	return url
}

// orEmpty keeps an absent map out of the echoed document as `{}` rather than
// `null`, which is what a client deserializing into a map expects to find.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// queryParamsOf splits the described URL's query the way a recorded entry
// carries it — one `key`/`values` object per parameter — so a client can
// deserialize the echoed request with the same type it deserializes a journal
// entry with.
func queryParamsOf(url string) map[string]any {
	out := map[string]any{}
	i := strings.IndexByte(url, '?')
	if i < 0 {
		return out
	}
	for _, pair := range strings.Split(url[i+1:], "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		entry, repeated := out[name].(map[string]any)
		if !repeated {
			out[name] = map[string]any{"key": name, "values": []string{value}}
			continue
		}
		entry["values"] = append(entry["values"].([]string), value)
	}
	return out
}

// quoteJSON renders one string as a JSON literal, which cannot fail.
func quoteJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
