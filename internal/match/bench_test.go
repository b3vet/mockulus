// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/jsonpath"
	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/regexx"
	"github.com/b3vet/mockulus/internal/response"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
	"github.com/b3vet/mockulus/internal/stub"
)

// The benchmark rig for SPEC §16.1's stub-set shapes and §16.3's hot-path rules.
//
// The URLs here are deliberately of realistic length. A short path hides work
// the real one pays for — index keys are "METHOD\x00url", and the compiler only
// keeps a concatenated key off the heap while it fits a 32-byte stack buffer —
// so a benchmark built on `/x` would report an allocation count no deployment
// ever sees.

// benchStubOptions compiles the full matcher surface the shapes below use,
// which the match package's other tests do not need: only these benchmarks
// register matchesJsonPath criteria.
func benchStubOptions() stub.Options {
	return stub.Options{
		CompileRegex: func(pattern string) (matchers.PatternMatcher, error) {
			return regexx.Compile(pattern, regexx.Options{Anchored: true})
		},
		CompileJSONPath: func(expr string) (matchers.JSONPathEvaluator, error) {
			return jsonpath.NewEvaluator(expr)
		},
	}
}

// benchMetrics builds the collector set the engine records into. They are
// registered, because an unregistered collector is still incremented on the
// request path and measuring the cheaper shape would flatter the numbers.
func benchMetrics() *metrics.Metrics { return metrics.New("bench", "bench", true) }

func benchCompile(b *testing.B, seq uint64, doc string) *stub.CompiledStub {
	b.Helper()
	cs, errs := stub.Compile([]byte(doc), seq, benchStubOptions())
	if errs != nil {
		b.Fatalf("compile %s: %v", doc, errs.Errors())
	}
	cs.ID = fmt.Sprintf("bench-%04d", seq)
	return cs
}

// benchBody is the 256-byte response body the reference rig serves (§16.1).
var benchBody = func() string {
	out := make([]byte, 256)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return string(out)
}()

// The three stub shapes §16.1 S2 mixes, at 70/20/10. Each carries the same
// response so a shape's cost is its matching cost and nothing else.
func exactStub(i int) string {
	return fmt.Sprintf(`{"request":{"method":"GET","urlPath":"/api/v2/customers/%06d/orders"},`+
		`"response":{"status":200,"headers":{"Content-Type":"application/json"},"body":%q}}`, i, benchBody)
}

func regexStub(i int) string {
	return fmt.Sprintf(`{"request":{"method":"GET","urlPathPattern":"/api/v2/inventory/%06d/sku-[a-z0-9]+"},`+
		`"response":{"status":200,"headers":{"Content-Type":"application/json"},"body":%q}}`, i, benchBody)
}

func jsonPathStub(i int) string {
	return fmt.Sprintf(`{"request":{"method":"POST","urlPath":"/api/v2/payments/%06d/authorize",`+
		`"bodyPatterns":[{"matchesJsonPath":{"expression":"$.card.brand","equalTo":"visa"}}]},`+
		`"response":{"status":200,"headers":{"Content-Type":"application/json"},"body":%q}}`, i, benchBody)
}

// benchPaths returns the request each of the three shapes is hit with, in the
// same index space the stub builders use.
const (
	benchJSONBody = `{"amount":1299,"currency":"EUR","card":{"brand":"visa","last4":"4242"}}`
)

func exactPath(i int) string { return fmt.Sprintf("/api/v2/customers/%06d/orders", i) }
func regexPath(i int) string { return fmt.Sprintf("/api/v2/inventory/%06d/sku-a1b2c3", i) }
func jsonPath(i int) string  { return fmt.Sprintf("/api/v2/payments/%06d/authorize", i) }

