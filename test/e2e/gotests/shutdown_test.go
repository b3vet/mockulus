// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

// The drain window only exists between SIGTERM and process exit, which is why
// this cannot be a corpus case: cases share a pooled instance per variant, and
// nothing in a YAML step can signal a process — signalling the shared one would
// end the run for every other case using it.
//
// What SPEC §4.5 orders is the point. A pod being deleted has to tell
// Kubernetes to stop sending it traffic *before* it stops serving the traffic
// it already has, because endpoint removal takes time to propagate. A drain
// that stops serving is therefore exactly as wrong as one that stays ready, and
// both halves are asserted here.

// observation is one probe: what came back, and how long after the signal.
type observation struct {
	at     time.Duration
	status int
	body   string
	err    error
}

// sampleUntil probes continuously until the deadline. Continuously is the
// contract: a single probe at each end of the window could not tell a listener
// that stayed up from one that dropped out in the middle and came back.
func sampleUntil(t0, deadline time.Time, every time.Duration, probe func() (int, string, error)) []observation {
	var out []observation
	for time.Now().Before(deadline) {
		status, body, err := probe()
		out = append(out, observation{at: time.Since(t0), status: status, body: body, err: err})
		time.Sleep(every)
	}
	return out
}

// TestShutdownDrainKeepsServingWhileUnready pins the ordering of SPEC §4.5:
// readiness drops first, the drain window elapses with the listeners still
// serving, and only then does the process go.
func TestShutdownDrainKeepsServingWhileUnready(t *testing.T) {
	const drain = 3 * time.Second

	m := start(t, map[string]string{
		"MOCKULUS_SHUTDOWN_DRAIN":   drain.String(),
		"MOCKULUS_SHUTDOWN_TIMEOUT": "15s",
	})
	m.registerStub(t, `{
	  "request": {"method": "GET", "urlPath": "/e2e/gotests-shutdown-drain/hello"},
	  "response": {"status": 200, "body": "world"}
	}`)

	stub := m.mockURL("/e2e/gotests-shutdown-drain/hello")
	readyz := m.adminURL("/readyz")

	if status, _, err := plainGet(readyz); err != nil || status != http.StatusOK {
		t.Fatalf("/readyz before SIGTERM: status %d, err %v; want 200", status, err)
	}

	sent := time.Now()
	m.signal(t, syscall.SIGTERM)

	// The mock port is sampled from the instant the signal is sent, over most
	// of the drain window. The tail is left alone deliberately: the listener is
	// supposed to close at the end of it, and a probe racing that close would
	// assert nothing about the window.
	window := sent.Add(drain * 4 / 5)
	served := make(chan []observation, 1)
	go func() {
		served <- sampleUntil(sent, window, 20*time.Millisecond, func() (int, string, error) {
			return plainGet(stub)
		})
	}()

	// Readiness is sampled from the moment it first drops rather than from the
	// signal, because signal delivery is not instantaneous and a probe that
	// wins that race would see the pre-SIGTERM 200 and prove nothing. How long
	// the drop took is asserted separately, below.
	dropped, ok := awaitNotReady(readyz, 2*time.Second)
	if !ok {
		t.Fatal("/readyz never answered 503 after SIGTERM: a terminating pod that stays ready keeps receiving new traffic")
	}
	readiness := sampleUntil(sent, window, 20*time.Millisecond, func() (int, string, error) {
		return plainGet(readyz)
	})
	stubbed := <-served

	// Readiness drops *first* — before the drain window, not at the end of it.
	if dropped > time.Second {
		t.Errorf("/readyz took %s to answer 503; readiness must drop at the head of the sequence, not after the drain", dropped)
	}

	// ...and stays down for the whole window. A single flap back to 200 would
	// put the pod back in its Service.
	if len(readiness) < 20 {
		t.Fatalf("only %d readiness probes over the drain window; that is not continuous enough to prove anything", len(readiness))
	}
	for _, o := range readiness {
		// 503, from every probe, all the way through: http.StatusServiceUnavailable.
		if o.err != nil || o.status != http.StatusServiceUnavailable {
			t.Fatalf("/readyz at %s into the drain: status %d, err %v; want 503 continuously", o.at, o.status, o.err)
		}
	}

	// The other half: not-ready must not mean not-serving. Traffic already
	// pointed at this pod keeps being answered for the whole window.
	if len(stubbed) < 20 {
		t.Fatalf("only %d mock requests over the drain window; that is not continuous enough to prove anything", len(stubbed))
	}
	for _, o := range stubbed {
		if o.err != nil || o.status != http.StatusOK || o.body != "world" {
			t.Fatalf("the stub at %s into the drain: status %d, body %q, err %v; want 200 world — "+
				"a drain that stops serving cuts off the in-flight traffic it exists to protect",
				o.at, o.status, o.body, o.err)
		}
	}

	code, exited := m.awaitExit(drain + 15*time.Second)
	if !exited {
		t.Fatal("mockulus never exited after SIGTERM")
	}
	if elapsed := time.Since(sent); elapsed < drain {
		t.Errorf("mockulus exited %s after SIGTERM, inside its %s drain window: the window was not honoured", elapsed, drain)
	}
	if code != 0 {
		t.Errorf("exit code %d, want 0: an orderly shutdown is not a failure (SPEC §4.5)", code)
	}
}

// TestShutdownTimeoutBoundsAnOpenConnection pins the other half of the pair:
// shutdown_timeout is what stops a client from holding a terminating pod open.
func TestShutdownTimeoutBoundsAnOpenConnection(t *testing.T) {
	m := start(t, map[string]string{
		// No drain: this case is about the bound on closing the listeners, and a
		// drain window would only add a constant to what is being measured.
		"MOCKULUS_SHUTDOWN_DRAIN":   "0s",
		"MOCKULUS_SHUTDOWN_TIMEOUT": "500ms",
	})

	// A connection that is open but has sent nothing is the one that hangs a
	// naive shutdown: net/http cannot treat it as idle, since the client may be
	// about to send a request, so Shutdown waits on it. Without a bound a
	// terminating pod would sit here; shutdown_timeout is the bound.
	conn, err := net.DialTimeout("tcp", m.mockAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial the mock port: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sent := time.Now()
	m.signal(t, syscall.SIGTERM)

	code, exited := m.awaitExit(4 * time.Second)
	if !exited {
		t.Fatal("mockulus outlived its 500ms shutdown_timeout by seconds while one connection sat open; " +
			"the timeout must bound the wait, or a single idle client delays every rollout")
	}
	if elapsed := time.Since(sent); elapsed > 3*time.Second {
		t.Errorf("shutdown took %s with a 500ms shutdown_timeout; the bound is not being applied", elapsed)
	}

	// The exit code is recorded rather than asserted. Reaching the timeout is
	// the configured outcome — the operator asked for the connections to be cut
	// after 500ms — but it currently surfaces as exit 1, which reads to
	// Kubernetes as a crash rather than as the bound doing its job. Pinning
	// either answer here would pin an open question; the orderly path, which is
	// not ambiguous, has its exit code asserted in the drain case above.
	t.Logf("exit code after a bounded shutdown: %d", code)
}

// awaitNotReady polls until readiness drops, reporting how long that took.
func awaitNotReady(url string, within time.Duration) (time.Duration, bool) {
	start := time.Now()
	for time.Since(start) < within {
		if status, _, err := plainGet(url); err == nil && status == http.StatusServiceUnavailable {
			return time.Since(start), true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0, false
}
