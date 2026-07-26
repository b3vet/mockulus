// SPDX-License-Identifier: Apache-2.0

package regexx

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Stub patterns are written for java.util.regex, and three families of Java
// syntax do not survive the trip to RE2 or to .NET intact:
//
//   - constructs both engines accept with a different meaning — `\h` is a
//     horizontal-whitespace class in Java and a literal 'h' to .NET, `\p{Digit}`
//     is US-ASCII [0-9] in Java and Unicode Nd to RE2;
//   - constructs neither engine parses although an exact equivalent exists —
//     possessive quantifiers, which .NET spells as an atomic group;
//   - constructs with no equivalent at all — character-class intersection.
//
// The first two are rewritten here, before either engine sees the pattern. The
// third is refused at registration, because a stub that matches something other
// than what its author wrote is the failure P3 exists to prevent, and a 422 is
// the better half of that trade.
//
// Every set below was measured against the pinned WireMock rather than copied
// from the javadoc, one code point at a time.

// runeRange is an inclusive code-point range. Classes are held as ranges so the
// negated form is derived rather than written out a second time that can drift.
type runeRange struct{ lo, hi rune }

// javaPOSIXClasses are java.util.regex's POSIX classes, which are US-ASCII
// whatever alphabet the subject is in. RE2 happens to accept three of these
// names — Digit, Punct and Cntrl — with Unicode meanings, so leaving them alone
// would be the accept-and-behave-differently case rather than a missing feature.
var javaPOSIXClasses = map[string][]runeRange{
	"Lower":  {{'a', 'z'}},
	"Upper":  {{'A', 'Z'}},
	"ASCII":  {{0x00, 0x7F}},
	"Alpha":  {{'A', 'Z'}, {'a', 'z'}},
	"Digit":  {{'0', '9'}},
	"Alnum":  {{'0', '9'}, {'A', 'Z'}, {'a', 'z'}},
	"Punct":  {{0x21, 0x2F}, {0x3A, 0x40}, {0x5B, 0x60}, {0x7B, 0x7E}},
	"Graph":  {{0x21, 0x7E}},
	"Print":  {{0x20, 0x7E}},
	"Blank":  {{0x09, 0x09}, {0x20, 0x20}},
	"Cntrl":  {{0x00, 0x1F}, {0x7F, 0x7F}},
	"XDigit": {{'0', '9'}, {'A', 'F'}, {'a', 'f'}},
	"Space":  {{0x09, 0x0D}, {0x20, 0x20}},
}

// javaLetterClasses are the single-letter classes Java defines, keyed by the
// lower-case spelling; the upper-case spelling is the complement. `\h` and `\v`
// are the dangerous pair — .NET reads `\h` as a literal 'h' and `\v` as the
// single vertical-tab character, where Java means a class in both cases. `\s`
// is here for one code point: Java's includes U+000B and RE2's does not.
var javaLetterClasses = map[byte][]runeRange{
	'h': {{0x09, 0x09}, {0x20, 0x20}, {0xA0, 0xA0}, {0x1680, 0x1680},
		{0x180E, 0x180E}, {0x2000, 0x200A}, {0x202F, 0x202F},
		{0x205F, 0x205F}, {0x3000, 0x3000}},
	'v': {{0x0A, 0x0D}, {0x85, 0x85}, {0x2028, 0x2029}},
	's': {{0x09, 0x0D}, {0x20, 0x20}},
}

// translateJava rewrites a Java pattern into syntax RE2 and .NET read the same
// way Java does, and reports the constructs that have no faithful equivalent.
// It runs before either engine, not just before the fallback, because some of
// the divergent constructs are ones RE2 accepts with the wrong meaning.
func translateJava(source string) (string, error) {
	// Nothing else can introduce a rewrite or a refusal, so a pattern without
	// any of these three characters is handed back without touching a byte
	// (P2: a stub using no Java-only syntax pays nothing for this).
	if !strings.ContainsAny(source, `\[+`) {
		return source, nil
	}
	t := javaTranslator{src: source, out: make([]byte, 0, len(source)+16), atom: -1}
	if err := t.run(); err != nil {
		return "", err
	}
	if !t.changed {
		return source, nil
	}
	return string(t.out), nil
}

