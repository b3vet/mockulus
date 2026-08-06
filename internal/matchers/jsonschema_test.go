// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"strings"
	"testing"
)

// The schema compile policies are tested in internal/jsonschemax. What is tested
// here is the matcher: which subject it reads, what it does with input that is
// not JSON, and how it behaves over a repeated key.

func TestMatchesJSONSchemaValidatesTheBody(t *testing.T) {
	m := compileBody(t, `{"matchesJsonSchema":{"type":"object","required":["id"],
		"properties":{"id":{"type":"integer"}}}}`)

	if !m.Match(NewBody([]byte(`{"id":1}`))) {
		t.Error("a conforming body should match")
	}
	for _, body := range []string{`{"id":"x"}`, `{"nope":1}`, `[1,2]`, `"hello"`, `42`} {
		if m.Match(NewBody([]byte(body))) {
			t.Errorf("%s should not satisfy the schema", body)
		}
	}
}

// TestBothOperandSpellingsCompile covers the two forms WireMock accepts: the
// schema inline, and the same schema as an escaped JSON string.
func TestBothOperandSpellingsCompile(t *testing.T) {
	inline := compileBody(t, `{"matchesJsonSchema":{"type":"object","required":["id"]}}`)
	escaped := compileBody(t, `{"matchesJsonSchema":"{\"type\":\"object\",\"required\":[\"id\"]}"}`)

	for _, m := range []Matcher{inline, escaped} {
		if !m.Match(NewBody([]byte(`{"id":1}`))) {
			t.Error("both operand spellings should accept a conforming body")
		}
		if m.Match(NewBody([]byte(`{"nope":1}`))) {
			t.Error("both operand spellings should reject a non-conforming body")
		}
	}
}

// TestABodyThatIsNotJSONIsAPlainNonMatch pins CHK-JS decision 1, which is a
// deliberate divergence.
//
// WireMock falls back to validating the raw request text as a JSON string, so
// `not json at all` satisfies `{"type":"string"}` there, and for a number,
// boolean or null body it tries both readings — which makes a schema and its own
// negation both match the body `4`. Here a body that is not JSON is simply not a
// match, which is what §6.7 already does for a body that is not JSON.
func TestABodyThatIsNotJSONIsAPlainNonMatch(t *testing.T) {
	// A schema that accepts every JSON document there is, so a non-match can
	// only mean the body never became a document.
	m := compileBody(t, `{"matchesJsonSchema":true}`)

	if !m.Match(NewBody([]byte(`{"a":1}`))) {
		t.Fatal("the always-true schema should accept a JSON body")
	}
	for _, body := range []string{`not json at all`, `<xml/>`, ``, `{"a":1`, `   `} {
		if m.Match(NewBody([]byte(body))) {
			t.Errorf("%q is not a JSON document, so it should not match", body)
		}
	}

	// The case that makes WireMock self-contradictory: a scalar body against a
	// string-typed schema. Here it is a straightforward non-match.
	str := compileBody(t, `{"matchesJsonSchema":{"type":"string"}}`)
	if str.Match(NewBody([]byte(`4`))) {
		t.Error("the number 4 is not a string; WireMock's raw-text reading is not reproduced")
	}
	if !str.Match(NewBody([]byte(`"4"`))) {
		t.Error("a JSON string body should still satisfy a string schema")
	}
}

// TestTrailingContentIsNotAJSONDocument keeps the strictness deviation #35
// already records for equalToJson and matchesJsonPath. WireMock reads the first
// value and ignores whatever follows it.
func TestTrailingContentIsNotAJSONDocument(t *testing.T) {
	m := compileBody(t, `{"matchesJsonSchema":{"type":"object","required":["id"]}}`)

	if !m.Match(NewBody([]byte(`{"id":1}`))) {
		t.Fatal("the control body should match")
	}
	for _, body := range []string{`{"id":1} trailing`, `{"id":1}{"id":2}`} {
		if m.Match(NewBody([]byte(body))) {
			t.Errorf("%s carries content after a complete document and should not match", body)
		}
	}
}

// TestSchemaMatcherWorksInKeyPositions covers the positions a content matcher
// appears in besides the body. WireMock validates in all of them.
func TestSchemaMatcherWorksInKeyPositions(t *testing.T) {
	m := compile(t, `{"matchesJsonSchema":{"type":"object","required":["id"]}}`)

	if !m.Match(NewKeyValues(`{"id":1}`)) {
		t.Error("a conforming header value should match")
	}
	if m.Match(NewKeyValues(`{"nope":1}`)) {
		t.Error("a non-conforming header value should not match")
	}
	if m.Match(NewKeyValues(`not json`)) {
		t.Error("a header value that is not JSON should not match")
	}
	if m.Match(AbsentKey()) {
		t.Error("an absent key cannot satisfy a schema")
	}
}

