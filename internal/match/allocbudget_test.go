// SPDX-License-Identifier: Apache-2.0

//go:build !race

// The allocation ceilings of SPEC §16.3 rule 1 and D-OPEN-14, asserted rather
// than merely reported. They are tests because a ceiling nobody runs is a
// comment.
//
// They are in their own file, out of race builds, because the race detector
// allocates on its own account and does not do it the same number of times
// twice. Measured at a fixed 200,000 iterations on one quiet machine, the
// JSONPath hit reports 4 allocations six times out of six without `-race` and
// either 4 or 5 with it; the exact-URL hit reports 1 without and either 1 or 2
// with. So under `-race` the JSONPath assertion fails about half the time it is
// run, and the exact-URL one sits one perturbation below its budget — a gate
// that fails for the instrumentation rather than for the code.
//
// Raising the budgets instead would have been the smaller edit and the wrong
// one: `make test` runs the whole suite with `-race`, so a budget with the
// detector's slack baked in is a budget that no longer notices the regression
// it exists to catch. Excluding the file keeps the numbers honest, and
// `make test-alloc` is what runs them.
package match

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestHotPathAllocBudget enforces SPEC §16.3 rule 1: an exact-URL hit, matched
// and written, allocates at most twice in mockulus-owned code. The one
// allocation that remains is net/http's header map growing by a slot, which D8
// puts outside our control.
func TestHotPathAllocBudget(t *testing.T) {
	const budget = 2

	result := testing.Benchmark(BenchmarkMatchAndRender)
	if got := result.AllocsPerOp(); got > budget {
		t.Errorf("exact-URL hit allocates %d times per request, budget is %d (SPEC §16.3 rule 1); "+
			"run `go test -run '^$' -bench MatchAndRender -benchmem ./internal/match` and profile with -memprofile",
			got, budget)
	}
}

// TestJSONPathBodyAllocBudget holds the ceiling D-OPEN-14 bought. A
// `matchesJsonPath` body criterion used to decode the whole body into a tree to
// read one leaf — 29 allocations — and now scans the raw bytes instead. What is
// left is the seam above the evaluation: materializing the selected node as an
// `any`, and the subject its inner matcher reads.
//
// The number is here rather than only in BASELINE.md because losing it would
// not fail anything else: the scanner is reached by two type assertions, so a
// subject or an evaluator that stopped offering the capability would quietly go
// back to decoding a tree per request and every test would still pass.
func TestJSONPathBodyAllocBudget(t *testing.T) {
	const budget = 4

	result := testing.Benchmark(func(b *testing.B) {
		snap := mixedSnapshot(b, 1000)
		req := httptest.NewRequestWithContext(context.Background(), "POST", jsonPath(509), nil)
		body := []byte(benchJSONBody)

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			pr := AcquireRequest(req, body)
			cs := snap.Match(context.Background(), pr, nil, nil)
			ReleaseRequest(pr)
			if cs == nil {
				b.Fatal("expected a match")
			}
		}
	})

	if got := result.AllocsPerOp(); got > budget {
		t.Errorf("a JSONPath body hit allocates %d times per request, budget is %d (D-OPEN-14); "+
			"run `go test -run '^$' -bench Match/mixed/1000/jsonpath -benchmem ./internal/match` "+
			"and check that MatchesJSONPath still reaches the byte scanner",
			got, budget)
	}
}
