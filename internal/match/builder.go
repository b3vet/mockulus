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
	store    store.StubStore
	engine   *Engine
	log      *slog.Logger
	metrics  *metrics.Metrics
	stubOpts stub.Options
	cache    *compileCache

	mu sync.Mutex
	// dirty records that a change arrived while a rebuild was already running,
	// so exactly one further rebuild follows it.
	dirty atomic.Bool
}

// NewBuilder wires a builder to its store and engine. stubOpts carries the
// regex policy so every compilation in the process — admin writes and snapshot
// rebuilds alike — uses exactly the same one.
func NewBuilder(st store.StubStore, engine *Engine, log *slog.Logger,
	m *metrics.Metrics, stubOpts stub.Options) *Builder {
	return &Builder{
		store:    st,
		engine:   engine,
		log:      log,
		metrics:  m,
		stubOpts: stubOpts,
		cache:    newCompileCache(),
	}
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

	stored, files, epoch, err := b.store.LoadAll(ctx)
	if err != nil {
		b.metrics.SnapshotReloadFailures.Inc()
		b.log.Error("snapshot reload failed; keeping previous snapshot",
			"trigger", trigger, "error", err)
		return fmt.Errorf("load store state: %w", err)
	}

	byName := make(map[string][]byte, len(files))
	for _, f := range files {
		byName[f.Name] = f.Data
	}

	compiled := make([]*stub.CompiledStub, 0, len(stored))
	live := make(map[string]bool, len(stored))

	for _, doc := range stored {
		live[doc.ID] = true
		cs, reason := b.compile(doc, byName)
		if cs == nil {
			b.metrics.SnapshotQuarantined.WithLabelValues(reason).Inc()
			continue
		}
		compiled = append(compiled, cs)
	}

	// A deleted stub must not keep its compiled form — and its inlined response
	// body — alive in the cache.
	b.cache.retain(live)

	b.engine.Swap(BuildSnapshot(compiled, epoch))

	took := time.Since(start)
	hits, misses := b.cache.stats()
	b.metrics.SnapshotReloads.WithLabelValues(trigger).Inc()
	b.metrics.SnapshotReloadDuration.Observe(took.Seconds())
	b.log.Debug("snapshot rebuilt",
		"trigger", trigger, "epoch", epoch, "stubs", len(compiled),
		"quarantined", len(stored)-len(compiled),
		"recompiled", misses, "reused", hits, "took", took.String())
	return nil
}

// compile turns one stored document into a compiled stub, or reports why it was
// quarantined. An unchanged document reuses its previous compilation.
func (b *Builder) compile(doc store.StoredStub, files map[string][]byte) (*stub.CompiledStub, string) {
	if doc.SchemaVersion > store.SchemaVersion {
		b.log.Warn("stub quarantined: document schema is newer than this build",
			"id", doc.ID, "schemaVersion", doc.SchemaVersion)
		return nil, quarantineSchema
	}
	if len(doc.Mapping) == 0 || !json.Valid(doc.Mapping) {
		b.log.Warn("stub quarantined: mapping is not valid JSON", "id", doc.ID)
		return nil, quarantineDecode
	}

	if cached, ok := b.cache.get(doc.ID, doc.Mapping); ok {
		// The compiled form is reused pointer and all, but the body file may
		// have appeared or vanished since, so that reference is re-resolved.
		// Copying first keeps the cached stub immutable, which matters because
		// an older snapshot may still be serving from it.
		if cached.Response.BodyFileName != "" {
			refreshed := *cached
			resolveBodyFile(&refreshed, files, b.log)
			return &refreshed, ""
		}
		return cached, ""
	}

	cs, errs := stub.Compile(doc.Mapping, doc.Seq, b.stubOpts)
	if errs != nil {
		b.log.Warn("stub quarantined: mapping does not compile",
			"id", doc.ID, "problems", len(errs.Errors()))
		return nil, quarantineStub
	}
	if cs.ID == "" {
		cs.ID = doc.ID
	}
	cs.Persistent = doc.Persistent

	// Cached before the body file is resolved, so the cached form stays the
	// pure compilation of the document and file changes are picked up above.
	b.cache.put(doc.ID, doc.Mapping, cs)

	if cs.Response.BodyFileName != "" {
		resolved := *cs
		resolveBodyFile(&resolved, files, b.log)
		return &resolved, ""
	}
	return cs, ""
}

// resolveBodyFile inlines a file-backed response body into the snapshot, so the
// request path never reads a file (P1).
//
// A stub whose file is not there still enters the snapshot, carrying an error
// response instead. Registering a stub before uploading its file is legal, and
// the later file write bumps the epoch so the next rebuild resolves it
// (SPEC §6.9).
func resolveBodyFile(cs *stub.CompiledStub, files map[string][]byte, log *slog.Logger) {
	name := cs.Response.BodyFileName
	if name == "" {
		return
	}
	data, ok := files[name]
	if !ok {
		cs.Response.BodyFileMissing = true
		log.Warn("stub references a body file that does not exist; it will serve an error until the file is uploaded",
			"id", cs.ID, "bodyFileName", name)
		return
	}
	cs.Response.BodyFileMissing = false
	cs.Response.Body = data
}

// CacheSize reports how many compiled stubs are held for reuse, which is what
// makes the cache's effect observable rather than assumed.
func (b *Builder) CacheSize() int { return b.cache.size() }
