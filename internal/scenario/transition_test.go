// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
)

// errLostRace is what a driver returns when a compare-and-swap loses: the
// store's sentinel, wrapped with whatever context the driver adds. Both drivers
// produce this shape — the memory one from a stale Replace, the Couchbase one by
// mapping gocb's ErrCasMismatch and ErrDocumentExists onto it — and the sentinel
// rather than the wording is what the retry decision is allowed to read.
var errLostRace = fmt.Errorf(`scenario "checkout": %w`, store.ErrCASConflict)

// failFirst builds a write hook that fails the first n writes with err and lets
// everything after through.
func failFirst(n int, err error) func(attempt int) error {
	return func(attempt int) error {
		if attempt <= n {
			return err
		}
		return nil
	}
}

// failAlways builds a write hook that never lets a write through.
func failAlways(err error) func(attempt int) error {
	return func(int) error { return err }
}

func assertNoStoreCalls(t *testing.T, st *controlledStore) {
	t.Helper()
	gets, inserts, replaces, upserts, _ := st.counts()
	if gets|inserts|replaces|upserts != 0 {
		t.Errorf("the store saw %d gets, %d inserts, %d replaces and %d upserts, want none",
			gets, inserts, replaces, upserts)
	}
}

// A stub that gates on a state but does not declare a new one is the common
// shape of the middle of a flow, and it must cost nothing: no read, no write,
// no round trip. This is the "non-scenario requests pay zero KV ops" promise of
// P2 extended to the stubs that only read.
func TestAStubThatDeclaresNoNewStateNeverTouchesTheStore(t *testing.T) {
	for _, required := range []string{"", "cart filled"} {
		t.Run("required state "+strconv.Quote(required), func(t *testing.T) {
			st := newControlledStore()
			client, _ := newTestClient(st)

			if err := client.Transition(context.Background(), "checkout", required, ""); err != nil {
				t.Fatalf("a transition with no target reported an error: %v", err)
			}
			assertNoStoreCalls(t, st)
		})
	}
}

// A stub with no gate is asking for the scenario to end up somewhere whatever
// it was, so the write is unconditional. Running it through the CAS loop would
// buy a second round trip on the request path and could still abandon the write
// after three lost races — an outcome "unconditional" does not have. The read
// count is the assertion that matters: a get here would mean the loop was used.
func TestAnUngatedTransitionWritesOnceWithoutReadingFirst(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	if err := client.Transition(context.Background(), "checkout", "", "paid"); err != nil {
		t.Fatalf("an ungated transition failed: %v", err)
	}

	gets, inserts, replaces, upserts, _ := st.counts()
	if gets != 0 {
		t.Errorf("an ungated transition read the state %d times; it has nothing to compare against", gets)
	}
	if upserts != 1 || inserts != 0 || replaces != 0 {
		t.Errorf("an ungated transition made %d upserts, %d inserts, %d replaces; want exactly one upsert",
			upserts, inserts, replaces)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q, want %q", got.State, "paid")
	}
}

// Last-write-wins is the documented semantics for an ungated transition, so a
// concurrent write by another replica must be overwritten rather than deferred
// to. The gated path is the one with a race to lose.
func TestAnUngatedTransitionOverwritesWhateverAnotherReplicaLeft(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "somewhere else entirely")

	if err := client.Transition(context.Background(), "checkout", "", "paid"); err != nil {
		t.Fatalf("an ungated transition failed: %v", err)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q, want the unconditional write to have won with %q", got.State, "paid")
	}
}

// An ungated transition has no race to lose, so the only way its write fails is
// that the store did — and that is reported, not retried and not counted as
// contention. The caller serves the response and logs the failure; what it must
// not do is believe the flow moved.
func TestAnUngatedTransitionReportsAFailedWrite(t *testing.T) {
	st := newControlledStore()
	broken := errors.New("store unavailable: upsert rejected")
	st.upsertErr = broken
	client, m := newTestClient(st)

	if err := client.Transition(context.Background(), "checkout", "", "paid"); !errors.Is(err, broken) {
		t.Fatalf("the transition returned %v, want the store's own error", err)
	}
	if _, _, _, upserts, _ := st.counts(); upserts != 1 {
		t.Errorf("the transition made %d upserts; a failed unconditional write is not retried (want 1)", upserts)
	}
	if got := counterValue(t, m, metricConflicts); got != 0 {
		t.Errorf("%s = %v; a broken store is not a lost race", metricConflicts, got)
	}
}

