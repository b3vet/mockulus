// SPDX-License-Identifier: Apache-2.0

package jsonschemax

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every expectation here is either the pinned oracle's own answer, recorded from
// WireMock 3.13.2 during the JS-1 probe stage, or a CHK-JS decision that
// deliberately departs from it. The cases that depart say so and say why.

// compile builds a validator, failing the test if the schema is refused.
func compile(t *testing.T, schema, version string) Validator {
	t.Helper()
	v, err := Compile(schema, version)
	if err != nil {
		t.Fatalf("Compile(%s, %q): %v", schema, version, err)
	}
	return v
}

// refuse asserts a schema is rejected, returning the message so a case can check
// it names the problem.
func refuse(t *testing.T, schema, version string) string {
	t.Helper()
	_, err := Compile(schema, version)
	if err == nil {
		t.Fatalf("Compile(%s, %q) was accepted, want a refusal", schema, version)
	}
	return err.Error()
}

// valid runs a document through a validator.
func valid(t *testing.T, v Validator, body string) bool {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("test body %s is not JSON: %v", body, err)
	}
	return v.Valid(doc)
}

func TestValidatesTheDocumentShape(t *testing.T) {
	v := compile(t, `{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`, "")

	if !valid(t, v, `{"id":1}`) {
		t.Error("a conforming document should validate")
	}
	for _, body := range []string{`{"id":"x"}`, `{"nope":1}`, `[1,2]`, `"hello"`, `42`, `null`} {
		if valid(t, v, body) {
			t.Errorf("%s should not satisfy an object schema requiring an integer id", body)
		}
	}
}

// TestBooleanSchemas covers the two degenerate schemas that are nonetheless
// legal and meaningful.
func TestBooleanSchemas(t *testing.T) {
	always := compile(t, `true`, "")
	never := compile(t, `false`, "")
	for _, body := range []string{`{"a":1}`, `[1]`, `"s"`, `42`, `null`} {
		if !valid(t, always, body) {
			t.Errorf("the always-true schema should accept %s", body)
		}
		if valid(t, never, body) {
			t.Errorf("the always-false schema should reject %s", body)
		}
	}
}

// TestFormatIsAssertedOnlyByTheOlderDrafts pins CHK-JS decision 2.
//
// The split is the specification's own vocabulary boundary and WireMock follows
// it: drafts 4, 6 and 7 assert `format`, and 2019-09 and 2020-12 treat it as an
// annotation. Since 2020-12 is the default, the out-of-the-box behaviour is that
// `format` does nothing — which surprises people, and is the behaviour to
// reproduce.
func TestFormatIsAssertedOnlyByTheOlderDrafts(t *testing.T) {
	const schema = `{"type":"object","properties":{"e":{"type":"string","format":"email"}}}`

	asserting := []string{"V4", "V6", "V7"}
	ignoring := []string{"V201909", "V202012", ""}

	for _, version := range asserting {
		v := compile(t, schema, version)
		if valid(t, v, `{"e":"not-an-email"}`) {
			t.Errorf("%s should assert format and reject a malformed email", version)
		}
		// The control: the schema is live, so a well-formed value still passes.
		if !valid(t, v, `{"e":"a@b.com"}`) {
			t.Errorf("%s rejected a well-formed email, so the schema itself is broken", version)
		}
	}

	for _, version := range ignoring {
		v := compile(t, schema, version)
		if !valid(t, v, `{"e":"not-an-email"}`) {
			t.Errorf("%s should ignore format; the default draft asserting it is the surprise", version)
		}
		// The control that proves the schema still does something: `type` holds
		// even where `format` does not.
		if valid(t, v, `{"e":123}`) {
			t.Errorf("%s should still enforce type", version)
		}
	}
}

// TestDocumentSchemaOverridesTheVersionParameter pins the precedence, in both
// directions, using probes constructed so the two candidates disagree.
func TestDocumentSchemaOverridesTheVersionParameter(t *testing.T) {
	// draft-07 asserts format; declaring it in the document must win over a
	// schemaVersion that would not assert.
	up := compile(t,
		`{"$schema":"http://json-schema.org/draft-07/schema#","type":"string","format":"email"}`,
		"V202012")
	if valid(t, up, `"not-an-email"`) {
		t.Error("an in-document draft-07 should assert format even under schemaVersion V202012")
	}

	// And the other way: 2020-12 in the document must win over a V7 parameter.
	down := compile(t,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","format":"email"}`,
		"V7")
	if !valid(t, down, `"not-an-email"`) {
		t.Error("an in-document 2020-12 should stop asserting format even under schemaVersion V7")
	}
}

func TestSchemaVersionSetIsExact(t *testing.T) {
	const schema = `{"type":"object"}`
	for _, version := range []string{"V4", "V6", "V7", "V201909", "V202012"} {
		compile(t, schema, version)
	}
	// Case-sensitive, no trimming, no aliases — WireMock refuses each of these
	// too, so this is parity rather than a deviation.
	for _, version := range []string{"v7", "V99", "BANANA", "draft-07", "V7 ", "V3"} {
		msg := refuse(t, schema, version)
		if !strings.Contains(msg, "schemaVersion must be one of") {
			t.Errorf("the refusal of %q should name the accepted set, got %q", version, msg)
		}
	}
}

