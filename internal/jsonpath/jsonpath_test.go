// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"encoding/json"
	"testing"
)

func doc(t *testing.T, src string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(src), &v); err != nil {
		t.Fatalf("fixture %s: %v", src, err)
	}
	return v
}

// The bare-form truthiness rules, each probed against the pinned WireMock. The
// emptiness test applies to collections only — an empty string, false and 0 all
// match, and only a null node, an empty collection or selecting nothing do not.
func TestBareFormTruthiness(t *testing.T) {
	cases := []struct {
		body string
		expr string
		want bool
		why  string
	}{
		{`{"v":"text"}`, "$.v", true, "a present non-empty string"},
		{`{"v":""}`, "$.v", true, "an EMPTY string is still a present scalar"},
		{`{"v":null}`, "$.v", false, "a selected null does not match"},
		{`{"v":false}`, "$.v", true, "false is a value, not emptiness"},
		{`{"v":0}`, "$.v", true, "zero is a value, not emptiness"},
		{`{"v":[]}`, "$.v", false, "an empty array is empty"},
		{`{"v":[1]}`, "$.v", true, "a non-empty array matches"},
		{`{"v":[null]}`, "$.v", true, "a non-empty array of nulls still matches"},
		{`{"v":{}}`, "$.v", false, "an empty object is empty"},
		{`{"v":{"a":1}}`, "$.v", true, "a non-empty object matches"},
		{`{"other":1}`, "$.v", false, "a missing key selects nothing"},
		{`{"v":"x"}`, "$.v.deeper", false, "traversing through a scalar selects nothing"},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		if got := p.Eval(doc(t, c.body)).Matches(); got != c.want {
			t.Errorf("%s on %s = %v, want %v (%s)", c.expr, c.body, got, c.want, c.why)
		}
	}
}

// The distinction the whole design turns on: a definite path returns the node,
// an indefinite one returns a list of hits. A null under $..v therefore matches
// where the same null under $.v does not.
func TestDefiniteAndIndefinitePathsDiffer(t *testing.T) {
	body := doc(t, `{"v":null}`)

	definite, err := Compile("$.v")
	if err != nil {
		t.Fatal(err)
	}
	if !definite.Definite() {
		t.Error("$.v should be a definite path")
	}
	if definite.Eval(body).Matches() {
		t.Error("$.v selecting null must not match")
	}

	indefinite, err := Compile("$..v")
	if err != nil {
		t.Fatal(err)
	}
	if indefinite.Definite() {
		t.Error("$..v should be an indefinite path")
	}
	if !indefinite.Eval(body).Matches() {
		t.Error("$..v selecting a one-element list of null must match: the LIST is not empty")
	}
}

func TestPathForms(t *testing.T) {
	body := doc(t, `{
		"store": {
			"book": [
				{"title":"a","price":10,"tags":["x","y"]},
				{"title":"b","price":25},
				{"title":"c","price":5}
			],
			"name": "corner"
		}
	}`)

	cases := []struct {
		expr string
		want []any
	}{
		{"$.store.name", []any{"corner"}},
		{"$.store.book[0].title", []any{"a"}},
		{"$['store']['name']", []any{"corner"}},
		{"$.store.book[-1].title", []any{"c"}},
		{"$.store.book[0].tags[1]", []any{"y"}},
		{"$.store.book[0:2].title", []any{"a", "b"}},
		{"$.store.book[*].title", []any{"a", "b", "c"}},
		{"$..title", []any{"a", "b", "c"}},
		{"$.store.book[?(@.price > 8)].title", []any{"a", "b"}},
		{"$.store.book[?(@.price < 8)].title", []any{"c"}},
		{"$.store.book[?(@.title == 'b')].price", []any{float64(25)}},
		{"$.store.book[?(@.tags)].title", []any{"a"}},
		{"$.store.book[?(@.price > 8 && @.price < 20)].title", []any{"a"}},
		{"$.store.book[?(@.title == 'a' || @.title == 'c')].title", []any{"a", "c"}},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		got := p.Eval(body).Values()
		if len(got) != len(c.want) {
			t.Errorf("%s selected %v, want %v", c.expr, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s [%d] = %v, want %v", c.expr, i, got[i], c.want[i])
			}
		}
	}
}

// Unsupported or malformed syntax is rejected at compile time, so a stub using
// it is refused at registration rather than never matching.
func TestMalformedExpressionsAreRejected(t *testing.T) {
	for _, expr := range []string{
		"", "x.y", "$[", "$.a[?(@.x", "$.a[nonsense]", "$..",
	} {
		if _, err := Compile(expr); err == nil {
			t.Errorf("%q should be rejected", expr)
		}
	}
}

// A filter selecting nothing is a non-match, never an error.
func TestFilterSelectingNothingIsNotAnError(t *testing.T) {
	p, err := Compile("$.items[?(@.missing == 'x')]")
	if err != nil {
		t.Fatal(err)
	}
	result := p.Eval(doc(t, `{"items":[{"a":1}]}`))
	if result.Matches() {
		t.Error("a filter matching nothing should not match")
	}
}

func BenchmarkEvalDefinite(b *testing.B) {
	p, err := Compile("$.customer.id")
	if err != nil {
		b.Fatal(err)
	}
	var body any
	_ = json.Unmarshal([]byte(`{"customer":{"id":"AB123456","name":"x"},"items":[1,2,3]}`), &body)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !p.Eval(body).Matches() {
			b.Fatal("expected a match")
		}
	}
}
