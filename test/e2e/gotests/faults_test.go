// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fault injection is the clearest case for a Go-native test in the whole suite.
// Every fault of SPEC §12.5 exists precisely to produce something that is not a
// response, and net/http collapses all four into one opaque transport error —
// so a corpus case could assert that the client failed, but not that it failed
// for the reason the stub asked for. These read the bytes instead.

// faultInstance starts a mockulus for a fault case with no drain window.
// Nothing here is about shutdown, and every case in this file needs its own
// process; waiting out the default five-second drain five times over would cost
// more than the assertions do.
func faultInstance(t *testing.T) *mockulus {
	t.Helper()
	return start(t, map[string]string{"MOCKULUS_SHUTDOWN_DRAIN": "0"})
}

// faultMapping is a stub whose entire response is the named fault. A fault
// replaces the response rather than decorating it, so there is nothing else to
// state.
func faultMapping(path, fault string) string {
	return fmt.Sprintf(`{"request": {"method": "GET", "urlPath": %q},
	                     "response": {"fault": %q}}`, path, fault)
}

// faultExchange sends one HTTP/1.1 request over a bare socket and reads until
// the far end stops talking, returning every byte that arrived and the error
// that ended the read.
//
// Writing the request by hand is the point: it is the only way to observe a
// reply that is not a reply, and to see how the connection ended.
func faultExchange(t *testing.T, addr, path string) ([]byte, error) {
	t.Helper()

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial the mock port: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set the socket deadline: %v", err)
	}
	request := "GET " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write the request: %v", err)
	}

	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			return got, err
		}
	}
}

// TestFaultEmptyResponse asserts EMPTY_RESPONSE closes without writing.
//
// The distinction worth defending is against a 200 with an empty body: both
// leave an HTTP client holding nothing, and only the socket can tell them
// apart. Zero bytes followed by an orderly EOF is the fault; fifteen bytes of
// status line is not.
func TestFaultEmptyResponse(t *testing.T) {
	m := faultInstance(t)
	m.registerStub(t, faultMapping("/e2e/gotest-fault-empty/nothing", "EMPTY_RESPONSE"))

	body, err := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-empty/nothing")

	if len(body) != 0 {
		t.Fatalf("EMPTY_RESPONSE wrote %d bytes, want none: %q", len(body), body)
	}
	// An orderly FIN, not a reset: EMPTY_RESPONSE closes the connection, and
	// CONNECTION_RESET_BY_PEER is the fault that aborts it. If this ever became
	// ECONNRESET the two faults would have collapsed into one.
	if !errors.Is(err, io.EOF) {
		t.Fatalf("EMPTY_RESPONSE ended the connection with %v, want io.EOF", err)
	}
}

// TestFaultRandomDataThenClose asserts RANDOM_DATA_THEN_CLOSE sends bytes that
// are not a response at all.
//
// A client cannot report this as anything more specific than "bad protocol", so
// the assertions that matter — that something arrived, that it does not parse
// as HTTP, and that it is actually random rather than a fixed blob — are only
// available here.
func TestFaultRandomDataThenClose(t *testing.T) {
	m := faultInstance(t)
	m.registerStub(t, faultMapping("/e2e/gotest-fault-random/junk", "RANDOM_DATA_THEN_CLOSE"))

	first, err := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-random/junk")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("RANDOM_DATA_THEN_CLOSE ended the connection with %v, want io.EOF after the data", err)
	}
	// randomDataSize: enough to be unmistakably not a response, and fixed, so a
	// truncated write would show up as a short read rather than pass unnoticed.
	if len(first) != 512 {
		t.Fatalf("RANDOM_DATA_THEN_CLOSE sent %d bytes, want 512", len(first))
	}

	if bytes.HasPrefix(first, []byte("HTTP/")) {
		t.Fatalf("RANDOM_DATA_THEN_CLOSE sent something beginning like a status line: %q", first[:16])
	}
	if parsed, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(first)), nil); err == nil {
		_ = parsed.Body.Close()
		t.Fatalf("RANDOM_DATA_THEN_CLOSE sent bytes that parse as an HTTP response: %q", first[:64])
	}

	// Two requests must not produce the same bytes. A constant payload would
	// satisfy every assertion above while no longer being random data.
	second, _ := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-random/junk")
	if bytes.Equal(first, second) {
		t.Fatal("RANDOM_DATA_THEN_CLOSE sent identical bytes twice, so the data is not random")
	}
}

