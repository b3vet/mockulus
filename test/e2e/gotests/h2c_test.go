// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// h2cTransport speaks cleartext HTTP/2 with prior knowledge: AllowHTTP lets the
// http:// scheme through, and dialing plain TCP where TLS would go means the
// connection preface lands on the socket with no negotiation of any kind. A
// listener that is not serving h2c has no way to answer it.
func h2cTransport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}

// noDrainEnv is what every case here starts from: the drain window zeroed, so
// tearing down three processes costs nothing rather than fifteen seconds.
// Nothing else is set — h2c_enabled in particular — which is what makes an
// instance started with it the default one.
func noDrainEnv() map[string]string {
	return map[string]string{"MOCKULUS_SHUTDOWN_DRAIN": "0"}
}

// h2cEnv turns cleartext HTTP/2 on, the one knob under test.
func h2cEnv() map[string]string {
	env := noDrainEnv()
	env["MOCKULUS_H2C_ENABLED"] = "true"
	return env
}

// h2cStub is a stub whose only job is to prove which protocol carried it.
func h2cStub(path, body string) string {
	return fmt.Sprintf(
		`{"request":{"method":"GET","url":%q},"response":{"status":200,"body":%q}}`,
		path, body)
}

// readStatusLine consumes an HTTP/1.1 status line and header block from conn,
// returning the buffered reader so whatever follows on the socket is not lost.
func readStatusLine(t *testing.T, conn net.Conn) (*bufio.Reader, string, http.Header) {
	t.Helper()

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	status := strings.TrimRight(line, "\r\n")

	headers := http.Header{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return br, status, headers
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("malformed header line %q", line)
		}
		headers.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
}

// readStreamOne reads frames until stream 1 ends, returning its :status and
// body. Everything else the peer sends is either acknowledged or ignored.
func readStreamOne(t *testing.T, fr *http2.Framer) (status, body string) {
	t.Helper()

	for {
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				if err := fr.WriteSettingsAck(); err != nil {
					t.Fatalf("ack settings: %v", err)
				}
			}
		case *http2.PingFrame:
			if err := fr.WritePing(true, f.Data); err != nil {
				t.Fatalf("ack ping: %v", err)
			}
		case *http2.MetaHeadersFrame:
			if f.StreamID == 1 {
				status = f.PseudoValue("status")
				if f.StreamEnded() {
					return status, body
				}
			}
		case *http2.DataFrame:
			if f.StreamID == 1 {
				body += string(f.Data())
				if f.StreamEnded() {
					return status, body
				}
			}
		case *http2.RSTStreamFrame:
			t.Fatalf("stream %d was reset with %v", f.StreamID, f.ErrCode)
		case *http2.GoAwayFrame:
			t.Fatalf("connection went away with %v: %s", f.ErrCode, f.DebugData())
		}
	}
}

