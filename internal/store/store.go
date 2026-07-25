// SPDX-License-Identifier: Apache-2.0

// Package store defines the persistence seam of SPEC §7.1 and the document
// shapes every driver stores. Three drivers implement it: `couchbase` for
// production, `memory` for the zero-config start and as the unit-test fake,
// and `file` for reading a WireMock `mappings/` directory during local dev.
//
// Scenario state and the request journal are separate, smaller interfaces so a
// consumer that only reads stubs cannot reach them; a driver implements as many
// as it supports.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the envelope version this build writes. A document carrying
// a newer version is quarantined rather than guessed at (SPEC §6.9).
const SchemaVersion = 1

// ErrNotFound is returned by reads for a document that does not exist.
var ErrNotFound = errors.New("not found")

// ErrUnavailable is returned when the backing store cannot be reached. Callers
// translate it into the degraded-mode behavior of SPEC §4.6.
var ErrUnavailable = errors.New("store unavailable")

// StoredStub is the envelope a stub mapping is persisted in (SPEC §7.2). The
// mapping itself is kept verbatim so that a GET returns byte-identical JSON to
// what was registered.
type StoredStub struct {
	ID            string          `json:"-"`
	SchemaVersion int             `json:"schemaVersion"`
	Seq           uint64          `json:"seq"`
	Persistent    bool            `json:"persistent"`
	CreatedAt     time.Time       `json:"createdAt"`
	Mapping       json.RawMessage `json:"mapping"`
}

// StoredFile is a response body file addressed by `bodyFileName` (SPEC §7.2).
type StoredFile struct {
	Name string
	Data []byte
}

