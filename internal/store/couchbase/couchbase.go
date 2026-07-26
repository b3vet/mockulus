// SPDX-License-Identifier: Apache-2.0

// Package couchbase implements the store interfaces against Couchbase, which is
// the source of truth in a real deployment (SPEC §7.2, D3, D4, D6).
//
// The shape of this driver follows from one constraint: nothing here may be on
// the request path. Stubs are read in bulk at startup and on change, compiled
// once, and served from memory — so this package optimises for a cheap change
// signal and a fast bulk load, not for per-request latency. The two exceptions
// are deliberate and narrow: scenario state, read only by stubs that are in a
// scenario, and journal writes, which are batched and asynchronous.
package couchbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/couchbase/gocb/v2"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/store"
)

// Collection names within the configured scope (SPEC §7.2).
const (
	collMappings  = "mappings"
	collFiles     = "files"
	collScenarios = "scenarios"
	collJournal   = "journal"
	collMeta      = "meta"
)

// Document key prefixes and the fixed keys of the meta collection.
const (
	keyPrefixStub     = "stub::"
	keyPrefixFile     = "file::"
	keyPrefixScenario = "scenario::"
	keyPrefixJournal  = "journal::"

	keyEpoch    = "epoch"
	keySeq      = "seq"
	keySchema   = "schema"
	keySettings = "settings"
)

// journalIndex is the GSI the journal's time-window queries need.
const journalIndex = "ix_journal_ts"

// loadParallelism bounds the concurrent scans of a bulk load. Sixteen keeps a
// 10k-stub load in the low seconds without turning the boot of one pod into a
// thundering herd against the cluster (SPEC §7.2).
const loadParallelism = 16

// Store is the Couchbase-backed implementation of every store interface.
type Store struct {
	cluster *gocb.Cluster
	bucket  *gocb.Bucket
	scope   *gocb.Scope
	log     *slog.Logger

	mappings  *gocb.Collection
	files     *gocb.Collection
	scenarios *gocb.Collection
	journal   *gocb.Collection
	meta      *gocb.Collection

	bucketName string
	scopeName  string

	durability gocb.DurabilityLevel
	kvTimeout  time.Duration
	// scenarioKVTimeout is the tighter budget scenario reads get, because they
	// are the one store operation on the request path (SPEC §7.2, §9.2).
	scenarioKVTimeout time.Duration
	queryTimeout      time.Duration
	ephemeralTTL      time.Duration
	journalTTL        time.Duration

	// useRangeScan records whether the server supports KV range scan. It is
	// resolved once at boot and falls back to N1QL on older servers.
	useRangeScan bool

	// writes carries this pod's own mutations into the bulk read, so a reload
	// cannot answer from a disk view that predates them.
	writes writeWatermark
}

// writeWatermark holds the mutations this pod has made that no bulk read has
// yet been shown to observe.
//
// It exists because a KV range scan answers from the vbucket's *persisted*
// snapshot. A write acknowledged from memory is simply missing from the scan
// until the disk queue catches up — measured at up to ~100 ms against an idle
// single node, and the delete direction lags the same way — and nothing in the
// answer says so: the scan reports success with the document absent. A reload
// landing in that window builds a snapshot without a stub that was registered
// before it started, stamps it with the epoch that write bumped, and the poller
// of SPEC §8 then reads the two as converged. So the loss is not corrected on
// the next tick; it survives to the unconditional resync, five minutes away by
// default. Against a keyspace that was empty just before the write — a suite
// that resets and re-registers, which is the normal shape of CI usage — that
// same window empties the snapshot outright and every stub in the deployment
// stops matching.
//
// Snapshot requirements are the protocol's own answer: a scan carrying them
// either waits for the vbucket to reach the sequence number or fails, and both
// outcomes are correct here. A failure abandons the rebuild and keeps the
// previous snapshot, which is what SPEC §4.6 already asks for from a store this
// pod cannot read.
type writeWatermark struct {
	mu sync.Mutex
	// One token per vbucket, the newest written. Merging is not an
	// optimisation: gocb refuses a scan outright when two tokens name the same
	// partition with different UUIDs.
	tokens map[uint16]gocb.MutationToken
}

