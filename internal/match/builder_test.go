// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
)

// unreadableStore is the memory driver with its bulk read replaced, which is
// the only call a rebuild makes that has a degraded mode of its own (SPEC §4.6).
type unreadableStore struct {
	*memory.Store
	fail error
}

func (s *unreadableStore) LoadAll(ctx context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	if s.fail != nil {
		return nil, nil, 0, s.fail
	}
	return s.Store.LoadAll(ctx)
}

// testBuilder wires a builder over a store, returning both plus the engine it
// swaps snapshots into.
func testBuilder(t *testing.T) (*unreadableStore, *Builder, *Engine) {
	t.Helper()
	st := &unreadableStore{Store: memory.New(0)}
	m := metrics.New("test", "test", false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := NewEngine(config.Config{}, m, log, nil)
	return st, NewBuilder(st, engine, log, m, testStubOptions()), engine
}

// putStub registers one mapping directly in the store, the way a write that
// this pod did not handle arrives.
func putStub(t *testing.T, st *unreadableStore, id, path string) {
	t.Helper()
	mapping := `{"id":"` + id + `","request":{"method":"GET","urlPath":"` + path +
		`"},"response":{"status":200,"body":"served"}}`
	err := st.PutStub(context.Background(), store.StoredStub{
		ID:            id,
		SchemaVersion: store.SchemaVersion,
		Seq:           1,
		Persistent:    true,
		Mapping:       json.RawMessage(mapping),
	})
	if err != nil {
		t.Fatalf("put stub: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
}

// A store that cannot be read must cost freshness and nothing else. Swapping in
// whatever the failed read managed to return would empty the snapshot on the
// first failure and take every stub in the deployment down with it, which is
// the whole of SPEC §4.6's row for a LoadAll error.
func TestRebuildKeepsThePreviousSnapshotWhenTheReadFails(t *testing.T) {
	st, builder, engine := testBuilder(t)
	putStub(t, st, "11111111-0000-4000-8000-000000000001", "/keep")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if got := engine.Snapshot().Len(); got != 1 {
		t.Fatalf("snapshot holds %d stubs, want 1", got)
	}

	st.fail = errors.New("store unavailable")
	if err := builder.Rebuild(context.Background(), "test"); err == nil {
		t.Fatal("a failed read reported success")
	}

	if got := engine.Snapshot().Len(); got != 1 {
		t.Fatalf("snapshot holds %d stubs after the failed read, want the previous 1", got)
	}
	if id := match(t, engine.Snapshot(), "GET", "/keep", "", nil); id == "" {
		t.Fatal("the previous snapshot stopped serving after a failed read")
	}
}

// The other half of the same decision, and the reason a rebuild cannot simply
// refuse to empty a populated snapshot: this is `DELETE /__admin/mappings`, and
// it is indistinguishable from a store that answered from a stale view. The
// builder takes a successful read at its word; keeping a read honest is the
// driver's job.
func TestRebuildEmptiesTheSnapshotWhenTheStoreIsEmpty(t *testing.T) {
	st, builder, engine := testBuilder(t)
	putStub(t, st, "11111111-0000-4000-8000-000000000002", "/gone")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if got := engine.Snapshot().Len(); got != 1 {
		t.Fatalf("snapshot holds %d stubs, want 1", got)
	}

	if err := st.DeleteAllStubs(context.Background()); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("rebuild after delete: %v", err)
	}
	if got := engine.Snapshot().Len(); got != 0 {
		t.Fatalf("snapshot holds %d stubs after a deployment-wide delete, want 0", got)
	}
}
