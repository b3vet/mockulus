// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"strings"
	"testing"
)

// A mustache holding one token has two readings — a member of the context, or a
// helper invoked with no arguments — and Handlebars settles it by asking
// whether a helper of that name exists. WireMock 3.13.2 settles it the same
// way: inside a scope that carries its own `now`, the helper's timestamp is
// what renders. Read as a path instead, `{{now}}` — the most commonly written
// expression in any WireMock template — renders as nothing at all, with no
// registration error and no serve-time error to say so.

// bindingRegistry holds the two shapes of no-argument helper that matter: one
// that ignores its arguments, and one that would report being handed any.
func bindingRegistry() *Registry {
	reg := NewRegistry()
	reg.Register("now", func([]any, map[string]any) (any, error) { return "TIME", nil })
	reg.Register("randomValue", func(args []any, _ map[string]any) (any, error) {
		if len(args) > 0 {
			return "ARGS", nil
		}
		return "DRAW", nil
	})
	reg.Register("echo", func(args []any, _ map[string]any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		return args[0], nil
	})
	return reg
}

func bound(t *testing.T, source string) *Template {
	t.Helper()

	tpl, err := Parse(source)
	if err != nil {
		t.Fatalf("parse %q: %v", source, err)
	}
	reg := bindingRegistry()
	tpl.BindBareHelpers(reg.Has)
	return tpl
}

func renderBound(t *testing.T, source string, ctx any) string {
	t.Helper()

	out, err := bound(t, source).Render(ctx, bindingRegistry(), RenderOptions{})
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}

func TestABareHelperNameIsCalledWithNoArguments(t *testing.T) {
	ctx := map[string]any{"request": map[string]any{"method": "POST"}}

	cases := map[string]string{
		`{{now}}`:                       "TIME",
		`{{randomValue}}`:               "DRAW",
		`[{{now}}] [{{now}}]`:           "[TIME] [TIME]",
		`{{{now}}}`:                     "TIME",
		`{{#if request}}{{now}}{{/if}}`: "TIME",
	}
	for source, want := range cases {
		if got := renderBound(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The helper wins over a member of the same name, which is the oracle's answer
// and the reason this cannot be done by falling back to the helper only when
// the lookup misses: a request whose body happens to carry a `now` field would
// then render one thing and the next request another.
func TestARegisteredNameBeatsAMemberThatSharesIt(t *testing.T) {
	ctx := map[string]any{"now": "MEMBER", "randomValue": "MEMBER"}

	if got := renderBound(t, `{{now}}/{{randomValue}}`, ctx); got != "TIME/DRAW" {
		t.Fatalf("render = %q, want the helpers rather than the members", got)
	}
}

// The control: binding must reach the names in the registry and no others. A
// bare name nothing has registered is still a path, and a path that misses
// still renders as nothing rather than becoming an error — which is what keeps
// `{{customerId}}` in a body a lookup of the model.
func TestABareNameThatIsNotAHelperStaysAPath(t *testing.T) {
	ctx := map[string]any{"tier": "gold", "nowish": "no"}

	cases := map[string]string{
		`{{tier}}`:   "gold",
		`{{nowish}}`: "no",
		`{{nope}}`:   "",
	}
	for source, want := range cases {
		if got := renderBound(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The second control, and the one a fix applied to every expression rather than
// to the mustache position would fail. An argument is a path in Handlebars even
// where a helper shares its name, so `{{echo now}}` passes the member — the
// helper is reached in that position only by writing the parentheses.
func TestAHelperNameInArgumentPositionIsStillAPath(t *testing.T) {
	ctx := map[string]any{"now": "MEMBER"}

	cases := map[string]string{
		`{{echo now}}`:                    "MEMBER",
		`{{echo (now)}}`:                  "TIME",
		`{{#if now}}yes{{else}}no{{/if}}`: "yes",
		`{{#with now}}{{this}}{{/with}}`:  "MEMBER",
		`{{echo (randomValue)}}`:          "DRAW",
	}
	for source, want := range cases {
		if got := renderBound(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The third control. Only a single segment is a helper name: a dotted path that
// ends in one is a member of something, and a stub echoing `{{clock.now}}` out
// of its own transformer parameters must keep getting the parameter.
func TestOnlyASingleSegmentBinds(t *testing.T) {
	ctx := map[string]any{
		"clock":  map[string]any{"now": "MEMBER"},
		"parent": map[string]any{"randomValue": "MEMBER"},
	}

	cases := map[string]string{
		`{{clock.now}}`:                   "MEMBER",
		`{{parent.randomValue}}`:          "MEMBER",
		`{{clock.[now]}}`:                 "MEMBER",
		`{{#with clock}}{{now}}{{/with}}`: "TIME",
	}
	for source, want := range cases {
		if got := renderBound(t, source, ctx); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// A bound name has to be visible to the registration-time check, or a stub
// could carry a helper the engine never validated. The names collected from a
// template are what the 422 for an unknown helper is built from.
func TestABoundHelperIsReportedAsAHelper(t *testing.T) {
	tpl := bound(t, `{{now}} {{tier}} {{echo (randomValue)}}`)

	got := strings.Join(tpl.Helpers(), ",")
	if got != "echo,now,randomValue" {
		t.Fatalf("Helpers() = %q, want the three called helpers and not the path", got)
	}
}

// Parentheses are the unambiguous spelling of a call, so the parser settles
// those without the registry — and a subexpression naming something no registry
// has is then reported before the stub is stored rather than rendering as
// nothing on every request (P3). WireMock refuses the same template, though it
// waits until the first request to say so.
func TestAParenthesisedNameIsAlwaysACall(t *testing.T) {
	tpl, err := Parse(`{{echo (unregistered)}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := strings.Join(tpl.Helpers(), ","); got != "echo,unregistered" {
		t.Fatalf("Helpers() = %q, want the unknown name reported as a helper", got)
	}

	// A quoted literal in the same position is still a literal: `('x')` is the
	// string, not a helper called x.
	if got := renderBound(t, `{{echo ('x')}}`, nil); got != "x" {
		t.Fatalf(`{{echo ('x')}} = %q, want the literal`, got)
	}
}

// Binding happens once, at compile time, so a template rendered twice cannot
// answer differently — and nothing on the render path has to consult the
// registry to interpolate a variable (§16.3 rule 2).
func TestBindingSurvivesRepeatedRenders(t *testing.T) {
	tpl := bound(t, `{{now}}|{{tier}}`)
	ctx := map[string]any{"tier": "gold"}

	for i := range 3 {
		out, err := tpl.Render(ctx, bindingRegistry(), RenderOptions{})
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		if out != "TIME|gold" {
			t.Fatalf("render %d = %q, want TIME|gold", i, out)
		}
	}
}
