// SPDX-License-Identifier: Apache-2.0

package match

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/handlebars"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/stub"
)

// testEngine builds an engine serving the given stubs and hands back the buffer
// its logger writes to, so a test can assert on what was — and was not — logged.
func testEngine(t *testing.T, cfg config.Config, stubs ...*stub.CompiledStub) (*Engine, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewEngine(cfg, metrics.New("test", "test", false), log, nil)
	e.Swap(BuildSnapshot(stubs, 1))
	return e, logs
}

func serve(e *Engine, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func get(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
}

// An instance holding nothing at all is nearly always a misconfiguration, and
// the 404 says which of the two situations the reader is in rather than leaving
// them to guess whether their stub is wrong or absent (SPEC §5.4).
func TestAnUnmatchedRequestSaysWhetherTheInstanceIsEmpty(t *testing.T) {
	only := mustCompile(t, 1, "only",
		`{"request":{"method":"GET","urlPath":"/known"},"response":{"status":200,"body":"served"}}`)

	empty, _ := testEngine(t, config.Config{})
	populated, _ := testEngine(t, config.Config{}, only)

	for _, tc := range []struct {
		name   string
		engine *Engine
		want   string
	}{
		{"no stubs registered at all", empty, UnmatchedNoStubsBody},
		{"stubs registered but none matched", populated, UnmatchedBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(tc.engine, get(t, "/unknown"))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			// The charset spelling carries no space after the semicolon, which
			// is the pinned WireMock byte and therefore compat surface.
			if got := rec.Header().Get("Content-Type"); got != "text/plain;charset=UTF-8" {
				t.Errorf("content-type = %q, want text/plain;charset=UTF-8", got)
			}
		})
	}
}

// Near misses cost a walk of every stub, so they stay behind the flag. The test
// is both halves at once: turning it on has to change the body, and leaving it
// off has to leave the body exactly as WireMock's unmatched text (deviation #2).
func TestNearMissDetailAppearsInA404OnlyWhenDiagnosticsAreOn(t *testing.T) {
	near := mustCompile(t, 1, "orders",
		`{"request":{"method":"GET","urlPath":"/api/orders"},"response":{"status":200}}`)

	off, _ := testEngine(t, config.Config{}, near)
	if got := serve(off, get(t, "/api/order")).Body.String(); got != UnmatchedBody {
		t.Errorf("with diagnostics off the body was %q, want the bare %q", got, UnmatchedBody)
	}

	on, _ := testEngine(t, config.Config{DiagnosticsOnUnmatched: true}, near)
	body := serve(on, get(t, "/api/order")).Body.String()
	if !strings.HasPrefix(body, UnmatchedBody) {
		t.Fatalf("the diagnostic body dropped the unmatched message: %q", body)
	}
	for _, want := range []string{"orders", "expected /api/orders", "got /api/order"} {
		if !strings.Contains(body, want) {
			t.Errorf("diagnostic body does not mention %q:\n%s", want, body)
		}
	}
}

