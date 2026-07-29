// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// These cases live here rather than in the corpus because their observable is
// not a response. Tracing is a second output channel: what a request produces
// is a span at a collector, and a YAML case has no way to be one. The receiver
// below is that collector — it speaks OTLP/HTTP, so mockulus is exercised
// through the exporter it actually ships rather than through a seam opened for
// the test.

// otlpReceiver is a collector that keeps what it is sent.
type otlpReceiver struct {
	server *httptest.Server

	mu      sync.Mutex
	spans   []*tracepb.Span
	scopes  []*tracepb.ResourceSpans
	headers []http.Header
	// fail makes every export attempt be refused, which is how a case about the
	// failure counter gets a collector that is reachable and still says no.
	fail bool
}

func newOTLPReceiver(t *testing.T, fail bool) *otlpReceiver {
	t.Helper()

	r := &otlpReceiver{fail: fail}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", r.handle)
	r.server = httptest.NewServer(mux)
	t.Cleanup(r.server.Close)
	return r
}

func (r *otlpReceiver) handle(w http.ResponseWriter, req *http.Request) {
	body, err := readExportBody(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var export coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	r.headers = append(r.headers, req.Header.Clone())
	for _, rs := range export.GetResourceSpans() {
		r.scopes = append(r.scopes, rs)
		for _, ss := range rs.GetScopeSpans() {
			r.spans = append(r.spans, ss.GetSpans()...)
		}
	}
	failing := r.fail
	r.mu.Unlock()

	if failing {
		// A rejected credential rather than a 503: it is the outage a
		// misconfigured ingestion token actually produces, and OTLP treats it as
		// final rather than retrying it, so the case observes the counter
		// instead of the retry schedule.
		http.Error(w, "receiver rejects this credential", http.StatusUnauthorized)
		return
	}

	out, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

// readExportBody decodes one export payload, which the exporter may or may not
// have compressed depending on its build defaults.
func readExportBody(req *http.Request) ([]byte, error) {
	var reader io.Reader = req.Body
	if strings.Contains(req.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(req.Body)
		if err != nil {
			return nil, fmt.Errorf("gunzip export: %w", err)
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	return io.ReadAll(reader)
}

// endpoint returns the host:port form the `tracing.endpoint` key takes.
func (r *otlpReceiver) endpoint() string {
	return strings.TrimPrefix(r.server.URL, "http://")
}

func (r *otlpReceiver) collected() []*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tracepb.Span(nil), r.spans...)
}

func (r *otlpReceiver) resources() []*tracepb.ResourceSpans {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tracepb.ResourceSpans(nil), r.scopes...)
}

func (r *otlpReceiver) exportHeaders() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...)
}

// spanNamed returns the first collected span with the given name.
func (r *otlpReceiver) spanNamed(name string) *tracepb.Span {
	for _, s := range r.collected() {
		if s.GetName() == name {
			return s
		}
	}
	return nil
}

// attr reads a span attribute as its string form, so a case can assert on one
// without unwrapping the union by hand at every call site.
func attr(attrs []*commonpb.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if kv.GetKey() != key {
			continue
		}
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			return v.StringValue, true
		case *commonpb.AnyValue_IntValue:
			return strconv.FormatInt(v.IntValue, 10), true
		case *commonpb.AnyValue_BoolValue:
			return strconv.FormatBool(v.BoolValue), true
		default:
			return kv.GetValue().String(), true
		}
	}
	return "", false
}

// tracedInstance boots mockulus exporting to the given receiver.
//
// The drain window is zeroed because these cases stop the process to make it
// flush: shutdown is where the last spans are exported (SPEC §4.5), and waiting
// out a five-second drain in every case would buy nothing.
func tracedInstance(t *testing.T, r *otlpReceiver, extra map[string]string) *mockulus {
	t.Helper()

	env := map[string]string{
		"MOCKULUS_TRACING_ENABLED":      "true",
		"MOCKULUS_TRACING_ENDPOINT":     r.endpoint(),
		"MOCKULUS_TRACING_INSECURE":     "true",
		"MOCKULUS_TRACING_SAMPLE_RATIO": "1",
		"MOCKULUS_SHUTDOWN_DRAIN":       "0s",
	}
	for k, v := range extra {
		env[k] = v
	}
	return start(t, env)
}

