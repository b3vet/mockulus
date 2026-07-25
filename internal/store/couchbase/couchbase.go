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
	return stubs, files, epoch, nil
}

func (s *Store) loadStubs(ctx context.Context) ([]store.StoredStub, error) {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings)
	if err != nil {
		return nil, err
	}

	out := make([]store.StoredStub, 0, len(raw))
	for id, content := range raw {
		var doc storedDoc
		if err := json.Unmarshal(content, &doc); err != nil {
			// A single undecodable document is quarantined by the snapshot
			// builder, not fatal to the load (SPEC §6.9). Carrying it through
			// with an empty mapping is what lets the builder count it.
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
	return out, nil
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
			s.log.Warn("could not read a scanned document", "id", row.ID(), "error", err)
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
			continue
		}
		if content, ok := row[name]; ok {
			out[id] = content
		}
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
	_, err := s.mappings.Upsert(keyPrefixStub+stub.ID, doc, opts)
	return wrap(err)
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
	_, err := s.mappings.Remove(keyPrefixStub+id, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	return wrap(err)
}

// DeleteAllStubs removes every mapping.
func (s *Store) DeleteAllStubs(ctx context.Context) error {
	return s.deleteWhere(ctx, collMappings, "")
}

// DeleteEphemeralStubs removes only the non-persistent mappings.
func (s *Store) DeleteEphemeralStubs(ctx context.Context) error {
	return s.deleteWhere(ctx, collMappings, "WHERE persistent = false OR persistent IS MISSING")
}

// deleteWhere issues a bulk delete. Admin-path latency is acceptable here, and
// one statement beats N round trips.
func (s *Store) deleteWhere(ctx context.Context, coll, where string) error {
	stmt := fmt.Sprintf("DELETE FROM `%s`.`%s`.`%s` %s",
		s.bucketName, s.scopeName, coll, where)
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
		if _, err := s.mappings.Upsert(id, doc, &gocb.UpsertOptions{
			Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
			// Expiry zero clears the TTL.
			Expiry: 0,
		}); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// PutFile stores a response body file.
func (s *Store) PutFile(ctx context.Context, file store.StoredFile) error {
	_, err := s.files.Upsert(keyPrefixFile+file.Name, file.Data, &gocb.UpsertOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	return wrap(err)
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
	_, err := s.files.Remove(keyPrefixFile+name, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	return wrap(err)
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
