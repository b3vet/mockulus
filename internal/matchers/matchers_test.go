// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/jsonschemax"
)

// testPattern is a minimal PatternMatcher so this package's tests do not depend
// on the regex engine seam.
type testPattern struct{ re *regexp.Regexp }

func (p testPattern) MatchString(s string) bool { return p.re.MatchString(s) }
func (p testPattern) Source() string            { return p.re.String() }

func testOpts() Options {
	return Options{
		CompileRegex: func(pattern string) (PatternMatcher, error) {
			re, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
			if err != nil {
				return nil, err
			}
			return testPattern{re}, nil
		},
		// The real compiler rather than a fake: the draft, format and $ref
		// policies are the behaviour under test wherever a schema appears, and
		// a stand-in would agree with the matcher and not with the product.
		CompileSchema: func(schema, version string) (SchemaValidator, error) {
			return jsonschemax.Compile(schema, version)
		},
	}
}

// compile is a test helper that fails the test on any compilation problem. It
// compiles in the key-criterion position, which is the restrictive one.
func compile(t *testing.T, doc string) Matcher {
	t.Helper()
	m, probs := Compile(json.RawMessage(doc), "", testOpts())
	if len(probs) > 0 {
		t.Fatalf("compile %s: %v", doc, probs)
	}
	return m
}

// compileBody compiles in the bodyPatterns position, where the byte-oriented
// matchers are admitted.
func compileBody(t *testing.T, doc string) Matcher {
	t.Helper()
	opts := testOpts()
	opts.AllowContentPatterns = true
	m, probs := Compile(json.RawMessage(doc), "", opts)
	if len(probs) > 0 {
		t.Fatalf("compile %s: %v", doc, probs)
	}
	return m
}

func TestEqualTo(t *testing.T) {
	m := compile(t, `{"equalTo":"application/json"}`)

	if !m.Match(NewKeyValues("application/json")) {
		t.Error("exact value should match")
	}
	if m.Match(NewKeyValues("Application/JSON")) {
		t.Error("equalTo is case-sensitive by default")
	}
	if m.Match(AbsentKey()) {
		t.Error("an absent key cannot equal anything")
	}
	if m.Match(NewKeyValues("")) {
		t.Error("an empty value is not the expected value")
	}

	ci := compile(t, `{"equalTo":"application/json","caseInsensitive":true}`)
	if !ci.Match(NewKeyValues("Application/JSON")) {
		t.Error("caseInsensitive should relax case")
	}
	if ci.Match(NewKeyValues("application/xml")) {
		t.Error("caseInsensitive relaxes case, not content")
	}
}

// A repeated header or query parameter satisfies a criterion when any one of
// its values does (SPEC §5.2).
func TestMultiValueIsAnyOf(t *testing.T) {
	m := compile(t, `{"equalTo":"b"}`)

	if !m.Match(NewKeyValues("a", "b", "c")) {
		t.Error("a matching value anywhere in the list should satisfy the matcher")
	}
	if !m.Match(NewKeyValues("b")) {
		t.Error("a single matching value should match")
	}
	if m.Match(NewKeyValues("a", "c")) {
		t.Error("no matching value should not match")
	}
	if m.Match(NewKeyValues()) {
		t.Error("a present key with no values has nothing to match")
	}
}

func TestContainsAndItsComplement(t *testing.T) {
	c := compile(t, `{"contains":"order"}`)
	if !c.Match(NewKeyValues("my-order-42")) {
		t.Error("substring should match")
	}
	if c.Match(NewKeyValues("my-invoice-42")) {
		t.Error("absent substring should not match")
	}
	if c.Match(AbsentKey()) {
		t.Error("an absent subject contains nothing")
	}

	d := compile(t, `{"doesNotContain":"order"}`)
	if d.Match(NewKeyValues("my-order-42")) {
		t.Error("doesNotContain should reject a subject containing the text")
	}
	if !d.Match(NewKeyValues("my-invoice-42")) {
		t.Error("doesNotContain should accept a subject without the text")
	}
	// An absent subject genuinely does not contain the text; the negative
	// matchers are satisfied by absence.
	if !d.Match(AbsentKey()) {
		t.Error("an absent subject does not contain the text, so doesNotContain holds")
	}
}

func TestRegexMatchers(t *testing.T) {
	m := compile(t, `{"matches":"[0-9]{3}"}`)
	if !m.Match(NewKeyValues("123")) {
		t.Error("123 should match")
	}
	if m.Match(NewKeyValues("12")) {
		t.Error("12 should not match")
	}
	if m.Match(NewKeyValues("1234")) {
		t.Error("matching is anchored, so 1234 should not match [0-9]{3}")
	}

	n := compile(t, `{"doesNotMatch":"[0-9]{3}"}`)
	if n.Match(NewKeyValues("123")) {
		t.Error("doesNotMatch should reject a matching value")
	}
	if !n.Match(NewKeyValues("abc")) {
		t.Error("doesNotMatch should accept a non-matching value")
	}
}

func TestAbsent(t *testing.T) {
	m := compile(t, `{"absent":true}`)
	if !m.Match(AbsentKey()) {
		t.Error("absent should match a missing key")
	}
	if m.Match(NewKeyValues("anything")) {
		t.Error("absent should not match a present key")
	}
	// A present-but-empty value is present. Conflating the two would silently
	// change which stub serves a request.
	if m.Match(NewKeyValues("")) {
		t.Error("a present key with an empty value is not absent")
	}
}

func TestBinaryEqualTo(t *testing.T) {
	// "hello" in base64.
	m := compileBody(t, `{"binaryEqualTo":"aGVsbG8="}`)
	if !m.Match(NewBody([]byte("hello"))) {
		t.Error("decoded operand should match the raw bytes")
	}
	if m.Match(NewBody([]byte("hellp"))) {
		t.Error("different bytes should not match")
	}
	if m.Match(NewBody([]byte("hello!"))) {
		t.Error("a longer body should not match")
	}

	bodyOpts := testOpts()
	bodyOpts.AllowContentPatterns = true
	if _, probs := Compile(json.RawMessage(`{"binaryEqualTo":"not!base64!"}`), "", bodyOpts); len(probs) == 0 {
		t.Error("an invalid base64 operand must be rejected at registration")
	}
}