// max_body_bytes is a cap, and a cap that is off by one either refuses a legal
// request or admits the one request it exists to refuse. Both spellings of the
// length are exercised because they take different branches: a declared length
// within the cap sizes the buffer exactly, and everything else goes through the
// limited read that detects the overrun.
func TestTheBodyCapRefusesOnlyWhatIsOverIt(t *testing.T) {
	const cap8 = 8
	echo := mustCompile(t, 1, "echo",
		`{"request":{"method":"POST","urlPath":"/echo"},"response":{"status":200,"body":"served"}}`)

	for _, tc := range []struct {
		name     string
		body     string
		declared bool
		want     int
	}{
		{"a declared length exactly at the cap", strings.Repeat("a", cap8), true, http.StatusOK},
		{"a declared length one byte over the cap", strings.Repeat("a", cap8+1), true, http.StatusRequestEntityTooLarge},
		{"an undeclared length exactly at the cap", strings.Repeat("a", cap8), false, http.StatusOK},
		{"an undeclared length one byte over the cap", strings.Repeat("a", cap8+1), false, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine(t, config.Config{MaxBodyBytes: cap8}, echo)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/echo",
				strings.NewReader(tc.body))
			if !tc.declared {
				// What a chunked request looks like to net/http: a body of
				// unknown length, which only the limited read can bound.
				req.ContentLength = -1
			}

			rec := serve(e, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusRequestEntityTooLarge {
				if got := rec.Body.String(); !strings.Contains(got, `"code":1030`) {
					t.Errorf("the refusal did not carry catalog code 1030: %s", got)
				}
				// The refusal has to come before matching, not after: a body
				// over the cap must never reach a stub.
				if strings.Contains(rec.Body.String(), "served") {
					t.Error("a body over the cap was matched and served anyway")
				}
			} else if rec.Body.String() != "served" {
				t.Errorf("body = %q, want served", rec.Body.String())
			}
		})
	}
}

// A cap of zero means unbounded, which is a distinct branch rather than a cap
// of zero bytes — reading it the other way would refuse every request with a
// body on a deployment that deliberately turned the cap off (SPEC §13).
func TestACapOfZeroReadsTheWholeBody(t *testing.T) {
	e, _ := testEngine(t, config.Config{MaxBodyBytes: 0}, mustCompile(t, 1, "big",
		`{"request":{"method":"POST","urlPath":"/big","bodyPatterns":[{"contains":"needle"}]},
		  "response":{"status":200,"body":"served"}}`))

	body := strings.Repeat("x", 1<<16) + "needle"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/big",
		strings.NewReader(body))

	rec := serve(e, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "served" {
		t.Fatalf("status = %d body = %q, want 200 served", rec.Code, rec.Body.String())
	}
}

// A bodyless request reads nothing at all, and the saving is only correct if a
// stub that wants a body still fails to match it.
func TestABodylessRequestIsMatchedAsCarryingNoBody(t *testing.T) {
	wantsBody := mustCompile(t, 2, "wants-body",
		`{"request":{"urlPath":"/b","bodyPatterns":[{"contains":"x"}]},
		  "response":{"status":200,"body":"wants-body"}}`)
	plain := mustCompile(t, 1, "plain",
		`{"request":{"urlPath":"/b"},"response":{"status":200,"body":"plain"}}`)

	e, _ := testEngine(t, config.Config{}, wantsBody, plain)
	rec := serve(e, get(t, "/b"))

	if rec.Body.String() != "plain" {
		t.Errorf("body = %q, want plain — a bodyless request satisfied a body criterion",
			rec.Body.String())
	}
}

// failingBody is a request body that cannot be read, which is what a client
// disconnecting mid-upload looks like from inside the handler.
type failingBody struct{ err error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b failingBody) Close() error             { return nil }

// A body that cannot be read is matched as though it were absent rather than
// aborting the request. The connection carrying it is already broken, so the
// only question left is whether the engine stays on its feet: turning this into
// a panic or a 500 would make one dead client look like a server fault.
func TestABodyThatCannotBeReadIsMatchedAsAbsent(t *testing.T) {
	wantsBody := mustCompile(t, 2, "wants-body",
		`{"request":{"urlPath":"/t","bodyPatterns":[{"contains":"x"}]},
		  "response":{"status":200,"body":"wants-body"}}`)
	plain := mustCompile(t, 1, "plain",
		`{"request":{"urlPath":"/t"},"response":{"status":200,"body":"plain"}}`)

	for _, tc := range []struct {
		name  string
		limit config.Bytes
	}{
		{"under a cap, where the declared length sizes the read", 1 << 20},
		{"with the cap off, where the whole body is read", 0},
		{"under a cap the declared length exceeds, where the read is limited", 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine(t, config.Config{MaxBodyBytes: tc.limit}, wantsBody, plain)
			req := get(t, "/t")
			req.Body = failingBody{err: errors.New("connection reset")}
			req.ContentLength = 64

			rec := serve(e, req)
			if rec.Code != http.StatusOK || rec.Body.String() != "plain" {
				t.Fatalf("status = %d body = %q, want 200 plain", rec.Code, rec.Body.String())
			}
		})
	}
}

