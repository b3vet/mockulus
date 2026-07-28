// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"strings"
)

// Subjects are always used through a pointer. Boxing a pointer into an
// interface stores it in the interface's data word and allocates nothing, so a
// request can carry its subjects in pooled memory and hand them to matchers
// without heap traffic on the hot path (SPEC §16.3).

// jsonState tracks memoized parsing so a body is parsed at most once per
// request, however many matchers ask for it.
type jsonState int8

const (
	jsonUnparsed jsonState = iota
	jsonOK
	jsonInvalid
)

// KeyValues is the subject for a header, query parameter, cookie, form field
// or path variable: present or not, with zero or more values.
type KeyValues struct {
	present bool
	values  []string
	// strictAbsence marks a subject whose absence fails a negative matcher as
	// well as a positive one.
	strictAbsence bool

	state jsonState
	value any
}

// NewKeyValues builds a present subject over the given values.
func NewKeyValues(values ...string) *KeyValues {
	return &KeyValues{present: true, values: values}
}

// AbsentKey builds a subject for a key that is not there.
func AbsentKey() *KeyValues { return &KeyValues{} }

// Set repoints an existing subject, so pooled instances can be reused across
// requests without allocating.
func (k *KeyValues) Set(present bool, values []string) {
	k.present = present
	k.values = values
	k.strictAbsence = false
	k.state = jsonUnparsed
	k.value = nil
}

// SetStrictAbsence repoints the subject and marks absence as failing negative
// matchers too.
//
// WireMock's absence rule is not uniform across field kinds, verified directly:
// an absent header satisfies doesNotMatch, an absent cookie satisfies neither
// doesNotMatch nor matches. The distinction lives on the subject rather than in
// the matcher, because it is a property of where the value came from.
func (k *KeyValues) SetStrictAbsence(present bool, values []string) {
	k.Set(present, values)
	k.strictAbsence = true
}

// AbsenceFailsNegative reports whether absence fails a negative matcher.
func (k *KeyValues) AbsenceFailsNegative() bool { return k.strictAbsence }

// RepeatedValues reports the values a criterion must be applied to one at a
// time, and only when there are enough of them for that to change the answer.
// A key bound to a single value is answered the same way whether the criterion
// is split over it or not, so it is not reported as repeated.
func (k *KeyValues) RepeatedValues() ([]string, bool) {
	if !k.present || len(k.values) < 2 {
		return nil, false
	}
	return k.values, true
}

// Present implements Subject.
func (k *KeyValues) Present() bool { return k.present }

// Values implements Subject.
func (k *KeyValues) Values() []string { return k.values }

// Bytes implements Subject. A key's bytes are those of its first value, which
// is what a binary comparison against a header means.
func (k *KeyValues) Bytes() []byte {
	if !k.present || len(k.values) == 0 {
		return nil
	}
	return []byte(k.values[0])
}

// JSON implements Subject, parsing the first value at most once.
func (k *KeyValues) JSON() (any, bool) {
	if k.state == jsonUnparsed {
		k.state = jsonInvalid
		if k.present && len(k.values) > 0 {
			var v any
			if err := json.Unmarshal([]byte(k.values[0]), &v); err == nil {
				k.value, k.state = v, jsonOK
			}
		}
	}
	return k.value, k.state == jsonOK
}

// singleValue presents one value of a repeated key as a subject in its own
// right, so a whole criterion can be evaluated against each value in turn.
//
// One instance is filled and refilled by the combinator that owns it, which is
// what keeps splitting a repeated key at one allocation however many values the
// key carries. Nothing outside this package holds a reference to it.
type singleValue struct {
	// value is an array rather than a string so Values can return a slice over
	// it without allocating.
	value [1]string

	state jsonState
	json  any
}

// set repoints the view at the next value, dropping the previous one's parse.
func (v *singleValue) set(s string) {
	v.value[0] = s
	v.state, v.json = jsonUnparsed, nil
}

// Present implements Subject. A view only ever exists over a value that is
// there, so absence never reaches one.
func (v *singleValue) Present() bool { return true }

// Values implements Subject.
func (v *singleValue) Values() []string { return v.value[:] }

// Bytes implements Subject.
func (v *singleValue) Bytes() []byte { return []byte(v.value[0]) }

// JSON implements Subject, parsing this value at most once.
func (v *singleValue) JSON() (any, bool) {
	if v.state == jsonUnparsed {
		v.state = jsonInvalid
		var parsed any
		if err := json.Unmarshal([]byte(v.value[0]), &parsed); err == nil {
			v.json, v.state = parsed, jsonOK
		}
	}
	return v.json, v.state == jsonOK
}

