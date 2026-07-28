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

// ProblemKind classifies a compilation problem, so the caller can map it onto
// the right catalog code without matching on error text.
type ProblemKind uint8

const (
	// ProblemMalformed is a document that does not say a valid thing.
	ProblemMalformed ProblemKind = iota
	// ProblemDeferred is a feature that exists in WireMock and is on the roadmap.
	ProblemDeferred
	// ProblemRegex is a regular expression that compiles on neither engine.
	ProblemRegex
	// ProblemJSONPath is a JSONPath expression that does not parse.
	ProblemJSONPath
)

// Problem is one reason a matcher document was rejected. The caller maps it
// onto the error catalog of SPEC Appendix B.
type Problem struct {
	// Kind classifies the problem for the error catalog.
	Kind ProblemKind
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
	// CompileJSONPath builds path evaluators for `matchesJsonPath`.
	CompileJSONPath JSONPathCompiler
	// AllowContentPatterns admits the byte-oriented matchers, which are valid
	// in some positions and not others — see contentPatterns.
	AllowContentPatterns bool

	// depth counts how many combinators this document sits inside; see
	// maxNesting.
	depth int
}

// maxNesting bounds how deeply combinators may be nested inside one another.
//
// The bound exists because every level re-reads the whole document below it:
// a nested operand arrives as a json.RawMessage, and decoding one costs a
// validating scan plus a copy of everything it contains. That makes compiling a
// chain of combinators quadratic in the document, which one admin POST turns
// into a denial of service against every team sharing the deployment. Measured
// on the pinned build before this bound existed: a 32 MiB body — inside the
// admin cap — nested a thousand deep spent 73 s of CPU and allocated 16.9 GB,
// and WireMock's own parser accepts nesting to a thousand, so matching its
// limit would not have bounded anything. Twenty is five times deeper than the
// deepest combinator anyone writes and leaves the worst case around a second.
//
// Refusing is safe in the way P3 asks for: the stub never registers, so nothing
// silently matches differently. Only the expected documents of equalToJson and
// the response bodies nest freely — those are parsed once, not per level.
const maxNesting = 20

// nested returns the options a criterion's operands compile under.
//
// The byte-oriented matchers do not survive nesting: WireMock's combinators and
// the nested form of matchesJsonPath are declared over StringValuePattern, so a
// binaryEqualTo inside one is refused even in a position where a bare
// binaryEqualTo would have been accepted. Verified against the pinned version —
// {"bodyPatterns":[{"not":{"binaryEqualTo":"…"}}]} is a 422 there.
func (o Options) nested() Options {
	o.AllowContentPatterns = false
	o.depth++
	return o
}

// contentPatterns are the matchers WireMock declares over byte[] rather than
// over a string. They compare the subject's raw bytes, so they are only
// meaningful — and only accepted — where the subject is a body.
var contentPatterns = map[string]bool{
	"binaryEqualTo": true,
}

// JSONPathCompiler builds a compiled path evaluator.
type JSONPathCompiler func(expr string) (JSONPathEvaluator, error)

// deferredMatchers are WireMock matchers mockulus does not implement yet. They
// are named individually so the 422 tells a team exactly which roadmap item
// they are waiting on, rather than a generic refusal.
var deferredMatchers = map[string]string{
	"before":            "the before date-time matcher",
	"after":             "the after date-time matcher",
	"equalToDateTime":   "the equalToDateTime matcher",
	"matchesJsonSchema": "matchesJsonSchema",
	"equalToXml":        "equalToXml (XML matching)",
	"matchesXPath":      "matchesXPath (XPath matching)",
	"hasExactly":        "the hasExactly multi-value operator",
	"includes":          "the includes multi-value operator",
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
}

