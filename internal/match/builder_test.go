// SPDX-License-Identifier: Apache-2.0

package match

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
)

// unreadableStore is the memory driver with each of the three reads a rebuild
// or a splice makes made failable, because every one of them has a degraded
// mode of its own and none of them can be provoked from a black-box run
// (SPEC §4.6).
//
// It also counts bulk reads, which is how the poller's "rebuild only when the
// counter moved" rule is made observable.
type unreadableStore struct {
	*memory.Store

	mu           sync.Mutex
	fail         error
	settingsFail error
	fileFail     error
	loads        int
}

func (s *unreadableStore) LoadAll(ctx context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	s.mu.Lock()
	s.loads++
	fail := s.fail
	s.mu.Unlock()

	if fail != nil {
		return nil, nil, 0, fail
	}
	return s.Store.LoadAll(ctx)
}

func (s *unreadableStore) GetSettings(ctx context.Context) (store.StoredSettings, error) {
	s.mu.Lock()
	fail := s.settingsFail
	s.mu.Unlock()

	if fail != nil {
		return store.StoredSettings{}, fail
	}
	return s.Store.GetSettings(ctx)
}

func (s *unreadableStore) GetFile(ctx context.Context, name string) (store.StoredFile, error) {
	s.mu.Lock()
	fail := s.fileFail
	s.mu.Unlock()

	if fail != nil {
		return store.StoredFile{}, fail
	}
	return s.Store.GetFile(ctx, name)
}

// failReads makes the named read fail from here on. It is a method rather than
// a field write so a test that arranges a failure while a poller goroutine is
// running does not race it.
func (s *unreadableStore) failReads(loadAll, settings, file error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail, s.settingsFail, s.fileFail = loadAll, settings, file
}