// javaTranslator is a single left-to-right pass. It is a hand-written scanner
// rather than a set of replacements because every rewrite here depends on
// context: `+` is possessive only after a quantifier, `&&` means intersection
// only inside a character class, and neither is true inside `\Q…\E`.
type javaTranslator struct {
	src string
	out []byte

	// atom is the offset in out where the most recently emitted quantifiable
	// atom begins, or -1 when a quantifier at this point would be a syntax
	// error anyway. A possessive quantifier is rewritten by wrapping that atom,
	// so the offset has to cover the whole of it: the atom of `\x41++` is the
	// four characters `\x41`, not the trailing `1`.
	atom int

	// groups is the stack of offsets where each currently open group begins,
	// which is how a group becomes the atom of a quantifier that follows it.
	groups []int

	changed bool

	// err records a construct the scan can only reject once it has already
	// emitted the atom it applies to. quantifierSuffix is the case: it cannot
	// know a possessive is illegal until it sees what follows.
	err error
}

// maxTranslatedBytes bounds what a rewrite may produce.
//
// Expansion is inherent rather than accidental: `\H` is the complement of
// horizontal whitespace across Unicode and comes out as a few hundred bytes of
// explicit ranges, and a POSIX class is not much smaller. That is fine for a
// pattern a person wrote, and not fine for one machine-generated from a
// template — a stub body of repeated class escapes would otherwise turn a few
// kilobytes of admin request into hundreds of megabytes of compiled pattern,
// held in the snapshot for as long as the stub lives.
//
// The bound is on the output rather than a ratio to the input, because the
// ratio for a legitimate two-character escape is already a hundredfold and any
// ratio loose enough to admit it admits the attack too.
const maxTranslatedBytes = 1 << 20

func (t *javaTranslator) run() error {
	for i := 0; i < len(t.src); {
		if t.err != nil {
			return t.err
		}
		if len(t.out) > maxTranslatedBytes {
			return fmt.Errorf(
				"the pattern expands past %d bytes once its Java-only syntax is written out; "+
					"the class escapes in it cover most of Unicode", maxTranslatedBytes)
		}
		switch c := t.src[i]; c {
		case '\\':
			next, err := t.escape(i)
			if err != nil {
				return err
			}
			i = next

		case '[':
			next, err := t.class(i)
			if err != nil {
				return err
			}
			i = next

		case '(':
			t.groups = append(t.groups, len(t.out))
			t.out = append(t.out, '(')
			i++
			// The `?` opening a non-capturing group, a lookaround or an inline
			// flag is part of the group header, not a quantifier.
			if i < len(t.src) && t.src[i] == '?' {
				t.out = append(t.out, '?')
				i++
			}
			t.atom = -1

		case ')':
			t.out = append(t.out, ')')
			i++
			if n := len(t.groups); n > 0 {
				t.atom = t.groups[n-1]
				t.groups = t.groups[:n-1]
			} else {
				t.atom = -1
			}

		case '*', '+', '?':
			t.out = append(t.out, c)
			i = t.quantifierSuffix(i + 1)

		case '{':
			if end, ok := countedClosure(t.src, i); ok {
				t.out = append(t.out, t.src[i:end]...)
				i = t.quantifierSuffix(end)
				break
			}
			// A brace that is not a well-formed counted closure is a literal to
			// both engines; leave it to them to say so.
			t.out = append(t.out, '{')
			t.atom = len(t.out) - 1
			i++

		case '|', '^', '$':
			t.out = append(t.out, c)
			t.atom = -1
			i++

		default:
			// A literal character, which is one atom however many bytes its
			// UTF-8 encoding takes.
			w := runeWidth(t.src, i)
			t.atom = len(t.out)
			t.out = append(t.out, t.src[i:i+w]...)
			i += w
		}
	}
	return t.err
}

// quantifierSuffix consumes the lazy or possessive marker that may follow a
// quantifier. `X++` is Java's possessive form and .NET's atomic group `(?>X+)`
// is the same thing: match greedily and refuse to give any of it back.
func (t *javaTranslator) quantifierSuffix(i int) int {
	if i < len(t.src) {
		switch t.src[i] {
		case '+':
			if t.atom >= 0 {
				t.makeAtomic()
				t.atom = -1
				// Java takes no quantifier on a possessive one: `a*+*` and
				// `a++?` are syntax errors there, and pinned WireMock rejects
				// both with a 422. .NET would happily quantify the atomic group
				// this just emitted, so accepting them would mean registering a
				// stub that cannot exist on the server we claim compatibility
				// with — and silently giving it a meaning Java never assigned.
				if next := i + 1; next < len(t.src) {
					switch t.src[next] {
					case '*', '+', '?', '{':
						t.err = fmt.Errorf(
							"a quantifier cannot follow a possessive quantifier (at offset %d); "+
								"Java rejects this and so does WireMock", next)
					}
				}
				return i + 1
			}
		case '?':
			t.out = append(t.out, '?')
			t.atom = -1
			return i + 1
		}
	}
	t.atom = -1
	return i
}

