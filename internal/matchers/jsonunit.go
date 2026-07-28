// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// json-unit placeholders let an expected document say "any string here" rather
// than a literal value. WireMock interprets them inside equalToJson by default,
// with no opt-in flag — verified against the pinned version.
//
// They are resolved at compile time: the expected document is walked once and
// every placeholder string is replaced by a node that knows how to match. The
// comparison then costs the same as a literal one, and an unparseable
// placeholder is a registration error rather than a stub that quietly matches
// nothing.

// Placeholder prefixes recognised inside an expected document.
const (
	placeholderIgnore        = "${json-unit.ignore}"
	placeholderIgnoreElement = "${json-unit.ignore-element}"
	placeholderAnyString     = "${json-unit.any-string}"
	placeholderAnyNumber     = "${json-unit.any-number}"
	placeholderAnyBoolean    = "${json-unit.any-boolean}"
	placeholderRegexPrefix   = "${json-unit.regex}"
	// placeholderPrefix identifies any json-unit placeholder, including ones
	// this build does not implement.
	placeholderPrefix = "${json-unit."
)

// placeholderKind selects what a resolved placeholder accepts.
type placeholderKind uint8

const (
	// phAny accepts any value at all, of any type, but the member must be there.
	phAny placeholderKind = iota
	// phAnyOrAbsent additionally allows the member to be missing entirely.
	phAnyOrAbsent
	// phAnyString accepts any JSON string.
	phAnyString
	// phAnyNumber accepts any JSON number.
	phAnyNumber
	// phAnyBoolean accepts any JSON boolean.
	phAnyBoolean
	// phRegex accepts a string fully matching a pattern.
	phRegex
	// phDecimal is not a placeholder in the json-unit sense: it carries an
	// expected number as an exact decimal. It rides this node because the
	// comparison walk already dispatches to it, so the exact document needs no
	// case of its own alongside the ordinary one.
	phDecimal
)

// jsonPlaceholder is a compiled placeholder standing in for an expected value.
type jsonPlaceholder struct {
	kind    placeholderKind
	pattern PatternMatcher
	// decimal holds the expected number of a phDecimal node.
	decimal jsonDecimal
	// source is the placeholder as written, for diagnostics.
	source string
}

// matches reports whether an actual JSON value satisfies the placeholder.
//
// A number arrives as a float64 from the ordinary decode and as a jsonDecimal
// from the exact one, so every rule that looks at numbers accepts both: the two
// documents differ in how faithfully they carry digits, not in what they say.
func (p *jsonPlaceholder) matches(actual any) bool {
	switch p.kind {
	case phAny, phAnyOrAbsent:
		return true
	case phAnyString:
		_, ok := actual.(string)
		return ok
	case phAnyNumber:
		switch actual.(type) {
		case float64, jsonDecimal:
			return true
		}
		return false
	case phAnyBoolean:
		_, ok := actual.(bool)
		return ok
	case phRegex:
		s, ok := actual.(string)
		return ok && p.pattern.MatchString(s)
	case phDecimal:
		d, ok := actual.(jsonDecimal)
		return ok && d == p.decimal
	default:
		return false
	}
}

// optional reports whether the placeholder also stands in for a member that is
// not there at all.
func (p *jsonPlaceholder) optional() bool { return p.kind == phAnyOrAbsent }

// HasPlaceholder reports whether a value written into an expected document is a
// json-unit placeholder.
func hasPlaceholder(s string) bool { return strings.HasPrefix(s, placeholderPrefix) }

// resolvePlaceholders walks an expected document and replaces every placeholder
// string with a compiled placeholder node. It returns the rewritten document,
// whether anything was replaced, and any problem found.
//
// compileRegex may be nil, in which case a regex placeholder is a problem
// rather than being silently downgraded to a literal comparison.
func resolvePlaceholders(expected any, compileRegex RegexCompiler, pointer string) (any, bool, []Problem) {
	switch v := expected.(type) {
	case string:
		if !hasPlaceholder(v) {
			return v, false, nil
		}
		ph, kind, err := compilePlaceholder(v, compileRegex)
		if err != nil {
			return nil, false, []Problem{{Kind: kind, Pointer: pointer, Detail: err.Error()}}
		}
		return ph, true, nil

	case map[string]any:
		found := false
		var problems []Problem
		out := make(map[string]any, len(v))
		for key, child := range v {
			resolved, childFound, probs := resolvePlaceholders(child, compileRegex, pointer)
			problems = append(problems, probs...)
			found = found || childFound
			out[key] = resolved
		}
		return out, found, problems

	case []any:
		found := false
		var problems []Problem
		out := make([]any, len(v))
		for i, child := range v {
			resolved, childFound, probs := resolvePlaceholders(child, compileRegex, pointer)
			problems = append(problems, probs...)
			found = found || childFound
			out[i] = resolved
		}
		return out, found, problems

	default:
		return expected, false, nil
	}
}

