// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The helpers of SPEC §10.3 at their boundaries. The E2E corpus renders each of
// them somewhere, so the middle of every range is covered already; what a unit
// test adds is the edge on either side of a rule, where an inverted comparison
// or a limit that counts one element too many still produces a plausible answer
// and a passing end-to-end run.

// render is the whole path a stub body takes: compiled once as it would be at
// registration, rendered against a real request model. Going through the
// template source rather than calling the helper keeps the argument types the
// ones the parser actually produces — every numeric literal is a float64, which
// is where a helper expecting an int would go wrong.
func render(t *testing.T, source string) string {
	t.Helper()

	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/helpers/values?n=6&word=Ada", nil)
	out, err := engine.Render(tpl, BuildContext(r, []byte(`{"k":"v"}`), nil, map[string]any{"tier": "gold"}))
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return out
}

// drawn reads a draw as the string it has to be. A helper that started
// returning a number or a list would otherwise fail the assertions below with a
// message about the value rather than about its type.
func drawn(t *testing.T, v any) string {
	t.Helper()

	s, ok := v.(string)
	if !ok {
		t.Fatalf("the helper returned %T, want a string", v)
	}
	return s
}

// `range` builds the list an {{#each}} walks, and the guard on its size is the
// only thing between a template and an allocation it names itself. The rows
// either side of 10000 are the point: the limit is on the element count, so a
// range that produces exactly the limit is served and one element more is not.
func TestRangeIsInclusiveAtBothEndsAndStopsAtItsLimit(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{#each (range 1 3)}}{{this}};{{/each}}`, "1;2;3;", "both bounds are in the list"},
		{`{{#each (range 0 0)}}{{this}};{{/each}}`, "0;", "equal bounds are one element, not none"},
		{`{{#each (range -2 2)}}{{this}};{{/each}}`, "-2;-1;0;1;2;", "a range may start below zero"},
		{`{{#each (range -5 -3)}}{{this}};{{/each}}`, "-5;-4;-3;", "and stay below it"},
		// An inverted range is empty rather than an error: a template walking
		// `range 1 request.query.count` over a request that said zero is asking
		// for no elements, which is an answer.
		{`[{{#each (range 5 1)}}{{this}}{{else}}empty{{/each}}]`, "[empty]", "an inverted range is empty"},
		// A bound arriving as text from the request is the ordinary case, and it
		// has to count the same as a literal.
		{`{{#each (range 1 request.query.n)}}{{this}}{{/each}}`, "123456", "a bound read off the query"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}

	// The limit itself, asserted as a count rather than as text: 10000 elements
	// is the largest range served, and the refusal for 10001 is in
	// helper_errors_test.go. A guard written `>=` would refuse both and a
	// stub legitimately paging 10000 items would start failing.
	out, err := rangeHelper([]any{1.0, 10000.0}, nil)
	if err != nil {
		t.Fatalf("a range of exactly the limit was refused: %v", err)
	}
	list, ok := out.([]any)
	if !ok {
		t.Fatalf("range 1 10000 produced %T, want a list", out)
	}
	if len(list) != 10000 {
		t.Errorf("range 1 10000 produced %d elements, want exactly the limit", len(list))
	}
}

// `substring` clamps rather than failing, and it counts runes rather than bytes.
// Both halves are load-bearing on the serve path: a stub taking the first eight
// characters of a name it was sent must not fail on the request whose name is
// six characters long, and must not cut a multibyte character in half and put
// the pieces on the wire as a broken rune.
func TestSubstringClampsItsBoundsAndCutsOnRunes(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{substring 'abcdef' 2 4}}`, "cd", "the ordinary case: start inclusive, end exclusive"},
		{`{{substring 'abcdef' 2}}`, "cdef", "an omitted end runs to the end of the string"},
		{`{{substring 'abcdef' 0 0}}`, "", "an empty slice is legal"},
		{`{{substring 'abcdef' 6 6}}`, "", "and so is one at the very end"},
		{`{{substring 'abcdef' -3 2}}`, "ab", "a negative start clamps to the beginning"},
		{`{{substring 'abcdef' 2 99}}`, "cdef", "an end past the string clamps to its length"},
		{`{{substring 'abcdef' 99 2}}`, "", "a start past the string leaves nothing to take"},
		// end is clamped to at least start, so the reversed pair is empty rather
		// than a slice expression that would panic on the serve path.
		{`{{substring 'abcdef' 4 2}}`, "", "an end before the start is empty, not a panic"},
		{`{{substring 'héllo wörld' 0 5}}`, "héllo", "five runes, seven bytes"},
		{`{{substring 'héllo wörld' 6 11}}`, "wörld", "an offset counted in runes, not bytes"},
		{`{{substring '日本語' 1 2}}`, "本", "and a string with no single-byte runes at all"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// `math` over the operator set, with the results as they reach a body. The
// numbers matter as much as the arithmetic: a stub writing {{math a '+' b}} into
// a JSON document needs `3` and not `3.0`, which is what makes the integral
// results below part of the assertion rather than incidental to it.
func TestMathCoversItsOperatorSetAndRendersJSONNumbers(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{math 2 '+' 3}}`, "5", "an integral sum renders without a decimal point"},
		{`{{math 2 '-' 5}}`, "-3", "and a negative one keeps its sign"},
		{`{{math 3 '*' 4}}`, "12", "the asterisk spelling"},
		{`{{math 3 'x' 4}}`, "12", "and the x spelling WireMock templates also use"},
		{`{{math 7 '/' 2}}`, "3.5", "a division that does not divide evenly keeps its fraction"},
		{`{{math 6 '/' 3}}`, "2", "one that does renders as an integer"},
		{`{{math 7 '%' 3}}`, "1", "modulo"},
		{`{{math -7 '%' 3}}`, "-1", "whose result takes the sign of the dividend, as Java's does"},
		{`{{math 0.1 '+' 0.2}}`, "0.30000000000000004", "float arithmetic is not hidden by the formatting"},
		// The operands a request supplies arrive as text, and reading them as
		// numbers is the whole reason toFloat accepts a Stringer.
		{`{{math request.query.n '*' 2}}`, "12", "a query parameter is an operand"},
		{`{{math (size 'abcd') '+' 1}}`, "5", "and so is another helper's result"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// `number` formats to a fixed number of decimals, which is what a stub writing a
// money amount needs, and without the option it must not reach for scientific
// notation. `1e+06` in a price field is a body no client will parse.
func TestNumberFormatsToItsDecimalsAndNeverToExponent(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{number 3.14159 decimals=2}}`, "3.14", "truncating decimals is rounding, not cutting"},
		{`{{number 3.14159 decimals=3}}`, "3.142", "which rounds up where the next digit says so"},
		{`{{number 1234.5678 decimals=2}}`, "1234.57", "the same at a larger magnitude"},
		{`{{number 3.7 decimals=0}}`, "4", "no decimals at all is still a formatted number"},
		{`{{number 5 decimals=2}}`, "5.00", "an integer padded out to a money field"},
		{`{{number 1000000}}`, "1000000", "a million without decimals is not 1e+06"},
		{`{{number 7}}`, "7", "and an integral value carries no trailing .0"},
		{`{{number '42.5'}}`, "42.5", "text from the request is read as the number it spells"},
		{`{{number request.query.n decimals=1}}`, "6.0", "as is a query parameter"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// `default` walks its arguments and takes the first truthy one, which makes the
// falsy set the whole of its behaviour. Handlebars counts zero and the empty
// string as absent, so a stub writing {{default request.query.page 1}} gets its
// fallback for `?page=0` — surprising, and the reason the boundary is pinned
// rather than assumed.
func TestDefaultTakesTheFirstTruthyArgumentAndFallsBackToTheLast(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{default 'set' 'fallback'}}`, "set", "a value that is there wins"},
		{`{{default '' 'fallback'}}`, "fallback", "the empty string is absent"},
		{`{{default 0 'fallback'}}`, "fallback", "and so is zero, which is Handlebars' rule and not an accident"},
		{`{{default false 'fallback'}}`, "fallback", "as is false"},
		{`{{default null 'fallback'}}`, "fallback", "and null"},
		{`{{default '' '' 'third'}}`, "third", "the walk continues past more than one absence"},
		{`[{{default '' ''}}]`, "[]", "when nothing is truthy the last argument is served anyway"},
		{`{{default request.query.missing 'anon'}}`, "anon", "the request form: a parameter that was not sent"},
		{`{{default request.query.word 'anon'}}`, "Ada", "and one that was"},
		{`{{default parameters.tier 'bronze'}}`, "gold", "a transformer parameter is a source too"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// `lookup` indexes a collection by a key computed at render time, and a key that
// names nothing is a blank rather than a failure. That asymmetry is deliberate —
// a template walking a list and reaching for a field only some entries carry
// would otherwise fail the whole response — so both halves are pinned here: the
// hits resolve, and every shape of miss renders empty without an error.
func TestLookupResolvesAHitAndRendersAMissAsNothing(t *testing.T) {
	hits := []struct {
		args []any
		want any
		why  string
	}{
		{[]any{map[string]any{"a": "1"}, "a"}, "1", "a document member"},
		{[]any{map[string]string{"a": "1"}, "a"}, "1", "and one whose values are all strings"},
		{[]any{[]any{"x", "y", "z"}, "1"}, "y", "a list by index"},
		{[]any{[]string{"x", "y"}, "0"}, "x", "a string list at its first element"},
		{[]any{[]any{"x", "y", "z"}, "2"}, "z", "and at its last"},
		// The key is stringified before it is used, so a numeric literal from
		// the template — which the parser hands over as a float64 — indexes a
		// list the same way the quoted spelling does.
		{[]any{[]any{"x", "y"}, 1}, "y", "an integer key"},
	}
	for _, c := range hits {
		got, err := lookupHelper(c.args, nil)
		if err != nil {
			t.Fatalf("lookup %s: %v", c.why, err)
		}
		if got != c.want {
			t.Errorf("lookup %s gave %v, want %v", c.why, got, c.want)
		}
	}

	misses := []struct {
		args []any
		why  string
	}{
		{[]any{map[string]any{"a": "1"}, "b"}, "a member the document does not have"},
		{[]any{[]any{"x", "y"}, "2"}, "an index one past the end"},
		{[]any{[]any{"x", "y"}, "-1"}, "a negative index, which is not the last element here"},
		{[]any{[]any{"x", "y"}, "last"}, "a key that is not a number at all"},
		{[]any{[]string{"x"}, "9"}, "the same past the end of a string list"},
		{[]any{"a plain string", "0"}, "an argument that is not a collection"},
		{[]any{nil, "0"}, "and one that is nothing"},
	}
	for _, c := range misses {
		got, err := lookupHelper(c.args, nil)
		if err != nil {
			t.Errorf("lookup over %s returned an error (%v), want a blank miss", c.why, err)
		}
		if got != nil {
			t.Errorf("lookup over %s gave %v, want nothing", c.why, got)
		}
	}
}

// `split` and `join` are each other's inverse over a separator, and the pair is
// how a stub takes a delimited header apart and puts it back together. The empty
// separator is the boundary: Go splits into runes there, and a helper that
// special-cased it would silently return the whole string as one element.
func TestSplitAndJoinRoundTripThroughASeparator(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{join (split 'a,b,c' ',') '|'}}`, "a|b|c", "apart and back together"},
		{`{{#each (split 'a,b,c' ',')}}[{{this}}]{{/each}}`, "[a][b][c]", "the pieces are a list an each walks"},
		{`{{size (split 'a,b,c' ',')}}`, "3", "and a list size counts"},
		{`{{size (split 'abc' '')}}`, "3", "an empty separator splits into runes"},
		{`{{size (split 'a' ',')}}`, "1", "a string without the separator is one piece"},
		{`[{{join (split '' ',') '|'}}]`, "[]", "and an empty string is one empty piece"},
		{`{{size (split 'a,,b' ',')}}`, "3", "an empty piece between two separators is still a piece"},
		{`{{join (split 'a-b' '-') ''}}`, "ab", "joining with nothing concatenates"},
		// The loose form, where the arguments were never a list: everything but
		// the last argument is joined by it.
		{`{{join 'a' 'b' 'c' '-'}}`, "a-b-c", "loose arguments join by the last of them"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// `base64` and `urlEncode` each carry a decode option, which means each has two
// directions that must actually be inverses. A decode flag read as a plain
// presence check rather than a truthy one would encode where the template said
// `decode=false`, and the round trips below are what notices.
func TestBase64AndURLEncodeInvertThemselvesUnderTheDecodeOption(t *testing.T) {
	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{base64 'hello'}}`, "aGVsbG8=", "encoding, padding included"},
		{`{{base64 (base64 'hello') decode=true}}`, "hello", "and the round trip back"},
		{`{{base64 'Ben & Jerry <fine>'}}`, "QmVuICYgSmVycnkgPGZpbmU+", "characters a body would otherwise have to escape"},
		{`[{{base64 ''}}]`, "[]", "nothing encodes to nothing"},
		// decode=false is the option written out rather than omitted, and it
		// must mean the same as leaving it off.
		{`{{base64 'hello' decode=false}}`, "aGVsbG8=", "an explicitly false flag still encodes"},
		{`{{urlEncode 'a b&c=d'}}`, "a+b%26c%3Dd", "query encoding, where a space is a plus"},
		{`{{urlEncode (urlEncode 'a b&c=d') decode=true}}`, "a b&c=d", "and the round trip back"},
		{`{{urlEncode 'a b' decode=false}}`, "a+b", "an explicitly false flag still encodes"},
		{`{{urlEncode 'plain'}}`, "plain", "text with nothing to escape passes through"},
		// The body of the request, encoded on the way out, which is the reason
		// the helper exists next to request.bodyAsBase64.
		{`{{base64 request.body}}`, "eyJrIjoidiJ9", "the request body encodes like any other string"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, got, c.want, c.why)
		}
	}
}

// The string helpers and the aliases §10.3 lists for two of them. `lower` and
// `lowercase` are both spelled in the allowlist because WireMock accepts both,
// and a registry that registered one of each pair would turn a template ported
// from WireMock into a 422 at registration — a failure that looks like the
// template is wrong when the allowlist is.
func TestTheStringHelpersAndBothSpellingsOfTheirAliases(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{`{{upper 'Ada'}}`, "ADA"},
		{`{{uppercase 'Ada'}}`, "ADA"},
		{`{{lower 'Ada'}}`, "ada"},
		{`{{lowercase 'Ada'}}`, "ada"},
		{`[{{trim '  padded  '}}]`, "[padded]"},
		// A real tab and newline rather than the two-character escapes, because
		// trimming has to reach every kind of whitespace a caller's value can
		// carry and not only the space bar.
		{"[{{trim '\t mixed \n'}}]", "[mixed]"},
		{`{{concat 'a' 'b' 'c'}}`, "abc"},
		{`{{concat 'n=' 42 ' ok=' true}}`, "n=42 ok=true"},
		{`{{replace 'a-b-c' '-' '+'}}`, "a+b+c"},
		{`{{replace 'aaa' 'a' 'aa'}}`, "aaaaaa"},
		{`{{replace 'abc' 'z' 'y'}}`, "abc"},
		// The helpers composing, which is how they are actually written.
		{`{{upper (replace request.query.word 'A' 'a')}}`, "ADA"},
		{`{{lower (concat request.query.word '-' request.query.n)}}`, "ada-6"},
	}

	for _, c := range cases {
		if got := render(t, c.source); got != c.want {
			t.Errorf("%s = %q, want %q", c.source, got, c.want)
		}
	}
}

// `randomValue` draws from the alphabet its type names, and the type is the part
// that can go wrong quietly: a NUMERIC draw that fell through to the
// alphanumeric alphabet still looks random, still has the right length, and
// breaks the first stub that parses it as a number. Asserting the alphabet is
// what separates those two outcomes.
func TestRandomValueDrawsFromTheAlphabetItsTypeNames(t *testing.T) {
	cases := []struct {
		kind     string
		alphabet string
	}{
		{"ALPHANUMERIC", alphaLower + alphaUpper + digits},
		{"ALPHABETIC", alphaLower + alphaUpper},
		{"NUMERIC", digits},
		{"HEXADECIMAL", hexDigits},
	}

	for _, c := range cases {
		out, err := randomValueHelper(nil, map[string]any{"type": c.kind, "length": 256.0})
		if err != nil {
			t.Fatalf("randomValue %s: %v", c.kind, err)
		}
		got, ok := out.(string)
		if !ok || len([]rune(got)) != 256 {
			t.Fatalf("randomValue %s drew %v, want 256 characters", c.kind, out)
		}
		if i := strings.IndexFunc(got, func(r rune) bool { return !strings.ContainsRune(c.alphabet, r) }); i >= 0 {
			t.Errorf("randomValue %s drew %q at index %d, which is not in %q", c.kind, got[i:i+1], i, c.alphabet)
		}
		// 256 draws from an alphabet of at least ten leaves no plausible chance
		// of a single repeated character, so this catches a draw that is not one.
		if strings.Count(got, got[:1]) == len(got) {
			t.Errorf("randomValue %s drew %q, which is one character repeated", c.kind, got)
		}
	}

	// The type is read case-insensitively, because a template author writes it
	// as WireMock's documentation spells it or as they remember it.
	for _, spelling := range []string{"numeric", "Numeric", "NUMERIC"} {
		out, err := randomValueHelper(nil, map[string]any{"type": spelling, "length": 16.0})
		if err != nil {
			t.Fatalf("randomValue type=%q: %v", spelling, err)
		}
		got := drawn(t, out)
		if strings.IndexFunc(got, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			t.Errorf("randomValue type=%q drew %q, want digits", spelling, got)
		}
	}
}

// The options that shape a draw rather than choosing its alphabet. A length of
// zero is a legitimate request for nothing and must not become the default 36,
// and `uppercase` has to reach the value after it is drawn — a UUID drawn in
// lowercase and returned that way under uppercase=true is the failure this
// catches.
func TestRandomValueHonoursItsLengthAndUppercaseOptions(t *testing.T) {
	for _, length := range []float64{0, 1, 7, 36, 512} {
		out, err := randomValueHelper(nil, map[string]any{"length": length})
		if err != nil {
			t.Fatalf("randomValue length=%v: %v", length, err)
		}
		if got := drawn(t, out); len([]rune(got)) != int(length) {
			t.Errorf("randomValue length=%v drew %d characters", length, len([]rune(got)))
		}
	}

	out, err := randomValueHelper(nil, map[string]any{"type": "HEXADECIMAL", "length": 64.0, "uppercase": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := drawn(t, out); got != strings.ToUpper(got) {
		t.Errorf("randomValue uppercase=true drew %q, want it uppercased", got)
	}

	// A UUID is the type whose length is fixed by its own shape, so the length
	// option does not apply to it — and the version and variant nibbles have to
	// be the ones RFC 4122 fixes, or a client validating the value rejects it.
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for range 32 {
		drawnUUID, drawErr := randomValueHelper(nil, map[string]any{"type": "UUID", "length": 4.0})
		if drawErr != nil {
			t.Fatal(drawErr)
		}
		if got := drawn(t, drawnUUID); !uuid.MatchString(got) {
			t.Fatalf("randomValue type=UUID drew %q, want a version 4 UUID", got)
		}
	}

	// uppercase applies to a UUID as well, which is the combination a stub
	// echoing an upper-cased correlation id writes.
	upper, err := randomValueHelper(nil, map[string]any{"type": "UUID", "uppercase": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := drawn(t, upper); got != strings.ToUpper(got) || !uuid.MatchString(strings.ToLower(got)) {
		t.Errorf("randomValue type=UUID uppercase=true drew %q", got)
	}
}

// `randomInt` and `randomDecimal` must stay inside the bounds they were given
// and must actually move within them. Bounds honoured but never varied is a
// helper that has stopped drawing, and bounds varied but not honoured is a
// stub serving a page number outside the range it advertised; one test cannot
// tell them apart without asserting both.
func TestRandomIntAndDecimalStayInBoundsAndStillVary(t *testing.T) {
	const draws = 500

	seen := map[int]bool{}
	for range draws {
		out, err := randomIntHelper(nil, map[string]any{"lower": 1.0, "upper": 6.0})
		if err != nil {
			t.Fatalf("randomInt: %v", err)
		}
		n, ok := out.(int)
		if !ok {
			t.Fatalf("randomInt returned %T, want an int a JSON body can carry", out)
		}
		if n < 1 || n > 6 {
			t.Fatalf("randomInt lower=1 upper=6 drew %d, which is outside the bounds", n)
		}
		seen[n] = true
	}
	// Both ends have to be reachable: bounds applied as a half-open interval
	// would never draw 6, and 500 draws of a six-sided range that never showed
	// one face is not a shortfall of luck.
	if len(seen) != 6 {
		t.Errorf("500 draws of randomInt lower=1 upper=6 produced %d distinct values, want all 6", len(seen))
	}

	// Equal bounds are the degenerate case, and it must be the value rather
	// than an error or a panic on a zero-width interval.
	for range 16 {
		out, err := randomIntHelper(nil, map[string]any{"lower": 4.0, "upper": 4.0})
		if err != nil || out != 4 {
			t.Fatalf("randomInt with equal bounds gave %v (%v), want 4", out, err)
		}
	}

	distinct := map[float64]bool{}
	for range draws {
		out, err := randomDecimalHelper(nil, map[string]any{"lower": 2.0, "upper": 3.0})
		if err != nil {
			t.Fatalf("randomDecimal: %v", err)
		}
		f, ok := out.(float64)
		if !ok {
			t.Fatalf("randomDecimal returned %T, want a float64", out)
		}
		if f < 2.0 || f >= 3.0 {
			t.Fatalf("randomDecimal lower=2 upper=3 drew %v, which is outside the bounds", f)
		}
		distinct[f] = true
	}
	if len(distinct) < draws/2 {
		t.Errorf("500 draws of randomDecimal produced only %d distinct values", len(distinct))
	}
}

// `pickRandom` chooses among the arguments it was given, and over a list it
// chooses among the elements rather than returning the list. The single-argument
// case is the one worth pinning: one string is not a collection to pick from, so
// it is the answer.
func TestPickRandomChoosesAmongItsOptions(t *testing.T) {
	const draws = 300

	seen := map[any]bool{}
	for range draws {
		out, err := pickRandomHelper([]any{"a", "b", "c"}, nil)
		if err != nil {
			t.Fatalf("pickRandom: %v", err)
		}
		if out != "a" && out != "b" && out != "c" {
			t.Fatalf("pickRandom chose %v, which was not among the options", out)
		}
		seen[out] = true
	}
	if len(seen) != 3 {
		t.Errorf("300 draws chose %d of the 3 options, want all of them", len(seen))
	}

	// A single list argument is unwrapped, so `{{pickRandom (split ...)}}`
	// picks an element and not the whole list.
	fromList := map[any]bool{}
	for range draws {
		out, err := pickRandomHelper([]any{[]any{"x", "y"}}, nil)
		if err != nil {
			t.Fatalf("pickRandom over a list: %v", err)
		}
		if out != "x" && out != "y" {
			t.Fatalf("pickRandom over a list chose %v, want one of its elements", out)
		}
		fromList[out] = true
	}
	if len(fromList) != 2 {
		t.Errorf("300 draws over a two-element list chose %d of them", len(fromList))
	}

	// A list of strings is the other shape a helper hands over, and it unwraps
	// the same way.
	out, err := pickRandomHelper([]any{[]string{"only"}}, nil)
	if err != nil || out != "only" {
		t.Errorf("pickRandom over a one-element string list gave %v (%v), want only", out, err)
	}

	// And a single scalar is itself, which is what keeps a template written
	// against a parameter that turned out not to be a list rendering.
	if out, err := pickRandomHelper([]any{"solo"}, nil); err != nil || out != "solo" {
		t.Errorf("pickRandom of one value gave %v (%v), want it back", out, err)
	}
	if out, err := pickRandomHelper([]any{[]any{}}, nil); err != nil || out != "" {
		t.Errorf("pickRandom of an empty list gave %v (%v), want the empty string", out, err)
	}
}

// A quote opened and never closed is a template author's typo, and the pattern
// it appears in has to keep rendering: everything after the quote is the literal
// text they were in the middle of writing. Dropping the tail instead would serve
// a timestamp that is silently missing its suffix — an ISO-8601 string without
// its Z is a different instant to whoever parses it, not a malformed one they
// would notice.
func TestAFormatPatternWithAnUnclosedQuoteKeepsTheTextAfterIt(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		why     string
	}{
		{"yyyy'T", "2006T", "the tail of an unclosed quote is literal text, not a lost suffix"},
		{"yyyy-MM-dd'", "2006-01-02", "a quote at the very end opens a run with nothing in it"},
		{"'literal", "literal", "a pattern that is nothing but an unclosed run"},
		// The controls: a closed quote behaves as it always did, and the tokens
		// on either side of it still translate.
		{"yyyy'T'HH", "2006T15", "a properly closed run separates two token groups"},
		{"yyyy''MM", "200601", "an empty quoted run contributes nothing and consumes both quotes"},
		// A token the table does not carry is passed through a character at a
		// time, which is how a pattern's punctuation survives.
		{"yyyy/MM/dd@HH", "2006/01/02@15", "unrecognised characters are literal"},
	}

	for _, c := range cases {
		if got := javaToGoLayout(c.pattern); got != c.want {
			t.Errorf("javaToGoLayout(%q) = %q, want %q (%s)", c.pattern, got, c.want, c.why)
		}
	}

	// And through the helper, so the pattern that reaches Format is the one the
	// translation produced rather than the author's Java text.
	out, err := nowHelper(nil, map[string]any{"format": "yyyy'T"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := out.(string); !ok || !regexp.MustCompile(`^20\d\dT$`).MatchString(got) {
		t.Errorf("{{now format=\"yyyy'T\"}} rendered %q, want a year followed by the literal T", out)
	}
}

// `size` and `lookup` each claim a string-keyed map among the shapes they
// handle, and the two claims have to agree: a helper's result handed to `size`
// and then indexed by `lookup` must be the same collection to both. Nothing in
// the request model builds one today, so this arm of each type switch is a
// contract with future callers rather than a path the corpus walks — which is
// exactly why it needs saying somewhere other than in the switch itself.
func TestTheCollectionHelpersAgreeOnAStringKeyedMap(t *testing.T) {
	doc := map[string]string{"region": "eu-west-1", "zone": "b", "tier": "gold"}

	got, err := sizeHelper([]any{doc}, nil)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if got != 3 {
		t.Errorf("size of a string-keyed map is %v, want its 3 members and not the length of its printed form", got)
	}

	member, err := lookupHelper([]any{doc, "region"}, nil)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if member != "eu-west-1" {
		t.Errorf("lookup of a string-keyed map gave %v, want the member", member)
	}
}

// The conversion every numeric helper goes through, exercised at the types the
// model and the parser actually produce. The Stringer row is the one that earns
// its place: a query parameter is not a string, it is a node that prints as one,
// and a conversion that only handled `case string` would fail
// `{{math request.query.a '+' request.query.b}}` on every request.
func TestNumericConversionAcceptsEveryShapeTheModelProduces(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want float64
	}{
		{"a parsed literal", 2.5, 2.5},
		{"an int a helper returned", 7, 7},
		{"an int64", int64(9), 9},
		{"true", true, 1},
		{"false", false, 0},
		{"text", "3.25", 3.25},
		{"text with padding", "  4  ", 4},
		{"a negative", "-8", -8},
		{"exponent notation", "1e3", 1000},
		{"a repeated query node, which prints as its first value", multiValue{"12", "13"}, 12},
	}

	for _, c := range cases {
		got, err := toFloat(c.in)
		if err != nil {
			t.Errorf("toFloat(%s): %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("toFloat(%s) = %v, want %v", c.name, got, c.want)
		}
	}

	// Nothing is not zero. A missing query parameter converted to 0 would make
	// {{math request.query.absent '+' 1}} answer 1 on a request that said
	// nothing about it, which is an arithmetic result invented out of an
	// absence.
	if _, err := toFloat(nil); err == nil {
		t.Error("toFloat(nil) succeeded, want a refusal rather than a zero")
	} else if err.Error() != "expected a number, got nothing" {
		t.Errorf("toFloat(nil) said %q, want it to name the absence", err)
	}

	// And a shape no helper knows how to read names its type, which is what
	// tells the author what they passed.
	if _, err := toFloat(struct{ X int }{1}); err == nil || !strings.Contains(err.Error(), "struct") {
		t.Errorf("toFloat of an unconvertible value said %v, want the type named", err)
	}
}
