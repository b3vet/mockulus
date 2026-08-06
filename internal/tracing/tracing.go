// SPDX-License-Identifier: Apache-2.0

// Package tracing implements the optional OpenTelemetry tracing of SPEC §14.3.
//
// Two properties shape everything here. Tracing is **off by default**, and when
// it is off it must cost nothing: the rest of mockulus holds a tracer in an
// atomic pointer that is nil unless a collector was configured, so an untraced
// request pays one atomic load and a branch — the same shape the journal uses
// for the same reason (SPEC §16.3, P2). And export is **never on the request
// path**: spans go to a batching processor that hands them to a background
// exporter, so a slow or absent collector costs served requests nothing but the
// spans it dropped, which are counted rather than logged one by one.
//
// This package is the seam that keeps OpenTelemetry types out of the engine and
// the admin handlers. They see Tracer, Span and Attr; nothing else imports the
// SDK.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// scopeName identifies mockulus as the instrumentation scope on every span it
// produces.
const scopeName = "github.com/b3vet/mockulus"

// Options is the resolved tracing configuration. It mirrors the `tracing.*`
// keys of SPEC §13 with the headers already parsed, so this package never
// interprets a configuration string.
type Options struct {
	Endpoint    string
	Insecure    bool
	Headers     map[string]string
	SampleRatio float64
	ServiceName string
	Version     string
	InstanceID  string
}

// Provider owns the exporter, the span processor and the tracer built on them.
type Provider struct {
	tp     *sdktrace.TracerProvider
	tracer *Tracer
}

// New builds a provider exporting over OTLP/HTTP.
//
// OTLP/HTTP is the only transport offered. It is the protocol every mainstream
// collector accepts and the one OpenTelemetry recommends by default; a gRPC
// exporter would be a second configuration surface for the same job, and can be
// added later without changing anything a deployment already sets.
//
// onExportFailure is called once per failed export batch. A collector that is
// unreachable must not look like a deployment with nothing to say, and it must
// not fill the log either — the counter is the signal, and the log line is rate
// limited behind it.
func New(ctx context.Context, opts Options, log *slog.Logger, onExportFailure func()) (*Provider, error) {
	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(opts.Endpoint),
		// The SDK would retry a refused batch for a minute by default. That is a
		// minute of an export worker held by spans that are already late, and a
		// minute a drain can be waiting on at shutdown; a collector that has not
		// recovered in fifteen seconds is an outage, and the counter is the
		// answer to an outage rather than a longer queue.
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: time.Second,
			MaxInterval:     5 * time.Second,
			MaxElapsedTime:  15 * time.Second,
		}),
	}
	if opts.Insecure {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}
	if len(opts.Headers) > 0 {
		exporterOpts = append(exporterOpts, otlptracehttp.WithHeaders(opts.Headers))
	}

	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("build otlp trace exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(opts.ServiceName),
		semconv.ServiceVersion(opts.Version),
		semconv.ServiceInstanceID(opts.InstanceID),
	))
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}

	// ParentBased is the whole point of the default ratio being conservative: a
	// request arriving with W3C trace context follows the decision its caller
	// already made, so a sampled test trace always carries the mock spans it
	// caused. The ratio governs only the traces this pod starts itself.
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// The SDK reports export failures through the global error handler rather
	// than through a return value, since by then nothing is waiting on the
	// batch. Routing it here is what turns a dead collector into a counter an
	// alert can see.
	otel.SetErrorHandler(newErrorHandler(log, onExportFailure))

	return &Provider{
		tp: tp,
		tracer: &Tracer{
			t: tp.Tracer(scopeName, trace.WithInstrumentationVersion(opts.Version)),
			// W3C trace context only. Baggage has no consumer in mockulus, and
			// a propagator that parses a header nothing reads is surface
			// without a purpose; adding it later changes nothing already set.
			prop: propagation.TraceContext{},
		},
	}, nil
}

// Tracer returns the handle the rest of mockulus starts spans with.
func (p *Provider) Tracer() *Tracer { return p.tracer }

// Shutdown flushes pending spans and stops the exporter. It is called after the
// listeners have drained, so the spans of the last requests served are exported
// rather than discarded (SPEC §4.5).
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tp == nil {
		return nil
	}
	if err := p.tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down tracer provider: %w", err)
	}
	return nil
}

// errorHandler funnels SDK errors into the export-failure counter and a rate
// limited log line.
type errorHandler struct {
	log       *slog.Logger
	onFail    func()
	lastLogNs atomic.Int64
}

// logEvery bounds how often an export failure is logged. The counter carries
// the rate; the log line carries the reason, and one reason a minute is enough
// to diagnose a collector outage without burying everything else in it.
const logEvery = time.Minute

func newErrorHandler(log *slog.Logger, onFail func()) otel.ErrorHandler {
	return &errorHandler{log: log, onFail: onFail}
}

func (h *errorHandler) Handle(err error) {
	if err == nil {
		return
	}
	if h.onFail != nil {
		h.onFail()
	}
	now := time.Now().UnixNano()
	last := h.lastLogNs.Load()
	if now-last < int64(logEvery) && last != 0 {
		return
	}
	if !h.lastLogNs.CompareAndSwap(last, now) {
		return
	}
	h.log.Warn("trace export failed; spans are being dropped", "error", err)
}