// The whole point of carrying a scenario read failure out of matching: which
// side of the state machine the flow is on is unknown, so answering from a
// lower-precedence stub would put a passing test on the wrong branch of its own
// flow and say nothing about it (SPEC §9.2).
func TestAnUnreadableScenarioStateIsReportedRatherThanFallenThrough(t *testing.T) {
	gated := mustCompile(t, 2, "gated",
		`{"scenarioName":"flow","requiredScenarioState":"Started",
		  "request":{"urlPath":"/g"},"response":{"status":200,"body":"gated"}}`)
	plain := mustCompile(t, 1, "plain",
		`{"request":{"urlPath":"/g"},"response":{"status":200,"body":"plain"}}`)

	// The contrast that gives the test its meaning: a gate that merely refuses
	// falls through to the next candidate, and only a gate that could not read
	// at all stops the request.
	refusing, _ := testEngine(t, config.Config{}, gated, plain)
	refusing.SetScenarioGate(func(*stub.ScenarioRef, *ParsedRequest) bool { return false })
	if rec := serve(refusing, get(t, "/g")); rec.Body.String() != "plain" {
		t.Fatalf("a refused gate served %q, want the next candidate plain", rec.Body.String())
	}

	unreadable, _ := testEngine(t, config.Config{}, gated, plain)
	unreadable.SetScenarioGate(func(_ *stub.ScenarioRef, req *ParsedRequest) bool {
		req.FailScenarioRead(errors.New("couchbase unreachable"))
		return false
	})

	rec := serve(unreadable, get(t, "/g"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":1021`) {
		t.Errorf("the failure did not carry catalog code 1021: %s", body)
	}
	if !strings.Contains(body, "couchbase unreachable") {
		t.Errorf("the failure did not say what went wrong: %s", body)
	}
	if strings.Contains(body, "plain") {
		t.Error("an unreadable scenario state fell through to a lower-precedence stub")
	}
}

type scenarioTransition struct{ name, required, next string }

// recordingTransitioner stands in for the scenario client, remembering what it
// was asked to advance.
type recordingTransitioner struct {
	mu    sync.Mutex
	calls []scenarioTransition
	fail  error
}

func (r *recordingTransitioner) Transition(_ context.Context, name, required, next string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, scenarioTransition{name, required, next})
	return r.fail
}

func (r *recordingTransitioner) seen() []scenarioTransition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]scenarioTransition(nil), r.calls...)
}

// Only a stub that declares a new state advances anything. A stub that merely
// belongs to a scenario must not move it, or every read of a flow would push
// that flow forward.
func TestOnlyAStubDeclaringANewStateAdvancesTheScenario(t *testing.T) {
	advancing := mustCompile(t, 2, "advancing",
		`{"scenarioName":"order","requiredScenarioState":"Started","newScenarioState":"created",
		  "request":{"urlPath":"/advance"},"response":{"status":200,"body":"advanced"}}`)
	member := mustCompile(t, 1, "member",
		`{"scenarioName":"order","requiredScenarioState":"Started",
		  "request":{"urlPath":"/read"},"response":{"status":200,"body":"read"}}`)

	e, _ := testEngine(t, config.Config{}, advancing, member)
	tr := &recordingTransitioner{}
	e.SetTransitioner(tr)
	e.SetScenarioGate(func(*stub.ScenarioRef, *ParsedRequest) bool { return true })

	if rec := serve(e, get(t, "/read")); rec.Body.String() != "read" {
		t.Fatalf("body = %q, want read", rec.Body.String())
	}
	if calls := tr.seen(); len(calls) != 0 {
		t.Fatalf("a stub with no newScenarioState advanced the scenario: %v", calls)
	}

	if rec := serve(e, get(t, "/advance")); rec.Body.String() != "advanced" {
		t.Fatalf("body = %q, want advanced", rec.Body.String())
	}
	calls := tr.seen()
	if len(calls) != 1 {
		t.Fatalf("transitions = %v, want exactly one", calls)
	}
	want := scenarioTransition{"order", "Started", "created"}
	if calls[0] != want {
		t.Errorf("transition = %+v, want %+v", calls[0], want)
	}
}