// TestInvalidSchemasAreRefused pins CHK-JS decision 3. WireMock accepts every
// one of these and the stub then misbehaves silently — the first three match
// nothing ever, and the bare values match everything.
func TestInvalidSchemasAreRefused(t *testing.T) {
	cases := map[string]string{
		`{"type":"banana"}`:          "a type that names no type",
		`{"type":42}`:                "a non-string type",
		`{"$ref":"#/$defs/missing"}`: "a reference to a location that is not there",
		`42`:                         "a bare number, which would accept everything",
		`"hello"`:                    "a bare string",
		`null`:                       "a bare null",
		`[1,2]`:                      "a bare array",
		`{not json`:                  "not JSON at all",
		``:                           "nothing",
	}
	for schema, why := range cases {
		refuse(t, schema, "")
		_ = why
	}
}

// TestRefCyclesAreAnsweredRatherThanCrashing covers CHK-JS decision 4, and the
// outcome is better than that decision anticipated.
//
// A `$ref` cycle that consumes no instance is where WireMock falls over: it
// registers the stub and then answers HTTP 500 with a StackOverflowError on the
// first matching request. `{"$ref":"#"}` is enough to do it, and so are the
// mutual and `allOf` forms below — all four were reproduced against the oracle.
//
// The decision was to let compilation deal with these rather than special-case
// them, expecting a 422. In fact nothing needs refusing: this library detects the
// recursion and answers. A cycle with no escape is unsatisfiable, so nothing
// validates against it — which is the correct answer rather than an error — and a
// cycle with an escape branch resolves through it normally. So the difference
// from WireMock here is one where mockulus is simply more robust, and it costs
// nothing to state: no stub is refused that WireMock accepts.
func TestRefCyclesAreAnsweredRatherThanCrashing(t *testing.T) {
	unsatisfiable := []string{
		`{"$ref":"#"}`,
		`{"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}},"$ref":"#/$defs/a"}`,
		`{"$defs":{"a":{"allOf":[{"$ref":"#/$defs/a"}]}},"$ref":"#/$defs/a"}`,
	}
	for _, schema := range unsatisfiable {
		v := compile(t, schema, "")
		// The assertion that matters is that this returns at all. A cycle with
		// no base case can never be satisfied, so the answer is a non-match.
		if valid(t, v, `{"a":1}`) {
			t.Errorf("%s has no escape, so nothing should validate against it", schema)
		}
	}

	// A cycle with an escape branch has an ordinary meaning, and keeps it.
	escapable := compile(t,
		`{"$defs":{"a":{"anyOf":[{"$ref":"#/$defs/a"},{"type":"integer"}]}},"$ref":"#/$defs/a"}`, "")
	if !valid(t, escapable, `5`) {
		t.Error("the integer branch should satisfy the cycle")
	}
	if valid(t, escapable, `"not-an-integer"`) {
		t.Error("and the constraint should still hold for a value neither branch accepts")
	}
}

// TestLegitimateRecursionStillWorks is the other half of that decision: a
// recursive schema that consumes instance on each step is useful, works on
// WireMock, and must keep working here.
func TestLegitimateRecursionStillWorks(t *testing.T) {
	v := compile(t, `{
		"$defs": {"node": {"type":"object","properties":{
			"child": {"$ref":"#/$defs/node"},
			"v": {"type":"integer"}}}},
		"$ref": "#/$defs/node"}`, "")

	if !valid(t, v, `{"v":1,"child":{"v":2,"child":{"v":3}}}`) {
		t.Error("a recursive schema should validate a nested document")
	}
	if valid(t, v, `{"v":1,"child":{"v":"not-an-integer"}}`) {
		t.Error("recursion must still enforce the constraint at depth")
	}
}

// TestInDocumentReferencesResolve covers the reference forms WireMock does
// resolve, each with the negative probe that distinguishes resolution from the
// reference being ignored.
func TestInDocumentReferencesResolve(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"$defs", `{"$defs":{"pos":{"type":"integer","minimum":1}},
			"type":"object","properties":{"n":{"$ref":"#/$defs/pos"}}}`},
		{"definitions", `{"definitions":{"pos":{"type":"integer","minimum":1}},
			"type":"object","properties":{"n":{"$ref":"#/definitions/pos"}}}`},
		{"sibling pointer", `{"type":"object","properties":{
			"a":{"type":"integer","minimum":1},"n":{"$ref":"#/properties/a"}}}`},
		{"$anchor", `{"$defs":{"pos":{"$anchor":"posA","type":"integer","minimum":1}},
			"type":"object","properties":{"n":{"$ref":"#posA"}}}`},
	}
	for _, c := range cases {
		v := compile(t, c.schema, "")
		if !valid(t, v, `{"n":5}`) {
			t.Errorf("%s: a conforming value should validate", c.name)
		}
		// Without this the reference could simply be ignored and the first
		// assertion would still pass.
		if valid(t, v, `{"n":-5}`) {
			t.Errorf("%s: the reference is not being resolved — a violating value passed", c.name)
		}
	}
}

