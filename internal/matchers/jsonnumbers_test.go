// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"testing"
)

// TestCanonicalDecimal pins the normal form itself, because everything the
// exact comparison claims reduces to it: two literals are the same number
// exactly when they normalise alike.
func TestCanonicalDecimal(t *testing.T) {
	same := [][]string{
		// Scale is not part of a value, in either direction.
		{"1", "1.0", "1.000", "1e0", "0.1e1", "10e-1"},
		{"100", "1e2", "100.00", "1.0e2", "10000e-2"},
		{"1.5", "1.500", "15e-1", "0.15e1"},
		// Nor is the sign of a zero, nor how the zero is spelled.
		{"0", "-0", "0.0", "-0.000", "0e0", "0e100", "-0e-100"},
		// Negative values keep their sign and nothing else changes.
		{"-2.50", "-2.5", "-25e-1"},
		// The digits a float64 cannot hold survive as digits.
		{"9007199254740993", "9007199254740993.0", "9.007199254740993e15"},
	}
	for _, group := range same {
		want := canonicalDecimal(group[0])
		for _, literal := range group[1:] {
			if got := canonicalDecimal(literal); got != want {
				t.Errorf("%s and %s denote one number; normalised to %q and %q",
					group[0], literal, want, got)
			}
		}
	}

	// The control: literals that a float64 rounds together must not normalise
	// together, and neither must ones no rounding is involved in.
	distinct := []string{
		"1", "1.0000000000000000000000001", "0.9999999999999999999999999",
		"9007199254740992", "9007199254740993", "9007199254740994",
		"0", "1e-400", "-1",
		"100", "1000",
	}
	seen := make(map[jsonDecimal]string, len(distinct))
	for _, literal := range distinct {
		normalised := canonicalDecimal(literal)
		if other, clash := seen[normalised]; clash {
			t.Errorf("%s and %s are different numbers but both normalised to %q",
				other, literal, normalised)
		}
		seen[normalised] = literal
	}
}

// TestDocumentNeedsExactNumbers pins the filter that decides whether a document
// has to be read twice. A false negative is a wrong answer, so the cases that
// matter most are the ones it must flag.
func TestDocumentNeedsExactNumbers(t *testing.T) {
	wide := []string{
		`{"id":9007199254740993}`,
		`{"a":1.0000000000000000000000001}`,
		`[0.000000000000000000000001]`,
		// Underflow: Go's decoder reads this as 0 without complaining, so a
		// stub keyed on zero would answer for it.
		`{"a":1e-400}`,
		`{"a":-1e-320}`,
		// The wide literal is at depth, behind a string that itself holds
		// digits and an escaped quote.
		`{"note":"order 9007199254740993 \" 12345678901234567890","v":[{"n":123456789012345678}]}`,
	}
	for _, doc := range wide {
		if !documentNeedsExactNumbers([]byte(doc)) {
			t.Errorf("%s holds a literal float64 cannot separate and was not flagged", doc)
		}
	}

	// The control: ordinary documents must stay on the one-pass path, or the
	// filter has stopped being a filter. Digits inside strings are text.
	narrow := []string{
		`{"a":1}`, `{"a":1.0}`, `{"a":-1.5e3}`, `{"a":0}`, `{"a":-0}`,
		`{"a":123456789012345}`, `{"a":1e300}`, `{"a":1e-300}`,
		`{"a":"9007199254740993"}`,
		`{"a":"1.0000000000000000000000001"}`,
		`{"a":"quoted \" then 12345678901234567890"}`,
		`{"brand":"visa","last4":"4242"}`,
		`not json at all`,
	}
	for _, doc := range narrow {
		if documentNeedsExactNumbers([]byte(doc)) {
			t.Errorf("%s holds nothing float64 blurs and was flagged anyway", doc)
		}
	}
}