// loadCount reports how many bulk reads have been made.
func (s *unreadableStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

// testBuilder wires a builder over a store, returning both plus the engine it
// swaps snapshots into.
func testBuilder(t *testing.T) (*unreadableStore, *Builder, *Engine) {
	t.Helper()
	st, builder, engine, _ := testBuilderWithLog(t)
	return st, builder, engine
}

// testBuilderWithLog is testBuilder plus the buffer the builder logs into, for
// the paths whose only other evidence is a warning.
func testBuilderWithLog(t *testing.T) (*unreadableStore, *Builder, *Engine, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	st := &unreadableStore{Store: memory.New(0)}
	m := metrics.New("test", "test", false)
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	engine := NewEngine(config.Config{}, m, log, nil)
	return st, NewBuilder(st, engine, log, m, testStubOptions()), engine, logs
}

// putMapping registers an arbitrary document, which is how a stub written by a
// different build — or by a hand nobody validated — arrives in the store.
func putMapping(t *testing.T, st *unreadableStore, doc store.StoredStub) {
	t.Helper()
	if err := st.PutStub(context.Background(), doc); err != nil {
		t.Fatalf("put stub %s: %v", doc.ID, err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
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

	st.failReads(errors.New("store unavailable"), nil, nil)
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

// One document nobody can read must cost that one stub and nothing else.
// Aborting the build instead would let a single bad mapping — one written by a
// newer build, one truncated in transit, one hand-edited in the bucket — freeze
// propagation for every pod in the deployment (SPEC §6.9).
func TestOneUnreadableDocumentIsQuarantinedAndTheRestStillBuild(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  store.StoredStub
	}{
		{
			name: "a document written by a newer schema",
			doc: store.StoredStub{
				ID: "bad", SchemaVersion: store.SchemaVersion + 1, Seq: 2,
				Mapping: json.RawMessage(`{"request":{"urlPath":"/bad"},"response":{"status":200}}`),
			},
		},
		{
			name: "a mapping that is not JSON at all",
			doc: store.StoredStub{
				ID: "bad", SchemaVersion: store.SchemaVersion, Seq: 2,
				Mapping: json.RawMessage(`{"request":`),
			},
		},
		{
			name: "a mapping that is empty",
			doc: store.StoredStub{
				ID: "bad", SchemaVersion: store.SchemaVersion, Seq: 2,
				Mapping: json.RawMessage(``),
			},
		},
		{
			name: "a mapping that is valid JSON but does not compile",
			doc: store.StoredStub{
				ID: "bad", SchemaVersion: store.SchemaVersion, Seq: 2,
				Mapping: json.RawMessage(`{"request":{"urlPath":"no-leading-slash"},"response":{"status":200}}`),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, builder, engine := testBuilder(t)
			putStub(t, st, "11111111-0000-4000-8000-00000000000a", "/good")
			putMapping(t, st, tc.doc)

			if err := builder.Rebuild(context.Background(), "test"); err != nil {
				t.Fatalf("one bad document aborted the whole rebuild: %v", err)
			}

			snap := engine.Snapshot()
			if snap.Len() != 1 {
				t.Fatalf("snapshot holds %d stubs, want only the good one", snap.Len())
			}
			if id := match(t, snap, "GET", "/good", "", nil); id == "" {
				t.Error("the good stub stopped serving because of a quarantined neighbour")
			}
			if _, ok := snap.ByID("bad"); ok {
				t.Error("the quarantined stub reached the snapshot")
			}
		})
	}
}

// putSettings writes a settings document directly, the way a write this pod did
// not handle — or a build that is not this one — leaves it.
func putSettings(t *testing.T, st *unreadableStore, schemaVersion int, doc string) {
	t.Helper()
	err := st.PutSettings(context.Background(), store.StoredSettings{
		SchemaVersion: schemaVersion,
		Settings:      json.RawMessage(doc),
	})
	if err != nil {
		t.Fatalf("put settings: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
}

// Settings ride on the snapshot because the serve path already loads that
// pointer, so a rebuild that drops them switches the deployment's global delay
// off without anybody asking for it.
func TestGlobalSettingsRideOnTheSnapshot(t *testing.T) {
	st, builder, engine := testBuilder(t)
	putStub(t, st, "11111111-0000-4000-8000-00000000000b", "/s")
	putSettings(t, st, store.SchemaVersion, `{"fixedDelay":25}`)

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	got := engine.Snapshot().Settings
	if got == nil {
		t.Fatal("the snapshot carries no settings, so the global delay was silently dropped")
	}
	if got.FixedDelay != 25*time.Millisecond {
		t.Errorf("fixedDelay = %v, want 25ms", got.FixedDelay)
	}
}

// A settings document nobody can use must leave the snapshot without settings
// rather than without stubs. The admin API refuses an invalid write, so a
// stored document in any of these states came from somewhere else, and it must
// not be able to freeze propagation for the whole deployment (SPEC §6.9).
func TestUnusableSettingsAreIgnoredWithoutStoppingTheRebuild(t *testing.T) {
	for _, tc := range []struct {
		name          string
		schemaVersion int
		doc           string
	}{
		{"no settings document at all", 0, ""},
		{"a document written by a newer schema", store.SchemaVersion + 1, `{"fixedDelay":25}`},
		{"a document that does not compile", store.SchemaVersion, `{"fixedDelay":-1}`},
		{"a document naming a setting this build does not have", store.SchemaVersion, `{"invented":true}`},
		{"a document that is not JSON", store.SchemaVersion, `not json`},
		{"a document asking for nothing at all", store.SchemaVersion, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, builder, engine := testBuilder(t)
			putStub(t, st, "11111111-0000-4000-8000-00000000000c", "/s")
			if tc.doc != "" {
				putSettings(t, st, tc.schemaVersion, tc.doc)
			}

			if err := builder.Rebuild(context.Background(), "test"); err != nil {
				t.Fatalf("an unusable settings document aborted the rebuild: %v", err)
			}
			snap := engine.Snapshot()
			if snap.Settings != nil {
				t.Errorf("settings = %+v, want none", snap.Settings)
			}
			if id := match(t, snap, "GET", "/s", "", nil); id == "" {
				t.Error("stubs stopped serving because of the settings document")
			}
		})
	}
}

// A settings read that fails is not the same as a settings document that is
// unusable: nothing is known about the deployment's configuration, so half a
// snapshot would be built. Abandoning the rebuild keeps the previous one, which
// is stale but whole (SPEC §4.6).
func TestASettingsReadFailureAbandonsTheRebuildRatherThanBuildingHalfASnapshot(t *testing.T) {
	st, builder, engine := testBuilder(t)
	putStub(t, st, "11111111-0000-4000-8000-00000000000d", "/first")
	putSettings(t, st, store.SchemaVersion, `{"fixedDelay":25}`)

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	before := engine.Snapshot()

	putStub(t, st, "11111111-0000-4000-8000-00000000000e", "/second")
	st.failReads(nil, errors.New("store unavailable"), nil)

	if err := builder.Rebuild(context.Background(), "test"); err == nil {
		t.Fatal("a failed settings read reported success")
	}
	if engine.Snapshot() != before {
		t.Fatal("the snapshot was replaced despite the settings read failing")
	}
	if engine.Snapshot().Settings == nil {
		t.Error("the previous snapshot lost its settings")
	}
	if id := match(t, engine.Snapshot(), "GET", "/first", "", nil); id == "" {
		t.Error("the previous snapshot stopped serving after a failed settings read")
	}
}

// The compile cache is what makes a level-triggered reload cost what changed
// rather than what exists. Pointer identity is the assertion because sharing
// the compiled form — inlined response body and all — is the whole mechanism:
// an equal-but-distinct stub would pass a value comparison and still double the
// memory of every rebuild (SPEC §6.2).
func TestAnUnchangedDocumentIsReusedRatherThanRecompiled(t *testing.T) {
	st, builder, engine := testBuilder(t)
	const id = "11111111-0000-4000-8000-00000000000f"
	putStub(t, st, id, "/cached")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	first, ok := engine.Snapshot().ByID(id)
	if !ok {
		t.Fatal("the stub is not in the first snapshot")
	}

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	second, ok := engine.Snapshot().ByID(id)
	if !ok {
		t.Fatal("the stub is not in the second snapshot")
	}
	if first != second {
		t.Error("an unchanged document was recompiled, so every reload costs the whole store")
	}
}

// The other half of the same rule, and the reason the cache is keyed on the
// document's hash rather than on its id alone: an edit under the same id has to
// be a miss, or the pod serves the previous version of a stub forever.
func TestAnEditedDocumentUnderTheSameIDIsRecompiled(t *testing.T) {
	st, builder, engine := testBuilder(t)
	const id = "11111111-0000-4000-8000-000000000010"
	putStub(t, st, id, "/before")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	first, _ := engine.Snapshot().ByID(id)

	putStub(t, st, id, "/after")
	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	second, _ := engine.Snapshot().ByID(id)

	if first == second {
		t.Fatal("an edited document reused its previous compilation")
	}
	if id := match(t, engine.Snapshot(), "GET", "/after", "", nil); id == "" {
		t.Error("the edit did not reach the snapshot")
	}
	if id := match(t, engine.Snapshot(), "GET", "/before", "", nil); id != "" {
		t.Error("the pre-edit URL still matches")
	}
}

// A deleted stub that stays in the cache holds its compiled form — and its
// whole inlined response body — alive for the life of the process. At the sizes
// a body file reaches that is a leak, not an inefficiency.
func TestADeletedStubIsDroppedFromTheCompileCache(t *testing.T) {
	st, builder, _ := testBuilder(t)
	const keep = "11111111-0000-4000-8000-000000000011"
	const drop = "11111111-0000-4000-8000-000000000012"
	putStub(t, st, keep, "/keep")
	putStub(t, st, drop, "/drop")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if got := builder.CacheSize(); got != 2 {
		t.Fatalf("cache holds %d stubs, want 2", got)
	}

	if err := st.DeleteStub(context.Background(), drop); err != nil {
		t.Fatalf("delete stub: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}

	if got := builder.CacheSize(); got != 1 {
		t.Errorf("cache holds %d stubs after a delete, want 1", got)
	}
}

// putFileBackedStub registers a stub whose body comes from a file that may or
// may not have been uploaded yet — a legal order of operations (SPEC §6.9).
func putFileBackedStub(t *testing.T, st *unreadableStore, id, path, fileName string) {
	t.Helper()
	putMapping(t, st, store.StoredStub{
		ID: id, SchemaVersion: store.SchemaVersion, Seq: 1, Persistent: true,
		Mapping: json.RawMessage(`{"id":"` + id + `","request":{"method":"GET","urlPath":"` + path +
			`"},"response":{"status":200,"bodyFileName":"` + fileName + `"}}`),
	})
}

// A cached stub is copied before its body file is re-resolved, and the copy is
// what protects the snapshot an older request is still serving from. Resolving
// in place would reach through the cache into a snapshot that was published as
// immutable and change the body under a request already reading it.
func TestResolvingABodyFileDoesNotMutateTheSnapshotAlreadyBeingServed(t *testing.T) {
	st, builder, engine := testBuilder(t)
	const id = "11111111-0000-4000-8000-000000000013"
	putFileBackedStub(t, st, id, "/file", "greeting.txt")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	before, _ := engine.Snapshot().ByID(id)
	if !before.Response.BodyFileMissing {
		t.Fatal("a stub whose file has not been uploaded is not marked missing")
	}

	if err := st.PutFile(context.Background(), store.StoredFile{
		Name: "greeting.txt", Data: []byte("hello"),
	}); err != nil {
		t.Fatalf("put file: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}

	after, _ := engine.Snapshot().ByID(id)
	if after.Response.BodyFileMissing {
		t.Fatal("the uploaded file did not reach the new snapshot")
	}
	if string(after.Response.Body) != "hello" {
		t.Errorf("body = %q, want hello", after.Response.Body)
	}
	if before == after {
		t.Fatal("the two snapshots share one stub, so resolving the file mutated the older one")
	}
	if !before.Response.BodyFileMissing || len(before.Response.Body) != 0 {
		t.Error("the snapshot an older request is still serving was changed under it")
	}
}

// The reverse direction, which is the one an admin file delete produces: a file
// that goes away has to un-resolve, or the pod keeps serving bytes that are no
// longer in the store.
func TestAFileThatIsDeletedTurnsTheStubBackIntoAMissingBodyFile(t *testing.T) {
	st, builder, engine, logs := testBuilderWithLog(t)
	const id = "11111111-0000-4000-8000-000000000014"
	if err := st.PutFile(context.Background(), store.StoredFile{
		Name: "greeting.txt", Data: []byte("hello"),
	}); err != nil {
		t.Fatalf("put file: %v", err)
	}
	putFileBackedStub(t, st, id, "/file", "greeting.txt")

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if cs, _ := engine.Snapshot().ByID(id); string(cs.Response.Body) != "hello" {
		t.Fatalf("body = %q, want hello", cs.Response.Body)
	}

	if err := st.DeleteFile(context.Background(), "greeting.txt"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	if _, err := st.BumpEpoch(context.Background()); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}

	cs, _ := engine.Snapshot().ByID(id)
	if !cs.Response.BodyFileMissing {
		t.Error("a stub whose file was deleted still claims to have one")
	}
	if !strings.Contains(logs.String(), "greeting.txt") {
		t.Error("the missing file was not named in the log, so nobody can tell what broke")
	}
}

// Stubs registered by this build carry no id in the mapping itself, so the
// envelope's id has to become the compiled stub's — otherwise a rebuild
// produces stubs the admin API cannot address and the cache cannot retain.
func TestTheEnvelopeSuppliesTheIDAndThePersistenceFlag(t *testing.T) {
	st, builder, engine := testBuilder(t)
	const id = "11111111-0000-4000-8000-000000000015"
	putMapping(t, st, store.StoredStub{
		ID: id, SchemaVersion: store.SchemaVersion, Seq: 3, Persistent: true,
		Mapping: json.RawMessage(`{"request":{"method":"GET","urlPath":"/anon"},"response":{"status":200}}`),
	})

	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	cs, ok := engine.Snapshot().ByID(id)
	if !ok {
		t.Fatalf("the stub is not addressable by its envelope id")
	}
	if !cs.Persistent {
		t.Error("the envelope's persistence flag did not reach the compiled stub")
	}
	if cs.Seq != 3 {
		t.Errorf("seq = %d, want the envelope's 3 — precedence would be wrong", cs.Seq)
	}
}

// The cache is shared by every rebuild, and rebuilds serialise on one mutex.
// The counters it keeps for the reload log are the part that a missing lock
// would corrupt silently, so they are exercised from several goroutines at
// once rather than assumed safe.
func TestConcurrentRebuildsProduceOneCoherentSnapshot(t *testing.T) {
	st, builder, engine := testBuilder(t)
	for i := range 8 {
		putStub(t, st, "11111111-0000-4000-8000-00000000002"+string(rune('0'+i)), "/c"+string(rune('0'+i)))
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := builder.Rebuild(context.Background(), "test"); err != nil {
				t.Errorf("rebuild: %v", err)
			}
		}()
	}
	wg.Wait()

	snap := engine.Snapshot()
	if snap.Len() != 8 {
		t.Fatalf("snapshot holds %d stubs, want 8", snap.Len())
	}
	if got := builder.CacheSize(); got != 8 {
		t.Errorf("cache holds %d stubs, want 8", got)
	}
	// Every ordered entry has to be addressable, which is what a snapshot torn
	// between two builders would break.
	for _, cs := range snap.Ordered {
		if found, ok := snap.ByID(cs.ID); !ok || found != cs {
			t.Fatalf("stub %s is in Ordered but not addressable by id", cs.ID)
		}
	}
}

// A stub with no bodyFileName has no file to be missing, and must never be
// marked as though it had one — that flag is what turns a response into the
// 1022 of SPEC §6.9, and setting it here would break every ordinary stub the
// moment the guard was removed from one of the two callers.
func TestAStubWithNoBodyFileIsNeverMarkedAsMissingOne(t *testing.T) {
	cs := mustCompile(t, 1, "plain",
		`{"request":{"urlPath":"/p"},"response":{"status":200,"body":"inline"}}`)

	resolveBodyFile(cs, map[string][]byte{"greeting.txt": []byte("hello")},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	if cs.Response.BodyFileMissing {
		t.Error("a stub that names no body file was marked as missing one")
	}
	if string(cs.Response.Body) != "inline" {
		t.Errorf("body = %q, want the inline body left alone", cs.Response.Body)
	}
}