// Padding is optional in Java's decoder and mandatory in StdEncoding, so an
// operand written without it is bytes on WireMock and used to be a 422 here —
// a stub that registers on one server and cannot move to the other.
func TestBinaryEqualToAcceptsUnpaddedOperands(t *testing.T) {
	// The same five bytes, written both ways.
	for _, operand := range []string{`aGVsbG8=`, `aGVsbG8`} {
		m := compileBody(t, `{"binaryEqualTo":"`+operand+`"}`)
		if !m.Match(NewBody([]byte("hello"))) {
			t.Errorf("%s should decode to the same five bytes", operand)
		}
		// The control over the fallback: dropping the padding must not turn the
		// operand into a prefix comparison or into a shorter operand.
		if m.Match(NewBody([]byte("hell"))) {
			t.Errorf("%s must not match a shorter body", operand)
		}
		if m.Match(NewBody([]byte("hello!"))) {
			t.Errorf("%s must not match a longer body", operand)
		}
	}

	// And the half of the change that has to stay refused. The fallback reads
	// the standard alphabet, so what it buys is the missing '=' and nothing
	// else: WireMock refuses each of these too, so accepting any of them would
	// replace one divergence with another.
	bodyOpts := testOpts()
	bodyOpts.AllowContentPatterns = true
	for _, operand := range []string{
		`-_8=`,      // base64url, padded
		`-_8`,       // base64url, unpadded
		`aGVs bG8=`, // embedded whitespace
		`aGVsbG8xx`, // a final unit too short to hold a byte
	} {
		if _, probs := Compile(json.RawMessage(`{"binaryEqualTo":"`+operand+`"}`), "", bodyOpts); len(probs) == 0 {
			t.Errorf("%s is not base64 either server reads, so it must stay refused", operand)
		}
	}
}

// DecodeBase64 is the one reader behind binaryEqualTo and base64Body, and what
// it has to reproduce is Java's Base64.getDecoder(): the acceptance sets below
// were each confirmed against the pinned WireMock 3.13.2.
func TestDecodeBase64ReadsWhatJavaReads(t *testing.T) {
	accepted := map[string]string{
		"":                     "",      // the empty operand is an empty body, not an error
		"aGVsbG8=":             "hello", // padded
		"aGVsbG8":              "hello", // unpadded, the spelling that used to be refused
		"QUJD":                 "ABC",   // no padding needed
		"QUJ":                  "AB",    // one byte short of a group
		"QQ":                   "A",     // two bytes short of a group
		"SGVsbG8=":             "Hello",
		"cGx1cyBhbmQgc2xhc2g=": "plus and slash",
	}
	for operand, want := range accepted {
		got, err := DecodeBase64(operand)
		if err != nil {
			t.Errorf("DecodeBase64(%q) = %v, WireMock accepts it", operand, err)
			continue
		}
		if string(got) != want {
			t.Errorf("DecodeBase64(%q) = %q, want %q", operand, got, want)
		}
	}

	// '\r' and '\n' are deliberately absent from this list: encoding/base64
	// skips them under every encoding, so an operand carrying one is read here
	// and refused there, and asserting either answer would be asserting
	// something this function does not decide.
	for _, operand := range []string{
		"-_8=", "-_8", // base64url is a different alphabet, not a spelling
		"SGVs bG8=", "SGVs\tbG8=", // a space or a tab is a character, not a separator
		"!!!not base64!!!",
		"a",         // a single character cannot encode anything
		"SGVsbG8xx", // nine characters: the last unit has no whole byte in it
	} {
		if _, err := DecodeBase64(operand); err == nil {
			t.Errorf("DecodeBase64(%q) succeeded, WireMock refuses it", operand)
		}
	}
}

// A JSON null unmarshals into a Go string as "" without an error, so every
// matcher that takes a string used to accept null and mean something its author
// never wrote — {"contains":null} matched every request carrying a body.
func TestNullOperandIsRefused(t *testing.T) {
	bodyOpts := testOpts()
	bodyOpts.AllowContentPatterns = true

	// The six string-operand matchers, each of which WireMock answers 422.
	for _, key := range []string{
		"equalTo", "binaryEqualTo", "contains", "doesNotContain", "matches", "doesNotMatch",
	} {
		doc := `{"` + key + `":null}`
		_, probs := Compile(json.RawMessage(doc), "/request/bodyPatterns/0", bodyOpts)
		if len(probs) == 0 {
			t.Errorf("%s should be refused: null is not an operand", doc)
			continue
		}
		// The pointer names the key rather than the criterion, which is where
		// every other operand problem in this package points.
		if probs[0].Pointer != "/request/bodyPatterns/0/"+key {
			t.Errorf("%s: pointer = %q", doc, probs[0].Pointer)
		}
		if probs[0].Kind != ProblemMalformed {
			t.Errorf("%s: kind = %v, a null operand is a malformed document", doc, probs[0].Kind)
		}
	}

	// The guard sits above the whole switch, so a matcher added later inherits
	// it — including the combinators, whose operands are documents rather than
	// strings.
	for _, doc := range []string{`{"and":null}`, `{"or":null}`, `{"not":null}`,
		`{"and":[{"equalTo":null},{"contains":"a"}]}`} {
		if _, probs := Compile(json.RawMessage(doc), "", bodyOpts); len(probs) == 0 {
			t.Errorf("%s should be refused", doc)
		}
	}
}

// The control over the guard: an operand that is empty, false or zero is a
// value, and only the literal null is not. A guard that refused any of these
// would refuse stubs WireMock registers, which is the failure direction that
// cannot be worked around from a mappings file.
func TestFalsyOperandsAreStillOperands(t *testing.T) {
	for _, doc := range []string{
		`{"equalTo":""}`,
		`{"contains":""}`,
		`{"matches":""}`,
		`{"binaryEqualTo":""}`,
		`{"absent":true}`,
		`{"equalToJson":"{}"}`,
		// A modifier is answered before the operand guard runs, so a null there
		// still reads as the modifier being absent, which is what WireMock does
		// with it.
		`{"equalTo":"x","caseInsensitive":null}`,
	} {
		if _, probs := Compile(json.RawMessage(doc), "", func() Options {
			o := testOpts()
			o.AllowContentPatterns = true
			return o
		}()); len(probs) > 0 {
			t.Errorf("%s should compile, got %v", doc, probs)
		}
	}
}

// binaryEqualTo is a content matcher over byte[], which WireMock accepts only
// in bodyPatterns. A stub that registers here but is a 422 there is a hole a
// team only finds on the way back, so the key-criterion positions refuse it —
// and so does any position reached by nesting, because the combinators and the
// nested form of matchesJsonPath are declared over string patterns.
func TestBinaryEqualToIsBodyOnly(t *testing.T) {
	t.Run("refused in a key criterion", func(t *testing.T) {
		_, probs := Compile(json.RawMessage(`{"binaryEqualTo":"aGVsbG8="}`),
			"/request/headers/X-Sig", testOpts())
		if len(probs) == 0 {
			t.Fatal("binaryEqualTo must not register as a header criterion")
		}
		// WireMock blames the criterion, not the key inside it: what was written
		// is not a match operation it has.
		if probs[0].Pointer != "/request/headers/X-Sig" {
			t.Errorf("pointer should name the criterion, got %q", probs[0].Pointer)
		}
		if probs[0].Kind != ProblemMalformed {
			t.Errorf("a matcher that cannot exist there is a schema violation, got kind %v", probs[0].Kind)
		}
	})

	t.Run("refused inside a combinator even in a body position", func(t *testing.T) {
		bodyOpts := testOpts()
		bodyOpts.AllowContentPatterns = true
		for _, doc := range []string{
			`{"not":{"binaryEqualTo":"aGVsbG8="}}`,
			`{"and":[{"binaryEqualTo":"aGVsbG8="},{"contains":"h"}]}`,
			`{"or":[{"binaryEqualTo":"aGVsbG8="},{"contains":"h"}]}`,
		} {
			if _, probs := Compile(json.RawMessage(doc), "/request/bodyPatterns/0", bodyOpts); len(probs) == 0 {
				t.Errorf("%s should be refused: the combinators take string patterns", doc)
			}
		}
	})
}