// TestFaultConnectionResetByPeer asserts the reset is a reset — an RST rather
// than the orderly close every other fault performs.
//
// This is the sharpest example of what an HTTP client hides. Both faults here
// deliver zero bytes and both surface as a transport error; the difference is
// visible only in how the connection died. On darwin and linux the two are
// reliably distinguishable: SetLinger(0) makes the peer's read fail with
// ECONNRESET, where a plain close yields io.EOF. EMPTY_RESPONSE is the control
// that proves the distinction is being observed rather than assumed.
func TestFaultConnectionResetByPeer(t *testing.T) {
	m := faultInstance(t)
	m.registerStub(t, faultMapping("/e2e/gotest-fault-reset/rst", "CONNECTION_RESET_BY_PEER"))
	m.registerStub(t, faultMapping("/e2e/gotest-fault-reset/fin", "EMPTY_RESPONSE"))

	reset, resetErr := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-reset/rst")
	if len(reset) != 0 {
		t.Fatalf("CONNECTION_RESET_BY_PEER wrote %d bytes before the reset, want none: %q", len(reset), reset)
	}
	if !errors.Is(resetErr, syscall.ECONNRESET) {
		t.Fatalf("CONNECTION_RESET_BY_PEER ended with %v, want ECONNRESET; "+
			"an io.EOF here means the connection was closed rather than reset", resetErr)
	}

	_, orderlyErr := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-reset/fin")
	if !errors.Is(orderlyErr, io.EOF) {
		t.Fatalf("the orderly-close control ended with %v, want io.EOF; "+
			"without it the reset assertion above proves nothing", orderlyErr)
	}
}

// malformedChunkResponse is what MALFORMED_RESPONSE_CHUNK puts on the wire: a
// status line and headers a client will accept, then garbage where the first
// chunk header belongs.
const malformedChunkResponse = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n" +
	"lskdu018973t09sylgasjkfg1][]'./.sdlv"

// TestFaultMalformedResponseChunk asserts the fault is malformed in the
// specific way that makes it useful: valid far enough for a client to commit to
// the response, then broken.
//
// Both halves are needed. The raw socket shows the bytes are the ones §12.5
// describes; the net/http leg shows the property a user actually depends on,
// which is that a real client accepts the headers and then fails reading the
// body. A corpus case sees only the second half, as an opaque error.
func TestFaultMalformedResponseChunk(t *testing.T) {
	m := faultInstance(t)
	m.registerStub(t, faultMapping("/e2e/gotest-fault-chunk/broken", "MALFORMED_RESPONSE_CHUNK"))

	raw, err := faultExchange(t, m.mockAddr, "/e2e/gotest-fault-chunk/broken")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("MALFORMED_RESPONSE_CHUNK ended the connection with %v, want io.EOF after the garbage", err)
	}
	if string(raw) != malformedChunkResponse {
		t.Fatalf("MALFORMED_RESPONSE_CHUNK sent\n%q\nwant\n%q", raw, malformedChunkResponse)
	}

	// A pooled connection must not be handed the corpse of this exchange, so
	// the client keeps nothing.
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := httpGet(t.Context(), client, m.mockURL("/e2e/gotest-fault-chunk/broken"))
	if err != nil {
		// Rejecting at header time is a legitimate way for a client to fail,
		// and it is still a failure, which is the property under test. This
		// cannot become a vacuous pass: the exact bytes were pinned above, so
		// the only way here is a client stricter than net/http is today.
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the client read status %d, want the 200 the fault's status line states", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a real HTTP client read the malformed body without error, " +
			"so the fault no longer breaks the clients it exists to break")
	}
}

// TestFaultWithBodyRejected asserts a stub cannot ask for both a fault and a
// body, and that the rejection is total.
//
// The 422 alone is corpus territory. What is not is the second half: proving
// the rejected stub left nothing behind that hijacks the connection. Every
// other case in this file reaches the socket and finds no HTTP there, so the
// boundary of that behavior belongs beside them — a well-formed response on a
// raw socket is what "the fault never registered" looks like.
func TestFaultWithBodyRejected(t *testing.T) {
	m := faultInstance(t)

	const path = "/e2e/gotest-fault-guard/both"
	mapping := fmt.Sprintf(`{"request": {"method": "GET", "urlPath": %q},
	                         "response": {"fault": "EMPTY_RESPONSE", "body": "served"}}`, path)

	resp, err := httpPost(t.Context(), harnessClient, m.adminURL("/__admin/mappings"),
		"application/json", strings.NewReader(mapping))
	if err != nil {
		t.Fatalf("register the fault-and-body stub: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the rejection: %v", err)
	}

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a stub with both a fault and a body registered with status %d, want 422: %s",
			resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "/response/fault") {
		t.Fatalf("the rejection does not point at the offending field: %s", body)
	}

	// Nothing registered, so the path is unmatched — and unmatched is served as
	// an ordinary HTTP response, not as a hijacked connection.
	raw, err := faultExchange(t, m.mockAddr, path)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("the rejected stub's path ended the connection with %v, want a served response", err)
	}
	if !bytes.HasPrefix(raw, []byte("HTTP/1.1 404")) {
		t.Fatalf("the rejected stub's path answered %q, want a 404 status line", raw)
	}
}
