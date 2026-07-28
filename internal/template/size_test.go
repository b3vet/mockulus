// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/b3vet/mockulus/internal/handlebars"
)

// modelNode walks the request model the way a template does, so what these
// tests hand `size` is the value a stub would have been handed and not a shape
// assembled here to suit the assertion.
func modelNode(t *testing.T, ctx map[string]any, path ...string) any {
	t.Helper()

	var node any = ctx
	for i, key := range path {
		container, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%v is not a container at %q", path[:i], key)
		}
		node, ok = container[key]
		if !ok {
			t.Fatalf("the model has no %v", path[:i+1])
		}
	}
	return node
}

func sizeOf(t *testing.T, v any) any {
	t.Helper()

	got, err := sizeHelper([]any{v}, nil)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	return got
}

// requestModel is the model behind both tests below: one request carrying a
// parameter whose single value is longer than one character, a parameter sent
// three times, an empty one, a header sent once and a header sent twice.
func requestModel(t *testing.T) map[string]any {
	t.Helper()

	r := httptest.NewRequestWithContext(context.Background(), "GET",
		"/e2e/size/orders?tier=go-ld&tag=crimson&tag=blue&tag=green&empty=", nil)
	r.Header.Add("X-One", "solo")
	r.Header.Add("X-Multi", "alpha")
	r.Header.Add("X-Multi", "beta")
	r.Header.Set("Cookie", "alpha=1; beta=2")

	return BuildContext(r, nil, nil, nil)
}

// A query parameter and a header are lists of values, and `size` over one of
// them is how many values arrived. Measuring the characters they print instead
// answers 5 for `?tier=go-ld` where WireMock answers 1, and 7 for a parameter
// sent as "crimson", "blue" and "green" where WireMock answers 3. Every value
// below was measured against pinned WireMock 3.13.2, and every one of them is
// a length that differs from its count — a header sent as "m0" and "m1" would
// agree by accident and prove nothing.
//
// The model is built by BuildContext rather than by hand on purpose. These
// nodes are the one part of the model with two natures, and the shape they take
// is not this file's to fix: if it changes, this is the test that says `size`
// stopped reading it.
func TestSizeCountsTheValuesOfARepeatedRequestNode(t *testing.T) {
	ctx := requestModel(t)

	cases := []struct {
		path []string
		want int
		why  string
	}{
		{[]string{"request", "query", "tier"}, 1, "one value, five characters: the divergence in one line"},
		{[]string{"request", "query", "tag"}, 3, "three values, whose first is seven characters"},
		{[]string{"request", "query", "empty"}, 1, "`?empty=` is present with an empty value, not absent"},
		{[]string{"request", "headers", "X-Multi"}, 2, "a header sent twice, as the wire spelled it"},
		{[]string{"request", "headers", "x-multi"}, 2, "and under the lowercased alias, which is the same node"},
		{[]string{"request", "headers", "X-One"}, 1, "a header sent once is a list of one, not four characters"},
	}

	for _, c := range cases {
		if got := sizeOf(t, modelNode(t, ctx, c.path...)); got != c.want {
			t.Errorf("size of %v is %v, want %d (%s)", c.path, got, c.want, c.why)
		}
	}
}

// The control on the same change, and the reason it is worth more than the test
// above: counting values is right for the nodes that carry a list and wrong for
// every other value in the model, which is most of it.
//
// `request.path` is the sharpest of them. It is dual-natured too — it prints as
// text and reaches its segments by index — and it still measures the text.
// WireMock counts its segments instead; that is a wider disagreement than the
// repeated values above and is not settled by this change, so what is pinned
// here is that the change did not reach it.
func TestSizeStillMeasuresTheTextOfAScalar(t *testing.T) {
	ctx := requestModel(t)

	cases := []struct {
		name string
		node any
		want int
		why  string
	}{
		{"request.path", modelNode(t, ctx, "request", "path"), 16,
			"the path prints /e2e/size/orders and measures its characters, not its 3 segments"},
		{"request.method", modelNode(t, ctx, "request", "method"), 3, "a plain string is its characters"},
		{"request.body", modelNode(t, ctx, "request", "body"), 0, "an absent body is the empty string"},
		{"a literal", "go-ld", 5, "the characters a query parameter carries still count as characters"},
		{"a number", 12345, 5, "and a value that is not a string at all prints first"},
	}

	for _, c := range cases {
		if got := sizeOf(t, c.node); got != c.want {
			t.Errorf("size of %s is %v, want %d (%s)", c.name, got, c.want, c.why)
		}
	}
}

