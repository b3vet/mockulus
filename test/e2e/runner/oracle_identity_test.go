// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// oracleAnswering stands up a server that answers /__admin/version with the
// given body, which is the one question that settles what is on a port.
func oracleAnswering(t *testing.T, versionBody string) *WireMock {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/__admin/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(versionBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &WireMock{Addr: srv.URL, client: &http.Client{Timeout: 5 * time.Second}}
}

// TestOracleIdentityRefusesAMockulus reproduces the incident this check exists
// for.
//
// A stray mockulus process was once answering on the port a probe took to be
// the oracle's, and a batch of confident, wrong findings was recorded from it
// before anyone asked what was actually listening. Every one of those findings
// was mockulus agreeing with itself. The tell is in the answer: mockulus
// reports `guessedWireMockVersion` beside its own version, which is what
// finally resolved it and is what the assertion looks for.
func TestOracleIdentityRefusesAMockulus(t *testing.T) {
	wm := oracleAnswering(t,
		`{"goVersion":"go1.25.4","guessedWireMockVersion":"3.x-subset","version":"v1.1.0"}`)

	err := wm.assertIdentity(t.Context(), "wiremock/wiremock:3.13.2")
	if err == nil {
		t.Fatal("a mockulus answering as the oracle must be refused, not believed")
	}
	if !strings.Contains(err.Error(), "is a mockulus") {
		t.Errorf("the refusal should name what it found instead, got %q", err)
	}
}

// A real oracle is accepted, so the check above is identifying an impostor
// rather than refusing everything.
func TestOracleIdentityAcceptsWireMock(t *testing.T) {
	wm := oracleAnswering(t, `{"version":"3.13.2"}`)
	if err := wm.assertIdentity(t.Context(), "wiremock/wiremock:3.13.2"); err != nil {
		t.Errorf("the pinned WireMock must be accepted, got %v", err)
	}
}

// The reported version is deliberately not matched against the pinned tag — an
// image named by digest or by a floating alias would fail that for no gain —
// but something has to be there. A service that answers the endpoint and says
// nothing about itself cannot be confirmed as the oracle.
func TestOracleIdentityRefusesAnAnswerThatNamesNothing(t *testing.T) {
	for _, body := range []string{`{}`, `{"version":""}`, `not json at all`} {
		wm := oracleAnswering(t, body)
		if err := wm.assertIdentity(t.Context(), "wiremock/wiremock:3.13.2"); err == nil {
			t.Errorf("an oracle answering %q must not be confirmed", body)
		}
	}
}

// And a port with nothing on it is an error rather than a silent pass, which is
// what would let a run derive its expectations from no oracle at all.
func TestOracleIdentityRefusesADeadPort(t *testing.T) {
	wm := &WireMock{
		Addr:   "http://127.0.0.1:1",
		client: &http.Client{Timeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wm.assertIdentity(ctx, "wiremock/wiremock:3.13.2"); err == nil {
		t.Error("an unreachable oracle must be an error")
	}
}
