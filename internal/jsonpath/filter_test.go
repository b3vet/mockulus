// SPDX-License-Identifier: Apache-2.0

package jsonpath

import "testing"

// An operator-less filter term is a presence test on the operand, so a field
// that is there selects its element whatever it holds. The three interesting
// values are the ones the bare form's emptiness rule rejects — a null, an empty
// object and an empty array — because they are exactly what a filter term would
// lose if it borrowed that rule.
func TestExistenceTermIsPresenceNotTruthiness(t *testing.T) {
	cases := []struct {
		body string
		want int
		why  string
	}{
		{`{"items":[{"flag":"on"}]}`, 1, "a present non-empty string"},
		{`{"items":[{"flag":""}]}`, 1, "an empty string is present"},
		{`{"items":[{"flag":false}]}`, 1, "false is present"},
		{`{"items":[{"flag":0}]}`, 1, "zero is present"},
		{`{"items":[{"flag":null}]}`, 1, "a null field is still a field that is there"},
		{`{"items":[{"flag":{}}]}`, 1, "an empty object is still a field that is there"},
		{`{"items":[{"flag":[]}]}`, 1, "an empty array is still a field that is there"},
		{`{"items":[{"flag":{"a":1}}]}`, 1, "a non-empty object"},
		{`{"items":[{"other":1}]}`, 0, "the control: no such field, so nothing is selected"},
		{`{"items":[]}`, 0, "no elements to test"},
	}

	p, err := Compile("$.items[?(@.flag)]")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		result := p.Eval(doc(t, c.body))
		if got := len(result.Values()); got != c.want {
			t.Errorf("$.items[?(@.flag)] on %s selected %d, want %d (%s)", c.body, got, c.want, c.why)
		}
		if got := result.Matches(); got != (c.want > 0) {
			t.Errorf("$.items[?(@.flag)] on %s matched %v, want %v (%s)", c.body, got, c.want > 0, c.why)
		}
	}
}

// The control on the widening: presence is not the same as a comparison, and
// only the operator-less term changed. A term that names an operator still
// tests the value, so a null field neither equals a string nor is skipped over
// on its way to an element that does match.
func TestComparisonTermsStillTestTheValue(t *testing.T) {
	cases := []struct {
		expr string
		body string
		want int
		why  string
	}{
		{"$.items[?(@.flag == 'on')]", `{"items":[{"flag":null}]}`, 0, "a null does not equal a string"},
		{"$.items[?(@.flag == null)]", `{"items":[{"flag":null}]}`, 1, "an explicit null literal still matches a null"},
		{"$.items[?(@.flag == null)]", `{"items":[{"flag":"on"}]}`, 0, "the null literal does not match a string"},
		{"$.items[?(@.flag != 'on')]", `{"items":[{"flag":null}]}`, 1, "a null is not 'on'"},
		{"$.items[?(@.flag != 'on')]", `{"items":[{"other":1}]}`, 0, "a missing operand fails every comparison"},
		{"$.items[?(@.qty > 1)]", `{"items":[{"qty":null}]}`, 0, "a null is not greater than a number"},
		{"$.items[?(@.qty > 1)]", `{"items":[{"qty":2}]}`, 1, "the comparison itself still works"},
	}

	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("compile %q: %v", c.expr, err)
			continue
		}
		if got := len(p.Eval(doc(t, c.body)).Values()); got != c.want {
			t.Errorf("%s on %s selected %d, want %d (%s)", c.expr, c.body, got, c.want, c.why)
		}
	}
}

// Presence inside a filter must not leak out to the expression as a whole. The
// top-level rule still tests what the evaluator returned, so a filter that
// selects no element is a non-match even though the elements it looked at
// carried the field, and a definite path onto a null is still a non-match.
func TestTopLevelEmptinessRuleIsUnchanged(t *testing.T) {
	selecting, err := Compile("$.items[?(@.flag)]")
	if err != nil {
		t.Fatal(err)
	}
	if selecting.Eval(doc(t, `{"items":[{"other":1}]}`)).Matches() {
		t.Error("a filter selecting no element must not match")
	}

	definite, err := Compile("$.v")
	if err != nil {
		t.Fatal(err)
	}
	if definite.Eval(doc(t, `{"v":null}`)).Matches() {
		t.Error("$.v onto a null must still be a non-match: the filter change is scoped to filter terms")
	}
}

// A filter over an object tests the object itself, and that form takes the same
// presence rule — the root document carrying a null field selects the document.
func TestExistenceTermOverAnObjectRoot(t *testing.T) {
	p, err := Compile("$[?(@.flag)]")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Eval(doc(t, `{"flag":null}`)).Matches() {
		t.Error("$[?(@.flag)] must select a document whose flag is null")
	}
	if p.Eval(doc(t, `{"other":1}`)).Matches() {
		t.Error("$[?(@.flag)] must not select a document without the field")
	}
}

// Conjunction and disjunction combine the terms rather than re-deciding them,
// so a present-but-null field satisfies its half of an && and does not rescue
// the other half.
func TestExistenceTermInsideBooleanCombination(t *testing.T) {
	both, err := Compile("$.items[?(@.a && @.b)]")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(both.Eval(doc(t, `{"items":[{"a":null,"b":null}]}`)).Values()); got != 1 {
		t.Errorf("both fields present selected %d, want 1", got)
	}
	if got := len(both.Eval(doc(t, `{"items":[{"a":null}]}`)).Values()); got != 0 {
		t.Errorf("one field missing selected %d, want 0", got)
	}

	either, err := Compile("$.items[?(@.a || @.b)]")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(either.Eval(doc(t, `{"items":[{"b":[]}]}`)).Values()); got != 1 {
		t.Errorf("one field present selected %d, want 1", got)
	}
}
