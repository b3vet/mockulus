// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Compilation turns a matcher document into an evaluable matcher, or into the
// list of reasons it cannot be one. Nothing is deferred to serve time: a stub
// that registers successfully will match, and a stub that cannot is rejected
// with every problem named at once (P3).

// Problem is one reason a matcher document was rejected. The caller maps it
// onto the error catalog of SPEC Appendix B.
type Problem struct {
	// Pointer is the JSON pointer of the offending element, relative to the
	// document root the caller supplied.
	Pointer string
	// Detail explains the problem.
	Detail string
	// Deferred marks a feature that exists in WireMock and is on the roadmap,
	// as opposed to a document that is simply malformed.
	Deferred bool
	// Feature names the roadmap feature, when Deferred.
	Feature string
}

func (p Problem) Error() string { return p.Pointer + ": " + p.Detail }

// RegexCompiler builds a compiled pattern. It is supplied by the caller so
// this package does not depend on the regex engine seam, and so the timeout
// and anchoring policy stay a single decision made in one place.
type RegexCompiler func(pattern string) (PatternMatcher, error)

// Options carry what compilation needs from outside.
type Options struct {
	// CompileRegex builds patterns for `matches` and `doesNotMatch`.
	CompileRegex RegexCompiler
}

// deferredMatchers are WireMock matchers mockulus does not implement yet. They
// are named individually so the 422 tells a team exactly which roadmap item
// they are waiting on, rather than a generic refusal.
var deferredMatchers = map[string]string{
	"before":               "the before date-time matcher",
	"after":                "the after date-time matcher",
	"equalToDateTime":      "the equalToDateTime matcher",
	"matchesJsonSchema":    "matchesJsonSchema",
	"equalToXml":           "equalToXml (XML matching)",
	"matchesXPath":         "matchesXPath (XPath matching)",
	"hasExactly":           "the hasExactly multi-value operator",
	"includes":             "the includes multi-value operator",
	"matchesJsonPath":      "matchesJsonPath",
	"doesNotMatchJsonPath": "doesNotMatchJsonPath",
}

// modifierKeys are recognised alongside a matcher rather than being matchers
// themselves, so their presence must not look like an unknown field.
var modifierKeys = map[string]bool{
	"caseInsensitive":     true,
	"ignoreArrayOrder":    true,
	"ignoreExtraElements": true,
	"truncateExpectedTo":  true,
	"truncateActualTo":    true,
	"expectedOffset":      true,
	"expression":          true,
}

// Compile builds a matcher from one matcher document.
//
// A document may carry several matcher keys at once, which WireMock treats as
// a conjunction — {"contains": "a", "doesNotContain": "b"} means both.
func Compile(raw json.RawMessage, pointer string, opts Options) (Matcher, []Problem) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, []Problem{{Pointer: pointer, Detail: "matcher must be a JSON object: " + err.Error()}}
	}
	if len(doc) == 0 {
		return nil, []Problem{{Pointer: pointer, Detail: "matcher object is empty"}}
	}

	var (
		built    []Matcher
		problems []Problem
	)

	// Deterministic order so a document with several problems reports them the
	// same way every time.
	for _, key := range sortedKeys(doc) {
		value := doc[key]

		if modifierKeys[key] {
			continue
		}
		if feature, deferred := deferredMatchers[key]; deferred {
			problems = append(problems, Problem{
				Pointer:  pointer + "/" + key,
				Detail:   feature + " is not supported in mockulus v1",
				Deferred: true,
				Feature:  feature,
			})
			continue
		}

		m, probs := compileOne(key, value, doc, pointer, opts)
		problems = append(problems, probs...)
		if m != nil {
			built = append(built, m)
		}
	}

	if len(problems) > 0 {
		return nil, problems
	}
	switch len(built) {
	case 0:
		return nil, []Problem{{
			Pointer: pointer,
			Detail:  "no recognised matcher in " + strings.Join(sortedKeys(doc), ", "),
		}}
	case 1:
		return built[0], nil
	default:
		// Several matcher keys on one document mean all of them must hold.
		return &And{Matchers: built}, nil
	}
}