// compilePlaceholder turns a placeholder string into a matcher node, and
// reports which catalog code its failure belongs to.
func compilePlaceholder(s string, compileRegex RegexCompiler) (*jsonPlaceholder, ProblemKind, error) {
	switch s {
	case placeholderIgnore:
		return &jsonPlaceholder{kind: phAny, source: s}, ProblemMalformed, nil
	case placeholderIgnoreElement:
		// Not a synonym for ignore, despite the name: probing shows `ignore`
		// requires the member to be present while `ignore-element` also accepts
		// its absence. Expected {"a": ignore} rejects {}; ignore-element accepts it.
		return &jsonPlaceholder{kind: phAnyOrAbsent, source: s}, ProblemMalformed, nil
	case placeholderAnyString:
		return &jsonPlaceholder{kind: phAnyString, source: s}, ProblemMalformed, nil
	case placeholderAnyNumber:
		return &jsonPlaceholder{kind: phAnyNumber, source: s}, ProblemMalformed, nil
	case placeholderAnyBoolean:
		return &jsonPlaceholder{kind: phAnyBoolean, source: s}, ProblemMalformed, nil
	}

	if pattern, found := strings.CutPrefix(s, placeholderRegexPrefix); found {
		if compileRegex == nil {
			return nil, ProblemMalformed,
				fmt.Errorf("no regex engine is configured for %s", placeholderRegexPrefix)
		}
		// json-unit applies the pattern as a full match, verified against the
		// pinned WireMock: [a-z]+ accepts "abc" and rejects "abc1".
		compiled, err := compileRegex(pattern)
		if err != nil {
			// A regex problem, not a document problem: the author needs to look
			// at the pattern, not at the placeholder syntax.
			return nil, ProblemRegex,
				fmt.Errorf("the pattern in %q does not compile: %w", s, err)
		}
		return &jsonPlaceholder{kind: phRegex, pattern: compiled, source: s}, ProblemMalformed, nil
	}

	// An unrecognised placeholder is refused rather than compared literally:
	// comparing it as text would mean the stub silently never matches, which is
	// the failure mode the whole fail-loud contract exists to prevent.
	return nil, ProblemMalformed, fmt.Errorf("unknown json-unit placeholder %q", s)
}

// A JSON number is a decimal of unbounded precision and the tree a document
// decodes into carries it as a float64, which is neither. That loss is one a
// matcher cannot shrug off, because it is exactly a loss of the ability to tell
// two documents apart: 9007199254740993 and 9007199254740992 are one float64, so
// a stub keyed on the first also answers for the second, and so do {"a": 1} and
// {"a": 1.0000000000000000000000001}. Nothing about that is confined to the 2^53
// boundary — any two decimals that round together collide, so an entirely
// ordinary stub over-matches. WireMock compares through BigDecimal, which
// separates every such pair while still calling 1 and 1.0, and 0 and -0, one
// number, since scale and the sign of zero are not part of a value.
//
// Reproducing that on the ordinary path would mean either decoding every body
// twice or changing the representation every other matcher reads a document
// through, and neither buys anything: for every number a request realistically
// carries, the float64 comparison already gives WireMock's answer. So the exact
// comparison is a confirmation rather than a replacement. equalToJson compares
// the decoded tree as it always did, and only a document that matched — and that
// holds a literal wide enough for the rounding to have mattered — is read a
// second time and compared as decimals. The hot path keeps its cost; the answer
// stops depending on how the digits happened to round.

// jsonDecimal is a JSON number normalised to a sign, its significant digits and
// a power of ten. Two literals denote the same number exactly when their
// normalised forms are equal, so the comparison is a string equality — and scale,
// the trailing zeros that make 1.0 look unlike 1, is normalised away rather than
// compared.
type jsonDecimal string

