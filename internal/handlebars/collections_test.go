// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"strings"
	"testing"
)

// Two questions about collections meet here. A value that is a scalar and a
// list at once — the shape a repeated query parameter or header takes — has to
// be walkable by {{#each}} without losing the scalar reading every stub written
// for the single-valued case depends on. And a value that is a document subtree
// has to render as the document, because Go's own spelling of a map is an
// internal and no client can parse `map[city:london name:ada]` back into
// anything.

// repeated is the model shape of §10.2: it prints as its first value, indexes
// as all of them, and iterates as the list.
type repeated []string

func (r repeated) String() string { return r[0] }

func (r repeated) List() []any {
	out := make([]any, len(r))
	for i, v := range r {
		out[i] = v
	}
	return out
}

// scalarOnly has the stringer nature and not the list one, which is what
// `request.path` looks like from here.
type scalarOnly string

func (s scalarOnly) String() string { return string(s) }

func TestEachWalksAValueThatIsAlsoAList(t *testing.T) {
	ctx := map[string]any{
		"tag":    repeated{"red", "blue"},
		"solo":   repeated{"gold"},
		"blank":  repeated{""},
		"absent": nil,
	}

	cases := map[string]string{
		`{{#each tag}}{{@index}}:{{this}};{{/each}}`:                                "0:red;1:blue;",
		`{{#each tag}}{{#if @first}}^{{/if}}{{this}}{{#if @last}}${{/if}}{{/each}}`: "^redblue$",
		// A key that arrived once is a list of one, not a scalar that cannot be
		// walked: a stub whose caller sometimes repeats a parameter must render
		// the same shape either way.
		`{{#each solo}}{{@index}}:{{this}};{{/each}}`: "0:gold;",
		// `?blank=` is present carrying nothing, which is one iteration of an
		// empty value and not zero iterations.
		`{{#each blank}}[{{this}}];{{/each}}`: "[];",
		// A name that is not there has nothing to walk and takes the else
		// branch, which is how a template writes "the caller sent none".
		`{{#each absent}}{{this}}{{else}}none{{/each}}`: "none",
	}
	for source, want := range cases {
		if got := render(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The control on the same value, and the reason the list is a third nature
// rather than a replacement. Every one of these readings is pinned by cases
// written before iteration worked, and a node flattened into a plain list would
// break all three: the bare name would print "[red blue]", `#if` would start
// keying off emptiness rather than presence, and the index form would still
// work — which is what would make the damage hard to spot.
func TestAListValueStillPrintsAndBranchesAsAScalar(t *testing.T) {
	ctx := map[string]any{
		"tag":   repeated{"red", "blue"},
		"blank": repeated{""},
	}

	cases := map[string]string{
		`{{tag}}`: "red",
		`{{#if tag}}present{{else}}absent{{/if}}`:   "present",
		`{{#if blank}}present{{else}}absent{{/if}}`: "present",
		`{{#if gone}}present{{else}}absent{{/if}}`:  "absent",
	}
	for source, want := range cases {
		if got := render(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}

	if got := Stringify(repeated{"red", "blue"}); got != "red" {
		t.Errorf("Stringify = %q, want the first value", got)
	}
	if !Truthy(repeated{""}) {
		t.Error("a key present with an empty value must stay truthy: it was sent")
	}
}

// The other control: only a value that says it is a list becomes one. A scalar
// that merely stringifies has nothing to walk, so {{#each}} over it takes the
// else branch rather than iterating the characters of a path or wrapping the
// value in a one-element list nobody put there.
//
// Handlebars.java iterates a scalar once instead, which is a difference this
// change deliberately leaves alone: it is a rule about {{#each}} itself rather
// than about the shape of a multi-valued key, and widening the block helper to
// wrap every value it is handed would put an iteration around request bodies,
// methods and cookies that no template asked to walk.
func TestEachDoesNotInventAListForAPlainScalar(t *testing.T) {
	ctx := map[string]any{
		"path":   scalarOnly("/a/b"),
		"text":   "gold",
		"number": 3.0,
	}

	for _, source := range []string{
		`{{#each path}}x{{else}}none{{/each}}`,
		`{{#each text}}x{{else}}none{{/each}}`,
		`{{#each number}}x{{else}}none{{/each}}`,
	} {
		if got := render(t, source, ctx); got != "none" {
			t.Errorf("%s = %q, want the else branch", source, got)
		}
	}
}

// A selection that lands on a list or an object renders as JSON. A stub echoing
// a subtree of the request is the ordinary reason to write one, and what it has
// to serve is the document — which is also what WireMock renders for the array
// case, byte for byte.
func TestACollectionRendersAsJSON(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{[]any{10.0, 20.0, 30.0}, `[10,20,30]`},
		{[]any{"a", "b"}, `["a","b"]`},
		{[]any{1.0, "a", true, nil}, `[1,"a",true,null]`},
		{[]any{}, `[]`},
		{map[string]any{"name": "ada", "city": "london"}, `{"city":"london","name":"ada"}`},
		{map[string]any{}, `{}`},
		{map[string]any{"a": map[string]any{"b": []any{1.0, 2.0}}}, `{"a":{"b":[1,2]}}`},
		{[]any{map[string]any{"sku": "A1"}}, `[{"sku":"A1"}]`},
	}
	for _, c := range cases {
		if got := Stringify(c.value); got != c.want {
			t.Errorf("Stringify(%v) = %q, want %q", c.value, got, c.want)
		}
	}
}

// The JSON is written with escaping off, like every other value this engine
// interpolates. A body carrying `Ben & Jerry <fine>` must come back as it was
// sent: the encoder's default spelling of those two characters is valid JSON
// and is not what WireMock writes, nor what the caller can compare against.
func TestJSONRenderingDoesNotEscapeMarkup(t *testing.T) {
	value := []any{"Ben & Jerry <fine>", `"quoted"`}

	got := Stringify(value)
	if strings.Contains(got, `\u`) {
		t.Fatalf("Stringify = %q, want the markup characters written as themselves", got)
	}
	if got != `["Ben & Jerry <fine>","\"quoted\""]` {
		t.Fatalf("Stringify = %q, want the values with only JSON's own quoting", got)
	}
}

// The control on the scalars, which reach this function far more often than any
// collection does and must be untouched by the collection rules. A JSON integer
// still renders without a fraction, and a value that is both a scalar and a
// container still renders as the scalar rather than as its parts.
func TestScalarRenderingIsUnchanged(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{nil, ""},
		{"gold", "gold"},
		{true, "true"},
		{7.0, "7"},
		{2.5, "2.5"},
		{42, "42"},
		{int64(42), "42"},
		{scalarOnly("/a/b"), "/a/b"},
		{repeated{"red", "blue"}, "red"},
		{[]string{"e2e", "orders"}, "e2e, orders"},
	}
	for _, c := range cases {
		if got := Stringify(c.value); got != c.want {
			t.Errorf("Stringify(%#v) = %q, want %q", c.value, got, c.want)
		}
	}
}
