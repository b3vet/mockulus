// SPDX-License-Identifier: Apache-2.0

package match

import (
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/response"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// UnmatchedBody is the plain-text body returned when no stub matched. Its exact
// wording is pending differential verification (SPEC §5.4); the corpus pins it
// the moment topology T5 resolves it.
const UnmatchedBody = "Request was not matched\n"

// Engine serves mock traffic from the current snapshot. It holds that snapshot
// in an atomic pointer, so a request reads it with one load and no lock, and a
// rebuild swaps a wholly new snapshot in behind readers still using the old one
// (SPEC §6.2).
type Engine struct {
	snapshot atomic.Pointer[Snapshot]
	metrics  *metrics.Metrics
	cfg      config.Config

	// gate is consulted for stubs in a scenario. It is nil until the scenario
	// client is wired in, and nil means scenario-gated stubs match on their
	// other criteria alone.
	gate atomic.Pointer[ScenarioGate]
}

// NewEngine returns an engine serving an empty snapshot.
func NewEngine(cfg config.Config, m *metrics.Metrics) *Engine {
	e := &Engine{metrics: m, cfg: cfg}
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

// SetScenarioGate installs the scenario state check.
func (e *Engine) SetScenarioGate(gate ScenarioGate) { e.gate.Store(&gate) }

// ServeHTTP is the mock-port handler: read the body, one atomic load, one
// match, one write.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	body, ok := e.readBody(w, r)
	if !ok {
		e.observe(false, http.StatusRequestEntityTooLarge, 0, time.Since(start))
		return
	}

	pr := AcquireRequest(r, body)
	defer ReleaseRequest(pr)

	snap := e.snapshot.Load()

	var gate ScenarioGate
	if g := e.gate.Load(); g != nil {
		gate = *g
	}

	candidates := 0
	cs := snap.Match(pr, gate, &candidates)

	status := http.StatusNotFound
	if cs != nil {
		status = response.Write(w, r, &cs.Response, e.cfg.WriteSlack.D())
	} else {
		writeUnmatched(w)
	}

	e.observe(cs != nil, status, candidates, time.Since(start))
}

// readBody reads the request body under the configured cap. Matching needs the
// whole body anyway, so it is read once here rather than streamed.
func (e *Engine) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	cap := e.cfg.MaxBodyBytes.B()
	if cap <= 0 {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, true
		}
		return body, true
	}

	// Read one byte past the cap so exceeding it is detectable.
	body, err := io.ReadAll(io.LimitReader(r.Body, cap+1))
	if err != nil {
		return nil, true
	}
	if int64(len(body)) > cap {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeBodyTooLarge,
			"request body exceeds max_body_bytes"))
		return nil, false
	}
	return body, true
}

func writeUnmatched(w http.ResponseWriter) {
	// The charset spelling has no space after the semicolon, matching the
	// pinned WireMock byte for byte.
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
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
