// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"reflect"
	"testing"
)

func TestPathTemplateMatching(t *testing.T) {
	cases := []struct {
		template string
		path     string
		want     bool
		bindings map[string]string
	}{
		{"/orders/{id}", "/orders/42", true, map[string]string{"id": "42"}},
		{"/orders/{id}", "/orders/abc-123", true, map[string]string{"id": "abc-123"}},
		{"/orders/{id}", "/orders", false, nil},
		{"/orders/{id}", "/orders/42/items", false, nil},
		{"/orders/{id}", "/invoices/42", false, nil},
		{"/orders/{id}/items/{itemId}", "/orders/42/items/7", true,
			map[string]string{"id": "42", "itemId": "7"}},
		{"/orders/{id}/items/{itemId}", "/orders/42/items", false, nil},
		{"/static/path", "/static/path", true, map[string]string{}},
		{"/static/path", "/static/other", false, nil},
		{"/{first}", "/anything", true, map[string]string{"first": "anything"}},

		// An empty segment must not bind a variable, or /orders//items would
		// satisfy /orders/{id}/items.
		{"/orders/{id}/items", "/orders//items", false, nil},

		// A trailing slash makes a different path.
		{"/orders/{id}", "/orders/42/", false, nil},
	}

	for _, c := range cases {
		tpl, err := ParsePathTemplate(c.template)
		if err != nil {
			t.Errorf("parse %q: %v", c.template, err)
			continue
		}
		got := map[string]string{}
		matched := tpl.Match(c.path, func(name, value string) { got[name] = value })

		if matched != c.want {
			t.Errorf("%q against %q: matched = %v, want %v", c.template, c.path, matched, c.want)
			continue
		}
		if c.want && !reflect.DeepEqual(got, c.bindings) {
			t.Errorf("%q against %q: bindings = %v, want %v", c.template, c.path, got, c.bindings)
		}
	}
}

func TestPathTemplateRejectsMalformedTemplates(t *testing.T) {
	for _, bad := range []string{
		"",                  // empty
		"orders/{id}",       // no leading slash
		"/orders/{}",        // unnamed variable
		"/orders/pre{id}",   // variable is not a whole segment
		"/orders/{id}post",  // same
		"/orders/{id}/{id}", // duplicate binding
		"/orders/{a{b}}",    // malformed
	} {
		if _, err := ParsePathTemplate(bad); err == nil {
			t.Errorf("template %q should be rejected at registration", bad)
		}
	}
}

func TestPathTemplateVarsAndPrefix(t *testing.T) {
	tpl, err := ParsePathTemplate("/api/v1/orders/{id}/items/{itemId}")
	if err != nil {
		t.Fatal(err)
	}
	if got := tpl.Vars(); !reflect.DeepEqual(got, []string{"id", "itemId"}) {
		t.Errorf("vars = %v", got)
	}
	if got := tpl.LiteralPrefix(); got != "/api/v1/orders" {
		t.Errorf("literal prefix = %q, want /api/v1/orders", got)
	}

	// A template that starts with a variable has nothing to prefilter on, and
	// must report the prefix that every path shares rather than guessing.
	tpl2, err := ParsePathTemplate("/{anything}/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := tpl2.LiteralPrefix(); got != "/" {
		t.Errorf("leading-variable template prefix = %q, want /", got)
	}
}

// The prefix is a matching prefilter, so every path that matches must start
// with it — otherwise candidates are silently dropped.
func TestPathTemplatePrefixNeverOverclaims(t *testing.T) {
	cases := []struct {
		template string
		paths    []string
	}{
		{"/api/orders/{id}", []string{"/api/orders/1", "/api/orders/abc"}},
		{"/{a}/b", []string{"/x/b", "/y/b"}},
		{"/a/{b}/c", []string{"/a/x/c"}},
	}
	for _, c := range cases {
		tpl, err := ParsePathTemplate(c.template)
		if err != nil {
			t.Fatal(err)
		}
		prefix := tpl.LiteralPrefix()
		for _, p := range c.paths {
			if !tpl.Match(p, nil) {
				t.Fatalf("test setup: %q should match %q", p, c.template)
			}
			if len(p) < len(prefix) || p[:len(prefix)] != prefix {
				t.Errorf("template %q reports prefix %q, but matching path %q does not start with it",
					c.template, prefix, p)
			}
		}
	}
}

func BenchmarkPathTemplateMatch(b *testing.B) {
	tpl, err := ParsePathTemplate("/api/v1/orders/{id}/items/{itemId}")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !tpl.Match("/api/v1/orders/12345/items/7", func(string, string) {}) {
			b.Fatal("expected a match")
		}
	}
}