// TestH2CEnabledGatesCleartextHTTP2 proves the h2c_enabled knob is the gate on
// cleartext HTTP/2, in both directions.
//
// This cannot be a corpus case: the assertion is about how the connection is
// established, and the corpus reaches mockulus through an HTTP/1.1 client that
// has already made that choice.
func TestH2CEnabledGatesCleartextHTTP2(t *testing.T) {
	off := start(t, noDrainEnv())
	on := start(t, h2cEnv())

	on.registerStub(t, h2cStub("/e2e/cfg-h2c-enabled/prior-knowledge", "served over h2c"))
	on.registerStub(t, h2cStub("/e2e/cfg-h2c-enabled/upgraded", "upgraded to h2c"))

	t.Run("refused by default", func(t *testing.T) {
		tr := h2cTransport()
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 15 * time.Second}

		resp, err := client.Get(off.mockURL("/e2e/cfg-h2c-enabled/prior-knowledge"))
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("a prior-knowledge HTTP/2 connection was accepted (proto %s) on an instance "+
				"with h2c off; h2c_enabled defaults to false (deviation #15)", resp.Proto)
		}
		// The default instance is an HTTP/1.1 listener, so the preface is
		// answered with something that is not a SETTINGS frame and the client
		// cannot get a stream open. Which error it is depends on how far the
		// handshake got; that it failed at all is the behavior.
		t.Logf("h2c off: prior-knowledge HTTP/2 rejected with %v", err)
	})

	t.Run("prior knowledge accepted when enabled", func(t *testing.T) {
		tr := h2cTransport()
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 15 * time.Second}

		resp, err := client.Get(on.mockURL("/e2e/cfg-h2c-enabled/prior-knowledge"))
		if err != nil {
			t.Fatalf("h2c on: prior-knowledge HTTP/2 failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.Proto != "HTTP/2.0" {
			t.Errorf("proto = %q, want HTTP/2.0: the response did not arrive over h2c", resp.Proto)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "served over h2c" {
			t.Errorf("body = %q, want %q", body, "served over h2c")
		}
	})

	t.Run("upgrade from HTTP/1.1 accepted when enabled", func(t *testing.T) {
		// The other half of h2c: an ordinary HTTP/1.1 request that asks to be
		// switched (RFC 7540 §3.2). Go's client cannot send one, so the request
		// goes out by hand and the answer is read frame by frame.
		conn, err := net.Dial("tcp", on.mockAddr)
		if err != nil {
			t.Fatalf("dial mock port: %v", err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}

		// SETTINGS_INITIAL_WINDOW_SIZE = 65535, which is what the upgrade
		// carries in place of the client's first SETTINGS frame.
		settings := base64.RawURLEncoding.EncodeToString([]byte{0x00, 0x04, 0x00, 0x00, 0xff, 0xff})
		_, err = fmt.Fprintf(conn,
			"GET /e2e/cfg-h2c-enabled/upgraded HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Connection: Upgrade, HTTP2-Settings\r\n"+
				"Upgrade: h2c\r\n"+
				"HTTP2-Settings: %s\r\n"+
				"\r\n", on.mockAddr, settings)
		if err != nil {
			t.Fatalf("write upgrade request: %v", err)
		}

		br, status, headers := readStatusLine(t, conn)
		if status != "HTTP/1.1 101 Switching Protocols" {
			t.Fatalf("upgrade answered %q, want 101 Switching Protocols", status)
		}
		if got := headers.Get("Upgrade"); got != "h2c" {
			t.Fatalf("Upgrade header = %q, want h2c", got)
		}

		// From here the socket is an HTTP/2 connection and the request that
		// asked for the upgrade is stream 1.
		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			t.Fatalf("write client preface: %v", err)
		}
		fr := http2.NewFramer(conn, br)
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		if err := fr.WriteSettings(); err != nil {
			t.Fatalf("write settings: %v", err)
		}

		gotStatus, gotBody := readStreamOne(t, fr)
		if gotStatus != "200" {
			t.Errorf(":status = %q, want 200 on the upgraded stream", gotStatus)
		}
		if gotBody != "upgraded to h2c" {
			t.Errorf("body = %q, want %q", gotBody, "upgraded to h2c")
		}
	})
}

