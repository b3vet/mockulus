// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"strings"
	"testing"
)

// keyOpts compiles in the position a query parameter or header occupies, which
// is the only place the multi-value operators are legal.
func keyOpts() Options {
	o := testOpts()
	o.AllowMultiValue = true
	return o
}

func compileKey(t *testing.T, doc string) Matcher {
	t.Helper()
	m, probs := Compile(json.RawMessage(doc), "/request/queryParameters/tag", keyOpts())
	if len(probs) > 0 {
		t.Fatalf("compiling %s: %v", doc, probs)
	}
	return m
}

// TestMultiValueIsNotABijection is the case the whole feature turns on.
//
// Both operands are `a.*` and the key carries "a1" and "b1". The list is two
// long, so the size test passes, and both operands are satisfied — by the same
// value. "b1" satisfies neither and is tolerated. A bijection between operands
// and values, which is what anyone implementing this from the name would write,
// refuses it.
//
// Verified against pinned WireMock 3.13.2 on 2026-08-12: it answers 200.
func TestMultiValueIsNotABijection(t *testing.T) {
	m := compileKey(t, `{"hasExactly":[{"matches":"a.*"},{"matches":"a.*"}]}`)

	if !m.Match(NewKeyValues("a1", "b1")) {
		t.Error(`hasExactly [a.*, a.*] must match ?tag=a1&tag=b1: the count is right ` +
			`and both operands are satisfied by "a1". A value matching nothing is tolerated`)
	}
	if !m.Match(NewKeyValues("a1", "a2")) {
		t.Error("both values matching should also match")
	}
	// The size test still bites: one value cannot satisfy a two-operand
	// hasExactly however well it matches.
	if m.Match(NewKeyValues("a1")) {
		t.Error("one value against two operands must not match")
	}
	if m.Match(NewKeyValues("a1", "a2", "a3")) {
		t.Error("three values against two operands must not match")
	}
}

// Every operand must still find someone. The non-bijection tolerates a value
// that matches nothing; it does not tolerate an operand that matches nothing.
func TestMultiValueEveryOperandMustBeSatisfied(t *testing.T) {
	m := compileKey(t, `{"hasExactly":[{"equalTo":"a"},{"equalTo":"b"}]}`)
	for _, values := range [][]string{{"a", "a"}, {"a", "z"}} {
		if m.Match(NewKeyValues(values...)) {
			t.Errorf("%v: nothing satisfies equalTo b, so this must not match", values)
		}
	}
	if !m.Match(NewKeyValues("a", "b")) {
		t.Error("a,b must match")
	}
	// Order is not significant.
	if !m.Match(NewKeyValues("b", "a")) {
		t.Error("b,a must match: the operators quantify over a set, not a sequence")
	}
}

func TestIncludesAllowsExtras(t *testing.T) {
	m := compileKey(t, `{"includes":[{"equalTo":"a"}]}`)
	if !m.Match(NewKeyValues("a")) {
		t.Error("a must match")
	}
	if !m.Match(NewKeyValues("a", "b")) {
		t.Error("includes allows values beyond the ones it names")
	}
	if m.Match(NewKeyValues("b")) {
		t.Error("b alone does not satisfy includes [a]")
	}
	if m.Match(AbsentKey()) {
		t.Error("an absent key satisfies neither operator")
	}
}

// One value may satisfy several operands under includes, for the same reason it
// may under hasExactly: values are not consumed.
func TestIncludesOperandsMayShareAValue(t *testing.T) {
	m := compileKey(t, `{"includes":[{"matches":"a.*"},{"matches":".*1"}]}`)
	if !m.Match(NewKeyValues("a1")) {
		t.Error(`"a1" satisfies both operands and must match on its own`)
	}
}

// `includes: []` is vacuous rather than inert: every operand in an empty list is
// satisfied, so it holds for any present key and reads as a presence assertion.
// WireMock registers and serves it, so it is mirrored.
func TestIncludesEmptyIsAPresenceAssertion(t *testing.T) {
	m := compileKey(t, `{"includes":[]}`)
	if !m.Match(NewKeyValues("anything")) {
		t.Error("includes [] holds for a present key")
	}
	if m.Match(AbsentKey()) {
		t.Error("includes [] does not hold for an absent key")
	}
}

