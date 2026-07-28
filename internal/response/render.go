// SPDX-License-Identifier: Apache-2.0

// Package response assembles what goes back on the wire: status, headers and
// body, plus the delay, dribble and fault behaviors a stub can ask for.
//
// Everything expensive was resolved when the stub was compiled, so rendering an
// ordinary stub is a few header writes and a single body write (SPEC §12.3).
// The interesting work here is the three ways a response can be deliberately
// abnormal — delayed, dribbled, or not a valid response at all — because those
// are what make a mock server useful for testing failure.
package response

import (
	"crypto/rand"
	"math"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/b3vet/mockulus/internal/handlebars"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Renderer renders the templated parts of a response. It is nil for a
// deployment with templating off, and unused by any stub that has no templates.
type Renderer interface {
	Render(tpl *handlebars.Template, ctx map[string]any) (string, error)
}

// Options carry what writing a response needs beyond the response itself.
type Options struct {
	WriteSlack time.Duration
	// Settings is the deployment's global response delay, nil when nobody has
	// set one. It comes off the snapshot, so an instance that has never had a
	// settings write pays a nil check for the feature and nothing else (P2).
	Settings *stub.Settings
	// Renderer and Context are supplied only when the stub is templated.
	Renderer Renderer
	Context  map[string]any
	// OnRenderError counts a serve-time render failure.
	OnRenderError func()
}

// Write emits a compiled response and reports the status it sent, which the
// caller records as a metric. A fault reports the status as 0: nothing that
// could be called a status ever reached the client.
func Write(w http.ResponseWriter, r *http.Request, resp *stub.CompiledResponse, opts Options) int {
	applyDelay(r, resp, opts.Settings)

	if resp.Fault != "" {
		injectFault(w, resp.Fault)
		return 0
	}

	// A stub whose body file has not been uploaded yet compiles fine and serves
	// this until the file appears (SPEC §6.9).
	if resp.BodyFileMissing {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeBodyFileMissing,
			"the stub's bodyFileName "+resp.BodyFileName+" has no corresponding file"))
		return wmcompat.StatusFor(wmcompat.CodeBodyFileMissing)
	}

	setDeadline(w, resp, opts.WriteSlack)

	body := resp.Body
	headers := resp.Headers

	if resp.Templated && opts.Renderer != nil {
		rendered, renderedHeaders, err := render(resp, opts)
		if err != nil {
			// WireMock renders the error text into the response body rather
			// than failing the request, so a template bug is visible to
			// whoever is looking at the response (SPEC §10.4).
			//
			// `text/plain` unadorned is WireMock 3.13.2's own spelling here,
			// re-derived by probe — and not the `text/plain;charset=UTF-8` of
			// the unmatched 404, which is a different response it spells
			// differently. The message inside is ours (deviation #18's line:
			// shape and Content-Type are compat surface, diagnostic text is
			// not), and it says what failed rather than only that something
			// did.
			if opts.OnRenderError != nil {
				opts.OnRenderError()
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Template render error: " + err.Error()))
			return http.StatusInternalServerError
		}
		body, headers = rendered, renderedHeaders
	}

	header := w.Header()
	for _, h := range headers {
		header.Add(h.Name, h.Value)
	}
	if _, declared := header["Content-Type"]; !declared {
		// A stub that declares no Content-Type gets none on the wire. Left to
		// itself net/http would sniff the body and invent one, so a JSON body
		// would arrive labelled text/plain — a header the stub author never
		// wrote and cannot remove. Assigning nil suppresses the sniffing.
		header["Content-Type"] = nil
	}

	if resp.HasStatusMessage {
		// Only a stub that asked for a reason phrase leaves the ordinary path,
		// and it rejoins it if the connection cannot be taken (HTTP/2). The
		// test is whether the key was there rather than whether it was
		// non-empty: `"statusMessage": ""` asks for a blank phrase, which
		// HTTP/1.1 permits and WireMock sends, and reading empty as unset
		// answered it with the canonical phrase instead.
		if status, served := writeWithReason(w, r, resp, body, opts.WriteSlack); served {
			return status
		}
	}

	if resp.Dribble != nil && len(body) > 0 {
		return dribble(w, resp, body)
	}

	w.WriteHeader(resp.Status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
	return resp.Status
}

// render evaluates the stub's templates. Only the parts that carry template
// syntax were compiled, so an untemplated header costs a slice copy at most.
func render(resp *stub.CompiledResponse, opts Options) ([]byte, []stub.Header, error) {
	body := resp.Body
	if resp.BodyTemplate != nil {
		out, err := opts.Renderer.Render(resp.BodyTemplate, opts.Context)
		if err != nil {
			return nil, nil, err
		}
		body = []byte(out)
	}

	headers := resp.Headers
	if len(resp.HeaderTemplates) > 0 {
		headers = make([]stub.Header, len(resp.Headers))
		copy(headers, resp.Headers)
		for _, ht := range resp.HeaderTemplates {
			out, err := opts.Renderer.Render(ht.Template, opts.Context)
			if err != nil {
				return nil, nil, err
			}
			headers[ht.Index].Value = out
		}
	}
	return body, headers, nil
}

// applyDelay waits out the delay this response is owed. A timer is used rather
// than a sleeping worker because there is no worker pool to block: net/http's
// goroutine-per-connection model scales to tens of thousands of concurrent
// sleepers, which is what makes delay simulation cheap here (SPEC §12.4).
func applyDelay(r *http.Request, resp *stub.CompiledResponse, global *stub.Settings) {
	total := composeDelay(resp, global)
	if total <= 0 {
		return
	}

	timer := time.NewTimer(total)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
		// The client gave up; there is nothing left to delay.
	}
}