// note records a mutation this pod has just made.
func (w *writeWatermark) note(token *gocb.MutationToken) {
	if token == nil {
		// Only reachable with mutation tokens disabled on the connection, which
		// costs the guarantee rather than the load.
		return
	}
	vb := uint16(token.PartitionID())

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tokens == nil {
		w.tokens = make(map[uint16]gocb.MutationToken, 1)
	}
	previous, held := w.tokens[vb]
	if !held || supersedes(previous.PartitionUUID(), previous.SequenceNumber(),
		token.PartitionUUID(), token.SequenceNumber()) {
		w.tokens[vb] = *token
	}
}

// supersedes reports whether a newly seen mutation replaces the one held for
// its vbucket.
//
// A UUID and a sequence number only mean anything together. A failover starts a
// vbucket's history over and its sequence numbers begin again, so comparing the
// numbers across two UUIDs would leave a requirement pinned to a position that
// never existed on the surviving branch — and every reload after it failing on
// a scan the server can never satisfy. Across UUIDs, newest simply wins.
func supersedes(heldUUID, heldSeq, gotUUID, gotSeq uint64) bool {
	return heldUUID != gotUUID || gotSeq > heldSeq
}

// covered reports whether a read that observed one mutation has also observed
// the one still held for that vbucket, which is what makes the held one safe to
// forget. A different UUID proves nothing either way, so it does not count.
func covered(heldUUID, heldSeq, provenUUID, provenSeq uint64) bool {
	return heldUUID == provenUUID && heldSeq <= provenSeq
}

// pending returns what a bulk read has to observe, and nil when there is
// nothing — the steady state on a replica that only serves, which then pays
// nothing for any of this (P2).
func (w *writeWatermark) pending() map[uint16]gocb.MutationToken {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tokens) == 0 {
		return nil
	}
	out := make(map[uint16]gocb.MutationToken, len(w.tokens))
	for vb, token := range w.tokens {
		out[vb] = token
	}
	return out
}

// observed drops the requirements a completed read has satisfied.
//
// Dropping them is what stops a failed-over vbucket wedging every later reload
// behind a sequence number its new history does not contain. It is safe because
// a persisted snapshot only moves forward: a mutation one scan returned is
// visible to every scan after it, so the requirement has done its work.
func (w *writeWatermark) observed(proven map[uint16]gocb.MutationToken) {
	if len(proven) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for vb, token := range proven {
		current, held := w.tokens[vb]
		if held && covered(current.PartitionUUID(), current.SequenceNumber(),
			token.PartitionUUID(), token.SequenceNumber()) {
			delete(w.tokens, vb)
		}
	}
}

// consistentWith renders a requirement set in the form a scan takes.
func consistentWith(tokens map[uint16]gocb.MutationToken) *gocb.MutationState {
	if len(tokens) == 0 {
		return nil
	}
	state := gocb.NewMutationState()
	for _, token := range tokens {
		state.Add(token)
	}
	return state
}

