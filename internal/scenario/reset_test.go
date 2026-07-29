// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"errors"
	"testing"
)

// A reset deletes the documents rather than writing Started into each of them,
// which is what makes it work for a scenario no request has ever touched and
// what leaves the collection empty afterwards. Every scenario reading Started
// again is the observable contract a suite depends on between cases
// (SPEC §9.4).
func TestResetPutsEveryScenarioBackToStarted(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "paid")
	seed(t, st.Store, "signup", "email verified")

	if err := client.ResetAll(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}

	for _, name := range []string{"checkout", "signup"} {
		state, err := client.State(context.Background(), name)
		if err != nil {
			t.Fatalf("reading %q after the reset: %v", name, err)
		}
		if state != Started {
			t.Errorf("%q reads as %q after a reset, want %q", name, state, Started)
		}
		if _, exists := storedState(t, st.Store, name); exists {
			t.Errorf("%q still has a state document after the reset", name)
		}
	}
}

// A reset that could not delete must say so. Answering 200 to a reset that did
// not happen is the worst shape of this failure: the next case walks a flow it
// believes is at Started and fails at whatever step first disagrees, which
// reads as a mock bug rather than as the store outage it is.
func TestAResetThatFailedIsReportedRatherThanSwallowed(t *testing.T) {
	st := newControlledStore()
	broken := errors.New("store unavailable: delete rejected")
	st.deleteErr = broken
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "paid")

	if err := client.ResetAll(context.Background()); !errors.Is(err, broken) {
		t.Fatalf("reset returned %v, want the store's own error", err)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q; the failed reset should have changed nothing", got.State)
	}
}

// The listing returns only what has been written. Scenario existence is derived
// from the stubs, not from this collection (SPEC §9.1), so a scenario nothing
// has moved yet has no entry here — the admin layer is what merges these states
// with the snapshot's definitions and fills in Started for the rest. Inventing
// an entry here would put a scenario in the listing that no stub defines.
func TestTheStateListingReturnsOnlyScenariosThatHaveMoved(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "paid")

	states, err := client.States(context.Background())
	if err != nil {
		t.Fatalf("list states: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("the listing holds %d entries, want only the scenario that has moved", len(states))
	}
	if got := states["checkout"].State; got != "paid" {
		t.Errorf("checkout is listed as %q, want %q", got, "paid")
	}
	if _, listed := states["signup"]; listed {
		t.Error("a scenario nothing has written is in the listing")
	}
}

// An empty collection is a valid answer, not a missing one: it is what every
// deployment looks like before the first stateful request and what a reset
// leaves behind.
func TestTheStateListingIsEmptyBeforeAnythingHasMoved(t *testing.T) {
	client, _ := newTestClient(newControlledStore())

	states, err := client.States(context.Background())
	if err != nil {
		t.Fatalf("listing an untouched deployment failed: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("the listing holds %d entries before anything has moved, want none", len(states))
	}
}

// A listing that could not be read must fail rather than return the empty map,
// which is indistinguishable from "nothing has moved yet" and would have the
// admin API report every scenario as Started during a store outage.
func TestAStateListingThatFailedIsReportedRatherThanReadAsEmpty(t *testing.T) {
	st := newControlledStore()
	broken := errors.New("store unavailable: query rejected")
	st.listErr = broken
	client, _ := newTestClient(st)

	states, err := client.States(context.Background())
	if !errors.Is(err, broken) {
		t.Fatalf("the listing returned %v, want the store's own error", err)
	}
	if states != nil {
		t.Errorf("the failed listing returned %d entries; any answer here would be read as the truth", len(states))
	}
}

// Setting a state is unconditional by design: it backs an admin PUT, whose
// whole purpose is to put a flow somewhere regardless of where it currently is.
// A compare-and-swap here would make the endpoint fail against exactly the
// contended scenario an operator is trying to rescue.
func TestSettingAStateOverwritesWhateverWasThere(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "paid")

	if err := client.SetState(context.Background(), "checkout", "cart filled"); err != nil {
		t.Fatalf("set state: %v", err)
	}

	got, exists := storedState(t, st.Store, "checkout")
	if !exists {
		t.Fatal("setting a state wrote no document")
	}
	if got.State != "cart filled" {
		t.Errorf("the scenario is at %q, want %q", got.State, "cart filled")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("the written document carries no update time")
	}
}

// The state a scenario is put into is validated against the states the stubs
// actually declare, and that validation belongs to the admin layer, which is
// the only place that can see the snapshot. This client writes what it is
// given, including for a scenario no document exists for yet — the endpoint is
// how a suite jumps a flow to a later step without walking it.
func TestSettingAStateCreatesTheDocumentForAScenarioThatHasNotMoved(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)

	if err := client.SetState(context.Background(), "checkout", "paid"); err != nil {
		t.Fatalf("set state: %v", err)
	}

	state, err := client.State(context.Background(), "checkout")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != "paid" {
		t.Errorf("the scenario reads as %q after being set, want %q", state, "paid")
	}
}

// A write that failed must be reported, so the endpoint answers with the store
// error rather than 200. A suite that was told its flow is at "paid" and finds
// it at Started has no way to tell that from a mock that lost the state.
func TestSettingAStateReportsAFailedWrite(t *testing.T) {
	st := newControlledStore()
	broken := errors.New("store unavailable: upsert rejected")
	st.upsertErr = broken
	client, _ := newTestClient(st)

	if err := client.SetState(context.Background(), "checkout", "paid"); !errors.Is(err, broken) {
		t.Fatalf("set state returned %v, want the store's own error", err)
	}
	if _, exists := storedState(t, st.Store, "checkout"); exists {
		t.Error("the failed write left a document behind")
	}
}
