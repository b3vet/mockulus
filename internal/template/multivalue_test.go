// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"testing"
)

// A name the wire carried more than once is one node with three readings, and
// the model has to give all three: `{{request.query.tag}}` is the first value,
// `{{request.query.tag.[1]}}` reaches past it, and `{{#each request.query.tag}}`
// walks the lot. Without the third, the repeated values a caller sent are
// dropped from the response with nothing to say so — no error at registration,
// no error at serve time, just a block that never runs.

// repeatedRequest carries every shape the assertions below need: a parameter
// sent twice, one sent once, one present with an empty value, a header sent
// twice, a header sent once, and a name that is not there at all.
func repeatedRequest(t *testing.T) map[string]any {
	t.Helper()

	r := httptest.NewRequestWithContext(context.Background(), "POST",
		"/e2e/multi/search?tag=red&tag=blue&one=solo&blank=", nil)
	r.Header.Add("X-Multi", "m0")
	r.Header.Add("X-Multi", "m1")
	r.Header.Add("X-One", "only")

	return BuildContext(r, []byte("body"), nil, nil)
}

func renderModel(t *testing.T, source string, ctx map[string]any) string {
	t.Helper()

	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	out, err := engine.Render(tpl, ctx)
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}

func TestEachWalksARepeatedQueryParameterAndHeader(t *testing.T) {
	ctx := repeatedRequest(t)

	cases := map[string]string{
		`{{#each request.query.tag}}{{@index}}:{{this}};{{/each}}`:       "0:red;1:blue;",
		`{{#each request.headers.X-Multi}}{{@index}}:{{this}};{{/each}}`: "0:m0;1:m1;",
		// The lowercased alias is the same node, not a copy that lost the list.
		`{{#each request.headers.x-multi}}{{this}};{{/each}}`: "m0;m1;",
		// A name that arrived once is a list of one. A stub whose caller
		// sometimes repeats a parameter must render the same shape either way.
		`{{#each request.query.one}}{{@index}}:{{this}};{{/each}}`: "0:solo;",
		`{{#each request.headers.X-One}}{{this}};{{/each}}`:        "only;",
		// `?blank=` is present carrying nothing: one iteration of an empty
		// value, which is a different request from one that omitted the key.
		`{{#each request.query.blank}}[{{this}}];{{/each}}`:            "[];",
		`{{#each request.query.gone}}[{{this}}];{{else}}none{{/each}}`: "none",
		// @first and @last have to fall on the right values, since a template
		// separating values with a comma has nothing else to key on.
		`{{#each request.query.tag}}{{this}}{{#unless @last}},{{/unless}}{{/each}}`: "red,blue",
	}
	for source, want := range cases {
		if got := renderModel(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The control, and the reason iteration is a nature added to the node rather
// than a different node. These readings are what wmv-tmpl-request-query-multi-001
// and wmv-tmpl-request-headers-001 pin, they were right before this changed,
// and a node turned into a plain list would break every one of them while
// leaving the iteration above passing.
func TestARepeatedNodeKeepsItsScalarAndIndexReadings(t *testing.T) {
	ctx := repeatedRequest(t)

	cases := map[string]string{
		`{{request.query.tag}}`:           "red",
		`{{request.query.tag.[0]}}`:       "red",
		`{{request.query.tag.[1]}}`:       "blue",
		`{{request.query.tag.[9]}}`:       "",
		`{{request.query.one}}`:           "solo",
		`{{request.query.blank}}`:         "",
		`{{request.headers.X-Multi}}`:     "m0",
		`{{request.headers.X-Multi.[1]}}`: "m1",
		`{{request.headers.x-multi}}`:     "m0",
		// Presence, not emptiness, is what the branch keys on: a key sent with
		// an empty value is a filter the caller asked for.
		`{{#if request.query.blank}}present{{else}}absent{{/if}}`: "present",
		`{{#if request.query.gone}}present{{else}}absent{{/if}}`:  "absent",
		// And the node is still a scalar everywhere a scalar is expected.
		`{{upper request.query.tag}}`:             "RED",
		`{{lookup request.query 'tag'}}`:          "red",
		`{{#with request.query}}{{tag}}{{/with}}`: "red",
	}
	for source, want := range cases {
		if got := renderModel(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The other control, and the boundary this change stops at. `request.path` is
// dual-natured in the same way, and the list nature is deliberately not given
// to it: its container is a set of named parts — the segments, and a path
// template's variables — rather than the sequence of values a repeated key
// carries.
//
// WireMock 3.13.2 does iterate it, one step per segment. That is a real
// disagreement and it is measured, not assumed: `{{#each request.path}}[{{this}}]{{/each}}`
// over /e2e/multi/search answers `[e2e][multi][search]` there and takes the
// else branch here. It is a different question from the repeated values above —
// it is about what `request.path` is, not about whether a list can be walked —
// and what this pins is that the change did not reach it.
func TestThePathNodeIsNotIterable(t *testing.T) {
	ctx := repeatedRequest(t)

	cases := map[string]string{
		`{{#each request.path}}[{{this}}]{{else}}none{{/each}}`: "none",
		`{{request.path}}`:     "/e2e/multi/search",
		`{{request.path.[1]}}`: "multi",
		// The segment list next to it is an ordinary list and always was.
		`{{#each request.pathSegments}}{{this}};{{/each}}`: "e2e;multi;search;",
	}
	for source, want := range cases {
		if got := renderModel(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// A repeated parameter grows as the query is read, so the values arrive in the
// order the caller wrote them and none of the spellings of the key wins over
// another.
func TestRepeatedValuesKeepTheirOrder(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/x?tag=c&tag=a&tag=b&tag=a", nil)
	ctx := BuildContext(r, nil, nil, nil)

	if got := renderModel(t, `{{#each request.query.tag}}{{this}}{{/each}}`, ctx); got != "caba" {
		t.Fatalf("render = %q, want the four values in the order they were sent", got)
	}
}