// mixedSnapshot builds the S2 shape: n stubs at 70% exact URL, 20% regex,
// 10% JSONPath body. Sequence numbers ascend with the index, so selection order
// is the reverse of registration order and a request for a low index lands deep
// in Ordered — the arrangement that shows what a scan costs.
func mixedSnapshot(b *testing.B, n int) *Snapshot {
	b.Helper()
	stubs := make([]*stub.CompiledStub, 0, n)
	for i := range n {
		var doc string
		switch {
		case i%10 < 7:
			doc = exactStub(i)
		case i%10 < 9:
			doc = regexStub(i)
		default:
			doc = jsonPathStub(i)
		}
		stubs = append(stubs, benchCompile(b, uint64(i), doc))
	}
	return BuildSnapshot(stubs, 1)
}

func exactSnapshot(b *testing.B, n int) *Snapshot {
	b.Helper()
	stubs := make([]*stub.CompiledStub, 0, n)
	for i := range n {
		stubs = append(stubs, benchCompile(b, uint64(i), exactStub(i)))
	}
	return BuildSnapshot(stubs, 1)
}

// BenchmarkMatch covers the stub-set shapes of SPEC §16.1: S1's single exact
// stub, S2's 1,000-stub 70/20/10 mix, and the pattern- and body-heavy tails of
// that mix in isolation. net/http is excluded — this is the matcher alone.
func BenchmarkMatch(b *testing.B) {
	cases := []struct {
		name   string
		snap   func(*testing.B) *Snapshot
		method string
		target string
		body   string
		want   bool
	}{
		// S1: one stub, exact URL, GET.
		{"exact/1", func(b *testing.B) *Snapshot { return exactSnapshot(b, 1) },
			"GET", exactPath(0), "", true},
		{"exact/1000", func(b *testing.B) *Snapshot { return exactSnapshot(b, 1000) },
			"GET", exactPath(500), "", true},
		{"exact/10000", func(b *testing.B) *Snapshot { return exactSnapshot(b, 10000) },
			"GET", exactPath(5000), "", true},

		// S2's mix, hit on each of its three classes. The exact hit is the
		// dominant case; the other two say what the tail costs.
		{"mixed/1000/exact", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) },
			"GET", exactPath(500), "", true},
		{"mixed/1000/regex", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) },
			"GET", regexPath(507), "", true},
		{"mixed/1000/jsonpath", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) },
			"POST", jsonPath(509), benchJSONBody, true},

		// The worst case of the merge: nothing matches, so every pattern stub
		// is evaluated. This is the number that decides whether the linear
		// pattern list needs the index of SPEC §6.3's roadmap note.
		{"mixed/1000/unmatched", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) },
			"GET", "/api/v2/nothing/here", "", false},
		{"mixed/10000/unmatched", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 10000) },
			"GET", "/api/v2/nothing/here", "", false},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			snap := tc.snap(b)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, nil)
			body := []byte(tc.body)

			// Candidates evaluated per request is reported alongside ns/op,
			// because it is the number that explains the ns: the exact-URL
			// indexes answer in one, while every pattern stub ahead of the
			// answer in selection order has to be looked at (SPEC §6.3).
			candidates := 0
			total := 0

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				pr := AcquireRequest(req, body)
				cs := snap.Match(pr, nil, &candidates)
				ReleaseRequest(pr)
				if (cs != nil) != tc.want {
					b.Fatalf("match = %v, want %v", cs != nil, tc.want)
				}
				total += candidates
			}
			b.StopTimer()
			b.ReportMetric(float64(total)/float64(b.N), "cands/op")
		})
	}
}

// benchWriter is an http.ResponseWriter that keeps its header map across
// iterations. net/http hands a fresh one to every real request, so counting its
// growth here would measure net/http rather than the response path (§16.3
// rule 1 scopes the alloc budget to mockulus-owned code, D8).
type benchWriter struct {
	header http.Header
	status int
	n      int
}

func newBenchWriter() *benchWriter { return &benchWriter{header: make(http.Header, 4)} }

func (w *benchWriter) Header() http.Header         { return w.header }
func (w *benchWriter) WriteHeader(status int)      { w.status = status }
func (w *benchWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }

