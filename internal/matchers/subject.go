// SPDX-License-Identifier: Apache-2.0

package matchers

import "encoding/json"

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
type Body struct {
	raw     []byte
	present bool

	text    string
	textSet bool

	state jsonState
	value any
}

// NewBody builds a subject over raw request bytes.
func NewBody(raw []byte) *Body {
	return &Body{raw: raw, present: len(raw) > 0}
}

// Set repoints a pooled instance at a new request's body.
func (b *Body) Set(raw []byte) {
	b.raw = raw
	b.present = len(raw) > 0
	b.text, b.textSet = "", false
	b.state, b.value = jsonUnparsed, nil
}

// Reset clears the subject, dropping every reference so pooled memory does not
// outlive the request that filled it.
func (b *Body) Reset() {
	b.raw = nil
	b.present = false
	b.text, b.textSet = "", false
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
func (b *Body) Values() []string {
	if !b.textSet {
		b.text, b.textSet = string(b.raw), true
	}
	return []string{b.text}
}

// Bytes implements Subject.
func (b *Body) Bytes() []byte { return b.raw }

// RawJSON implements the raw-document capability. A body's bytes ARE the
// document, so a criterion that can read them — a definite JSONPath — never has
// to have the tree below built for it (D-OPEN-14).
func (b *Body) RawJSON() []byte { return b.raw }

// JSON implements Subject, parsing at most once per request.
func (b *Body) JSON() (any, bool) {
	if b.state == jsonUnparsed {
		b.state = jsonInvalid
		var v any
		if err := json.Unmarshal(b.raw, &v); err == nil {
			b.value, b.state = v, jsonOK
		}
	}
	return b.value, b.state == jsonOK
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
