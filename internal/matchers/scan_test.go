// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"testing"

	"github.com/b3vet/mockulus/internal/jsonpath"
)

// The jsonpath package holds its two evaluators to the same Result. This file
// holds the seam ABOVE that to the same verdict, because the seam is where a
// difference would actually reach a client: MatchesJSONPath decides whether to
// scan, hands what was selected to an inner matcher as text, and applies the
// negation and the null rule around both. Any of those could be right about a
// Result and wrong about an answer.

// decodeOnly hides an evaluator's scanning capability, so the same criterion
// over the same body can be driven down the decoded path and compared. It is
// how this file gets two implementations to compare at all — the real seam
// picks the scanner whenever the engine offers it.
type decodeOnly struct{ JSONPathEvaluator }

// scanSeamBodies are the documents both paths are run over. The interesting
// ones are the values whose TEXT an inner matcher compares: a number that is
// stored as a float64 and rendered back, an object whose members a re-encode
// would reorder, and the nulls and non-documents the rules around the inner
// matcher turn on.
var scanSeamBodies = []string{
	`{"amount":1299,"currency":"EUR","card":{"brand":"visa","last4":"4242"}}`,
	`{"card":{"brand":"VISA"}}`,
	`{"card":{"brand":""}}`,
	`{"card":{"brand":null}}`,
	`{"card":{}}`,
	`{"card":[]}`,
	`{"card":{"last4":"4242","brand":"visa"}}`,
	`{"amount":1e3}`,
	`{"amount":1.0}`,
	`{"amount":0}`,
	`{"amount":-0}`,
	`{"amount":1.5}`,
	`{"amount":true}`,
	`{"amount":false}`,
	`{"amount":[1,2]}`,
	`{"amount":{"a":1}}`,
	`{"amount":"1299"}`,
	`{"amount":null}`,
	`{"items":[{"sku":"a"},{"sku":"b"}]}`,
	`{"items":[]}`,
	`{"card":{"brand":"visa"}}`,
	`{"card":{"brand":"ünïcøde"}}`,
	`{"card":{"brand":"visa"},"card":{"brand":"amex"}}`,
	`{}`,
	`[]`,
	`null`,
	`"visa"`,
	`{"card":{"brand":"visa"}} junk`,
	`not json`,
	``,
}

// scanSeamPaths mixes what the scanner takes with what it refuses, so the
// fallback is exercised by the same table.
var scanSeamPaths = []string{
	"$.card.brand", "$.amount", "$.items[0].sku", "$.card", "$.items", "$.missing",
	"$", "$['card']['brand']",
	"$..brand", "$.items[*].sku", "$.items[-1].sku",
}

func TestMatchesJSONPathScansAndDecodesAlike(t *testing.T) {
	inners := []struct {
		name  string
		build func() Matcher
	}{
		{"bare", func() Matcher { return nil }},
		{"equalTo visa", func() Matcher { return &EqualTo{Expected: "visa"} }},
		{"equalTo VISA case-insensitive", func() Matcher {
			return &EqualTo{Expected: "VISA", CaseInsensitive: true}
		}},
		{"equalTo 1299", func() Matcher { return &EqualTo{Expected: "1299"} }},
		{"equalTo 1000", func() Matcher { return &EqualTo{Expected: "1000"} }},
		{"equalTo 1", func() Matcher { return &EqualTo{Expected: "1"} }},
		{"equalTo empty", func() Matcher { return &EqualTo{Expected: ""} }},
		{"equalTo true", func() Matcher { return &EqualTo{Expected: "true"} }},
		{"equalTo null", func() Matcher { return &EqualTo{Expected: "null"} }},
		{"contains 42", func() Matcher { return &Contains{Expected: "42"} }},
		{"doesNotContain 42", func() Matcher { return &Contains{Expected: "42", Negate: true} }},
		{"equalToJson", func() Matcher {
			return &EqualToJSON{Expected: expectedJSON(t, `{"brand":"visa","last4":"4242"}`)}
		}},
		{"equalToJson ignoring extras", func() Matcher {
			return &EqualToJSON{Expected: expectedJSON(t, `{"brand":"visa"}`), IgnoreExtraElements: true}
		}},
	}

	for _, expr := range scanSeamPaths {
		evaluator, err := jsonpath.NewEvaluator(expr)
		if err != nil {
			t.Fatalf("compile %q: %v", expr, err)
		}

		for _, inner := range inners {
			for _, negate := range []bool{false, true} {
				scanning := &MatchesJSONPath{Path: evaluator, Inner: inner.build(), Negate: negate}
				decoding := &MatchesJSONPath{
					Path:   decodeOnly{evaluator},
					Inner:  inner.build(),
					Negate: negate,
				}

				for _, body := range scanSeamBodies {
					got := scanning.Match(NewBody([]byte(body)))
					want := decoding.Match(NewBody([]byte(body)))
					if got != want {
						t.Errorf("%s (%s, negate=%v) over %s: scanning says %v, decoding says %v",
							expr, inner.name, negate, body, got, want)
					}
				}
			}
		}
	}
}

// TestMatchesJSONPathFallsBackForSubjectsWithoutRawBytes covers the other half
// of the seam's condition: a header value is a string, so the criterion has to
// decode it, and it has to reach the same answer as it would over a body
// carrying the same document.
func TestMatchesJSONPathFallsBackForSubjectsWithoutRawBytes(t *testing.T) {
	evaluator, err := jsonpath.NewEvaluator("$.card.brand")
	if err != nil {
		t.Fatal(err)
	}
	m := &MatchesJSONPath{Path: evaluator, Inner: &EqualTo{Expected: "visa"}}

	for _, body := range scanSeamBodies {
		if body == "" {
			// An empty body is absent and an empty header value is present but
			// not JSON. Both are a non-match, by different rules, and the rules
			// are not this test's subject.
			continue
		}
		got := m.Match(NewKeyValues(body))
		want := m.Match(NewBody([]byte(body)))
		if got != want {
			t.Errorf("over %s: a key subject says %v, a body says %v", body, got, want)
		}
	}
}

func expectedJSON(t *testing.T, src string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(src), &v); err != nil {
		t.Fatalf("fixture %s: %v", src, err)
	}
	return v
}
