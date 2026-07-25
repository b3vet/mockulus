// SPDX-License-Identifier: Apache-2.0

// Package file serves a WireMock project directory — `mappings/` beside
// `__files/` — as a store (SPEC §7.1). It exists for one story: a team that
// already has a WireMock project points mockulus at the directory they have,
// with no import step and no rewriting of their fixtures, and it works.
//
// The directory is the source of truth, which makes this driver read-only.
// Admin writes are refused with the store-unavailable error of SPEC §4.6 — the
// same answer a Couchbase outage earns, because the condition is the same one:
// the store cannot take the write. The alternative, keeping writes in an
// in-process overlay, was rejected: it makes a running deployment disagree with
// the files an operator is editing, and the disagreement is invisible until
// someone restarts a pod and their stub evaporates. Refusing is the loud
// failure P3 asks for.
//
// Scenario state is the one exception, and it is not project content: it is
// runtime state, WireMock keeps it in memory too, and without it every stub of
// a scenario would match at once because the engine treats an absent scenario
// gate as "no gate" rather than as "never matches".
//
// Nothing here is on the request path. The directory is read at boot and on
// each reload, compiled once, and served from the snapshot.
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
	"github.com/b3vet/mockulus/internal/stub"
)

// The WireMock project layout. The names are fixed rather than configurable:
// a tree that spells them differently is not the thing this driver claims
// compatibility with.
const (
	mappingsDir = "mappings"
	filesDir    = "__files"
)

// mappingExt is the extension WireMock loads stubs from. Anything else under
// `mappings/` — a README, an editor's swap file — is ignored rather than
// quarantined, because it never claimed to be a stub.
const mappingExt = ".json"

// idNamespace seeds the ids derived for mappings that declare none. A fixed
// namespace is what makes the derivation reproducible across processes.
var idNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/b3vet/mockulus/file-store"))

// errReadOnly is every mutating call's answer. It wraps store.ErrUnavailable so
// the admin layer produces the 503 + code 1020 of SPEC §4.6 without knowing
// which driver is underneath.
var errReadOnly = fmt.Errorf(
	"%w: the file store serves a WireMock project directory read-only; edit the mappings and restart or wait for the reload",
	store.ErrUnavailable)

// Store is the read-only view of a WireMock project directory.
type Store struct {
	root string
	log  *slog.Logger

	// scenarios holds scenario state in process; see the package comment for
	// why it is the one thing this driver accepts writes for.
	scenarios *memory.Store

	// mu guards the cache of the last load. The cache exists so an admin read
	// of one mapping or one file does not re-walk the tree; mock traffic never
	// reaches it at all.
	mu    sync.RWMutex
	stubs map[string]store.StoredStub
	files map[string][]byte
}

// Open validates the project directory and prepares the driver.
//
// A root that is not there fails startup rather than yielding an empty
// deployment. The failure a typo in a mounted path would otherwise produce is
// the worst one available: a pod that is live, ready and serving nothing, which
// Kubernetes routes traffic straight at (SPEC §4.4 step 1) — the same argument
// that has the TLS key pair loaded at config time rather than at first
// handshake.
func Open(cfg config.Config, log *slog.Logger) (*Store, error) {
	root := cfg.File.Root
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("file.root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("file.root %s is not a directory", root)
	}
	return &Store{
		root: root,
		log:  log,
		// No TTL: nothing here expires, since nothing here was written by a
		// client that might walk away and leave it behind.
		scenarios: memory.New(0),
		stubs:     map[string]store.StoredStub{},
		files:     map[string][]byte{},
	}, nil
}

// LoadAll reads the whole project directory.
//
// It is the only read on the reload path, so an edit anywhere in the tree is
// picked up by the same level-triggered rebuild that a cluster-wide change
// would be — no watcher, no delta bookkeeping (SPEC §8).
func (s *Store) LoadAll(_ context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	if err := s.checkRoot(); err != nil {
		return nil, nil, 0, err
	}

	h := fnv.New64a()

	stubs, err := s.loadStubs(h)
	if err != nil {
		return nil, nil, 0, err
	}
	files, err := s.loadFiles(h)
	if err != nil {
		return nil, nil, 0, err
	}

	byID := make(map[string]store.StoredStub, len(stubs))
	for _, doc := range stubs {
		byID[doc.ID] = doc
	}
	byName := make(map[string][]byte, len(files))
	for _, f := range files {
		byName[f.Name] = f.Data
	}

	s.mu.Lock()
	s.stubs, s.files = byID, byName
	s.mu.Unlock()

	return stubs, files, h.Sum64(), nil
}

