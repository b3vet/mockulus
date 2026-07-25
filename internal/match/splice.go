// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"errors"

	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
)

// Splicing is what makes a single-pod test flow see zero staleness.
//
// A test that registers a stub and immediately calls it must not race the sync
// interval. The pod handling the admin write therefore updates its own snapshot
// synchronously, before the write returns — but rebuilding from the store would
// mean a round trip and a full recompile on the write path, which at 10k stubs
// is not something to do per registration.
//
// The splice avoids both. The stub is already compiled (validation compiled it,
// which is how an invalid stub is rejected before anything is persisted), and
// its sorted position follows from priority and sequence alone. So the new
// snapshot is the old ordered slice with one element inserted or replaced, and
// the indexes rebuilt over it — a slice copy and a map rebuild, no compilation
// and, for all but a file-backed stub, no I/O (SPEC §4.3 step 5).
//
// The level-triggered reload still follows within sync_interval on every pod
// and reconciles concurrent writes, so the splice is an optimisation of
// latency, never the mechanism of correctness.
//
// What the splice must not become is a second, subtly different answer. Serving
// a spliced stub differently from the rebuilt one turns the sync interval into
// a window where the same request gets two answers, which is worse than the
// staleness splicing exists to remove.

// SpliceStub installs a compiled stub into the served snapshot, replacing any
// stub with the same id.
func (b *Builder) SpliceStub(ctx context.Context, cs *stub.CompiledStub) {
	// stub.Compile has no store to read, so it leaves bodyFileName unresolved.
	// Resolving it here is what keeps the spliced answer identical to the
	// rebuilt one: without it a file-backed stub serves an empty 200 until the
	// poller catches up, and then silently starts serving the file's bytes — or
	// the 1022 that SPEC §5.2 and §6.9 require when the file is absent.
	//
	// Deliberately before the lock: this is the splice path's only I/O and it
	// has no business holding the rebuild mutex.
	if cs.Response.BodyFileName != "" {
		b.spliceBodyFile(ctx, cs)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	current := b.engine.Snapshot()
	ordered := make([]*stub.CompiledStub, 0, len(current.Ordered)+1)

	replaced := false
	for _, existing := range current.Ordered {
		if existing.ID == cs.ID {
			ordered = append(ordered, cs)
			replaced = true
			continue
		}
		ordered = append(ordered, existing)
	}
	if !replaced {
		ordered = append(ordered, cs)
	}

	// The epoch is carried forward unchanged: this pod's snapshot now reflects
	// a write the counter has already recorded, and claiming the new epoch here
	// would make the poller skip the reload that reconciles concurrent writes.
	next := BuildSnapshot(ordered, current.Epoch)
	// Carried forward for the same reason: a stub write says nothing about the
	// deployment's global delay, and dropping it here would make every
	// registration silently switch the setting off until the next reload.
	next.Settings = current.Settings
	b.engine.Swap(next)
}

// spliceBodyFile resolves one stub's body file from the store.
//
// One point read, not the rebuild's LoadAll: fetching every file to inline one
// of them is the round trip splicing exists to avoid. A stub without a
// bodyFileName never reaches here, so nobody pays for a feature they have not
// used (P2).
func (b *Builder) spliceBodyFile(ctx context.Context, cs *stub.CompiledStub) {
	files := make(map[string][]byte, 1)

	file, err := b.store.GetFile(ctx, cs.Response.BodyFileName)
	switch {
	case err == nil:
		files[file.Name] = file.Data
	case !errors.Is(err, store.ErrNotFound):
		// Unreadable is not the same as absent, but this pod cannot serve the
		// body either way, and the reload within sync_interval revisits it. The
		// admin write itself already succeeded, so failing it here would be
		// reporting a serving problem as a write problem.
		b.log.Warn("could not read body file while splicing stub",
			"id", cs.ID, "bodyFileName", cs.Response.BodyFileName, "error", err)
	}

	// The build path's own resolution, called with a one-entry map, so the two
	// paths cannot answer differently for the same stub and the same file.
	resolveBodyFile(cs, files, b.log)
}

// SpliceDelete removes a stub from the served snapshot.
func (b *Builder) SpliceDelete(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	current := b.engine.Snapshot()
	ordered := make([]*stub.CompiledStub, 0, len(current.Ordered))
	for _, existing := range current.Ordered {
		if existing.ID == id {
			continue
		}
		ordered = append(ordered, existing)
	}
	if len(ordered) == len(current.Ordered) {
		// Nothing to do; avoid churning the snapshot pointer for a no-op.
		return
	}
	next := BuildSnapshot(ordered, current.Epoch)
	next.Settings = current.Settings
	b.engine.Swap(next)
}
