// SPDX-License-Identifier: Apache-2.0

// Package memory implements every store interface in process. It backs the
// zero-config start (`docker run mockulus` with no Couchbase), the single-pod
// WireMock drop-in mode, and — per SPEC §18 — doubles as the test fake, which
// is why mockulus needs no mock-generation tooling.
//
// Everything here is guarded by one mutex: this driver is off the hot path,
// since mock traffic is served from the compiled snapshot, not the store.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/b3vet/mockulus/internal/store"
)

// Store is the in-process implementation of store.StubStore, store.ScenarioStore
// and store.JournalStore.
type Store struct {
	// ephemeralTTL mirrors `ephemeral_stub_ttl`; zero disables expiry.
	ephemeralTTL time.Duration
	// now is the clock, swappable in tests.
	now func() time.Time

	mu        sync.RWMutex
	stubs     map[string]*entry
	files     map[string][]byte
	scenarios map[string]scenarioEntry
	journal   map[string]store.JournalEntry
	epoch     uint64
	seq       uint64
	casSeq    uint64
}

type entry struct {
	stub      store.StoredStub
	expiresAt time.Time // zero means no expiry
}

type scenarioEntry struct {
	state store.ScenarioState
	cas   store.CAS
}

// New creates an empty in-memory store. ephemeralTTL is the lifetime given to
// stubs registered with `persistent:false` (SPEC §7.4, deviation #3).
func New(ephemeralTTL time.Duration) *Store {
	return &Store{
		ephemeralTTL: ephemeralTTL,
		now:          time.Now,
		stubs:        map[string]*entry{},
		files:        map[string][]byte{},
		scenarios:    map[string]scenarioEntry{},
		journal:      map[string]store.JournalEntry{},
	}
}

// SetClock replaces the driver's clock; tests use it to age documents without
// sleeping.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// LoadAll returns the full current state, dropping anything whose TTL has passed.
func (s *Store) LoadAll(_ context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	stubs := make([]store.StoredStub, 0, len(s.stubs))
	for _, e := range s.stubs {
		stubs = append(stubs, e.stub)
	}
	// A stable order keeps snapshot builds deterministic; selection order is
	// established later by priority and sequence (SPEC §5.3).
	sort.Slice(stubs, func(i, j int) bool { return stubs[i].Seq < stubs[j].Seq })

	files := make([]store.StoredFile, 0, len(s.files))
	for name, data := range s.files {
		files = append(files, store.StoredFile{Name: name, Data: data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	return stubs, files, s.epoch, nil
}

func (s *Store) expireLocked() {
	if s.ephemeralTTL <= 0 {
		return
	}
	now := s.now()
	for id, e := range s.stubs {
		if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
			delete(s.stubs, id)
		}
	}
}

// PutStub upserts a mapping, applying the ephemeral TTL when it is not persistent.
func (s *Store) PutStub(_ context.Context, stub store.StoredStub) error {
	if stub.ID == "" {
		return fmt.Errorf("memory store: stub has no id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e := &entry{stub: stub}
	if !stub.Persistent && s.ephemeralTTL > 0 {
		e.expiresAt = s.now().Add(s.ephemeralTTL)
	}
	s.stubs[stub.ID] = e
	return nil
}

// GetStub returns one mapping.
func (s *Store) GetStub(_ context.Context, id string) (store.StoredStub, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.stubs[id]
	if !ok || s.expiredLocked(e) {
		return store.StoredStub{}, store.ErrNotFound
	}
	return e.stub, nil
}

func (s *Store) expiredLocked(e *entry) bool {
	return !e.expiresAt.IsZero() && s.now().After(e.expiresAt)
}

// DeleteStub removes one mapping.
func (s *Store) DeleteStub(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.stubs, id)
	return nil
}

// DeleteAllStubs removes every mapping.
func (s *Store) DeleteAllStubs(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.stubs)
	return nil
}

// DeleteEphemeralStubs removes only the non-persistent mappings.
func (s *Store) DeleteEphemeralStubs(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.stubs {
		if !e.stub.Persistent {
			delete(s.stubs, id)
		}
	}
	return nil
}

// MarkAllPersistent makes every mapping durable and clears its expiry.
func (s *Store) MarkAllPersistent(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.stubs {
		e.stub.Persistent = true
		e.expiresAt = time.Time{}
	}
	return nil
}

// PutFile stores a response body file.
func (s *Store) PutFile(_ context.Context, file store.StoredFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[file.Name] = file.Data
	return nil
}

// GetFile returns one file.
func (s *Store) GetFile(_ context.Context, name string) (store.StoredFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.files[name]
	if !ok {
		return store.StoredFile{}, store.ErrNotFound
	}
	return store.StoredFile{Name: name, Data: data}, nil
}

// DeleteFile removes one file.
func (s *Store) DeleteFile(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, name)
	return nil
}

// ListFiles returns every stored file name, sorted.
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

// NextSeq draws the next insertion sequence.
func (s *Store) NextSeq(_ context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq, nil
}

// Epoch reads the change counter.
func (s *Store) Epoch(_ context.Context) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch, nil
}

