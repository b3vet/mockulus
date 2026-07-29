// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
)

// The series SPEC §14.1 promises an operator watching a scenario for contention.
const (
	metricReads     = "mockulus_scenario_reads_total"
	metricRetries   = "mockulus_scenario_cas_retries_total"
	metricConflicts = "mockulus_scenario_transition_conflicts_total"
)

// controlledStore is the in-memory driver with a hook on each call a scenario
// makes.
//
// The memory driver is the fake this codebase reaches for first (SPEC §18), and
// for scenarios it earns that: it mints a fresh CAS token per write and reports
// a lost race in the same words the Couchbase driver does, so a transition run
// against it exercises a real compare-and-swap rather than a mock of one. The
// two things it cannot do are fail, and let another replica slip a write into
// the window between this transition's read and its write. Those are exactly
// the branches a black-box run cannot stage, so the hooks add only those.
type controlledStore struct {
	*memory.Store

	mu sync.Mutex

	gets     int // GetScenario calls
	inserts  int // InsertScenario calls, including ones the hook failed
	replaces int // ReplaceScenario calls, including ones the hook failed
	upserts  int // UpsertScenario calls
	writes   int // inserts + replaces, which is what orders the write hook
	landed   int // writes the underlying driver actually accepted

	getErr    error                   // GetScenario fails with this instead of reading
	writeErr  func(attempt int) error // Insert/Replace fail with this instead of writing
	upsertErr error
	deleteErr error
	listErr   error

	// afterGet runs once a read has returned, so whatever it writes lands in
	// the window the CAS token is meant to protect: the caller is now holding a
	// state and a token that another replica has already moved past.
	afterGet func(call int)
}

func newControlledStore() *controlledStore {
	return &controlledStore{Store: memory.New(0)}
}

func (s *controlledStore) GetScenario(ctx context.Context, name string) (store.ScenarioState, store.CAS, error) {
	s.mu.Lock()
	s.gets++
	call, failWith, after := s.gets, s.getErr, s.afterGet
	s.mu.Unlock()

	if failWith != nil {
		return store.ScenarioState{}, 0, failWith
	}
	state, cas, err := s.Store.GetScenario(ctx, name)
	if after != nil {
		after(call)
	}
	return state, cas, err
}

func (s *controlledStore) InsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	if failWith := s.beginWrite(&s.inserts); failWith != nil {
		return failWith
	}
	err := s.Store.InsertScenario(ctx, name, state)
	s.noteLanded(err)
	return err
}

func (s *controlledStore) ReplaceScenario(ctx context.Context, name string, state store.ScenarioState, cas store.CAS) error {
	if failWith := s.beginWrite(&s.replaces); failWith != nil {
		return failWith
	}
	err := s.Store.ReplaceScenario(ctx, name, state, cas)
	s.noteLanded(err)
	return err
}

func (s *controlledStore) UpsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	s.mu.Lock()
	s.upserts++
	failWith := s.upsertErr
	s.mu.Unlock()

	if failWith != nil {
		return failWith
	}
	return s.Store.UpsertScenario(ctx, name, state)
}

func (s *controlledStore) DeleteAllScenarios(ctx context.Context) error {
	s.mu.Lock()
	failWith := s.deleteErr
	s.mu.Unlock()

	if failWith != nil {
		return failWith
	}
	return s.Store.DeleteAllScenarios(ctx)
}

func (s *controlledStore) ListScenarioStates(ctx context.Context) (map[string]store.ScenarioState, error) {
	s.mu.Lock()
	failWith := s.listErr
	s.mu.Unlock()

	if failWith != nil {
		return nil, failWith
	}
	return s.Store.ListScenarioStates(ctx)
}

// beginWrite counts a conditional write and returns the error the test wants
// that write — identified by its 1-based position among this store's writes —
// to fail with.
func (s *controlledStore) beginWrite(kind *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*kind++
	s.writes++
	if s.writeErr == nil {
		return nil
	}
	return s.writeErr(s.writes)
}

func (s *controlledStore) noteLanded(err error) {
	if err != nil {
		return
	}
	s.mu.Lock()
	s.landed++
	s.mu.Unlock()
}

// counts snapshots the call tallies, so an assertion never reads a field while
// a concurrent transition is writing it.
func (s *controlledStore) counts() (gets, inserts, replaces, upserts, landed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.inserts, s.replaces, s.upserts, s.landed
}

