// SPDX-License-Identifier: Apache-2.0

package response

import (
	"bufio"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/b3vet/mockulus/internal/stub"
)

// A stub that sets `statusMessage` is asking for something Go's net/http does
// not offer: WriteHeader always emits the canonical phrase for the status code,
// so "418 I'm a little teapot" arrives as "418 I'm a teapot". The only way to
// choose the phrase is to take the connection and write the status line by
// hand — the same http.Hijacker the fault paths use, for the same reason
// (SPEC §5.2, §12.5).
//
// Two things keep that from being a bad trade. It happens only for a stub that
// actually set a phrase, so every other stub is written exactly as it was
// before and pays nothing (P2). And it is confined to HTTP/1.1: over HTTP/2
// there is no connection to hijack and no reason phrase in the protocol to put
// one in, so the response falls back to the ordinary write. That is deviation
// #7, and it is the whole of it.

var headerNewlineToSpace = strings.NewReplacer("\r", " ", "\n", " ")

// sanitizeHeaderValue mirrors what net/http does to a header value on the way
// out: the line breaks replaced, then the surrounding space trimmed. The
// hijacked path serialises headers itself, and it must not be the one place in
// the server where a header value can inject a line of its own.
func sanitizeHeaderValue(v string) string {
	return textproto.TrimString(headerNewlineToSpace.Replace(v))
}

// chunkedFraming reads a stub's Transfer-Encoding and reports whether the body
// must be chunk-encoded here, and whether `chunked` still has to be added to say
// so on the wire.
//
// Any declared coding means chunking: net/http appends `chunked` to whatever the
// handler asked for and encodes the body for it, and nothing does that for a
// hijacked connection. A Transfer-Encoding header over a body written raw is the
// one response a client cannot recover from — it reads the first bytes of the
// body as a chunk size — so this is the one header that cannot simply be copied
// through.
func chunkedFraming(values []string) (chunked, declare bool) {
	if len(values) == 0 {
		return false, false
	}
	for _, v := range values {
		if strings.EqualFold(v, "chunked") {
			return true, false
		}
	}
	return true, true
}