// float64 carries 15 significant decimal digits without loss, so any literal
// within that width round-trips through it and two such literals are therefore
// distinguishable by the ordinary comparison. The bound on the exponent is there
// for subnormals, where 1e-320 loses precision on one digit, and for the
// underflow Go's decoder accepts silently: 1e-400 decodes to 0 and would
// otherwise answer a stub keyed on zero.
const (
	float64SafeDigits   = 15
	float64SafeExponent = 300
)

// canonicalDecimal normalises a JSON number literal.
func canonicalDecimal(literal string) jsonDecimal {
	rest := literal
	negative := strings.HasPrefix(rest, "-")
	if negative {
		rest = rest[1:]
	}

	mantissa, exponent := rest, 0
	if i := strings.IndexAny(rest, "eE"); i >= 0 {
		mantissa = rest[:i]
		parsed, err := strconv.Atoi(rest[i+1:])
		if err != nil {
			// Only a literal no decoder produced can reach this, and comparing
			// it as written is still an answer rather than a panic.
			return jsonDecimal(literal)
		}
		exponent = parsed
	}

	digits := mantissa
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		digits = mantissa[:i] + mantissa[i+1:]
		exponent -= len(mantissa) - i - 1
	}
	for len(digits) > 0 && digits[0] == '0' {
		digits = digits[1:]
	}
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exponent++
	}
	if digits == "" {
		// Every digit was a zero, so the value is zero whatever the exponent
		// and whatever the sign says: -0.000 and 0 are one number.
		return "0"
	}

	var out strings.Builder
	out.Grow(len(digits) + 8)
	if negative {
		out.WriteByte('-')
	}
	out.WriteString(digits)
	out.WriteByte('e')
	out.WriteString(strconv.Itoa(exponent))
	return jsonDecimal(out.String())
}

// errTrailingJSON reports bytes after a complete document, which the ordinary
// decode refuses as well — the exact one has to refuse the same inputs or a
// body's two readings could disagree about whether it is a document at all.
var errTrailingJSON = errors.New("unexpected content after a complete JSON document")

// decodeExactJSON decodes a document with every number kept as an exact decimal.
func decodeExactJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailingJSON
	}
	return exactNumbers(decoded), nil
}

