// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Filter expressions are the part of JSONPath stub authors reach for when a
// path alone will not do: `$.items[?(@.status == 'shipped')]`. The subset here
// is comparisons and boolean combination, which is what SPEC §6.7 commits to.
//
// A filter that selects nothing is a non-match, not an error — which is why an
// existence filter over a missing key simply yields no hits.

// filter is a compiled predicate.
type filter struct {
	// or holds disjuncts, each a conjunction of terms. `a && b || c` compiles
	// to [[a b] [c]], which is enough for the expressions that appear in stubs
	// and keeps evaluation allocation-free.
	or [][]term
}

// term is one comparison, or an existence test on the operand alone.
type term struct {
	path *Path
	// op is empty for an existence test.
	op string
	// value is the literal being compared against.
	value any
}

// parseFilter compiles the inside of ?( ).
func parseFilter(src string) (*filter, error) {
	if src == "" {
		return nil, errors.New("empty filter expression")
	}

	f := &filter{}
	for _, disjunct := range splitOutsideQuotes(src, "||") {
		var conj []term
		for _, clause := range splitOutsideQuotes(disjunct, "&&") {
			t, err := parseTerm(strings.TrimSpace(clause))
			if err != nil {
				return nil, err
			}
			conj = append(conj, t)
		}
		if len(conj) == 0 {
			return nil, errors.New("empty filter clause")
		}
		f.or = append(f.or, conj)
	}
	return f, nil
}

// comparisonOps are ordered longest-first so "==" is not read as "=".
var comparisonOps = []string{"==", "!=", ">=", "<=", ">", "<"}

func parseTerm(src string) (term, error) {
	for _, op := range comparisonOps {
		if i := indexOutsideQuotes(src, op); i >= 0 {
			left := strings.TrimSpace(src[:i])
			right := strings.TrimSpace(src[i+len(op):])

			path, err := compileRelative(left)
			if err != nil {
				return term{}, err
			}
			value, err := parseLiteral(right)
			if err != nil {
				return term{}, err
			}
			return term{path: path, op: op, value: value}, nil
		}
	}

	// No operator: an existence test.
	path, err := compileRelative(src)
	if err != nil {
		return term{}, err
	}
	return term{path: path}, nil
}

// compileRelative compiles the `@.x` form used inside a filter, by rewriting it
// to a root-relative path over the item under test.
//
// A union is refused here even though the dialect carries one elsewhere. The
// pinned WireMock resolves no union inside a predicate — `?(@['a','b'])` and
// `?(@.xs[0,1] == 'a')` both select nothing over documents where the
// single-member spellings of the same operands match — so there is no behaviour
// to agree with, and an operand that silently never resolves is a filter that
// silently never selects.
func compileRelative(src string) (*Path, error) {
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(src, "@") {
		return nil, fmt.Errorf("a filter operand must start with @, got %q", src)
	}
	path, err := Compile("$" + src[1:])
	if err != nil {
		return nil, err
	}
	for _, st := range path.steps {
		if st.kind == stepUnionIndex || st.kind == stepUnionName {
			return nil, fmt.Errorf("a union is not supported inside a filter operand: %q", src)
		}
	}
	return path, nil
}

func parseLiteral(src string) (any, error) {
	src = strings.TrimSpace(src)
	if len(src) >= 2 {
		if (src[0] == '\'' && src[len(src)-1] == '\'') || (src[0] == '"' && src[len(src)-1] == '"') {
			return src[1 : len(src)-1], nil
		}
	}
	switch src {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if n, err := strconv.ParseFloat(src, 64); err == nil {
		return n, nil
	}
	return nil, fmt.Errorf("unsupported filter literal %q", src)
}

// eval applies the predicate to one item.
func (f *filter) eval(item any) bool {
	for _, conj := range f.or {
		all := true
		for _, t := range conj {
			if !t.eval(item) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func (t term) eval(item any) bool {
	result := t.path.Eval(item)

	if t.op == "" {
		// An operator-less term asks whether the operand resolved, and nothing
		// else. The emptiness-and-null rule Result.Matches applies belongs to
		// the top level, where the test is on what the evaluator returned for
		// the whole expression; reapplying it here would quietly turn
		// `?(@.flag)` into "carries a flag that is neither null nor an empty
		// collection", so an element holding `"flag": null`, `{}` or `[]` would
		// be dropped from the selection even though the field is plainly there.
		return result.Found
	}
	if !result.Found {
		return false
	}

	for _, candidate := range result.Values() {
		if compare(candidate, t.op, t.value) {
			return true
		}
	}
	return false
}

// compare applies one operator. Numbers compare numerically and strings
// lexically; a mismatch of kinds is simply false rather than an error, because
// a filter over heterogeneous data should skip what does not fit rather than
// abandon the whole evaluation.
func compare(actual any, op string, expected any) bool {
	switch op {
	case "==":
		return equalValues(actual, expected)
	case "!=":
		return !equalValues(actual, expected)
	}

	an, aok := numeric(actual)
	en, eok := numeric(expected)
	if aok && eok {
		switch op {
		case ">":
			return an > en
		case ">=":
			return an >= en
		case "<":
			return an < en
		case "<=":
			return an <= en
		}
		return false
	}

	as, aIsStr := actual.(string)
	es, eIsStr := expected.(string)
	if aIsStr && eIsStr {
		switch op {
		case ">":
			return as > es
		case ">=":
			return as >= es
		case "<":
			return as < es
		case "<=":
			return as <= es
		}
	}
	return false
}

func equalValues(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if an, ok := numeric(a); ok {
		if bn, ok := numeric(b); ok {
			return an == bn
		}
	}
	return a == b
}

func numeric(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

// splitOutsideQuotes splits on a separator that is not inside a quoted string.
func splitOutsideQuotes(s, sep string) []string {
	var (
		out   []string
		start int
	)
	for i := 0; i+len(sep) <= len(s); {
		if j := indexOutsideQuotesFrom(s, sep, i); j >= 0 {
			out = append(out, s[start:j])
			start = j + len(sep)
			i = start
			continue
		}
		break
	}
	return append(out, s[start:])
}

func indexOutsideQuotes(s, sub string) int { return indexOutsideQuotesFrom(s, sub, 0) }

func indexOutsideQuotesFrom(s, sub string, from int) int {
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if i >= from && strings.HasPrefix(s[i:], sub) {
			return i
		}
	}
	return -1
}