// Open connects to Couchbase and prepares the keyspace.
//
// Connection failure is not fatal to the process: SPEC §4.4 has the pod stay
// alive and not-ready, retrying forever, because a Couchbase outage during a
// rollout must not turn into a crash loop. That retry lives in the caller; this
// returns the error.
func Open(ctx context.Context, cfg config.Config, log *slog.Logger) (*Store, error) {
	cb := cfg.Couchbase

	cluster, err := gocb.Connect(cb.ConnStr, gocb.ClusterOptions{
		Authenticator: gocb.PasswordAuthenticator{
			Username: cb.Username,
			Password: cb.Password,
		},
		TimeoutsConfig: gocb.TimeoutsConfig{
			KVTimeout:      cb.KVTimeout.D(),
			QueryTimeout:   cb.QueryTimeout.D(),
			ConnectTimeout: 10 * time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("connect to couchbase: %w", err)
	}

	bucket := cluster.Bucket(cb.Bucket)
	if err := bucket.WaitUntilReady(10*time.Second, nil); err != nil {
		_ = cluster.Close(nil)
		return nil, fmt.Errorf("bucket %s not ready: %w", cb.Bucket, err)
	}

	s := &Store{
		cluster:           cluster,
		bucket:            bucket,
		log:               log,
		bucketName:        cb.Bucket,
		scopeName:         cb.Scope,
		durability:        durabilityLevel(cb.Durability),
		kvTimeout:         cb.KVTimeout.D(),
		scenarioKVTimeout: cfg.ScenarioKVTimeout.D(),
		queryTimeout:      cb.QueryTimeout.D(),
		ephemeralTTL:      cfg.EphemeralStubTTL.D(),
		journalTTL:        cfg.JournalTTL.D(),
	}

	if cb.ManageBucket {
		if err := s.bootstrap(ctx); err != nil {
			_ = cluster.Close(nil)
			return nil, fmt.Errorf("bootstrap keyspace: %w", err)
		}
	}

	s.scope = bucket.Scope(cb.Scope)
	s.mappings = s.scope.Collection(collMappings)
	s.files = s.scope.Collection(collFiles)
	s.scenarios = s.scope.Collection(collScenarios)
	s.journal = s.scope.Collection(collJournal)
	s.meta = s.scope.Collection(collMeta)

	s.useRangeScan = s.probeRangeScan(ctx)
	if !s.useRangeScan {
		log.Info("KV range scan is unavailable; bulk loads will use N1QL",
			"hint", "range scan needs Couchbase 7.6 or newer")
		if cb.ManageBucket {
			if err := s.ensurePrimaryIndex(ctx); err != nil {
				log.Warn("could not create the primary index the N1QL fallback needs", "error", err)
			}
		}
	}

	if err := s.checkSchema(ctx); err != nil {
		_ = cluster.Close(nil)
		return nil, err
	}
	return s, nil
}

func durabilityLevel(name string) gocb.DurabilityLevel {
	if name == "majority" {
		return gocb.DurabilityLevelMajority
	}
	return gocb.DurabilityLevelNone
}

// bootstrap creates any missing collections and indexes.
//
// Doing this from the application keeps the zero-config promise: pointing
// mockulus at an empty bucket is enough. Deployments whose application user
// lacks manager rights set manage_bucket: false and apply the DDL out of band
// (SPEC §7.2).
func (s *Store) bootstrap(ctx context.Context) error {
	mgr := s.bucket.Collections()

	if s.scopeName != "_default" {
		if err := mgr.CreateScope(s.scopeName, nil); err != nil && !errors.Is(err, gocb.ErrScopeExists) {
			return fmt.Errorf("create scope %s: %w", s.scopeName, err)
		}
	}

	for _, name := range []string{collMappings, collFiles, collScenarios, collJournal, collMeta} {
		spec := gocb.CollectionSpec{Name: name, ScopeName: s.scopeName}
		if err := mgr.CreateCollection(spec, nil); err != nil && !errors.Is(err, gocb.ErrCollectionExists) {
			return fmt.Errorf("create collection %s: %w", name, err)
		}
	}

	// Collections take a moment to become usable after creation.
	if err := s.bucket.WaitUntilReady(10*time.Second, nil); err != nil {
		return fmt.Errorf("keyspace not ready after bootstrap: %w", err)
	}

	return s.ensureJournalIndex(ctx)
}

// ensureJournalIndex creates the GSI backing journal time-window queries.
func (s *Store) ensureJournalIndex(ctx context.Context) error {
	stmt := fmt.Sprintf("CREATE INDEX `%s` IF NOT EXISTS ON `%s`.`%s`.`%s`(ts)",
		journalIndex, s.bucketName, s.scopeName, collJournal)
	_, err := s.cluster.Query(stmt, &gocb.QueryOptions{Context: ctx, Timeout: s.queryTimeout})
	if err != nil && !errors.Is(err, gocb.ErrIndexExists) {
		return fmt.Errorf("create journal index: %w", err)
	}
	return nil
}

// ensurePrimaryIndex creates the primary index the N1QL bulk-load fallback
// needs. It is created only in fallback mode: on a modern server the range scan
// makes it dead weight.
func (s *Store) ensurePrimaryIndex(ctx context.Context) error {
	for _, coll := range []string{collMappings, collFiles} {
		stmt := fmt.Sprintf("CREATE PRIMARY INDEX IF NOT EXISTS ON `%s`.`%s`.`%s`",
			s.bucketName, s.scopeName, coll)
		if _, err := s.cluster.Query(stmt, &gocb.QueryOptions{
			Context: ctx, Timeout: s.queryTimeout,
		}); err != nil && !errors.Is(err, gocb.ErrIndexExists) {
			return fmt.Errorf("create primary index on %s: %w", coll, err)
		}
	}
	return nil
}

// probeRangeScan reports whether the server supports KV range scan, by trying
// one. Feature detection by attempt rather than by version string: the version
// is not always what the deployment actually supports.
func (s *Store) probeRangeScan(ctx context.Context) bool {
	scanCtx, cancel := context.WithTimeout(ctx, s.kvTimeout)
	defer cancel()

	res, err := s.mappings.Scan(gocb.RangeScan{}, &gocb.ScanOptions{
		Context: scanCtx,
		IDsOnly: true,
	})
	if err != nil {
		return false
	}
	_ = res.Close()
	return true
}

// checkSchema refuses to start against documents written by a newer build.
//
// A schema guard is cheap and the alternative is bad: an older pod silently
// mis-reading newer documents during a rolling upgrade, which is exactly when
// nobody is watching.
func (s *Store) checkSchema(ctx context.Context) error {
	type schemaDoc struct {
		SchemaVersion int `json:"schemaVersion"`
	}

	res, err := s.meta.Get(keySchema, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			_, err := s.meta.Upsert(keySchema, schemaDoc{SchemaVersion: store.SchemaVersion},
				&gocb.UpsertOptions{Context: ctx, Timeout: s.kvTimeout})
			return err
		}
		return fmt.Errorf("read the schema marker: %w", err)
	}

	var doc schemaDoc
	if err := res.Content(&doc); err != nil {
		return fmt.Errorf("decode the schema marker: %w", err)
	}
	if doc.SchemaVersion > store.SchemaVersion {
		return fmt.Errorf(
			"the keyspace holds schema version %d but this build understands %d; "+
				"upgrade mockulus rather than running an older build against newer documents",
			doc.SchemaVersion, store.SchemaVersion)
	}
	return nil
}

// Close releases the cluster connection.
func (s *Store) Close(ctx context.Context) error {
	if s.cluster == nil {
		return nil
	}
	return s.cluster.Close(nil)
}

// storedDoc is the envelope a mapping is persisted in (SPEC §7.2). The mapping
// is held verbatim so a GET returns exactly what was registered.
type storedDoc struct {
	SchemaVersion int             `json:"schemaVersion"`
	Seq           uint64          `json:"seq"`
	Persistent    bool            `json:"persistent"`
	CreatedAt     time.Time       `json:"createdAt"`
	Mapping       json.RawMessage `json:"mapping"`
}

// LoadAll reads the complete current state.
//
// This is the only read on the reload path, which is what keeps convergence
// level-triggered: any epoch change reloads everything, so there is no delta
// bookkeeping and no missed-event class of bug (SPEC §8).
func (s *Store) LoadAll(ctx context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read epoch: %w", err)
	}

	// Taken before the reads rather than after, so a write that lands while they
	// run stays pending and it is the next reload that has to account for it.
	pending := s.writes.pending()

	var (
		stubs []store.StoredStub
		files []store.StoredFile
		wg    sync.WaitGroup
		errMu sync.Mutex
		errs  []error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		loaded, err := s.loadStubs(ctx)
		errMu.Lock()
		defer errMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("load mappings: %w", err))
			return
		}
		stubs = loaded
	}()
	go func() {
		defer wg.Done()
		loaded, err := s.loadFiles(ctx)
		errMu.Lock()
		defer errMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("load files: %w", err))
			return
		}
		files = loaded
	}()
	wg.Wait()

	if len(errs) > 0 {
		return nil, nil, 0, errors.Join(errs...)
	}
	s.writes.observed(pending)
	return stubs, files, epoch, nil
}

