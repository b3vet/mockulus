// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"fmt"
	"strings"
)

// json-unit placeholders let an expected document say "any string here" rather
// than a literal value. WireMock interprets them inside equalToJson by default,
// with no opt-in flag — verified against the pinned version.
//
// They are resolved at compile time: the expected document is walked once and
// every placeholder string is replaced by a node that knows how to match. The
// comparison then costs the same as a literal one, and an unparseable
// placeholder is a registration error rather than a stub that quietly matches
// nothing.

// Placeholder prefixes recognised inside an expected document.
const (
	placeholderIgnore        = "${json-unit.ignore}"
	placeholderIgnoreElement = "${json-unit.ignore-element}"
	placeholderAnyString     = "${json-unit.any-string}"
	placeholderAnyNumber     = "${json-unit.any-number}"
	placeholderAnyBoolean    = "${json-unit.any-boolean}"
	placeholderRegexPrefix   = "${json-unit.regex}"
	// placeholderPrefix identifies any json-unit placeholder, including ones
	// this build does not implement.
	placeholderPrefix = "${json-unit."
)

// placeholderKind selects what a resolved placeholder accepts.
type placeholderKind uint8

const (
	// phAny accepts any value at all, of any type, but the member must be there.
	phAny placeholderKind = iota
	// phAnyOrAbsent additionally allows the member to be missing entirely.
	phAnyOrAbsent
	// phAnyString accepts any JSON string.
	phAnyString
	// phAnyNumber accepts any JSON number.
	phAnyNumber
	// phAnyBoolean accepts any JSON boolean.
	phAnyBoolean
	// phRegex accepts a string fully matching a pattern.
	phRegex
)

// jsonPlaceholder is a compiled placeholder standing in for an expected value.
type jsonPlaceholder struct {
	kind    placeholderKind
	pattern PatternMatcher
	// source is the placeholder as written, for diagnostics.
	source string
}

// matches reports whether an actual JSON value satisfies the placeholder.
func (p *jsonPlaceholder) matches(actual any) bool {
	switch p.kind {
	case phAny, phAnyOrAbsent:
		return true
	case phAnyString:
		_, ok := actual.(string)
		return ok
	case phAnyNumber:
		_, ok := actual.(float64)
		return ok
	case phAnyBoolean:
		_, ok := actual.(bool)
		return ok
	case phRegex:
		s, ok := actual.(string)
		return ok && p.pattern.MatchString(s)
	default:
		return false
	}
}

// optional reports whether the placeholder also stands in for a member that is
// not there at all.
func (p *jsonPlaceholder) optional() bool { return p.kind == phAnyOrAbsent }

// HasPlaceholder reports whether a value written into an expected document is a
// json-unit placeholder.
func hasPlaceholder(s string) bool { return strings.HasPrefix(s, placeholderPrefix) }

// resolvePlaceholders walks an expected document and replaces every placeholder
// string with a compiled placeholder node. It returns the rewritten document,
// whether anything was replaced, and any problem found.
//
// compileRegex may be nil, in which case a regex placeholder is a problem
// rather than being silently downgraded to a literal comparison.
func resolvePlaceholders(expected any, compileRegex RegexCompiler, pointer string) (any, bool, []Problem) {
	switch v := expected.(type) {
	case string:
		if !hasPlaceholder(v) {
			return v, false, nil
		}
		ph, kind, err := compilePlaceholder(v, compileRegex)
		if err != nil {
			return nil, false, []Problem{{Kind: kind, Pointer: pointer, Detail: err.Error()}}
		}
		return ph, true, nil

	case map[string]any:
		found := false
		var problems []Problem
		out := make(map[string]any, len(v))
		for key, child := range v {
			resolved, childFound, probs := resolvePlaceholders(child, compileRegex, pointer)
			problems = append(problems, probs...)
			found = found || childFound
			out[key] = resolved
		}
		return out, found, problems

	case []any:
		found := false
		var problems []Problem
		out := make([]any, len(v))
		for i, child := range v {
			resolved, childFound, probs := resolvePlaceholders(child, compileRegex, pointer)
			problems = append(problems, probs...)
			found = found || childFound
			out[i] = resolved
		}
		return out, found, problems

	default:
		return expected, false, nil
	}
}

// compilePlaceholder turns a placeholder string into a matcher node, and
// reports which catalog code its failure belongs to.
func compilePlaceholder(s string, compileRegex RegexCompiler) (*jsonPlaceholder, ProblemKind, error) {
	switch s {
	case placeholderIgnore:
		return &jsonPlaceholder{kind: phAny, source: s}, ProblemMalformed, nil
	case placeholderIgnoreElement:
		// Not a synonym for ignore, despite the name: probing shows `ignore`
		// requires the member to be present while `ignore-element` also accepts
		// its absence. Expected {"a": ignore} rejects {}; ignore-element accepts it.
		return &jsonPlaceholder{kind: phAnyOrAbsent, source: s}, ProblemMalformed, nil
	case placeholderAnyString:
		return &jsonPlaceholder{kind: phAnyString, source: s}, ProblemMalformed, nil
	case placeholderAnyNumber:
		return &jsonPlaceholder{kind: phAnyNumber, source: s}, ProblemMalformed, nil
	case placeholderAnyBoolean:
		return &jsonPlaceholder{kind: phAnyBoolean, source: s}, ProblemMalformed, nil
	}

	if pattern, found := strings.CutPrefix(s, placeholderRegexPrefix); found {
		if compileRegex == nil {
			return nil, ProblemMalformed,
				fmt.Errorf("no regex engine is configured for %s", placeholderRegexPrefix)
		}
		// json-unit applies the pattern as a full match, verified against the
		// pinned WireMock: [a-z]+ accepts "abc" and rejects "abc1".
		compiled, err := compileRegex(pattern)
		if err != nil {
			// A regex problem, not a document problem: the author needs to look
			// at the pattern, not at the placeholder syntax.
			return nil, ProblemRegex,
				fmt.Errorf("the pattern in %q does not compile: %w", s, err)
		}
		return &jsonPlaceholder{kind: phRegex, pattern: compiled, source: s}, ProblemMalformed, nil
	}

	// An unrecognised placeholder is refused rather than compared literally:
	// comparing it as text would mean the stub silently never matches, which is
	// the failure mode the whole fail-loud contract exists to prevent.
	return nil, ProblemMalformed, fmt.Errorf("unknown json-unit placeholder %q", s)
}