// TestEqualToJSONSeparatesDecimalsFloat64Cannot is the divergence itself: a stub
// keyed on one number answering for its float64 neighbour.
func TestEqualToJSONSeparatesDecimalsFloat64Cannot(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		actual   string
		want     bool
	}{
		// 9007199254740993 is the first integer binary64 cannot hold, and it
		// rounds onto its neighbour.
		{"wide stub, neighbour body", `{"id":9007199254740993}`, `{"id":9007199254740992}`, false},
		// The other direction, which a fix applied only to the stub's side
		// would miss: the request is the document carrying the extra digit.
		{"narrow stub, wide body", `{"id":9007199254740992}`, `{"id":9007199254740993}`, false},
		// And the ordinary stub the triage widened the entry to: nothing about
		// this is near 2^53.
		{"plain stub, absurd precision", `{"a":1}`, `{"a":1.0000000000000000000000001}`, false},
		{"plain stub, absurd precision below", `{"a":1}`, `{"a":0.9999999999999999999999999}`, false},
		// The pair a binary fraction cannot separate at all: the long literal
		// is the exact decimal value of the double nearest 0.1.
		{"0.1 and the double nearest it", `{"a":0.1}`,
			`{"a":0.1000000000000000055511151231257827}`, false},
		// Wide integers nowhere near 2^53 round together just as readily.
		{"twenty-digit neighbours", `{"a":10000000000000000001}`, `{"a":10000000000000000000}`, false},
		// Underflow rounds onto zero the same way, and Go's decoder reads it
		// without complaining.
		{"zero stub, underflowing body", `{"a":0}`, `{"a":1e-400}`, false},
		{"zero stub, subnormal body", `{"a":0}`, `{"a":1e-320}`, false},

		// The controls. Each is a document that must still match, and each is
		// one a comparison that had merely become string equality would break.
		{"same wide number", `{"id":9007199254740993}`, `{"id":9007199254740993}`, true},
		{"same wide number, respelled", `{"id":9007199254740993}`, `{"id":9.007199254740993e15}`, true},
		{"integer and its decimal spelling", `{"a":1}`, `{"a":1.0}`, true},
		{"decimal and its integer spelling", `{"a":1.0}`, `{"a":1}`, true},
		{"exponent form", `{"a":1}`, `{"a":1e0}`, true},
		{"scale on both sides", `{"a":1.500}`, `{"a":15e-1}`, true},
		{"negative zero is zero", `{"a":0}`, `{"a":-0.0}`, true},
		{"trailing zeros past the float64 width", `{"a":1}`, `{"a":1.0000000000000000000000000}`, true},
		{"sixteen decimal places of the same 1", `{"a":1}`, `{"a":1.000000000000000}`, true},
		{"wide integer written with a redundant scale", `{"id":9007199254740993}`, `{"id":9007199254740993.00}`, true},
		{"the same underflowing literal on both sides", `{"a":1e-400}`, `{"a":1e-400}`, true},
		{"the same twenty-digit integer", `{"a":10000000000000000001}`, `{"a":10000000000000000001}`, true},
		{"a literal past every width", `{"a":123456789012345678901234567890}`,
			`{"a":123456789012345678901234567890}`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := compileBody(t, `{"equalToJson":`+quoteJSON(t, c.expected)+`}`)
			if got := m.Match(NewBody([]byte(c.actual))); got != c.want {
				t.Errorf("expected %s against body %s: matched=%v, want %v",
					c.expected, c.actual, got, c.want)
			}
		})
	}
}

