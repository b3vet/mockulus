// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"testing"

	"github.com/b3vet/mockulus/internal/jsonpath"
)

// The bytes of "café" under the two readings a request can declare. They are
// written as bytes rather than as literals because half the point is that the
// same four characters are four bytes under one charset and five under the
// other, and a Go source literal is always the UTF-8 one.
var (
	cafeLatin1 = []byte{'c', 'a', 'f', 0xe9}
	cafeUTF8   = []byte{'c', 'a', 'f', 0xc3, 0xa9}
)

// declaredBody builds the subject the way a served request builds it: a pooled
// instance repointed at bytes and the Content-Type they arrived under.
func declaredBody(raw []byte, contentType string) *Body {
	b := &Body{}
	b.SetWithContentType(raw, contentType)
	return b
}

// A string matcher compares text, and which text a body spells is a question
// only its declared charset can answer. Before this, the two answers below were
// mirror images of WireMock's: an ISO-8859-1 body carrying 0xE9 failed a stub
// written for "café" and a UTF-8 body passed one that declared ISO-8859-1.
func TestBodyTextIsDecodedThroughTheDeclaredCharset(t *testing.T) {
	accented := compileBody(t, `{"equalTo":"café"}`)

	if !accented.Match(declaredBody(cafeLatin1, "text/plain; charset=ISO-8859-1")) {
		t.Error(`0xE9 declared ISO-8859-1 spells "café" and should match`)
	}
	// The same operand against the same bytes with nothing decoding them. This
	// is the half that fails if the charset is read but not applied.
	if accented.Match(declaredBody(cafeLatin1, "text/plain; charset=UTF-8")) {
		t.Error("0xE9 is not UTF-8 for é and must not match under that declaration")
	}
	if accented.Match(declaredBody(cafeLatin1, "text/plain")) {
		t.Error("an undeclared body is UTF-8, where 0xE9 is not é")
	}

	// And the other direction, which is where a server that ignores the charset
	// over-matches instead: UTF-8 bytes read as ISO-8859-1 are the mojibake
	// spelling, not the accented one. Asserting the mojibake matches — rather
	// than only that "café" does not — pins the decoding to byte-per-code-point
	// and not to some tolerant fallback that quietly still succeeds.
	if accented.Match(declaredBody(cafeUTF8, "text/plain; charset=ISO-8859-1")) {
		t.Error("UTF-8 bytes declared ISO-8859-1 do not spell café")
	}
	mojibake := compileBody(t, `{"equalTo":"cafÃ©"}`)
	if !mojibake.Match(declaredBody(cafeUTF8, "text/plain; charset=ISO-8859-1")) {
		t.Error("UTF-8 bytes declared ISO-8859-1 spell cafÃ©")
	}

	// The undecoded readings, unchanged: a UTF-8 body matches whether it says so
	// or says nothing.
	for _, contentType := range []string{"", "text/plain", "text/plain; charset=UTF-8"} {
		if !accented.Match(declaredBody(cafeUTF8, contentType)) {
			t.Errorf("UTF-8 bytes under %q should still match", contentType)
		}
	}
}

// The control on the decoding: it must change the reading of the bytes that
// differ between the charsets and nothing else. ASCII is identical under both,
// so every ASCII stub in the corpus has to answer the same way whatever a
// client declares — which is the case that fails if the decoder ever reached
// for a transliteration or a normalization instead of a byte-per-code-point
// mapping.
func TestDecodedCharsetLeavesASCIIAlone(t *testing.T) {
	plain := compileBody(t, `{"equalTo":"cafe"}`)
	for _, contentType := range []string{
		"", "text/plain", "text/plain; charset=UTF-8", "text/plain; charset=ISO-8859-1",
	} {
		if !plain.Match(declaredBody([]byte("cafe"), contentType)) {
			t.Errorf("an ASCII body under %q should match an ASCII operand", contentType)
		}
		if plain.Match(declaredBody([]byte("café"), contentType)) {
			t.Errorf("under %q an accented body is not the ASCII one", contentType)
		}
	}

	// Every other text matcher reads the same decoded value, so the substring
	// and pattern forms move together with equalTo rather than one at a time.
	latin1 := declaredBody(cafeLatin1, "text/plain; charset=ISO-8859-1")
	if !compileBody(t, `{"contains":"café"}`).Match(latin1) {
		t.Error("contains should read the decoded text")
	}
	if !compileBody(t, `{"matches":"caf."}`).Match(latin1) {
		t.Error("matches should read the decoded text, where é is one character")
	}
	if compileBody(t, `{"doesNotContain":"café"}`).Match(latin1) {
		t.Error("doesNotContain is the complement over one value and must move with it")
	}
}

