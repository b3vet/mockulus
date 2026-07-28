// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"errors"
	"strings"
	"testing"
)

// The two mustache forms are one instruction written two ways. WireMock runs its
// response transformer with escaping switched off, so nothing there tells
// {{x}} and {{{x}}} apart, and the characters that would have — `&`, `<`, `>`,
// `"`, `'` — are the ones every templated JSON, XML and URL body is built from.
// A renderer that escaped the double form would corrupt the common case and
// still not agree with the server it is copying, because Go spells a quote
// `&#34;` where Java spells it `&quot;`.

// markup carries all five characters at once, so no test below can pass by
// handling four of them.
const markup = `Ben & Jerry <fine> "quoted" 'single'`

func TestBothMustacheFormsRenderTheValueRaw(t *testing.T) {
	ctx := map[string]any{"v": markup}
	out := render(t, `esc=[{{v}}] raw=[{{{v}}}]`, ctx)

	want := "esc=[" + markup + "] raw=[" + markup + "]"
	if out != want {
		t.Fatalf("render = %q, want %q", out, want)
	}
}

// Character by character, because an escaper that had been narrowed rather than
// removed would keep exactly one of these and the combined string above would
// point at the string rather than at the character.
func TestNoCharacterIsEscapedInEitherForm(t *testing.T) {
	for _, char := range []string{"&", "<", ">", `"`, "'", "&&", "<&>", `a"b'c`} {
		ctx := map[string]any{"v": char}
		for _, tpl := range []string{`{{v}}`, `{{{v}}}`} {
			if out := render(t, tpl, ctx); out != char {
				t.Errorf("%s over %q = %q, want the value back unchanged", tpl, char, out)
			}
		}
	}
}

// The control against the fix over-applying in the obvious direction: replacing
// an escape with an unescape reads as "no escaping" on every input above and is
// wrong on this one. A stub serving an HTML fixture, or a body echoing a request
// that legitimately carried `&amp;`, must get its own bytes back — the renderer
// interpolates, it does not decide what the text means.
func TestEntityTextInAValueIsNeitherEscapedNorDecoded(t *testing.T) {
	for _, value := range []string{"&amp;", "&lt;b&gt;", "&#34;", "&quot;", "&#x27;", "&amp;amp;"} {
		ctx := map[string]any{"v": value}
		for _, tpl := range []string{`{{v}}`, `{{{v}}}`} {
			if out := render(t, tpl, ctx); out != value {
				t.Errorf("%s over %q = %q, want the value back unchanged", tpl, value, out)
			}
		}
	}
}

// The other control: literal template text never went through the escaper and
// must not start now. A template is written by whoever registered the stub, so
// the markup in it is the response they asked for.
func TestLiteralTemplateTextIsUntouched(t *testing.T) {
	ctx := map[string]any{"v": "x"}
	tpl := `<a href="?a=1&b=2">'{{v}}'</a>`
	want := `<a href="?a=1&b=2">'x'</a>`
	if out := render(t, tpl, ctx); out != want {
		t.Fatalf("render = %q, want %q", out, want)
	}
}

// Values that reach the output through a helper, a block body or an iteration
// take the same write, and the fix has to hold for all of them: the widest real
// case is `{{jsonPath request.body '$.n'}}` interpolated into a JSON body, which
// is a helper result rather than a plain lookup.
func TestHelperAndBlockOutputIsRawToo(t *testing.T) {
	reg := NewRegistry()
	reg.Register("echo", func(args []any, hash map[string]any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		return args[0], nil
	})
	ctx := map[string]any{
		"v":     markup,
		"items": []any{"a&b", "c<d"},
	}
	cases := map[string]string{
		`{{echo v}}`:                           markup,
		`{{{echo v}}}`:                         markup,
		`{{echo (echo v)}}`:                    markup,
		`{{#if v}}{{v}}{{/if}}`:                markup,
		`{{#with .}}{{v}}{{/with}}`:            markup,
		`{{#each items}}[{{this}}]{{/each}}`:   "[a&b][c<d]",
		`{{#each items}}[{{{this}}}]{{/each}}`: "[a&b][c<d]",
	}
	for tpl, want := range cases {
		parsed, err := Parse(tpl)
		if err != nil {
			t.Errorf("parse %q: %v", tpl, err)
			continue
		}
		out, err := parsed.Render(ctx, reg, RenderOptions{})
		if err != nil {
			t.Errorf("render %q: %v", tpl, err)
			continue
		}
		if out != want {
			t.Errorf("%q = %q, want %q", tpl, out, want)
		}
	}
}

// The output cap counts what is written, and what is written is now the raw
// value. While the double form expanded `&` to five bytes, a body sat against
// its cap could be refused for characters the client never sees, and the size an
// operator configured had no relation to the size of the response. The pair
// fixes the boundary from both sides so the cap is not merely loose.
func TestTheOutputCapCountsTheRawValue(t *testing.T) {
	ctx := map[string]any{"v": strings.Repeat("&", 10)}
	tpl, err := Parse(`{{v}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	out, err := tpl.Render(ctx, NewRegistry(), RenderOptions{MaxOutput: 10})
	if err != nil {
		t.Fatalf("render under a 10-byte cap: %v", err)
	}
	if out != strings.Repeat("&", 10) {
		t.Fatalf("render = %q, want ten ampersands", out)
	}

	if _, err := tpl.Render(ctx, NewRegistry(), RenderOptions{MaxOutput: 9}); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("render under a 9-byte cap: err = %v, want %v", err, ErrOutputTooLarge)
	}
}

func render(t *testing.T, source string, ctx any) string {
	t.Helper()
	tpl, err := Parse(source)
	if err != nil {
		t.Fatalf("parse %q: %v", source, err)
	}
	out, err := tpl.Render(ctx, NewRegistry(), RenderOptions{})
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}
