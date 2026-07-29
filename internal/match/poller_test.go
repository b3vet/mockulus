// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store/memory"
)

// syncBuffer is a log sink a test can read while the poller goroutine is still
// writing to it. A plain bytes.Buffer here is a data race, not a flake.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeSignal stands in for the store's change counter, which is the one thing
// the poller reads on its fast path.
type fakeSignal struct {
	mu          sync.Mutex
	epoch       uint64
	err         error
	calls       int
	hadDeadline bool
	block       chan struct{}
}

func (s *fakeSignal) Epoch(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	s.calls++
	_, hasDeadline := ctx.Deadline()
	s.hadDeadline = hasDeadline
	epoch, err, block := s.epoch, s.err, s.block
	s.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return epoch, err
}

// stall makes every read hang until its context gives up, which is what a store
// that accepted a request and went silent looks like.
func (s *fakeSignal) stall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.block = make(chan struct{})
}

func (s *fakeSignal) report(epoch uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch, s.err = epoch, err
}

func (s *fakeSignal) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSignal) sawDeadline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hadDeadline
}

// testPoller wires a poller over a store whose reads can be made to fail, with
// a log sink that survives being read while the poller is running.
func testPoller(t *testing.T, sync, resync time.Duration) (
	*unreadableStore, *fakeSignal, *Poller, *Engine, *syncBuffer) {

	t.Helper()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := &unreadableStore{Store: memory.New(0)}
	sig := &fakeSignal{}
	m := metrics.New("test", "test", false)
	engine := NewEngine(config.Config{}, m, log, nil)
	builder := NewBuilder(st, engine, log, m, testStubOptions())
	return st, sig, NewPoller(sig, builder, engine, log, m, sync, resync), engine, logs
}

// waitFor blocks until cond holds, failing the test rather than hanging if it
// never does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// storeEpoch reads the counter the store actually holds, so a test never has to
// assume how many times a write bumped it.
func storeEpoch(t *testing.T, st *unreadableStore) uint64 {
	t.Helper()
	epoch, err := st.Epoch(context.Background())
	if err != nil {
		t.Fatalf("read epoch: %v", err)
	}
	return epoch
}

// The epoch poll is the fast path, and it only earns that name by not
// rebuilding. A poller that reloads on every tick turns one cheap counter read
// per interval into a full store reload per interval, on every pod (SPEC §8).
func TestTheEpochPollRebuildsOnlyWhenTheCounterMoved(t *testing.T) {
	st, sig, p, engine, _ := testPoller(t, 50*time.Millisecond, time.Hour)
	putStub(t, st, "11111111-0000-4000-8000-000000000030", "/polled")
	epoch := storeEpoch(t, st)

	// The counter has not moved as far as this pod is concerned: it is still on
	// the empty snapshot at epoch zero and the signal agrees.
	sig.report(0, nil)
	p.pollOnce(context.Background())
	if got := st.loadCount(); got != 0 {
		t.Fatalf("an unchanged counter caused %d store reloads, want 0", got)
	}
	if engine.Snapshot().Len() != 0 {
		t.Fatal("the snapshot was rebuilt without the counter moving")
	}

	sig.report(epoch, nil)
	p.pollOnce(context.Background())
	if got := st.loadCount(); got != 1 {
		t.Fatalf("a moved counter caused %d store reloads, want 1", got)
	}
	if engine.Snapshot().Epoch != epoch {
		t.Fatalf("snapshot epoch = %d, want %d", engine.Snapshot().Epoch, epoch)
	}
	if id := match(t, engine.Snapshot(), "GET", "/polled", "", nil); id == "" {
		t.Fatal("the rebuilt snapshot does not serve the stub the counter was bumped for")
	}

	// Having converged, the next tick must go back to costing one counter read.
	p.pollOnce(context.Background())
	if got := st.loadCount(); got != 1 {
		t.Errorf("a converged pod reloaded again: %d reloads, want 1", got)
	}
}

// A store outage is a degraded mode, not a failure. The loaded snapshot keeps
// serving and the next tick tries again — a poller that gave up here would
// leave the pod permanently stale after one bad read (SPEC §4.6).
func TestAFailedEpochPollKeepsServingAndRetriesOnTheNextTick(t *testing.T) {
	st, sig, p, engine, logs := testPoller(t, 50*time.Millisecond, time.Hour)
	putStub(t, st, "11111111-0000-4000-8000-000000000031", "/retried")

	sig.report(storeEpoch(t, st), errors.New("store unavailable"))
	p.pollOnce(context.Background())

	if got := st.loadCount(); got != 0 {
		t.Fatalf("a failed epoch read caused %d store reloads, want 0", got)
	}
	if !strings.Contains(logs.String(), "epoch poll failed") {
		t.Error("the failed poll was not logged, so an outage would be invisible")
	}

	sig.report(storeEpoch(t, st), nil)
	p.pollOnce(context.Background())
	if id := match(t, engine.Snapshot(), "GET", "/retried", "", nil); id == "" {
		t.Error("the poller did not converge once the store came back")
	}
}