// Body is the subject for a request body. Both the string form and the parsed
// JSON form are memoized, so a request whose stubs use several body matchers
// still converts and parses once.
//
// A body arrives as bytes and a string matcher compares text, so something has
// to say how the two are related. That something is the charset parameter of
// the request's own Content-Type, which WireMock decodes the body with before
// applying any matcher that reads it as text. Reading the bytes as text
// regardless would answer a stub written for `café` one way for a client that
// sends UTF-8 and the opposite way for one that declares ISO-8859-1, on the
// same two servers — which is exactly the pair of mirror-image answers this
// subject exists to keep identical.
type Body struct {
	raw     []byte
	present bool

	// contentType is the request's declaration, kept unparsed. The charset is
	// resolved on first use, because a stub matched on URL alone never asks for
	// the body as text and must not be charged for the scan (SPEC §6.4, P2).
	contentType string
	charset     bodyCharset

	text    string
	textSet bool

	// doc holds the decoded text re-encoded for the JSON readers, and is filled
	// only for a body whose declared charset was not the bytes themselves.
	doc    []byte
	docSet bool

	state jsonState
	value any
}

// NewBody builds a subject over raw request bytes that carry no declaration of
// how to read them, so the bytes are the text. Callers holding a request's
// Content-Type use SetWithContentType instead.
func NewBody(raw []byte) *Body {
	return &Body{raw: raw, present: len(raw) > 0}
}

// Set repoints a pooled instance at a new request's body, undeclared.
func (b *Body) Set(raw []byte) { b.SetWithContentType(raw, "") }

// SetWithContentType repoints a pooled instance at a new request's body
// together with the Content-Type it was sent under.
//
// The header is handed in rather than read here because a subject cannot see
// the request it was cut from, and it is stored rather than parsed because
// most requests never have their body read as text at all.
func (b *Body) SetWithContentType(raw []byte, contentType string) {
	b.raw = raw
	b.present = len(raw) > 0
	b.contentType = contentType
	b.charset = charsetUnresolved
	b.text, b.textSet = "", false
	b.doc, b.docSet = nil, false
	b.state, b.value = jsonUnparsed, nil
}

// Reset clears the subject, dropping every reference so pooled memory does not
// outlive the request that filled it.
func (b *Body) Reset() {
	b.raw = nil
	b.present = false
	b.contentType = ""
	b.charset = charsetUnresolved
	b.text, b.textSet = "", false
	b.doc, b.docSet = nil, false
	b.state, b.value = jsonUnparsed, nil
}

// Present implements Subject.
//
// A zero-length body counts as ABSENT, which is WireMock's rule and not the
// obvious one: a stub declaring any bodyPatterns fails every one of them
// against an empty body, including matches:".*" and equalTo:"". Only
// {"absent": true} matches an empty body. Verified directly against the pinned
// version, for Content-Length: 0, chunked-empty and no body alike.
func (b *Body) Present() bool { return b.present }

// Values implements Subject.
func (b *Body) Values() []string { return []string{b.asText()} }

// asText returns the body read through the charset the request declared,
// memoized so a body examined by several matchers is decoded once.
func (b *Body) asText() string {
	if !b.textSet {
		if b.resolvedCharset() == charsetLatin1 {
			b.text = decodeLatin1(b.raw)
		} else {
			b.text = string(b.raw)
		}
		b.textSet = true
	}
	return b.text
}

// Bytes implements Subject. These are the bytes as they arrived and are never
// decoded: binaryEqualTo compares the payload rather than the text it spells,
// and a charset that changed the answer there would make a stub keyed on an
// exact octet string depend on what the sender said those octets meant.
func (b *Body) Bytes() []byte { return b.raw }

// RawJSON implements the raw-document capability. A body's bytes are the
// document whenever they are also its text, so a criterion that can read them —
// a definite JSONPath — never has to have the tree below built for it
// (D-OPEN-14).
func (b *Body) RawJSON() []byte { return b.jsonDocument() }

// JSON implements Subject, parsing at most once per request.
func (b *Body) JSON() (any, bool) {
	if b.state == jsonUnparsed {
		b.state = jsonInvalid
		var v any
		if err := json.Unmarshal(b.jsonDocument(), &v); err == nil {
			b.value, b.state = v, jsonOK
		}
	}
	return b.value, b.state == jsonOK
}

// jsonDocument returns the bytes the JSON readers should parse: the body's own,
// unless the request declared a charset that had to be decoded, in which case
// they are that decoded text.
//
// equalToJson and matchesJsonPath read the same text the string matchers do —
// they are handed the decoded body on WireMock too — so a JSON document sent as
// ISO-8859-1 has to reach them decoded or a stub matches the body's text and
// not its fields. Decoding is what the memo is for: a body that declared
// nothing, or declared UTF-8, hands its own bytes over and copies nothing.
func (b *Body) jsonDocument() []byte {
	if b.resolvedCharset() != charsetLatin1 {
		return b.raw
	}
	if !b.docSet {
		b.doc, b.docSet = []byte(b.asText()), true
	}
	return b.doc
}

// bodyCharset is how a request asked for its body to be read as text, resolved
// from the Content-Type at most once per request.
type bodyCharset int8