// exactNumbers rewrites the numbers of a freshly decoded document in place.
func exactNumbers(node any) any {
	switch v := node.(type) {
	case json.Number:
		return canonicalDecimal(v.String())
	case map[string]any:
		for key, child := range v {
			v[key] = exactNumbers(child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = exactNumbers(child)
		}
		return v
	default:
		return node
	}
}

// numberWidth records what an exact-decimal confirmation could still change
// about a match the ordinary comparison has already accepted. It is stored on
// the compiled matcher so the common stub never pays for the question.
type numberWidth uint8

const (
	// numbersIrrelevant is the zero value, and says the expected document holds
	// no number at all: no request can make the two comparisons disagree, so a
	// matcher assembled by hand rather than compiled is answered as before.
	numbersIrrelevant numberWidth = iota
	// numbersNarrow marks expected numbers float64 holds faithfully. Only a wide
	// literal in the request can change the answer, so the request's bytes are
	// scanned and usually settle it.
	numbersNarrow
	// numbersWide marks an expected document that itself holds a literal float64
	// cannot hold, so every match it accepts is confirmed as decimals.
	numbersWide
)

// confirmNumbers re-runs a match that succeeded at float64 precision as a
// comparison of exact decimals, when either document holds a literal that
// precision could have blurred.
func (m *EqualToJSON) confirmNumbers(s Subject) bool {
	if m.Numbers == numbersIrrelevant {
		return true
	}
	document := jsonDocumentOf(s)
	if m.Numbers == numbersNarrow && !documentNeedsExactNumbers(document) {
		return true
	}
	if m.Exact == nil {
		// A matcher put together by hand rather than compiled carries no exact
		// document; there is nothing to confirm against and the comparison that
		// already ran stands.
		return true
	}
	actual, err := decodeExactJSON(document)
	if err != nil {
		// The ordinary decode of these same bytes succeeded, so this is
		// unreachable; a document that will not parse is a non-match either way.
		return false
	}
	return jsonEqual(m.Exact, actual, m.IgnoreArrayOrder, m.IgnoreExtraElements)
}

// jsonDocumentOf returns the bytes a subject's JSON reading was taken from,
// which are not always the bytes it arrived in: a body sent under a declared
// charset is decoded to text before any JSON reader sees it. The confirmation
// has to read the document the first comparison read, or the two disagree about
// something other than precision.
func jsonDocumentOf(s Subject) []byte {
	if document, ok := s.(rawJSON); ok {
		return document.RawJSON()
	}
	return s.Bytes()
}

// documentNeedsExactNumbers reports whether a JSON document holds a numeric
// literal wide enough that float64 may not tell it from a neighbour. It reads
// the bytes rather than the decoded tree because decoding is precisely what
// throws away the digits it is looking for.
//
// It is a filter and it errs towards yes: a literal it flags that would have
// compared correctly anyway costs one extra comparison, while one it lets past
// would cost the answer.
func documentNeedsExactNumbers(doc []byte) bool {
	for i := 0; i < len(doc); i++ {
		switch c := doc[i]; {
		case c == '"':
			// Digits inside a string are text, and text compares as text.
			for i++; i < len(doc) && doc[i] != '"'; i++ {
				if doc[i] == '\\' {
					i++
				}
			}
		case c >= '0' && c <= '9':
			last, wide := scanNumberLiteral(doc, i)
			if wide {
				return true
			}
			i = last
		}
	}
	return false
}

// scanNumberLiteral reads the numeric token starting at a digit, and reports the
// index of its last byte together with whether float64 may blur it.
func scanNumberLiteral(doc []byte, start int) (last int, wide bool) {
	digits, exponent, sign := 0, 0, 1
	inExponent := false
	i := start
scan:
	for ; i < len(doc); i++ {
		switch c := doc[i]; {
		case c >= '0' && c <= '9':
			if !inExponent {
				digits++
				continue
			}
			// An exponent past the bound is already an answer, so it stops
			// growing rather than overflowing on a long run of digits.
			if exponent <= float64SafeExponent {
				exponent = exponent*10 + int(c-'0')
			}
		case c == '.':
		case (c == 'e' || c == 'E') && !inExponent:
			inExponent = true
		case (c == '+' || c == '-') && inExponent && (doc[i-1] == 'e' || doc[i-1] == 'E'):
			if c == '-' {
				sign = -1
			}
		default:
			break scan
		}
	}
	exponent *= sign
	return i - 1, digits > float64SafeDigits ||
		exponent > float64SafeExponent || exponent < -float64SafeExponent
}

// resolveExactExpected builds the expected document a second time, with numbers
// carried as exact decimals.
//
// It walks the already-resolved document alongside a fresh decode of the same
// text rather than resolving placeholders again: the two have the same shape by
// construction, so every placeholder node is the one already compiled — which
// keeps one regex compilation per pattern, and keeps the two documents from ever
// disagreeing about what a placeholder means.
func resolveExactExpected(resolved any, source string) (any, numberWidth, error) {
	decoded, err := decodeExactJSON([]byte(source))
	if err != nil {
		return nil, numbersIrrelevant, err
	}
	exact, found := pairExactNumbers(resolved, decoded)
	switch {
	case !found:
		return exact, numbersIrrelevant, nil
	case documentNeedsExactNumbers([]byte(source)):
		return exact, numbersWide, nil
	default:
		return exact, numbersNarrow, nil
	}
}

// pairExactNumbers copies the resolved document, replacing each number with the
// exact decimal the parallel decode read for it, and reports whether it found
// one.
func pairExactNumbers(resolved, decoded any) (any, bool) {
	switch v := resolved.(type) {
	case *jsonPlaceholder:
		// Placeholder nodes are immutable once compiled, so both documents point
		// at the same one.
		return v, false

	case map[string]any:
		exact := make(map[string]any, len(v))
		source, _ := decoded.(map[string]any)
		found := false
		for key, child := range v {
			node, childFound := pairExactNumbers(child, source[key])
			found = found || childFound
			exact[key] = node
		}
		return exact, found

	case []any:
		exact := make([]any, len(v))
		source, _ := decoded.([]any)
		found := false
		for i, child := range v {
			var counterpart any
			if i < len(source) {
				counterpart = source[i]
			}
			node, childFound := pairExactNumbers(child, counterpart)
			found = found || childFound
			exact[i] = node
		}
		return exact, found

	case float64:
		d, ok := decoded.(jsonDecimal)
		if !ok {
			// Unreachable while the two decodes read the same text; falling back
			// to the float keeps the exact pass no stricter than the one that
			// already accepted the document.
			return v, false
		}
		return &jsonPlaceholder{kind: phDecimal, decimal: d, source: string(d)}, true

	default:
		return resolved, false
	}
}
