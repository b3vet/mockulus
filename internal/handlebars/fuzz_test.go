// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"testing"
	"time"
)

// A response template is attacker-supplied in exactly the sense a stub mapping
// is: whoever can POST a mapping decides what text this parser sees, and the
// deployment they are posting to is shared with other teams. So the properties
// asserted here are not about what a template means — the corpus and the
// differential suite own that — but about the three ways a hand-written parser
// ruins someone else's afternoon: a panic, an allocation without bound, and an
// input that never finishes.

// parseBudget is how long one template may spend in the parser before the input
// counts as a hang rather than a slow case. Registration is a synchronous admin
// call, so an input that buys minutes of CPU with one POST is a denial of
// service against every other team on the instance. The value is far above any
// honest template and far below "noticed in production".
const parseBudget = 2 * time.Second

// renderContext is the shape of the real request model (SPEC §10.2), so a
// template the fuzzer builds out of `request.…` paths resolves against
// something rather than bailing at the first lookup.
var renderContext = map[string]any{
	"request": map[string]any{
		"method":  "POST",
		"path":    "/orders/7",
		"url":     "/orders/7?tier=gold",
		"body":    `{"customer":{"id":"c-1","vip":true},"items":[{"sku":"a"},{"sku":"b"}]}`,
		"query":   map[string]any{"tier": "gold", "region": "eu"},
		"headers": map[string]any{"Accept": "application/json"},
	},
	"parameters": map[string]any{"tier": "gold", "limit": 3.0},
	"items":      []any{"a", "b", "c"},
	"empty":      []any{},
}

// fuzzRegistry mirrors the shape of the §10.3 allowlist without depending on
// the package that owns it: enough helpers that helper calls, subexpressions
// and hash arguments are all exercised, and none that can reach outside.
func fuzzRegistry() *Registry {
	reg := NewRegistry()
	echo := func(args []any, hash map[string]any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		return args[0], nil
	}
	for _, name := range []string{"upper", "lower", "trim", "join", "size", "lookup", "jsonPath"} {
		reg.Register(name, echo)
	}
	reg.Register("range", func(args []any, hash map[string]any) (any, error) {
		return []any{1.0, 2.0, 3.0}, nil
	})
	return reg
}

// FuzzParse drives the template parser and, for anything it accepts, the
// renderer behind it. Rendering is part of the target because a tree the parser
// built is a tree the request path will walk: a node shape only the fuzzer can
// produce still has to render or fail, not panic.
func FuzzParse(f *testing.F) {
	// Seeds are the templates the E2E corpus registers, plus the shapes that
	// exercise the scanner's own edges — unterminated mustaches, mismatched
	// block tags, quoting and nesting.
	seeds := []string{
		`hello {{name}}`,
		`{{request.path}} tier={{request.query.tier}}`,
		`{{{request.body}}}`,
		`{{#if request.query.tier}}gold{{else}}anonymous{{/if}}`,
		`{{#unless request.query.tier}}no-tier{{else}}tiered{{/unless}}`,
		`{{#with request.query}}{{tier}}/{{region}}{{/with}}`,
		`{{#each (range 1 4)}}{{this}}{{#unless @last}},{{/unless}}{{/each}}`,
		`{{#each (jsonPath request.body '$.items')}}{{@index}}={{this}};{{/each}}`,
		`{{lookup request.query 'region'}}`,
		`{{join (split 'a,b,c' ',') '|'}}`,
		`{{now format='yyyy-MM-dd' offset='-100 years'}}`,
		`{{randomValue type='ALPHABETIC' length=8 uppercase=true}}`,
		`{{math (math 2 'x' 3) '+' 4}}`,
		`{{items.[1]}}`,
		`literal {{! a comment }}text`,
		`{{`,
		`{{}}`,
		`{{#if a}}`,
		`{{/if}}`,
		`{{else}}`,
		`{{#each items}}{{else}}{{/each}}`,
		`{{a "unterminated}}`,
		`{{a b=}}`,
		`{{((((a))))}}`,
		`{{#if a}}{{#if a}}{{#if a}}x{{/if}}{{/if}}{{/if}}`,
		`{{a.[unclosed}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	reg := fuzzRegistry()

	f.Fuzz(func(t *testing.T, source string) {
		start := time.Now()
		tpl, err := Parse(source)
		if took := time.Since(start); took > parseBudget {
			t.Fatalf("parsing %d bytes took %s, over the %s budget", len(source), took, parseBudget)
		}
		if err != nil {
			return
		}

		// A parsed template must round-trip its own source: Source is what the
		// 422 and the diagnostics quote back, and a parser that loses it reports
		// a problem in a document nobody wrote.
		if tpl.Source != source {
			t.Fatalf("Source = %q, want the input back", tpl.Source)
		}

		// The output cap is what stands between an expanding template and the
		// process's memory (SPEC §10.4), so the render is asked to respect one.
		const maxOutput = 1 << 16
		start = time.Now()
		out, err := tpl.Render(renderContext, reg, RenderOptions{MaxOutput: maxOutput})
		if took := time.Since(start); took > parseBudget {
			t.Fatalf("rendering %d bytes took %s, over the %s budget", len(source), took, parseBudget)
		}
		if err == nil && len(out) > maxOutput {
			t.Fatalf("render produced %d bytes over a %d cap", len(out), maxOutput)
		}

		// Helpers() is called on every registration to reject helpers outside
		// the allowlist, so it walks the same fuzzer-built trees the renderer
		// does and gets the same guarantee.
		tpl.Helpers()
	})
}
