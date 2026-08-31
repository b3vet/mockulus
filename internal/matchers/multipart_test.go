// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"strings"
	"testing"
)

// The rows here are the ones recorded from pinned WireMock 3.13.2 in
// internalDocs/PROBE_MULTIPART.md, P1–P7. The two quantifiers are the part worth
// testing directly: `matchingType` ranges over the parts inside one element and
// the elements themselves are ANDed, and an implementation that collapsed those
// into a single "any part satisfies anything" rule passes a naive test.

const boundary = "xxBOUNDARYxx"

func multipartBody(parts ...[2]string) (string, string) {
	var body strings.Builder
	for _, p := range parts {
		body.WriteString("--" + boundary + "\r\n" +
			"Content-Disposition: form-data; name=\"" + p[0] + "\"\r\n\r\n" +
			p[1] + "\r\n")
	}
	body.WriteString("--" + boundary + "--\r\n")
	return body.String(), "multipart/form-data; boundary=" + boundary
}

func subjectFor(t *testing.T, parts ...[2]string) *Body {
	t.Helper()
	raw, contentType := multipartBody(parts...)
	var b Body
	b.SetWithContentType([]byte(raw), contentType)
	return &b
}

func equalToBody(s string) Matcher { return &EqualTo{Expected: s} }

func TestMultipartMatchingTypeQuantifiesOverParts(t *testing.T) {
	body := subjectFor(t, [2]string{"a", "hello"}, [2]string{"b", "other"})

	anyPart := &MultipartPatterns{Patterns: []MultipartPattern{
		{BodyMatchers: []Matcher{equalToBody("hello")}},
	}}
	if !anyPart.Match(body) {
		t.Error("ANY should match when one part satisfies the patterns")
	}

	allParts := &MultipartPatterns{Patterns: []MultipartPattern{
		{All: true, BodyMatchers: []Matcher{equalToBody("hello")}},
	}}
	if allParts.Match(body) {
		t.Error("ALL should not match when only one of two parts satisfies")
	}

	bothSatisfy := subjectFor(t, [2]string{"a", "hello"}, [2]string{"b", "hello"})
	if !allParts.Match(bothSatisfy) {
		t.Error("ALL should match when every part satisfies")
	}
}

// The elements of the array are ANDed. An implementation treating the array as
// alternatives passes every single-element test in this file.
func TestMultipartElementsAreConjunctive(t *testing.T) {
	two := &MultipartPatterns{Patterns: []MultipartPattern{
		{Name: "a", BodyMatchers: []Matcher{equalToBody("hello")}},
		{Name: "b", BodyMatchers: []Matcher{equalToBody("bye")}},
	}}
	if !two.Match(subjectFor(t, [2]string{"a", "hello"}, [2]string{"b", "bye"})) {
		t.Error("both elements satisfied should match")
	}
	if two.Match(subjectFor(t, [2]string{"a", "hello"}, [2]string{"b", "WRONG"})) {
		t.Error("only the first element satisfied should not match")
	}
	if two.Match(subjectFor(t, [2]string{"a", "WRONG"}, [2]string{"b", "bye"})) {
		t.Error("only the second element satisfied should not match")
	}
}

// A body with no parts never matches, and the empty element is what proves the
// requirement belongs to the request rather than to the element.
func TestMultipartNeedsAtLeastOnePart(t *testing.T) {
	var empty Body
	empty.SetWithContentType([]byte("--"+boundary+"--\r\n"),
		"multipart/form-data; boundary="+boundary)

	for _, c := range []struct {
		name    string
		pattern MultipartPattern
	}{
		{"ANY with patterns", MultipartPattern{BodyMatchers: []Matcher{equalToBody("x")}}},
		{"ALL with patterns", MultipartPattern{All: true, BodyMatchers: []Matcher{equalToBody("x")}}},
		{"no criteria at all", MultipartPattern{}},
	} {
		m := &MultipartPatterns{Patterns: []MultipartPattern{c.pattern}}
		if m.Match(&empty) {
			t.Errorf("%s: a body with no parts must not match", c.name)
		}
	}

	// The same element does match once a part exists, so the refusal above is
	// about the request and not about an element that can never be satisfied.
	bare := &MultipartPatterns{Patterns: []MultipartPattern{{}}}
	if !bare.Match(subjectFor(t, [2]string{"a", "v"})) {
		t.Error("an element with no criteria should match a body that has parts")
	}
}

