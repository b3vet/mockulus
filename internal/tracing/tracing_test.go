// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
)

// TestZeroSpanIsASafeNoOp is the one that holds the design up.
//
// Every call site declares a Span before it knows whether tracing is on and
// then annotates it unconditionally, so the untraced path — which is the
// default path, and the one the alloc budget of SPEC §16.3 is measured on —
// runs entirely through this zero value. If any method here needed a live span,
// serving with tracing off would panic rather than cost nothing.
func TestZeroSpanIsASafeNoOp(t *testing.T) {
	var span Span

	if span.Recording() {
		t.Error("a zero Span reports itself as recording, so call sites would build attributes for nothing")
	}
	if id := span.TraceID(); id != "" {
		t.Errorf("a zero Span has trace id %q, want empty so nothing correlates against a span that does not exist", id)
	}

	// None of these may panic.
	span.SetAttributes(String("k", "v"), Int("n", 1), Int64("n64", 2), Bool("b", true))
	span.SetHTTPStatus(http.StatusInternalServerError)
	span.SetError(errors.New("boom"))
	span.End()
}

// TestServerNameBoundsSpanNameCardinality pins the reason the vocabulary is
// fixed. mockulus serves whatever method a client sends, so a name built
// straight from the method would let mock traffic mint span names without
// bound — the same failure a per-stub metric label would be (SPEC §14.1).
func TestServerNameBoundsSpanNameCardinality(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "CONNECT"} {
		if got, want := ServerName("mock", method), "mock "+method; got != want {
			t.Errorf("ServerName(mock, %s) = %q, want %q", method, got, want)
		}
	}

	for _, method := range []string{"FOOBAR", "", "PROPFIND", "\x00weird"} {
		if got, want := ServerName("mock", method), "mock _OTHER"; got != want {
			t.Errorf("ServerName(mock, %q) = %q, want %q — an unregistered method must not become a span name",
				method, got, want)
		}
	}
}

// TestMethodAttrsCarryTheOriginal checks that bounding the name does not lose
// the method: an unrecognised one travels as an attribute instead, which is
// what the HTTP conventions ask for.
func TestMethodAttrsCarryTheOriginal(t *testing.T) {
	attrs := MethodAttrs("GET")
	if len(attrs) != 1 {
		t.Fatalf("a standard method produced %d attributes, want 1", len(attrs))
	}
	if got := attrs[0].Value.AsString(); got != "GET" {
		t.Errorf("http.request.method = %q, want GET", got)
	}

	attrs = MethodAttrs("FOOBAR")
	if len(attrs) != 2 {
		t.Fatalf("an unregistered method produced %d attributes, want the placeholder and the original", len(attrs))
	}
	if got := attrs[0].Value.AsString(); got != "_OTHER" {
		t.Errorf("http.request.method = %q, want _OTHER", got)
	}
	if got := attrs[1].Value.AsString(); got != "FOOBAR" {
		t.Errorf("http.request.method_original = %q, want the method as sent", got)
	}
}

// TestErrorHandlerCountsEveryFailureAndLogsSparingly separates the two signals.
// The counter is what an alert reads, so it must move on every refused batch;
// the log line explains one of them, and a collector that is down for an hour
// must not write an hour of identical lines.
func TestErrorHandlerCountsEveryFailureAndLogsSparingly(t *testing.T) {
	var counted int
	logged := &countingHandler{}
	h := newErrorHandler(slog.New(logged), func() { counted++ })

	for range 50 {
		h.Handle(errors.New("collector refused the batch"))
	}

	if counted != 50 {
		t.Errorf("counted %d failures, want all 50 — the counter is the rate signal", counted)
	}
	if logged.records != 1 {
		t.Errorf("wrote %d log lines for 50 failures in the same minute, want 1", logged.records)
	}

	// A nil error is not a failure and must not be counted as one.
	h.Handle(nil)
	if counted != 50 {
		t.Errorf("a nil error moved the counter to %d", counted)
	}
}

type countingHandler struct {
	slog.Handler
	records int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(context.Context, slog.Record) error {
	h.records++
	return nil
}

// TestShutdownOnNilProviderIsHarmless keeps the lifecycle wiring simple: the
// shutdown path can call this without first proving tracing was ever built.
func TestShutdownOnNilProviderIsHarmless(t *testing.T) {
	var p *Provider
	if err := p.Shutdown(t.Context()); err != nil {
		t.Errorf("shutting down an unbuilt provider returned %v", err)
	}
}
