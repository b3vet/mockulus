// SPDX-License-Identifier: Apache-2.0

// Package matchers implements the content matchers of SPEC §5.2 as pure
// functions over a subject.
//
// The same matcher set is reused everywhere a value is compared: request
// bodies, header, query, cookie, form and path-parameter values, verification
// criteria, and metadata search. Keeping them subject-agnostic is what makes
// that reuse possible, and means a matcher only has to be got right once.
//
// Everything expensive — regex compilation, expected-JSON parsing — happens
// when the stub is registered. Matching itself allocates nothing beyond what
// the comparison inherently needs.
package matchers

import (
	"encoding/json"
	"strings"
)

// Subject is what a matcher is applied to: a body, or the value or values
// bound to one key.
//
// Accessors are expected to be cheap and memoized by the implementation, so a
// matcher may call them freely — a stub matched on URL alone never causes its
// request's body to be parsed (SPEC §6.4, P2).
type Subject interface {
	// Present reports whether the subject exists at all. A missing header and a
	// header with an empty value are different things.
	Present() bool
	// Values returns the textual values bound to the subject. A body has one;
	// a repeated header has several.
	Values() []string
	// Bytes returns the subject's raw bytes, for byte-exact comparison.
	Bytes() []byte
	// JSON returns the subject parsed as JSON, and whether it parsed.
	JSON() (any, bool)
}

// Matcher is one criterion.
type Matcher interface {
	// Match reports whether the subject satisfies the criterion.
	Match(s Subject) bool
	// Describe renders the criterion for near-miss diagnostics.
	Describe() string
}

// multiValueAnyMatches records how a matcher is applied to a subject holding
// several values, which is the case for a repeated header or query parameter.
// WireMock's rule is any-of: the criterion is satisfied when at least one value
// satisfies it (SPEC §5.2). It is named rather than inlined because it is a
// compatibility decision, not an implementation detail.
const multiValueAnyMatches = true

// matchAnyValue applies a single-value predicate under the multi-value rule.
func matchAnyValue(s Subject, pred func(string) bool) bool {
	if !s.Present() {
		return false
	}
	values := s.Values()
	if len(values) == 0 {
		return false
	}
	if !multiValueAnyMatches {
		for _, v := range values {
			if !pred(v) {
				return false
			}
		}
		return true
	}
	for _, v := range values {
		if pred(v) {
			return true
		}
	}
	return false
}

// matchNegatedValue applies the negative form of a predicate under the
// multi-value rule.
//
// The negative matchers are NOT the logical complement of their positive twins
// over a repeated key: both use any-of, so `doesNotMatch` is satisfied when at
// least one value fails the pattern, not when every value fails it. A header
// carrying both "a" and "b" therefore satisfies matches("a") AND
// doesNotMatch("a") at once. Verified against the pinned WireMock; the shape is
// surprising enough that inverting the positive result — the obvious
// implementation — is wrong.
//
// An absent subject satisfies a negative matcher: there is no value there to
// match the pattern.
func matchNegatedValue(s Subject, pred func(string) bool) bool {
	if !s.Present() {
		return true
	}
	values := s.Values()
	if len(values) == 0 {
		return true
	}
	for _, v := range values {
		if !pred(v) {
			return true
		}
	}
	return false
}

// EqualTo compares the subject to an exact string.
type EqualTo struct {
	Expected        string
	CaseInsensitive bool
}

// Match implements Matcher.
func (m *EqualTo) Match(s Subject) bool {
	return matchAnyValue(s, func(v string) bool {
		if m.CaseInsensitive {
			return strings.EqualFold(v, m.Expected)
		}
		return v == m.Expected
	})
}

// Describe implements Matcher.
func (m *EqualTo) Describe() string {
	if m.CaseInsensitive {
		return "equalTo (case-insensitive) " + quote(m.Expected)
	}
	return "equalTo " + quote(m.Expected)
}

// BinaryEqualTo compares the subject's raw bytes to a base64-decoded operand,
// which is how a binary body is matched without going through a string.
type BinaryEqualTo struct {
	Expected []byte
	// Source is the operand as written, for diagnostics.
	Source string
}

// Match implements Matcher.
func (m *BinaryEqualTo) Match(s Subject) bool {
	if !s.Present() {
		return false
	}
	got := s.Bytes()
	if len(got) != len(m.Expected) {
		return false
	}
	for i := range got {
		if got[i] != m.Expected[i] {
			return false
		}
	}
	return true
}

// Describe implements Matcher.
func (m *BinaryEqualTo) Describe() string { return "binaryEqualTo " + quote(m.Source) }

// Contains requires the expected text to appear somewhere in the subject.
type Contains struct {
	Expected string
	// Negate turns this into doesNotContain.
	Negate bool
}

// Match implements Matcher.
func (m *Contains) Match(s Subject) bool {
	if m.Negate {
		return matchNegatedValue(s, func(v string) bool { return strings.Contains(v, m.Expected) })
	}
	return matchAnyValue(s, func(v string) bool { return strings.Contains(v, m.Expected) })
}

// Describe implements Matcher.
func (m *Contains) Describe() string {
	if m.Negate {
		return "doesNotContain " + quote(m.Expected)
	}
	return "contains " + quote(m.Expected)
}

// Regex matches the subject against a compiled pattern.
type Regex struct {
	Pattern PatternMatcher
	// Negate turns this into doesNotMatch.
	Negate bool
}

// PatternMatcher is the compiled-regex capability this package needs, defined
// here at the point of use so the matchers do not depend on the engine seam.
type PatternMatcher interface {
	MatchString(string) bool
	// Source is the pattern as written, for diagnostics.
	Source() string
}