// stallingStore never answers a read. That is how a sick KV node looks from the
// request path: not an error, just silence, which is the case a per-call
// deadline exists for.
type stallingStore struct {
	*memory.Store
}

func (s *stallingStore) GetScenario(ctx context.Context, _ string) (store.ScenarioState, store.CAS, error) {
	<-ctx.Done()
	return store.ScenarioState{}, 0, ctx.Err()
}

// withinDeadline runs call on its own goroutine and fails if it has not
// returned by limit.
//
// The tests that use it are about the client bounding its own store calls, and
// a client that stopped doing so would otherwise hang the whole package instead
// of failing one test — a hang says far less about what broke than a name does.
// The caller cancels its context at cleanup, which is what lets the abandoned
// goroutine finish.
func withinDeadline(t *testing.T, limit time.Duration, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()

	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("the call was still running after %s, so nothing is bounding it", limit)
		return nil
	}
}

// newTestClient wires a client with a generous timeout, so a test that is not
// about the deadline never races it.
func newTestClient(st store.ScenarioStore) (*Client, *metrics.Metrics) {
	// Registering the collectors is what makes the counters readable; every
	// call gets its own registry, so tests do not share tallies.
	m := metrics.New("test", "test", true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewClient(st, log, m, 5*time.Second), m
}

// seed writes a state document the way a previous request or an admin PUT would
// have left it.
func seed(t *testing.T, st store.ScenarioStore, name, state string) {
	t.Helper()
	err := st.UpsertScenario(context.Background(), name, store.ScenarioState{
		State: state, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed scenario %q: %v", name, err)
	}
}

// storedState reads a state document back without going through the client, so
// an assertion about what was persisted cannot be satisfied by the client's own
// interpretation of an absent or empty document.
func storedState(t *testing.T, st store.ScenarioStore, name string) (store.ScenarioState, bool) {
	t.Helper()
	state, _, err := st.GetScenario(context.Background(), name)
	if errors.Is(err, store.ErrNotFound) {
		return store.ScenarioState{}, false
	}
	if err != nil {
		t.Fatalf("read back scenario %q: %v", name, err)
	}
	return state, true
}

// counterValue reads one counter out of the registry by name. It goes through
// Gather rather than the struct field because the exported series is what SPEC
// §14.1 promises an operator; a field that is incremented but never registered
// would satisfy the field and not the promise.
func counterValue(t *testing.T, m *metrics.Metrics, name string) float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		if len(family.GetMetric()) != 1 {
			t.Fatalf("%s carries %d series, want exactly 1", name, len(family.GetMetric()))
		}
		return family.GetMetric()[0].GetCounter().GetValue()
	}
	t.Fatalf("%s is not registered, so no operator can see it", name)
	return 0
}

// A scenario comes into being because a stub mentions it, not because anything
// wrote a document for it, so the very first read of a brand-new flow has to
// answer Started rather than fail (SPEC §9.1). Reporting ErrNotFound here would
// turn every first request through a stateful mock into a 500.
func TestAScenarioNoRequestHasTouchedYetReadsAsStarted(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)

	state, err := client.State(context.Background(), "checkout")
	if err != nil {
		t.Fatalf("reading an untouched scenario failed: %v", err)
	}
	if state != Started {
		t.Errorf("an untouched scenario reads as %q, want %q", state, Started)
	}
}

// Once a flow has moved, the stored state is the answer — this is the whole
// point of keeping it in the store rather than in the pod.
func TestAStoredStateIsWhatAScenarioReadsAsOnceItHasMoved(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	state, err := client.State(context.Background(), "checkout")
	if err != nil {
		t.Fatalf("reading a moved scenario failed: %v", err)
	}
	if state != "cart filled" {
		t.Errorf("the scenario reads as %q, want %q", state, "cart filled")
	}
}

// A document whose state field is empty is not something the admin layer will
// write — it rejects a body without a state — but it is what a hand-edited
// document, a half-written file-store snapshot or an older schema leaves
// behind. Reading it as the empty string would gate every stub in the scenario
// on a state no stub can declare, and the flow would answer 404 forever with
// nothing in the logs to say why. Started is the recoverable reading.
func TestAStateDocumentWithNoStateInItReadsAsStarted(t *testing.T) {
	st := newControlledStore()
	client, _ := newTestClient(st)
	seed(t, st.Store, "checkout", "")

	state, err := client.State(context.Background(), "checkout")
	if err != nil {
		t.Fatalf("reading an empty state document failed: %v", err)
	}
	if state != Started {
		t.Errorf("an empty state document reads as %q, want %q", state, Started)
	}
}