// SetWriteDeadline is here because net/http's own writer has it. Without it
// http.ResponseController falls through to its unsupported branch, which builds
// an error with fmt.Errorf — an allocation the real serve path never makes, and
// the largest one in this benchmark's profile until the rig grew this method.
func (w *benchWriter) SetWriteDeadline(time.Time) error { return nil }

func (w *benchWriter) reset() {
	clear(w.header)
	w.status, w.n = 0, 0
}

// BenchmarkMatchAndRender is the budget SPEC §16.3 rule 1 names: an exact-URL
// hit, matched and written, with net/http and its per-request allocations left
// out. TestHotPathAllocBudget asserts the ceiling; this reports the number.
func BenchmarkMatchAndRender(b *testing.B) {
	snap := exactSnapshot(b, 1000)
	req := httptest.NewRequestWithContext(context.Background(), "GET", exactPath(500), nil)
	w := newBenchWriter()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w.reset()
		pr := AcquireRequest(req, nil)
		cs := snap.Match(pr, nil, nil)
		ReleaseRequest(pr)
		if cs == nil {
			b.Fatal("expected a match")
		}
		response.Write(w, req, &cs.Response, response.Options{Settings: snap.Settings})
	}
}

// The two allocation ceilings these benchmarks report are asserted in
// allocbudget_test.go, which is excluded from race builds — see the comment
// there for why the assertion cannot live next to the benchmark.

// BenchmarkServe measures the whole mock-port handler, net/http's own
// per-request work included, which is what separates matcher cost from
// everything the engine does around it.
func BenchmarkServe(b *testing.B) {
	shapes := []struct {
		name   string
		snap   func(*testing.B) *Snapshot
		method string
		target string
		body   string
	}{
		{"exact/1", func(b *testing.B) *Snapshot { return exactSnapshot(b, 1) }, "GET", exactPath(0), ""},
		{"mixed/1000", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) }, "GET", exactPath(500), ""},
		{"unmatched/1000", func(b *testing.B) *Snapshot { return mixedSnapshot(b, 1000) }, "GET", "/api/v2/nothing/here", ""},
	}

	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			e := benchEngine(b)
			e.Swap(shape.snap(b))
			req := httptest.NewRequestWithContext(context.Background(), shape.method, shape.target, nil)
			w := newBenchWriter()

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.reset()
				e.ServeHTTP(w, req)
			}
		})
	}
}

func benchEngine(b *testing.B) *Engine {
	b.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEngine(config.Default(), benchMetrics(), log, nil)
}

// BenchmarkBuildSnapshot measures ordering and indexing alone, at the stub
// counts SPEC §16.1 S6/S7 care about. Compilation is excluded: it is the
// builder's cache that decides whether a rebuild pays for it (see
// BenchmarkRebuild).
func BenchmarkBuildSnapshot(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			stubs := make([]*stub.CompiledStub, 0, n)
			for i := range n {
				stubs = append(stubs, benchCompile(b, uint64(i), exactStub(i)))
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if snap := BuildSnapshot(stubs, 1); snap.Len() != n {
					b.Fatalf("snapshot holds %d stubs, want %d", snap.Len(), n)
				}
			}
		})
	}
}