func compileOne(key string, value json.RawMessage, doc map[string]json.RawMessage,
	pointer string, opts Options) (Matcher, []Problem) {

	at := pointer + "/" + key
	fail := func(detail string) (Matcher, []Problem) {
		return nil, []Problem{{Pointer: at, Detail: detail}}
	}

	switch key {
	case "equalTo":
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return fail("equalTo takes a string")
		}
		return &EqualTo{Expected: s, CaseInsensitive: boolField(doc, "caseInsensitive")}, nil

	case "binaryEqualTo":
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return fail("binaryEqualTo takes a base64 string")
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return fail("binaryEqualTo operand is not valid base64: " + err.Error())
		}
		return &BinaryEqualTo{Expected: decoded, Source: s}, nil

	case "contains", "doesNotContain":
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return fail(key + " takes a string")
		}
		return &Contains{Expected: s, Negate: key == "doesNotContain"}, nil

	case "matches", "doesNotMatch":
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return fail(key + " takes a regular expression string")
		}
		if opts.CompileRegex == nil {
			return fail("no regex engine is configured")
		}
		p, err := opts.CompileRegex(s)
		if err != nil {
			return nil, []Problem{{Pointer: at, Detail: "regular expression does not compile: " + err.Error()}}
		}
		return &Regex{Pattern: p, Negate: key == "doesNotMatch"}, nil

	case "absent":
		var b bool
		if err := json.Unmarshal(value, &b); err != nil {
			return fail("absent takes a boolean")
		}
		if !b {
			// {"absent": false} is not a criterion WireMock gives meaning to;
			// accepting it silently would be accept-and-ignore.
			return fail(`"absent": false is not a matcher; omit the criterion instead`)
		}
		return &Absent{}, nil

	case "equalToJson":
		expected, source, err := decodeExpectedJSON(value)
		if err != nil {
			return fail("equalToJson operand is not valid JSON: " + err.Error())
		}
		return &EqualToJSON{
			Expected:            expected,
			Source:              source,
			IgnoreArrayOrder:    boolField(doc, "ignoreArrayOrder"),
			IgnoreExtraElements: boolField(doc, "ignoreExtraElements"),
		}, nil

	case "and", "or":
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return fail(key + " takes an array of matchers")
		}
		if len(items) == 0 {
			return fail(key + " needs at least one matcher")
		}
		children := make([]Matcher, 0, len(items))
		var problems []Problem
		for i, item := range items {
			child, probs := Compile(item, fmt.Sprintf("%s/%d", at, i), opts)
			problems = append(problems, probs...)
			if child != nil {
				children = append(children, child)
			}
		}
		if len(problems) > 0 {
			return nil, problems
		}
		if key == "and" {
			return &And{Matchers: children}, nil
		}
		return &Or{Matchers: children}, nil

	case "not":
		child, probs := Compile(value, at, opts)
		if len(probs) > 0 {
			return nil, probs
		}
		return &Not{Matcher: child}, nil

	default:
		return nil, []Problem{{
			Pointer: at,
			Detail:  "unknown matcher " + key,
		}}
	}
}

// decodeExpectedJSON accepts the expected document either as an escaped JSON
// string — the form WireMock's own examples use — or inline. Both appear in
// real stub corpora, so both are accepted.
func decodeExpectedJSON(value json.RawMessage) (parsed any, source string, err error) {
	var asString string
	if err := json.Unmarshal(value, &asString); err == nil {
		if uErr := json.Unmarshal([]byte(asString), &parsed); uErr != nil {
			return nil, asString, uErr
		}
		return parsed, asString, nil
	}
	if uErr := json.Unmarshal(value, &parsed); uErr != nil {
		return nil, string(value), uErr
	}
	return parsed, string(value), nil
}

func boolField(doc map[string]json.RawMessage, key string) bool {
	raw, ok := doc[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