// TestTracingExportsMockAndAdminServerSpans is the load-bearing case: with a
// collector configured, a served mock request and an admin call each become one
// server span, carrying the attributes SPEC §14.3 says they carry.
func TestTracingExportsMockAndAdminServerSpans(t *testing.T) {
	receiver := newOTLPReceiver(t, false)
	m := tracedInstance(t, receiver, nil)

	m.registerStub(t, `{"request":{"method":"GET","urlPath":"/gotest/tracing/order"},
	                    "response":{"status":200,"body":"traced"}}`)

	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/order"))
	if err != nil {
		t.Fatalf("serve traced request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("traced request: status %d, want 200", resp.StatusCode)
	}

	// Stopping is what flushes; asserting before it would be asserting on the
	// batch timer rather than on the spans.
	m.stop()

	mock := receiver.spanNamed("mock GET")
	if mock == nil {
		t.Fatalf("no `mock GET` span was exported; got %v", spanNames(receiver))
	}
	if got, _ := attr(mock.GetAttributes(), "url.path"); got != "/gotest/tracing/order" {
		t.Errorf("mock span url.path = %q, want the served path", got)
	}
	if got, _ := attr(mock.GetAttributes(), "http.response.status_code"); got != "200" {
		t.Errorf("mock span http.response.status_code = %q, want 200", got)
	}
	if got, _ := attr(mock.GetAttributes(), "mockulus.matched"); got != "true" {
		t.Errorf("mock span mockulus.matched = %q, want true", got)
	}
	if _, ok := attr(mock.GetAttributes(), "mockulus.stub.id"); !ok {
		t.Error("mock span carries no mockulus.stub.id, so a trace cannot say which stub answered")
	}
	if mock.GetKind() != tracepb.Span_SPAN_KIND_SERVER {
		t.Errorf("mock span kind = %v, want server", mock.GetKind())
	}

	// The admin call that registered the stub is a span of its own, named by
	// endpoint group rather than by path.
	admin := receiver.spanNamed("admin mappings")
	if admin == nil {
		t.Fatalf("no `admin mappings` span was exported; got %v", spanNames(receiver))
	}
	if got, _ := attr(admin.GetAttributes(), "http.response.status_code"); got != "201" {
		t.Errorf("admin span http.response.status_code = %q, want 201", got)
	}
}

// TestTracingInsecureSelectsPlainHTTP pins what the key actually decides.
//
// The collector is plain HTTP. With `MOCKULUS_TRACING_INSECURE` left at its
// default of false the exporter offers TLS to a listener that speaks none, so
// the handshake fails and nothing is ever delivered — which is the same
// observation as a broken deployment, and is exactly why the key exists as an
// explicit choice rather than something inferred from the endpoint. The
// sibling cases run the identical path with the key set and do receive spans;
// the pair is what makes this a statement about the key.
func TestTracingInsecureSelectsPlainHTTP(t *testing.T) {
	receiver := newOTLPReceiver(t, false)

	m := start(t, map[string]string{
		"MOCKULUS_TRACING_ENABLED":      "true",
		"MOCKULUS_TRACING_ENDPOINT":     receiver.endpoint(),
		"MOCKULUS_TRACING_INSECURE":     "false",
		"MOCKULUS_TRACING_SAMPLE_RATIO": "1",
		"MOCKULUS_SHUTDOWN_DRAIN":       "0s",
		// The flush at shutdown would otherwise sit out the exporter's whole
		// retry budget against a listener that cannot answer it.
		"MOCKULUS_SHUTDOWN_TIMEOUT": "2s",
	})

	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/tls"))
	if err != nil {
		t.Fatalf("serve request: %v", err)
	}
	_ = resp.Body.Close()

	m.stop()

	if spans := receiver.collected(); len(spans) != 0 {
		t.Fatalf("a cleartext collector received %d spans while tracing.insecure was false: %v",
			len(spans), spanNames(receiver))
	}
}

// TestTracingDisabledExportsNothing pins the default. The endpoint is
// configured and the collector is listening; only the master switch is off, so
// anything arriving would mean the switch is not the thing deciding.
func TestTracingDisabledExportsNothing(t *testing.T) {
	receiver := newOTLPReceiver(t, false)

	m := start(t, map[string]string{
		"MOCKULUS_TRACING_ENABLED":  "false",
		"MOCKULUS_TRACING_ENDPOINT": receiver.endpoint(),
		"MOCKULUS_TRACING_INSECURE": "true",
		"MOCKULUS_SHUTDOWN_DRAIN":   "0s",
	})

	m.registerStub(t, `{"request":{"method":"GET","urlPath":"/gotest/tracing/off"},
	                    "response":{"status":200}}`)
	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/off"))
	if err != nil {
		t.Fatalf("serve untraced request: %v", err)
	}
	_ = resp.Body.Close()

	m.stop()

	if spans := receiver.collected(); len(spans) != 0 {
		t.Fatalf("tracing is off by default, but %d spans were exported: %v",
			len(spans), spanNames(receiver))
	}
}

// TestTracingJoinsCallerTraceContext proves the two halves that make a mock's
// spans useful to the suite driving it: W3C context is extracted from the
// request, and the caller's sampling decision overrides the local ratio.
//
// The ratio is zero, so nothing this pod started on its own would be sampled.
// A span arriving under the caller's trace id can only have come from the
// caller's decision.
func TestTracingJoinsCallerTraceContext(t *testing.T) {
	receiver := newOTLPReceiver(t, false)
	m := tracedInstance(t, receiver, map[string]string{
		"MOCKULUS_TRACING_SAMPLE_RATIO": "0",
	})

	m.registerStub(t, `{"request":{"method":"GET","urlPath":"/gotest/tracing/joined"},
	                    "response":{"status":200}}`)

	const (
		callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
		callerSpan  = "00f067aa0ba902b7"
	)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		m.mockURL("/gotest/tracing/joined"), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// The trailing 01 is the sampled flag: the caller has decided this trace is
	// being recorded.
	req.Header.Set("traceparent", "00-"+callerTrace+"-"+callerSpan+"-01")

	resp, err := harnessClient.Do(req)
	if err != nil {
		t.Fatalf("serve joined request: %v", err)
	}
	_ = resp.Body.Close()

	m.stop()

	mock := receiver.spanNamed("mock GET")
	if mock == nil {
		t.Fatalf("a sampled caller's request produced no span; got %v", spanNames(receiver))
	}
	if got := hex.EncodeToString(mock.GetTraceId()); got != callerTrace {
		t.Errorf("span trace id = %s, want the caller's %s — the trace was not joined",
			got, callerTrace)
	}
	if got := hex.EncodeToString(mock.GetParentSpanId()); got != callerSpan {
		t.Errorf("span parent = %s, want the caller's span %s", got, callerSpan)
	}
}

// TestTracingUnsampledRootProducesNoSpans is the other side of the ratio: with
// no caller decision to follow and a zero ratio, a request this pod is the root
// of is not recorded.
func TestTracingUnsampledRootProducesNoSpans(t *testing.T) {
	receiver := newOTLPReceiver(t, false)
	m := tracedInstance(t, receiver, map[string]string{
		"MOCKULUS_TRACING_SAMPLE_RATIO": "0",
	})

	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/unsampled"))
	if err != nil {
		t.Fatalf("serve request: %v", err)
	}
	_ = resp.Body.Close()

	m.stop()

	if spans := receiver.collected(); len(spans) != 0 {
		t.Fatalf("sample_ratio 0 still exported %d spans: %v", len(spans), spanNames(receiver))
	}
}

// TestTracingSendsConfiguredHeadersAndServiceName covers the two keys that
// describe the exporter rather than what it exports: the headers a collector
// authenticates with, and the service name every span is attributed to.
func TestTracingSendsConfiguredHeadersAndServiceName(t *testing.T) {
	receiver := newOTLPReceiver(t, false)
	m := tracedInstance(t, receiver, map[string]string{
		"MOCKULUS_TRACING_HEADERS":      "x-scope-orgid=checkout,authorization=Bearer tok",
		"MOCKULUS_TRACING_SERVICE_NAME": "mockulus-checkout",
	})

	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/headers"))
	if err != nil {
		t.Fatalf("serve request: %v", err)
	}
	_ = resp.Body.Close()

	m.stop()

	headers := receiver.exportHeaders()
	if len(headers) == 0 {
		t.Fatal("the collector was never called, so no export headers were observed")
	}
	if got := headers[0].Get("X-Scope-Orgid"); got != "checkout" {
		t.Errorf("export header x-scope-orgid = %q, want checkout", got)
	}
	if got := headers[0].Get("Authorization"); got != "Bearer tok" {
		t.Errorf("export header authorization = %q, want the configured token", got)
	}

	resources := receiver.resources()
	if len(resources) == 0 {
		t.Fatal("no resource spans were exported")
	}
	name, ok := attr(resources[0].GetResource().GetAttributes(), "service.name")
	if !ok || name != "mockulus-checkout" {
		t.Errorf("resource service.name = %q, want the configured mockulus-checkout", name)
	}
}

// TestTraceExportFailuresAreCounted asserts the outage signal. The collector
// refuses every batch; the counter is what tells an operator that the
// deployment believing it is tracing is not.
func TestTraceExportFailuresAreCounted(t *testing.T) {
	receiver := newOTLPReceiver(t, true)
	m := tracedInstance(t, receiver, nil)

	resp, err := httpGet(t.Context(), harnessClient, m.mockURL("/gotest/tracing/failing"))
	if err != nil {
		t.Fatalf("serve request: %v", err)
	}
	_ = resp.Body.Close()

	// The batch processor exports on its own schedule, so this is a poll rather
	// than a single read. It stays a bounded wait, never a sleep (SPEC §19.1).
	deadline := time.Now().Add(30 * time.Second)
	for {
		if failures := scrapeCounter(t, m, "mockulus_trace_export_failures_total"); failures > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a refusing collector never moved mockulus_trace_export_failures_total")
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatal("test ended before the export failure was counted")
		}
	}
}