// TestFaultFidelityDegradesOverH2C proves deviation #15: fault injection is
// byte-faithful on HTTP/1.1, and over HTTP/2 it degrades to a stream reset.
//
// The two halves have to be observed at different layers — a socket that ends
// with nothing on it, and a stream that is reset while its connection keeps
// working — which is exactly what a corpus case cannot say.
func TestFaultFidelityDegradesOverH2C(t *testing.T) {
	// h2c is on so both protocols reach the same instance and the same stub;
	// the difference measured is the protocol, nothing else. The listener logs
	// at debug, which is where a handler panic would surface.
	env := h2cEnv()
	env["MOCKULUS_LOG_LEVEL"] = "debug"
	inst := start(t, env)

	const (
		faultPath = "/e2e/dev-deviation-15/empty-response"
		okPath    = "/e2e/dev-deviation-15/healthy"
	)
	inst.registerStub(t, fmt.Sprintf(
		`{"request":{"method":"GET","url":%q},"response":{"fault":"EMPTY_RESPONSE"}}`, faultPath))
	inst.registerStub(t, h2cStub(okPath, "still serving"))

	// HTTP/1.1: byte-faithful. EMPTY_RESPONSE means the connection is taken
	// over and closed with nothing written, so a raw socket sees zero bytes and
	// then end-of-file — connection-level breakage, no status to read.
	conn, err := net.Dial("tcp", inst.mockAddr)
	if err != nil {
		t.Fatalf("dial mock port: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", faultPath, inst.mockAddr); err != nil {
		t.Fatalf("write request: %v", err)
	}
	got, err := io.ReadAll(conn)
	if len(got) != 0 {
		t.Fatalf("HTTP/1.1 EMPTY_RESPONSE put %d bytes on the wire (%q); the fault is meant to be "+
			"byte-faithful, which means none", len(got), got)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		// A reset instead of a clean close is still connection-level breakage,
		// so it is worth recording rather than failing on.
		t.Logf("HTTP/1.1 connection ended with %v", err)
	}

	// h2c: there is no connection to hijack, so the same stub degrades to a
	// stream reset. Both requests go down one connection this test dialed
	// itself, which is what makes "stream" rather than "connection" provable.
	tr := h2cTransport()
	defer tr.CloseIdleConnections()

	h2conn, err := net.Dial("tcp", inst.mockAddr)
	if err != nil {
		t.Fatalf("dial mock port for h2c: %v", err)
	}
	defer func() { _ = h2conn.Close() }()

	cc, err := tr.NewClientConn(h2conn)
	if err != nil {
		t.Fatalf("open h2c connection: %v", err)
	}

	faultReq, err := http.NewRequest(http.MethodGet, inst.mockURL(faultPath), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := cc.RoundTrip(faultReq)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("EMPTY_RESPONSE over h2c returned %d; a fault must never look like a served "+
			"response, it degrades to a stream reset (deviation #15)", resp.StatusCode)
	}
	var streamErr http2.StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("EMPTY_RESPONSE over h2c failed with %T (%v); deviation #15 says it degrades to a "+
			"stream reset", err, err)
	}
	t.Logf("h2c: EMPTY_RESPONSE degraded to RST_STREAM %v on stream %d", streamErr.Code, streamErr.StreamID)

	// The connection outliving the reset is the difference from HTTP/1.1: there
	// the socket is gone, here only the stream is.
	okReq, err := http.NewRequest(http.MethodGet, inst.mockURL(okPath), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	okResp, err := cc.RoundTrip(okReq)
	if err != nil {
		t.Fatalf("the h2c connection did not survive the fault: %v; a stream reset must not take "+
			"the connection with it", err)
	}
	defer func() { _ = okResp.Body.Close() }()

	if okResp.Proto != "HTTP/2.0" || okResp.StatusCode != http.StatusOK {
		t.Fatalf("follow-up on the same h2c connection = %s %d, want HTTP/2.0 200",
			okResp.Proto, okResp.StatusCode)
	}
	body, _ := io.ReadAll(okResp.Body)
	if string(body) != "still serving" {
		t.Errorf("body = %q, want %q", body, "still serving")
	}

	// The reset has to be the deliberate degradation, not a crash that happens
	// to look like one: an unguarded hijack over h2c would produce the same
	// RST_STREAM by panicking, and net/http logs that where ErrAbortHandler is
	// logged nowhere.
	for _, line := range inst.logs() {
		if strings.Contains(line, "panic serving") {
			t.Errorf("the h2c fault path panicked instead of degrading deliberately: %s", line)
		}
	}
}
