// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"net/url"
	"strings"
)

// SplitQuery splits urlencoded text — a request's query string, or the body of
// a urlencoded form — into the parameters a criterion is compared against.
// Elements are separated by `&`, each is cut at its first `=`, and the name and
// value are percent-decoded afterwards.
//
// It is written out here rather than delegated to net/url.ParseQuery because
// ParseQuery answers a different question. Since Go 1.17 it treats `;` as a
// syntax error and discards the element containing one, so `?range=1;5` reaches
// the matcher with no `range` parameter at all: the criterion that names it
// fails, an `{"absent": true}` criterion succeeds against a request that
// plainly carried it, and the journal records a request nobody sent. WireMock
// splits on `&` alone, which leaves the semicolon an ordinary character in a
// value and costs a filter or range expression nothing.
//
// The split is deliberately total — every element becomes a parameter, whatever
// it holds. A parser that drops what it cannot make sense of is the one shape
// this must not have: what it drops is indistinguishable from what a client
// never sent, and that is precisely the confusion a mock server exists to
// remove.
func SplitQuery(query string) url.Values {
	out := url.Values{}
	for query != "" {
		var element string
		element, query, _ = strings.Cut(query, "&")
		if element == "" {
			// A separator with nothing either side of it is not a parameter, and
			// must not be allowed to disturb the ones around it: query strings
			// assembled by concatenation carry leading, trailing and doubled
			// separators whenever an optional value is omitted.
			continue
		}
		// Only the first `=` separates the name from the value, so a later one
		// needs no escaping and belongs to the value it appears in. An element
		// with no `=` at all is a name carrying the empty string, which is a
		// different thing from the name being absent.
		name, value, _ := strings.Cut(element, "=")
		decoded := decodeQueryText(name)
		out[decoded] = append(out[decoded], decodeQueryText(value))
	}
	return out
}

// decodeQueryText percent-decodes one parameter name or value, keeping the text
// exactly as it arrived when it will not decode.
//
// The decode itself is not optional: an operand is written the way a person says
// the value rather than the way it travels, so `{"equalTo": "a b"}` has to meet
// `?q=a%20b`. What is a choice is the failure. An escape that is not valid — the
// bare `%` of a client that forgot to escape one — still describes a value the
// request carried, and dropping the parameter over it would take the request's
// own evidence away from every criterion written about it.
func decodeQueryText(s string) string {
	// Nothing to decode is the ordinary case, and recognising it keeps the
	// unescaper off the path of a request whose parameters are plain text.
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}
