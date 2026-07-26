// SPDX-License-Identifier: Apache-2.0

package gotests

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Scenario transitions are compare-and-swap, and the two series that expose
// contention only move when a transition actually loses its race (SPEC §9.3).
// A corpus case cannot produce one: its steps run one after another, so every
// transition it drives has the scenario to itself and both counters stay at
// nought however many times it walks the flow. Contention means requests
// overlapping inside the read-modify-write of a single state document, which
// needs goroutines — and owning the process, so the counters can also be held
// to the half a shared instance makes unassertable: that they do not move when
// nothing is racing.

const (
	contentionURL  = "/e2e/scenario-cas-contention/order"
	contentionOpen = "basket open"
	contentionHeld = "basket held"
)

// A two-state cycle rather than a walk with an end. Every request matches a
// stub declaring newScenarioState, so every request transitions and every
// request is a chance to lose one — where a flow that finishes in a terminal
// state transitions once and then stops contending, which is what makes even a
// wide burst aimed at it read as uncontended.
var contentionStubs = []string{`{
  "id": "10040001-0000-4000-8000-000000000001",
  "scenarioName": "scenario-cas-contention",
  "requiredScenarioState": "Started",
  "newScenarioState": "held",
  "request": {"method": "GET", "urlPath": "/e2e/scenario-cas-contention/order"},
  "response": {"status": 200, "body": "basket open"}}`, `{
  "id": "10040001-0000-4000-8000-000000000002",
  "scenarioName": "scenario-cas-contention",
  "requiredScenarioState": "held",
  "newScenarioState": "Started",
  "request": {"method": "GET", "urlPath": "/e2e/scenario-cas-contention/order"},
  "response": {"status": 200, "body": "basket held"}}`}

const (
	// The window a transition can lose in is the microsecond between reading
	// the state document and writing it back, so what decides whether the race
	// happens is how many requests are inside it at once. Measured on the
	// reference rig, this load leaves hundreds of increments on both series and
	// takes under 50 ms.
	contendingClients  = 32
	contendingRequests = 4000
	// Contention is a race, so it is driven until it happens rather than
	// assumed to happen first time. The bound is what keeps "until it happens"
	// from being "for ever": a build where the counters cannot move fails here
	// instead of hanging.
	contentionRounds = 20
	// An uncontended walk long enough to be a claim of its own: the cycle turns
	// over on every call, so this is forty transitions with nobody to lose to.
	uncontendedWalk = 40
)

func TestScenarioContentionCountsRetriesAndConflicts(t *testing.T) {
	// The server's parallelism is the premise of the whole test, so it is
	// pinned rather than inherited from the machine. On a single-processor
	// runtime nothing preempts a transition between its read and its write,
	// every one of them completes before the next begins, and a correct product
	// would fail here for want of a second processor to race on.
	m := start(t, map[string]string{"GOMAXPROCS": "8"})
	for _, mapping := range contentionStubs {
		m.registerStub(t, mapping)
	}

	// Exported from the start rather than springing into existence with the
	// first conflict: an operator cannot alert on contention through a series
	// that is absent until the incident it is meant to catch.
	if got := m.counters(t, "mockulus_scenario_cas_retries_total",
		"mockulus_scenario_transition_conflicts_total"); got["mockulus_scenario_cas_retries_total"] != 0 ||
		got["mockulus_scenario_transition_conflicts_total"] != 0 {
		t.Fatalf("fresh instance: retries=%v conflicts=%v, want both nought",
			got["mockulus_scenario_cas_retries_total"],
			got["mockulus_scenario_transition_conflicts_total"])
	}

	// An uncontended walk first, down one connection so no two requests can
	// overlap. Every one of these transitions and not one of them retries, so a
	// deployment whose scenarios are used the ordinary way — one test driving
	// one flow — reads nought on both series, and a dashboard showing anything
	// else is showing real contention rather than ordinary use.
	for i := range uncontendedWalk {
		want := contentionOpen
		if i%2 == 1 {
			want = contentionHeld
		}
		m.expectServed(t, contentionURL, want)
	}
	if got := m.counters(t, "mockulus_scenario_cas_retries_total",
		"mockulus_scenario_transition_conflicts_total"); got["mockulus_scenario_cas_retries_total"] != 0 ||
		got["mockulus_scenario_transition_conflicts_total"] != 0 {
		t.Fatalf("after %d uncontended transitions: retries=%v conflicts=%v, want both nought",
			uncontendedWalk, got["mockulus_scenario_cas_retries_total"],
			got["mockulus_scenario_transition_conflicts_total"])
	}

	for round := 1; ; round++ {
		m.hammer(t, contentionURL, contendingClients, contendingRequests)

		got := m.counters(t, "mockulus_scenario_cas_retries_total",
			"mockulus_scenario_transition_conflicts_total")
		if got["mockulus_scenario_cas_retries_total"] > 0 &&
			got["mockulus_scenario_transition_conflicts_total"] > 0 {
			break
		}
		if round == contentionRounds {
			t.Fatalf("%d rounds of %d requests from %d clients left mockulus_scenario_cas_retries_total=%v and mockulus_scenario_transition_conflicts_total=%v, want both above nought",
				contentionRounds, contendingRequests, contendingClients,
				got["mockulus_scenario_cas_retries_total"],
				got["mockulus_scenario_transition_conflicts_total"])
		}
	}

	// The counters are the evidence, but the point is what they are counting:
	// the losers left the document alone. After thousands of racing writes the
	// machine is still on its cycle — the stored state is one of the two its
	// stubs define, not a third nobody wrote and not one a loser put back — and
	// the next call still advances it to the other.
	first := m.serveBody(t, contentionURL)
	if first != contentionOpen && first != contentionHeld {
		t.Fatalf("after contention the scenario serves %q, want %q or %q",
			first, contentionOpen, contentionHeld)
	}
	if second := m.serveBody(t, contentionURL); second == first {
		t.Fatalf("after contention the cycle stopped turning over: two calls both served %q", first)
	}
}

