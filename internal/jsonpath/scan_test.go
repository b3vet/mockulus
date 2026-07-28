// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/matchers"
)

// The scanner is a second implementation of something already verified against
// the pinned WireMock, so what has to be tested is not what it selects — the
// corpus and jsonpath_test.go pin that — but that it selects the SAME thing the
// tree path does, for every document either of them will meet. Anything below
// that bar is a rewrite that is right about the cases someone thought of.
//
// The pin: a matcher reaches the scanner by type assertion, so an evaluator
// that stopped implementing the capability would not fail to compile — it would
// quietly go back to decoding a tree per request.
var _ matchers.JSONPathScanner = (*Evaluator)(nil)

// scanBodies is the document side of the differential. Half of it is shapes a
// stub really meets and half is the ones the two paths could disagree about:
// duplicate keys, numbers a re-render would reformat, strings the decoder does
// not copy through, and documents encoding/json refuses for reasons a scan that
// stopped at the selected node would never see.
var scanBodies = []string{
	`{"amount":1299,"currency":"EUR","card":{"brand":"visa","last4":"4242"}}`,
	`{"customer":{"id":"c-1","vip":true,"age":41},"items":[{"sku":"a","qty":1},{"sku":"b","qty":2}]}`,

	// Every leaf the bare form's truthiness test distinguishes.
	`{"v":"text"}`, `{"v":""}`, `{"v":null}`, `{"v":false}`, `{"v":0}`,
	`{"v":[]}`, `{"v":[1]}`, `{"v":[null]}`, `{"v":{}}`, `{"v":{"a":1}}`,
	`{"v":[ ]}`, `{"v":{ }}`,
	`{"other":1}`, `{"v":"x"}`,

	// Duplicate keys. encoding/json assigns them in order, so the last wins —
	// including when the last is the member the rest of the path cannot be
	// found under, which "last that found something" would get wrong.
	`{"v":1,"v":2}`,
	`{"v":{"b":1},"v":{"c":2}}`,
	`{"v":{"b":1},"v":3}`,
	`{"v":3,"v":{"b":1}}`,
	`{"v":null,"v":1}`,
	`{"v":1,"v":null}`,
	`{"v":{"b":1,"b":2}}`,

	// Numbers: what the tree path holds is a float64, and an inner matcher
	// compares its rendering, so every literal that renders as something else
	// is a chance for the two paths to differ.
	`{"v":1.0}`, `{"v":1e2}`, `{"v":1E2}`, `{"v":1e+2}`, `{"v":-0}`, `{"v":0.1}`,
	`{"v":1e-7}`, `{"v":100000000000000000000}`, `{"v":1e21}`, `{"v":0.000001}`,
	`{"v":1.7976931348623157e308}`, `{"v":123456789012345678901234567890}`,
	`{"v":1e-400}`, `{"v":-1.5e-9}`,

	// Strings and keys the decoder rewrites rather than copies.
	`{"v":"a\"b"}`, `{"v":"A"}`, `{"v":"😀"}`, `{"v":"\ud800"}`,
	`{"v":"\\"}`, `{"v":"a\/b"}`, `{"v":"line\nbreak"}`,
	`{"v":"ünïcøde"}`, "{\"v\":\"\x7f\"}",
	`{"ab":1}`, `{"ünï":1}`, `{"":1}`, `{"v":1,"":2}`,

	// Whitespace in every position the grammar allows it.
	" {\n\t\"v\" : [ 1 , 2 ] , \"w\" : { } \r\n} ",

	// Arrays, including the ones an index step walks off the end of.
	`[1,2,3]`, `[]`, `[[1],[2]]`, `[{"v":1},{"v":2}]`, `[null]`,

	// Documents that are not objects at all.
	`null`, `true`, `false`, `0`, `""`, `"text"`, `1299`,

	// Documents encoding/json refuses. Every one of them is a plain non-match,
	// and the ones with a well-formed prefix are why the scan validates all the
	// way to the end rather than stopping at the node it wanted.
	``, ` `, `not json`, `{`, `[`, `{"v":}`, `{"v":1,}`, `[1,]`, `{'v':1}`,
	`{v:1}`, `{"v" 1}`, `{"v":1 "w":2}`,
	`{"v":01}`, `{"v":+1}`, `{"v":.5}`, `{"v":1.}`, `{"v":1e}`, `{"v":-}`,
	`{"v":1e+}`, `{"v":00}`, `{"v":1.2.3}`,
	`{"v":1} junk`, `{"v":1}{"w":2}`, `{"v":1}]`, `[1,2] [3]`,
	`{"v":"unterminated}`, `{"v":"\q"}`, `{"v":"\u00"}`, `{"v":"\uZZZZ"}`,
	"{\"v\":\"raw\tcontrol\"}",
	`{"v":NaN}`, `{"v":Infinity}`, `{"v":1e400}`, `{"v":-1e400}`, `{"v":1e309}`,
	`nulll`, `tru`, `truex`, "\ufeff{}",
}