// `hasExactly: []` is the opposite case and is refused. A present key carries at
// least one value, so requiring zero can never be satisfied — a stub that would
// register and never serve, which is what P3 forbids.
func TestHasExactlyEmptyIsRefused(t *testing.T) {
	_, probs := Compile(json.RawMessage(`{"hasExactly":[]}`),
		"/request/queryParameters/tag", keyOpts())
	if len(probs) == 0 {
		t.Fatal("hasExactly [] must be refused")
	}
	if !strings.Contains(probs[0].Detail, "can never match") {
		t.Errorf("the refusal should say why, got %q", probs[0].Detail)
	}
}

// The operators are legal only as the sole key of a criterion directly under
// queryParameters or headers. Every other position is refused, which is what
// WireMock does — probed, not inferred.
func TestMultiValueIsRefusedOutsideKeyPositions(t *testing.T) {
	t.Run("a body position", func(t *testing.T) {
		_, probs := Compile(json.RawMessage(`{"includes":[{"equalTo":"a"}]}`),
			"/request/bodyPatterns/0", testOpts())
		if len(probs) == 0 {
			t.Fatal("a body has one value; there is no list to quantify over")
		}
		if !strings.Contains(probs[0].Detail, "queryParameters or headers") {
			t.Errorf("the refusal should name where it is legal, got %q", probs[0].Detail)
		}
	})

	t.Run("inside a combinator", func(t *testing.T) {
		for _, doc := range []string{
			`{"and":[{"hasExactly":[{"equalTo":"a"}]},{"equalTo":"a"}]}`,
			`{"not":{"includes":[{"equalTo":"a"}]}}`,
		} {
			_, probs := Compile(json.RawMessage(doc), "/request/queryParameters/tag", keyOpts())
			if len(probs) == 0 {
				t.Errorf("%s: a combinator is declared over single-value patterns", doc)
			}
		}
	})

	t.Run("beside a sibling", func(t *testing.T) {
		for _, doc := range []string{
			`{"includes":[{"equalTo":"a"}],"equalTo":"z"}`,
			`{"hasExactly":[{"equalTo":"a"}],"includes":[{"equalTo":"a"}]}`,
		} {
			_, probs := Compile(json.RawMessage(doc), "/request/queryParameters/tag", keyOpts())
			if len(probs) == 0 {
				t.Errorf("%s: the operator is the whole criterion for a key", doc)
				continue
			}
			if !strings.Contains(probs[0].Detail, "takes no siblings") {
				t.Errorf("%s: detail %q should say why", doc, probs[0].Detail)
			}
		}
	})
}

// Operands are matcher documents. Bare strings are refused, as they are on
// WireMock, and any single-value matcher is accepted — not only equalTo.
func TestMultiValueOperandShape(t *testing.T) {
	if _, probs := Compile(json.RawMessage(`{"hasExactly":["a","b"]}`),
		"/request/queryParameters/tag", keyOpts()); len(probs) == 0 {
		t.Error("bare strings are not match operations")
	}
	m := compileKey(t, `{"hasExactly":[{"matches":"a.*"},{"contains":"b"}]}`)
	if !m.Match(NewKeyValues("a1", "xbx")) {
		t.Error("any single-value matcher is a legal operand")
	}
}

// The name ROADMAP.md published for months earns a hint rather than a bare
// refusal, because we are the reason somebody would be typing it.
func TestHavingExactlyIsNamedAsAMistake(t *testing.T) {
	_, probs := Compile(json.RawMessage(`{"havingExactly":[{"equalTo":"a"}]}`),
		"/request/queryParameters/tag", keyOpts())
	if len(probs) == 0 {
		t.Fatal("havingExactly is not a WireMock matcher")
	}
	if !strings.Contains(probs[0].Detail, "hasExactly") {
		t.Errorf("the refusal should name the real operator, got %q", probs[0].Detail)
	}
}

func TestMultiValueDescribe(t *testing.T) {
	for doc, want := range map[string]string{
		`{"hasExactly":[{"equalTo":"a"},{"equalTo":"b"}]}`: `hasExactly [equalTo "a", equalTo "b"]`,
		`{"includes":[{"equalTo":"a"}]}`:                   `includes [equalTo "a"]`,
		`{"includes":[]}`:                                  `includes []`,
	} {
		if got := compileKey(t, doc).Describe(); got != want {
			t.Errorf("%s described as %q, want %q", doc, got, want)
		}
	}
}
