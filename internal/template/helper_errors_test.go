// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// SPEC §10.1 draws a line through helper failures and §10.4 says where each side
// lands. A parse error and a name outside the allowlist are decidable from the
// source alone, so they are a 422 at registration and the stub never exists. An
// argument count, an argument type and a domain error like division by zero are
// not: they depend on what a request carries, so they are render errors at serve
// time, and internal/response writes the error text into the body as
// "Template render error: <err>".
//
// That last detail is why these tests assert on the message and not merely on
// err != nil. The text is not a log line, it is the response body a stub author
// reads when their template stops working, and a helper answering "invalid
// argument" would leave them with a 500 and nothing to act on.

// mustCompile is the registration half. Everything in this file compiles: if one
// of these ever started failing here instead, the failure would have moved from
// the request that triggered it to the stub that registered it, which is a
// different contract and one no test below would otherwise notice.
func mustCompile(t *testing.T, source string) string {
	t.Helper()

	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(source)
	if err != nil {
		t.Fatalf("compile %q: %v — arity and type failures belong to render, not registration", source, err)
	}

	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/helpers/errors?n=notanumber", nil)
	out, err := engine.Render(tpl, BuildContext(r, nil, nil, nil))
	if err != nil {
		return "\x00" + err.Error()
	}
	return out
}

// renderErr renders a template that is expected to fail and returns the message
// a caller would be served.
func renderErr(t *testing.T, source string) string {
	t.Helper()

	got := mustCompile(t, source)
	if !strings.HasPrefix(got, "\x00") {
		t.Fatalf("%s rendered %q, want a render error", source, got)
		return ""
	}
	return got[1:]
}

// A helper called with the wrong number of arguments refuses the render and says
// what it wanted instead. These are the mistakes a template author actually
// makes — a separator left off a `join`, an operator left out of a `math` — and
// the message is the only thing standing between them and a blank 500.
func TestAHelperCalledWithTooFewArgumentsSaysWhatItNeeded(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{`{{math 1}}`, "math takes a left operand, an operator and a right operand"},
		{`{{math 1 '+'}}`, "math takes a left operand, an operator and a right operand"},
		{`{{math 1 '+' 2 3}}`, "math takes a left operand, an operator and a right operand"},
		{`{{range 1}}`, "range takes a lower and an upper bound"},
		{`{{range 1 2 3}}`, "range takes a lower and an upper bound"},
		{`{{split 'a,b'}}`, "split takes a string and a separator"},
		{`{{join 'a'}}`, "join takes a list and a separator"},
		{`{{replace 'a' 'b'}}`, "replace takes a string, a target and a replacement"},
		{`{{substring 'abc'}}`, "substring takes a string, a start and an optional end"},
		{`{{lookup request.query}}`, "lookup takes a collection and a key"},
	}

	for _, c := range cases {
		if got := renderErr(t, c.source); got != c.want {
			t.Errorf("%s failed with %q, want %q", c.source, got, c.want)
		}
	}
}