// makeAtomic wraps everything emitted since the start of the current atom in an
// atomic group.
func (t *javaTranslator) makeAtomic() {
	const open = "(?>"
	n := len(t.out)
	t.out = append(t.out, open...)
	copy(t.out[t.atom+len(open):], t.out[t.atom:n])
	copy(t.out[t.atom:], open)
	t.out = append(t.out, ')')
	t.changed = true
}

// escape handles a backslash sequence outside a character class.
func (t *javaTranslator) escape(i int) (int, error) {
	end := escapeEnd(t.src, i)
	esc := t.src[i:end]

	switch {
	case strings.HasPrefix(esc, `\Q`):
		t.writeQuoted(quotedText(esc))

	case esc == `\R`:
		// Java's any-line-break: a CRLF pair, or any single terminator. The
		// group keeps a following quantifier bound to the whole sequence.
		t.atom = len(t.out)
		t.out = append(t.out, "(?:"...)
		writeClassRune(&t.out, 0x0D)
		writeClassRune(&t.out, 0x0A)
		t.out = append(t.out, '|', '[')
		writeRanges(&t.out, javaLetterClasses['v'])
		t.out = append(t.out, ']', ')')
		t.changed = true

	default:
		if rs, ok := javaClassEscape(esc); ok {
			t.atom = len(t.out)
			t.out = append(t.out, '[')
			writeRanges(&t.out, rs)
			t.out = append(t.out, ']')
			t.changed = true
			break
		}
		if s, ok := javaScriptProperty(esc); ok {
			t.atom = len(t.out)
			t.out = append(t.out, s...)
			t.changed = true
			break
		}
		if r, ok := javaOctalEscape(esc); ok {
			t.atom = len(t.out)
			writeClassRune(&t.out, r)
			t.changed = true
			break
		}
		t.atom = len(t.out)
		t.out = append(t.out, esc...)
	}
	return end, nil
}

// quotedText returns the literal text of a `\Q…\E` block. escapeEnd has already
// found where the block ends, which for an unterminated one is the end of the
// pattern — the reading Java takes as well.
func quotedText(esc string) string {
	if body, found := strings.CutSuffix(esc[2:], `\E`); found {
		return body
	}
	return esc[2:]
}

// writeQuoted emits the contents of a `\Q…\E` block as ordinary pattern text.
// Neither engine reads such a block the way Java does: RE2 does not recognise
// it inside a character class, where it stops the class at the first quoted
// `]`, and the fallback engine either refuses `\Q` outright or takes it for a
// literal Q. So the quoting is resolved here and no `\Q` reaches either engine.
// Each character is emitted in the form both engines take literally in either
// context, which is what makes the result safe to splice into the brackets the
// block was written inside.
func (t *javaTranslator) writeQuoted(text string) {
	for i := 0; i < len(text); {
		t.atom = len(t.out)
		if c := text[i]; c < utf8.RuneSelf {
			writeClassRune(&t.out, rune(c))
			i++
			continue
		}
		// No byte of a multi-byte encoding can collide with regex syntax, so
		// the encoding is copied through rather than decoded and re-rendered:
		// ill-formed input stays byte for byte what its author wrote.
		w := runeWidth(text, i)
		t.out = append(t.out, text[i:i+w]...)
		i += w
	}
	t.changed = true
}

// javaOctalEscape resolves Java's octal escape, `\0` followed by one to three
// octal digits. RE2 spells the same thing `\0` plus at most two digits, so
// `\0101` is 'A' to Java and a backspace followed by '1' to both engines.
func javaOctalEscape(esc string) (rune, bool) {
	if len(esc) < 3 || esc[1] != '0' {
		return 0, false
	}
	var r rune
	for i := 2; i < len(esc); i++ {
		r = r*8 + rune(esc[i]-'0')
	}
	return r, true
}

