// SPDX-License-Identifier: Apache-2.0

package regexx

import (
	"strings"
	"testing"
	"time"
)

// The translation only earns its keep if the rewritten pattern matches exactly
// what java.util.regex matches, so the cases below are stated as subjects and
// verdicts rather than as expected output text. Every verdict was measured
// against the pinned WireMock.
func TestJavaConstructsMatchWhatJavaMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		match   []string
		reject  []string
	}{{
		name:    "horizontal whitespace",
		pattern: `\h+`,
		match:   []string{" ", "\t", " ", " ", "᠎", " ", " ", " ", " ", "　", " \t"},
		reject:  []string{"h", "hh", "\n", "​", " ", ""},
	}, {
		name:    "non-horizontal whitespace",
		pattern: `\H+`,
		match:   []string{"abc", "H", "\n", "​"},
		reject:  []string{" ", "\t", "　"},
	}, {
		name:    "vertical whitespace",
		pattern: `\v`,
		match:   []string{"\n", "\v", "\f", "\r", "", " ", " "},
		reject:  []string{"v", " ", " "},
	}, {
		name:    "non-vertical whitespace",
		pattern: `\V+`,
		match:   []string{"abc", "V", " "},
		reject:  []string{"\n", " "},
	}, {
		name:    "any line break",
		pattern: `\R`,
		match:   []string{"\r\n", "\n", "\v", "\f", "\r", "", " ", " "},
		reject:  []string{"R", "\n\r", "ab"},
	}, {
		name:    "line break is one atom under a quantifier",
		pattern: `a\R+b`,
		match:   []string{"a\r\nb", "a\n\nb", "a\r\n b"},
		reject:  []string{"aRb", "ab"},
	}, {
		// Java's \s carries U+000B where RE2's does not.
		name:    "whitespace includes the vertical tab",
		pattern: `\s`,
		match:   []string{" ", "\t", "\n", "\v", "\f", "\r"},
		reject:  []string{" ", " ", " ", "s"},
	}, {
		name:    "non-whitespace excludes the vertical tab",
		pattern: `\S`,
		match:   []string{"a", " ", " "},
		reject:  []string{" ", "\v", "\n"},
	}, {
		name:    "POSIX alpha is US-ASCII",
		pattern: `\p{Alpha}+`,
		match:   []string{"abc", "Z", "aZ"},
		reject:  []string{"5", "é", "α", ""},
	}, {
		name:    "POSIX digit is US-ASCII",
		pattern: `\p{Digit}+`,
		match:   []string{"123"},
		reject:  []string{"٣", "５", "a"},
	}, {
		name:    "POSIX punct is US-ASCII punctuation",
		pattern: `\p{Punct}+`,
		match:   []string{`!"#$%&'()*+,-./`, ":;<=>?@", "[\\]^_`", "{|}~"},
		reject:  []string{"«", "。", "—", "a", "5"},
	}, {
		name:    "POSIX cntrl excludes the C1 controls",
		pattern: `\p{Cntrl}`,
		match:   []string{"\x00", "\x1f", "\x7f"},
		reject:  []string{"", "", " "},
	}, {
		name:    "POSIX space",
		pattern: `\p{Space}+`,
		match:   []string{" ", "\t", "\n", "\v", "\f", "\r"},
		reject:  []string{" ", " ", "S"},
	}, {
		name:    "POSIX blank",
		pattern: `\p{Blank}+`,
		match:   []string{" ", "\t", " \t"},
		reject:  []string{"\n", " "},
	}, {
		name:    "POSIX xdigit",
		pattern: `\p{XDigit}+`,
		match:   []string{"1aF", "deadBEEF"},
		reject:  []string{"g", "٣"},
	}, {
		name:    "POSIX graph and print differ by the space",
		pattern: `\p{Print}\p{Graph}`,
		match:   []string{" a", "a!"},
		reject:  []string{"a ", "\t!"},
	}, {
		name:    "POSIX alnum",
		pattern: `\p{Alnum}+`,
		match:   []string{"a1", "Z9"},
		reject:  []string{"_", "é", "a-1"},
	}, {
		name:    "POSIX upper and lower",
		pattern: `\p{Upper}\p{Lower}`,
		match:   []string{"Ab"},
		reject:  []string{"aB", "ÉA", "AÉ"},
	}, {
		name:    "POSIX ascii",
		pattern: `\p{ASCII}+`,
		match:   []string{"abc", "\x00", "\x7f"},
		reject:  []string{"é"},
	}, {
		name:    "negated POSIX class",
		pattern: `\P{Alpha}+`,
		match:   []string{"5", "!", "é", "5!"},
		reject:  []string{"a", "Z", "a5"},
	}, {
		name:    "POSIX class inside a character class",
		pattern: `[\p{Alpha}0-9]+`,
		match:   []string{"a1", "Z", "9"},
		reject:  []string{"!", "é"},
	}, {
		name:    "negated POSIX class inside a character class",
		pattern: `[\P{Alpha}]+`,
		match:   []string{"5", "!", "é"},
		reject:  []string{"a", "Z"},
	}, {
		name:    "letter class inside a character class",
		pattern: `[\h,]+`,
		match:   []string{" ", "\t", ",", " ,　"},
		reject:  []string{"h", "\n"},
	}, {
		name:    "negated letter class inside a character class",
		pattern: `[\H]+`,
		match:   []string{"abc", "H", "\n"},
		reject:  []string{" ", "　"},
	}, {
		name:    "letter class inside a negated character class",
		pattern: `[^\h]+`,
		match:   []string{"abc", "h", "\n"},
		reject:  []string{" ", "\t"},
	}, {
		name:    "script property",
		pattern: `\p{IsLatin}+`,
		match:   []string{"abc", "Z", "é"},
		reject:  []string{"α", "漢", "5"},
	}, {
		name:    "negated script property",
		pattern: `\P{IsGreek}+`,
		match:   []string{"abc", "5"},
		reject:  []string{"α"},
	}, {
		name:    "possessive plus",
		pattern: `a++b`,
		match:   []string{"aab", "ab"},
		reject:  []string{"b", "aa"},
	}, {
		name:    "possessive star",
		pattern: `a*+b`,
		match:   []string{"aab", "ab", "b"},
		reject:  []string{"a"},
	}, {
		name:    "possessive question mark",
		pattern: `a?+b`,
		match:   []string{"ab", "b"},
		reject:  []string{"aab"},
	}, {
		name:    "possessive counted closure",
		pattern: `a{1,3}+b`,
		match:   []string{"ab", "aaab"},
		reject:  []string{"b", "aaaab"},
	}, {
		name:    "possessive on a group",
		pattern: `(?:ab)++c`,
		match:   []string{"abc", "ababc"},
		reject:  []string{"c", "ab"},
	}, {
		name:    "possessive on a character class",
		pattern: `[ab]++c`,
		match:   []string{"abc", "bc"},
		reject:  []string{"ab", "c"},
	}, {
		name:    "possessive on a backreference",
		pattern: `(a)\1++b`,
		match:   []string{"aab", "aaab"},
		reject:  []string{"ab"},
	}, {
		// The atomic group must actually refuse to give ground: `a++a` can
		// never match, because the possessive run swallows every a.
		name:    "possessive really is atomic",
		pattern: `a++a`,
		match:   nil,
		reject:  []string{"aa", "aaa", "a"},
	}, {
		// A quote block is literal text, and every character in it has to
		// stay a member of the class it was written inside rather than
		// closing the class or spanning a range.
		name:    "a quote block inside a class contributes members",
		pattern: `[\Qa-z\E]+`,
		match:   []string{"a", "-", "z", "a-z"},
		reject:  []string{"b", "Q", "E"},
	}, {
		name:    "a quoted bracket does not close the class",
		pattern: `[a\Qb]c\Ed]+`,
		match:   []string{"ab]cd", "]", "d"},
		reject:  []string{"e", "Q"},
	}, {
		name:    "a quote block is literal outside a class too",
		pattern: `\Q[a&&b]\E`,
		match:   []string{"[a&&b]"},
		reject:  []string{"a", "b", "[ab]"},
	}, {
		// Java applies the quantifier to the last character of the block,
		// not to the block as a whole.
		name:    "a quantifier after a quote block binds its last character",
		pattern: `\Qab\E++`,
		match:   []string{"ab", "abb"},
		reject:  []string{"abab", "a"},
	}, {
		// Java's octal escape takes up to three digits after the 0, where RE2
		// reads only two — `\0101` is 'A' to one and a backspace to the other.
		name:    "a three-digit octal escape",
		pattern: `\0101`,
		match:   []string{"A"},
		reject:  []string{"\x081", "\x08"},
	}, {
		name:    "an octal escape inside a class",
		pattern: `[\0101]+`,
		match:   []string{"A", "AA"},
		reject:  []string{"1", "\x08"},
	}, {
		// 0377 is the largest value the three-digit form can hold, so the
		// fourth digit of `\0777` is a literal.
		name:    "an octal value stops at 0377",
		pattern: `\0777`,
		match:   []string{"?7"},
		reject:  []string{"ǿ"},
	}, {
		// The fallback engine has no `\Q` at all, so a quote block alongside a
		// construct only that engine accepts has to be resolved before it gets
		// there or the stub matches nothing.
		name:    "a quote block survives the trip to the fallback engine",
		pattern: `x\Qa+\E(?=b)b`,
		match:   []string{"xa+b"},
		reject:  []string{"xab", "xa+"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Compile(c.pattern, Options{Anchored: true, Timeout: time.Second})
			if err != nil {
				t.Fatalf("compile %q: %v", c.pattern, err)
			}
			for _, s := range c.match {
				if !p.MatchString(s) {
					t.Errorf("%q should match %q", c.pattern, s)
				}
			}
			for _, s := range c.reject {
				if p.MatchString(s) {
					t.Errorf("%q should not match %q", c.pattern, s)
				}
			}
		})
	}
}

