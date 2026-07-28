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

		// A union has to be a union of one kind of thing, and every member has
		// to be one. The pinned WireMock refuses each of these at registration
		// as well, so a mappings file carrying one is refused wherever it is
		// pointed.
		"$[0,'a']", "$['a',0]", "$[0,]", "$[,0]", "$['a',]", "$[0,x]", "$[0:1,2]",

		// A call this evaluator does not implement. Compiling it into a member
		// named "sum()" is the silent non-match SPEC §6.7 rules out.
		"$.xs.sum()", "$.xs.avg()", "$.xs.foo()", "$.xs.length(1)", "$..length()",

		// A union inside a predicate resolves on neither server.
		"$.items[?(@['a','b'])]", "$.items[?(@.xs[0,1] == 'a')]",
	} {
		if _, err := Compile(expr); err == nil {
			t.Errorf("%q should be rejected", expr)
		}
	}
}

// The controls for the refusals above: each is the spelling that still means a
// member, and each has to keep compiling. A parser that refused calls by
// looking for a bracket anywhere, or unions by looking for a comma anywhere,
// would pass the test above and take these with it.
func TestSpellingsThatStillNameAMemberAreAccepted(t *testing.T) {
	cases := []struct {
		expr string
		body string
		want any
	}{
		{"$.xs['length()']", `{"xs":{"length()":1}}`, float64(1)},
		{"$.xs['sum()']", `{"xs":{"sum()":2}}`, float64(2)},
		{"$.xs.length", `{"xs":{"length":3}}`, float64(3)},
		{"$['a,b']", `{"a,b":4}`, float64(4)},
		{`$["a,b"]`, `{"a,b":5}`, float64(5)},
		{"$['a']", `{"a":6}`, float64(6)},
		{"$[0]", `[7]`, float64(7)},
		{"$.items[0:2]", `{"items":[8,9]}`, float64(8)},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		values := p.Eval(doc(t, c.body)).Values()
		if len(values) == 0 || values[0] != c.want {
			t.Errorf("%s on %s selected %v, want %v first", c.expr, c.body, values, c.want)
		}
	}
}

// length() counts a collection and nothing else, which is the rule the pinned
// WireMock applies: an array's elements, an object's members, and no answer at
// all for a string, a number or a scalar in the way.
func TestLengthCountsCollections(t *testing.T) {
	cases := []struct {
		expr string
		body string
		want []any
		why  string
	}{
		{"$.xs.length()", `{"xs":[1,2]}`, []any{float64(2)}, "an array's elements"},
		{"$.xs.length()", `{"xs":[]}`, []any{float64(0)}, "an empty array counts zero rather than selecting nothing"},
		{"$.xs.length()", `{"xs":{"a":1,"b":2}}`, []any{float64(2)}, "an object's members"},
		{"$.xs.length()", `{"xs":{}}`, []any{float64(0)}, "an empty object counts zero"},
		{"$.xs.length()", `{"xs":[[1,2]]}`, []any{float64(1)}, "the outer array, not what it holds"},
		{"$.xs.length()", `{"xs":"abcd"}`, nil, "a string has no length here"},
		{"$.xs.length()", `{"xs":5}`, nil, "a number has none either"},
		{"$.xs.length()", `{"xs":null}`, nil, "and neither does a null"},
		{"$.xs.length()", `{"other":1}`, nil, "a missing member counts nothing"},
		{"$.length()", `[1,2,3]`, []any{float64(3)}, "the document itself is countable"},
		{"$.length()", `{"a":1}`, []any{float64(1)}, "including when it is an object"},
		{"$['xs'].length()", `{"xs":[1,2]}`, []any{float64(2)}, "the bracket form reaches it too"},
		{"$.xs[*].length()", `{"xs":[[1],[2,3]]}`, []any{float64(1), float64(2)}, "an indefinite prefix counts each hit"},
		{"$.o.xs.length()", `{"o":{"xs":[1]}}`, []any{float64(1)}, "at any depth"},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		got := p.Eval(doc(t, c.body)).Values()
		if len(got) != len(c.want) {
			t.Errorf("%s on %s selected %v, want %v (%s)", c.expr, c.body, got, c.want, c.why)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s on %s [%d] = %v, want %v (%s)", c.expr, c.body, i, got[i], c.want[i], c.why)
			}
		}
	}
}

// A count of zero is a present value, so the bare form matches on it. This is
// the case a truthiness test written as "is the result non-empty" gets wrong,
// and it is the same rule that makes `{"v":0}` match `$.v`.
func TestLengthOfAnEmptyCollectionStillMatches(t *testing.T) {
	p, err := Compile("$.xs.length()")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Eval(doc(t, `{"xs":[]}`)).Matches() {
		t.Error("$.xs.length() over an empty array selects 0, which matches")
	}
	if p.Eval(doc(t, `{"xs":"abcd"}`)).Matches() {
		t.Error("$.xs.length() over a string selects nothing, which does not match")
	}
}