// Tracer starts spans. A nil *Tracer is not valid — callers hold one only when
// tracing is enabled, and check for that before reaching this package.
type Tracer struct {
	t    trace.Tracer
	prop propagation.TextMapPropagator
}

// Attr is one span attribute. It is aliased rather than wrapped so that call
// sites can build attributes without importing OpenTelemetry themselves.
type Attr = attribute.KeyValue

// String builds a string attribute.
func String(k, v string) Attr { return attribute.String(k, v) }

// Int builds an integer attribute.
func Int(k string, v int) Attr { return attribute.Int(k, v) }

// Int64 builds a 64-bit integer attribute.
func Int64(k string, v int64) Attr { return attribute.Int64(k, v) }

// Bool builds a boolean attribute.
func Bool(k string, v bool) Attr { return attribute.Bool(k, v) }

// StartServer begins a server span for an inbound request, continuing the
// caller's trace when the request carries W3C trace context.
//
// The returned context carries the span; a caller that wants child spans puts
// it on the request.
func (t *Tracer) StartServer(r *http.Request, name string, attrs ...Attr) (context.Context, Span) {
	ctx := t.prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := t.t.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attrs...))
	return ctx, Span{s: span}
}

// StartInternal begins a span for work inside the process — a match decision, a
// template render — as a child of whatever ctx carries.
func (t *Tracer) StartInternal(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	ctx, span := t.t.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
	return ctx, Span{s: span}
}

// StartClient begins a span for a call this process makes out to its store.
func (t *Tracer) StartClient(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	ctx, span := t.t.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	return ctx, Span{s: span}
}

// StartRoot begins a span for background work that no request caused — a
// snapshot rebuild, a journal flush. It deliberately drops any ambient context
// so that a rebuild triggered by an admin write does not attach itself to that
// request's trace and outlive it.
func (t *Tracer) StartRoot(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	ctx, span := t.t.Start(trace.ContextWithSpanContext(ctx, trace.SpanContext{}), name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
	return ctx, Span{s: span}
}

// Span is a started span. The zero value is a valid no-op, which is what lets a
// caller declare one before knowing whether tracing is on and then use it
// unconditionally.
type Span struct{ s trace.Span }

// Recording reports whether anything this span is told will be exported. Call
// sites use it to skip building attributes for an unsampled request.
func (s Span) Recording() bool { return s.s != nil && s.s.IsRecording() }

// SetAttributes annotates the span.
func (s Span) SetAttributes(attrs ...Attr) {
	if s.s == nil {
		return
	}
	s.s.SetAttributes(attrs...)
}

// SetHTTPStatus records a response status, marking 5xx as an error.
//
// 4xx is deliberately not an error: an unmatched request answering 404 is
// mockulus working exactly as specified (SPEC §5.4), and a trace view that
// paints every one of them red would be useless in the suites that produce them
// by the hundred.
func (s Span) SetHTTPStatus(status int) {
	if s.s == nil {
		return
	}
	s.s.SetAttributes(semconv.HTTPResponseStatusCode(status))
	if status >= 500 {
		s.s.SetStatus(codes.Error, http.StatusText(status))
	}
}

// SetError marks the span as failed.
func (s Span) SetError(err error) {
	if s.s == nil || err == nil {
		return
	}
	s.s.RecordError(err)
	s.s.SetStatus(codes.Error, err.Error())
}

// TraceID returns the span's trace id in its hex form, or the empty string when
// there is no sampled span to correlate against. It is what puts a trace id on
// a journal entry and an access log line.
func (s Span) TraceID() string {
	if s.s == nil {
		return ""
	}
	sc := s.s.SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// End closes the span.
func (s Span) End() {
	if s.s == nil {
		return
	}
	s.s.End()
}

// ServerName renders the span name for an inbound request.
//
// The vocabulary is fixed on purpose. Span names are what a trace backend
// groups by, and mockulus serves whatever method a client sends — including
// ones no registry defines — so a name built straight from the method would let
// mock traffic mint span names without bound, the same way a per-stub metric
// label would mint series (SPEC §14.1). An unrecognised method is reported as
// `_OTHER`, which is what OpenTelemetry's own HTTP conventions do, and the
// method itself still travels as an attribute.
func ServerName(prefix, method string) string {
	return prefix + " " + methodName(method)
}

// MethodAttrs describes a request method under the HTTP conventions: the method
// itself when it is a standard one, `_OTHER` plus the original when it is not.
func MethodAttrs(method string) []Attr {
	if name := methodName(method); name != methodOther {
		return []Attr{semconv.HTTPRequestMethodKey.String(name)}
	}
	return []Attr{
		semconv.HTTPRequestMethodKey.String(methodOther),
		semconv.HTTPRequestMethodOriginal(method),
	}
}

// URLPath builds the request-path attribute.
func URLPath(path string) Attr { return semconv.URLPath(path) }

const methodOther = "_OTHER"

func methodName(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return method
	default:
		return methodOther
	}
}
