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

// absenceStrict is the optional capability of a subject whose absence fails a
// negative matcher as well as a positive one.
type absenceStrict interface{ AbsenceFailsNegative() bool }

// repeatable is the optional capability of a subject that can be bound to more
// than one value, which a header, query parameter or form field can be and a
// body cannot. Declaring it separately from Values() lets the per-value rule be
// asked about without materializing a subject's text — see perValueScope.
type repeatable interface{ RepeatedValues() ([]string, bool) }

// rawJSON is the optional capability of a subject that already holds its
// document as bytes, so a matcher can read it without asking for the decoded
// tree. It is not Bytes(): a body's bytes are the ones it arrived in, while a
// key's are a copy of a string, and this seam exists to avoid exactly that copy.
type rawJSON interface{ RawJSON() []byte }

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
// An absent subject usually satisfies a negative matcher — there is no value
// there to match the pattern — but not for every field kind: an absent cookie
// satisfies neither form. Subjects that need the stricter rule say so.
func matchNegatedValue(s Subject, pred func(string) bool) bool {
	if !s.Present() {
		return !absenceFailsEverything(s)
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

// absenceFailsEverything reports a subject whose absence fails every criterion
// except a bare `absent`.
//
// WireMock settles this before it looks at the pattern at all: an absent cookie
// matches only when the criterion IS the absent pattern, so a combinator that
// merely wraps one fails even though its operand would have matched. Verified
// against the pinned version — an absent cookie against
// {"or":[{"absent":true},{"equalTo":"a"}]} is a non-match, while the same
// criterion on an absent header matches.
func absenceFailsEverything(s Subject) bool {
	strict, ok := s.(absenceStrict)
	return ok && strict.AbsenceFailsNegative()
}

// perValueScope reports the values a whole criterion must be applied to one at
// a time, and whether such a split is needed at all.
//
// WireMock evaluates a complete StringValuePattern — combinators included —
// once per value of a repeated key and takes the best result, so the any-of
// rule belongs at the top of the criterion rather than at each leaf. The
// difference is visible as soon as a combinator is involved: over a query
// parameter carrying "a" and "b", `not(matches "a")` holds because of "b",
// while `and(matches "a", matches "b")` holds for neither value and so does not
// hold at all. Applying any-of leaf by leaf gets both backwards, because it
// lets one value satisfy one branch and another value satisfy the next.
//
// A subject holding one value needs no split — the leaves' own any-of over a
// single value already is the whole rule — so bodies and ordinary single-valued
// keys stay on exactly the path they were on.
//
// The question is put through the optional capability rather than through
// Values() because Values() is not free for every subject: a body materializes
// its text there, which is a copy of the whole request. Only a key-bound
// subject can carry a second value, so only a key-bound subject is asked, and a
// body-level combinator pays nothing for a rule that cannot apply to it (P2).
func perValueScope(s Subject) ([]string, bool) {
	repeated, ok := s.(repeatable)
	if !ok {
		return nil, false
	}
	return repeated.RepeatedValues()
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
// json-unit placeholders such as ${json-unit.any-string} are resolved at
// compile time into nodes that match by rule rather than by value, which is
// what WireMock does by default.
type EqualToJSON struct {
	Expected any
	// Exact is the same expected document with every number carried as an exact
	// decimal, which is what confirms a match float64 precision accepted.
	Exact any
	// Numbers records whether that confirmation can change the answer at all, so
	// the documents it cannot change never pay for it.
	Numbers             numberWidth
	IgnoreArrayOrder    bool
	IgnoreExtraElements bool
	// HasPlaceholders records that the expected document carries json-unit
	// placeholders, which the near-miss description mentions.
	HasPlaceholders bool
	// Source is the operand as written, for diagnostics.
	Source string
}

// Match implements Matcher.
func (m *EqualToJSON) Match(s Subject) bool {
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

func (m *EqualToJSON) matchOne(s Subject) bool {
	if !s.Present() {
		return false
	}
	actual, ok := s.JSON()
	if !ok {
		return false
	}
	if !jsonEqual(m.Expected, actual, m.IgnoreArrayOrder, m.IgnoreExtraElements) {
		return false
	}
	// float64 cannot separate every pair of decimals, so a match it accepted is
	// confirmed against the bytes whenever the digits could have rounded together.
	return m.confirmNumbers(s)
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
	if m.HasPlaceholders {
		opts = append(opts, "json-unit placeholders")
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
	case *jsonPlaceholder:
		// A placeholder replaced this node at compile time, so the comparison
		// here is a rule rather than a value.
		return exp.matches(actual)

	case map[string]any:
		act, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		// Unexpected members are rejected by name rather than by counting, so
		// that an optional expected member can be missing without a length
		// mismatch letting an unexpected one through in its place.
		if !ignoreExtra {
			for k := range act {
				if _, expected := exp[k]; !expected {
					return false
				}
			}
		}
		for k, ev := range exp {
			av, present := act[k]
			if !present {
				// Only ${json-unit.ignore-element} stands in for a member that
				// is not there; ${json-unit.ignore} still requires one.
				if ph, isPlaceholder := ev.(*jsonPlaceholder); isPlaceholder && ph.optional() {
					continue
				}
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
		// ignoreExtraElements relaxes an array the way it relaxes an object —
		// elements the expected document never accounted for are ignored — and
		// positional comparison puts those elements at the tail: the actual
		// array has to *begin* with the expected one, so [1,2] accepts [1,2,3]
		// and still refuses [3,1,2]. It relaxes in that direction only; an
		// actual array shorter than the expected one is missing an expected
		// element, which is a mismatch either way.
		if len(act) < len(exp) || (!ignoreExtra && len(act) != len(exp)) {
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
//
// ignoreExtraElements relaxes the cardinality rather than the pairing: the
// actual array may carry elements no expected element claims, and they are
// simply left unpaired, which turns the equality into a subset test. The
// pairing itself is unchanged — one actual element still answers at most one
// expected element, so [1,1] needs two 1s however many extras arrive.
//
// The pairing has to be a real bipartite matching. A greedy first-fit scan is
// exact only while elements are distinguishable, and two features make them
// genuinely ambiguous: a json-unit placeholder matches whole classes of actual
// elements, and ignoreExtraElements lets an expected object match any actual
// object that merely contains it. Once an expected element can pair with more
// than one actual, greedy can consume the pairing a later element needed and
// then report a non-match for an array that does match. What that produced was
// an order-independence that depended on order — expected
// ["${json-unit.any-number}", 2] accepted [7,2] and rejected [2,7] — which is
// worse than not offering the option at all, because the stub passes in
// development and fails on whichever request happens to arrive reordered.
func arrayEqualUnordered(expected, actual []any, ignoreArrayOrder, ignoreExtra bool) bool {
	// Cardinality settles some arrays without any pairing work: an actual array
	// with fewer elements cannot answer every expected one, and without
	// ignoreExtraElements a longer one leaves an element nothing accounts for.
	if len(actual) < len(expected) || (!ignoreExtra && len(actual) != len(expected)) {
		return false
	}

	// Greedy stays as the fast path. When it succeeds it has exhibited a valid
	// pairing, which is the whole question; only its failures prove nothing, so
	// only they pay for the full matching. That keeps the unambiguous arrays —
	// which is most of them — at one pass and no adjacency to build.
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
			return arrayMatchable(expected, actual, ignoreArrayOrder, ignoreExtra)
		}
	}
	return true
}

// arrayMatchable reports whether every expected element can be paired with a
// distinct actual one, by augmenting paths (Kuhn's).
//
// The adjacency is materialised first because comparing two nested documents is
// far more expensive than the search over it, and the search revisits pairs.
// Under ignoreExtraElements the actual side is the request's array rather than
// the stub's, so the bound is worth stating: the work is linear in what the
// client sent and quadratic only in the elements the stub declared, which are
// few. A long array in a request costs one pass per expected element.
func arrayMatchable(expected, actual []any, ignoreArrayOrder, ignoreExtra bool) bool {
	candidates := make([][]int, len(expected))
	for i, ev := range expected {
		for j, av := range actual {
			if jsonEqual(ev, av, ignoreArrayOrder, ignoreExtra) {
				candidates[i] = append(candidates[i], j)
			}
		}
		if len(candidates[i]) == 0 {
			// An expected element with nothing to pair with settles it, and
			// settles it before the rest of the adjacency is built.
			return false
		}
	}

	pairedWith := make([]int, len(actual))
	for i := range pairedWith {
		pairedWith[i] = -1
	}
	visited := make([]bool, len(actual))
	for i := range expected {
		clear(visited)
		if !augmentPairing(i, candidates, pairedWith, visited) {
			return false
		}
	}
	return true
}

// augmentPairing looks for an augmenting path from one expected element,
// re-pairing the actual elements along it. visited stops the search from
// reconsidering an actual element within one path.
func augmentPairing(i int, candidates [][]int, pairedWith []int, visited []bool) bool {
	for _, j := range candidates[i] {
		if visited[j] {
			continue
		}
		visited[j] = true
		if pairedWith[j] == -1 || augmentPairing(pairedWith[j], candidates, pairedWith, visited) {
			pairedWith[j] = i
			return true
		}
	}
	return false
}

// JSONPathEvaluator is the compiled-path capability this package needs,
// declared at the point of use so the matchers do not depend on the engine.
type JSONPathEvaluator interface {
	// Match implements the bare form: does the expression select anything
	// non-empty?
	Match(doc any) bool
	// Select returns the selected values for the nested form, and whether the
	// expression selected anything at all.
	Select(doc any) ([]any, bool)
	// Source is the expression as written, for diagnostics.
	Source() string
}

// JSONPathScanner is the optional capability of an evaluator that can answer
// from a document it has not decoded.
//
// Decoding is the expensive half: a body criterion reads one leaf, and building
// the whole tree to reach it was the last allocation left on the request path
// (D-OPEN-14). An engine that cannot do this for a given expression says so per
// call rather than per evaluator, because whether it can depends on the shape of
// the path and not on the engine.
type JSONPathScanner interface {
	// MatchBytes answers the bare form. handled is false when the evaluator
	// will not take this path, and the caller must fall back to Match over the
	// decoded document.
	MatchBytes(raw []byte) (matched, handled bool)
	// SelectBytes answers the nested form with the single node such a path
	// selects, which is the value — and the Go type — Select would have yielded.
	SelectBytes(raw []byte) (node any, found, handled bool)
}

// MatchesJSONPath applies a JSONPath expression to the subject's JSON.
//
// Two forms, and they mean different things. The BARE form asks whether the
// expression selects anything non-empty. The NESTED form applies an inner
// matcher to what was selected, and is ANY-OF: the criterion holds when at
// least one selected value satisfies it.
type MatchesJSONPath struct {
	Path JSONPathEvaluator
	// Inner is nil for the bare form.
	Inner Matcher
	// Negate turns this into doesNotMatchJsonPath.
	Negate bool
}

// Match implements Matcher.
func (m *MatchesJSONPath) Match(s Subject) bool {
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

func (m *MatchesJSONPath) matchOne(s Subject) bool {
	matched := m.evaluate(s)
	if m.Negate {
		return !matched
	}
	return matched
}

func (m *MatchesJSONPath) evaluate(s Subject) bool {
	if !s.Present() {
		return false
	}

	// A subject that holds its document as bytes, met by an engine that can walk
	// bytes, skips the decode entirely — which for a body is the whole cost of
	// the criterion. Whatever either side declines falls through to the decoded
	// document below, and the two answer alike; the jsonpath package holds them
	// to that with a differential suite and a fuzz target.
	if held, ok := s.(rawJSON); ok {
		if scanner, ok := m.Path.(JSONPathScanner); ok {
			raw := held.RawJSON()
			if m.Inner == nil {
				if matched, handled := scanner.MatchBytes(raw); handled {
					return matched
				}
			} else if node, found, handled := scanner.SelectBytes(raw); handled {
				return found && m.innerMatches(node)
			}
		}
	}

	doc, ok := s.JSON()
	if !ok {
		// A body that is not JSON is a plain non-match, never an error.
		return false
	}

	if m.Inner == nil {
		return m.Path.Match(doc)
	}

	values, found := m.Path.Select(doc)
	if !found {
		return false
	}
	for _, v := range values {
		if m.innerMatches(v) {
			return true
		}
	}
	return false
}

// innerMatches applies the nested form's inner matcher to one selected value.
func (m *MatchesJSONPath) innerMatches(v any) bool {
	// A directly-selected null never satisfies an inner matcher, whatever the
	// matcher says — absence is absence.
	if v == nil {
		return false
	}
	return m.Inner.Match(NewKeyValues(renderSelected(v)))
}

// renderSelected converts a selected value to the text an inner matcher sees.
// Strings pass through raw; everything else renders as JSON.
func renderSelected(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// Describe implements Matcher.
func (m *MatchesJSONPath) Describe() string {
	name := "matchesJsonPath"
	if m.Negate {
		name = "doesNotMatchJsonPath"
	}
	if m.Inner == nil {
		return name + " " + quote(m.Path.Source())
	}
	return name + " " + quote(m.Path.Source()) + " -> " + m.Inner.Describe()
}

// The three combinators share a shape: each is a whole criterion in its own
// right, so each carries the multi-value any-of rule (perValueScope) and the
// strict-absence rule instead of leaving them to its operands. Pushing either
// down to the leaves changes the answer — see the comments on those two
// helpers for what it changes it to.
//
// The same split belongs to any criterion that reads the subject as one
// document rather than value by value, which is why EqualToJSON and
// MatchesJSONPath carry it too: a subject's JSON() is the first value's
// document, so without the split those two would answer for one value of a
// repeated key and ignore the rest — and `or` around a single matcher would
// change the answer, which is a difference no reader would predict.
//
// Splitting a repeated key costs one allocation for the view, and only for the
// stubs that meet a repeated key with one of these criteria; everything else
// takes the direct path and allocates nothing (P2).

// And requires every child matcher to be satisfied.
type And struct{ Matchers []Matcher }

// Match implements Matcher.
func (m *And) Match(s Subject) bool {
	if !s.Present() {
		return !absenceFailsEverything(s) && m.matchOne(s)
	}
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

func (m *And) matchOne(s Subject) bool {
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
	if !s.Present() {
		return !absenceFailsEverything(s) && m.matchOne(s)
	}
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

func (m *Or) matchOne(s Subject) bool {
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
func (m *Not) Match(s Subject) bool {
	if !s.Present() {
		return !absenceFailsEverything(s) && !m.Matcher.Match(s)
	}
	if values, split := perValueScope(s); split {
		var view singleValue
		for _, v := range values {
			view.set(v)
			if !m.Matcher.Match(&view) {
				return true
			}
		}
		return false
	}
	return !m.Matcher.Match(s)
}

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
	const maxBytes = 120
	if len(s) > maxBytes {
		s = s[:maxBytes] + "…"
	}
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}