// Compile builds a matcher from one matcher document.
//
// A document may carry several matcher keys at once, and mockulus treats them
// as a conjunction: {"contains": "a", "doesNotContain": "b"} means both.
//
// WireMock does not. Probing the pinned version showed it honours only the
// first key its binding happens to visit and discards the rest, so the same
// document means less there — silently, and in a way that makes a stub match
// requests its author wrote a criterion to exclude. Conjunction is the reading
// a person writing two criteria intends, and dropping one is not a failure mode
// worth reproducing, so this is a deliberate divergence (SPEC §5.5).
func Compile(raw json.RawMessage, pointer string, opts Options) (Matcher, []Problem) {
	// Checked before the decode rather than after it, so the level that breaks
	// the bound is also the last one whose cost is paid.
	if opts.depth > maxNesting {
		return nil, []Problem{{Pointer: pointer, Detail: fmt.Sprintf(
			"matchers may not nest more than %d combinators deep", maxNesting)}}
	}

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
				Kind:     ProblemDeferred,
				Pointer:  pointer + "/" + key,
				Detail:   feature + " is not supported in mockulus v1",
				Deferred: true,
				Feature:  feature,
			})
			continue
		}
		if contentPatterns[key] && !opts.AllowContentPatterns {
			// The pointer names the whole criterion rather than the offending
			// key, which is where WireMock puts it: the criterion as written is
			// not a match operation it has, so there is nothing narrower to
			// blame. A stub that registers here but is a 422 there is a hole a
			// team only discovers on the way back.
			problems = append(problems, Problem{
				Pointer: pointer,
				Detail:  key + " compares raw bytes, so it is only valid in bodyPatterns",
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

	// A JSON null is not an operand, and it is refused here rather than in each
	// key's decode below because that decode cannot see it: json.Unmarshal reads
	// null into a Go string as "" and reports no error, so {"contains": null}
	// would compile into a criterion that holds for every body and the stub
	// would then answer the requests its author wrote a criterion to exclude.
	// Silently meaning something nobody wrote is the accept-and-behave-
	// differently failure P3 exists to forbid, and WireMock refuses all six of
	// the string-operand matchers 422 ("contains operand must be a non-null
	// string"), so accepting one is also a stub that cannot move between the two
	// servers.
	//
	// One guard over every key, rather than a check per key, because the shape
	// that has to hold is "no matcher takes null" — six per-key checks would
	// leave the seventh matcher someone adds later carrying the same hole.
	// Nothing above it loses meaning: the modifier keys and the deferred
	// matchers are answered in Compile before this runs, so {"caseInsensitive":
	// null} still reads as the absent modifier it does on WireMock.
	if string(value) == "null" {
		return fail(key + " operand must not be null")
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
		decoded, err := DecodeBase64(s)
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
			return nil, []Problem{{Kind: ProblemRegex, Pointer: at,
				Detail: "regular expression does not compile: " + err.Error()}}
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
		// Placeholders are lowered into matcher nodes now, so the comparison
		// itself stays a plain structural walk.
		resolved, hasPlaceholders, problems := resolvePlaceholders(expected, opts.CompileRegex, at)
		if len(problems) > 0 {
			return nil, problems
		}
		return &EqualToJSON{
			Expected:            resolved,
			Source:              source,
			HasPlaceholders:     hasPlaceholders,
			IgnoreArrayOrder:    boolField(doc, "ignoreArrayOrder"),
			IgnoreExtraElements: boolField(doc, "ignoreExtraElements"),
		}, nil

	case "matchesJsonPath", "doesNotMatchJsonPath":
		if opts.CompileJSONPath == nil {
			return fail("no JSONPath engine is configured")
		}
		negate := key == "doesNotMatchJsonPath"

		// The bare form is a string; the nested form is an object carrying the
		// expression plus an inner matcher.
		var expr string
		if err := json.Unmarshal(value, &expr); err == nil {
			path, err := opts.CompileJSONPath(expr)
			if err != nil {
				return nil, []Problem{{Kind: ProblemJSONPath, Pointer: at,
					Detail: "JSONPath does not compile: " + err.Error()}}
			}
			return &MatchesJSONPath{Path: path, Negate: negate}, nil
		}

		var nested map[string]json.RawMessage
		if err := json.Unmarshal(value, &nested); err != nil {
			return fail(key + " takes an expression string or an object with an expression")
		}
		rawExpr, hasExpr := nested["expression"]
		if !hasExpr {
			return fail(key + " object form needs an expression")
		}
		if err := json.Unmarshal(rawExpr, &expr); err != nil {
			return fail("expression must be a string")
		}
		path, err := opts.CompileJSONPath(expr)
		if err != nil {
			return nil, []Problem{{Kind: ProblemJSONPath, Pointer: at,
				Detail: "JSONPath does not compile: " + err.Error()}}
		}

		// Whatever else the object carries is the inner matcher.
		inner := make(map[string]json.RawMessage, len(nested))
		for k, v := range nested {
			if k == "expression" {
				continue
			}
			inner[k] = v
		}
		if len(inner) == 0 {
			// An object form with no inner matcher is the bare form written the
			// long way.
			return &MatchesJSONPath{Path: path, Negate: negate}, nil
		}
		encoded, err := json.Marshal(inner)
		if err != nil {
			return fail("could not read the nested matcher")
		}
		innerMatcher, problems := Compile(encoded, at, opts.nested())
		if len(problems) > 0 {
			return nil, problems
		}
		return &MatchesJSONPath{Path: path, Inner: innerMatcher, Negate: negate}, nil

	case "and", "or":
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return fail(key + " takes an array of matchers")
		}
		// WireMock requires two, and answers 422 for a one-operand form. A
		// combinator over a single matcher is that matcher, so accepting it
		// costs nothing at match time — but a mappings file that registers here
		// and is refused there is one that cannot move back, which is the
		// direction of D2 that matters.
		if len(items) < 2 {
			return fail(key + " needs at least two matchers")
		}
		children := make([]Matcher, 0, len(items))
		var problems []Problem
		for i, item := range items {
			child, probs := Compile(item, fmt.Sprintf("%s/%d", at, i), opts.nested())
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
		child, probs := Compile(value, at, opts.nested())
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

// DecodeBase64 reads an operand the way Java's Base64.getDecoder() does, which
// is not what any single encoding/base64 encoding offers.
//
// Padding is optional there and mandatory in StdEncoding, so `aGVsbG8` — the
// spelling anything calling Java's own encodeToString on a raw encoder produces,
// and the one that turns up in hand-written mappings — is five bytes on WireMock
// and a 422 here. Trying RawStdEncoding when the padded reading fails buys
// exactly that spelling and nothing more: RawStdEncoding reads the same 64
// characters, so the url-safe alphabet's '-' and '_' stay refused, a space or a
// tab inside the operand stays refused, and a trailing unit too short to hold a
// byte stays refused — all three of which WireMock refuses too, so the two
// acceptance sets line up rather than merely overlapping.
//
// One character class is outside what either encoding can express: encoding/base64
// skips '\r' and '\n' wherever they appear, so an operand carrying them is read
// here and refused there. That is the decoder's rule rather than this function's,
// it is the same under StdEncoding alone, and narrowing it would be a separate
// decision about a separate spelling.
//
// The padded decoder's error is the one reported, because an operand that is not
// base64 at all fails both readings and the first one names the character that
// is wrong, which is what the author needs to see.
//
// It lives here, next to the matcher that decodes an operand, and is exported so
// the response body's base64 reads through the same function: two spellings of
// "what counts as base64" would drift, and the pair would then disagree about
// which stubs register while looking identical in review.
func DecodeBase64(s string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, nil
	}
	if unpadded, rawErr := base64.RawStdEncoding.DecodeString(s); rawErr == nil {
		return unpadded, nil
	}
	return nil, err
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