// class copies a character class, translating the escapes inside it and
// refusing the two forms whose Java meaning cannot be reproduced.
func (t *javaTranslator) class(i int) (int, error) {
	start := len(t.out)
	t.out = append(t.out, '[')
	i++
	if i < len(t.src) && t.src[i] == '^' {
		t.out = append(t.out, '^')
		i++
	}

	for i < len(t.src) {
		switch c := t.src[i]; {
		case c == ']':
			t.out = append(t.out, ']')
			t.atom = start
			return i + 1, nil

		case c == '&' && i+1 < len(t.src) && t.src[i+1] == '&':
			// Java intersects the operands; .NET has no intersection syntax and
			// reads the ampersands as two more members of the class, which is a
			// class that matches more than it was meant to rather than less.
			return 0, fmt.Errorf(
				"character-class intersection (&& at offset %d) has no equivalent in either regex engine; write the resulting class out explicitly", i)

		case c == '[':
			// Java unions a nested class into its parent, so `[a[bc]]` is
			// [abc] and `[[:alpha:]]` is the five characters of ":alpha".
			// Both engines read the bracket as an ordinary member instead.
			return 0, fmt.Errorf(
				"a nested character class ([ at offset %d) has no equivalent in either regex engine; write the union out as a single class", i)

		case c == '\\':
			next, err := t.classEscape(i)
			if err != nil {
				return 0, err
			}
			i = next

		default:
			t.out = append(t.out, c)
			i++
		}
	}

	// An unterminated class: emit what there is and let the engine produce the
	// diagnostic, which names the position better than anything here could.
	t.atom = start
	return i, nil
}

// classEscape handles a backslash sequence inside a character class, where a
// class-valued escape contributes its members rather than a bracketed class.
func (t *javaTranslator) classEscape(i int) (int, error) {
	end := escapeEnd(t.src, i)
	esc := t.src[i:end]

	if strings.HasPrefix(esc, `\Q`) {
		// A quoted `]` must not be allowed to close the class, which is what
		// both engines would do with the block left as written.
		t.writeQuoted(quotedText(esc))
		return end, nil
	}
	if esc == `\R` {
		// Java rejects it here too: `\R` can match two characters, so it is not
		// something a class can hold. Both engines would take it as a literal R.
		return 0, fmt.Errorf(`\R (at offset %d) matches a sequence, so it has no meaning inside a character class`, i)
	}
	if rs, ok := javaClassEscape(esc); ok {
		writeRanges(&t.out, rs)
		t.changed = true
		return end, nil
	}
	if s, ok := javaScriptProperty(esc); ok {
		t.out = append(t.out, s...)
		t.changed = true
		return end, nil
	}
	if r, ok := javaOctalEscape(esc); ok {
		writeClassRune(&t.out, r)
		t.changed = true
		return end, nil
	}
	t.out = append(t.out, esc...)
	return end, nil
}

// javaClassEscape resolves an escape that denotes a character class in Java,
// returning the code points it stands for with the negation already applied.
func javaClassEscape(esc string) ([]runeRange, bool) {
	if len(esc) == 2 {
		c := esc[1]
		rs, ok := javaLetterClasses[c|0x20]
		if !ok {
			return nil, false
		}
		if c >= 'A' && c <= 'Z' {
			return complementRanges(rs), true
		}
		return rs, true
	}
	name, negated, ok := propertyName(esc)
	if !ok {
		return nil, false
	}
	rs, ok := javaPOSIXClasses[name]
	if !ok {
		return nil, false
	}
	if negated {
		return complementRanges(rs), true
	}
	return rs, true
}

// javaScriptProperty rewrites Java's spelling of a Unicode script — `\p{IsLatin}`
// — into the spelling both engines use. Names that are not scripts are left
// alone: `\p{InGreek}` is a Unicode *block*, and `\p{IsAlphabetic}` a binary
// property, neither of which either engine has a table for, so they keep
// failing loudly instead of being guessed at.
func javaScriptProperty(esc string) (string, bool) {
	name, negated, ok := propertyName(esc)
	if !ok {
		return "", false
	}
	rest, found := strings.CutPrefix(name, "Is")
	if !found {
		return "", false
	}
	if _, isScript := unicode.Scripts[rest]; !isScript {
		return "", false
	}
	if negated {
		return `\P{` + rest + `}`, true
	}
	return `\p{` + rest + `}`, true
}

// propertyName splits `\p{Name}` or `\P{Name}` into its parts.
func propertyName(esc string) (name string, negated bool, ok bool) {
	if len(esc) < 5 || esc[0] != '\\' || (esc[1] != 'p' && esc[1] != 'P') {
		return "", false, false
	}
	if esc[2] != '{' || esc[len(esc)-1] != '}' {
		return "", false, false
	}
	return esc[3 : len(esc)-1], esc[1] == 'P', true
}

