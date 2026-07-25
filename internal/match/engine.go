// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/response"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/template"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// The unmatched-request bodies mirror WireMock's shape and Content-Type, with
// one deliberate change: mockulus names itself rather than claiming to be
// WireMock in its own diagnostics. Diagnostic text sits outside the strict
// compatibility surface (SPEC §5.4, §6.8, deviation #18).
const (
	// UnmatchedNoStubsBody is returned when the instance holds no stubs at all,
	// which is nearly always a misconfiguration worth saying out loud.
	UnmatchedNoStubsBody = "No response could be served as there are no stub mappings in this mockulus instance."
	// UnmatchedBody is returned when stubs exist but none matched. WireMock
	// renders a near-miss diff here; mockulus does not compute near misses on
	// the request path unless diagnostics_on_unmatched is set (deviation #2).
	UnmatchedBody = "Request was not matched"
)

// Engine serves mock traffic from the current snapshot. It holds that snapshot
// in an atomic pointer, so a request reads it with one load and no lock, and a
// rebuild swaps a wholly new snapshot in behind readers still using the old one
// (SPEC §6.2).
type Engine struct {
	snapshot atomic.Pointer[Snapshot]
	metrics  *metrics.Metrics
	cfg      config.Config
	log      *slog.Logger
	// served counts requests for access-log sampling. Access logging is off by
	// default because a log line per request is real work on the hot path.
	served atomic.Uint64
	// renderer is nil when templating is off; a stub with no templates never
	// consults it either way.
	renderer response.Renderer

	// gate is consulted for stubs in a scenario. It is nil until the scenario
	// client is wired in, and nil means scenario-gated stubs match on their
	// other criteria alone.
	gate atomic.Pointer[ScenarioGate]
	// transitioner advances a scenario after a stub with newScenarioState served.
	transitioner atomic.Pointer[Transitioner]
	// recorder is nil unless journaling is on.
	recorder atomic.Pointer[Recorder]
}

// Recorder records a served request. It must never block: the request has
// already been answered by the time it is called, and the journal's whole
// design is that recording cannot slow serving down (SPEC §11.1).
type Recorder interface {
	Record(r *http.Request, body []byte, matched *stub.CompiledStub, status int)
}

// NewEngine returns an engine serving an empty snapshot.
func NewEngine(cfg config.Config, m *metrics.Metrics, log *slog.Logger, renderer response.Renderer) *Engine {
	e := &Engine{metrics: m, cfg: cfg, log: log, renderer: renderer}
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

// Transitioner advances a scenario after a stub has served.
type Transitioner interface {
	Transition(ctx context.Context, name, requiredState, newState string) error
}

// SetTransitioner installs the scenario transition path.
func (e *Engine) SetTransitioner(t Transitioner) { e.transitioner.Store(&t) }

// SetRecorder installs the journal.
func (e *Engine) SetRecorder(rec Recorder) { e.recorder.Store(&rec) }

func (e *Engine) transition(ctx context.Context, ref *stub.ScenarioRef) {
	t := e.transitioner.Load()
	if t == nil {
		return
	}
	if err := (*t).Transition(ctx, ref.Name, ref.RequiredState, ref.NewState); err != nil {
		e.metrics.StoreErrors.WithLabelValues("scenario_transition").Inc()
	}
}

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

	if cs != nil && cs.Scenario != nil && cs.Scenario.NewState != "" {
		// The transition follows the match and precedes the response, so a test
		// that serves then immediately re-requests sees the new state. It never
		// fails the response: what was matched is served either way.
		e.transition(r.Context(), cs.Scenario)
	}

	status := http.StatusNotFound
	if cs != nil {
		opts := response.Options{WriteSlack: e.cfg.WriteSlack.D(), Settings: snap.Settings}
		if cs.Response.Templated && e.renderer != nil {
			// The context is built only for a stub that actually templates, so
			// an ordinary stub never pays for assembling the request model.
			opts.Renderer = e.renderer
			opts.Context = template.BuildContext(r, body, pr.PathVars(),
				cs.Response.TransformerParameters)
			opts.OnRenderError = e.metrics.TemplateRenderErrors.Inc
		}
		status = response.Write(w, r, &cs.Response, opts)
	} else {
		body := UnmatchedBody
		switch {
		case snap.Len() == 0:
			body = UnmatchedNoStubsBody
		case e.cfg.DiagnosticsOnUnmatched:
			// Off by default: WireMock computes near misses on every unmatched
			// request and mockulus deliberately does not (deviation #2).
			body = DiagnosticBody(snap, pr)
		}
		writeUnmatched(w, body)
	}

	if rec := e.recorder.Load(); rec != nil {
		// After the response, never before: recording is bookkeeping and must
		// not sit between the match and the write.
		(*rec).Record(r, body, cs, status)
	}

	took := time.Since(start)
	e.accessLog(r, cs, status, took)
	e.observe(cs != nil, status, candidates, took)
}

// accessLog writes a per-request line when log.requests is on.
//
// Sampled rather than complete: at 50k RPS a line per request is more work than
// serving the request, so log.request_sample_n keeps the feature usable under
// load instead of making it a switch nobody dares flip. Bodies and headers are
// never logged — teams put real credentials in their mocks (SPEC §14.2).
func (e *Engine) accessLog(r *http.Request, cs *stub.CompiledStub, status int, took time.Duration) {
	if !e.cfg.Log.Requests {
		return
	}
	n := e.served.Add(1)
	if sample := uint64(e.cfg.Log.RequestSampleN); sample > 1 && n%sample != 0 {
		return
	}

	stubID := ""
	if cs != nil {
		stubID = cs.ID
	}
	e.log.Info("request served",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"matched", cs != nil,
		"stub", stubID,
		"took_us", took.Microseconds(),
		"sampled_of", e.cfg.Log.RequestSampleN)
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

func writeUnmatched(w http.ResponseWriter, body string) {
	// The charset spelling has no space after the semicolon, matching the
	// pinned WireMock byte for byte.
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(body))
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
