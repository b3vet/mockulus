// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"encoding/json"
	"strconv"
)

// Evaluating a definite path against a decoded document builds a whole
// map[string]any tree in order to read one leaf, and that decode was the only
// thing still allocating on the request path (D-OPEN-14). Almost every stub
// names a definite path — `$.customer.id`, `$.card.brand` — so this file reads
// the leaf instead: one pass over the raw body that carries the path's steps
// with it and comes back with the byte range of the node they select.
//
// Two rules are what make the shortcut safe to take, and both cost something.
//
// The pass VALIDATES THE WHOLE DOCUMENT, not just the prefix that leads to the
// node. Stopping at the node would make `{"card":{"brand":"visa"}} junk` match
// where json.Unmarshal rejects it outright, and a body that is not JSON is a
// plain non-match (SPEC §6.7) — a divergence nobody would find until a client
// sent a truncated body.
//
// The selected node is DECODED BY encoding/json, over its own byte range,
// rather than handed on as text. The nested form gives what was selected to an
// inner matcher which compares it as text, so a number reformatted or an
// object's members reordered would change the answer. Running the same decoder
// over the same bytes is what makes the value identical rather than merely
// equivalent — see decodeNode for the one case that skips it and why that case
// is safe.
//
// TestScanMatchesTree and FuzzScanEquivalence hold the two paths to the same
// answers; neither of the rules above is something to take on trust.

// maxScanDepth mirrors encoding/json's nesting limit. A document the decoder
// refuses has to be refused here too — otherwise the scan would answer where
// the tree path reports "not JSON" — and it doubles as the bound on this file's
// recursion.
const maxScanDepth = 10000

// span is a node's byte range within the document being scanned.
type span struct{ start, end int }

func (s span) of(raw []byte) []byte { return raw[s.start:s.end] }

// scanner is one pass over one document.
type scanner struct {
	raw   []byte
	i     int
	depth int
}

// Scannable reports whether MatchBytes and EvalBytes can answer for this path.
//
// It is narrower than Definite by exactly one case. A negative index needs the
// array's length, which a single forward pass does not have; getting it means
// scanning the array to count and then scanning it again, and a path carrying
// several of them would multiply what a body costs by two per index. Those keep
// the tree evaluation, where they were already correct.
func (p *Path) Scannable() bool { return p.scannable }

// MatchBytes answers the bare form over the undecoded document. ok is false for
// a path this scanner does not take and for a document that is not JSON, which
// is a non-match either way.
//
// Nothing is decoded here at all: the truthiness test of Result.Matches only
// asks what KIND of node was selected and whether a collection is empty, and
// the bytes say both.
func (p *Path) MatchBytes(raw []byte) (matched, ok bool) {
	sel, found, valid := p.scan(raw)
	if !valid {
		return false, false
	}
	if !found {
		return false, true
	}
	return nonEmptyRaw(sel.of(raw)), true
}

// EvalBytes produces the Result that Eval would have produced for the same
// document, without decoding anything but the node it selects. ok is false on
// the same two conditions as MatchBytes.
func (p *Path) EvalBytes(raw []byte) (Result, bool) {
	sel, found, valid := p.scan(raw)
	if !valid {
		return Result{}, false
	}
	if !found {
		return Result{Definite: true}, true
	}
	return Result{Found: true, Definite: true, Node: decodeNode(sel.of(raw))}, true
}

// scan walks the document once, validating it the way encoding/json does and
// recording the range the path selects.
func (p *Path) scan(raw []byte) (sel span, found, valid bool) {
	if !p.scannable {
		return span{}, false, false
	}

	s := scanner{raw: raw}
	s.skipSpace()
	sel, found, ok := s.value(p.steps, true)
	if !ok {
		return span{}, false, false
	}
	s.skipSpace()
	if s.i != len(s.raw) {
		// Anything after the document is the document being rejected, however
		// well the part before it scanned.
		return span{}, false, false
	}
	return sel, found, true
}

// value validates the value at the cursor. When selecting, the steps still
// outstanding are applied inside it; an exhausted step list means the value at
// the cursor IS what the path selects.
func (s *scanner) value(steps []step, selecting bool) (sel span, found, ok bool) {
	if s.i >= len(s.raw) {
		return span{}, false, false
	}

	if selecting && len(steps) == 0 {
		start := s.i
		if _, _, valid := s.value(nil, false); !valid {
			return span{}, false, false
		}
		return span{start, s.i}, true, true
	}

	// A step still outstanding when the cursor is on a scalar selects nothing:
	// the scalar is validated and the path simply does not reach past it, which
	// is the tree path's failed type assertion written the other way round.
	switch s.raw[s.i] {
	case '{':
		return s.object(steps, selecting)
	case '[':
		return s.array(steps, selecting)
	case '"':
		return span{}, false, s.skipString()
	case 't':
		return span{}, false, s.skipLiteral("true")
	case 'f':
		return span{}, false, s.skipLiteral("false")
	case 'n':
		return span{}, false, s.skipLiteral("null")
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return span{}, false, s.skipNumber()
	default:
		return span{}, false, false
	}
}