// The other control: the collections that were already counted correctly. A
// branch for the repeated-key node must not displace the ordinary list and map
// cases, which is what `size` over a JSONPath result, a `range` and the cookie
// map all go through.
func TestSizeStillCountsOrdinaryCollections(t *testing.T) {
	ctx := requestModel(t)

	cases := []struct {
		name string
		node any
		want int
	}{
		{"request.pathSegments", modelNode(t, ctx, "request", "pathSegments"), 3},
		{"request.cookies", modelNode(t, ctx, "request", "cookies"), 2},
		{"a list of values", []any{"a", "b", "c"}, 3},
		{"a list of strings", []string{"a", "b"}, 2},
		{"a document", map[string]any{"a": 1, "b": 2, "c": 3}, 3},
		{"an empty list", []any{}, 0},
	}

	for _, c := range cases {
		if got := sizeOf(t, c.node); got != c.want {
			t.Errorf("size of %s is %v, want %d", c.name, got, c.want)
		}
	}
}

// `join` over the same node puts the separator between all of the values, not
// around the first one. A stub building a header or a URL out of what the
// caller sent drops everything after the first value otherwise, and drops it
// into a response that is still well-formed — measured against the oracle,
// which renders "crimson-blue-green" where reading the node as a scalar
// renders "crimson".
func TestJoinPutsTheSeparatorBetweenEveryValue(t *testing.T) {
	ctx := requestModel(t)

	cases := []struct {
		name string
		args []any
		want string
		why  string
	}{
		{"a repeated parameter", []any{modelNode(t, ctx, "request", "query", "tag"), "-"},
			"crimson-blue-green", "every value the wire carried under the name"},
		{"a repeated header", []any{modelNode(t, ctx, "request", "headers", "X-Multi"), ","},
			"alpha,beta", "a header is the same node as a query parameter"},
		{"a parameter sent once", []any{modelNode(t, ctx, "request", "query", "tier"), "-"},
			"go-ld", "one value is one value, with nothing to separate it from"},
		// The controls: the shapes join already handled.
		{"the loose form", []any{"a", "b", "c", "-"}, "a-b-c",
			"arguments that were never a list stay joinable"},
		{"a list of strings", []any{modelNode(t, ctx, "request", "pathSegments"), "/"}, "e2e/size/orders",
			"the segment list is a plain list and joins as one"},
		{"a list of values", []any{[]any{1, 2, 3}, ","}, "1,2,3", "a helper's output, likewise"},
		{"the path", []any{modelNode(t, ctx, "request", "path"), "-"}, "/e2e/size/orders",
			"the path is not a list of values and joins as the text it prints"},
	}

	for _, c := range cases {
		got, err := joinHelper(c.args, nil)
		if err != nil {
			t.Fatalf("join %s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("join over %s gave %q, want %q (%s)", c.name, got, c.want, c.why)
		}
	}
}

// Counting the values must not change what the node prints. A repeated
// parameter renders as its first value and indexes as all of them (§10.2), and
// a `size` fixed by flattening the node into a list would take the first of
// those with it — turning every `{{request.query.tag}}` in a stub into a Go
// slice printed with brackets.
func TestARepeatedNodeStillPrintsItsFirstValue(t *testing.T) {
	ctx := requestModel(t)

	node := modelNode(t, ctx, "request", "query", "tag")

	if got := handlebars.Stringify(node); got != "crimson" {
		t.Errorf("a repeated parameter printed %q, want its first value", got)
	}

	indexed, ok := node.(handlebars.Lookuper)
	if !ok {
		t.Fatalf("a repeated query parameter is %T, which no longer indexes", node)
	}
	if second, ok := indexed.Lookup("1"); !ok || second != "blue" {
		t.Errorf("indexing the second value gave %v, want blue", second)
	}
}
