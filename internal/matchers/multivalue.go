// SPDX-License-Identifier: Apache-2.0

package matchers

import "strings"

// MultiValue is `hasExactly` or `includes`: a criterion over the whole set of a
// key's values rather than over one of them.
//
// Every other matcher here answers a question about *a* value and reaches a
// repeated key through the any-of rule of §5.3 (see matchAnyValue, and deviation
// #29 for how that differs from WireMock's edit-distance selection). These two
// quantify over the list itself, so they sit beside that rule rather than
// inside it: there is no value to select, and nothing here ranks or scores.
//
// # The rule, which is not the obvious one
//
//	includes(M)    holds when every m in M is satisfied by some value.
//	hasExactly(M)  holds when includes(M) holds and len(values) == len(M).
//
// Values are **not consumed**. A value satisfying no matcher at all is
// tolerated, provided the count comes out right — so
//
//	hasExactly [ {matches "a.*"}, {matches "a.*"} ]   against   ?tag=a1&tag=b1
//
// matches, even though "b1" satisfies neither operand: the list is two long,
// and both operands found "a1". The intuitive implementation is a bijection —
// pair each operand with a distinct value — and it is wrong. This was probed
// against pinned WireMock 3.13.2 rather than reasoned about, because a bijection
// passes every test anybody would think to write, and the two rules differ only
// on inputs nobody writes by accident.
//
// Reproducing it is deliberate (CHK-MV, 2026-08-12). It is strictly more
// permissive than a bijection, so no suite that passes on WireMock can fail
// here for this reason.
type MultiValue struct {
	// Operands are the criteria, each compiled as an ordinary single-value
	// matcher. WireMock accepts any StringValuePattern here, not just equalTo.
	Operands []Matcher
	// Exact selects hasExactly over includes: the size equality above.
	Exact bool
}

// Match implements Matcher.
//
// An absent key satisfies neither operator, whatever the operands say — there
// is no list to quantify over. That includes the vacuous `includes: []`, which
// holds for any *present* key and so reads as a presence assertion.
func (m *MultiValue) Match(s Subject) bool {
	if !s.Present() {
		return false
	}
	values := s.Values()

	// The size test first: it is the cheap half, and for hasExactly it rejects
	// most non-matches without running an operand at all.
	if m.Exact && len(values) != len(m.Operands) {
		return false
	}

	// One view, refilled — the same device the per-value split uses, and what
	// keeps this at a single allocation however many values the key carries
	// (P1: the hot path does no I/O and this adds no per-value garbage).
	var view singleValue
	for _, op := range m.Operands {
		satisfied := false
		for _, v := range values {
			view.set(v)
			if op.Match(&view) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			return false
		}
	}
	return true
}

// Describe implements Matcher, rendering for near-miss diagnostics.
func (m *MultiValue) Describe() string {
	name := "includes"
	if m.Exact {
		name = "hasExactly"
	}
	if len(m.Operands) == 0 {
		return name + " []"
	}
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(" [")
	for i, op := range m.Operands {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(op.Describe())
	}
	b.WriteString("]")
	return b.String()
}
