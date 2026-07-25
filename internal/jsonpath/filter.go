// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
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

// term is one comparison, or a bare existence test.
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
		return nil, fmt.Errorf("empty filter expression")
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
			return nil, fmt.Errorf("empty filter clause")
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
func compileRelative(src string) (*Path, error) {
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(src, "@") {
		return nil, fmt.Errorf("a filter operand must start with @, got %q", src)
	}
	return Compile("$" + src[1:])
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
		// Existence: the key is there and is not null.
		return result.Matches()
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