// scrapeCounter reads one counter's value off /metrics.
func scrapeCounter(t *testing.T, m *mockulus, name string) float64 {
	t.Helper()

	resp, err := httpGet(t.Context(), harnessClient, m.adminURL("/metrics"))
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, name+" "), "%g", &v); err == nil {
			return v
		}
	}
	return 0
}

// TestTracingRefusesAnEndpointWithAScheme pins the fail-loud half of the
// configuration: `tracing.insecure` chooses the scheme, so an endpoint that
// also carries one is two answers to one question and is refused at startup
// rather than reinterpreted (P3).
func TestTracingRefusesAnEndpointWithAScheme(t *testing.T) {
	m := launch(t, map[string]string{
		"MOCKULUS_TRACING_ENABLED":  "true",
		"MOCKULUS_TRACING_ENDPOINT": "http://collector:4318",
	})

	code, exited := m.awaitExit(30 * time.Second)
	if !exited {
		t.Fatal("mockulus kept running with an endpoint it cannot honour")
	}
	if code == 0 {
		t.Errorf("exit code %d, want non-zero for an unusable configuration", code)
	}
	if out := strings.Join(m.logs(), "\n"); !strings.Contains(out, "tracing.endpoint") {
		t.Errorf("the refusal never names tracing.endpoint:\n%s", out)
	}
}

// TestTracingEnabledWithoutEndpointRefusesToStart is the other half: tracing
// that is on and aimed at nothing would build spans and drop every one, with a
// counter nobody has reason to look at as the only evidence.
func TestTracingEnabledWithoutEndpointRefusesToStart(t *testing.T) {
	m := launch(t, map[string]string{"MOCKULUS_TRACING_ENABLED": "true"})

	code, exited := m.awaitExit(30 * time.Second)
	if !exited {
		t.Fatal("mockulus started with tracing enabled and no collector to export to")
	}
	if code == 0 {
		t.Errorf("exit code %d, want non-zero for an unusable configuration", code)
	}
	if out := strings.Join(m.logs(), "\n"); !strings.Contains(out, "tracing.endpoint") {
		t.Errorf("the refusal never names tracing.endpoint:\n%s", out)
	}
}

func spanNames(r *otlpReceiver) []string {
	spans := r.collected()
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.GetName())
	}
	return out
}
