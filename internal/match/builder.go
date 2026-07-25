// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
)

// Quarantine reasons reported on `mockulus_snapshot_quarantined_total`
// (SPEC §6.9).
const (
	quarantineDecode = "decode"
	quarantineSchema = "schema"
	quarantineStub   = "compile"
)

// Builder rebuilds the served snapshot from store state. It is the single
// writer: admin-triggered and poller-triggered rebuilds serialise through one
// mutex, and concurrent triggers coalesce rather than queue (SPEC §6.2).
type Builder struct {
	store   store.StubStore
	engine  *Engine
	log     *slog.Logger
	metrics *metrics.Metrics

	mu sync.Mutex
	// dirty records that a change arrived while a rebuild was already running,
	// so exactly one further rebuild follows it.
	dirty atomic.Bool
}

// NewBuilder wires a builder to its store and engine.
func NewBuilder(st store.StubStore, engine *Engine, log *slog.Logger, m *metrics.Metrics) *Builder {
	return &Builder{store: st, engine: engine, log: log, metrics: m}
}

// Rebuild reloads every document from the store, compiles them and swaps the
// result in. A full reload keeps convergence level-triggered and idempotent, so
// there is no missed-event class of bug (SPEC §8).
//
// Individual bad documents never abort a build — they are quarantined so one
// unreadable stub cannot freeze propagation cluster-wide (SPEC §6.9). Only a
// store read failure abandons the rebuild, leaving the previous snapshot in
// place (SPEC §4.6).
func (b *Builder) Rebuild(ctx context.Context, trigger string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		b.dirty.Store(false)
		if err := b.rebuildOnce(ctx, trigger); err != nil {
			return err
		}
		if !b.dirty.Load() {
			return nil
		}
	}
}

// MarkDirty records that store state changed. If a rebuild is in flight it will
// run once more; otherwise the caller is expected to trigger one.
func (b *Builder) MarkDirty() { b.dirty.Store(true) }

func (b *Builder) rebuildOnce(ctx context.Context, trigger string) error {
	start := time.Now()

	stored, _, epoch, err := b.store.LoadAll(ctx)
	if err != nil {
		b.metrics.SnapshotReloadFailures.Inc()
		b.log.Error("snapshot reload failed; keeping previous snapshot",
			"trigger", trigger, "error", err)
		return fmt.Errorf("load store state: %w", err)
	}

	compiled := make([]*stub.CompiledStub, 0, len(stored))
	for _, doc := range stored {
		cs, reason := b.compile(doc)
		if cs == nil {
			b.metrics.SnapshotQuarantined.WithLabelValues(reason).Inc()
			continue
		}
		compiled = append(compiled, cs)
	}

	b.engine.Swap(BuildSnapshot(compiled, epoch))

	took := time.Since(start)
	b.metrics.SnapshotReloads.WithLabelValues(trigger).Inc()
	b.metrics.SnapshotReloadDuration.Observe(took.Seconds())
	b.log.Debug("snapshot rebuilt",
		"trigger", trigger, "epoch", epoch, "stubs", len(compiled),
		"quarantined", len(stored)-len(compiled), "took", took.String())
	return nil
}

// compile turns one stored document into a compiled stub, or reports why it was
// quarantined.
func (b *Builder) compile(doc store.StoredStub) (*stub.CompiledStub, string) {
	if doc.SchemaVersion > store.SchemaVersion {
		b.log.Warn("stub quarantined: document schema is newer than this build",
			"id", doc.ID, "schemaVersion", doc.SchemaVersion)
		return nil, quarantineSchema
	}
	if len(doc.Mapping) == 0 || !json.Valid(doc.Mapping) {
		b.log.Warn("stub quarantined: mapping is not valid JSON", "id", doc.ID)
		return nil, quarantineDecode
	}

	m, errs := stub.Parse(doc.Mapping)
	if errs != nil {
		b.log.Warn("stub quarantined: mapping does not compile",
			"id", doc.ID, "problems", len(errs.Errors()))
		return nil, quarantineStub
	}
	if m.ID == "" {
		m.ID = doc.ID
	}
	return stub.Compile(m, doc.Seq), ""
}