func (s *Store) loadStubs(ctx context.Context) ([]store.StoredStub, error) {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings)
	if err != nil {
		return nil, err
	}
	return s.decodeStubs(raw), nil
}

// decodeStubs turns the documents a bulk read returned into stored stubs.
//
// Every key the read produced comes back as a stub, whatever state its document
// was in. That is the property the caller depends on: a load reports the store's
// contents or it fails, and it never quietly reports less. A document dropped
// here would be indistinguishable from one that was deleted, and the rebuild
// would take the stub out of the serving snapshot on the strength of a decode
// error (SPEC §4.6). Carried through with no mapping it reaches the builder's
// quarantine instead, where it is counted and says so (SPEC §6.9).
func (s *Store) decodeStubs(raw map[string][]byte) []store.StoredStub {
	out := make([]store.StoredStub, 0, len(raw))
	for id, content := range raw {
		var doc storedDoc
		if err := json.Unmarshal(content, &doc); err != nil {
			s.log.Warn("stored mapping does not decode; it will be quarantined",
				"id", id, "error", err)
			out = append(out, store.StoredStub{ID: strings.TrimPrefix(id, keyPrefixStub)})
			continue
		}
		out = append(out, store.StoredStub{
			ID:            strings.TrimPrefix(id, keyPrefixStub),
			SchemaVersion: doc.SchemaVersion,
			Seq:           doc.Seq,
			Persistent:    doc.Persistent,
			CreatedAt:     doc.CreatedAt,
			Mapping:       doc.Mapping,
		})
	}
	return out
}

