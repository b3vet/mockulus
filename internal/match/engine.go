// SPDX-License-Identifier: Apache-2.0

package match

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/response"
)

// UnmatchedBody is the plain-text body WireMock returns when no stub matched.
// Its exact wording is pending differential verification (SPEC §5.4 [DH]); the
// corpus pins it the moment topology T5 resolves it.
const UnmatchedBody = "Request was not matched\n"

// Engine serves mock traffic from the current snapshot. It holds that snapshot
// in an atomic pointer, so a request reads it with one load and no lock, and a
// rebuild swaps a wholly new snapshot in behind readers still using the old one
// (SPEC §6.2).
type Engine struct {
	snapshot atomic.Pointer[Snapshot]
	metrics  *metrics.Metrics
}

// NewEngine returns an engine serving an empty snapshot.
func NewEngine(m *metrics.Metrics) *Engine {
	e := &Engine{metrics: m}
	e.snapshot.Store(EmptySnapshot())
	return e
}

// Snapshot returns the snapshot currently being served.
func (e *Engine) Snapshot() *Snapshot { return e.snapshot.Load() }

// Swap installs a new snapshot. Requests already in flight keep serving from
// the snapshot they loaded; the garbage collector reclaims it once they finish,
// which supplies the RCU grace period for free.
func (e *Engine) Swap(s *Snapshot) {
	e.snapshot.Store(s)
	e.metrics.SnapshotStubs.Set(float64(s.Len()))
	e.metrics.SnapshotEpoch.Set(float64(s.Epoch))
}

// ServeHTTP is the mock-port handler: one atomic load, one match, one write.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	snap := e.snapshot.Load()

	path, full := SplitURL(r.URL.RequestURI())
	candidates := 0
	cs := snap.Match(r.Method, path, full, &candidates)

	status := http.StatusNotFound
	if cs != nil {
		status = cs.Response.Status
		response.Write(w, &cs.Response)
	} else {
		writeUnmatched(w)
	}

	e.observe(cs != nil, status, candidates, time.Since(start))
}

func writeUnmatched(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(UnmatchedBody))
}

// observe records the per-request metrics of SPEC §14.1. Label values come from
// a lookup table so the hot path never formats a string.
func (e *Engine) observe(matched bool, status, candidates int, took time.Duration) {
	e.metrics.HTTPRequests.WithLabelValues(matchedLabel(matched), statusLabel(status)).Inc()
	e.metrics.HTTPRequestDuration.WithLabelValues(matchedLabel(matched)).Observe(took.Seconds())
	e.metrics.MatchCandidates.Observe(float64(candidates))
}

func matchedLabel(matched bool) string {
	if matched {
		return "true"
	}
	return "false"
}

// statusStrings caches the decimal form of every valid HTTP status code, so
// metric labelling allocates nothing (SPEC §16.3 rule 4).
var statusStrings = func() [600]string {
	var out [600]string
	for i := 100; i < 600; i++ {
		out[i] = strconv.Itoa(i)
	}
	return out
}()

func statusLabel(status int) string {
	if status >= 100 && status < 600 {
		return statusStrings[status]
	}
	return "unknown"
}

// Interface check: the engine is the mock listener's handler.
var _ http.Handler = (*Engine)(nil)