// object validates an object and, when a child step is what the path wants
// next, selects inside the member that step names.
func (s *scanner) object(steps []step, selecting bool) (sel span, found, ok bool) {
	s.depth++
	if s.depth > maxScanDepth {
		return span{}, false, false
	}

	// An index step over an object selects nothing, exactly as the tree path's
	// assertion to []any does. The object is still walked, because it still has
	// to be validated.
	var want string
	wanted := false
	if selecting && steps[0].kind == stepChild {
		want, wanted = steps[0].name, true
	}

	s.i++ // '{'
	s.skipSpace()
	if s.i < len(s.raw) && s.raw[s.i] == '}' {
		s.i++
		s.depth--
		return span{}, false, true
	}

	for {
		if s.i >= len(s.raw) || s.raw[s.i] != '"' {
			return span{}, false, false
		}
		keyStart := s.i
		if !s.skipString() {
			return span{}, false, false
		}
		key := s.raw[keyStart:s.i]

		s.skipSpace()
		if s.i >= len(s.raw) || s.raw[s.i] != ':' {
			return span{}, false, false
		}
		s.i++
		s.skipSpace()

		if wanted && keyIs(key, want) {
			// A repeated key replaces what the earlier one selected, whatever
			// the later one selects — including nothing. encoding/json assigns
			// duplicate members into the same map slot in order, so the last is
			// the subtree the tree path would have walked into, and "last that
			// found something" would be a different rule.
			sel, found, ok = s.value(steps[1:], true)
			if !ok {
				return span{}, false, false
			}
		} else if _, _, valid := s.value(nil, false); !valid {
			return span{}, false, false
		}

		s.skipSpace()
		if s.i >= len(s.raw) {
			return span{}, false, false
		}
		switch s.raw[s.i] {
		case ',':
			s.i++
			s.skipSpace()
		case '}':
			s.i++
			s.depth--
			return sel, found, true
		default:
			return span{}, false, false
		}
	}
}

// array validates an array and, when an index step is what the path wants next,
// selects inside the element it numbers.
func (s *scanner) array(steps []step, selecting bool) (sel span, found, ok bool) {
	s.depth++
	if s.depth > maxScanDepth {
		return span{}, false, false
	}

	// -1 is the index no element carries, so it stands for "select nothing
	// here": a child step over an array, or a pass that is only validating.
	// Scannable already refused the negative indices a path can really name.
	want := -1
	if selecting && steps[0].kind == stepIndex {
		want = steps[0].index
	}

	s.i++ // '['
	s.skipSpace()
	if s.i < len(s.raw) && s.raw[s.i] == ']' {
		s.i++
		s.depth--
		return span{}, false, true
	}

	for n := 0; ; n++ {
		if n == want {
			sel, found, ok = s.value(steps[1:], true)
			if !ok {
				return span{}, false, false
			}
		} else if _, _, valid := s.value(nil, false); !valid {
			return span{}, false, false
		}

		s.skipSpace()
		if s.i >= len(s.raw) {
			return span{}, false, false
		}
		switch s.raw[s.i] {
		case ',':
			s.i++
			s.skipSpace()
		case ']':
			s.i++
			s.depth--
			return sel, found, true
		default:
			return span{}, false, false
		}
	}
}

// skipString validates a string. It is also the one place a document may carry
// bytes this scanner does not interpret: encoding/json admits every byte from
// 0x20 up inside a string, invalid UTF-8 included, and coerces what it cannot
// read only when it decodes. Rejecting more here than the decoder does would
// turn a body it accepts into a non-match.
func (s *scanner) skipString() bool {
	s.i++ // opening quote
	for s.i < len(s.raw) {
		switch c := s.raw[s.i]; {
		case c == '"':
			s.i++
			return true

		case c == '\\':
			s.i++
			if s.i >= len(s.raw) {
				return false
			}
			switch s.raw[s.i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				s.i++
			case 'u':
				// Four hex digits and nothing more. A lone surrogate is well
				// formed here and becomes U+FFFD at decode time, which is the
				// decoder's rule and not this one's to tighten.
				s.i++
				for range 4 {
					if s.i >= len(s.raw) || !isHex(s.raw[s.i]) {
						return false
					}
					s.i++
				}
			default:
				return false
			}

		case c < 0x20:
			return false

		default:
			s.i++
		}
	}
	return false
}

func (s *scanner) skipLiteral(word string) bool {
	if len(s.raw)-s.i < len(word) || string(s.raw[s.i:s.i+len(word)]) != word {
		return false
	}
	s.i += len(word)
	return true
}

