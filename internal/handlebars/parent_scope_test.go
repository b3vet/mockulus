// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"reflect"
	"testing"
)

// `../` is how a template reaches out of the scope a block pushed, and reaching
// the request from inside an {{#each}} is the ordinary way to write a repeated
// block. Read as an ordinary character, the slash made `{{../request.method}}` a
// lookup of a member called "/request" — a name no model has, so the expression
// rendered as nothing, and the same path handed to a helper handed it nothing
// to work with.

// scoped is the model these tests walk: a request beside a document, so a step
// outward lands somewhere that can be told apart from where it started.
func scoped() map[string]any {
	return map[string]any{
		"request": map[string]any{"method": "POST", "body": "BODY"},
		"city":    "root",
		"xs":      []any{"p", "q"},
		"inner":   map[string]any{"city": "york"},
		"outer":   map[string]any{"city": "london"},
	}
}

func TestParentScopeReachesOutOfABlock(t *testing.T) {
	cases := map[string]string{
		// One step out of an {{#each}}, which is where this is written.
		`{{#each xs}}{{../request.method}}{{/each}}`: "POSTPOST",
		// And out of a {{#with}}.
		`{{#with inner}}{{city}}/{{../request.method}}{{/with}}`: "york/POST",
		// Two steps, from two frames deep.
		`{{#each xs}}{{#each xs}}{{../../request.method}}{{/each}}{{/each}}`: "POSTPOSTPOSTPOST",
		// One step from two frames deep: the frame it lands on has no
		// `request`, and the walk continues outward from there rather than
		// stopping — which is what lets the ordinary spelling keep working at
		// any depth.
		`{{#each xs}}{{#each xs}}{{../request.method}}{{/each}}{{/each}}`: "POSTPOSTPOSTPOST",
		// The item of the enclosing iteration, rather than a member of it.
		`{{#each xs}}{{#each xs}}{{../this}}{{this}};{{/each}}{{/each}}`: "pp;pq;qp;qq;",
		// Handlebars accepts either separator inside a path, so the same climb
		// written with slashes throughout means the same thing.
		`{{#each xs}}{{../request/method}}{{/each}}`: "POSTPOST",
	}
	for source, want := range cases {
		if got := render(t, source, scoped()); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The case the syntax exists for, and the one that decides between the two ways
// of implementing it. Both scopes carry `city`, so a reading that dropped the
// `../` and let the ordinary fall-through find the name answers "york" for both
// — a plausible value, silently wrong, in the only situation where anybody
// writes `../` in the first place.
func TestParentScopeIsNotFallThroughWhenTheInnerScopeShadows(t *testing.T) {
	source := `{{#with outer}}{{#with inner}}{{city}}|{{../city}}|{{../../city}}{{/with}}{{/with}}`

	if got := render(t, source, scoped()); got != "york|london|root" {
		t.Fatalf("render = %q, want york|london|root", got)
	}
}

// A climb that outruns the stack resolves to nothing. Clamping at the outermost
// frame instead would hand `{{../request.method}}` written at the top level the
// root context and render a value where the template named a scope that does
// not exist; WireMock 3.13.2 renders nothing for both of these.
func TestAClimbPastTheOutermostScopeRendersNothing(t *testing.T) {
	cases := map[string]string{
		`[{{../request.method}}]`: "[]",
		`[{{../../city}}]`:        "[]",
		`{{#each xs}}[{{../../../../request.method}}]{{/each}}`: "[][]",
		`{{#with inner}}[{{../../city}}]{{/with}}`:              "[]",
	}
	for source, want := range cases {
		if got := render(t, source, scoped()); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// A path is also what a helper argument is, and a subexpression is evaluated in
// the scope that encloses the block it opens. The nested form used to be worse
// than an empty render: `jsonPath` was handed the nothing the path resolved to
// and failed the whole response with a serve-time error.
func TestParentScopeResolvesInsideASubexpression(t *testing.T) {
	reg := NewRegistry()
	reg.Register("mark", func(args []any, _ map[string]any) (any, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, errNoDocument
		}
		return []any{Stringify(args[0]) + "!"}, nil
	})

	tpl, err := Parse(`{{#each xs}}{{#each (mark ../request.body)}}{{this}}{{/each}}{{/each}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := tpl.Render(scoped(), reg, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "BODY!BODY!" {
		t.Fatalf("render = %q, want the enclosing scope's body twice", out)
	}
}

// The control on the segmenting itself. `..` is a step outward only where a
// segment may start, and both separators Handlebars accepts are separators —
// but a bracketed segment is quoted text, so a member genuinely named with a
// slash or with dots in it is still reachable.
func TestPathSegmenting(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{"request.method", []string{"request", "method"}},
		{"request/method", []string{"request", "method"}},
		{"../request.method", []string{"..", "request", "method"}},
		{"../../a", []string{"..", "..", "a"}},
		{"..", []string{".."}},
		{"../this", []string{"..", "this"}},
		{"a.[b/c]", []string{"a", "b/c"}},
		{"[..]", []string{".."}},
		// A name that merely starts with two dots is not a climb, and the dots
		// segment it exactly as they always did.
		{"..x", []string{"x"}},
		{"a..b", []string{"a", "b"}},
		{"tag.[1]", []string{"tag", "1"}},
	}
	for _, c := range cases {
		if got := splitPath(c.path); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// The other control: a path with no `..` in it must keep resolving exactly as
// it did, innermost scope first and falling outward. Every template in the
// corpus is written that way, and the climb is an addition to that walk rather
// than a replacement for it.
func TestAnOrdinaryPathStillFallsOutward(t *testing.T) {
	cases := map[string]string{
		`{{request.method}}`:                         "POST",
		`{{#each xs}}{{request.method}}{{/each}}`:    "POSTPOST",
		`{{#with inner}}{{request.method}}{{/with}}`: "POST",
		`{{#with inner}}{{city}}{{/with}}`:           "york",
		`{{city}}`:                                   "root",
		`{{#each xs}}{{this}}{{/each}}`:              "pq",
		`{{#each xs}}{{@index}}{{/each}}`:            "01",
	}
	for source, want := range cases {
		if got := render(t, source, scoped()); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// errNoDocument stands in for the serve-time failure a helper handed nothing
// reports, so the test above fails loudly rather than rendering empty if the
// argument stops resolving.
var errNoDocument = errNoDoc{}

type errNoDoc struct{}

func (errNoDoc) Error() string { return "no document given" }
