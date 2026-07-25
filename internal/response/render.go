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

	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Write emits a compiled response and reports the status it sent, which the
// caller records as a metric. A fault reports the status as 0: nothing that
// could be called a status ever reached the client.
func Write(w http.ResponseWriter, r *http.Request, resp *stub.CompiledResponse, writeSlack time.Duration) int {
	applyDelay(r, resp)

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

	setDeadline(w, resp, writeSlack)

	header := w.Header()
	for _, h := range resp.Headers {
		header.Add(h.Name, h.Value)
	}
	if _, declared := header["Content-Type"]; !declared {
		// A stub that declares no Content-Type gets none on the wire. Left to
		// itself net/http would sniff the body and invent one, so a JSON body
		// would arrive labelled text/plain — a header the stub author never
		// wrote and cannot remove. Assigning nil suppresses the sniffing.
		header["Content-Type"] = nil
	}

	if resp.Dribble != nil && len(resp.Body) > 0 {
		return dribble(w, resp)
	}

	w.WriteHeader(resp.Status)
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}
	return resp.Status
}

// applyDelay waits out the stub's configured delay. A timer is used rather than
// a sleeping worker because there is no worker pool to block: net/http's
// goroutine-per-connection model scales to tens of thousands of concurrent
// sleepers, which is what makes delay simulation cheap here (SPEC §12.4).
func applyDelay(r *http.Request, resp *stub.CompiledResponse) {
	total := resp.FixedDelay + drawDelay(resp.Delay)
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
func dribble(w http.ResponseWriter, resp *stub.CompiledResponse) int {
	chunks := resp.Dribble.NumberOfChunks
	if chunks > len(resp.Body) {
		// More chunks than bytes would mean empty writes; one byte per chunk is
		// the finest division that still transmits something each time.
		chunks = len(resp.Body)
	}
	if chunks < 1 {
		chunks = 1
	}

	// The gap goes *between* chunks, so the last one is not followed by a wait
	// the client would experience as an idle connection.
	var gap time.Duration
	if chunks > 1 {
		gap = resp.Dribble.TotalDuration / time.Duration(chunks-1)
	}

	rc := http.NewResponseController(w)
	w.WriteHeader(resp.Status)

	size := len(resp.Body) / chunks
	for i := range chunks {
		start := i * size
		end := start + size
		if i == chunks-1 {
			end = len(resp.Body)
		}
		if _, err := w.Write(resp.Body[start:end]); err != nil {
			return resp.Status
		}
		_ = rc.Flush()
		if i < chunks-1 && gap > 0 {
			time.Sleep(gap)
		}
	}
	return resp.Status
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
