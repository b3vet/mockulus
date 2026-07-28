// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Scheduling is what makes `requires:` mean anything: a case that asked for a
// store and got the memory driver would pass while proving nothing.

func TestCasesScheduleOntoTheCheapestSatisfyingTopology(t *testing.T) {
	for _, c := range []struct {
		name     string
		requires []string
		want     string
	}{
		{"nothing declared", nil, TopologyT1},
		{"exclusive is a claim on the deployment, not a shape", []string{CapabilityExclusive}, TopologyT1},
		{"a store", []string{CapabilityCouchbase}, TopologyT2},
		{"replicas", []string{CapabilityCouchbase, CapabilityMultiPod}, TopologyT3},
		// multi-pod implies a shared store: three replicas of the memory driver
		// are three deployments, not one.
		{"replicas without naming the store", []string{CapabilityMultiPod}, TopologyT3},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := topologyFor(&Case{ID: "x", Requires: c.requires}); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestPodSelectorResolvesToOneReplica(t *testing.T) {
	dep := &Deployment{
		Topology:  TopologyT3,
		MockAddr:  "http://lb-mock",
		AdminAddr: "http://lb-admin",
		pods: []*Instance{
			{MockAddr: "http://pod0-mock", AdminAddr: "http://pod0-admin"},
			{MockAddr: "http://pod1-mock", AdminAddr: "http://pod1-admin"},
		},
	}

	// An unpinned step goes through the load balancer. A case that pins nothing
	// is asserting that any replica will do, so sending it to one would be the
	// harness quietly making the weaker claim on the case's behalf.
	for _, spec := range []string{"", PodAny} {
		mock, admin, err := dep.Pod(spec)
		if err != nil {
			t.Fatalf("pod %q: %v", spec, err)
		}
		if mock != dep.MockAddr || admin != dep.AdminAddr {
			t.Errorf("pod %q went to %s/%s, want the load balancer", spec, mock, admin)
		}
	}

	mock, admin, err := dep.Pod("1")
	if err != nil {
		t.Fatalf("pod 1: %v", err)
	}
	// Both listeners of one pin have to be the same process, or a case that
	// writes to pod 1's admin API and reads pod 1's mock port proves nothing.
	if mock != "http://pod1-mock" || admin != "http://pod1-admin" {
		t.Errorf("pod 1 resolved to %s/%s", mock, admin)
	}

	if _, _, err := dep.Pod("2"); err == nil {
		t.Error("a replica the deployment does not have must be an error, not a wrap-around")
	}
	if _, _, err := dep.Pod("first"); err == nil {
		t.Error("an unparseable selector must be an error")
	}
}

// A case pinning a replica in a single-pod topology cannot do what it says it
// does, and the run finding out three steps in reports the missing replica
// rather than the missing declaration.
func TestCaseValidationCatchesPinsThatCannotResolve(t *testing.T) {
	pinned := func(requires []string) *Case {
		return &Case{
			ID: "x", Behaviors: []string{"B-X"}, WM: WMNotApplicable, Requires: requires,
			Steps: []Step{{
				Request: &HTTPStep{Pod: "1", Method: "GET", Path: "/e2e/x/a"},
				Expect:  &Expect{Status: 200},
			}},
		}
	}

	if err := pinned(nil).validate(); err == nil {
		t.Error("pinning pod 1 without requiring multi-pod must fail at load")
	}
	if err := pinned([]string{CapabilityCouchbase, CapabilityMultiPod}).validate(); err != nil {
		t.Errorf("pinning pod 1 in a multi-pod case is exactly what pod: is for: %v", err)
	}
}

// A capability the runner does not know would schedule the case onto T1 and let
// it pass there — accept-and-behave-differently, from a typo.
func TestCaseValidationRejectsAnUnknownCapability(t *testing.T) {
	c := &Case{
		ID: "x", Behaviors: []string{"B-X"}, WM: WMNotApplicable,
		Requires: []string{"couchbse"},
		Steps: []Step{{
			Request: &HTTPStep{Method: "GET", Path: "/e2e/x/a"},
			Expect:  &Expect{Status: 200},
		}},
	}
	if err := c.validate(); err == nil {
		t.Fatal("an unknown capability must fail the case at load")
	}
}

// Taking the store away takes it away from every deployment sharing it, so the
// declarations that keep the outage inside one case have to be checked at load.
// Discovered any later, the failures land on the cases it broke.
func TestStoreChoreographyIsRejectedWithoutItsDeclarations(t *testing.T) {
	outage := func(requires []string) *Case {
		return &Case{
			ID: "x", Behaviors: []string{"B-X"}, WM: WMNotApplicable, Requires: requires,
			Steps: []Step{
				{StopStore: true},
				{Request: &HTTPStep{Method: "GET", Path: "/e2e/x/a"}, Expect: &Expect{Status: 200}},
				{StartStore: true},
			},
		}
	}

	for _, c := range []struct {
		name     string
		requires []string
	}{
		{"nothing declared", nil},
		{"a store but no claim on the run", []string{CapabilityCouchbase}},
		{"a claim on the run but no store to pause", []string{CapabilityExclusive}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := outage(c.requires).validate(); err == nil {
				t.Error("a case pausing the shared store must fail at load without both declarations")
			}
		})
	}

	both := []string{CapabilityCouchbase, CapabilityExclusive}
	if err := outage(both).validate(); err != nil {
		t.Errorf("both declared is what a degraded-mode case looks like: %v", err)
	}
}

// Per-request round-robin is what keeps "any replica serves any request" under
// continuous test. Per-connection balancing would pin a case to one replica for
// its whole run and let a broken replica through.
func TestLoadBalancerSpreadsRequestsOverEveryReplica(t *testing.T) {
	backends := make([]string, 0, 3)
	hits := map[string]int{}
	for i := range 3 {
		name := fmt.Sprintf("replica-%d", i)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		}))
		defer srv.Close()
		backends = append(backends, srv.URL)
		hits[name] = 0
	}

	proxy, err := StartLBProxy(backends, http.DefaultTransport)
	if err != nil {
		t.Fatalf("start the load balancer: %v", err)
	}
	defer func() { _ = proxy.Stop() }()

	order := make([]string, 0, 6)
	for range 6 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxy.Addr+"/anything", nil)
		if err != nil {
			t.Fatalf("build the request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("through the load balancer: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		order = append(order, string(body))
		hits[string(body)]++
	}

	for name, n := range hits {
		if n != 2 {
			t.Errorf("%s served %d of 6 requests, want an even share: %v", name, n, order)
		}
	}
	if order[0] != "replica-0" {
		t.Errorf("the first request went to %s; a transcript is easier to read when it starts at replica 0", order[0])
	}
}
