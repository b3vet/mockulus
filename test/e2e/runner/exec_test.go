// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// body_json_contains is the vocabulary for a deployment-global listing, so what
// it must never do is pass on a listing that does not hold the case's entry —
// that is the failure mode a positional assertion has, dressed up as a search.

func TestAssertJSONContainsFindsTheEntryWhereverItIs(t *testing.T) {
	listing := []byte(`{"mappings":[{"id":"other"},{"id":"mine","response":{"body":"world"}}],` +
		`"meta":{"total":2}}`)

	want := JSONContains{Path: "mappings", Match: map[string]any{
		"id":       "mine",
		"response": map[string]any{"body": "world"},
	}}
	if err := assertJSONContains(want, listing); err != nil {
		t.Errorf("the entry is in the listing, at an index no case can predict: %v", err)
	}

	// A "$." prefix is how a diff spells a path, so a case may spell it that way.
	want.Path = "$.mappings"
	if err := assertJSONContains(want, listing); err != nil {
		t.Errorf("a $-prefixed path names the same array: %v", err)
	}
}

func TestAssertJSONContainsRejectsWhatIsNotThere(t *testing.T) {
	listing := []byte(`{"mappings":[{"id":"other","response":{"body":"world"}}],"meta":{"total":1}}`)

	cases := []struct {
		name string
		want JSONContains
		why  string
	}{
		{
			name: "no entry with that id",
			want: JSONContains{Path: "mappings", Match: map[string]any{"id": "mine"}},
			why:  "a listing without the case's stub is exactly what the step exists to catch",
		},
		{
			name: "entry present but different",
			want: JSONContains{Path: "mappings", Match: map[string]any{
				"id": "other", "response": map[string]any{"body": "goodbye"}}},
			why: "matching on identity alone would make the assertion vacuous",
		},
		{
			name: "path names no array",
			want: JSONContains{Path: "meta", Match: map[string]any{"total": 1}},
			why:  "searching an object is a case-authoring mistake, not a pass",
		},
		{
			name: "path is not in the document",
			want: JSONContains{Path: "requests", Match: map[string]any{"id": "mine"}},
			why:  "a typo in the path must not read as an empty search that succeeded",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := assertJSONContains(c.want, listing); err == nil {
				t.Errorf("expected a failure: %s", c.why)
			}
		})
	}
}

// The clause is loaded from YAML, so half of one is a live risk: it would search
// the whole document, or match every element, and report a pass.
func TestExpectRejectsAHalfWrittenContainsClause(t *testing.T) {
	for _, c := range []struct {
		name string
		want JSONContains
	}{
		{"no path", JSONContains{Match: map[string]any{"id": "mine"}}},
		{"no match", JSONContains{Path: "mappings"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			step := Step{
				Admin:  &HTTPStep{Method: "GET", Path: "/__admin/mappings"},
				Expect: &Expect{BodyJSONContains: []JSONContains{c.want}},
			}
			err := step.validate()
			if err == nil {
				t.Fatal("a half-written contains clause must fail the case at load")
			}
			if !strings.Contains(err.Error(), "body_json_contains") {
				t.Errorf("the error should name the clause, got %v", err)
			}
		})
	}
}