func TestEqualToJSON(t *testing.T) {
	body := func(s string) Subject { return NewBody([]byte(s)) }

	t.Run("ignores key order and whitespace", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":1,\"b\":2}"}`)
		if !m.Match(body(`{"b": 2,   "a": 1}`)) {
			t.Error("key order and whitespace must not matter")
		}
	})

	t.Run("rejects extra fields by default", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":1}"}`)
		if m.Match(body(`{"a":1,"b":2}`)) {
			t.Error("an extra field should fail without ignoreExtraElements")
		}
	})

	t.Run("ignoreExtraElements accepts extra fields", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":1}","ignoreExtraElements":true}`)
		if !m.Match(body(`{"a":1,"b":2}`)) {
			t.Error("ignoreExtraElements should accept an extra field")
		}
		if !m.Match(body(`{"a":1,"nested":{"x":1}}`)) {
			t.Error("ignoreExtraElements should accept an extra nested object")
		}
		if m.Match(body(`{"b":2}`)) {
			t.Error("ignoreExtraElements must still require the expected fields")
		}
	})

	t.Run("nested objects honour ignoreExtraElements", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"o\":{\"a\":1}}","ignoreExtraElements":true}`)
		if !m.Match(body(`{"o":{"a":1,"b":2}}`)) {
			t.Error("the relaxation should apply at every depth, not just the root")
		}
	})

	t.Run("array order matters by default", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"[1,2,3]"}`)
		if !m.Match(body(`[1,2,3]`)) {
			t.Error("identical arrays should match")
		}
		if m.Match(body(`[3,2,1]`)) {
			t.Error("a reordered array should not match without ignoreArrayOrder")
		}
	})

	t.Run("ignoreArrayOrder relaxes order", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"[1,2,3]","ignoreArrayOrder":true}`)
		if !m.Match(body(`[3,1,2]`)) {
			t.Error("ignoreArrayOrder should accept a permutation")
		}
		if m.Match(body(`[1,2]`)) {
			t.Error("ignoreArrayOrder should still require the same elements")
		}
		if m.Match(body(`[1,2,3,4]`)) {
			t.Error("ignoreArrayOrder should still require the same length")
		}
	})

	t.Run("ignoreArrayOrder applies to nested arrays", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"xs\":[1,2]}","ignoreArrayOrder":true}`)
		if !m.Match(body(`{"xs":[2,1]}`)) {
			t.Error("the relaxation should apply to nested arrays")
		}
	})

	t.Run("accepts the operand inline as well as escaped", func(t *testing.T) {
		m := compile(t, `{"equalToJson":{"a":1}}`)
		if !m.Match(body(`{"a":1}`)) {
			t.Error("an inline expected document should work")
		}
	})

	t.Run("a non-JSON body never matches", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":1}"}`)
		if m.Match(body(`not json at all`)) {
			t.Error("a body that is not JSON cannot equal a JSON document")
		}
	})

	t.Run("types are distinguished", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":1}"}`)
		if m.Match(body(`{"a":"1"}`)) {
			t.Error("the string \"1\" is not the number 1")
		}
		if m.Match(body(`{"a":true}`)) {
			t.Error("true is not 1")
		}
		if m.Match(body(`{"a":null}`)) {
			t.Error("null is not 1")
		}
	})

}

// json-unit placeholders let an expected document say "any string here". Every
// rule below was verified against the pinned WireMock, which interprets them by
// default with no opt-in.
func TestJSONUnitPlaceholders(t *testing.T) {
	body := func(s string) Subject { return NewBody([]byte(s)) }

	t.Run("ignore accepts any value of any type", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.ignore}\"}"}`)
		for _, actual := range []string{
			`{"a":"text"}`, `{"a":42}`, `{"a":true}`, `{"a":null}`,
			`{"a":{"deep":1}}`, `{"a":[1,2]}`,
		} {
			if !m.Match(body(actual)) {
				t.Errorf("ignore should accept %s", actual)
			}
		}
		// It stands in for the value, not for the member: the key must be there.
		if m.Match(body(`{}`)) {
			t.Error("ignore should still require the member to be present")
		}
		if m.Match(body(`{"b":1}`)) {
			t.Error("ignore should not accept a different member")
		}
	})

	// ignore-element is not a synonym for ignore, despite the name: it also
	// stands in for a member that is not there. Probed side by side.
	t.Run("ignore-element additionally makes the member optional", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.ignore-element}\"}"}`)
		for _, actual := range []string{`{"a":{"deep":1}}`, `{"a":1}`, `{"a":null}`, `{}`} {
			if !m.Match(body(actual)) {
				t.Errorf("ignore-element should accept %s", actual)
			}
		}
		// It relaxes the member it stands for, not the whole document.
		if m.Match(body(`{"b":1}`)) {
			t.Error("ignore-element should not accept an unexpected extra member")
		}

		// The contrast that makes the distinction real.
		ignore := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.ignore}\"}"}`)
		if ignore.Match(body(`{}`)) {
			t.Error("ignore should still require the member to be present")
		}
	})

	t.Run("any-string accepts only strings", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.any-string}\"}"}`)
		if !m.Match(body(`{"a":"anything"}`)) {
			t.Error("any-string should accept a string")
		}
		if !m.Match(body(`{"a":""}`)) {
			t.Error("any-string should accept an empty string")
		}
		for _, actual := range []string{`{"a":42}`, `{"a":true}`, `{"a":null}`, `{"a":[]}`} {
			if m.Match(body(actual)) {
				t.Errorf("any-string should reject %s", actual)
			}
		}
	})

	t.Run("any-number accepts only numbers", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.any-number}\"}"}`)
		if !m.Match(body(`{"a":42}`)) || !m.Match(body(`{"a":-1.5}`)) || !m.Match(body(`{"a":0}`)) {
			t.Error("any-number should accept numbers")
		}
		if m.Match(body(`{"a":"42"}`)) {
			t.Error("any-number should reject the string \"42\"")
		}
	})

	t.Run("any-boolean accepts only booleans", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.any-boolean}\"}"}`)
		if !m.Match(body(`{"a":true}`)) || !m.Match(body(`{"a":false}`)) {
			t.Error("any-boolean should accept booleans")
		}
		if m.Match(body(`{"a":"true"}`)) || m.Match(body(`{"a":1}`)) {
			t.Error("any-boolean should reject non-booleans")
		}
	})

	t.Run("regex is a full match against a string", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.regex}[a-z]+\"}"}`)
		if !m.Match(body(`{"a":"abc"}`)) {
			t.Error("regex should accept a fully matching string")
		}
		// Full-match semantics, verified against the pinned WireMock.
		for _, actual := range []string{`{"a":"abc1"}`, `{"a":"1abc"}`, `{"a":""}`, `{"a":42}`} {
			if m.Match(body(actual)) {
				t.Errorf("regex should reject %s", actual)
			}
		}
	})

	t.Run("placeholders work at depth and inside arrays", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"o\":{\"a\":\"${json-unit.any-number}\"},\"xs\":[\"${json-unit.any-string}\",2]}"}`)
		if !m.Match(body(`{"o":{"a":7},"xs":["s",2]}`)) {
			t.Error("placeholders should resolve at any depth")
		}
		if m.Match(body(`{"o":{"a":"7"},"xs":["s",2]}`)) {
			t.Error("a nested placeholder should still constrain the type")
		}
		if m.Match(body(`{"o":{"a":7},"xs":["s",3]}`)) {
			t.Error("literal siblings of a placeholder must still match")
		}
	})

	t.Run("placeholders compose with the relaxation flags", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.any-string}\"}","ignoreExtraElements":true}`)
		if !m.Match(body(`{"a":"x","extra":1}`)) {
			t.Error("a placeholder should work alongside ignoreExtraElements")
		}
	})

	// An unrecognised placeholder is refused at registration. Comparing it as
	// literal text would mean the stub silently never matches.
	t.Run("an unknown placeholder is rejected", func(t *testing.T) {
		_, probs := Compile(json.RawMessage(
			`{"equalToJson":"{\"a\":\"${json-unit.no-such-thing}\"}"}`), "/x", testOpts())
		if len(probs) == 0 {
			t.Fatal("an unknown json-unit placeholder must be rejected")
		}
		if !strings.Contains(probs[0].Detail, "json-unit") {
			t.Errorf("the problem should name the placeholder, got %q", probs[0].Detail)
		}
	})

	t.Run("an uncompilable regex placeholder is rejected", func(t *testing.T) {
		_, probs := Compile(json.RawMessage(
			`{"equalToJson":"{\"a\":\"${json-unit.regex}(unclosed\"}"}`), "/x", testOpts())
		if len(probs) == 0 {
			t.Fatal("an invalid pattern inside a placeholder must be rejected at registration")
		}
	})

	// Text that merely looks similar is still compared literally.
	t.Run("a non-placeholder string is untouched", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"$notaplaceholder\"}"}`)
		if !m.Match(body(`{"a":"$notaplaceholder"}`)) {
			t.Error("ordinary text should compare literally")
		}
		if m.Match(body(`{"a":"anything"}`)) {
			t.Error("ordinary text must not behave as a placeholder")
		}
	})
}

func TestCombinators(t *testing.T) {
	t.Run("and requires every child", func(t *testing.T) {
		m := compile(t, `{"and":[{"contains":"a"},{"contains":"b"}]}`)
		if !m.Match(NewKeyValues("ab")) {
			t.Error("both satisfied should match")
		}
		if m.Match(NewKeyValues("a")) {
			t.Error("one satisfied should not match an and")
		}
	})

	t.Run("or requires one child", func(t *testing.T) {
		m := compile(t, `{"or":[{"equalTo":"a"},{"equalTo":"b"}]}`)
		if !m.Match(NewKeyValues("a")) {
			t.Error("first alternative should match")
		}
		if !m.Match(NewKeyValues("b")) {
			t.Error("second alternative should match")
		}
		if m.Match(NewKeyValues("c")) {
			t.Error("neither alternative should not match")
		}
	})

	t.Run("not inverts", func(t *testing.T) {
		m := compile(t, `{"not":{"equalTo":"a"}}`)
		if m.Match(NewKeyValues("a")) {
			t.Error("not should reject what its child accepts")
		}
		if !m.Match(NewKeyValues("b")) {
			t.Error("not should accept what its child rejects")
		}
	})

	t.Run("combinators nest", func(t *testing.T) {
		m := compile(t, `{"or":[{"and":[{"contains":"a"},{"contains":"b"}]},{"equalTo":"z"}]}`)
		if !m.Match(NewKeyValues("ab")) {
			t.Error("the and branch should satisfy the or")
		}
		if !m.Match(NewKeyValues("z")) {
			t.Error("the equalTo branch should satisfy the or")
		}
		if m.Match(NewKeyValues("a")) {
			t.Error("neither branch satisfied should not match")
		}
	})
}

// A combinator is a whole criterion, so the multi-value any-of rule applies to
// it and not to the leaves underneath it: WireMock evaluates the complete
// pattern once per value and takes any-of. Applying any-of leaf by leaf instead
// lets one value satisfy one branch while a different value satisfies the next,
// which inverts both combinators below. Every expectation here was read off the
// pinned WireMock with the values arriving as ?tag=a&tag=b.
func TestCombinatorsAreEvaluatedPerValue(t *testing.T) {
	multi := NewKeyValues("a", "b")

	// No single value fails "a", so `not` holds on the strength of "b" — even
	// though the operand it wraps is satisfied by the key as a whole.
	for _, doc := range []string{
		`{"not":{"matches":"a"}}`,
		`{"not":{"contains":"a"}}`,
		`{"not":{"equalTo":"a"}}`,
		`{"not":{"equalTo":"z"}}`,
		// doesNotMatch is itself any-of, so negating it is not a double
		// negative that cancels: "a" fails it, which satisfies the `not`.
		`{"not":{"doesNotMatch":"a"}}`,
	} {
		if !compile(t, doc).Match(multi) {
			t.Errorf(`%s should hold over ["a","b"]`, doc)
		}
	}

	// …and conversely, no single value satisfies both branches, so the
	// conjunction does not hold at all.
	for _, doc := range []string{
		`{"and":[{"matches":"a"},{"matches":"b"}]}`,
		`{"and":[{"contains":"a"},{"contains":"b"}]}`,
		`{"and":[{"matches":"a"},{"doesNotMatch":"a"}]}`,
	} {
		if compile(t, doc).Match(multi) {
			t.Errorf(`%s should not hold over ["a","b"]: no one value satisfies both`, doc)
		}
	}

	if !compile(t, `{"or":[{"equalTo":"a"},{"equalTo":"z"}]}`).Match(multi) {
		t.Error("or should hold when a value satisfies a branch")
	}
	if compile(t, `{"or":[{"equalTo":"y"},{"equalTo":"z"}]}`).Match(multi) {
		t.Error("or should not hold when no value satisfies any branch")
	}

	three := NewKeyValues("a", "b", "c")
	if !compile(t, `{"and":[{"matches":"."},{"not":{"equalTo":"a"}}]}`).Match(three) {
		t.Error(`"b" satisfies both branches, so the conjunction should hold`)
	}
	if !compile(t, `{"not":{"matches":"a|b"}}`).Match(three) {
		t.Error(`"c" fails the pattern, so not should hold`)
	}
	if compile(t, `{"not":{"matches":"a|b|c"}}`).Match(three) {
		t.Error("every value matching leaves nothing for not to hold on")
	}

	// Over a single value there is nothing to split, and the combinators are
	// the plain logical operators again — which is why the rule is easy to miss.
	single := NewKeyValues("a")
	if !compile(t, `{"and":[{"matches":"a"},{"contains":"a"}]}`).Match(single) {
		t.Error("single value: both branches satisfied should hold")
	}
	if compile(t, `{"not":{"matches":"a"}}`).Match(single) {
		t.Error("single value: not should invert")
	}
}

// memberPath is a JSONPathEvaluator over a single top-level member, which is
// all these tests need: the package deliberately does not depend on the path
// engine, so the criterion is built rather than compiled.
type memberPath string

func (p memberPath) Match(doc any) bool {
	_, found := p.Select(doc)
	return found
}

func (p memberPath) Select(doc any) ([]any, bool) {
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := obj[string(p)]
	if !ok {
		return nil, false
	}
	return []any{v}, true
}

func (p memberPath) Source() string { return "$." + string(p) }

// The criteria that read the subject as one document take the same per-value
// rule, because a subject's JSON() is the FIRST value's document: without the
// split they answer for one value of a repeated key and never look at the rest.
// A header carrying {"a":1} and {"b":2} satisfies matchesJsonPath $.b on the
// pinned WireMock, on the strength of the second value.
func TestJSONCriteriaAreEvaluatedPerValue(t *testing.T) {
	multi := NewKeyValues(`{"a":1}`, `{"b":2}`)

	for _, doc := range []string{
		`{"equalToJson":"{\"a\":1}"}`,
		`{"equalToJson":"{\"b\":2}"}`,
	} {
		if !compile(t, doc).Match(multi) {
			t.Errorf(`%s should hold: one value satisfies it`, doc)
		}
	}
	if compile(t, `{"equalToJson":"{\"c\":3}"}`).Match(multi) {
		t.Error("equalToJson should not hold when no value satisfies it")
	}

	for _, c := range []struct {
		path string
		want bool
	}{{"a", true}, {"b", true}, {"zz", false}} {
		m := &MatchesJSONPath{Path: memberPath(c.path)}
		if got := m.Match(multi); got != c.want {
			t.Errorf("matchesJsonPath $.%s over two values: got %v, want %v", c.path, got, c.want)
		}
	}

	// The nested form is split as one criterion, so the value that supplies the
	// path is the value the inner matcher is applied to.
	nested := &MatchesJSONPath{Path: memberPath("b"), Inner: &EqualTo{Expected: "2"}}
	if !nested.Match(multi) {
		t.Error("the second value selects $.b = 2, which the inner matcher accepts")
	}

	// A value that is not JSON at all is skipped rather than deciding the
	// criterion, which is the same any-of rule seen from the other side.
	ragged := NewKeyValues("notjson", `{"b":2}`)
	if !(&MatchesJSONPath{Path: memberPath("b")}).Match(ragged) {
		t.Error("an unparseable first value must not mask a later one that matches")
	}
	if !compile(t, `{"equalToJson":"{\"b\":2}"}`).Match(ragged) {
		t.Error("equalToJson should reach past an unparseable value too")
	}

	// The inconsistency this closes: wrapping a criterion in a one-armed `or`
	// is a no-op, and has to stay one.
	for _, m := range []Matcher{
		&MatchesJSONPath{Path: memberPath("b")},
		compile(t, `{"equalToJson":"{\"b\":2}"}`),
	} {
		bare := m.Match(multi)
		wrapped := (&Or{Matchers: []Matcher{m}}).Match(multi)
		if bare != wrapped {
			t.Errorf("%s: bare gave %v, or-wrapped gave %v", m.Describe(), bare, wrapped)
		}
	}
}

// Several matcher keys on one document are a conjunction, which is how
// WireMock reads them.
func TestSeveralKeysOnOneDocumentAreAnd(t *testing.T) {
	m := compile(t, `{"contains":"order","doesNotContain":"draft"}`)
	if !m.Match(NewKeyValues("my-order-42")) {
		t.Error("both criteria satisfied should match")
	}
	if m.Match(NewKeyValues("my-draft-order")) {
		t.Error("the second criterion should still be applied")
	}
	if m.Match(NewKeyValues("my-invoice")) {
		t.Error("the first criterion should still be applied")
	}
}

// ignoreArrayOrder has to be independent of the order it is given, which is
// less obvious than it sounds: as soon as an expected element can pair with
// more than one actual element, pairing them first-come-first-served can
// consume the partner a later element needed and report a non-match for an
// array that matches. Placeholders and ignoreExtraElements both create exactly
// that ambiguity. Every pair below matched on the pinned WireMock in both
// orders.
func TestIgnoreArrayOrderIsIndependentOfOrder(t *testing.T) {
	body := func(s string) Subject { return NewBody([]byte(s)) }

	t.Run("a placeholder competing with a literal", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-number}\",2]}","ignoreArrayOrder":true}`)
		// The placeholder matches 2 as readily as 7, so taking 2 first strands
		// the literal.
		for _, actual := range []string{`{"xs":[7,2]}`, `{"xs":[2,7]}`, `{"xs":[2,2]}`} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match whichever order it arrives in", actual)
			}
		}
		if m.Match(body(`{"xs":[7,9]}`)) {
			t.Error("order-independence must not turn into accepting a wrong element")
		}
	})

	t.Run("a placeholder competing with a string literal", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-string}\",\"b\"]}","ignoreArrayOrder":true}`)
		for _, actual := range []string{`{"xs":["a","b"]}`, `{"xs":["b","a"]}`, `{"xs":["b","b"]}`} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match whichever order it arrives in", actual)
			}
		}
	})

	t.Run("ignoreExtraElements makes objects ambiguous", func(t *testing.T) {
		// {"a":1} matches both actual objects once extra members are ignored.
		m := compileBody(t, `{"equalToJson":"{\"xs\":[{\"a\":1},{\"a\":1,\"b\":2}]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		for _, actual := range []string{
			`{"xs":[{"a":1},{"a":1,"b":2}]}`,
			`{"xs":[{"a":1,"b":2},{"a":1}]}`,
		} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match whichever order it arrives in", actual)
			}
		}
	})

	t.Run("three-way ambiguity", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-number}\",`+
			`\"${json-unit.any-string}\",\"z\"]}","ignoreArrayOrder":true}`)
		// "z" satisfies any-string as well as itself, so every permutation has
		// to be resolved rather than walked.
		for _, actual := range []string{
			`{"xs":[1,"y","z"]}`, `{"xs":["z","y",1]}`, `{"xs":["z",1,"y"]}`,
			`{"xs":["y","z",1]}`,
		} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match whichever order it arrives in", actual)
			}
		}
	})

	t.Run("a pairing that genuinely does not exist is still refused", func(t *testing.T) {
		// Two any-number placeholders and one number: there is no way to pair
		// them off, whatever order the search takes.
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-number}\",`+
			`\"${json-unit.any-number}\"]}","ignoreArrayOrder":true}`)
		if m.Match(body(`{"xs":[1,"s"]}`)) {
			t.Error("a string cannot stand in for the second any-number")
		}
		if !m.Match(body(`{"xs":[1,2]}`)) {
			t.Error("two numbers should pair with two any-number placeholders")
		}
	})

	t.Run("duplicates are still counted", func(t *testing.T) {
		// Pairing is one-to-one, so ignoreArrayOrder compares multisets and not
		// sets: one actual element cannot answer two expected ones.
		m := compileBody(t, `{"equalToJson":"{\"xs\":[1,1,2]}","ignoreArrayOrder":true}`)
		if !m.Match(body(`{"xs":[2,1,1]}`)) {
			t.Error("the same multiset in another order should match")
		}
		if m.Match(body(`{"xs":[1,2,2]}`)) {
			t.Error("a different multiset should not match")
		}
	})
}