// BenchmarkRebuild is SPEC §16.1 S7: a full reload of 10k stubs from the store,
// cold (every stub recompiled) and warm (every stub reused from the compile
// cache). The gap between the two is what the cache of SPEC §6.2 buys.
func BenchmarkRebuild(b *testing.B) {
	const stubs = 10000

	build := func(b *testing.B) (*Builder, context.Context) {
		b.Helper()
		st := memory.New(0)
		ctx := context.Background()
		for i := range stubs {
			doc := store.StoredStub{
				ID:            fmt.Sprintf("bench-%05d", i),
				SchemaVersion: store.SchemaVersion,
				Seq:           uint64(i),
				Mapping:       []byte(exactStub(i)),
			}
			if err := st.PutStub(ctx, doc); err != nil {
				b.Fatalf("put stub: %v", err)
			}
		}
		e := benchEngine(b)
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		return NewBuilder(st, e, log, benchMetrics(), benchStubOptions()), ctx
	}

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			// A fresh builder each iteration is what "cold" means: an empty
			// compile cache, as on a pod that has just started.
			builder, ctx := build(b)
			b.StartTimer()
			if err := builder.Rebuild(ctx, "bench"); err != nil {
				b.Fatalf("rebuild: %v", err)
			}
		}
	})

	b.Run("warm", func(b *testing.B) {
		builder, ctx := build(b)
		if err := builder.Rebuild(ctx, "warm-up"); err != nil {
			b.Fatalf("warm-up rebuild: %v", err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := builder.Rebuild(ctx, "bench"); err != nil {
				b.Fatalf("rebuild: %v", err)
			}
		}
	})
}

// BenchmarkSwapUnderLoad is the RCU claim of SPEC §6.2 made measurable: readers
// match against 10k stubs while a writer replaces the snapshot underneath them.
// A reader that blocked on the swap would show up here and nowhere else.
//
// Three writers, because they answer different questions. `quiescent` is the
// floor. `swapping` stores the pointer in a tight loop, isolating what a reader
// pays for the atomic alone — the writer runs unthrottled, several orders of
// magnitude above any rebuild a deployment performs, so it is an upper bound
// rather than a forecast. `rebuilding` builds a fresh 10k-stub snapshot for
// every swap, which is the event S7 actually asks about: it charges the reader
// for a core spent building and for the garbage that building makes.
//
// The writer reports its rate through b.ReportMetric, so the reader cost can be
// read against how hard it was being pushed.
func BenchmarkSwapUnderLoad(b *testing.B) {
	const stubs = 10000

	compiled := func(b *testing.B) []*stub.CompiledStub {
		out := make([]*stub.CompiledStub, 0, stubs)
		for i := range stubs {
			out = append(out, benchCompile(b, uint64(i), exactStub(i)))
		}
		return out
	}

	// next returns the snapshot the writer installs, or nil for no writer.
	run := func(b *testing.B, next func() *Snapshot) {
		e := benchEngine(b)
		e.Swap(BuildSnapshot(compiled(b), 1))

		var stop atomic.Bool
		var swaps atomic.Uint64
		done := make(chan struct{})
		go func() {
			defer close(done)
			for next != nil && !stop.Load() {
				e.Swap(next())
				swaps.Add(1)
			}
		}()

		req := httptest.NewRequestWithContext(context.Background(), "GET", exactPath(5000), nil)
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				snap := e.Snapshot()
				pr := AcquireRequest(req, nil)
				cs := snap.Match(pr, nil, nil)
				ReleaseRequest(pr)
				if cs == nil {
					b.Error("expected a match")
					return
				}
			}
		})
		b.StopTimer()
		elapsed := b.Elapsed().Seconds()
		stop.Store(true)
		<-done
		if elapsed > 0 {
			b.ReportMetric(float64(swaps.Load())/elapsed, "swaps/s")
		}
	}

	b.Run("quiescent", func(b *testing.B) { run(b, nil) })

	b.Run("swapping", func(b *testing.B) {
		// Two snapshots over the same stubs, alternated: the pointer moves, no
		// snapshot is built, so what the reader pays is the atomic and nothing
		// else.
		pair := [2]*Snapshot{BuildSnapshot(compiled(b), 2), BuildSnapshot(compiled(b), 3)}
		i := 0
		run(b, func() *Snapshot { i++; return pair[i%2] })
	})

	b.Run("rebuilding", func(b *testing.B) {
		built := compiled(b)
		var epoch uint64
		run(b, func() *Snapshot { epoch++; return BuildSnapshot(built, epoch) })
	})
}
