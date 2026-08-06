// SPDX-License-Identifier: Apache-2.0

package matchers

// SchemaValidator is the compiled-schema capability this package needs, defined
// here at the point of use so the matchers do not depend on the schema library
// — the same arrangement PatternMatcher and JSONPathEvaluator have.
type SchemaValidator interface {
	// Valid reports whether a parsed JSON document satisfies the schema.
	Valid(doc any) bool
	// Source is the schema as registered, for diagnostics.
	Source() string
}

// SchemaCompiler builds a validator from a schema document and the draft the
// stub named, or reports why it cannot. Supplied by the caller so the compile
// policies — which draft, whether `format` asserts, what a `$ref` may reach —
// stay one decision made in one place (internal/jsonschemax).
type SchemaCompiler func(schema, version string) (SchemaValidator, error)

// MatchesJSONSchema validates a value against an embedded JSON Schema.
//
// The subject is parsed as JSON and the parsed document is validated. A value
// that is not JSON is a plain non-match, which is the answer every other matcher
// here gives input it cannot read (SPEC §6.7) — and is a deliberate divergence.
//
// WireMock does something stranger. It falls back to validating the *raw request
// text as a JSON string*, so `not json at all` satisfies `{"type":"string"}`; and
// for a number, boolean or null body it tries both readings and matches if
// either succeeds, which makes a schema and its own negation both hold for the
// body `4`. That is not a behaviour a stub author can reason about, so it is not
// reproduced (deviation, §5.5). Object and array bodies — what schemas are
// actually written for — agree exactly.
type MatchesJSONSchema struct {
	Schema SchemaValidator
}

// Match implements Matcher, following the any-of rule over a repeated key.
func (m *MatchesJSONSchema) Match(s Subject) bool {
	if values, split := perValueScope(s); split {
		var view singleValue
		for _, v := range values {
			view.set(v)
			if m.matchOne(&view) {
				return true
			}
		}
		return false
	}
	return m.matchOne(s)
}

func (m *MatchesJSONSchema) matchOne(s Subject) bool {
	doc, ok := s.JSON()
	if !ok {
		// Not JSON, or JSON with something after it: `encoding/json` refuses
		// trailing content and so does this, which is the strictness deviation
		// #35 already records for equalToJson and matchesJsonPath. WireMock
		// reads the first value and ignores the rest.
		return false
	}
	return m.Schema.Valid(doc)
}

// Describe implements Matcher.
func (m *MatchesJSONSchema) Describe() string {
	return "matchesJsonSchema " + quote(truncateSchema(m.Schema.Source()))
}

// truncateSchema keeps a near-miss line readable. A schema is often longer than
// everything else on the line put together, and the first fragment is enough to
// tell two criteria apart.
func truncateSchema(s string) string {
	const limit = 80
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
