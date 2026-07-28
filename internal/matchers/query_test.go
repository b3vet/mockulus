// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"net/url"
	"testing"
)

// The split is what every query and form criterion is compared against, and
// what a journal entry and a response template describe, so it is pinned as a
// table rather than through any one of them.
//
// The first group is the behaviour this exists for: `;` and an escape that will
// not decode are ordinary text. The rest is the control — the structure that
// must survive being lenient about those two, because a splitter that gave up
// and returned the query whole would satisfy the first group perfectly.
func TestSplitQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  url.Values
	}{
		{
			name:  "a semicolon is an ordinary character in a value",
			query: "a=1;b=2",
			want:  url.Values{"a": {"1;b=2"}},
		},
		{
			name:  "and in a name",
			query: "a;b=1",
			want:  url.Values{"a;b": {"1"}},
		},
		{
			name:  "an escape that will not decode is kept as it arrived",
			query: "a=100%&b=ok",
			want:  url.Values{"a": {"100%"}, "b": {"ok"}},
		},
		{
			name:  "a truncated escape at the very end is kept too",
			query: "a=x%2",
			want:  url.Values{"a": {"x%2"}},
		},

		{
			name:  "the empty query has no parameters",
			query: "",
			want:  url.Values{},
		},
		{
			name:  "only the first = separates a name from its value",
			query: "a=b=c",
			want:  url.Values{"a": {"b=c"}},
		},
		{
			name:  "an element with no = is a name carrying the empty string",
			query: "flag",
			want:  url.Values{"flag": {""}},
		},
		{
			name:  "an element that is only = is a parameter with an empty name",
			query: "=v",
			want:  url.Values{"": {"v"}},
		},
		{
			name:  "separators with nothing either side of them are not parameters",
			query: "&a=1&&b=2&",
			want:  url.Values{"a": {"1"}, "b": {"2"}},
		},
		{
			name:  "a repeated name keeps every value, in order",
			query: "a=2&a=1&a=2",
			want:  url.Values{"a": {"2", "1", "2"}},
		},
		{
			name:  "percent escapes decode exactly once, in the name and the value",
			query: "a%20b=c%2Fd&e=%2520",
			want:  url.Values{"a b": {"c/d"}, "e": {"%20"}},
		},
		{
			name:  "a plus is a space, as it is in a form",
			query: "a=x+y&b=%2B",
			want:  url.Values{"a": {"x y"}, "b": {"+"}},
		},
		{
			name:  "an escaped separator belongs to the value it is written in",
			query: "a=1%262&b=3",
			want:  url.Values{"a": {"1&2"}, "b": {"3"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitQuery(tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("SplitQuery(%q) = %v, want %v", tc.query, got, tc.want)
			}
			for name, want := range tc.want {
				values, ok := got[name]
				if !ok {
					t.Fatalf("SplitQuery(%q) has no %q: %v", tc.query, name, got)
				}
				if len(values) != len(want) {
					t.Fatalf("SplitQuery(%q)[%q] = %v, want %v", tc.query, name, values, want)
				}
				for i := range want {
					if values[i] != want[i] {
						t.Errorf("SplitQuery(%q)[%q][%d] = %q, want %q",
							tc.query, name, i, values[i], want[i])
					}
				}
			}
		})
	}
}