// binaryEqualTo is the control on the whole change: it compares the payload,
// not the text the payload spells, so no declaration may move it. WireMock
// reads it off the raw body too — verified directly, both directions.
func TestBinaryEqualToIgnoresTheDeclaredCharset(t *testing.T) {
	// The UTF-8 bytes of "café", base64-encoded.
	m := compileBody(t, `{"binaryEqualTo":"Y2Fmw6k="}`)

	for _, contentType := range []string{
		"", "text/plain", "text/plain; charset=UTF-8", "text/plain; charset=ISO-8859-1",
	} {
		if !m.Match(declaredBody(cafeUTF8, contentType)) {
			t.Errorf("under %q the operand's bytes are the body's bytes", contentType)
		}
		// The bytes that spell the same four characters under ISO-8859-1 are
		// still different bytes, and a byte comparison has to say so.
		if m.Match(declaredBody(cafeLatin1, contentType)) {
			t.Errorf("under %q a body of different bytes must not match", contentType)
		}
	}
}

// The JSON readers are handed the decoded body on WireMock as well, so a
// document sent as ISO-8859-1 has to reach them decoded here. Both routes into
// the document are covered: the parsed tree equalToJson compares, and the raw
// bytes a definite JSONPath scans without building the tree.
func TestJSONDocumentIsDecodedThroughTheDeclaredCharset(t *testing.T) {
	latin1 := []byte(`{"n":"caf` + "\xe9" + `"}`)
	utf8 := []byte(`{"n":"café"}`)

	evaluator, err := jsonpath.NewEvaluator("$.n")
	if err != nil {
		t.Fatal(err)
	}

	structural := compileBody(t, `{"equalToJson":"{\"n\":\"café\"}"}`)
	// A definite path is answered off the document's bytes without the tree
	// being built, so the decoding has to reach the bytes the scanner walks and
	// not only the tree beside it. decodeOnly drives the same criterion down
	// the other route, which is what would catch the two disagreeing.
	scanning := &MatchesJSONPath{Path: evaluator, Inner: &EqualTo{Expected: "café"}}
	decoding := &MatchesJSONPath{Path: decodeOnly{evaluator}, Inner: &EqualTo{Expected: "café"}}

	for _, m := range []Matcher{structural, scanning, decoding} {
		if !m.Match(declaredBody(latin1, "application/json; charset=ISO-8859-1")) {
			t.Errorf("%s should read the declared charset", m.Describe())
		}
		// The same bytes with nothing decoding them are not that document: the
		// stray 0xE9 is not a character UTF-8 can spell.
		if m.Match(declaredBody(latin1, "application/json")) {
			t.Errorf("%s should not match an undecoded body", m.Describe())
		}
		// And the ordinary case is untouched, which is what keeps the decoding
		// off the path of every JSON body that never declared anything.
		if !m.Match(declaredBody(utf8, "application/json")) {
			t.Errorf("%s should match a UTF-8 document", m.Describe())
		}
		if m.Match(declaredBody(utf8, "application/json; charset=ISO-8859-1")) {
			t.Errorf("%s should not match a document read as mojibake", m.Describe())
		}
	}
}