// A transition that fails must not fail the response. What matched is served
// either way: the request was already answerable, and reporting a bookkeeping
// error as a serving error would break flows that were working.
func TestAFailedTransitionStillServesTheMatchedResponse(t *testing.T) {
	e, _ := testEngine(t, config.Config{}, mustCompile(t, 1, "advancing",
		`{"scenarioName":"order","requiredScenarioState":"Started","newScenarioState":"created",
		  "request":{"urlPath":"/advance"},"response":{"status":201,"body":"advanced"}}`))
	e.SetTransitioner(&recordingTransitioner{fail: errors.New("cas conflict exhausted")})
	e.SetScenarioGate(func(*stub.ScenarioRef, *ParsedRequest) bool { return true })

	rec := serve(e, get(t, "/advance"))
	if rec.Code != http.StatusCreated || rec.Body.String() != "advanced" {
		t.Fatalf("status = %d body = %q, want 201 advanced", rec.Code, rec.Body.String())
	}
}

type recordedRequest struct {
	method, path, body, stubID string
	status                     int
}

type recordingRecorder struct {
	mu      sync.Mutex
	entries []recordedRequest
}

func (r *recordingRecorder) Record(req *http.Request, body []byte, matched *stub.CompiledStub, status int) {
	id := ""
	if matched != nil {
		id = matched.ID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, recordedRequest{
		method: req.Method, path: req.URL.Path, body: string(body), stubID: id, status: status,
	})
}

func (r *recordingRecorder) seen() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.entries...)
}

// The journal has to see misses as well as hits: "no stub matched this request"
// is the single most useful thing a failing test can be told, and a recorder
// wired only to matched requests could never say it (SPEC §11.1).
func TestTheJournalRecordsUnmatchedRequestsToo(t *testing.T) {
	e, _ := testEngine(t, config.Config{}, mustCompile(t, 1, "hit",
		`{"request":{"method":"POST","urlPath":"/hit"},"response":{"status":202,"body":"ok"}}`))
	rec := &recordingRecorder{}
	e.SetRecorder(rec)

	serve(e, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/hit",
		strings.NewReader(`{"a":1}`)))
	serve(e, get(t, "/miss"))

	entries := rec.seen()
	if len(entries) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(entries))
	}
	want := []recordedRequest{
		{method: http.MethodPost, path: "/hit", body: `{"a":1}`, stubID: "hit", status: 202},
		{method: http.MethodGet, path: "/miss", body: "", stubID: "", status: http.StatusNotFound},
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

// Access logging is off by default and sampled when on, because a line per
// request is more work than serving the request. A sampler that is off by one
// either logs everything under load or logs nothing at all (SPEC §14.2).
func TestAccessLoggingIsOffByDefaultAndSampledWhenOn(t *testing.T) {
	only := mustCompile(t, 1, "only",
		`{"request":{"method":"GET","urlPath":"/x"},"response":{"status":200}}`)

	for _, tc := range []struct {
		name     string
		log      config.LogConfig
		requests int
		want     int
	}{
		{"off by default", config.LogConfig{}, 5, 0},
		{"a sample of one logs every request", config.LogConfig{Requests: true, RequestSampleN: 1}, 5, 5},
		{"a sample of three logs every third", config.LogConfig{Requests: true, RequestSampleN: 3}, 9, 3},
		{"a sample larger than the traffic logs nothing yet", config.LogConfig{Requests: true, RequestSampleN: 100}, 9, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, logs := testEngine(t, config.Config{Log: tc.log}, only)
			for range tc.requests {
				serve(e, get(t, "/x"))
			}
			if got := strings.Count(logs.String(), "request served"); got != tc.want {
				t.Errorf("logged %d lines for %d requests, want %d", got, tc.requests, tc.want)
			}
		})
	}
}