func (s *Store) loadFiles(ctx context.Context) ([]store.StoredFile, error) {
	raw, err := s.loadCollection(ctx, s.files, collFiles)
	if err != nil {
		return nil, err
	}

	out := make([]store.StoredFile, 0, len(raw))
	for id, content := range raw {
		var data []byte
		// Files are stored base64-wrapped in JSON so any byte sequence
		// round-trips through a JSON document store unharmed.
		if err := json.Unmarshal(content, &data); err != nil {
			s.log.Warn("stored file does not decode; skipping", "name", id, "error", err)
			continue
		}
		out = append(out, store.StoredFile{
			Name: strings.TrimPrefix(id, keyPrefixFile),
			Data: data,
		})
	}
	return out, nil
}

// loadCollection reads every document of a collection, by range scan where the
// server supports it and by N1QL otherwise.
func (s *Store) loadCollection(ctx context.Context, coll *gocb.Collection, name string) (map[string][]byte, error) {
	if s.useRangeScan {
		return s.scanCollection(ctx, coll)
	}
	return s.queryCollection(ctx, name)
}

func (s *Store) scanCollection(ctx context.Context, coll *gocb.Collection) (map[string][]byte, error) {
	res, err := coll.Scan(gocb.RangeScan{}, &gocb.ScanOptions{
		Context:     ctx,
		Concurrency: loadParallelism,
		// The scan's own budget, because gocb's default for it is a separate
		// number from the KV one this driver configures — and a bulk read left
		// on that default is bounded by something no operator chose. The
		// N1QL arm of this same call already runs on the query budget, and it
		// is the same operation: one read of one collection, sized for 10k
		// stubs in low seconds (SPEC §7.2), not for a point lookup. Unbounded
		// by configuration it can also outlast resync_interval, which leaves
		// ticks queueing behind a rebuild — and the write path's splice takes
		// the same lock, so an admin registration waits it out too.
		Timeout: s.queryTimeout,
		// Sequence numbers are per vbucket and shared by every collection in the
		// bucket, so one watermark covers both scans: a token drawn from a
		// mapping write is a valid requirement for the file scan of the same
		// partition, and asking for it costs nothing extra.
		ConsistentWith: consistentWith(s.writes.pending()),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Close() }()

	out := map[string][]byte{}
	for {
		row := res.Next()
		if row == nil {
			break
		}
		var content json.RawMessage
		if err := row.Content(&content); err != nil {
			// Recorded with no content rather than left out: the document is in
			// the collection, and a bulk read that omitted it would be reporting
			// a deletion that did not happen. Its decoders answer for the
			// content; this loop answers for the set of keys.
			s.log.Warn("could not read a scanned document", "id", row.ID(), "error", err)
			out[row.ID()] = nil
			continue
		}
		out[row.ID()] = content
	}
	return out, res.Err()
}

// queryCollection is the fallback for servers without KV range scan.
func (s *Store) queryCollection(ctx context.Context, name string) (map[string][]byte, error) {
	stmt := fmt.Sprintf("SELECT META().id AS id, * FROM `%s`.`%s`.`%s`",
		s.bucketName, s.scopeName, name)
	rows, err := s.cluster.Query(stmt, &gocb.QueryOptions{
		Context: ctx,
		Timeout: s.queryTimeout,
		// The index behind this statement is maintained asynchronously, so the
		// default consistency would drop a just-registered stub exactly the way
		// an unpersisted range scan does — same silent loss, same five-minute
		// wait for the resync to undo it. This is the reload; it is allowed to
		// wait for the index to catch up.
		ScanConsistency: gocb.QueryScanConsistencyRequestPlus,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]byte{}
	for rows.Next() {
		var row map[string]json.RawMessage
		if err := rows.Row(&row); err != nil {
			return nil, err
		}
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			// Nothing to record it under, and skipping it would report a
			// document the collection holds as absent — the one answer a bulk
			// read must never give (SPEC §4.6).
			return nil, fmt.Errorf("a row of %s carries no readable id: %w", name, err)
		}
		// Present with no content for the same reason a scanned row that would
		// not decode is: the key is what this call answers for.
		out[id] = row[name]
	}
	return out, rows.Err()
}

// PutStub writes one mapping, applying the ephemeral TTL when it is not
// persistent (SPEC §7.4, deviation #3).
func (s *Store) PutStub(ctx context.Context, stub store.StoredStub) error {
	doc := storedDoc{
		SchemaVersion: stub.SchemaVersion,
		Seq:           stub.Seq,
		Persistent:    stub.Persistent,
		CreatedAt:     stub.CreatedAt,
		Mapping:       stub.Mapping,
	}
	opts := &gocb.UpsertOptions{
		Context:         ctx,
		Timeout:         s.kvTimeout,
		DurabilityLevel: s.durability,
	}
	if !stub.Persistent && s.ephemeralTTL > 0 {
		opts.Expiry = s.ephemeralTTL
	}
	res, err := s.mappings.Upsert(keyPrefixStub+stub.ID, doc, opts)
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// GetStub reads one mapping.
func (s *Store) GetStub(ctx context.Context, id string) (store.StoredStub, error) {
	res, err := s.mappings.Get(keyPrefixStub+id, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredStub{}, wrap(err)
	}
	var doc storedDoc
	if err := res.Content(&doc); err != nil {
		return store.StoredStub{}, err
	}
	return store.StoredStub{
		ID:            id,
		SchemaVersion: doc.SchemaVersion,
		Seq:           doc.Seq,
		Persistent:    doc.Persistent,
		CreatedAt:     doc.CreatedAt,
		Mapping:       doc.Mapping,
	}, nil
}

// DeleteStub removes one mapping. Removing an absent id is not an error.
func (s *Store) DeleteStub(ctx context.Context, id string) error {
	res, err := s.mappings.Remove(keyPrefixStub+id, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	// Noted for the same reason a write is: a scan reads the persisted view, so
	// a stub deleted a moment ago is still in it and would be reinstated by the
	// reload the deletion itself triggers.
	s.writes.note(res.MutationToken())
	return nil
}

// DeleteAllStubs removes every mapping.
func (s *Store) DeleteAllStubs(ctx context.Context) error {
	return s.removeMappings(ctx, func(storedDoc) bool { return true })
}

// DeleteEphemeralStubs removes only the non-persistent mappings.
func (s *Store) DeleteEphemeralStubs(ctx context.Context) error {
	return s.removeMappings(ctx, func(doc storedDoc) bool { return !doc.Persistent })
}

// removeMappings deletes the mappings a predicate selects, key by key.
//
// One `DELETE FROM` statement is a single round trip instead of N, which is
// the obvious way to do this and is wrong in both directions.
//
// The statement's own scan is a KV sequential scan of the persisted view, so a
// stub registered milliseconds earlier is not there to be deleted and survives
// the reset outright — 7 of 20 against an idle single node. Nothing corrects
// that afterwards: the document really does exist, so every later reload keeps
// serving it, and the caller was told 200. And a DML reports no mutation
// tokens, so the reload the delete triggers has nothing holding the persisted
// view past the removals and can read back the documents it did remove.
//
// Removing by key fixes both with one mechanism rather than two. The read that
// chooses the keys is the watermarked bulk read, which is exactly the guarantee
// the statement lacked — it sees this pod's own writes or it fails — and each
// removal produces the token that stops the reload resurrecting it. The cost is
// N round trips on an admin call that is already allowed to be slow, bounded by
// the same concurrency a bulk load uses.
//
// A document that will not decode has no persistent flag to read, and a missing
// flag is what ephemeral means (SPEC §7.4), so a reset takes it rather than
// leaving behind a document no reload will ever serve.
func (s *Store) removeMappings(ctx context.Context, remove func(storedDoc) bool) error {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings)
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(raw))
	for id, content := range raw {
		var doc storedDoc
		_ = json.Unmarshal(content, &doc)
		if remove(doc) {
			keys = append(keys, id)
		}
	}
	return s.removeKeys(ctx, keys)
}

// removeKeys removes a set of mapping documents, noting every token.
//
// A failure is reported rather than swallowed, so an incomplete reset reaches
// the operator as the 503 of SPEC §4.6 instead of as a keyspace that quietly
// still holds stubs. The removals that did land stay removed: a bulk delete has
// no transaction either way, and reporting the failure is what lets the caller
// retry an operation that is idempotent by construction.
func (s *Store) removeKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	workers := min(loadParallelism, len(keys))
	work := make(chan string)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
		first  error
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range work {
				res, err := s.mappings.Remove(key, &gocb.RemoveOptions{
					Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
				})
				if errors.Is(err, gocb.ErrDocumentNotFound) {
					// Another pod's reset reached it first, which is the outcome
					// this call wanted.
					continue
				}
				if err != nil {
					mu.Lock()
					failed++
					if first == nil {
						first = err
					}
					mu.Unlock()
					continue
				}
				s.writes.note(res.MutationToken())
			}
		}()
	}
	for _, key := range keys {
		work <- key
	}
	close(work)
	wg.Wait()

	if first != nil {
		return fmt.Errorf("%d of %d mappings could not be removed: %w",
			failed, len(keys), wrap(first))
	}
	return nil
}