// ignoreExtraElements forgives elements the expected document never accounted
// for, in arrays as well as in objects. Which elements those are depends on the
// other flag: positionally they are the tail of the actual array, and under
// ignoreArrayOrder they are whichever ones no expected element claims. Every
// pairing below was probed against the pinned WireMock.
func TestIgnoreExtraElementsRelaxesArrayLength(t *testing.T) {
	body := func(s string) Subject { return NewBody([]byte(s)) }

	t.Run("a longer array matches from the front", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[1,2]}","ignoreExtraElements":true}`)
		for _, actual := range []string{`{"xs":[1,2,3]}`, `{"xs":[1,2,3,4,5]}`} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match: the elements past the expected ones are ignored", actual)
			}
		}
		// Ignored where they fall, not wherever they would help — otherwise the
		// flag would quietly imply ignoreArrayOrder as well.
		for _, actual := range []string{`{"xs":[2,1,3]}`, `{"xs":[1,3,2]}`, `{"xs":[3,1,2]}`} {
			if m.Match(body(actual)) {
				t.Errorf("%s should not match: the expected elements must be the leading ones", actual)
			}
		}
		// Nor does the relaxation reach the elements it does compare.
		if m.Match(body(`{"xs":[1,"2",3]}`)) {
			t.Error("the string \"2\" is not the number 2, extra elements or not")
		}
	})

	t.Run("a shorter array is still a mismatch", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[1,2]}","ignoreExtraElements":true}`)
		for _, actual := range []string{`{"xs":[1]}`, `{"xs":[]}`} {
			if m.Match(body(actual)) {
				t.Errorf("%s is missing an expected element, which the flag does not forgive", actual)
			}
		}
	})

	t.Run("an empty expected array accounts for nothing", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[]}","ignoreExtraElements":true}`)
		if !m.Match(body(`{"xs":[1,2]}`)) {
			t.Error("every element is extra, so every element is ignored")
		}
	})

	t.Run("the relaxation applies at every depth", func(t *testing.T) {
		nested := compileBody(t, `{"equalToJson":"{\"o\":{\"xs\":[1,2]}}","ignoreExtraElements":true}`)
		if !nested.Match(body(`{"o":{"xs":[1,2,3]}}`)) {
			t.Error("an array below the root should be relaxed too")
		}
		root := compileBody(t, `{"equalToJson":"[1,2]","ignoreExtraElements":true}`)
		if !root.Match(body(`[1,2,3]`)) {
			t.Error("the root array should be relaxed as well")
		}
		arrays := compileBody(t, `{"equalToJson":"{\"xs\":[[1,2],[3]]}","ignoreExtraElements":true}`)
		if !arrays.Match(body(`{"xs":[[1,2,9],[3,4],[5]]}`)) {
			t.Error("arrays inside arrays should be relaxed at both levels")
		}
	})

	t.Run("without the flag the length stays exact", func(t *testing.T) {
		strict := compileBody(t, `{"equalToJson":"{\"xs\":[1,2]}"}`)
		if strict.Match(body(`{"xs":[1,2,3]}`)) {
			t.Error("the length relaxation must come from ignoreExtraElements")
		}
		unordered := compileBody(t, `{"equalToJson":"{\"xs\":[1,2]}","ignoreArrayOrder":true}`)
		if unordered.Match(body(`{"xs":[1,2,3]}`)) {
			t.Error("ignoreArrayOrder gives up the positions, not the count")
		}
		if !unordered.Match(body(`{"xs":[2,1]}`)) {
			t.Error("a permutation of the same elements should still match")
		}
	})

	t.Run("placeholders keep their rule in the leading slots", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-string}\",2]}",`+
			`"ignoreExtraElements":true}`)
		for _, actual := range []string{`{"xs":["s",2]}`, `{"xs":["s",2,9]}`} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match", actual)
			}
		}
		if m.Match(body(`{"xs":[9,"s",2]}`)) {
			t.Error("a wildcard slot is still a position, so the leading element must satisfy it")
		}
		typed := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-number}\",2]}",`+
			`"ignoreExtraElements":true}`)
		if typed.Match(body(`{"xs":["s",2,9]}`)) {
			t.Error("the placeholder still constrains its own type")
		}
	})

	t.Run("ignore-element does not make an array slot optional", func(t *testing.T) {
		// It stands in for an absent *member*; an array has no member names, so
		// the slot it occupies still has to exist.
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.ignore-element}\"]}",`+
			`"ignoreExtraElements":true}`)
		if m.Match(body(`{"xs":[]}`)) {
			t.Error("an empty array is short of the slot the placeholder occupies")
		}
		for _, actual := range []string{`{"xs":[5]}`, `{"xs":[5,9]}`} {
			if !m.Match(body(actual)) {
				t.Errorf("%s should match", actual)
			}
		}
	})

	t.Run("with ignoreArrayOrder it becomes a subset test", func(t *testing.T) {
		m := compileBody(t, `{"equalToJson":"{\"xs\":[1,2]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		for _, actual := range []string{
			`{"xs":[1,2]}`, `{"xs":[3,2,1]}`, `{"xs":[2,1,9]}`, `{"xs":[1,2,3]}`,
		} {
			if !m.Match(body(actual)) {
				t.Errorf("%s contains both expected elements, so it should match", actual)
			}
		}
		for _, actual := range []string{`{"xs":[1]}`, `{"xs":[]}`, `{"xs":[1,9]}`} {
			if m.Match(body(actual)) {
				t.Errorf("%s cannot answer both expected elements", actual)
			}
		}
		empty := compileBody(t, `{"equalToJson":"{\"xs\":[]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		if !empty.Match(body(`{"xs":[1]}`)) {
			t.Error("an expected array claiming nothing is satisfied by anything")
		}
	})

	t.Run("the subset test keeps the pairing one-to-one", func(t *testing.T) {
		// Relaxed cardinality is not a relaxed pairing: two expected 1s need two
		// actual 1s, however many other elements arrive.
		m := compileBody(t, `{"equalToJson":"{\"xs\":[1,1]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		if m.Match(body(`{"xs":[1,2,3]}`)) {
			t.Error("one actual element cannot answer two expected ones")
		}
		if !m.Match(body(`{"xs":[1,1,2]}`)) {
			t.Error("two actual 1s should answer two expected 1s")
		}
	})

	t.Run("the subset test resolves ambiguity rather than walking it", func(t *testing.T) {
		// The unclaimed elements make the pairing harder, not easier: the search
		// still has to find the assignment that leaves no expected element
		// stranded (deviation #25 — WireMock stops looking here).
		m := compileBody(t, `{"equalToJson":"{\"xs\":[\"${json-unit.any-number}\",2]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		for _, actual := range []string{
			`{"xs":[2,5,9]}`, `{"xs":[5,2,9]}`, `{"xs":[9,2,5]}`, `{"xs":[2,5]}`,
		} {
			if !m.Match(body(actual)) {
				t.Errorf("%s has a number for the placeholder and a 2 beside it", actual)
			}
		}
		if m.Match(body(`{"xs":[2,"s","t"]}`)) {
			t.Error("the placeholder and the literal cannot share the one 2")
		}
		objects := compileBody(t, `{"equalToJson":"{\"xs\":[{\"a\":1}]}",`+
			`"ignoreArrayOrder":true,"ignoreExtraElements":true}`)
		if !objects.Match(body(`{"xs":[{"z":9},{"a":1,"b":2}]}`)) {
			t.Error("the expected object should pair with the actual one that contains it")
		}
	})
}

// Every deferred matcher must be rejected by name, so a team migrating from
// WireMock learns exactly which roadmap item they are waiting on.
func TestDeferredMatchersAreRejectedByName(t *testing.T) {
	cases := map[string]string{
		`{"matchesXPath":"//a"}`:           "matchesXPath",
		`{"equalToXml":"<a/>"}`:            "equalToXml",
		`{"hasExactly":[{"equalTo":"a"}]}`: "hasExactly",
		`{"includes":[{"equalTo":"a"}]}`:   "includes",
	}
	for doc, want := range cases {
		m, probs := Compile(json.RawMessage(doc), "/request/bodyPatterns/0", testOpts())
		if m != nil {
			t.Errorf("%s should not compile", doc)
		}
		if len(probs) == 0 {
			t.Errorf("%s should be rejected", doc)
			continue
		}
		if !probs[0].Deferred {
			t.Errorf("%s should be reported as deferred, not malformed", doc)
		}
		if !strings.Contains(probs[0].Detail, want) {
			t.Errorf("%s: detail %q should name %q", doc, probs[0].Detail, want)
		}
		if !strings.HasPrefix(probs[0].Pointer, "/request/bodyPatterns/0/") {
			t.Errorf("%s: pointer %q should locate the offending field", doc, probs[0].Pointer)
		}
	}
}

func TestUnknownMatcherIsRejected(t *testing.T) {
	_, probs := Compile(json.RawMessage(`{"soundsLike":"x"}`), "", testOpts())
	if len(probs) == 0 {
		t.Fatal("an unknown matcher must be rejected rather than ignored")
	}
	if !strings.Contains(probs[0].Detail, "soundsLike") {
		t.Errorf("the problem should name the unknown matcher, got %q", probs[0].Detail)
	}
}

// Every problem in one document is reported at once, so a CI user fixes them
// all in one round (SPEC Appendix B).
func TestAllProblemsReportedTogether(t *testing.T) {
	_, probs := Compile(json.RawMessage(`{"matchesXPath":"//a","equalToXml":"<a/>","bogus":1}`), "", testOpts())
	if len(probs) < 3 {
		t.Fatalf("expected every problem to be reported, got %d: %v", len(probs), probs)
	}
}

func TestInvalidRegexIsRejectedAtRegistration(t *testing.T) {
	_, probs := Compile(json.RawMessage(`{"matches":"(unclosed"}`), "/x", testOpts())
	if len(probs) == 0 {
		t.Fatal("an uncompilable regex must be rejected at registration, never at serve time")
	}
	if !strings.Contains(probs[0].Detail, "does not compile") {
		t.Errorf("detail should explain the failure, got %q", probs[0].Detail)
	}
}

func TestEmptyAndMalformedDocuments(t *testing.T) {
	for _, doc := range []string{`{}`, `[]`, `"a string"`, `null`} {
		if _, probs := Compile(json.RawMessage(doc), "", testOpts()); len(probs) == 0 {
			t.Errorf("%s should be rejected", doc)
		}
	}
}

// The body is parsed at most once however many matchers examine it, which is
// what keeps a multi-matcher stub from re-parsing per criterion.
func TestBodyJSONIsMemoized(t *testing.T) {
	b := NewBody([]byte(`{"a":1}`))
	v1, ok1 := b.JSON()
	v2, ok2 := b.JSON()
	if !ok1 || !ok2 {
		t.Fatal("valid JSON should parse")
	}
	m1, _ := v1.(map[string]any)
	m2, _ := v2.(map[string]any)
	m1["mutated"] = true
	if _, seen := m2["mutated"]; !seen {
		t.Error("repeated JSON() calls should return the same parsed document, not re-parse")
	}

	bad := NewBody([]byte(`{`))
	if _, ok := bad.JSON(); ok {
		t.Error("invalid JSON should report not-ok")
	}
	if _, ok := bad.JSON(); ok {
		t.Error("the invalid result should be memoized too")
	}
}

func TestBodyResetDropsReferences(t *testing.T) {
	b := NewBody([]byte(`{"a":1}`))
	if _, ok := b.JSON(); !ok {
		t.Fatal("should parse")
	}
	b.Reset()
	if b.Present() {
		t.Error("a reset body should not be present")
	}
	if b.Bytes() != nil {
		t.Error("a reset body must drop its buffer so pooled memory does not outlive the request")
	}
	if v, ok := b.JSON(); ok || v != nil {
		t.Error("a reset body should have no parsed document")
	}
}

func BenchmarkEqualToHeader(b *testing.B) {
	m := &EqualTo{Expected: "application/json"}
	subject := NewKeyValues("application/json")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !m.Match(subject) {
			b.Fatal("expected a match")
		}
	}
}

func BenchmarkEqualToJSONBody(b *testing.B) {
	m, probs := Compile(json.RawMessage(`{"equalToJson":"{\"channel\":\"web\",\"id\":42}"}`), "", Options{})
	if len(probs) > 0 {
		b.Fatal(probs)
	}
	raw := []byte(`{"id":42,"channel":"web"}`)
	body := NewBody(raw)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		body.Set(raw)
		if !m.Match(body) {
			b.Fatal("expected a match")
		}
	}
}

// Over a repeated key the negative matchers are NOT the complement of their
// positive twins: both use any-of, so a header carrying "a" and "b" satisfies
// matches("a") and doesNotMatch("a") at the same time. Inverting the positive
// result — the obvious implementation — gets this wrong.
func TestNegativeMatchersUseAnyOfNotComplement(t *testing.T) {
	multi := NewKeyValues("a", "b")

	if !compile(t, `{"matches":"a"}`).Match(multi) {
		t.Error("matches should hold: one value matches")
	}
	if !compile(t, `{"doesNotMatch":"a"}`).Match(multi) {
		t.Error("doesNotMatch should also hold: one value does not match")
	}
	if !compile(t, `{"contains":"a"}`).Match(multi) {
		t.Error("contains should hold: one value contains it")
	}
	if !compile(t, `{"doesNotContain":"a"}`).Match(multi) {
		t.Error("doesNotContain should also hold: one value does not contain it")
	}

	// Over a single value the two ARE complements, which is why the difference
	// is easy to miss.
	single := NewKeyValues("a")
	if !compile(t, `{"matches":"a"}`).Match(single) {
		t.Error("single value: matches should hold")
	}
	if compile(t, `{"doesNotMatch":"a"}`).Match(single) {
		t.Error("single value: doesNotMatch should not hold")
	}

	// Every value failing satisfies the negative form.
	if !compile(t, `{"doesNotMatch":"z"}`).Match(multi) {
		t.Error("no value matching should satisfy doesNotMatch")
	}
	if compile(t, `{"matches":"z"}`).Match(multi) {
		t.Error("no value matching should not satisfy matches")
	}
}

// A zero-length body is ABSENT, which is WireMock's rule and not the obvious
// one. Every value matcher fails on it — including matches:".*" and equalTo:""
// which are true of every string — and only absent:true succeeds.
func TestEmptyBodyIsAbsent(t *testing.T) {
	empty := NewBody(nil)
	zeroLen := NewBody([]byte{})

	for _, subject := range []Subject{empty, zeroLen} {
		if subject.Present() {
			t.Error("a zero-length body should not be present")
		}
		for _, doc := range []string{
			`{"matches":".*"}`, `{"matches":"a*"}`, `{"equalTo":""}`,
			`{"contains":""}`, `{"equalToJson":"{}"}`,
		} {
			if compileBody(t, doc).Match(subject) {
				t.Errorf("%s should fail against an empty body", doc)
			}
		}
		if !compile(t, `{"absent":true}`).Match(subject) {
			t.Error("absent:true should match an empty body")
		}
		// binaryEqualTo with an empty operand is the one exception to the rule
		// above, and it is not an inconsistency: every matcher in that list asks
		// a question about a body's content, which an absent body has none of,
		// while this one compares two byte arrays that are both empty. WireMock
		// answers it the same way. Without it this would be the only body
		// matcher that cannot express "the body is empty".
		if !compileBody(t, `{"binaryEqualTo":""}`).Match(subject) {
			t.Error(`binaryEqualTo:"" should match an empty body`)
		}
		// And it stays a comparison rather than becoming a wildcard.
		if compileBody(t, `{"binaryEqualTo":"eA=="}`).Match(subject) {
			t.Error("a non-empty binaryEqualTo operand should fail against an empty body")
		}
	}

	// A body with content behaves normally.
	full := NewBody([]byte("x"))
	if !full.Present() {
		t.Error("a non-empty body is present")
	}
	if !compile(t, `{"matches":".*"}`).Match(full) {
		t.Error(".* should match a non-empty body")
	}
	if compile(t, `{"absent":true}`).Match(full) {
		t.Error("absent:true should not match a non-empty body")
	}
}

// Absence semantics are per field kind: an absent header satisfies a negative
// matcher, an absent cookie satisfies neither form. Verified directly against
// the pinned WireMock.
func TestAbsenceStrictnessIsPerFieldKind(t *testing.T) {
	header := AbsentKey()
	if !compile(t, `{"doesNotMatch":"zzz"}`).Match(header) {
		t.Error("an absent header should satisfy doesNotMatch")
	}
	if compile(t, `{"matches":"zzz"}`).Match(header) {
		t.Error("an absent header should not satisfy matches")
	}

	cookie := &KeyValues{}
	cookie.SetStrictAbsence(false, nil)
	if compile(t, `{"doesNotMatch":"zzz"}`).Match(cookie) {
		t.Error("an absent cookie should satisfy neither form, not even doesNotMatch")
	}
	if compile(t, `{"matches":"zzz"}`).Match(cookie) {
		t.Error("an absent cookie should not satisfy matches")
	}
	if !compile(t, `{"absent":true}`).Match(cookie) {
		t.Error("absent:true is how a stub asserts a missing cookie")
	}

	// A present cookie behaves like any other subject.
	present := &KeyValues{}
	present.SetStrictAbsence(true, []string{"abc"})
	if !compile(t, `{"doesNotMatch":"zzz"}`).Match(present) {
		t.Error("a present cookie failing the pattern should satisfy doesNotMatch")
	}
}

// The stricter rule is decided before the criterion is looked at, so wrapping a
// matcher in a combinator does not get round it: an absent cookie matches only
// a bare `absent`, and `or(absent, …)` is not one. Probed directly — the same
// criteria against an absent header all match, which is what makes this a rule
// about the field kind rather than about the operands.
func TestStrictAbsenceSurvivesCombinators(t *testing.T) {
	cookie := func() Subject {
		k := &KeyValues{}
		k.SetStrictAbsence(false, nil)
		return k
	}

	for _, doc := range []string{
		`{"not":{"matches":"zzz"}}`,
		`{"not":{"contains":"zzz"}}`,
		`{"not":{"absent":true}}`,
		`{"and":[{"doesNotMatch":"zzz"},{"doesNotContain":"zzz"}]}`,
		`{"or":[{"absent":true},{"equalTo":"a"}]}`,
	} {
		if compile(t, doc).Match(cookie()) {
			t.Errorf("%s should not match an absent cookie", doc)
		}
	}

	// The one criterion that does, and the reason the others cannot be allowed
	// to: `absent` is how a stub asserts the cookie is missing.
	if !compile(t, `{"absent":true}`).Match(cookie()) {
		t.Error("absent:true should match an absent cookie")
	}

	// An absent header takes the ordinary path, combinators included.
	for _, doc := range []string{
		`{"not":{"matches":"zzz"}}`,
		`{"and":[{"doesNotMatch":"zzz"},{"doesNotContain":"zzz"}]}`,
		`{"or":[{"absent":true},{"equalTo":"a"}]}`,
	} {
		if !compile(t, doc).Match(AbsentKey()) {
			t.Errorf("%s should match an absent header", doc)
		}
	}
	if compile(t, `{"not":{"absent":true}}`).Match(AbsentKey()) {
		t.Error("not(absent) should not match an absent header")
	}
}