// Match implements Matcher.
func (m *Regex) Match(s Subject) bool {
	if m.Negate {
		return matchNegatedValue(s, m.Pattern.MatchString)
	}
	return matchAnyValue(s, m.Pattern.MatchString)
}

// Describe implements Matcher.
func (m *Regex) Describe() string {
	if m.Negate {
		return "doesNotMatch " + quote(m.Pattern.Source())
	}
	return "matches " + quote(m.Pattern.Source())
}

// Absent matches only when the subject does not exist. It is the one matcher
// whose whole purpose is the negative case, which is why presence is part of
// the Subject contract rather than being inferred from an empty value.
type Absent struct{}

// Match implements Matcher.
func (m *Absent) Match(s Subject) bool { return !s.Present() }

// Describe implements Matcher.
func (m *Absent) Describe() string { return "absent" }

// EqualToJSON compares the subject structurally against an expected document,
// so key order and whitespace do not matter.
//
// json-unit placeholders such as ${json-unit.any-string} are compared
// literally rather than interpreted; that is documented deviation #5, and the
// roadmap item that removes it lowers the expected document into a matcher
// tree instead.
type EqualToJSON struct {
	Expected            any
	IgnoreArrayOrder    bool
	IgnoreExtraElements bool
	// Source is the operand as written, for diagnostics.
	Source string
}

// Match implements Matcher.
func (m *EqualToJSON) Match(s Subject) bool {
	if !s.Present() {
		return false
	}
	actual, ok := s.JSON()
	if !ok {
		return false
	}
	return jsonEqual(m.Expected, actual, m.IgnoreArrayOrder, m.IgnoreExtraElements)
}

// Describe implements Matcher.
func (m *EqualToJSON) Describe() string {
	var opts []string
	if m.IgnoreArrayOrder {
		opts = append(opts, "ignoreArrayOrder")
	}
	if m.IgnoreExtraElements {
		opts = append(opts, "ignoreExtraElements")
	}
	d := "equalToJson " + quote(m.Source)
	if len(opts) > 0 {
		d += " (" + strings.Join(opts, ", ") + ")"
	}
	return d
}

// jsonEqual is structural JSON comparison under the two relaxations WireMock
// offers.
func jsonEqual(expected, actual any, ignoreArrayOrder, ignoreExtra bool) bool {
	switch exp := expected.(type) {
	case map[string]any:
		act, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		if !ignoreExtra && len(act) != len(exp) {
			return false
		}
		for k, ev := range exp {
			av, present := act[k]
			if !present {
				return false
			}
			if !jsonEqual(ev, av, ignoreArrayOrder, ignoreExtra) {
				return false
			}
		}
		return true

	case []any:
		act, ok := actual.([]any)
		if !ok {
			return false
		}
		if ignoreArrayOrder {
			return arrayEqualUnordered(exp, act, ignoreArrayOrder, ignoreExtra)
		}
		// Without ignoreArrayOrder, arrays compare element by element.
		// ignoreExtraElements relaxes objects, not array length: an array with
		// extra elements is a different array.
		if len(exp) != len(act) {
			return false
		}
		for i := range exp {
			if !jsonEqual(exp[i], act[i], ignoreArrayOrder, ignoreExtra) {
				return false
			}
		}
		return true

	case nil:
		return actual == nil

	case bool:
		act, ok := actual.(bool)
		return ok && act == exp

	case float64:
		act, ok := actual.(float64)
		return ok && act == exp

	case string:
		act, ok := actual.(string)
		return ok && act == exp

	default:
		return false
	}
}

// arrayEqualUnordered pairs each expected element with a distinct actual one.
// A greedy scan is used rather than a full bipartite matching: the arrays in a
// stub are small, and greedy is exact whenever elements are distinguishable,
// which they are in every realistic stub.
func arrayEqualUnordered(expected, actual []any, ignoreArrayOrder, ignoreExtra bool) bool {
	if len(expected) != len(actual) {
		return false
	}
	used := make([]bool, len(actual))
	for _, ev := range expected {
		matched := false
		for i, av := range actual {
			if used[i] {
				continue
			}
			if jsonEqual(ev, av, ignoreArrayOrder, ignoreExtra) {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// And requires every child matcher to be satisfied.
type And struct{ Matchers []Matcher }

// Match implements Matcher.
func (m *And) Match(s Subject) bool {
	for _, child := range m.Matchers {
		if !child.Match(s) {
			return false
		}
	}
	return true
}

// Describe implements Matcher.
func (m *And) Describe() string { return "and(" + describeAll(m.Matchers) + ")" }

// Or requires at least one child matcher to be satisfied.
type Or struct{ Matchers []Matcher }

// Match implements Matcher.
func (m *Or) Match(s Subject) bool {
	for _, child := range m.Matchers {
		if child.Match(s) {
			return true
		}
	}
	return false
}

// Describe implements Matcher.
func (m *Or) Describe() string { return "or(" + describeAll(m.Matchers) + ")" }

// Not inverts a matcher.
type Not struct{ Matcher Matcher }

// Match implements Matcher.
func (m *Not) Match(s Subject) bool { return !m.Matcher.Match(s) }

// Describe implements Matcher.
func (m *Not) Describe() string { return "not(" + m.Matcher.Describe() + ")" }

func describeAll(ms []Matcher) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, m.Describe())
	}
	return strings.Join(parts, ", ")
}

func quote(s string) string {
	const max = 120
	if len(s) > max {
		s = s[:max] + "…"
	}
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}
