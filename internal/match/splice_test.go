// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
)

// A test that registers a stub and immediately calls it must not race the sync
// interval. That guarantee is the whole reason the splice exists (SPEC §4.3).
func TestASplicedStubServesWithoutWaitingForAReload(t *testing.T) {
	_, builder, engine := testBuilder(t)

	builder.SpliceStub(context.Background(), mustCompile(t, 1, "spliced",
		`{"request":{"method":"GET","urlPath":"/new"},"response":{"status":200,"body":"new"}}`))

	if id := match(t, engine.Snapshot(), "GET", "/new", "", nil); id != "spliced" {
		t.Errorf("selected %q, want spliced", id)
	}
}

// A PUT is an edit, not an addition. Appending would leave both versions in the
// ordered list, and the one that then served would depend on which of them the
// sort put first — the older one, on every tie the sequence does not break.
func TestASpliceReplacesTheStubWithTheSameIDRatherThanAddingASecond(t *testing.T) {
	_, builder, engine := testBuilder(t)

	builder.SpliceStub(context.Background(), mustCompile(t, 1, "same-id",
		`{"request":{"method":"GET","urlPath":"/v"},"response":{"status":200,"body":"first"}}`))
	builder.SpliceStub(context.Background(), mustCompile(t, 2, "same-id",
		`{"request":{"method":"GET","urlPath":"/v"},"response":{"status":200,"body":"second"}}`))

	snap := engine.Snapshot()
	if snap.Len() != 1 {
		t.Fatalf("snapshot holds %d stubs after replacing one, want 1", snap.Len())
	}
	cs, ok := snap.ByID("same-id")
	if !ok {
		t.Fatal("the replaced stub is not addressable")
	}
	if string(cs.Response.Body) != "second" {
		t.Errorf("body = %q, want second", cs.Response.Body)
	}
}

// The splice must land the stub where a rebuild would have put it. Appending
// without re-sorting is the tempting shortcut, and it would make a newly
// registered high-priority stub lose to an older one until the next reload —
// exactly the staleness splicing exists to remove.
func TestASplicedStubTakesTheSelectionPositionARebuildWouldGiveIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spliced string
		want    string
	}{
		{
			name:    "a higher-precedence stub jumps ahead of what is already there",
			spliced: `{"priority":1,"request":{"method":"GET","urlPath":"/p"},"response":{"status":200}}`,
			want:    "spliced",
		},
		{
			name:    "a lower-precedence stub lands behind it",
			spliced: `{"priority":9,"request":{"method":"GET","urlPath":"/p"},"response":{"status":200}}`,
			want:    "resident",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, builder, engine := testBuilder(t)
			engine.Swap(BuildSnapshot([]*stub.CompiledStub{
				mustCompile(t, 1, "resident",
					`{"priority":5,"request":{"method":"GET","urlPath":"/p"},"response":{"status":200}}`),
			}, 4))

			builder.SpliceStub(context.Background(), mustCompile(t, 2, "spliced", tc.spliced))

			if id := match(t, engine.Snapshot(), "GET", "/p", "", nil); id != tc.want {
				t.Errorf("selected %q, want %q", id, tc.want)
			}
		})
	}
}

// The epoch is carried forward unchanged. Claiming the new one here would make
// the poller think this pod had already reconciled, and the reload that merges
// a concurrent write from another pod would never run.
func TestASpliceLeavesTheEpochWhereTheReloadCanStillSeeIt(t *testing.T) {
	_, builder, engine := testBuilder(t)
	engine.Swap(BuildSnapshot(nil, 41))

	builder.SpliceStub(context.Background(), mustCompile(t, 1, "spliced",
		`{"request":{"urlPath":"/e"},"response":{"status":200}}`))
	if got := engine.Snapshot().Epoch; got != 41 {
		t.Errorf("epoch = %d after a splice, want the carried-forward 41", got)
	}

	builder.SpliceDelete("spliced")
	if got := engine.Snapshot().Epoch; got != 41 {
		t.Errorf("epoch = %d after a splice delete, want the carried-forward 41", got)
	}
}