// Teams put real credentials in their mocks, so the access log carries the
// request line and nothing that travelled inside it (SPEC §14.2).
func TestTheAccessLogNeverCarriesHeadersOrBodies(t *testing.T) {
	e, logs := testEngine(t, config.Config{Log: config.LogConfig{Requests: true, RequestSampleN: 1}},
		mustCompile(t, 1, "secretive",
			`{"request":{"method":"POST","urlPath":"/login"},"response":{"status":200}}`))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login",
		strings.NewReader(`{"password":"hunter2"}`))
	req.Header.Set("Authorization", "Bearer s3cr3t")
	serve(e, req)

	line := logs.String()
	if !strings.Contains(line, "request served") {
		t.Fatalf("nothing was logged: %s", line)
	}
	for _, secret := range []string{"hunter2", "s3cr3t", "password", "Bearer"} {
		if strings.Contains(line, secret) {
			t.Errorf("the access log leaked %q:\n%s", secret, line)
		}
	}
	if !strings.Contains(line, "/login") || !strings.Contains(line, "secretive") {
		t.Errorf("the access log dropped the request line or the stub id:\n%s", line)
	}
}

// The status label is a table lookup indexed by the status itself, so its
// bounds are exactly where a wrong label — or a panic — would come from. A
// response with no status at all is not hypothetical: a fault injection
// hijacks the connection and reports zero.
func TestStatusLabelsCoverValidCodesAndRefuseTheRest(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{-1, "unknown"},
		{0, "unknown"},
		{99, "unknown"},
		{100, "100"},
		{200, "200"},
		{404, "404"},
		{599, "599"},
		{600, "unknown"},
		{100000, "unknown"},
	} {
		if got := statusLabel(tc.status); got != tc.want {
			t.Errorf("statusLabel(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestMatchedLabelsAreTheStringsThePrometheusSeriesUses(t *testing.T) {
	if got := matchedLabel(true); got != "true" {
		t.Errorf("matchedLabel(true) = %q", got)
	}
	if got := matchedLabel(false); got != "false" {
		t.Errorf("matchedLabel(false) = %q", got)
	}
}

// The swap is the whole of the RCU discipline: a reader holding the previous
// snapshot keeps serving from it, and the pointer it loaded is never mutated
// under it (SPEC §6.2).
func TestASwapLeavesReadersOnTheSnapshotTheyAlreadyLoaded(t *testing.T) {
	first := BuildSnapshot([]*stub.CompiledStub{mustCompile(t, 1, "first",
		`{"request":{"urlPath":"/x"},"response":{"status":200,"body":"first"}}`)}, 7)
	e := NewEngine(config.Config{}, metrics.New("test", "test", false),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	e.Swap(first)

	held := e.Snapshot()

	e.Swap(BuildSnapshot([]*stub.CompiledStub{mustCompile(t, 2, "second",
		`{"request":{"urlPath":"/x"},"response":{"status":200,"body":"second"}}`)}, 8))

	if id := match(t, held, http.MethodGet, "/x", "", nil); id != "first" {
		t.Errorf("the held snapshot now serves %q, want first", id)
	}
	if held.Epoch != 7 {
		t.Errorf("the held snapshot's epoch moved to %d, want 7", held.Epoch)
	}
	if got := serve(e, get(t, "/x")).Body.String(); got != "second" {
		t.Errorf("the engine serves %q after the swap, want second", got)
	}
}

// recordingRenderer stands in for the handlebars renderer so a test can see
// exactly what context the engine assembled, and whether it assembled one at
// all.
type recordingRenderer struct {
	mu    sync.Mutex
	calls int
	ctx   map[string]any
}

func (r *recordingRenderer) Render(_ *handlebars.Template, ctx map[string]any) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.ctx = ctx
	return "rendered", nil
}

func (r *recordingRenderer) seen() (int, map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.ctx
}

// Assembling the request model is real work — headers, cookies, query, body,
// base64 — and a stub that does not template must never pay for it. This is the
// pay-per-use rule of P2 asserted on the one path where breaking it would be
// invisible: a request model built and then thrown away serves the same bytes.
func TestTheTemplateContextIsBuiltOnlyForAStubThatTemplates(t *testing.T) {
	// Templating is compiled in, which is what `templating_enabled` switches on;
	// parsing alone is enough here, because the renderer is the thing under test.
	opts := testStubOptions()
	opts.CompileTemplate = handlebars.Parse

	templated := mustCompileWith(t, opts, 2, "templated",
		`{"request":{"method":"POST","urlPath":"/t"},
		  "response":{"status":200,"body":"hello {{request.method}}",
		    "transformers":["response-template"],
		    "transformerParameters":{"greeting":"hi"}}}`)
	plain := mustCompile(t, 1, "plain",
		`{"request":{"method":"POST","urlPath":"/p"},"response":{"status":200,"body":"plain"}}`)

	renderer := &recordingRenderer{}
	logs := &bytes.Buffer{}
	e := NewEngine(config.Config{}, metrics.New("test", "test", false),
		slog.New(slog.NewTextHandler(logs, nil)), renderer)
	e.Swap(BuildSnapshot([]*stub.CompiledStub{templated, plain}, 1))

	if rec := serve(e, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/p",
		strings.NewReader("body"))); rec.Body.String() != "plain" {
		t.Fatalf("body = %q, want plain", rec.Body.String())
	}
	if calls, _ := renderer.seen(); calls != 0 {
		t.Fatalf("an untemplated stub called the renderer %d times", calls)
	}

	rec := serve(e, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/t",
		strings.NewReader("payload")))
	if rec.Body.String() != "rendered" {
		t.Fatalf("body = %q, want rendered", rec.Body.String())
	}

	calls, ctx := renderer.seen()
	if calls != 1 {
		t.Fatalf("the renderer was called %d times for one templated request", calls)
	}
	request, ok := ctx["request"].(map[string]any)
	if !ok {
		t.Fatalf("the context carries no request model: %v", ctx)
	}
	if request["method"] != http.MethodPost {
		t.Errorf("request.method = %v, want POST", request["method"])
	}
	if request["body"] != "payload" {
		t.Errorf("request.body = %v, want payload — the body the engine already read", request["body"])
	}
	params, ok := ctx["parameters"].(map[string]any)
	if !ok || params["greeting"] != "hi" {
		t.Errorf("parameters = %v, want the stub's transformerParameters", ctx["parameters"])
	}
}

// Scenarios are opt-in infrastructure: an instance running without a scenario
// client must serve a stub that declares a new state rather than panicking on
// the transitioner that was never wired in.
func TestAStubThatAdvancesAScenarioServesWithNoTransitionerWiredIn(t *testing.T) {
	e, _ := testEngine(t, config.Config{}, mustCompile(t, 1, "advancing",
		`{"scenarioName":"order","requiredScenarioState":"Started","newScenarioState":"created",
		  "request":{"urlPath":"/advance"},"response":{"status":200,"body":"advanced"}}`))

	rec := serve(e, get(t, "/advance"))
	if rec.Code != http.StatusOK || rec.Body.String() != "advanced" {
		t.Fatalf("status = %d body = %q, want 200 advanced", rec.Code, rec.Body.String())
	}
}