// scanPaths is the expression side. The last two groups are the shapes the
// scanner must DECLINE — an indefinite path, and a definite one counted from
// the end — because declining is what keeps their answers the tree's.
var scanPaths = []string{
	"$",
	"$.v", "$.v.b", "$.v.c", "$.v.a", "$.missing", "$.v.deeper",
	"$['v']", `$["v"]`, "$['']", "$.w",
	"$.customer.id", "$.customer.vip", "$.customer.age", "$.card.brand", "$.amount",
	"$[0]", "$[1]", "$[9]", "$[0].v", "$[0][0]",
	"$.items[0].sku", "$.items[1].qty", "$.items[9].sku", "$.v[0]", "$.v[1]",
	"$.ab", "$.ünï", "$.😀",

	"$..v", "$.*", "$[*]", "$.items[0:2]", "$.items[?(@.qty > 1)]", "$..*",

	"$[-1]", "$.items[-1].sku", "$.v[-2]",
}

// TestScanMatchesTree is the equivalence D-OPEN-14 turns on: for every definite
// expression over every document, the scan and the decoded tree produce the
// same Result — the same Node, of the same Go type, found or not for the same
// reason — and agree about whether the document is JSON at all.
func TestScanMatchesTree(t *testing.T) {
	for _, expr := range scanPaths {
		p, err := Compile(expr)
		if err != nil {
			t.Fatalf("compile %q: %v", expr, err)
		}

		for _, body := range scanBodies {
			raw := []byte(body)
			var tree any
			decoded := json.Unmarshal(raw, &tree) == nil

			got, ok := p.EvalBytes(raw)
			if !p.Scannable() {
				if ok {
					t.Errorf("%q is not scannable, but EvalBytes answered for %s", expr, body)
				}
				continue
			}
			if ok != decoded {
				t.Errorf("%q over %s: the scan %s the document, encoding/json %s it",
					expr, body, accepted(ok), accepted(decoded))
				continue
			}
			if !ok {
				continue
			}

			if want := p.Eval(tree); !reflect.DeepEqual(got, want) {
				t.Errorf("%q over %s: scanned %s, the tree gives %s",
					expr, body, describe(got), describe(want))
				continue
			}

			// The bare form never decodes the node, so it reads emptiness off
			// the bytes. That is a second thing to hold to the tree rather than
			// something the Result above already covers.
			matched, mok := p.MatchBytes(raw)
			if !mok {
				t.Errorf("%q over %s: EvalBytes answered but MatchBytes did not", expr, body)
				continue
			}
			if want := p.Eval(tree).Matches(); matched != want {
				t.Errorf("%q over %s: MatchBytes = %v, the tree matches %v", expr, body, matched, want)
			}
		}
	}
}

func accepted(ok bool) string {
	if ok {
		return "accepted"
	}
	return "refused"
}

func describe(r Result) string {
	if !r.Found {
		return "nothing"
	}
	return "node " + reflect.TypeOf(r.Node).String() + " " + valueText(r.Node)
}

func valueText(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "<unrenderable>"
	}
	return string(encoded)
}

// TestScannableIsDefiniteWithoutNegativeIndices records where the two kinds of
// path part company, so narrowing or widening it is a deliberate act.
//
// Definite and scannable are not the same set, and the rows that differ are the
// point: a negative index needs a length one forward pass does not have, and a
// merged union or a length() selects a node the document does not contain, so
// there is no byte range for a scanner to come back with.
func TestScannableIsDefiniteWithoutNegativeIndices(t *testing.T) {
	cases := []struct {
		expr      string
		definite  bool
		scannable bool
	}{
		{"$", true, true},
		{"$.a.b", true, true},
		{"$['a']", true, true},
		{"$.a[0].b", true, true},
		{"$.a[10]", true, true},
		{"$.a[-1]", true, false},
		{"$.a[-1].b", true, false},
		{"$..a", false, false},
		{"$.*", false, false},
		{"$[*]", false, false},
		{"$.a[0:2]", false, false},
		{"$.a[?(@.b)]", false, false},
		{"$.a.length()", true, false},
		{"$['a','b']", true, false},
		{"$['a','b'].length()", true, false},
		{"$['a','b'].c", false, false},
		{"$.a[0,1]", false, false},
		{"$.a[*].length()", false, false},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Fatalf("compile %q: %v", c.expr, err)
		}
		if p.Definite() != c.definite {
			t.Errorf("%q: Definite = %v, want %v", c.expr, p.Definite(), c.definite)
		}
		if p.Scannable() != c.scannable {
			t.Errorf("%q: Scannable = %v, want %v", c.expr, p.Scannable(), c.scannable)
		}
	}
}