// An index union is a list of chances: it collects what it can reach, in the
// order written, and an index the array does not have drops out instead of
// emptying the selection.
func TestIndexUnionSelectsEachIndexInOrder(t *testing.T) {
	cases := []struct {
		expr string
		body string
		want []any
	}{
		{"$.items[0,2]", `{"items":["a","b","c"]}`, []any{"a", "c"}},
		{"$.items[2,0]", `{"items":["a","b","c"]}`, []any{"c", "a"}},
		{"$.items[0,0]", `{"items":["a","b","c"]}`, []any{"a", "a"}},
		{"$.items[0,9]", `{"items":["a","b","c"]}`, []any{"a"}},
		{"$.items[8,9]", `{"items":["a","b","c"]}`, nil},
		{"$.items[-1,0]", `{"items":["a","b","c"]}`, []any{"c", "a"}},
		{"$.items[0,1,2]", `{"items":["a","b","c"]}`, []any{"a", "b", "c"}},
		{"$[0,1]", `["a","b"]`, []any{"a", "b"}},
		{"$.items[0,1]", `{"items":{"a":1}}`, nil},
		{"$.items[0,1]", `{"items":"ab"}`, nil},
		{"$.items[0,1].sku", `{"items":[{"sku":"a"},{"sku":"b"}]}`, []any{"a", "b"}},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		got := p.Eval(doc(t, c.body)).Values()
		if len(got) != len(c.want) {
			t.Errorf("%s on %s selected %v, want %v", c.expr, c.body, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s on %s [%d] = %v, want %v", c.expr, c.body, i, got[i], c.want[i])
			}
		}
	}
}

// An index union returns a LIST of hits, which is what makes a selected null
// match through one and not through the plain index that selects the same node.
// It is the same distinction $..v draws against $.v, and losing it would make a
// union quietly definite.
func TestIndexUnionIsIndefinite(t *testing.T) {
	union, err := Compile("$.items[0,1]")
	if err != nil {
		t.Fatal(err)
	}
	if union.Definite() {
		t.Error("$.items[0,1] selects more than one node and is not definite")
	}
	if !union.Eval(doc(t, `{"items":[null,null]}`)).Matches() {
		t.Error("a union over nulls returns a non-empty list of hits, which matches")
	}

	single, err := Compile("$.items[0]")
	if err != nil {
		t.Fatal(err)
	}
	if single.Eval(doc(t, `{"items":[null]}`)).Matches() {
		t.Error("the control: the same null selected directly does NOT match")
	}
}

// A union of names at the end of a path returns one object carrying what it
// found, and in the middle of a path it branches. Both are the pinned
// WireMock's, and a stub can tell them apart: the merged form hands an inner
// matcher the text of an object rather than each member's own text.
func TestNameUnionMergesAtTheEndAndBranchesInside(t *testing.T) {
	leaf, err := Compile("$['a','b']")
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.Definite() {
		t.Error("$['a','b'] returns one merged object and is definite")
	}

	merged := leaf.Eval(doc(t, `{"a":1,"b":2,"c":3}`))
	object, ok := merged.Node.(map[string]any)
	if !ok {
		t.Fatalf("$['a','b'] selected %T, want a merged object", merged.Node)
	}
	if len(object) != 2 || object["a"] != float64(1) || object["b"] != float64(2) {
		t.Errorf("$['a','b'] merged %v, want only the members it names", object)
	}

	inside, err := Compile("$['a','b'].c")
	if err != nil {
		t.Fatal(err)
	}
	if inside.Definite() {
		t.Error("$['a','b'].c carries each member on separately and is not definite")
	}
	got := inside.Eval(doc(t, `{"a":{"c":1},"b":{"c":2}}`)).Values()
	if len(got) != 2 || got[0] != float64(1) || got[1] != float64(2) {
		t.Errorf("$['a','b'].c selected %v, want 1 and 2 in the order written", got)
	}
}

// The merged object decides the bare form by its own emptiness, which puts a
// member holding null on the matching side and a union that found nothing on
// the other.
func TestNameUnionEmptinessRules(t *testing.T) {
	cases := []struct {
		expr string
		body string
		want bool
		why  string
	}{
		{"$['a','b']", `{"a":1,"b":2}`, true, "both members present"},
		{"$['a','b']", `{"a":1}`, true, "one member present"},
		{"$['a','b']", `{"c":1}`, false, "neither present merges to an empty object"},
		{"$['a','b']", `{"a":null}`, true, "a member holding null is still a member"},
		{"$['a']", `{"a":null}`, false, "the control: selected directly, that null does not match"},
		{"$['a','b']", `{"a":{}}`, true, "the merged object is what is tested, not what it holds"},
		{"$['a','b']", `[1,2]`, false, "an array has no named members"},
		{"$['a','b']", `5`, false, "and neither does a scalar"},
		{"$.o['a','b']", `{"o":{"a":1}}`, true, "a union at the end of a longer path merges too"},
		{"$['a','b'].c", `{"a":{"c":null}}`, true, "branching returns a list of hits, so the null is a hit"},
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

// A function reads what the path selected rather than selecting in its own
// right, so a union it follows is still the end of the path — and counts as the
// merged object rather than as each member in turn.
func TestLengthCountsTheMergedUnion(t *testing.T) {
	p, err := Compile("$['a','b'].length()")
	if err != nil {
		t.Fatal(err)
	}
	got := p.Eval(doc(t, `{"a":1,"b":2,"c":3}`)).Values()
	if len(got) != 1 || got[0] != float64(2) {
		t.Errorf("$['a','b'].length() selected %v, want the merged object's 2 members", got)
	}
}

// A filter operand may still carry a length(), which is the half of the dialect
// that does resolve inside a predicate.
func TestFilterOperandCountsWithLength(t *testing.T) {
	p, err := Compile("$.items[?(@.xs.length() > 1)]")
	if err != nil {
		t.Fatal(err)
	}
	got := p.Eval(doc(t, `{"items":[{"xs":[1,2]},{"xs":[1]}]}`)).Values()
	if len(got) != 1 {
		t.Fatalf("the filter selected %v, want only the item with two elements", got)
	}
	item, ok := got[0].(map[string]any)
	if !ok || len(item["xs"].([]any)) != 2 {
		t.Errorf("the filter selected %v, want the item whose xs has two elements", got[0])
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