// skipNumber implements JSON's number grammar directly rather than handing a
// candidate to strconv, because the grammar is the strict one: a leading zero,
// a bare `-`, a trailing `.` and an exponent with no digits are all things a
// general number parser accepts and encoding/json refuses.
//
// It also has to refuse what the DECODER refuses. A literal past float64's
// range is a decode error, and one decode error rejects the whole document, so
// a magnitude that could overflow is settled by strconv before the scan goes on
// — a check that costs nothing for every number that is not astronomical.
func (s *scanner) skipNumber() bool {
	start := s.i
	if s.raw[s.i] == '-' {
		s.i++
	}

	intDigits := 0
	switch {
	case s.i >= len(s.raw):
		return false
	case s.raw[s.i] == '0':
		s.i++
		intDigits = 1
	case isDigit(s.raw[s.i]):
		for s.i < len(s.raw) && isDigit(s.raw[s.i]) {
			s.i++
			intDigits++
		}
	default:
		return false
	}

	if s.i < len(s.raw) && s.raw[s.i] == '.' {
		s.i++
		if s.i >= len(s.raw) || !isDigit(s.raw[s.i]) {
			return false
		}
		for s.i < len(s.raw) && isDigit(s.raw[s.i]) {
			s.i++
		}
	}

	exp := 0
	if s.i < len(s.raw) && (s.raw[s.i] == 'e' || s.raw[s.i] == 'E') {
		s.i++
		negative := false
		if s.i < len(s.raw) && (s.raw[s.i] == '+' || s.raw[s.i] == '-') {
			negative = s.raw[s.i] == '-'
			s.i++
		}
		if s.i >= len(s.raw) || !isDigit(s.raw[s.i]) {
			return false
		}
		for s.i < len(s.raw) && isDigit(s.raw[s.i]) {
			// Saturating, so an exponent written with a hundred digits cannot
			// wrap into a small one and let an overflow through.
			if exp < 1<<30 {
				exp = exp*10 + int(s.raw[s.i]-'0')
			}
			s.i++
		}
		if negative {
			exp = -exp
		}
	}

	// A number with intDigits integer digits is below 10^intDigits, so scaling
	// it by 10^exp keeps it below 10^(intDigits+exp). Anything at or under
	// 10^308 is inside float64's range with room to spare; the rest is rare
	// enough to pay for the exact answer.
	if intDigits+exp > 308 {
		if _, err := strconv.ParseFloat(string(s.raw[start:s.i]), 64); err != nil {
			return false
		}
	}
	return true
}

func (s *scanner) skipSpace() {
	for s.i < len(s.raw) {
		switch s.raw[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// nonEmptyRaw is nonEmptyNode read off a node's bytes instead of its value. The
// two have to agree exactly, so it is written against the same three cases: a
// null node, an empty array and an empty object are the only selections that
// are not a present value. An empty string, false and 0 all are.
func nonEmptyRaw(node []byte) bool {
	switch node[0] {
	case 'n':
		// `null` is the only value a validated document starts with n.
		return false
	case '[', '{':
		for _, c := range node[1 : len(node)-1] {
			switch c {
			case ' ', '\t', '\n', '\r':
			default:
				return true
			}
		}
		return false
	default:
		return true
	}
}

// keyIs compares a raw object key with the name a child step carries.
//
// The tree path compares against the DECODED key, so a key holding an escape
// has to be decoded before it can be answered — but only then. A key of plain
// ASCII is its own decoding, which is what practically every key in a request
// body is, and comparing it where it lies is what keeps the scan off the heap.
func keyIs(raw []byte, name string) bool {
	if body, ok := plainBody(raw); ok {
		return string(body) == name
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	return decoded == name
}

// plainBody returns a quoted string's contents when those contents are their
// own decoding: printable ASCII with no escape in it, which every JSON decoder
// copies through unchanged. A byte outside that range is left to encoding/json,
// which may coerce it — invalid UTF-8 becomes U+FFFD — and guessing at that
// coercion here is how the two paths would drift apart.
func plainBody(raw []byte) ([]byte, bool) {
	body := raw[1 : len(raw)-1]
	for _, c := range body {
		if c < 0x20 || c >= 0x80 || c == '\\' {
			return nil, false
		}
	}
	return body, true
}

// decodeNode returns the value the tree path would have held for this node.
//
// encoding/json does the decoding, over the node's own bytes, because the
// nested form compares what was selected as text: a number reformatted, or an
// object's members reordered by a hand-rolled decoder, would change the answer
// that `equalTo` gives. A plain string is the one shape that skips the decoder,
// and only because its bytes ARE its value — which is also the shape almost
// every stub selects, so it is the one worth the exception.
func decodeNode(node []byte) any {
	if node[0] == '"' {
		if body, ok := plainBody(node); ok {
			return string(body)
		}
	}
	var v any
	if err := json.Unmarshal(node, &v); err != nil {
		// Unreachable: the scan validated these bytes and refused the numbers
		// the decoder cannot hold. Selecting nothing is the answer that costs
		// least if it is ever reached anyway.
		return nil
	}
	return v
}