// The counterpart, and the reason the arity checks above are not simply
// `len(args) < n`: the helpers that treat no arguments as an empty input must
// keep doing so. `{{upper}}` and friends bind as zero-argument calls, because a
// mustache holding one registered name is a call (§10.1), and a template that
// mentions one where the model has nothing renders empty rather than serving a
// 500 to a caller who did nothing wrong.
func TestTheHelpersThatTolerateNoArgumentsRenderEmpty(t *testing.T) {
	cases := map[string]string{
		`[{{upper}}]`:      "[]",
		`[{{lower}}]`:      "[]",
		`[{{trim}}]`:       "[]",
		`[{{concat}}]`:     "[]",
		`[{{default}}]`:    "[]",
		`[{{base64}}]`:     "[]",
		`[{{urlEncode}}]`:  "[]",
		`[{{number}}]`:     "[]",
		`[{{pickRandom}}]`: "[]",
		// `size` of nothing is a count, and a count of nothing is zero rather
		// than the empty string: a stub branching on {{#if (size xs)}} needs a
		// number back even when there was no collection.
		`[{{size}}]`: "[0]",
	}

	for source, want := range cases {
		if got := mustCompile(t, source); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// A value that cannot be read as a number names the value that could not be
// read. The offending text matters more than the type here: a template doing
// arithmetic on a query parameter fails on the request that carried the wrong
// thing, and the caller's own value in the message is what identifies it.
func TestAValueThatIsNotANumberIsNamedInTheFailure(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{`{{math 'apples' '+' 1}}`, `"apples" is not a number`},
		{`{{math 1 '+' 'pears'}}`, `"pears" is not a number`},
		// The request-driven form, which is the one that reaches production: a
		// caller sends ?n=notanumber and the arithmetic has nothing to do.
		{`{{math request.query.n '*' 2}}`, `"notanumber" is not a number`},
		{`{{number 'apples'}}`, `"apples" is not a number`},
		{`{{number 1 decimals='two'}}`, `"two" is not a number`},
		{`{{range 'one' 'ten'}}`, `"one" is not a number`},
		// The upper bound is read after the lower one, so a template whose
		// lower bound is fine and whose upper bound came off a request that
		// carried the wrong thing fails on the second of them.
		{`{{range 1 'ten'}}`, `"ten" is not a number`},
		{`{{range 1 request.query.n}}`, `"notanumber" is not a number`},
		{`{{substring 'abcdef' 'x'}}`, `"x" is not a number`},
		{`{{substring 'abcdef' 0 'y'}}`, `"y" is not a number`},
		{`{{randomValue length='many'}}`, `randomValue length: "many" is not a number`},
		{`{{randomInt lower='low'}}`, `"low" is not a number`},
		{`{{randomInt upper='high'}}`, `"high" is not a number`},
		{`{{randomDecimal lower='low'}}`, `"low" is not a number`},
		{`{{randomDecimal upper='high'}}`, `"high" is not a number`},
	}

	for _, c := range cases {
		if got := renderErr(t, c.source); got != c.want {
			t.Errorf("%s failed with %q, want %q", c.source, got, c.want)
		}
	}
}

// The arithmetic and conversion failures that are not about types at all: an
// operator nothing implements, a divisor of zero, a length below zero, bounds
// the wrong way round. Each one is a template that would otherwise return a
// number a stub would go on to serve — NaN for a zero modulo, a negative slice
// length for a negative draw — so refusing is the behaviour, and the message
// names which of them it was.
func TestADomainErrorRefusesTheRenderRatherThanInventingAValue(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{math 1 '^' 2}}`, `unknown math operator "^"`,
			"the operator set is closed; exponentiation is not in it"},
		{`{{math 1 '/' 0}}`, "math: division by zero",
			"IEEE division would answer +Inf, which serialises into a body as +Inf"},
		{`{{math 1 '%' 0}}`, "math: modulo by zero",
			"and math.Mod by zero answers NaN, which serialises as NaN"},
		{`{{randomValue type='ROMAN'}}`, `unknown randomValue type "ROMAN"`,
			"an unknown type must not fall through to the default alphabet"},
		{`{{randomValue length=-1}}`, "randomValue length must not be negative",
			"a negative length is a mistake in the template, not a zero-length draw"},
		{`{{randomInt lower=10 upper=5}}`, "randomInt upper must not be below lower",
			"an inverted range has no value to draw from"},
		{`{{randomDecimal lower=1.5 upper=0.5}}`, "randomDecimal upper must not be below lower",
			"the same inversion in the decimal helper"},
		{`{{now offset='soonish'}}`, `offset "soonish" should look like "3 days"`,
			"an offset that is not a count and a unit"},
		{`{{now offset='3 fortnights'}}`, `unknown offset unit "fortnights"`,
			"a unit no calendar arithmetic here implements"},
		{`{{now timezone='Nowhere/Nothing'}}`, `unknown timezone "Nowhere/Nothing"`,
			"a zone the tzdata does not carry; silently serving UTC would be a timestamp wrong by hours"},
		{`{{base64 'not base64!' decode=true}}`, "base64 decode: illegal base64 data at input byte 3",
			"a decode of something that was never encoded"},
		{`{{urlEncode '%zz' decode=true}}`, `urlEncode decode: invalid URL escape "%zz"`,
			"and a percent escape that is not one"},
	}

	for _, c := range cases {
		if got := renderErr(t, c.source); got != c.want {
			t.Errorf("%s failed with %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// The `range` guard is a refusal rather than an allocation, and it has to say
// how big the range was. A template asking for a range it computed from the
// request is how this fires in practice, and "10001 exceeds 10000" tells the
// author both that they hit a limit and by how much.
func TestAnOversizedRangeIsRefusedBeforeItIsAllocated(t *testing.T) {
	if got := renderErr(t, `{{#each (range 1 10001)}}x{{/each}}`); got != "range of 10001 exceeds the 10000 limit" {
		t.Errorf("an 10001-element range failed with %q, want the count and the limit", got)
	}
}

// The half of §10.4 that is decided at registration. Both of these are properties
// of the source, so both are refused before a stub exists — which is the whole
// point of compiling at registration (P3, deviation #13): the alternative is a
// stub that registers cleanly and then fails on every single request.
func TestParseErrorsAndUnknownHelpersAreRefusedAtRegistration(t *testing.T) {
	engine := NewEngine(1<<20, nil)

	for _, source := range []string{
		`{{#if request.method}}yes`,
		`{{#each request.query}}{{this}}`,
		`{{#with request.query}}x{{/each}}`,
		`{{`,
	} {
		if _, err := engine.Compile(source); err == nil {
			t.Errorf("%q compiled, want a parse error at registration", source)
		}
	}

	// The helpers §10.3 deliberately leaves out, because a mock server has no
	// business reading the filesystem, the environment or the host it runs on
	// (SPEC §17). Each must be refused by name rather than silently rendering
	// as an empty lookup, or a stub author porting a WireMock template would
	// get a response with a hole in it and no indication why.
	for _, helper := range []string{"file", "systemValue", "secret", "hostname", "xPath", "soapXPath", "formatXml", "jwt"} {
		source := "{{" + helper + " 'x'}}"
		_, err := engine.Compile(source)
		if err == nil {
			t.Errorf("%q compiled, want the unknown-helper refusal of §10.3", source)
			continue
		}
		if !strings.Contains(err.Error(), helper) {
			t.Errorf("refusing %q said %q, want the helper name in it", source, err)
		}
		// The message doubles as the allowlist, which is what makes it
		// actionable: the author sees what they can use instead.
		if !strings.Contains(err.Error(), "substring") {
			t.Errorf("refusing %q said %q, want the available helpers listed", source, err)
		}
	}
}

// `jsonPath` is registered only when the caller supplies the matcher engine's
// implementation, so an engine built without it must refuse the name rather than
// register a stub whose body reaches for a helper that is not there. This is the
// one helper whose presence is a wiring decision, which makes it the one that
// could go missing without anything else noticing.
func TestJSONPathIsAbsentUntilTheMatcherEngineSuppliesIt(t *testing.T) {
	without := NewEngine(1<<20, nil)
	if _, err := without.Compile(`{{jsonPath request.body '$.id'}}`); err == nil {
		t.Fatal("jsonPath compiled on an engine that was given no implementation")
	}

	called := false
	with := NewEngine(1<<20, func(args []any, _ map[string]any) (any, error) {
		called = true
		return "from-the-matcher-engine", nil
	})
	tpl, err := with.Compile(`{{jsonPath request.body '$.id'}}`)
	if err != nil {
		t.Fatalf("jsonPath was supplied and still did not compile: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/helpers/jsonpath", nil)
	out, err := with.Render(tpl, BuildContext(r, []byte(`{"id":1}`), nil, nil))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !called || out != "from-the-matcher-engine" {
		t.Errorf("render gave %q (helper called: %v), want the supplied implementation to have run", out, called)
	}
}

// A helper that fails aborts the whole render, so nothing it had already written
// reaches the caller. Serving the prefix instead would put a truncated document
// on the wire under a 200, which is the one failure mode worse than the 500:
// a client parses half a body and cannot tell it was half.
func TestAFailedHelperDiscardsTheOutputWrittenBeforeIt(t *testing.T) {
	engine := NewEngine(1<<20, nil)

	tpl, err := engine.Compile(`{"ok":true,"n":{{math 'apples' '+' 1}},"tail":"never reached"}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/helpers/partial", nil)

	out, err := engine.Render(tpl, BuildContext(r, nil, nil, nil))
	if err == nil {
		t.Fatal("a failing helper rendered without error")
	}
	if out != "" {
		t.Errorf("the failed render returned %q, want nothing at all", out)
	}
}