// deleteWhere issues a bulk delete of a whole collection.
//
// The journal purge and the scenario reset use it, and for both the
// statement's staleness costs only what it deletes: neither collection is
// read by a bulk scan, so nothing can be resurrected by a reload the way a
// mapping could. What survives is the same exposure removeMappings documents —
// a document written moments earlier is not visible to the statement's scan and
// is not deleted — which those callers inherit until their own milestones land.
func (s *Store) deleteWhere(ctx context.Context, coll string) error {
	stmt := fmt.Sprintf("DELETE FROM `%s`.`%s`.`%s`", s.bucketName, s.scopeName, coll)
	_, err := s.cluster.Query(stmt, &gocb.QueryOptions{Context: ctx, Timeout: s.queryTimeout})
	return wrap(err)
}

// MarkAllPersistent makes every mapping durable and clears its expiry, backing
// `POST /__admin/mappings/save` (deviation #4).
//
// The TTL has to be cleared document by document: N1QL can set the field but
// not the expiry, and a document that still expires has not been saved.
func (s *Store) MarkAllPersistent(ctx context.Context) error {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings)
	if err != nil {
		return err
	}
	for id, content := range raw {
		var doc storedDoc
		if err := json.Unmarshal(content, &doc); err != nil {
			continue
		}
		if doc.Persistent {
			continue
		}
		mapping, err := store.PersistentMapping(doc.Mapping)
		if err != nil {
			// Undecodable documents are quarantined from serving anyway, and
			// failing the save for one would block it for every other stub.
			continue
		}
		doc.Mapping = mapping
		doc.Persistent = true
		res, err := s.mappings.Upsert(id, doc, &gocb.UpsertOptions{
			Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
			// Expiry zero clears the TTL.
			Expiry: 0,
		})
		if err != nil {
			return wrap(err)
		}
		s.writes.note(res.MutationToken())
	}
	return nil
}

