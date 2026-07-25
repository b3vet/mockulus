// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A pod whose store is unreachable at boot is the case an operator meets on the
// worst possible day: a cluster restart while Couchbase is still coming up.
//
// SPEC §4.4 and §4.6 answer it by separating the two health signals. The
// process stays LIVE — `/healthz` is 200, so Kubernetes does not shoot it — and
// stays UNREADY — `/readyz` is 503, so the Service sends it no traffic it
// cannot serve. It then retries forever rather than exiting, because a pod that
// exits enters CrashLoopBackOff and stops retrying at exactly the moment the
// store is about to come back.
//
// This is unreachable from a corpus case for a structural reason: the harness
// starts an instance by waiting for it to become ready, and this instance never
// does. There is also no startup summary line to read the ephemeral ports from,
// since that line is written after both listeners are serving — so the admin
// port has to be chosen in advance rather than discovered.

// reservePort takes a free port from the kernel and releases it, so the process
// under test can bind it. The window between release and bind is a race in
// principle; in practice the kernel does not immediately reuse a just-closed
// listening port, and the alternative — a hard-coded port — collides with
// whatever else is on the machine, which is the far more common failure.
func reservePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

func TestStoreUnreachableAtBootStaysLiveAndUnready(t *testing.T) {
	adminPort := reservePort(t)

	// A connstr pointing at an address nothing is listening on. The store is
	// therefore unreachable in the way that matters — reachable network, no
	// service — rather than unresolvable, which some clients fail differently.
	m := launch(t, map[string]string{
		"MOCKULUS_ADMIN_PORT":         fmt.Sprint(adminPort),
		"MOCKULUS_STORE":              "couchbase",
		"MOCKULUS_COUCHBASE_CONNSTR":  fmt.Sprintf("couchbase://127.0.0.1:%d", reservePort(t)),
		"MOCKULUS_COUCHBASE_USERNAME": "mockulus",
		"MOCKULUS_COUCHBASE_PASSWORD": "mockulus",
		"MOCKULUS_COUCHBASE_BUCKET":   "mockulus",
	})
	t.Cleanup(m.stop)

	admin := "http://127.0.0.1:" + fmt.Sprint(adminPort)
	client := &http.Client{Timeout: 5 * time.Second}

	// The admin listener binds before the store connects, which is the whole
	// point: without it there would be nothing to ask, and a pod stuck on a
	// store would be indistinguishable from one that had not started (§4.4).
	deadline := time.Now().Add(45 * time.Second)
	var live bool
	for time.Now().Before(deadline) {
		resp, err := client.Get(admin + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			live = resp.StatusCode == http.StatusOK
			if live {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !live {
		t.Fatalf("/healthz never answered 200 while the store was unreachable:\n%s",
			strings.Join(m.logs(), "\n"))
	}

	// Unready for as long as the store is away, and continuously so: a single
	// 503 could be a pod that had not finished starting, whereas a 503 that
	// holds is the contract. Liveness is re-checked alongside it, because the
	// pair is the behavior — 503 with a dead process is a CrashLoopBackOff, and
	// 200/200 would draw traffic the pod cannot serve.
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		resp, err := client.Get(admin + "/readyz")
		if err != nil {
			t.Fatalf("/readyz stopped answering, so the process did not stay live: %v", err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz answered %d, want 503 while the store is unreachable", code)
		}

		resp, err = client.Get(admin + "/healthz")
		if err != nil {
			t.Fatalf("/healthz stopped answering: %v", err)
		}
		liveCode := resp.StatusCode
		_ = resp.Body.Close()
		if liveCode != http.StatusOK {
			t.Fatalf("/healthz answered %d, want 200: an unreachable store must not fail liveness "+
				"or Kubernetes restarts a pod that is waiting correctly", liveCode)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// And it is still trying. Exiting would be the tempting implementation and
	// the wrong one, so this asserts the process is alive rather than merely
	// that it was a moment ago.
	if _, exited := m.awaitExit(500 * time.Millisecond); exited {
		t.Fatalf("the process exited instead of retrying:\n%s", strings.Join(m.logs(), "\n"))
	}

	// The retry is visible in the log, which is where an operator looks first.
	//
	// Waited for rather than sampled: the client's own connect attempt has to
	// time out before the first retry can be reported, so reading the buffer
	// once asserts how fast a network gives up rather than whether the retry
	// happens at all.
	if !m.awaitLog(20*time.Second, "retrying") {
		t.Errorf("nothing in the log says the store connection is being retried; "+
			"a pod that is unready for a reason nobody can see is a pod nobody can diagnose:\n%s",
			strings.Join(m.logs(), "\n"))
	}
}

// awaitLog reports whether a line containing the substring appears within the
// window.
func (m *mockulus) awaitLog(within time.Duration, substr string) bool {
	deadline := time.Now().Add(within)
	for {
		for _, line := range m.logs() {
			if strings.Contains(line, substr) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
