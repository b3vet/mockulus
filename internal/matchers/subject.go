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
	k.state = jsonUnparsed
	k.value = nil
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
	return &Body{raw: raw, present: true}
}

// Set repoints a pooled instance at a new request's body.
func (b *Body) Set(raw []byte) {
	b.raw = raw
	b.present = true
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

// Present implements Subject. A body is present even when empty: the request
// carried one, it just had no content.
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

// Interface checks.
var (
	_ Subject = (*KeyValues)(nil)
	_ Subject = (*Body)(nil)
	_ Subject = (*Document)(nil)
)