// hammer drives total requests at one path from clients goroutines that keep
// going until the count is used up, and fails the test unless every one of them
// was served.
//
// That last part is the client-visible half of SPEC §9.3: a transition that
// loses its race is skipped and counted, never turned into an error, so a
// caller cannot tell a lost race from an ordinary hit. Which body each caller
// saw is left unasserted on purpose — under contention several callers
// legitimately see the same step, which is inherent to a state machine shared
// by racing clients and not something CAS is there to prevent.
func (m *mockulus) hammer(t *testing.T, path string, clients, total int) {
	t.Helper()

	// A client each, so the requests actually overlap. Sharing one would put
	// them through a connection pool that keeps two idle connections per host
	// by default, and the load would queue rather than race.
	var issued atomic.Int64
	failures := make(chan string, clients)

	var wg sync.WaitGroup
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{}}
			for issued.Add(1) <= int64(total) {
				status, _, err := serve(c, m.mockURL(path))
				if err != nil {
					failures <- fmt.Sprintf("request failed: %v", err)
					return
				}
				if status != http.StatusOK {
					failures <- fmt.Sprintf("status %d, want 200 — a lost transition must not fail a response", status)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)

	// Reported from the test goroutine: t.Fatalf from the ones above would stop
	// only the goroutine that called it and leave the rest writing into a
	// finished test.
	for failure := range failures {
		t.Fatal(failure)
	}
}

// expectServed asserts what the gated URL answers right now, which is where a
// scenario's state is observable without asking the admin API what it thinks.
func (m *mockulus) expectServed(t *testing.T, path, want string) {
	t.Helper()

	if got := m.serveBody(t, path); got != want {
		t.Fatalf("GET %s served %q, want %q", path, got, want)
	}
}

// serveBody issues one request down the shared client and returns its body,
// failing the test on anything but a 200.
func (m *mockulus) serveBody(t *testing.T, path string) string {
	t.Helper()

	status, body, err := serve(harnessClient, m.mockURL(path))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, status)
	}
	return body
}

// serve issues one request and returns its status and body without touching
// *testing.T, so the goroutines of a load phase can call it.
func serve(client *http.Client, url string) (int, string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// counters reads several unlabelled series off one /metrics scrape.
//
// One scrape rather than one per series: a retry and the conflict it leads to
// are two increments of the same transition, and two scrapes could fall between
// them and report a state the product was never in.
func (m *mockulus) counters(t *testing.T, names ...string) map[string]float64 {
	t.Helper()

	resp, err := harnessClient.Get(m.adminURL("/metrics"))
	if err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}

	values := make(map[string]float64, len(names))
	for _, line := range strings.Split(string(body), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || !wanted[name] {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("%s: value %q is not a number", name, value)
		}
		values[name] = v
	}

	for _, name := range names {
		if _, found := values[name]; !found {
			t.Fatalf("%s is not exposed on /metrics:\n%s", name, body)
		}
	}
	return values
}