// A store that cannot be read must not be reported as Started. Started is a
// real state that stubs gate on, so guessing it serves the first step of a flow
// to a test that is somewhere in the middle of it — a wrong answer dressed as a
// right one. The caller turns this error into the 500 of SPEC §9.2 and §4.6,
// and it can only do that if the error arrives.
func TestAStoreFailureIsReportedRatherThanReadAsStarted(t *testing.T) {
	st := newControlledStore()
	st.getErr = errors.New("kv node down")
	client, _ := newTestClient(st)

	state, err := client.State(context.Background(), "checkout")
	if err == nil {
		t.Fatalf("a failed read reported success with state %q", state)
	}
	if state != "" {
		t.Errorf("a failed read returned the state %q; any non-empty answer here is a guess", state)
	}
}

// ErrNotFound is the one store error that is not a failure: it is how the store
// says "nobody has written this yet", which SPEC §9.1 defines as Started. The
// distinction matters because the two arrive on the same return value, and
// collapsing them either way breaks one of the two cases.
func TestOnlyErrNotFoundIsTreatedAsStartedAndOtherErrorsSurface(t *testing.T) {
	for _, tc := range []struct {
		name      string
		storeErr  error
		wantState string
		wantErr   bool
	}{
		{name: "the document has never been written", storeErr: store.ErrNotFound, wantState: Started},
		{name: "the document is wrapped as not found", storeErr: errors.Join(errors.New("get scenario"), store.ErrNotFound), wantState: Started},
		{name: "the store is unreachable", storeErr: store.ErrUnavailable, wantErr: true},
		{name: "the read timed out", storeErr: context.DeadlineExceeded, wantErr: true},
		{name: "the document did not decode", storeErr: errors.New("json: cannot unmarshal"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newControlledStore()
			st.getErr = tc.storeErr
			client, _ := newTestClient(st)

			state, err := client.State(context.Background(), "checkout")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the store failed with %v but the read reported success", tc.storeErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("the store said %v, which means Started, but the read failed: %v", tc.storeErr, err)
			}
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

// A state read that never comes back would hold a mock request open for as long
// as the client is willing to wait, and the caller passes a background context
// precisely because it has no deadline of its own to lend. The bound has to
// come from the client, so a KV node that has stopped answering costs a request
// scenario_kv_timeout and not a worker forever (SPEC §7.2, §9.2).
func TestAStateReadIsAbandonedWhenTheStoreNeverAnswers(t *testing.T) {
	m := metrics.New("test", "test", true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(&stallingStore{Store: memory.New(0)}, log, m, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var state string
	err := withinDeadline(t, 2*time.Second, func() error {
		var readErr error
		state, readErr = client.State(ctx, "checkout")
		return readErr
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("state = %q, err = %v; want the read to be abandoned on the deadline", state, err)
	}
}

// The read counter is what tells an operator how much a deployment is paying
// for scenarios (SPEC §14.1, P2). One increment per state read is what makes it
// comparable with the request count, and it is also the number the caller's
// per-request memo is measured against: the memo in the request-path gate turns
// N gated stubs in one scenario into one of these, and it can only do that if
// each call here is exactly one read.
func TestEveryStateReadCountsOnceAndCostsOneStoreRead(t *testing.T) {
	st := newControlledStore()
	client, m := newTestClient(st)
	seed(t, st.Store, "checkout", "cart filled")

	for range 3 {
		if _, err := client.State(context.Background(), "checkout"); err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	if got := counterValue(t, m, metricReads); got != 3 {
		t.Errorf("%s = %v after three reads, want 3", metricReads, got)
	}
	if gets, _, _, _, _ := st.counts(); gets != 3 {
		t.Errorf("three reads made %d store gets, want 3", gets)
	}
}

// A read that fails still cost the store a round trip, so it still counts. A
// counter that only tracked successes would go quiet exactly when the store is
// in trouble, which is when someone is looking at it.
func TestAFailedStateReadStillCountsAsARead(t *testing.T) {
	st := newControlledStore()
	st.getErr = errors.New("kv node down")
	client, m := newTestClient(st)

	if _, err := client.State(context.Background(), "checkout"); err == nil {
		t.Fatal("the failing store reported success")
	}
	if got := counterValue(t, m, metricReads); got != 1 {
		t.Errorf("%s = %v after one failed read, want 1", metricReads, got)
	}
}
