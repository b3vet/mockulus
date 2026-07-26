// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"encoding/json"
	"testing"
	"time"
)

// Both halves of this package read untrusted input, and from opposite
// directions: the expression comes from whoever registered the stub, the
// document from whoever sent the request. So there are two targets. Neither
// asserts what a path selects — the corpus and the differential suite pin that
// — only that a hostile input cannot panic, hang, or allocate without bound.

// evalBudget bounds one compile-plus-evaluate. Compilation happens on an admin
// call and evaluation on the request path, where the budget matters most: a
// stub is registered once and then evaluated against every request that reaches
// it, so a path that is merely slow is a per-request tax on the whole
// deployment.
const evalBudget = 2 * time.Second

// evalDocuments are the shapes a path is tried against: scalars and empty
// collections, because those are the ones a traversal walks off the end of, and
// one nested document deep enough to make a descent step do work.
var evalDocuments = []any{
	nil,
	"scalar",
	42.0,
	true,
	[]any{},
	map[string]any{},
	map[string]any{"a": nil},
	map[string]any{
		"customer": map[string]any{"id": "c-1", "vip": true, "age": 41.0},
		"items": []any{
			map[string]any{"sku": "a", "qty": 1.0, "status": "shipped"},
			map[string]any{"sku": "b", "qty": 2.0, "status": "pending"},
		},
		"tags": []any{"x", "y", "z"},
	},
}

// pathSeeds are the expressions the E2E corpus registers, plus the ones that
// take the parser to its edges: unclosed brackets, empty segments, filters over
// every operator, and the quoting the bracket scanner has to honour.
var pathSeeds = []string{
	"$.customer.id",
	"$.items[0].sku",
	"$.items[-1].sku",
	"$.items[0:2]",
	"$.items[:1]",
	"$.items[1:]",
	"$..sku",
	"$..*",
	"$.*",
	"$['customer']['id']",
	`$["customer"]["id"]`,
	"$.items[?(@.status == 'shipped')]",
	"$.items[?(@.qty > 1)]",
	"$.items[?(@.qty >= 1 && @.status != 'pending')]",
	"$.items[?(@.sku == 'a' || @.sku == 'b')]",
	"$.items[?(@.missing)]",
	"$[?(@.customer)]",
	"$",
	"",
	" ",
	"a.b",
	"$.",
	"$..",
	"$[",
	"$[]",
	"$[?(",
	"$[?()]",
	"$['unterminated]",
	"$.items[?(@.qty > )]",
	"$.items[?(x.qty > 1)]",
	"$[1:2:3]",
	"$[9223372036854775808]",
}

// FuzzCompile drives the expression parser, then evaluates whatever compiled.
// Evaluation is inside the target because compilation alone proves nothing
// about the tree it produced: a step the parser accepts is a step the request
// path will walk.
func FuzzCompile(f *testing.F) {
	for _, s := range pathSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, expr string) {
		start := time.Now()
		path, err := Compile(expr)
		if took := time.Since(start); took > evalBudget {
			t.Fatalf("compiling %d bytes took %s, over the %s budget", len(expr), took, evalBudget)
		}
		if err != nil {
			return
		}
		if path.Source != expr {
			t.Fatalf("Source = %q, want the input back", path.Source)
		}

		for _, doc := range evalDocuments {
			start = time.Now()
			result := path.Eval(doc)
			if took := time.Since(start); took > evalBudget {
				t.Fatalf("evaluating %q took %s, over the %s budget", expr, took, evalBudget)
			}

			// Definiteness is the distinction the whole package exists to carry
			// (SPEC §6.7): a result that claims to be definite and hands back a
			// list would make matchesJsonPath answer the other question.
			if result.Definite != path.Definite() {
				t.Fatalf("%q: result definiteness %v disagrees with the path's %v",
					expr, result.Definite, path.Definite())
			}
			if result.Definite && len(result.Hits) > 0 {
				t.Fatalf("%q: a definite result carries %d hits", expr, len(result.Hits))
			}
			if !result.Found && len(result.Values()) > 0 {
				t.Fatalf("%q: a result that found nothing yields %d values", expr, len(result.Values()))
			}
		}
	})
}

// FuzzEval fixes the expressions and fuzzes the document, which is the request
// side of the same surface: the body reaching a matchesJsonPath criterion is
// whatever the caller sent.
func FuzzEval(f *testing.F) {
	seeds := []string{
		`{"customer":{"id":"c-1"},"items":[{"sku":"a"}]}`,
		`{"a":null}`,
		`[]`,
		`{}`,
		`""`,
		`0`,
		`null`,
		`[[[[[]]]]]`,
		`{"a":{"a":{"a":{"a":1}}}}`,
		`{"items":[1,2,3,4,5,6,7,8]}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Compiled once: the target is the evaluator, so the expressions are the
	// fixed part and the document the variable one.
	paths := make([]*Path, 0, len(pathSeeds))
	for _, expr := range pathSeeds {
		if p, err := Compile(expr); err == nil {
			paths = append(paths, p)
		}
	}

	f.Fuzz(func(t *testing.T, body string) {
		var doc any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			// A body that is not JSON never reaches the evaluator: the matcher
			// parses first and treats a failure as a non-match (SPEC §6.7).
			return
		}
		for _, p := range paths {
			start := time.Now()
			p.Eval(doc)
			if took := time.Since(start); took > evalBudget {
				t.Fatalf("evaluating %q over %d bytes took %s, over the %s budget",
					p.Source, len(body), took, evalBudget)
			}
		}
	})
}