// A body that is not multipart is a non-match rather than an error.
func TestMultipartAgainstNonMultipartBody(t *testing.T) {
	var plain Body
	plain.SetWithContentType([]byte("just text"), "text/plain")
	m := &MultipartPatterns{Patterns: []MultipartPattern{{BodyMatchers: []Matcher{equalToBody("just text")}}}}
	if m.Match(&plain) {
		t.Error("a text/plain body must not satisfy a multipart criterion")
	}

	// Nor does a body whose Content-Type claims multipart but names no boundary.
	var noBoundary Body
	noBoundary.SetWithContentType([]byte("whatever"), "multipart/form-data")
	if m.Match(&noBoundary) {
		t.Error("multipart without a boundary must not match")
	}
}

// Deviation #63. On WireMock all four of these match; here the name selects.
func TestMultipartNameSelectsThePart(t *testing.T) {
	named := &MultipartPatterns{Patterns: []MultipartPattern{
		{Name: "meta", BodyMatchers: []Matcher{equalToBody("hello")}},
	}}
	if !named.Match(subjectFor(t, [2]string{"meta", "hello"})) {
		t.Error("the named part satisfies the patterns and should match")
	}
	if named.Match(subjectFor(t, [2]string{"other", "hello"})) {
		t.Error("a differently-named part must not satisfy a named element")
	}

	nothing := &MultipartPatterns{Patterns: []MultipartPattern{{Name: "NOPE"}}}
	if nothing.Match(subjectFor(t, [2]string{"whatever", "hello"})) {
		t.Error("a name matching no part must not match")
	}
}

// The name comes from the parsed Content-Disposition parameter, so both legal
// spellings work. A substring criterion — which is how WireMock's own header
// matching sees it — distinguishes them, and that difference is the reason the
// field is honoured by parsing rather than by sugar.
func TestMultipartNameIgnoresQuotingOfTheHeader(t *testing.T) {
	raw := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=meta\r\n\r\nhello\r\n" +
		"--" + boundary + "--\r\n"
	var b Body
	b.SetWithContentType([]byte(raw), "multipart/form-data; boundary="+boundary)

	m := &MultipartPatterns{Patterns: []MultipartPattern{
		{Name: "meta", BodyMatchers: []Matcher{equalToBody("hello")}},
	}}
	if !m.Match(&b) {
		t.Error("an unquoted name= parameter should still be found")
	}
}

func TestMultipartHeaderCriteriaSeeThePartsOwnHeaders(t *testing.T) {
	raw := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"doc\"\r\n" +
		"Content-Type: application/json\r\n\r\n" +
		`{"id":1}` + "\r\n" +
		"--" + boundary + "--\r\n"
	var b Body
	b.SetWithContentType([]byte(raw), "multipart/form-data; boundary="+boundary)

	m := &MultipartPatterns{Patterns: []MultipartPattern{{
		Headers: []PartCriterion{{Name: "Content-Type", Matcher: &EqualTo{Expected: "application/json"}}},
	}}}
	if !m.Match(&b) {
		t.Error("a part's own Content-Type should be visible to a header criterion")
	}

	wrong := &MultipartPatterns{Patterns: []MultipartPattern{{
		Headers: []PartCriterion{{Name: "Content-Type", Matcher: &EqualTo{Expected: "text/plain"}}},
	}}}
	if wrong.Match(&b) {
		t.Error("a header criterion that does not hold must not match")
	}
}

// Splitting happens once however many patterns read it, which is the property
// that keeps a stub with several multipart criteria from paying per criterion.
func TestMultipartPartsAreSplitOnce(t *testing.T) {
	body := subjectFor(t, [2]string{"a", "1"}, [2]string{"b", "2"})
	first, ok := body.Parts()
	if !ok || len(first) != 2 {
		t.Fatalf("expected 2 parts, got %d ok=%v", len(first), ok)
	}
	second, _ := body.Parts()
	if &first[0] != &second[0] {
		t.Error("Parts() re-split the body instead of reusing the memoized slice")
	}

	// And repointing the subject at another request drops them.
	raw, ct := multipartBody([2]string{"z", "9"})
	body.SetWithContentType([]byte(raw), ct)
	again, _ := body.Parts()
	if len(again) != 1 || again[0].Name() != "z" {
		t.Error("a repointed body kept the previous request's parts")
	}
}