// StoredSettings is the envelope the deployment's global settings are persisted
// in — the `meta::settings` document of SPEC §7.2. The settings JSON is kept
// verbatim for the same reason a mapping is: a GET must return what was
// written, not this build's idea of it.
type StoredSettings struct {
	SchemaVersion int             `json:"schemaVersion"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Settings      json.RawMessage `json:"settings"`
}

// PersistentMapping returns a stub mapping with its `persistent` field set to
// true, which is half of what `POST /__admin/mappings/save` records.
//
// The envelope flag alone would not do. It is what the TTL keys off, but it is
// not what a client sees: a GET returns the mapping document, and WireMock's
// save writes the flag into that document. Leaving the two out of step means a
// saved stub reads back as ephemeral, and a later PUT echoing what it read
// silently un-persists it (SPEC §5.1, deviation #4).
func PersistentMapping(mapping json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(mapping, &doc); err != nil {
		return nil, fmt.Errorf("stub mapping is not a JSON object: %w", err)
	}
	doc["persistent"] = json.RawMessage("true")
	return json.Marshal(doc)
}

// StubStore is the persistence interface the snapshot builder and the admin API
// are written against.
type StubStore interface {
	// LoadAll returns the complete current state: every mapping, every file and
	// the epoch they were read at. It is the only read on the reload path, which
	// keeps convergence level-triggered (SPEC §8).
	LoadAll(ctx context.Context) (stubs []StoredStub, files []StoredFile, epoch uint64, err error)

	// PutStub upserts at the store level; PUT-versus-POST existence semantics
	// are enforced by the admin layer (SPEC §5.1).
	PutStub(ctx context.Context, stub StoredStub) error
	// GetStub returns one mapping, or ErrNotFound.
	GetStub(ctx context.Context, id string) (StoredStub, error)
	// DeleteStub removes one mapping; removing an absent id is not an error.
	DeleteStub(ctx context.Context, id string) error
	// DeleteAllStubs removes every mapping, persistent or not.
	DeleteAllStubs(ctx context.Context) error
	// DeleteEphemeralStubs removes only non-persistent mappings, backing
	// `POST /__admin/mappings/reset`.
	DeleteEphemeralStubs(ctx context.Context) error
	// MarkAllPersistent makes every current mapping durable, backing
	// `POST /__admin/mappings/save` (SPEC §5.1, deviation #4).
	MarkAllPersistent(ctx context.Context) error

	// PutFile stores a response body file.
	PutFile(ctx context.Context, file StoredFile) error
	// GetFile returns one file, or ErrNotFound.
	GetFile(ctx context.Context, name string) (StoredFile, error)
	// DeleteFile removes one file; removing an absent name is not an error.
	DeleteFile(ctx context.Context, name string) error
	// ListFiles returns the names of every stored file.
	ListFiles(ctx context.Context) ([]string, error)

	// NextSeq draws the next cluster-global insertion sequence. It is consumed
	// once per newly created stub and reproduces WireMock's newest-wins
	// precedence across replicas (SPEC §5.3, §7.3).
	NextSeq(ctx context.Context) (uint64, error)
	// Epoch reads the change counter; this is the one call the poller makes
	// every sync_interval, so it must stay cheap (SPEC §8).
	Epoch(ctx context.Context) (uint64, error)
	// BumpEpoch records that mappings, files or settings changed.
	BumpEpoch(ctx context.Context) (uint64, error)

	// GetSettings reads the global settings document, or ErrNotFound when the
	// deployment has never written one.
	GetSettings(ctx context.Context) (StoredSettings, error)
	// PutSettings replaces the global settings document. It sits beside the
	// epoch rather than inside LoadAll because it is one small meta key, not
	// part of the bulk state that serves traffic; the caller bumps the epoch
	// after it, and that is what carries the change to the other replicas
	// (SPEC §5.1, §8).
	PutSettings(ctx context.Context, settings StoredSettings) error

	// Close releases driver resources.
	Close(ctx context.Context) error
}

// ScenarioState is the persisted state of one scenario (SPEC §7.2, §9).
type ScenarioState struct {
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CAS is an opaque compare-and-swap token; zero means "document absent".
type CAS uint64

// ScenarioStore is the small interface scenario transitions are written
// against. Its CAS operations are what make transitions correct with any
// replica count (SPEC §9.3).
type ScenarioStore interface {
	// GetScenario returns the stored state and its CAS token. A missing
	// document is reported with ErrNotFound; callers treat that as "Started".
	GetScenario(ctx context.Context, name string) (ScenarioState, CAS, error)
	// InsertScenario creates a state document, failing if one already exists.
	InsertScenario(ctx context.Context, name string, state ScenarioState) error
	// ReplaceScenario overwrites a state document only if its CAS still matches.
	ReplaceScenario(ctx context.Context, name string, state ScenarioState, cas CAS) error
	// UpsertScenario writes unconditionally, for transitions with no gate.
	UpsertScenario(ctx context.Context, name string, state ScenarioState) error
	// DeleteAllScenarios clears every state document, so all scenarios read
	// back as Started (SPEC §9.4).
	DeleteAllScenarios(ctx context.Context) error
	// ListScenarioStates returns every stored state by scenario name.
	ListScenarioStates(ctx context.Context) (map[string]ScenarioState, error)
}

// JournalEntry is one recorded request/response exchange (SPEC §11.2).
type JournalEntry struct {
	ID   string          `json:"id"`
	TS   int64           `json:"ts"`
	Pod  string          `json:"pod"`
	Data json.RawMessage `json:"-"`
}

// JournalQuery bounds a journal read (SPEC §11.3).
type JournalQuery struct {
	Since time.Time
	Limit int
}

// JournalStore is the interface the batch writer and the verification
// endpoints are written against.
type JournalStore interface {
	// AppendJournal writes a batch of entries.
	AppendJournal(ctx context.Context, entries []JournalEntry) error
	// QueryJournal returns entries newest-first within the given bounds.
	QueryJournal(ctx context.Context, q JournalQuery) ([]JournalEntry, error)
	// GetJournalEntry returns one entry, or ErrNotFound.
	GetJournalEntry(ctx context.Context, id string) (JournalEntry, error)
	// DeleteJournalEntry removes one entry.
	DeleteJournalEntry(ctx context.Context, id string) error
	// ClearJournal removes every entry.
	ClearJournal(ctx context.Context) error
}