// composeDelay resolves how long this response waits, out of the stub's own
// delay and the deployment's global one.
//
// The two compose the way WireMock 3.13.2 composes them, verified by probe: the
// fixed and the sampled part are summed, but within each part a value the stub
// declared *replaces* the global one rather than adding to it. So
// `fixedDelayMilliseconds: 0` is a stub opting out of the global fixed delay,
// which is why a declared zero has to be distinguishable from an absent field
// (SPEC §12.4). Only a matched response reaches here at all, so an unmatched
// request still 404s immediately.
func composeDelay(resp *stub.CompiledResponse, global *stub.Settings) time.Duration {
	fixed, dist := resp.FixedDelay, resp.Delay
	if global != nil {
		if !resp.FixedDelaySet {
			fixed = global.FixedDelay
		}
		if dist.Kind == stub.DelayNone {
			dist = global.Delay
		}
	}
	return fixed + drawDelay(dist)
}

// drawDelay samples the configured distribution.
func drawDelay(d stub.DelayDistribution) time.Duration {
	switch d.Kind {
	case stub.DelayUniform:
		span := d.Upper - d.Lower
		if span <= 0 {
			return d.Lower
		}
		return d.Lower + time.Duration(mathrand.Int64N(int64(span)+1))

	case stub.DelayLogNormal:
		// WireMock parameterises this by the median and sigma of the underlying
		// normal, so the median maps to exp(mu) directly.
		if d.Median <= 0 {
			return 0
		}
		mu := math.Log(float64(d.Median))
		sample := math.Exp(mu + d.Sigma*mathrand.NormFloat64())
		if sample <= 0 || math.IsInf(sample, 0) || math.IsNaN(sample) {
			return 0
		}
		if sample > float64(math.MaxInt64) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(sample)

	default:
		return 0
	}
}

// setDeadline bounds how long writing this response may take: the delay it
// asked for plus slack. Without it a stalled client could hold a connection
// indefinitely, and with a fixed timeout a legitimately delayed stub could not
// be served at all (SPEC §12.1).
func setDeadline(w http.ResponseWriter, resp *stub.CompiledResponse, slack time.Duration) {
	if slack <= 0 {
		return
	}
	rc := http.NewResponseController(w)
	budget := slack
	if resp.Dribble != nil {
		budget += resp.Dribble.TotalDuration
	}
	// A deadline is best-effort: HTTP/2 does not support it, and that is fine.
	_ = rc.SetWriteDeadline(time.Now().Add(budget))
}

// dribble spreads the body across several writes over a total duration, which
// is how a slow or trickling upstream is simulated (SPEC §12.6).
func dribble(w http.ResponseWriter, resp *stub.CompiledResponse, body []byte) int {
	chunks, size, gap := dribblePlan(resp.Dribble, len(body))

	rc := http.NewResponseController(w)

	for i := range chunks {
		if gap > 0 {
			time.Sleep(gap)
		}
		if i == 0 {
			w.WriteHeader(resp.Status)
		}
		start := i * size
		end := start + size
		if i == chunks-1 {
			end = len(body)
		}
		if _, err := w.Write(body[start:end]); err != nil {
			return resp.Status
		}
		_ = rc.Flush()
	}
	return resp.Status
}

// dribblePlan resolves how a dribbled body is divided and how long each pause
// is. It is shared with the hijacked writer so that adding a reason phrase to a
// dribbling stub cannot change its timing.
func dribblePlan(d *stub.ChunkedDribble, n int) (chunks, size int, gap time.Duration) {
	chunks = d.NumberOfChunks
	if chunks > n {
		// More chunks than bytes would mean empty writes; one byte per chunk is
		// the finest division that still transmits something each time.
		chunks = n
	}
	if chunks < 1 {
		chunks = 1
	}

	// One interval per chunk, and the header block waits out the first one
	// alongside chunk zero — so the total is the configured duration rather
	// than the duration plus a free first chunk.
	return chunks, n / chunks, d.TotalDuration / time.Duration(chunks)
}

// randomDataSize is how many bytes RANDOM_DATA_THEN_CLOSE sends. Enough to be
// unmistakably not a response, small enough to cost nothing.
const randomDataSize = 512

// injectFault produces a deliberately broken response.
//
// Every fault works by hijacking the connection and writing below the HTTP
// layer, because that is the only way to produce bytes that are not a valid
// response. Hijacking is available on HTTP/1.1 only, which is precisely why
// h2c is off by default (SPEC §12.5, deviation #15).
func injectFault(w http.ResponseWriter, fault string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		// Over HTTP/2 there is no connection to take over, so the fault
		// degrades to the closest available signal: an abrupt stream reset.
		panic(http.ErrAbortHandler)
	}

	conn, buf, err := hijacker.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	defer func() { _ = conn.Close() }()

	switch fault {
	case stub.FaultConnectionReset:
		// SetLinger(0) makes Close send an RST rather than a FIN, which is what
		// a peer resetting the connection actually looks like.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}

	case stub.FaultEmptyResponse:
		// Close with nothing written at all.

	case stub.FaultMalformedChunk:
		// A valid status line and headers, then garbage where the chunked body
		// should be — the client parses far enough to commit, then fails.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
		_, _ = buf.WriteString("lskdu018973t09sylgasjkfg1][]'./.sdlv")
		_ = buf.Flush()

	case stub.FaultRandomThenClose:
		junk := make([]byte, randomDataSize)
		if _, err := rand.Read(junk); err == nil {
			_, _ = buf.Write(junk)
			_ = buf.Flush()
		}
	}
}