// writeWithReason serves a response over a hijacked connection so that the
// stub's reason phrase reaches the wire. It reports the status it sent and
// whether it served at all; a false return means the connection could not be
// taken and the caller should write the response the ordinary way.
func writeWithReason(w http.ResponseWriter, r *http.Request, resp *stub.CompiledResponse,
	body []byte, slack time.Duration) (int, bool) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return 0, false
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		// Nothing has been written yet, so the ordinary path is still open and
		// the client gets its response — just with the canonical phrase.
		return 0, false
	}
	defer func() { _ = conn.Close() }()

	if slack > 0 {
		// The same budget setDeadline computes, applied to the connection now
		// that net/http no longer owns it.
		budget := slack
		if resp.Dribble != nil {
			budget += resp.Dribble.TotalDuration
		}
		_ = conn.SetWriteDeadline(time.Now().Add(budget))
	}

	header := w.Header()

	_, _ = buf.WriteString("HTTP/1.1 ")
	_, _ = buf.WriteString(strconv.Itoa(resp.Status))
	_, _ = buf.WriteString(" ")
	_, _ = buf.WriteString(resp.StatusMessage)
	_, _ = buf.WriteString("\r\n")

	// The connection ends with this response. Keeping it alive would mean
	// reading and dispatching the next request ourselves, because a hijacked
	// connection is no longer served by anything — that is re-implementing the
	// server we are standing inside, to save a handshake on the one kind of stub
	// that opted into this path. WireMock keeps it alive; mockulus closing it is
	// the cost of the phrase, and it is why nothing else takes this path.
	_, _ = buf.WriteString("Connection: close\r\n")

	if _, declared := header["Date"]; !declared {
		// net/http adds this on the ordinary path, and a stub should not lose a
		// header merely by naming a reason phrase.
		_, _ = buf.WriteString("Date: ")
		_, _ = buf.WriteString(time.Now().UTC().Format(http.TimeFormat))
		_, _ = buf.WriteString("\r\n")
	}

	// 1xx, 204 and 304 carry no body and must not be framed as if they did.
	bodyless := resp.Status < 200 || resp.Status == http.StatusNoContent ||
		resp.Status == http.StatusNotModified
	chunked, declareChunked := chunkedFraming(header["Transfer-Encoding"])
	if _, declared := header["Content-Length"]; !declared && !bodyless && !chunked {
		// Content-Length rather than chunking: the body is already a complete
		// []byte, so its length is known and the framing matches what the
		// ordinary path sends (DECISIONS.md D-OPEN-4).
		_, _ = buf.WriteString("Content-Length: ")
		_, _ = buf.WriteString(strconv.Itoa(len(body)))
		_, _ = buf.WriteString("\r\n")
	}
	for name, values := range header {
		switch {
		case name == "Connection":
			// The Connection: close above is the truth about this connection,
			// and a stub's own value would contradict it rather than change it.
			continue
		case name == "Content-Length" && chunked:
			// A message that declares a transfer coding must not also declare a
			// length: the two are different answers to how it ends.
			continue
		case !httpguts.ValidHeaderFieldName(name):
			// What net/http does with a name it cannot write, and for the same
			// reason: a name is as good a place to hide a CRLF as a value, and
			// this must not be the one writer in the server through which a stub
			// can put a header of its own on the wire.
			continue
		}
		// A Content-Type suppressed by the caller is present with a nil value,
		// which writes nothing here — the same "no header at all" the ordinary
		// path produces.
		for _, v := range values {
			_, _ = buf.WriteString(name)
			_, _ = buf.WriteString(": ")
			_, _ = buf.WriteString(sanitizeHeaderValue(v))
			_, _ = buf.WriteString("\r\n")
		}
	}
	if declareChunked && !bodyless {
		// After the stub's own codings, never before: the field lines combine in
		// the order they are sent, and chunked has to be the last coding applied
		// or it does not describe the framing below.
		_, _ = buf.WriteString("Transfer-Encoding: chunked\r\n")
	}
	_, _ = buf.WriteString("\r\n")

	// A HEAD response carries the headers of the GET and none of its body — and
	// no terminating chunk either, since there is no chunked body to terminate.
	if bodyless || r.Method == http.MethodHead {
		_ = buf.Flush()
		return resp.Status, true
	}

	if resp.Dribble != nil && len(body) > 0 {
		dribbleTo(buf.Writer, resp, body, chunked)
		return resp.Status, true
	}

	if chunked {
		writeChunked(buf.Writer, body)
	} else if len(body) > 0 {
		_, _ = buf.Write(body)
	}
	_ = buf.Flush()
	return resp.Status, true
}

// writeChunked writes a body as a single chunk followed by the terminator. An
// empty body still gets the terminator: the chunked framing has to be closed
// however little went through it.
func writeChunked(buf *bufio.Writer, body []byte) {
	if len(body) > 0 {
		writeChunk(buf, body)
	}
	_, _ = buf.WriteString("0\r\n\r\n")
}

// writeChunk frames one chunk: its length in hex, the bytes, and the CRLF that
// separates it from the next.
func writeChunk(buf *bufio.Writer, b []byte) {
	_, _ = buf.WriteString(strconv.FormatInt(int64(len(b)), 16))
	_, _ = buf.WriteString("\r\n")
	_, _ = buf.Write(b)
	_, _ = buf.WriteString("\r\n")
}

// dribbleTo is dribble over a hijacked connection: the same division and the
// same timing as the ordinary path, flushing the connection's writer rather
// than the ResponseWriter. A stub dribbles identically whether or not it also
// asked for a reason phrase.
func dribbleTo(buf *bufio.Writer, resp *stub.CompiledResponse, body []byte, chunked bool) {
	chunks, size, gap := dribblePlan(resp.Dribble, len(body))

	for i := range chunks {
		if gap > 0 {
			time.Sleep(gap)
		}
		start := i * size
		end := start + size
		if i == chunks-1 {
			end = len(body)
		}
		if chunked {
			// One HTTP chunk per dribble chunk, which is what the trickle is:
			// each flush has to be a complete message part or the client cannot
			// read it until the last one arrives.
			writeChunk(buf, body[start:end])
		} else if _, err := buf.Write(body[start:end]); err != nil {
			return
		}
		if err := buf.Flush(); err != nil {
			return
		}
	}

	if chunked {
		_, _ = buf.WriteString("0\r\n\r\n")
		_ = buf.Flush()
	}
}