// TestEqualToJSONExactPassKeepsTheRelaxations checks that the second comparison
// is the same comparison. It runs only over documents that carry a wide
// literal, which is precisely where a shortcut — comparing raw text, or
// dropping the flags — would go unnoticed.
func TestEqualToJSONExactPassKeepsTheRelaxations(t *testing.T) {
	cases := []struct {
		name    string
		operand string
		actual  string
		want    bool
	}{
		// Placeholders still stand in for whole classes of value when the
		// document is read the second time.
		{"any-number covers a wide literal",
			`{"a":"${json-unit.any-number}","b":9007199254740993}`,
			`{"a":9007199254740993,"b":9007199254740993}`, true},
		{"any-number still refuses a string",
			`{"a":"${json-unit.any-number}","b":9007199254740993}`,
			`{"a":"7","b":9007199254740993}`, false},
		{"ignore covers anything at all",
			`{"a":"${json-unit.ignore}","b":9007199254740993}`,
			`{"a":[1,2],"b":9007199254740993}`, true},
		{"ignore-element still excuses an absent member",
			`{"a":"${json-unit.ignore-element}","b":9007199254740993}`,
			`{"b":9007199254740993}`, true},
		{"regex still applies to its sibling",
			`{"a":"${json-unit.regex}[a-z]+","b":9007199254740993}`,
			`{"a":"abc","b":9007199254740993}`, true},
		{"regex still refuses a non-match",
			`{"a":"${json-unit.regex}[a-z]+","b":9007199254740993}`,
			`{"a":"abc1","b":9007199254740993}`, false},

		// Structure survives too: order, absence and extra members mean what
		// they meant on the first pass.
		{"member order is still not part of the document",
			`{"a":1,"b":9007199254740993}`, `{"b":9007199254740993,"a":1}`, true},
		{"an extra member is still a mismatch",
			`{"b":9007199254740993}`, `{"b":9007199254740993,"z":1}`, false},
		{"a missing member is still a mismatch",
			`{"a":1,"b":9007199254740993}`, `{"b":9007199254740993}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := compileBody(t, `{"equalToJson":`+quoteJSON(t, c.operand)+`}`)
			if got := m.Match(NewBody([]byte(c.actual))); got != c.want {
				t.Errorf("%s against %s: matched=%v, want %v", c.operand, c.actual, got, c.want)
			}
		})
	}

	// The flags relax the exact comparison exactly as far as they relax the
	// ordinary one, wide literals included.
	flagged := []struct {
		name   string
		doc    string
		actual string
		want   bool
	}{
		{"ignoreArrayOrder pairs on value, not on spelling",
			`{"equalToJson":"{\"xs\":[9007199254740993,2.5]}","ignoreArrayOrder":true}`,
			`{"xs":[2.50,9007199254740993.0]}`, true},
		{"ignoreArrayOrder does not pair a wide literal with its neighbour",
			`{"equalToJson":"{\"xs\":[9007199254740993,2.5]}","ignoreArrayOrder":true}`,
			`{"xs":[2.5,9007199254740992]}`, false},
		{"ignoreExtraElements still forgives an unexpected member",
			`{"equalToJson":"{\"b\":9007199254740993}","ignoreExtraElements":true}`,
			`{"b":9007199254740993,"z":1}`, true},
		{"ignoreExtraElements does not forgive a wrong number",
			`{"equalToJson":"{\"b\":9007199254740993}","ignoreExtraElements":true}`,
			`{"b":9007199254740992,"z":1}`, false},
		// A wide literal only the request carries, at a position the stub never
		// looks at. The document is read twice and still matches, which is what
		// keeps the second reading from being a stricter comparison.
		{"a wide literal at an ignored member",
			`{"equalToJson":"{\"b\":1}","ignoreExtraElements":true}`,
			`{"b":1,"z":9007199254740993}`, true},
		{"a wide literal deep inside an ignored member",
			`{"equalToJson":"{\"b\":1}","ignoreExtraElements":true}`,
			`{"b":1,"z":[{"n":1.0000000000000000000000001}]}`, true},
	}
	for _, c := range flagged {
		t.Run(c.name, func(t *testing.T) {
			m := compileBody(t, c.doc)
			if got := m.Match(NewBody([]byte(c.actual))); got != c.want {
				t.Errorf("%s against %s: matched=%v, want %v", c.doc, c.actual, got, c.want)
			}
		})
	}
}

// TestEqualToJSONExactDocumentIsCompiled guards the seam the confirmation hangs
// off: a compiled matcher whose exact document went missing, or whose width was
// left at the zero value, would silently stop confirming anything and the
// divergence would come back without a test noticing.
func TestEqualToJSONExactDocumentIsCompiled(t *testing.T) {
	cases := []struct {
		operand string
		want    numberWidth
	}{
		{`{\"a\":1}`, numbersNarrow},
		{`{\"a\":[1,{\"b\":2}]}`, numbersNarrow},
		{`{\"a\":9007199254740993}`, numbersWide},
		{`{\"a\":\"text\"}`, numbersIrrelevant},
		{`{\"a\":\"${json-unit.any-number}\"}`, numbersIrrelevant},
		{`{\"a\":\"9007199254740993\"}`, numbersIrrelevant},
	}
	for _, c := range cases {
		m := compileBody(t, `{"equalToJson":"`+c.operand+`"}`)
		compiled, ok := m.(*EqualToJSON)
		if !ok {
			t.Fatalf("equalToJson compiled to %T", m)
		}
		if compiled.Exact == nil {
			t.Errorf("%s compiled without an exact document", c.operand)
		}
		if compiled.Numbers != c.want {
			t.Errorf("%s compiled with width %d, want %d", c.operand, compiled.Numbers, c.want)
		}
	}
}

// TestEqualToJSONWideNumbersOffTheBody covers the subjects that are not a body,
// since the confirmation reads the subject's bytes and a key's bytes are not
// its whole document.
func TestEqualToJSONWideNumbersOffTheBody(t *testing.T) {
	m := compile(t, `{"equalToJson":"{\"id\":9007199254740993}"}`)

	if !m.Match(NewKeyValues(`{"id":9007199254740993}`)) {
		t.Error("a header carrying the expected document should match")
	}
	if m.Match(NewKeyValues(`{"id":9007199254740992}`)) {
		t.Error("a header carrying the float64 neighbour should not match")
	}
	// A repeated key is answered value by value, so the confirmation has to
	// read the value it was applied to rather than the first one.
	if !m.Match(NewKeyValues(`{"id":9007199254740992}`, `{"id":9007199254740993}`)) {
		t.Error("one value of a repeated key satisfying the document should match")
	}
	if m.Match(NewKeyValues(`{"id":9007199254740992}`, `{"id":9007199254740994}`)) {
		t.Error("no value of the repeated key satisfies the document")
	}
	if m.Match(AbsentKey()) {
		t.Error("an absent key satisfies nothing")
	}
}

// TestEqualToJSONExactPassReadsTheDecodedDocument covers a body whose bytes are
// not its text. The confirmation has to read the document the first comparison
// read, so a stub carrying both a wide number and a non-ASCII string keeps
// matching a request that declared the charset those bytes are in — reading the
// undecoded bytes instead would turn the accented member into replacement
// characters and report a mismatch nobody wrote.
func TestEqualToJSONExactPassReadsTheDecodedDocument(t *testing.T) {
	m := compileBody(t, `{"equalToJson":"{\"n\":\"café\",\"id\":9007199254740993}"}`)

	latin1 := []byte("{\"n\":\"caf\xe9\",\"id\":9007199254740993}")
	body := &Body{}
	body.SetWithContentType(latin1, "application/json; charset=ISO-8859-1")
	if !m.Match(body) {
		t.Error("a declared ISO-8859-1 body holding the expected document should match")
	}

	body.SetWithContentType([]byte("{\"n\":\"caf\xe9\",\"id\":9007199254740992}"),
		"application/json; charset=ISO-8859-1")
	if m.Match(body) {
		t.Error("the same body with the neighbouring number should not match")
	}
}

// quoteJSON renders a document as the escaped-string operand equalToJson takes.
func quoteJSON(t *testing.T, document string) string {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("quote %s: %v", document, err)
	}
	return string(encoded)
}