// PutFile stores a response body file.
func (s *Store) PutFile(ctx context.Context, file store.StoredFile) error {
	res, err := s.files.Upsert(keyPrefixFile+file.Name, file.Data, &gocb.UpsertOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// GetFile reads one file.
func (s *Store) GetFile(ctx context.Context, name string) (store.StoredFile, error) {
	res, err := s.files.Get(keyPrefixFile+name, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredFile{}, wrap(err)
	}
	var data []byte
	if err := res.Content(&data); err != nil {
		return store.StoredFile{}, err
	}
	return store.StoredFile{Name: name, Data: data}, nil
}

// DeleteFile removes one file.
func (s *Store) DeleteFile(ctx context.Context, name string) error {
	res, err := s.files.Remove(keyPrefixFile+name, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// ListFiles returns every stored file name.
func (s *Store) ListFiles(ctx context.Context) ([]string, error) {
	raw, err := s.loadCollection(ctx, s.files, collFiles)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw))
	for id := range raw {
		names = append(names, strings.TrimPrefix(id, keyPrefixFile))
	}
	return names, nil
}

// NextSeq draws the next cluster-global insertion sequence.
//
// An atomic counter document is what makes newest-wins precedence work across
// replicas: two pods creating stubs at the same moment still get a total order
// (SPEC §5.3, §7.3).
func (s *Store) NextSeq(ctx context.Context) (uint64, error) {
	res, err := s.meta.Binary().Increment(keySeq, &gocb.IncrementOptions{
		Context: ctx, Timeout: s.kvTimeout, Initial: 1, Delta: 1,
	})
	if err != nil {
		return 0, wrap(err)
	}
	return res.Content(), nil
}

// Epoch reads the change counter. This is the one call every pod makes every
// sync interval, so it stays a single KV get of a counter document.
func (s *Store) Epoch(ctx context.Context) (uint64, error) {
	res, err := s.meta.Get(keyEpoch, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			// No writes have happened yet.
			return 0, nil
		}
		return 0, wrap(err)
	}
	var epoch uint64
	if err := res.Content(&epoch); err != nil {
		return 0, err
	}
	return epoch, nil
}

// BumpEpoch records that mappings, files or settings changed. Convergence keys
// off inequality rather than counting steps, so a lost increment costs nothing.
func (s *Store) BumpEpoch(ctx context.Context) (uint64, error) {
	res, err := s.meta.Binary().Increment(keyEpoch, &gocb.IncrementOptions{
		Context: ctx, Timeout: s.kvTimeout, Initial: 1, Delta: 1,
	})
	if err != nil {
		return 0, wrap(err)
	}
	return res.Content(), nil
}

// GetSettings reads the global settings document.
func (s *Store) GetSettings(ctx context.Context) (store.StoredSettings, error) {
	res, err := s.meta.Get(keySettings, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredSettings{}, wrap(err)
	}
	var doc store.StoredSettings
	if err := res.Content(&doc); err != nil {
		return store.StoredSettings{}, err
	}
	return doc, nil
}

// PutSettings replaces the global settings document. It is written at the
// configured durability like every other admin write: a settings change every
// replica is about to converge on must survive the node that accepted it.
func (s *Store) PutSettings(ctx context.Context, settings store.StoredSettings) error {
	_, err := s.meta.Upsert(keySettings, settings, &gocb.UpsertOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	return wrap(err)
}

// wrap translates SDK errors into the store package's sentinel errors, so
// callers never have to import gocb to tell "missing" from "broken".
func wrap(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gocb.ErrDocumentNotFound):
		return store.ErrNotFound
	case errors.Is(err, gocb.ErrTimeout),
		errors.Is(err, gocb.ErrUnambiguousTimeout),
		errors.Is(err, gocb.ErrAmbiguousTimeout),
		errors.Is(err, gocb.ErrServiceNotAvailable),
		errors.Is(err, gocb.ErrTemporaryFailure):
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	default:
		return err
	}
}

// Interface check.
var _ store.StubStore = (*Store)(nil)