// BumpEpoch records a change to mappings or files.
func (s *Store) BumpEpoch(_ context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	return s.epoch, nil
}

// Close releases resources; the in-memory driver holds none.
func (s *Store) Close(_ context.Context) error { return nil }

// GetScenario returns a scenario's stored state and CAS token.
func (s *Store) GetScenario(_ context.Context, name string) (store.ScenarioState, store.CAS, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.scenarios[name]
	if !ok {
		return store.ScenarioState{}, 0, store.ErrNotFound
	}
	return e.state, e.cas, nil
}

// InsertScenario creates a state document, failing if one exists.
func (s *Store) InsertScenario(_ context.Context, name string, state store.ScenarioState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.scenarios[name]; exists {
		return fmt.Errorf("scenario %q already exists", name)
	}
	s.casSeq++
	s.scenarios[name] = scenarioEntry{state: state, cas: store.CAS(s.casSeq)}
	return nil
}

// ReplaceScenario overwrites a state document only if its CAS still matches.
func (s *Store) ReplaceScenario(_ context.Context, name string, state store.ScenarioState, cas store.CAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.scenarios[name]
	if !ok {
		return store.ErrNotFound
	}
	if e.cas != cas {
		return fmt.Errorf("scenario %q: cas mismatch", name)
	}
	s.casSeq++
	s.scenarios[name] = scenarioEntry{state: state, cas: store.CAS(s.casSeq)}
	return nil
}

// UpsertScenario writes a state document unconditionally.
func (s *Store) UpsertScenario(_ context.Context, name string, state store.ScenarioState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casSeq++
	s.scenarios[name] = scenarioEntry{state: state, cas: store.CAS(s.casSeq)}
	return nil
}

// DeleteAllScenarios clears every stored state.
func (s *Store) DeleteAllScenarios(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.scenarios)
	return nil
}

// ListScenarioStates returns every stored state by name.
func (s *Store) ListScenarioStates(_ context.Context) (map[string]store.ScenarioState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]store.ScenarioState, len(s.scenarios))
	for name, e := range s.scenarios {
		out[name] = e.state
	}
	return out, nil
}

// AppendJournal writes a batch of journal entries.
func (s *Store) AppendJournal(_ context.Context, entries []store.JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		s.journal[e.ID] = e
	}
	return nil
}

// QueryJournal returns entries newest-first within the given bounds.
func (s *Store) QueryJournal(_ context.Context, q store.JournalQuery) ([]store.JournalEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	since := int64(0)
	if !q.Since.IsZero() {
		since = q.Since.UnixMilli()
	}
	out := make([]store.JournalEntry, 0, len(s.journal))
	for _, e := range s.journal {
		if e.TS >= since {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS > out[j].TS
		}
		return out[i].ID > out[j].ID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// GetJournalEntry returns one entry.
func (s *Store) GetJournalEntry(_ context.Context, id string) (store.JournalEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.journal[id]
	if !ok {
		return store.JournalEntry{}, store.ErrNotFound
	}
	return e, nil
}

// DeleteJournalEntry removes one entry.
func (s *Store) DeleteJournalEntry(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.journal, id)
	return nil
}

// ClearJournal removes every entry.
func (s *Store) ClearJournal(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.journal)
	return nil
}

// Interface checks: the memory driver must satisfy all three seams.
var (
	_ store.StubStore     = (*Store)(nil)
	_ store.ScenarioStore = (*Store)(nil)
	_ store.JournalStore  = (*Store)(nil)
)