// escapeEnd returns the index one past the escape sequence beginning at i,
// where src[i] is a backslash. The whole sequence has to be delimited even for
// escapes nothing here rewrites, because the possessive rewrite wraps the atom
// a quantifier applies to and an escape is one atom however many bytes it runs.
func escapeEnd(src string, i int) int {
	j := i + 1
	if j >= len(src) {
		return j
	}
	c := src[j]
	j++

	switch c {
	case 'Q':
		if k := strings.Index(src[j:], `\E`); k >= 0 {
			return j + k + 2
		}
		return len(src)

	case 'p', 'P', 'N':
		if j < len(src) && src[j] == '{' {
			if k := strings.IndexByte(src[j:], '}'); k >= 0 {
				return j + k + 1
			}
			return len(src)
		}
		return j + runeWidth(src, j)

	case 'x':
		if j < len(src) && src[j] == '{' {
			if k := strings.IndexByte(src[j:], '}'); k >= 0 {
				return j + k + 1
			}
			return len(src)
		}
		return j + digitRun(src, j, 2, isHexDigit)

	case 'u':
		return j + digitRun(src, j, 4, isHexDigit)

	case 'c':
		return j + runeWidth(src, j)

	case 'k':
		if j < len(src) && src[j] == '<' {
			if k := strings.IndexByte(src[j:], '>'); k >= 0 {
				return j + k + 1
			}
		}
		return j

	case '0':
		// \0n, \0nn or \0mnn, where m is at most 3 because the value is a
		// byte: `\0777` is `\077` followed by a literal 7.
		n := digitRun(src, j, 3, isOctalDigit)
		if n == 3 && src[j] > '3' {
			n = 2
		}
		return j + n

	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		// A group reference, which Java lets run to as many digits as there are
		// capturing groups.
		return j + digitRun(src, j, len(src), isDecimalDigit)

	default:
		return i + 1 + runeWidth(src, i+1)
	}
}

func runeWidth(src string, i int) int {
	if i >= len(src) {
		return 0
	}
	_, w := utf8.DecodeRuneInString(src[i:])
	return w
}

func digitRun(src string, i, max int, is func(byte) bool) int {
	n := 0
	for i+n < len(src) && n < max && is(src[i+n]) {
		n++
	}
	return n
}

func isDecimalDigit(c byte) bool { return c >= '0' && c <= '9' }
func isOctalDigit(c byte) bool   { return c >= '0' && c <= '7' }
func isHexDigit(c byte) bool {
	return isDecimalDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// countedClosure reports the end of a well-formed {n}, {n,} or {n,m} at i.
func countedClosure(src string, i int) (int, bool) {
	j := i + 1
	n := digitRun(src, j, len(src), isDecimalDigit)
	if n == 0 {
		return 0, false
	}
	j += n
	if j < len(src) && src[j] == ',' {
		j++
		j += digitRun(src, j, len(src), isDecimalDigit)
	}
	if j < len(src) && src[j] == '}' {
		return j + 1, true
	}
	return 0, false
}

// closureMinIsZero reports whether the counted closure at i can take none of
// the atom before it, which is what makes that atom optional.
func closureMinIsZero(src string, i int) bool {
	if _, ok := countedClosure(src, i); !ok {
		return false
	}
	j := i + 1
	for j < len(src) && src[j] == '0' {
		j++
	}
	return j > i+1 && j < len(src) && (src[j] == ',' || src[j] == '}')
}

// complementRanges returns the ranges a negated class covers. Java's negated
// forms complement over the whole code-point space, and deriving them keeps one
// definition of each class instead of two that can drift apart. The input must
// be sorted and disjoint, which javaClassRangesAreWellFormed asserts.
func complementRanges(rs []runeRange) []runeRange {
	out := make([]runeRange, 0, len(rs)+1)
	var next rune
	for _, r := range rs {
		if r.lo > next {
			out = append(out, runeRange{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, runeRange{next, unicode.MaxRune})
	}
	return out
}

// writeRanges emits ranges as the body of a character class.
func writeRanges(out *[]byte, rs []runeRange) {
	for _, r := range rs {
		writeClassRune(out, r.lo)
		if r.hi != r.lo {
			*out = append(*out, '-')
			writeClassRune(out, r.hi)
		}
	}
}

// writeClassRune emits one code point in a form both engines read identically
// inside a character class: alphanumerics as themselves, so a translated class
// still reads like one, and everything else as \x{…}, which sidesteps the
// question of which characters need escaping where.
func writeClassRune(out *[]byte, r rune) {
	if r < utf8.RuneSelf && (isDecimalDigit(byte(r)) ||
		(r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
		*out = append(*out, byte(r))
		return
	}
	*out = append(*out, fmt.Sprintf(`\x{%02X}`, r)...)
}