// A stub write says nothing about the deployment's global delay. Dropping the
// settings here would switch that delay off on every registration and back on
// at the next reload, which reads as a flaky mock rather than as a bug.
func TestASpliceCarriesTheGlobalSettingsForward(t *testing.T) {
	st, builder, engine := testBuilder(t)
	putStub(t, st, "11111111-0000-4000-8000-000000000040", "/resident")
	putSettings(t, st, store.SchemaVersion, `{"fixedDelay":25}`)
	if err := builder.Rebuild(context.Background(), "test"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if engine.Snapshot().Settings == nil {
		t.Fatal("the settings never reached the snapshot")
	}

	builder.SpliceStub(context.Background(), mustCompile(t, 2, "spliced",
		`{"request":{"urlPath":"/s"},"response":{"status":200}}`))
	if engine.Snapshot().Settings == nil {
		t.Fatal("a stub registration silently switched the global delay off")
	}

	builder.SpliceDelete("spliced")
	if engine.Snapshot().Settings == nil {
		t.Fatal("a stub deletion silently switched the global delay off")
	}
}

// The splice must never become a second, subtly different answer. A file-backed
// stub is where the two paths would most easily diverge, because stub.Compile
// has no store to read and leaves the reference unresolved: without the point
// read, the same stub serves an empty 200 until the poller catches up and then
// silently starts serving the file.
func TestASplicedFileBackedStubAnswersExactlyAsARebuiltOneDoes(t *testing.T) {
	const id = "11111111-0000-4000-8000-000000000041"
	mapping := `{"id":"` + id + `","request":{"method":"GET","urlPath":"/f"},` +
		`"response":{"status":200,"bodyFileName":"greeting.txt"}}`

	for _, tc := range []struct {
		name        string
		upload      bool
		wantMissing bool
		wantBody    string
	}{
		{"with the file already uploaded", true, false, "hello"},
		{"with the file not uploaded yet", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, builder, engine := testBuilder(t)
			if tc.upload {
				if err := st.PutFile(context.Background(), store.StoredFile{
					Name: "greeting.txt", Data: []byte("hello"),
				}); err != nil {
					t.Fatalf("put file: %v", err)
				}
			}

			builder.SpliceStub(context.Background(), mustCompile(t, 1, id, mapping))
			spliced, ok := engine.Snapshot().ByID(id)
			if !ok {
				t.Fatal("the spliced stub is not in the snapshot")
			}

			// The same document, now taken the long way round.
			putMapping(t, st, store.StoredStub{
				ID: id, SchemaVersion: store.SchemaVersion, Seq: 1,
				Mapping: json.RawMessage(mapping),
			})
			if err := builder.Rebuild(context.Background(), "test"); err != nil {
				t.Fatalf("rebuild: %v", err)
			}
			rebuilt, ok := engine.Snapshot().ByID(id)
			if !ok {
				t.Fatal("the rebuilt stub is not in the snapshot")
			}

			if spliced.Response.BodyFileMissing != tc.wantMissing {
				t.Errorf("spliced BodyFileMissing = %v, want %v",
					spliced.Response.BodyFileMissing, tc.wantMissing)
			}
			if string(spliced.Response.Body) != tc.wantBody {
				t.Errorf("spliced body = %q, want %q", spliced.Response.Body, tc.wantBody)
			}
			if spliced.Response.BodyFileMissing != rebuilt.Response.BodyFileMissing ||
				string(spliced.Response.Body) != string(rebuilt.Response.Body) {
				t.Errorf("the splice and the rebuild disagree: spliced missing=%v body=%q, "+
					"rebuilt missing=%v body=%q",
					spliced.Response.BodyFileMissing, spliced.Response.Body,
					rebuilt.Response.BodyFileMissing, rebuilt.Response.Body)
			}
		})
	}
}

// Unreadable is not the same as absent, but this pod cannot serve the body
// either way. Failing the splice would report a serving problem as a write
// problem — the admin write itself already succeeded, and the reload within
// sync_interval revisits the file.
func TestAnUnreadableBodyFileSplicesAsMissingRatherThanFailingTheWrite(t *testing.T) {
	st, builder, engine, logs := testBuilderWithLog(t)
	if err := st.PutFile(context.Background(), store.StoredFile{
		Name: "greeting.txt", Data: []byte("hello"),
	}); err != nil {
		t.Fatalf("put file: %v", err)
	}
	st.failReads(nil, nil, errors.New("store unavailable"))

	const id = "11111111-0000-4000-8000-000000000042"
	builder.SpliceStub(context.Background(), mustCompile(t, 1, id,
		`{"request":{"method":"GET","urlPath":"/f"},"response":{"status":200,"bodyFileName":"greeting.txt"}}`))

	cs, ok := engine.Snapshot().ByID(id)
	if !ok {
		t.Fatal("the stub was dropped because its file could not be read")
	}
	if !cs.Response.BodyFileMissing {
		t.Error("a file that could not be read was treated as resolved")
	}
	if !strings.Contains(logs.String(), "could not read body file") {
		t.Error("the unreadable file was not logged, so nobody can tell why the stub 500s")
	}
}

func TestASpliceDeleteStopsTheStubFromServing(t *testing.T) {
	_, builder, engine := testBuilder(t)
	builder.SpliceStub(context.Background(), mustCompile(t, 1, "doomed",
		`{"request":{"method":"GET","urlPath":"/d"},"response":{"status":200}}`))
	builder.SpliceStub(context.Background(), mustCompile(t, 2, "kept",
		`{"request":{"method":"GET","urlPath":"/k"},"response":{"status":200}}`))

	builder.SpliceDelete("doomed")

	snap := engine.Snapshot()
	if id := match(t, snap, "GET", "/d", "", nil); id != "" {
		t.Errorf("the deleted stub still serves as %q", id)
	}
	if id := match(t, snap, "GET", "/k", "", nil); id != "kept" {
		t.Errorf("deleting one stub took another with it: /k selected %q", id)
	}
	if _, ok := snap.ByID("doomed"); ok {
		t.Error("the deleted stub is still addressable by id")
	}
}