// A rewrite is only safe if it fires exactly where Java would read the
// construct and nowhere else. These are the places it must keep its hands off.
func TestTranslationRespectsEscapingAndContext(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    string
	}{
		{"an escaped backslash is not an escape", `\\h`, `\\h`},
		{"an escaped backslash before a possessive", `\\++`, `(?>\\+)`},
		{"a literal plus quantified is not possessive", `a\++b`, `a\++b`},
		{"a lazy quantifier is left alone", `a+?b`, `a+?b`},
		{"nested groups are not possessive", `(a+)+b`, `(a+)+b`},
		{"a plus inside a class is a member", `[+]+a`, `[+]+a`},
		{"an escaped h is still a literal h", `\\\h`, `\\[\x{09}\x{20}\x{A0}\x{1680}\x{180E}\x{2000}-\x{200A}\x{202F}\x{205F}\x{3000}]`},
		{"a hex escape is one atom", `\x41++`, `(?>\x41+)`},
		{"a braced hex escape is one atom", `\x{1F600}++`, `(?>\x{1F600}+)`},
		{"a unicode escape is one atom", `A++`, `(?>A+)`},
		{"an octal escape is one atom", `\0101++`, `(?>A+)`},
		{"a three-digit octal escape is one code point", `\0101`, `A`},
		{"an octal value stops at 0377", `\0777`, `\x{3F}7`},
		{"a control escape is one atom", `\cA++`, `(?>\cA+)`},
		{"a property escape is one atom", `\p{Lu}++`, `(?>\p{Lu}+)`},
		{"a group is the atom of a possessive", `(ab)++`, `(?>(ab)+)`},
		{"a class is the atom of a possessive", `[a-z]++`, `(?>[a-z]+)`},
		{"a multi-byte literal is one atom", `é++`, `(?>é+)`},
		{"a quote block becomes the literals it stands for", `\Qa++b\E`, `a\x{2B}\x{2B}b`},
		{"a quote block hides a class escape", `\Q\h\E`, `\x{5C}h`},
		{"a quote block hides an intersection", `\Q[a&&b]\E`, `\x{5B}a\x{26}\x{26}b\x{5D}`},
		{"a quote block inside a class hides an intersection", `[\Qa&&b\E]`, `[a\x{26}\x{26}b]`},
		{"a quote block inside a class hides a bracket", `[\Q[]\E]`, `[\x{5B}\x{5D}]`},
		{"a quote block inside a class hides a range dash", `[\Qa-z\E]`, `[a\x{2D}z]`},
		{"a quoted bracket does not close the class", `[a\Qb]c\Ed]`, `[ab\x{5D}cd]`},
		{"a quantifier after a quote block binds its last character", `\Qab\E++`, `a(?>b+)`},
		{"a quote block keeps multi-byte text as written", `\Qé+\E`, `é\x{2B}`},
		{"an open group is not moved by an atomic wrap", `(a(b)++)`, `(a(?>(b)+))`},
		{"an alternation branch is its own atom", `a|b++`, `a|(?>b+)`},
		{"an unterminated quote block runs to the end", `\Qa++`, `a\x{2B}\x{2B}`},
		{"ampersands outside a class are literal", `a&&b`, `a&&b`},
		{"escaped ampersands in a class are literal", `[a\&\&b]`, `[a\&\&b]`},
		{"a single ampersand in a class is literal", `[a&b]`, `[a&b]`},
		{"a group header question mark is not a quantifier", `(?:a)`, `(?:a)`},
		{"a lookahead is left alone", `(?=.*bar)foo.*`, `(?=.*bar)foo.*`},
		{"an atomic group is already atomic", `(?>a|ab)c`, `(?>a|ab)c`},
		{"a named group header survives", `(?<n>a)+`, `(?<n>a)+`},
		{"a brace that is not a closure is a literal", `a{x}+`, `a{x}+`},
		{"a dangling plus is left for the engine", `|+`, `|+`},
		{"a trailing backslash is left for the engine", `a\`, `a\`},
		{"an escaped bracket does not open a class", `\[a&&b\]`, `\[a&&b\]`},
		{"a bracket inside a class must be escaped", `[a\[b]`, `[a\[b]`},
		{"a class-valued escape is one atom", `\h++`, `(?>[\x{09}\x{20}\x{A0}\x{1680}\x{180E}\x{2000}-\x{200A}\x{202F}\x{205F}\x{3000}]+)`},
		{"a script rewrite is one atom", `\p{IsHan}++`, `(?>\p{Han}+)`},
		{"a block property is not a script", `\p{InGreek}`, `\p{InGreek}`},
		{"a binary property is not a script", `\p{IsAlphabetic}`, `\p{IsAlphabetic}`},
		{"a general category is left alone", `\p{Lu}`, `\p{Lu}`},
		{"a bare word needs no rewrite", `/api/orders`, `/api/orders`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := translateJava(c.pattern)
			if err != nil {
				t.Fatalf("translate %q: %v", c.pattern, err)
			}
			if got != c.want {
				t.Errorf("translate(%q)\n got %q\nwant %q", c.pattern, got, c.want)
			}
		})
	}
}

// The constructs with no faithful equivalent are refused at registration. The
// alternative is a stub that quietly matches a different set of subjects, which
// is the outcome the whole seam exists to avoid.
func TestUntranslatableConstructsAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		names   string
	}{
		{"intersection with a nested operand", `[a-z&&[^aeiou]]`, "intersection"},
		{"intersection between two ranges", `[a-z&&b-d]`, "intersection"},
		{"intersection between two escapes", `[\w&&[^\d]]`, "intersection"},
		{"a nested class union", `[a[bc]]`, "nested character class"},
		{"POSIX bracket syntax", `[[:alpha:]]`, "nested character class"},
		{"a line break inside a class", `[\R]`, `\R`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(c.pattern, Options{Anchored: true})
			if err == nil {
				t.Fatalf("%q must be refused, not read as something else", c.pattern)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("error %q does not name the construct (%q)", err, c.names)
			}
		})
	}
}

// A pattern with no Java-only syntax has to come out of the translator byte for
// byte, so that the two engines see exactly what the stub's author wrote.
func TestOrdinaryPatternsAreUntouched(t *testing.T) {
	for _, pattern := range []string{
		`/api/orders/[0-9]+`, `/api/.*`, `^abc$`, `a|b`, `\d{3}-\d{4}`,
		`(?i)HeLLo`, `[^/]+`, `\w+@\w+\.com`, `a{2,}`, `(a)(b)\2\1`,
	} {
		got, err := translateJava(pattern)
		if err != nil {
			t.Fatalf("translate %q: %v", pattern, err)
		}
		if got != pattern {
			t.Errorf("translate(%q) = %q, want it unchanged", pattern, got)
		}
	}
}

// `\Q` is the one construct the two engines disagree about in three ways at
// once — RE2 honours it outside a class and not inside one, the fallback engine
// honours it in neither — so the translation is only sound if none survives.
func TestNoQuoteBlockReachesAnEngine(t *testing.T) {
	for _, pattern := range []string{
		`\Qa++b\E`, `[\Qa-z\E]`, `[a\Qb]c\Ed]`, `\Qa++`, `\Q\E`, `x\Qa+\E(?=b)b`,
		`\Q` + "é" + `\E`, `[\Q\\E]`,
	} {
		got, err := translateJava(pattern)
		if err != nil {
			t.Fatalf("translate %q: %v", pattern, err)
		}
		if strings.Contains(got, `\Q`) || strings.Contains(got, `\E`) {
			t.Errorf("translate(%q) = %q still carries a quote block", pattern, got)
		}
	}
}

// Translating a Java class into an explicit one must not push a pattern off
// RE2: a class is a class to both engines, and the linear-time engine is what
// keeps a stub-supplied pattern safe on the hot path.
func TestTranslatedClassesStayOnRE2(t *testing.T) {
	for _, pattern := range []string{
		`\h+`, `\v`, `\R`, `\p{Alpha}+`, `\P{Digit}`, `[\p{Alnum}_]+`, `\p{IsLatin}+`,
	} {
		p, err := Compile(pattern, Options{Anchored: true})
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		if p.Engine != EngineRE2 {
			t.Errorf("%q compiled on %s; a character class does not need backtracking", pattern, p.Engine)
		}
	}
}

// A possessive quantifier is the construct SPEC §6.6 step 2 names as a reason
// the fallback exists, so it must register — on the fallback engine.
func TestPossessiveQuantifiersUseTheFallback(t *testing.T) {
	p, err := Compile(`a++b`, Options{Anchored: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("a possessive quantifier must register: %v", err)
	}
	if p.Engine != EngineBacktracking {
		t.Errorf("engine = %s, want %s — an atomic group is a backtracking construct", p.Engine, EngineBacktracking)
	}
	if p.Source() != `a++b` {
		t.Errorf("Source() = %q; diagnostics must quote the pattern as it was written", p.Source())
	}
}

// The negated forms are derived by complementing the positive ones, so the two
// have to partition the code-point space exactly. Sampling both sides of every
// range boundary is what catches an off-by-one in complementRanges.
func TestNegatedClassesAreExactComplements(t *testing.T) {
	patterns := map[string]string{
		`\h`: `\H`, `\v`: `\V`, `\s`: `\S`,
		`\p{Alpha}`: `\P{Alpha}`, `\p{Punct}`: `\P{Punct}`,
		`\p{Cntrl}`: `\P{Cntrl}`, `\p{Space}`: `\P{Space}`,
		`\p{ASCII}`: `\P{ASCII}`,
	}
	for pos, neg := range patterns {
		p := MustCompile(pos, Options{Anchored: true})
		n := MustCompile(neg, Options{Anchored: true})
		for _, r := range boundarySamples() {
			s := string(r)
			if p.MatchString(s) == n.MatchString(s) {
				t.Errorf("%s and %s both %v on U+%04X", pos, neg, p.MatchString(s), r)
			}
		}
	}
}

// boundarySamples covers every code point that starts or ends a range in any of
// the class tables, plus its neighbours, plus a spread of ordinary characters.
func boundarySamples() []rune {
	seen := map[rune]bool{}
	var out []rune
	add := func(r rune) {
		if r < 0 || r > 0x10FFFF || (r >= 0xD800 && r <= 0xDFFF) || seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	for _, rs := range javaPOSIXClasses {
		for _, r := range rs {
			add(r.lo - 1)
			add(r.lo)
			add(r.hi)
			add(r.hi + 1)
		}
	}
	for _, rs := range javaLetterClasses {
		for _, r := range rs {
			add(r.lo - 1)
			add(r.lo)
			add(r.hi)
			add(r.hi + 1)
		}
	}
	for _, r := range []rune{'a', 'Z', '5', '_', 'é', 'α', '漢', 0x10FFFF} {
		add(r)
	}
	return out
}

// The tables are the source of truth for both a class and its complement, and
// complementRanges only produces the right answer for input that is sorted and
// disjoint. Asserting it here means a future addition cannot go quietly wrong.
func TestClassTablesAreSortedAndDisjoint(t *testing.T) {
	check := func(name string, rs []runeRange) {
		var prev rune = -2
		for _, r := range rs {
			if r.lo > r.hi {
				t.Errorf("%s: range U+%04X-U+%04X is inverted", name, r.lo, r.hi)
			}
			if r.lo <= prev+1 {
				t.Errorf("%s: range starting U+%04X abuts or overlaps the one before", name, r.lo)
			}
			prev = r.hi
		}
	}
	for name, rs := range javaPOSIXClasses {
		check(`\p{`+name+`}`, rs)
	}
	for c, rs := range javaLetterClasses {
		check(`\`+string(rune(c)), rs)
	}
}

// A translated class is spliced into whatever brackets surrounded the escape,
// so the splice has to mean exactly the class however it is neighboured — a
// rendered code point that the enclosing class could read as a range dash, a
// negation or a terminator would silently widen or narrow the stub's criterion.
func TestSplicedClassesMeanExactlyTheClass(t *testing.T) {
	for name := range javaPOSIXClasses {
		alone := MustCompile(`\p{`+name+`}`, Options{Anchored: true})
		bracketed := MustCompile(`[\p{`+name+`}]`, Options{Anchored: true})
		doubled := MustCompile(`[\p{`+name+`}\p{`+name+`}]`, Options{Anchored: true})
		negated := MustCompile(`[^\p{`+name+`}]`, Options{Anchored: true})
		// A neighbour that is in no class table, so a splice that leaked syntax
		// into the surrounding brackets shows up as this character going missing.
		beside := MustCompile(`[\p{`+name+`}~]`, Options{Anchored: true})

		for _, r := range boundarySamples() {
			s := string(r)
			want := alone.MatchString(s)
			if bracketed.MatchString(s) != want {
				t.Errorf(`[\p{%s}] disagrees with \p{%s} on U+%04X`, name, name, r)
			}
			if doubled.MatchString(s) != want {
				t.Errorf(`[\p{%s}\p{%s}] disagrees with \p{%s} on U+%04X`, name, name, name, r)
			}
			if negated.MatchString(s) == want {
				t.Errorf(`[^\p{%s}] agrees with \p{%s} on U+%04X`, name, name, r)
			}
			if beside.MatchString(s) != (want || r == '~') {
				t.Errorf(`[\p{%s}~] is wrong on U+%04X`, name, r)
			}
		}
	}
}
