// SPDX-License-Identifier: Apache-2.0

package match

import (
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
// the indexes rebuilt over it — a slice copy and a map rebuild, no I/O and no
// compilation (SPEC §4.3 step 5).
//
// The level-triggered reload still follows within sync_interval on every pod
// and reconciles concurrent writes, so the splice is an optimisation of
// latency, never the mechanism of correctness.

// SpliceStub installs a compiled stub into the served snapshot, replacing any
// stub with the same id.
func (b *Builder) SpliceStub(cs *stub.CompiledStub) {
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
	b.engine.Swap(BuildSnapshot(ordered, current.Epoch))
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
	b.engine.Swap(BuildSnapshot(ordered, current.Epoch))
}
