// SPDX-License-Identifier: Apache-2.0

package handlebars

import "testing"

func TestSmoke(t *testing.T) {
	reg := NewRegistry()
	reg.Register("upper", func(args []any, hash map[string]any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		s, _ := args[0].(string)
		return s + "!", nil
	})
	ctx := map[string]any{
		"name":  "world",
		"items": []any{"a", "b"},
		"req":   map[string]any{"path": "/x", "headers": map[string]any{"H": "v"}},
		"empty": []any{},
	}
	cases := map[string]string{
		`hello {{name}}`:                                "hello world",
		`{{req.path}}`:                                  "/x",
		`{{req.headers.H}}`:                             "v",
		`{{upper name}}`:                                "world!",
		`{{#if name}}yes{{else}}no{{/if}}`:              "yes",
		`{{#if missing}}yes{{else}}no{{/if}}`:           "no",
		`{{#unless missing}}u{{/unless}}`:               "u",
		`{{#each items}}[{{this}}:{{@index}}]{{/each}}`: "[a:0][b:1]",
		`{{#each empty}}x{{else}}none{{/each}}`:         "none",
		`{{#with req}}{{path}}{{/with}}`:                "/x",
		`{{items.[1]}}`:                                 "b",
		`literal {{! a comment }}text`:                  "literal text",
		`{{upper "hi"}}`:                                "hi!",
	}
	for tpl, want := range cases {
		p, err := Parse(tpl)
		if err != nil {
			t.Errorf("parse %q: %v", tpl, err)
			continue
		}
		got, err := p.Render(ctx, reg, RenderOptions{})
		if err != nil {
			t.Errorf("render %q: %v", tpl, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %q, want %q", tpl, got, want)
		}
	}
}
