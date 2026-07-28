// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/jsonpath"
)

// Echoing part of the request back is the most common thing a templated stub
// does, and a path expression that lands on a list or an object is how it is
// written. What came back was Go's own spelling of the value — `[10 20 30]` for
// an array, `map[city:london name:ada]` for an object — which is not a
// serialisation of anything: a client parsing the response cannot read it, and
// neither can the caller who sent the document.

func selectFrom(t *testing.T, body, source string) string {
	t.Helper()

	engine := NewEngine(1<<20, jsonpath.TemplateHelper)
	tpl, err := engine.Compile(source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/select/echo", strings.NewReader(body))
	out, err := engine.Render(tpl, BuildContext(r, []byte(body), nil, nil))
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}

// The array forms, which reach the oracle byte for byte: WireMock 3.13.2
// renders `[10,20,30]` for the first of these and `["A1","B2"]` for the
// wildcard.
func TestASelectedArrayRendersAsJSON(t *testing.T) {
	body := `{"xs": [10, 20, 30], "ss": ["a", "b"], "mixed": [1, "a", true, null],
	          "items": [{"sku": "A1"}, {"sku": "B2"}], "none": []}`

	cases := map[string]string{
		`{{jsonPath request.body '$.xs'}}`:           `[10,20,30]`,
		`{{jsonPath request.body '$.ss'}}`:           `["a","b"]`,
		`{{jsonPath request.body '$.mixed'}}`:        `[1,"a",true,null]`,
		`{{jsonPath request.body '$.items[*].sku'}}`: `["A1","B2"]`,
		`{{jsonPath request.body '$.none'}}`:         `[]`,
		`{{jsonPath request.body '$.items'}}`:        `[{"sku":"A1"},{"sku":"B2"}]`,
	}
	for source, want := range cases {
		if got := selectFrom(t, body, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// An object renders as JSON too. This one does not reach parity — WireMock
// pretty-prints the object and keeps the document's key order, where an
// encoder over a Go map writes it compactly and in sorted order — but the
// difference is now whitespace and ordering between two JSON documents rather
// than a Go internal against a document.
func TestASelectedObjectRendersAsJSON(t *testing.T) {
	body := `{"who": {"name": "ada", "city": "london"}, "deep": {"a": {"b": [1, 2]}}, "empty": {}}`

	cases := map[string]string{
		`{{jsonPath request.body '$.who'}}`:   `{"city":"london","name":"ada"}`,
		`{{jsonPath request.body '$.deep'}}`:  `{"a":{"b":[1,2]}}`,
		`{{jsonPath request.body '$.empty'}}`: `{}`,
	}
	for source, want := range cases {
		if got := selectFrom(t, body, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The control, and the half of this that was already right. A scalar selection
// is the overwhelmingly common one, and it must keep rendering as the scalar —
// a number without quotes, a string without them either, an absent node as
// nothing at all. A rule that reached scalars would put quotes around every
// `{{jsonPath request.body '$.id'}}` in the corpus.
func TestASelectedScalarIsUnchanged(t *testing.T) {
	body := `{"n": 7, "d": 2.5, "s": "ada", "b": true, "z": null, "xs": [10, 20]}`

	cases := map[string]string{
		`{{jsonPath request.body '$.n'}}`:      "7",
		`{{jsonPath request.body '$.d'}}`:      "2.5",
		`{{jsonPath request.body '$.s'}}`:      "ada",
		`{{jsonPath request.body '$.b'}}`:      "true",
		`[{{jsonPath request.body '$.z'}}]`:    "[]",
		`[{{jsonPath request.body '$.gone'}}]`: "[]",
		`{{jsonPath request.body '$.xs[1]'}}`:  "20",
		`{{jsonPath request.body '$.xs[-1]'}}`: "20",
	}
	for source, want := range cases {
		if got := selectFrom(t, body, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The other control: rendering a subtree must not start escaping what the
// caller sent. A body carrying markup is exactly what a mock stands in for, and
// an encoder left on its defaults writes those characters as escapes that no
// comparison against the request will match.
func TestASelectedSubtreeKeepsItsMarkup(t *testing.T) {
	body := `{"xs": ["Ben & Jerry <fine>"], "who": {"n": "a&b"}}`

	if got := selectFrom(t, body, `{{jsonPath request.body '$.xs'}}`); got != `["Ben & Jerry <fine>"]` {
		t.Errorf("array = %q, want the markup as it was sent", got)
	}
	if got := selectFrom(t, body, `{{jsonPath request.body '$.who'}}`); got != `{"n":"a&b"}` {
		t.Errorf("object = %q, want the markup as it was sent", got)
	}
}

// A selected collection still composes with the block helpers, which is how a
// stub whose response follows the request's list is written. The JSON rendering
// is what {{this}} falls back to for an element that is itself a subtree.
func TestASelectedCollectionStillIterates(t *testing.T) {
	body := `{"items": [{"sku": "A1"}, {"sku": "B2"}]}`

	cases := map[string]string{
		`{{#each (jsonPath request.body '$.items[*].sku')}}{{this}};{{/each}}`: "A1;B2;",
		`{{#each (jsonPath request.body '$.items')}}{{this}};{{/each}}`:        `{"sku":"A1"};{"sku":"B2"};`,
		`{{#with (jsonPath request.body '$.items[0]')}}{{sku}}{{/with}}`:       "A1",
		`{{size (jsonPath request.body '$.items')}}`:                           "2",
	}
	for source, want := range cases {
		if got := selectFrom(t, body, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}
