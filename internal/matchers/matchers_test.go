// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// testPattern is a minimal PatternMatcher so this package's tests do not depend
// on the regex engine seam.
type testPattern struct{ re *regexp.Regexp }

func (p testPattern) MatchString(s string) bool { return p.re.MatchString(s) }
func (p testPattern) Source() string            { return p.re.String() }

func testOpts() Options {
	return Options{CompileRegex: func(pattern string) (PatternMatcher, error) {
		re, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
		if err != nil {
			return nil, err
		}
		return testPattern{re}, nil
	}}
}

// compile is a test helper that fails the test on any compilation problem.
func compile(t *testing.T, doc string) Matcher {
	t.Helper()
	m, probs := Compile(json.RawMessage(doc), "", testOpts())
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
	m := compile(t, `{"binaryEqualTo":"aGVsbG8="}`)
	if !m.Match(NewBody([]byte("hello"))) {
		t.Error("decoded operand should match the raw bytes")
	}
	if m.Match(NewBody([]byte("hellp"))) {
		t.Error("different bytes should not match")
	}
	if m.Match(NewBody([]byte("hello!"))) {
		t.Error("a longer body should not match")
	}

	if _, probs := Compile(json.RawMessage(`{"binaryEqualTo":"not!base64!"}`), "", testOpts()); len(probs) == 0 {
		t.Error("an invalid base64 operand must be rejected at registration")
	}
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

	// Deviation #5: placeholders are compared as literal text, not interpreted.
	t.Run("json-unit placeholders are literal", func(t *testing.T) {
		m := compile(t, `{"equalToJson":"{\"a\":\"${json-unit.any-string}\"}"}`)
		if m.Match(body(`{"a":"anything"}`)) {
			t.Error("the placeholder must not be interpreted (deviation #5)")
		}
		if !m.Match(body(`{"a":"${json-unit.any-string}"}`)) {
			t.Error("the placeholder should compare as literal text")
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

// Every deferred matcher must be rejected by name, so a team migrating from
// WireMock learns exactly which roadmap item they are waiting on.
func TestDeferredMatchersAreRejectedByName(t *testing.T) {
	cases := map[string]string{
		`{"matchesXPath":"//a"}`:                  "matchesXPath",
		`{"equalToXml":"<a/>"}`:                   "equalToXml",
		`{"matchesJsonSchema":{"type":"object"}}`: "matchesJsonSchema",
		`{"before":"2026-01-01T00:00:00Z"}`:       "before",
		`{"after":"2026-01-01T00:00:00Z"}`:        "after",
		`{"equalToDateTime":"2026-01-01"}`:        "equalToDateTime",
		`{"hasExactly":[{"equalTo":"a"}]}`:        "hasExactly",
		`{"includes":[{"equalTo":"a"}]}`:          "includes",
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