// loadStubs reads every mapping document in the project.
//
// A file that cannot be read or does not decode is carried through with an
// empty mapping rather than dropped, so the snapshot builder quarantines and
// counts it exactly as it counts a bad document from any other driver
// (SPEC §6.9). One unparseable file must never cost a project the stubs that
// are fine, because the file being edited is by definition the one most likely
// to be broken.
func (s *Store) loadStubs(h hash.Hash64) ([]store.StoredStub, error) {
	var (
		out []store.StoredStub
		seq uint64
	)

	err := s.scan(mappingsDir, h, func(rel, path string, info fs.FileInfo) error {
		if !strings.EqualFold(filepath.Ext(rel), mappingExt) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			s.log.Warn("mapping file cannot be read; it will be quarantined",
				"file", rel, "error", err)
			id, _ := identify(nil, rel+"#0")
			seq++
			out = append(out, s.storedStub(id, nil, seq, info))
			return nil
		}

		for i, mapping := range splitMappings(data) {
			id, doc := identify(mapping, rel+"#"+strconv.Itoa(i))
			seq++
			out = append(out, s.storedStub(id, doc, seq, info))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// storedStub wraps one mapping in the envelope every driver hands the builder.
//
// Persistence is not the mapping's to declare here: `persistent:false` asks for
// the TTL of SPEC §7.4, and a document in a project directory has none — the
// file is its durability, and it outlives every process that reads it.
func (s *Store) storedStub(id string, mapping json.RawMessage, seq uint64, info fs.FileInfo) store.StoredStub {
	return store.StoredStub{
		ID:            id,
		SchemaVersion: store.SchemaVersion,
		Seq:           seq,
		Persistent:    true,
		CreatedAt:     info.ModTime().UTC(),
		Mapping:       mapping,
	}
}

// splitMappings returns the stub documents one mappings file holds. WireMock
// accepts both spellings — one stub per file, or many wrapped in a `mappings`
// array — and a real project mixes them, so a driver that only understood one
// would read half of it.
func splitMappings(data []byte) []json.RawMessage {
	var collection struct {
		Mappings []json.RawMessage `json:"mappings"`
	}
	if err := json.Unmarshal(data, &collection); err == nil && collection.Mappings != nil {
		return collection.Mappings
	}
	// Anything else is one document, including bytes that are not JSON at all:
	// deciding they are broken is the builder's job, and it is the one that
	// counts the quarantine.
	return []json.RawMessage{data}
}

// identify resolves the id a mapping is served under, stamping one in when the
// project declares none.
//
// WireMock gives every file-loaded stub a UUID and reports it in the admin
// listing, so a mapping with no id has to acquire one here or it could never be
// fetched, and would be listed without the field a WireMock client looks for.
// The derived id is a v5 UUID over the mapping's position in the project, which
// keeps it stable across restarts — an id recorded yesterday still resolves
// today. Position is the key, so reordering a collection file reassigns its
// entries' ids; a project that needs them pinned declares them, exactly as it
// would under WireMock.
//
// A document that declares an id keeps its bytes untouched, even when the id is
// unusable. Rewriting one mockulus would have rejected is how a stub ends up
// serving under an identity nobody chose: compilation refuses it instead, and
// the refusal names the field (SPEC §5.2).
func identify(mapping json.RawMessage, key string) (string, json.RawMessage) {
	derived := uuid.NewSHA1(idNamespace, []byte(key)).String()

	var declared struct {
		ID   string `json:"id"`
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(mapping, &declared); err != nil {
		// Not decodable this far: leave it alone and let the builder quarantine
		// it. The derived id is only a label for the log at that point.
		return derived, mapping
	}
	if declared.ID != "" {
		return declared.ID, mapping
	}
	if declared.UUID != "" {
		return declared.UUID, mapping
	}

	stamped, err := stub.WithIdentity(mapping, derived)
	if err != nil {
		return derived, mapping
	}
	return derived, stamped
}

// loadFiles reads `__files/`, the response bodies `bodyFileName` addresses.
func (s *Store) loadFiles(h hash.Hash64) ([]store.StoredFile, error) {
	var out []store.StoredFile

	err := s.scan(filesDir, h, func(rel, path string, _ fs.FileInfo) error {
		data, err := os.ReadFile(path)
		if err != nil {
			// A missing body file is a serve-time 1022, not a failed load
			// (SPEC §6.9), so one unreadable fixture costs its own stubs and
			// nothing else.
			s.log.Warn("body file cannot be read; stubs naming it will serve an error",
				"file", rel, "error", err)
			return nil
		}
		// A bodyFileName is written with forward slashes whatever the host uses
		// as a separator, because it is a name in the project, not a path on
		// this machine.
		out = append(out, store.StoredFile{Name: filepath.ToSlash(rel), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkRoot fails when the project directory itself has gone.
//
// A missing `mappings/` or `__files/` is a project with none of that kind of
// thing and reads as empty, but a missing root is a volume that was unmounted
// under a running pod. Reading that as "the project is empty now" would swap in
// an empty snapshot and stop answering every request; reporting it as a store
// failure keeps the last good snapshot serving and retries on the next tick,
// which is what SPEC §4.6 asks of a read that fails during a rebuild.
func (s *Store) checkRoot() error {
	if _, err := os.Stat(s.root); err != nil {
		return fmt.Errorf("%w: file.root %s: %w", store.ErrUnavailable, s.root, err)
	}
	return nil
}

// scan walks one of the project's directories, feeding every file's identity
// into the fingerprint and handing regular files to visit. A nil visit stats
// the tree and reads nothing, which is what Epoch needs.
//
// Two orderings are load-bearing. WalkDir is lexical, and that order is the
// order stubs draw their insertion sequence in, so one directory produces the
// same precedence on every boot and every replica (SPEC §5.3). And the stat
// that feeds the fingerprint happens before the read, never after: a file
// rewritten between the two is then read as its old self under its old
// fingerprint, so the next poll sees the difference and rebuilds. The other
// ordering would record the new fingerprint against the old bytes and never
// look again.
func (s *Store) scan(dir string, h hash.Hash64, visit func(rel, path string, info fs.FileInfo) error) error {
	root := filepath.Join(s.root, dir)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			// A project with no `__files/` is normal, and so is one whose
			// mappings have not been created yet.
			return nil
		}
		return fmt.Errorf("%w: %s: %w", store.ErrUnavailable, root, err)
	}

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %s: %w", store.ErrUnavailable, path, err)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", store.ErrUnavailable, path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		fmt.Fprintf(h, "%s\x00%d\x00%d\n", rel, info.Size(), info.ModTime().UnixNano())

		if visit == nil {
			return nil
		}
		return visit(rel, path, info)
	})
}

// GetStub returns one mapping from the last load.
func (s *Store) GetStub(_ context.Context, id string) (store.StoredStub, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.stubs[id]
	if !ok {
		return store.StoredStub{}, store.ErrNotFound
	}
	return doc, nil
}

// GetFile returns one response body file from the last load.
func (s *Store) GetFile(_ context.Context, name string) (store.StoredFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.files[name]
	if !ok {
		return store.StoredFile{}, store.ErrNotFound
	}
	return store.StoredFile{Name: name, Data: data}, nil
}

// ListFiles returns every body file name in the project, sorted.
func (s *Store) ListFiles(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.files))
	for name := range s.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Epoch fingerprints the project directory.
//
// This is the one call the poller makes every sync_interval (SPEC §8), so it
// stats the tree and reads none of it: an edit moves a file's size or its
// modification time, which is all the signal a reload needs. Level-triggered
// convergence keys off inequality rather than +1 steps, which is what lets a
// hash stand in for a counter here — and what makes editing a mapping in a
// running project take effect within sync_interval instead of at the next
// resync.
func (s *Store) Epoch(_ context.Context) (uint64, error) {
	if err := s.checkRoot(); err != nil {
		return 0, err
	}

	h := fnv.New64a()
	if err := s.scan(mappingsDir, h, nil); err != nil {
		return 0, err
	}
	if err := s.scan(filesDir, h, nil); err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}

// The write half of the interface. Every one of these would have to change the
// project directory, and the directory is the operator's, not ours.

// PutStub is refused: mappings come from the project.
func (s *Store) PutStub(context.Context, store.StoredStub) error { return errReadOnly }

// DeleteStub is refused: mappings come from the project.
func (s *Store) DeleteStub(context.Context, string) error { return errReadOnly }

// DeleteAllStubs is refused: mappings come from the project.
func (s *Store) DeleteAllStubs(context.Context) error { return errReadOnly }

// DeleteEphemeralStubs is refused. Nothing here is ephemeral, so the reset it
// backs would find nothing to sweep and could honestly answer 200 — but then
// half the admin write surface would work and half would not, and a client
// would have to know which half it was in. One rule for the whole driver is the
// contract worth having.
func (s *Store) DeleteEphemeralStubs(context.Context) error { return errReadOnly }

// MarkAllPersistent is refused: every mapping in a project directory already
// outlives the process, and the call is only reachable from an admin write.
func (s *Store) MarkAllPersistent(context.Context) error { return errReadOnly }

// PutFile is refused: `__files/` comes from the project.
func (s *Store) PutFile(context.Context, store.StoredFile) error { return errReadOnly }

// DeleteFile is refused: `__files/` comes from the project.
func (s *Store) DeleteFile(context.Context, string) error { return errReadOnly }

// NextSeq is refused. Only the creation of a stub draws one, and creation is a
// write.
func (s *Store) NextSeq(context.Context) (uint64, error) { return 0, errReadOnly }

// BumpEpoch is refused. The epoch of this driver is the state of the tree, so
// there is nothing to bump: it moves when a file does.
func (s *Store) BumpEpoch(context.Context) (uint64, error) { return 0, errReadOnly }

// GetSettings reports that the deployment has never written settings, which is
// the truth for a store that takes no writes: the caller falls back to the
// documented defaults, exactly as it does against an untouched deployment.
func (s *Store) GetSettings(context.Context) (store.StoredSettings, error) {
	return store.StoredSettings{}, store.ErrNotFound
}

// PutSettings is refused. Unlike scenario state, which the serve path has to be
// able to advance, this is an ordinary admin write, and accepting it into
// process memory would have `POST /__admin/settings` report a change that a
// restart quietly discards.
func (s *Store) PutSettings(context.Context, store.StoredSettings) error { return errReadOnly }

// Close releases resources; this driver holds none between calls.
func (s *Store) Close(context.Context) error { return nil }

// Scenario state, forwarded to the in-process store. It is deliberately
// writable: see the package comment.

// GetScenario returns a scenario's state and CAS token.
func (s *Store) GetScenario(ctx context.Context, name string) (store.ScenarioState, store.CAS, error) {
	return s.scenarios.GetScenario(ctx, name)
}

// InsertScenario creates a state document, failing if one already exists.
func (s *Store) InsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	return s.scenarios.InsertScenario(ctx, name, state)
}

// ReplaceScenario overwrites a state document only if its CAS still matches.
func (s *Store) ReplaceScenario(ctx context.Context, name string, state store.ScenarioState, cas store.CAS) error {
	return s.scenarios.ReplaceScenario(ctx, name, state, cas)
}

// UpsertScenario writes a state document unconditionally.
func (s *Store) UpsertScenario(ctx context.Context, name string, state store.ScenarioState) error {
	return s.scenarios.UpsertScenario(ctx, name, state)
}

// DeleteAllScenarios clears every stored state, so all scenarios read back as
// Started (SPEC §9.4).
func (s *Store) DeleteAllScenarios(ctx context.Context) error {
	return s.scenarios.DeleteAllScenarios(ctx)
}

// ListScenarioStates returns every stored state by scenario name.
func (s *Store) ListScenarioStates(ctx context.Context) (map[string]store.ScenarioState, error) {
	return s.scenarios.ListScenarioStates(ctx)
}

// Interface checks. The journal is deliberately absent: a driver that cannot
// write cannot record one, and `journal_enabled` says so at startup rather than
// dropping entries silently.
var (
	_ store.StubStore     = (*Store)(nil)
	_ store.ScenarioStore = (*Store)(nil)
)