// TestReferencesOutsideTheDocumentAreRefused pins the JS2 decision.
//
// Note what this is and is not. WireMock does not fetch these — proven during
// probing — so nothing here closes a network hole. What it does is turn a silent
// never-match into a message naming the field.
func TestReferencesOutsideTheDocumentAreRefused(t *testing.T) {
	for _, schema := range []string{
		`{"$ref":"http://example.invalid/schema.json"}`,
		`{"$ref":"https://json-schema.org/nonexistent.json"}`,
		`{"$ref":"file:///etc/passwd"}`,
		`{"$ref":"other-schema.json"}`,
		`{"type":"object","properties":{"x":{"$ref":"http://example.invalid/s.json"}}}`,
	} {
		msg := refuse(t, schema, "")
		if !strings.Contains(msg, "$ref") && !strings.Contains(msg, "ref") {
			t.Errorf("the refusal of %s should name the reference, got %q", schema, msg)
		}
	}
}

// TestUnrecognisedSchemaURIIsRefused pins one of the CHK-JS decision-5
// refusals. On WireMock this registers and the stub then matches nothing at
// all — even a schema that would otherwise accept everything.
func TestUnrecognisedSchemaURIIsRefused(t *testing.T) {
	msg := refuse(t, `{"$schema":"http://example.com/nonsense","type":"object"}`, "")
	if !strings.Contains(msg, "$schema") {
		t.Errorf("the refusal should name $schema, got %q", msg)
	}
	refuse(t, `{"$schema":42,"type":"object"}`, "")

	// Every recognised spelling still works, with and without the trailing "#"
	// the older ones are conventionally written with.
	for _, uri := range []string{
		"http://json-schema.org/draft-04/schema#",
		"http://json-schema.org/draft-06/schema#",
		"http://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft/2019-09/schema",
		"https://json-schema.org/draft/2020-12/schema",
	} {
		compile(t, `{"$schema":"`+uri+`","type":"object"}`, "")
	}
}

// TestDraftKeywordsFollowTheDeclaredVersion is the coarse check that the draft
// selection reaches the compiler at all, using keywords that exist in one draft
// and not another.
func TestDraftKeywordsFollowTheDeclaredVersion(t *testing.T) {
	// `prefixItems` is 2020-12; under 2019-09 it is not a keyword, so the
	// constraint simply does not apply.
	const schema = `{"type":"array","prefixItems":[{"type":"integer"}]}`

	modern := compile(t, schema, "V202012")
	if modern.Valid(mustDoc(t, `["not-an-integer"]`)) {
		t.Error("V202012 should enforce prefixItems")
	}
	older := compile(t, schema, "V201909")
	if !older.Valid(mustDoc(t, `["not-an-integer"]`)) {
		t.Error("V201909 does not know prefixItems, so the constraint should not apply")
	}
}

func mustDoc(t *testing.T, body string) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("test body %s is not JSON: %v", body, err)
	}
	return doc
}

func TestSourceIsPreservedForDiagnostics(t *testing.T) {
	const schema = `{"type":"object"}`
	if got := compile(t, schema, "").Source(); got != schema {
		t.Errorf("Source() = %q, want the schema as registered", got)
	}
}

// TestListsCoverEveryDraft holds the two enumerated error messages to the maps
// they describe. Both are hand-ordered so a stub author reads the drafts in the
// order they were published, and a hand-ordered list is one a new draft can be
// added to a map without reaching.
func TestListsCoverEveryDraft(t *testing.T) {
	if len(versionOrder) != len(versions) {
		t.Fatalf("versionOrder lists %d spellings, versions has %d", len(versionOrder), len(versions))
	}
	for _, name := range versionOrder {
		if _, ok := versions[name]; !ok {
			t.Errorf("versionOrder names %q, which is not an accepted spelling", name)
		}
	}
	if len(schemaURIOrder) != len(schemaURIs) {
		t.Fatalf("schemaURIOrder lists %d URIs, schemaURIs has %d", len(schemaURIOrder), len(schemaURIs))
	}
	for _, uri := range schemaURIOrder {
		if _, ok := schemaURIs[uri]; !ok {
			t.Errorf("schemaURIOrder names %q, which is not an accepted $schema", uri)
		}
	}
	// The two lists describe the same five drafts, so they must agree
	// position by position or one message contradicts the other.
	for i, name := range versionOrder {
		if versions[name] != schemaURIs[schemaURIOrder[i]] {
			t.Errorf("position %d: %s and %s are different drafts", i, name, schemaURIOrder[i])
		}
	}
}