// The stored timestamp is read by every other replica and by the admin listing.
// Writing it in the pod's local zone would have two replicas describe the same
// document differently, so the write stamps UTC.
func TestATransitionStampsTheUpdateTimeInUTC(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)

	before := time.Now().UTC().Add(-time.Second)
	if err := client.Transition(context.Background(), "checkout", "", "paid"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	got, ok := storedState(t, st.Store, "checkout")
	if !ok {
		t.Fatal("the transition wrote no document")
	}
	if got.UpdatedAt.Before(before) {
		t.Errorf("updatedAt is %s, which predates the transition", got.UpdatedAt)
	}
	if got.UpdatedAt.Location() != time.UTC {
		t.Errorf("updatedAt is in %s, want UTC so every replica reads it the same way", got.UpdatedAt.Location())
	}
}

// The first transition of a flow has no document to replace, and an absent
// document reads as Started (SPEC §9.1), so a stub gated on Started must insert
// rather than skip. Getting this wrong would make every scenario stall on its
// first step, which is also the step every scenario has.
func TestTheFirstTransitionOfAFlowInsertsTheStateDocument(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)

	if err := client.Transition(context.Background(), "checkout", Started, "cart filled"); err != nil {
		t.Fatalf("the first transition failed: %v", err)
	}

	gets, inserts, replaces, _, landed := st.counts()
	if gets != 1 || inserts != 1 || replaces != 0 || landed != 1 {
		t.Errorf("the first transition made %d gets, %d inserts, %d replaces and landed %d writes; want 1, 1, 0, 1",
			gets, inserts, replaces, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "cart filled" {
		t.Errorf("the scenario is at %q, want %q", got.State, "cart filled")
	}
	if got := counterValue(t, m, metricConflicts); got != 0 {
		t.Errorf("%s = %v after an uncontended transition, want 0", metricConflicts, got)
	}
}

// A transition whose gate still holds replaces the document under its CAS token
// and moves the flow on. One get and one replace is the whole cost.
func TestATransitionThatWinsItsCASMovesTheScenario(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	if err := client.Transition(context.Background(), "checkout", "cart filled", "paid"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	gets, inserts, replaces, _, landed := st.counts()
	if gets != 1 || replaces != 1 || inserts != 0 || landed != 1 {
		t.Errorf("the transition made %d gets, %d replaces, %d inserts and landed %d writes; want 1, 1, 0, 1",
			gets, replaces, inserts, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q, want %q", got.State, "paid")
	}
	for _, name := range []string{metricRetries, metricConflicts} {
		if got := counterValue(t, m, name); got != 0 {
			t.Errorf("%s = %v after an uncontended transition, want 0", name, got)
		}
	}
}

// The gate is re-checked under CAS because matching and transitioning are not
// atomic across pods. If the state has already moved by the time the write is
// attempted, this transition no longer applies: forcing it would drag the flow
// backwards from wherever the other replica put it, and the test driving the
// flow would see a step it had already passed. Skipping is the documented
// answer, and the response is served either way.
func TestATransitionSkipsWhenTheStateHasAlreadyMovedOn(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "paid")

	if err := client.Transition(context.Background(), "checkout", "cart filled", "shipped"); err != nil {
		t.Fatalf("a skipped transition must not fail the response, but it returned: %v", err)
	}

	gets, inserts, replaces, upserts, _ := st.counts()
	if gets != 1 {
		t.Errorf("a skipped transition read the state %d times, want 1", gets)
	}
	if inserts|replaces|upserts != 0 {
		t.Errorf("a skipped transition wrote (%d inserts, %d replaces, %d upserts); it must leave the document alone",
			inserts, replaces, upserts)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q; the skipped transition overwrote the other replica's %q", got.State, "paid")
	}
	if got := counterValue(t, m, metricConflicts); got != 1 {
		t.Errorf("%s = %v after a skipped transition, want 1", metricConflicts, got)
	}
}

// An absent document is Started, so a stub gated on anything else must skip
// rather than treat "nothing written yet" as a match. Reading the absent
// document as a wildcard would let the second step of a flow serve before the
// first one had run.
func TestATransitionGatedOnALaterStateSkipsWhileTheDocumentIsAbsent(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)

	if err := client.Transition(context.Background(), "checkout", "cart filled", "paid"); err != nil {
		t.Fatalf("a skipped transition must not fail the response, but it returned: %v", err)
	}

	_, inserts, replaces, upserts, _ := st.counts()
	if inserts|replaces|upserts != 0 {
		t.Errorf("the transition wrote (%d inserts, %d replaces, %d upserts) against a gate that does not hold",
			inserts, replaces, upserts)
	}
	if _, exists := storedState(t, st.Store, "checkout"); exists {
		t.Error("a skipped transition created a state document")
	}
	if got := counterValue(t, m, metricConflicts); got != 1 {
		t.Errorf("%s = %v, want 1", metricConflicts, got)
	}
}

// A document whose state field is empty reads as Started here for the same
// reason it does on the read path: the two have to agree, or a flow would match
// a stub gated on Started and then refuse to transition out of it.
func TestATransitionReadsAnEmptyStateDocumentAsStarted(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "")

	if err := client.Transition(context.Background(), "checkout", Started, "cart filled"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "cart filled" {
		t.Errorf("the scenario is at %q, want %q", got.State, "cart filled")
	}
}

// Losing the compare-and-swap is not the same as losing the gate: the state is
// still the one this transition was matched against, someone else merely
// touched the document. Re-reading and trying again is what keeps a transition
// from being dropped over a write that raced with an unrelated one.
func TestATransitionThatLosesItsCASReReadsAndTriesAgain(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")
	st.writeErr = failFirst(1, errLostRace)

	if err := client.Transition(context.Background(), "checkout", "cart filled", "paid"); err != nil {
		t.Fatalf("a transition that retried and won reported: %v", err)
	}

	gets, _, replaces, _, landed := st.counts()
	if gets != 2 || replaces != 2 || landed != 1 {
		t.Errorf("the transition made %d gets and %d replaces and landed %d; want it to re-read once and land once (2, 2, 1)",
			gets, replaces, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "paid" {
		t.Errorf("the scenario is at %q, want the retry to have landed %q", got.State, "paid")
	}
	if got := counterValue(t, m, metricRetries); got != 1 {
		t.Errorf("%s = %v after one lost race, want 1", metricRetries, got)
	}
	if got := counterValue(t, m, metricConflicts); got != 0 {
		t.Errorf("%s = %v; a race that was won on the retry is not a skipped transition", metricConflicts, got)
	}
}

// This is the race the whole design exists for, and it cannot be staged from
// outside the process: the gate passes at read time, another replica moves the
// state before this write lands, the write is rejected on its token, and the
// re-read finds the gate no longer holds. The transition must then be abandoned
// with the other replica's state intact. A client that retried blindly — or
// that trusted the gate it checked before the write — would put the flow back
// to a step it had already left.
func TestALateMoveByAnotherReplicaIsNotOverwrittenOnTheRetry(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	st.afterGet = func(call int) {
		if call != 1 {
			return
		}
		// The other replica served the same gated stub a moment earlier and
		// moved the flow on. Its write invalidates the token this transition is
		// holding without changing what this transition already read.
		seed(t, st.Store, "checkout", "cancelled by another replica")
	}

	if err := client.Transition(context.Background(), "checkout", "cart filled", "paid"); err != nil {
		t.Fatalf("an abandoned transition must not fail the response, but it returned: %v", err)
	}

	gets, _, replaces, _, landed := st.counts()
	if gets != 2 {
		t.Errorf("the transition made %d gets; it must re-read after losing the write (want 2)", gets)
	}
	if replaces != 1 || landed != 0 {
		t.Errorf("the transition made %d replaces of which %d landed; the second attempt must not write at all (want 1, 0)",
			replaces, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "cancelled by another replica" {
		t.Errorf("the scenario is at %q; the retry overwrote the state the other replica had already moved to", got.State)
	}
	if got := counterValue(t, m, metricRetries); got != 1 {
		t.Errorf("%s = %v, want 1", metricRetries, got)
	}
	if got := counterValue(t, m, metricConflicts); got != 1 {
		t.Errorf("%s = %v, want the abandoned transition counted once", metricConflicts, got)
	}
}

// Two replicas serving the first request of the same flow both find no document
// and both insert. The loser's insert is a lost race, not a broken store, so it
// re-reads — and finds the flow already at the state it wanted to write, which
// is a gate that no longer holds. Skipping leaves the winner's document alone
// rather than replacing it with an identical one and bumping its timestamp.
func TestAnInsertThatLosesToAnotherReplicaSkipsRatherThanRewriting(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)

	st.afterGet = func(call int) {
		if call != 1 {
			return
		}
		seed(t, st.Store, "checkout", "cart filled")
	}

	if err := client.Transition(context.Background(), "checkout", Started, "cart filled"); err != nil {
		t.Fatalf("a lost insert must not fail the response, but it returned: %v", err)
	}

	gets, inserts, replaces, _, landed := st.counts()
	if gets != 2 || inserts != 1 || replaces != 0 || landed != 0 {
		t.Errorf("the transition made %d gets, %d inserts, %d replaces and landed %d; want 2, 1, 0, 0",
			gets, inserts, replaces, landed)
	}
	if got := counterValue(t, m, metricConflicts); got != 1 {
		t.Errorf("%s = %v, want 1", metricConflicts, got)
	}
}

// The loop re-derives on every attempt whether the document exists, rather than
// committing to insert-or-replace from the first read. An admin PUT that puts
// the flow back to Started between this transition's read and its insert leaves
// a document where there was none, and the retry has to notice and replace it.
// A loop that kept inserting would fail the same way three times and give up on
// a transition that was there to be made.
func TestARetryAfterAnInsertConflictReplacesTheDocumentThatAppeared(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)

	st.afterGet = func(call int) {
		if call != 1 {
			return
		}
		// An admin PUT /{name}/state re-asserting Started, which is what a
		// suite resetting one flow between steps does.
		seed(t, st.Store, "checkout", Started)
	}

	if err := client.Transition(context.Background(), "checkout", Started, "cart filled"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	gets, inserts, replaces, _, landed := st.counts()
	if gets != 2 || inserts != 1 || replaces != 1 || landed != 1 {
		t.Errorf("the transition made %d gets, %d inserts, %d replaces and landed %d; want it to insert, lose, then replace (2, 1, 1, 1)",
			gets, inserts, replaces, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "cart filled" {
		t.Errorf("the scenario is at %q, want %q", got.State, "cart filled")
	}
}

// Three lost races is where a transition stops. A scenario is a functional-test
// construct, not a contended counter: if the write has lost three times the
// document is being driven by something else, and retrying forever would hold a
// mock response open on a KV loop. The response is served regardless, so the
// caller must see success — a returned error here would turn contention into a
// 500 for a request that matched perfectly well.
func TestATransitionGivesUpAfterThreeLostRacesWithoutFailingTheResponse(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")
	st.writeErr = failAlways(errLostRace)

	if err := client.Transition(context.Background(), "checkout", "cart filled", "paid"); err != nil {
		t.Fatalf("an abandoned transition failed the response with: %v", err)
	}

	gets, _, replaces, _, landed := st.counts()
	if gets != 3 || replaces != 3 || landed != 0 {
		t.Errorf("the transition made %d gets and %d replaces and landed %d; want exactly three attempts (3, 3, 0)",
			gets, replaces, landed)
	}
	if got, _ := storedState(t, st.Store, "checkout"); got.State != "cart filled" {
		t.Errorf("the scenario is at %q, want the abandoned transition to have left %q", got.State, "cart filled")
	}
	if got := counterValue(t, m, metricRetries); got != 2 {
		t.Errorf("%s = %v; three attempts are two retries", metricRetries, got)
	}
	if got := counterValue(t, m, metricConflicts); got != 1 {
		t.Errorf("%s = %v, want the abandoned transition counted once", metricConflicts, got)
	}
}

// A store that cannot be read is not a lost race, and pretending it is would
// have the transition skipped and counted as contention — a metric that says
// "another replica moved first" when the truth is that the store is down. The
// error goes back to the caller, which logs it against the served response.
func TestATransitionReportsAFailedReadInsteadOfGuessingTheState(t *testing.T) {
	st := newControlledStore()
	st.getErr = errors.New("kv node down")
	client, m := newTestClient(st)

	err := client.Transition(context.Background(), "checkout", "cart filled", "paid")
	if err == nil {
		t.Fatal("a transition over a store that cannot be read reported success")
	}

	gets, inserts, replaces, upserts, _ := st.counts()
	if gets != 1 {
		t.Errorf("the transition read %d times; a failed read is not retried (want 1)", gets)
	}
	if inserts|replaces|upserts != 0 {
		t.Errorf("the transition wrote (%d inserts, %d replaces, %d upserts) without knowing the state",
			inserts, replaces, upserts)
	}
	if got := counterValue(t, m, metricConflicts); got != 0 {
		t.Errorf("%s = %v; a store failure is not a lost race", metricConflicts, got)
	}
}

// A write that failed for any reason other than a lost race is reported and not
// retried. Retrying a store that is refusing writes spends the request's
// remaining budget on two more round trips that will fail the same way, and
// then reports the outcome as contention.
func TestAWriteFailureThatIsNotALostRaceIsReportedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seedState  string
		required   string
		wantWrites int
	}{
		{name: "a failing replace", seedState: "cart filled", required: "cart filled", wantWrites: 1},
		{name: "a failing insert", required: Started, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newControlledStore()
			client, m := newTestClient(st)
			if tc.seedState != "" {
				seed(t, st.Store, "checkout", tc.seedState)
			}
			broken := errors.New("store unavailable: connection refused")
			st.writeErr = failAlways(broken)

			err := client.Transition(context.Background(), "checkout", tc.required, "paid")
			if !errors.Is(err, broken) {
				t.Fatalf("the transition returned %v, want the store's own error", err)
			}

			gets, inserts, replaces, _, _ := st.counts()
			if inserts+replaces != tc.wantWrites {
				t.Errorf("the transition attempted %d writes, want %d", inserts+replaces, tc.wantWrites)
			}
			if gets != 1 {
				t.Errorf("the transition read %d times, want 1", gets)
			}
			if got := counterValue(t, m, metricRetries); got != 0 {
				t.Errorf("%s = %v after an unretryable failure, want 0", metricRetries, got)
			}
		})
	}
}

// The CAS loop is bounded by the same deadline the read path is, so a store
// that has stopped answering costs a transition scenario_kv_timeout rather than
// holding the request open across three attempts of unbounded length.
func TestATransitionIsAbandonedWhenTheStoreNeverAnswers(t *testing.T) {
	m := metrics.New("test", "test", true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(&stallingStore{Store: memory.New(0)}, log, m, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err := withinDeadline(t, 2*time.Second, func() error {
		return client.Transition(ctx, "checkout", "cart filled", "paid")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the transition returned %v, want it abandoned on the deadline", err)
	}
}

// Compare-and-swap is what makes the state correct with any replica count, and
// the property it buys is that exactly one of a set of simultaneous transitions
// out of the same state takes effect. Every other one finds the gate no longer
// holds and steps aside — quietly, because the response it belongs to was
// served on a match that was valid when it was made.
//
// Run under -race, this is also the only place the client is exercised from
// several goroutines at once, which is how it runs in production.
func TestOnlyOneOfManySimultaneousTransitionsOutOfAStateLands(t *testing.T) {
	const racers = 8

	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	targets := make([]string, racers)
	failures := make([]error, racers)
	for i := range racers {
		targets[i] = "paid, by racer " + strconv.Itoa(i)
	}

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			failures[i] = client.Transition(context.Background(), "checkout", "cart filled", targets[i])
		}()
	}
	close(release)
	wg.Wait()

	for i, err := range failures {
		if err != nil {
			t.Errorf("racer %d failed its response over a lost race: %v", i, err)
		}
	}

	_, _, _, _, landed := st.counts()
	if landed != 1 {
		t.Errorf("%d of %d simultaneous transitions landed a write; compare-and-swap must admit exactly one",
			landed, racers)
	}

	final, ok := storedState(t, st.Store, "checkout")
	if !ok {
		t.Fatal("no state document survived the race")
	}
	won := false
	for _, target := range targets {
		if final.State == target {
			won = true
			break
		}
	}
	if !won {
		t.Errorf("the scenario ended at %q, which is not any racer's target", final.State)
	}

	// Every racer but the winner steps aside exactly once, whether it found the
	// state already moved or lost the write and then found it moved.
	if got := counterValue(t, m, metricConflicts); got != racers-1 {
		t.Errorf("%s = %v, want one per loser (%d)", metricConflicts, got, racers-1)
	}
}

// Only a lost race is retried. The classification is made on the error text
// because the sentinel belongs to the driver, so the cases worth pinning are
// the exact wordings the two drivers produce and the errors that must not be
// mistaken for them: a store that is down, a read that timed out and a document
// that has been deleted are all failures to report, not races to re-run.
func TestOnlyALostRaceIsTreatedAsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "no error at all", err: nil},
		{name: "the store's lost-race sentinel", err: store.ErrCASConflict, want: true},
		{name: "a driver wrapping it with context", err: fmt.Errorf(`scenario "checkout": %w`, store.ErrCASConflict), want: true},
		{name: "wrapped twice", err: fmt.Errorf("replace scenario: %w", fmt.Errorf(`scenario "checkout": %w`, store.ErrCASConflict)), want: true},
		{name: "the store is unreachable", err: store.ErrUnavailable},
		{name: "the document was deleted under us", err: store.ErrNotFound},
		{name: "the write timed out", err: context.DeadlineExceeded},
		{name: "an empty message", err: errors.New("")},
		{name: "an unrelated failure", err: errors.New("durability requirement not met")},
		// The reason the sentinel exists. A scenario name reaches the driver's
		// error text through the document key, so these once read as lost races
		// and had a store outage retried and then swallowed.
		{name: "a scenario named after the old text match", err: errors.New(`scenario "document exists": durability requirement not met`)},
		{name: "the other spelling of it", err: errors.New(`scenario "cas mismatch": temporary failure`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