func TestRepeatedValuesFollowAnyOfForSchemas(t *testing.T) {
	m := compile(t, `{"matchesJsonSchema":{"type":"object","required":["id"]}}`)

	if !m.Match(NewKeyValues(`{"nope":1}`, `{"id":1}`)) {
		t.Error("any-of: one satisfying value is enough, whatever its position")
	}
	if !m.Match(NewKeyValues(`{"id":1}`, `{"nope":1}`)) {
		t.Error("any-of is order-independent")
	}
	if m.Match(NewKeyValues(`{"nope":1}`, `{"also":2}`)) {
		t.Error("no satisfying value must not match")
	}
}

// TestUnusableSchemasAreRefusedAtRegistration pins CHK-JS decision 3 at the
// compile boundary; the shapes themselves are covered in internal/jsonschemax.
func TestUnusableSchemasAreRefusedAtRegistration(t *testing.T) {
	for _, doc := range []string{
		`{"matchesJsonSchema":{"type":"banana"}}`,
		`{"matchesJsonSchema":{"$ref":"#/$defs/missing"}}`,
		`{"matchesJsonSchema":{"$ref":"http://example.invalid/s.json"}}`,
		`{"matchesJsonSchema":42}`,
		`{"matchesJsonSchema":"not json"}`,
	} {
		m, probs := Compile(json.RawMessage(doc), "/request/bodyPatterns/0", testOpts())
		if m != nil || len(probs) == 0 {
			t.Errorf("%s should be refused", doc)
			continue
		}
		// The kind is what the caller maps onto error code 1006, so a schema
		// problem must not arrive as a generic malformed one.
		if doc == `{"matchesJsonSchema":{"type":"banana"}}` && probs[0].Kind != ProblemSchema {
			t.Errorf("an uncompilable schema should be reported as a schema problem, got kind %v",
				probs[0].Kind)
		}
	}
}

// TestSchemaVersionOnlyAppliesToASchemaMatcher pins one of the CHK-JS decision-5
// refusals. WireMock drops the key silently anywhere else — and drops it before
// validating the value, so a nonsense version registers cleanly there.
func TestSchemaVersionOnlyAppliesToASchemaMatcher(t *testing.T) {
	// Alongside its own matcher it is honoured.
	m := compileBody(t, `{"matchesJsonSchema":{"type":"string","format":"email"},"schemaVersion":"V7"}`)
	if m.Match(NewBody([]byte(`"not-an-email"`))) {
		t.Error("V7 asserts format, so a malformed email should not match")
	}

	for _, doc := range []string{
		`{"equalTo":"x","schemaVersion":"V7"}`,
		`{"contains":"x","schemaVersion":"BANANA"}`,
		`{"and":[{"equalTo":"a"},{"equalTo":"b"}],"schemaVersion":"V7"}`,
	} {
		_, probs := Compile(json.RawMessage(doc), "/request/bodyPatterns/0", testOpts())
		if len(probs) == 0 {
			t.Errorf("%s should be refused: schemaVersion has nothing to apply to", doc)
			continue
		}
		if !strings.Contains(probs[0].Detail, "schemaVersion") {
			t.Errorf("the refusal should name schemaVersion, got %q", probs[0].Detail)
		}
	}
}

func TestDescribeNamesTheSchema(t *testing.T) {
	m := compileBody(t, `{"matchesJsonSchema":{"type":"object"}}`)
	got := m.Describe()
	if !strings.Contains(got, "matchesJsonSchema") {
		t.Errorf("Describe() = %q, want it to name the matcher", got)
	}

	// A long schema is truncated so a near-miss line stays readable.
	long := `{"matchesJsonSchema":{"type":"object","properties":{` +
		`"aaaaaaaaaa":{"type":"string"},"bbbbbbbbbb":{"type":"string"},` +
		`"cccccccccc":{"type":"string"},"dddddddddd":{"type":"string"}}}}`
	if d := compileBody(t, long).Describe(); !strings.Contains(d, "…") {
		t.Errorf("a long schema should be elided in Describe(), got %q", d)
	}
}