// TestBareFormScanAllocatesNothing is the point of the exercise (SPEC §16.3
// rule 1, D-OPEN-14). It is a test rather than a note in BASELINE.md because a
// number nobody runs is a comment: allocation counts are deterministic, so this
// cannot flake.
func TestBareFormScanAllocatesNothing(t *testing.T) {
	p, err := Compile("$.card.brand")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"amount":1299,"currency":"EUR","card":{"brand":"visa","last4":"4242"}}`)

	allocs := testing.AllocsPerRun(100, func() {
		if matched, ok := p.MatchBytes(raw); !ok || !matched {
			t.Fatalf("MatchBytes = %v, %v; want a match", matched, ok)
		}
	})
	if allocs != 0 {
		t.Errorf("the bare form allocates %v times per evaluation, want 0", allocs)
	}
}

// TestScanRefusesTrailingContent names the failure the whole-document pass
// prevents, because it is the one an evaluation that stopped at the selected
// node would get wrong and no corpus case would notice.
func TestScanRefusesTrailingContent(t *testing.T) {
	p, err := Compile("$.card.brand")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"card":{"brand":"visa"}} junk`,
		`{"card":{"brand":"visa"}}{"card":{"brand":"amex"}}`,
		`{"card":{"brand":"visa"},"amount":1e400}`,
		`{"card":{"brand":"visa"},"bad":01}`,
	} {
		if _, ok := p.EvalBytes([]byte(body)); ok {
			t.Errorf("%s should not scan: encoding/json refuses it", body)
		}
		if matched, ok := p.MatchBytes([]byte(body)); ok || matched {
			t.Errorf("%s: MatchBytes = %v, %v; want a refusal", body, matched, ok)
		}
	}
}

// TestScanHonoursTheDecodersNestingLimit pins the scan to encoding/json's
// depth cap. Without it a body nested past the cap would scan and match where
// the decoded path reports "not JSON" — and it is also what bounds the
// recursion here, so a hostile body cannot walk the stack down.
func TestScanHonoursTheDecodersNestingLimit(t *testing.T) {
	p, err := Compile("$")
	if err != nil {
		t.Fatal(err)
	}

	for _, depth := range []int{2, 100, maxScanDepth - 1, maxScanDepth, maxScanDepth + 1} {
		body := []byte(strings.Repeat("[", depth) + strings.Repeat("]", depth))
		var tree any
		decoded := json.Unmarshal(body, &tree) == nil

		if _, ok := p.EvalBytes(body); ok != decoded {
			t.Errorf("%d deep: the scan %s the document, encoding/json %s it",
				depth, accepted(ok), accepted(decoded))
		}
	}
}

// BenchmarkEvalDefiniteBytes is the pair to BenchmarkEvalDefinite, which
// evaluates the same path against a document someone else already decoded. The
// gap between them is what a request pays that this file removes.
func BenchmarkEvalDefiniteBytes(b *testing.B) {
	p, err := Compile("$.customer.id")
	if err != nil {
		b.Fatal(err)
	}
	raw := []byte(`{"customer":{"id":"AB123456","name":"x"},"items":[1,2,3]}`)

	b.Run("bare", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if matched, ok := p.MatchBytes(raw); !ok || !matched {
				b.Fatal("expected a match")
			}
		}
	})

	b.Run("nested", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if result, ok := p.EvalBytes(raw); !ok || result.Node != "AB123456" {
				b.Fatal("expected the selected id")
			}
		}
	})

	// The same work the other way round, decode included, which is what a
	// request paid before: json.Unmarshal plus a walk over the tree.
	b.Run("decoded", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				b.Fatal(err)
			}
			if !p.Eval(doc).Matches() {
				b.Fatal("expected a match")
			}
		}
	})
}