const (
	charsetUnresolved bodyCharset = iota
	// charsetVerbatim takes the bytes as the text. It is the reading for a body
	// that declared nothing, for UTF-8, and for every name below that is not
	// recognised — Go strings are UTF-8, so it costs nothing.
	charsetVerbatim
	// charsetLatin1 reads each byte as the code point of the same value.
	charsetLatin1
)

// resolvedCharset scans the declaration on first use and remembers the answer.
func (b *Body) resolvedCharset() bodyCharset {
	if b.charset == charsetUnresolved {
		b.charset = charsetOf(b.contentType)
	}
	return b.charset
}

// charsetOf reads the charset parameter out of a Content-Type header.
//
// The walk is over every ';'-separated part, as WireMock's is, so a declaration
// carrying other parameters — `text/plain; charset=ISO-8859-1; boundary=x` — is
// still read, and the first charset parameter is the one that counts. What it
// is NOT is a substring search: `x-charset=ISO-8859-1` names a parameter that
// is not this one, and both servers ignore it. A name that this server cannot
// decode is read verbatim rather than refused: refusing would answer with an
// error where the oracle answers with a match, and the one thing WireMock does
// do with a name Java cannot resolve is fail the request with a 500, which is
// not a behaviour worth copying.
func charsetOf(contentType string) bodyCharset {
	for contentType != "" {
		var part string
		part, contentType, _ = strings.Cut(contentType, ";")
		name, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "charset") {
			continue
		}
		if isLatin1(strings.Trim(strings.TrimSpace(value), `"`)) {
			return charsetLatin1
		}
		return charsetVerbatim
	}
	return charsetVerbatim
}

// latin1Names are the spellings that resolve to ISO-8859-1, which is the one
// legacy charset that still turns up in the wild and the one WireMock's own
// answers were re-derived against. The list is Java's alias table for that
// charset rather than a guess, so a client writing `latin1` or `cp819` is read
// the same way on both servers; a spelling outside it, `latin-1` among them, is
// not a charset name to Java either.
//
// Two neighbours are deliberately absent. windows-1252 and US-ASCII are names
// Java resolves and this does not, so a body declaring one of them is read as
// its bytes here — a divergence that stays, rather than a second decoder and a
// second table to keep true.
var latin1Names = [...]string{
	"iso-8859-1", "iso8859-1", "iso8859_1", "iso_8859-1", "iso_8859_1",
	"iso_8859-1:1987", "iso-ir-100", "8859_1", "latin1", "l1",
	"ibm819", "ibm-819", "cp819", "819", "csisolatin1",
}

func isLatin1(name string) bool {
	for _, alias := range latin1Names {
		if strings.EqualFold(name, alias) {
			return true
		}
	}
	return false
}

// decodeLatin1 reads every byte as the code point of the same value, which is
// what ISO-8859-1 is, and returns that text.
//
// A body that is all ASCII spells the same text under either reading, so it is
// handed back without the conversion — which is every body that declares the
// charset out of habit rather than because it needs it.
func decodeLatin1(raw []byte) string {
	high := 0
	for _, c := range raw {
		if c >= 0x80 {
			high++
		}
	}
	if high == 0 {
		return string(raw)
	}
	var text strings.Builder
	// Every byte above ASCII becomes two, and no other byte grows.
	text.Grow(len(raw) + high)
	for _, c := range raw {
		text.WriteRune(rune(c))
	}
	return text.String()
}

// Document is the subject for an already-parsed JSON value, which is what stub
// metadata search matches against.
type Document struct {
	value   any
	present bool
	text    string
	textSet bool
	raw     []byte
}

// NewDocument builds a subject over raw JSON, parsing it eagerly since the
// caller already holds the bytes and this is off the hot path.
func NewDocument(raw []byte) *Document {
	d := &Document{raw: raw, present: len(raw) > 0}
	if d.present {
		if err := json.Unmarshal(raw, &d.value); err != nil {
			d.value = nil
		}
	}
	return d
}

// Present implements Subject.
func (d *Document) Present() bool { return d.present }

// Values implements Subject.
func (d *Document) Values() []string {
	if !d.textSet {
		d.text, d.textSet = string(d.raw), true
	}
	return []string{d.text}
}

// Bytes implements Subject.
func (d *Document) Bytes() []byte { return d.raw }

// JSON implements Subject.
func (d *Document) JSON() (any, bool) { return d.value, d.value != nil }

// Interface checks. The optional capabilities are pinned too: they are reached
// by type assertion, so losing one would not fail to compile — it would quietly
// drop the rule that depends on it.
var (
	_ Subject = (*KeyValues)(nil)
	_ Subject = (*Body)(nil)
	_ Subject = (*Document)(nil)
	_ Subject = (*singleValue)(nil)

	_ absenceStrict = (*KeyValues)(nil)
	_ repeatable    = (*KeyValues)(nil)
	// Only a body: a Document is parsed eagerly by whoever built it, so scanning
	// its bytes again would be work for an answer it already holds.
	_ rawJSON = (*Body)(nil)
)