// Deleting something that is not there must not publish a new snapshot. Every
// swap costs a slice copy and a map rebuild, and a delete of an absent id is
// what `DELETE /__admin/mappings/{id}` looks like on every pod but the one that
// held the stub.
func TestDeletingAStubThatIsNotThereDoesNotChurnTheSnapshot(t *testing.T) {
	_, builder, engine := testBuilder(t)
	builder.SpliceStub(context.Background(), mustCompile(t, 1, "resident",
		`{"request":{"method":"GET","urlPath":"/r"},"response":{"status":200}}`))
	before := engine.Snapshot()

	builder.SpliceDelete("never-existed")

	if engine.Snapshot() != before {
		t.Error("deleting an absent id replaced the snapshot for nothing")
	}
}

// The splice is a read-modify-write of the snapshot pointer, and the only thing
// making it safe is the rebuild mutex. Without it two concurrent registrations
// both read the same current snapshot and the second's write discards the
// first — a stub that the admin API reported as created and that then serves
// nothing.
func TestConcurrentSplicesDoNotLoseStubs(t *testing.T) {
	_, builder, engine := testBuilder(t)

	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "concurrent-" + strconv.Itoa(i)
			builder.SpliceStub(context.Background(), mustCompile(t, uint64(i+1), id,
				`{"request":{"method":"GET","urlPath":"/c/`+strconv.Itoa(i)+`"},"response":{"status":200}}`))
		}()
	}
	wg.Wait()

	snap := engine.Snapshot()
	if snap.Len() != n {
		t.Fatalf("snapshot holds %d stubs after %d concurrent registrations", snap.Len(), n)
	}
	for i := range n {
		if _, ok := snap.ByID("concurrent-" + strconv.Itoa(i)); !ok {
			t.Errorf("stub concurrent-%d was lost", i)
		}
	}
}

// incoherence reports the first invariant a reader is entitled to assume of any
// snapshot it loads and that this one breaks: selection order holds, and every
// stub being served is reachable through the id index the admin reads use.
//
// It returns rather than failing, so a reader goroutine can report through the
// test's own goroutine instead of calling Fatal from somewhere it must not.
func incoherence(snap *Snapshot) string {
	for i := 1; i < len(snap.Ordered); i++ {
		prev, cur := snap.Ordered[i-1], snap.Ordered[i]
		if prev.Priority > cur.Priority ||
			(prev.Priority == cur.Priority && prev.Seq < cur.Seq) {
			return "selection order is broken at index " + strconv.Itoa(i)
		}
	}
	for _, cs := range snap.Ordered {
		if found, ok := snap.ByID(cs.ID); !ok || found != cs {
			return "stub " + cs.ID + " is served but not addressable by id"
		}
	}
	return ""
}

// Snapshots are published whole, so a reader must never observe one halfway
// through being assembled — not while a splice, a delete and a reload are all
// trying to replace it. This is the RCU discipline of SPEC §6.2 stated as a
// property rather than as a comment, and it is the shape of failure that a
// splice writing into the live snapshot instead of a new one would produce.
func TestAReaderNeverSeesAHalfBuiltSnapshot(t *testing.T) {
	st, builder, engine := testBuilder(t)
	for i := range 8 {
		putStub(t, st, "11111111-0000-4000-8000-00000000005"+strconv.Itoa(i), "/r"+strconv.Itoa(i))
	}

	stop := make(chan struct{})
	problems := make(chan string, 1)

	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if bad := incoherence(engine.Snapshot()); bad != "" {
				select {
				case problems <- bad:
				default:
				}
				return
			}
		}
	}()

	var writers sync.WaitGroup
	for i := range 8 {
		writers.Add(3)
		go func() {
			defer writers.Done()
			if err := builder.Rebuild(context.Background(), "test"); err != nil {
				t.Errorf("rebuild: %v", err)
			}
		}()
		go func() {
			defer writers.Done()
			builder.SpliceStub(context.Background(), mustCompile(t, uint64(100+i), "raced-"+strconv.Itoa(i),
				`{"priority":`+strconv.Itoa(i+1)+`,"request":{"method":"GET","urlPath":"/raced/`+
					strconv.Itoa(i)+`"},"response":{"status":200}}`))
		}()
		go func() {
			defer writers.Done()
			builder.SpliceDelete("raced-" + strconv.Itoa(i))
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	select {
	case bad := <-problems:
		t.Fatalf("a reader saw a snapshot it could not have served from: %s", bad)
	default:
	}
	if bad := incoherence(engine.Snapshot()); bad != "" {
		t.Fatalf("the final snapshot is incoherent: %s", bad)
	}
}