// The epoch is only ever adopted by a rebuild that succeeded. Recording it
// optimistically would make the failure permanent: the counter would look
// converged and no later tick would ever try again.
func TestARebuildThatFailsLeavesTheEpochChangeOutstanding(t *testing.T) {
	st, sig, p, engine, logs := testPoller(t, 50*time.Millisecond, time.Hour)
	putStub(t, st, "11111111-0000-4000-8000-000000000032", "/outstanding")
	epoch := storeEpoch(t, st)

	st.failReads(errors.New("store unavailable"), nil, nil)
	sig.report(epoch, nil)
	p.pollOnce(context.Background())

	if engine.Snapshot().Epoch != 0 {
		t.Fatalf("the snapshot adopted epoch %d from a rebuild that failed", engine.Snapshot().Epoch)
	}
	if !strings.Contains(logs.String(), "epoch-triggered rebuild failed") {
		t.Error("the failed rebuild was not logged")
	}

	st.failReads(nil, nil, nil)
	p.pollOnce(context.Background())
	if id := match(t, engine.Snapshot(), "GET", "/outstanding", "", nil); id == "" {
		t.Error("the pod never retried the rebuild the epoch change asked for")
	}
}

// The epoch read is bounded by the poll interval, because a store that has
// stopped answering must not let polls pile up behind each other — one hung
// read would otherwise hold a goroutine for as long as the store stayed down.
func TestASlowEpochReadIsAbandonedRatherThanAllowedToStackUp(t *testing.T) {
	st, sig, p, _, _ := testPoller(t, 20*time.Millisecond, time.Hour)
	// Never closed: this is a store that accepted the read and went silent.
	sig.stall()

	done := make(chan struct{})
	go func() {
		p.pollOnce(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pollOnce never returned, so a silent store holds the poller forever")
	}

	if !sig.sawDeadline() {
		t.Error("the epoch read was made with no deadline")
	}
	if got := st.loadCount(); got != 0 {
		t.Errorf("an abandoned epoch read still triggered %d reloads", got)
	}
}

// The resync is the backstop, and the only thing that sweeps documents whose
// TTL expired: their disappearance bumps no counter, so an epoch poll can never
// notice it. This is the test that would fail if the resync ticker were quietly
// made conditional (SPEC §7.4, §8).
func TestTheResyncReloadsEvenThoughTheCounterNeverMoves(t *testing.T) {
	st, sig, p, engine, _ := testPoller(t, time.Hour, 2*time.Millisecond)
	putStub(t, st, "11111111-0000-4000-8000-000000000033", "/swept")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	waitFor(t, "the resync to reload the store twice", func() bool { return st.loadCount() >= 2 })

	if got := sig.callCount(); got != 0 {
		t.Errorf("the epoch was read %d times, but the sync interval is an hour — "+
			"the resync is not unconditional", got)
	}
	if id := match(t, engine.Snapshot(), "GET", "/swept", "", nil); id == "" {
		t.Error("the resync did not install what it loaded")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
}

// The two timers do different jobs, so the sync one has to be shown to fire on
// its own. A poller whose epoch ticker never ran would still converge, just
// only ever at the resync interval — minutes rather than the sub-second
// propagation the fast path exists for.
func TestTheEpochTickerConvergesWithoutWaitingForTheResync(t *testing.T) {
	st, sig, p, engine, _ := testPoller(t, 2*time.Millisecond, time.Hour)
	putStub(t, st, "11111111-0000-4000-8000-000000000034", "/converged")
	sig.report(storeEpoch(t, st), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	waitFor(t, "the epoch ticker to converge the snapshot", func() bool {
		return engine.Snapshot().Len() == 1
	})
	if got := sig.callCount(); got == 0 {
		t.Error("the snapshot converged without the epoch ever being read")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
}

// A resync that cannot read the store must keep the snapshot it has. The
// backstop exists to heal a pod, and a backstop that empties one on a bad read
// would be the fastest way to take a whole deployment down.
func TestAFailedResyncKeepsTheSnapshotItAlreadyHas(t *testing.T) {
	st, sig, p, engine, logs := testPoller(t, time.Hour, 2*time.Millisecond)
	putStub(t, st, "11111111-0000-4000-8000-000000000035", "/held")

	// Converge first, so there is something to lose.
	sig.report(storeEpoch(t, st), nil)
	p.pollOnce(context.Background())
	if engine.Snapshot().Len() != 1 {
		t.Fatal("the pod did not converge before the outage")
	}
	converged := engine.Snapshot()

	st.failReads(errors.New("store unavailable"), nil, nil)
	before := st.loadCount()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	waitFor(t, "two failed resync attempts", func() bool { return st.loadCount() >= before+2 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}

	if engine.Snapshot() != converged {
		t.Error("a failed resync replaced the snapshot that was serving")
	}
	if !strings.Contains(logs.String(), "resync rebuild failed") {
		t.Error("the failed resync was not logged")
	}
}
