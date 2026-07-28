// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/handlebars"
)

// `{{now}}` and `{{randomValue}}` are the two helpers anybody calls with no
// arguments, and a mustache holding one token cannot be told from a variable
// without the registry. The registry is here, and so is compilation, which is
// where the question is settled: once, for a stub that will serve it on every
// request, rather than on the render path §16.3 keeps clear.

func compileAndRender(t *testing.T, source string) string {
	t.Helper()

	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/bare/orders?tier=gold", nil)
	out, err := engine.Render(tpl, BuildContext(r, nil, nil, nil))
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}

// The default `now` format is the one WireMock's own ISO-8601 helper writes,
// down to the Z it collapses to at a zero offset. Asserting the shape rather
// than an instant is the only way to pin a clock, and the shape is the whole
// claim: `now=[2026-07-28T22:41:22Z]` is what the oracle answers this template.
func TestABareNowRendersTheDefaultTimestamp(t *testing.T) {
	got := compileAndRender(t, `{{now}}`)

	iso := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	if !iso.MatchString(got) {
		t.Fatalf("{{now}} rendered %q, want an ISO-8601 instant ending in Z", got)
	}
}

// The same for the other one, whose default draw is 36 alphanumeric characters
// — the length WireMock 3.13.2 draws for a bare `{{randomValue}}`.
func TestABareRandomValueRendersADraw(t *testing.T) {
	got := compileAndRender(t, `{{randomValue}}`)

	if len(got) != 36 {
		t.Fatalf("{{randomValue}} rendered %q (%d characters), want a 36-character draw", got, len(got))
	}
	if strings.ContainsAny(got, " -_") {
		t.Fatalf("{{randomValue}} rendered %q, want alphanumerics", got)
	}
	if second := compileAndRender(t, `{{randomValue}}`); second == got {
		t.Fatalf("two draws both rendered %q, which is not a draw", got)
	}
}

// The control on which names bind. Only the allowlist does, so a body echoing a
// field the model happens to carry keeps echoing it, and a name nothing has
// registered stays a lookup that renders as nothing rather than becoming a
// registration error.
func TestABareNameOutsideTheAllowlistIsStillALookup(t *testing.T) {
	cases := map[string]string{
		`{{request.method}}`:     "GET",
		`{{request.query.tier}}`: "gold",
		`[{{customerId}}]`:       "[]",
		`[{{nowish}}]`:           "[]",
		`[{{randomValues}}]`:     "[]",
	}
	for source, want := range cases {
		if got := compileAndRender(t, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// The second control. A helper reached by name in argument position is a path
// in Handlebars, so the helpers that error without arguments are not called by
// the binding — `{{concat now}}` passes the member `now`, which the model does
// not have, and renders as nothing rather than failing the response.
func TestHelperNamesInArgumentPositionAreNotCalled(t *testing.T) {
	cases := map[string]string{
		`[{{concat now}}]`:                  "[]",
		`[{{upper now}}]`:                   "[]",
		`[{{#if now}}yes{{else}}no{{/if}}]`: "[no]",
	}
	for source, want := range cases {
		if got := compileAndRender(t, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// Parentheses are the other spelling of a call, and the one that composes: a
// zero-argument helper feeding another is how a template pins the part of a
// timestamp that does not move between two servers.
func TestAZeroArgumentHelperComposesThroughASubexpression(t *testing.T) {
	day := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	if got := compileAndRender(t, `{{substring (now) 0 10}}`); !day.MatchString(got) {
		t.Errorf("{{substring (now) 0 10}} = %q, want a date", got)
	}
	if got := compileAndRender(t, `{{substring (now) 19 20}}`); got != "Z" {
		t.Errorf("{{substring (now) 19 20}} = %q, want the zone the default format ends in", got)
	}
	if got := compileAndRender(t, `{{size (randomValue)}}`); got != "36" {
		t.Errorf("{{size (randomValue)}} = %q, want 36", got)
	}
}

// A subexpression naming a helper the allowlist does not have is refused at
// registration, listing what is available (§10.4, P3). WireMock refuses the same
// template, but not until a request arrives — a stub that registers cleanly and
// then fails on every request is the outcome the registration-time check exists
// to prevent.
func TestAnUnknownHelperInASubexpressionIsRefusedAtCompileTime(t *testing.T) {
	engine := NewEngine(1<<20, nil)

	_, err := engine.Compile(`{{substring (hostname) 0 3}}`)
	if err == nil {
		t.Fatal("a subexpression naming an unregistered helper compiled")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("the error is %q, want the helper name in it", err)
	}

	// And the control: the same name outside parentheses is a path, which is a
	// legal template that renders as nothing.
	if _, err := engine.Compile(`{{hostname}}`); err != nil {
		t.Errorf("a bare name outside the allowlist should compile as a lookup: %v", err)
	}
}

// Binding must leave the parsed tree in a state the unknown-helper check can
// still read, or a stub could carry a call nothing validated.
func TestABoundHelperIsVisibleToTheHelperCheck(t *testing.T) {
	engine := NewEngine(1<<20, nil)

	tpl, err := engine.Compile(`{{now}} {{request.method}} {{upper request.query.tier}}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := strings.Join(tpl.Helpers(), ","); got != "now,upper" {
		t.Fatalf("Helpers() = %q, want both calls and neither path", got)
	}
	for _, name := range tpl.Helpers() {
		if !engine.registry.Has(name) {
			t.Errorf("%q was bound but is not registered", name)
		}
	}
}

// The engine is the only place binding happens, so a template parsed without it
// keeps the reading the parser gave: this is what stops the render path from
// depending on a registry it is not handed.
func TestParsingAloneDoesNotBind(t *testing.T) {
	tpl, err := handlebars.Parse(`{{now}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tpl.Helpers()) != 0 {
		t.Fatalf("Helpers() = %v, want none before the registry has been consulted", tpl.Helpers())
	}
}