// The declaration is a parameter of a header written by clients that do not
// agree on spelling, so the spellings are pinned. Every row here was taken
// from the pinned WireMock, including the ones that must NOT decode: `latin-1`
// is not a charset name to Java at all, and a parameter whose name merely ends
// in "charset" is not the charset parameter.
func TestCharsetParameterSpellings(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		want        bodyCharset
	}{
		{"", charsetVerbatim},
		{"text/plain", charsetVerbatim},
		{"text/plain; charset=UTF-8", charsetVerbatim},
		{"text/plain; charset=utf-8", charsetVerbatim},
		{"text/plain; charset=ISO-8859-1", charsetLatin1},
		{"text/plain;charset=iso-8859-1", charsetLatin1},
		{"text/plain;   charset=ISO-8859-1", charsetLatin1},
		{`text/plain; charset="ISO-8859-1"`, charsetLatin1},
		{"text/plain; charset=latin1", charsetLatin1},
		{"text/plain; charset=L1", charsetLatin1},
		{"text/plain; charset=cp819", charsetLatin1},
		{"text/plain; charset=ISO_8859-1:1987", charsetLatin1},
		{"text/plain; charset=ISO-8859-1; boundary=x", charsetLatin1},
		{"text/plain; boundary=x; charset=ISO-8859-1", charsetLatin1},
		// The first charset parameter is the one that counts, as it is there.
		{"text/plain; charset=ISO-8859-1; charset=UTF-8", charsetLatin1},
		// Names Java resolves and this does not, read as their bytes: a
		// divergence that is recorded rather than papered over.
		{"text/plain; charset=windows-1252", charsetVerbatim},
		{"text/plain; charset=US-ASCII", charsetVerbatim},
		// Not charset parameters at all.
		{"text/plain; charset=latin-1", charsetVerbatim},
		{"text/plain; x-charset=ISO-8859-1", charsetVerbatim},
		{"text/plain; charsetx=ISO-8859-1", charsetVerbatim},
		{"text/plain; charset", charsetVerbatim},
		{"text/plain; charset=", charsetVerbatim},
	} {
		if got := charsetOf(tc.contentType); got != tc.want {
			t.Errorf("charsetOf(%q) = %v, want %v", tc.contentType, got, tc.want)
		}
	}
}

// Subjects come from a pool, so the reading one request declared must not be
// left behind for the next. This is the failure that would not show up in any
// single-request test and would answer one client's stub with another client's
// charset under load.
func TestReusedBodyDropsThePreviousDeclaration(t *testing.T) {
	accented := compileBody(t, `{"equalTo":"café"}`)

	subject := &Body{}
	subject.SetWithContentType(cafeLatin1, "text/plain; charset=ISO-8859-1")
	if !accented.Match(subject) {
		t.Fatal("the declared reading should match")
	}

	// The next request through the same instance sends the same bytes and says
	// nothing about them.
	subject.SetWithContentType(cafeLatin1, "text/plain")
	if accented.Match(subject) {
		t.Error("a repointed subject kept the previous request's charset")
	}

	// Set is the undeclared spelling of the same thing, and Reset has to clear
	// the declaration as exhaustively as it clears the bytes.
	subject.SetWithContentType(cafeLatin1, "text/plain; charset=ISO-8859-1")
	subject.Set(cafeLatin1)
	if accented.Match(subject) {
		t.Error("Set left the previous request's charset in place")
	}
	subject.SetWithContentType(cafeLatin1, "text/plain; charset=ISO-8859-1")
	subject.Reset()
	subject.SetWithContentType(cafeLatin1, "text/plain")
	if accented.Match(subject) {
		t.Error("Reset left the previous request's charset in place")
	}

	// The decoded text is memoized, so the memo has to be dropped too — a
	// subject asked for its text before it is repointed must not answer with
	// the old one.
	subject.SetWithContentType(cafeLatin1, "text/plain; charset=ISO-8859-1")
	if got := subject.Values()[0]; got != "café" {
		t.Fatalf("decoded text = %q", got)
	}
	subject.SetWithContentType(cafeUTF8, "text/plain; charset=ISO-8859-1")
	if got := subject.Values()[0]; got != "cafÃ©" {
		t.Errorf("repointed text = %q, want the new body's", got)
	}
}
