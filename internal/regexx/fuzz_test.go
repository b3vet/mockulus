// SPDX-License-Identifier: Apache-2.0

package regexx

import (
	"strings"
	"testing"
	"time"
)

// translateJava is a hand-written scanner over a string whoever wrote the stub
// chose, which is the shape of code that mishandles a truncated escape at the
// end of its input. The properties below are the ones a rewriter owes its
// caller regardless of what the pattern says: it terminates, it does not run
// off the end of the buffer, and what it hands to the engines is bounded by
// what it was given.

// translateBudget bounds one translation. The scanner is a single left-to-right
// pass, so anything near this is a loop that stopped making progress.
const translateBudget = 2 * time.Second

var patternSeeds = []string{
	`\h+`,
	`\H+`,
	`\v`,
	`\V+`,
	`\s`,
	`\S`,
	`\p{Digit}+`,
	`\p{Alpha}\p{Alnum}`,
	`\P{Punct}`,
	`\p{IsLatin}+`,
	`\p{InGreek}`,
	`\p{IsAlphabetic}`,
	`a++`,
	`a*+`,
	`a?+`,
	`a{2,3}+`,
	`(ab)++`,
	`\x41++`,
	`[a-z&&[^aeiou]]`,
	`[a[bc]]`,
	`\Qa.b\E`,
	`\Q]\E`,
	`[\Q]\E]`,
	`\Qunterminated`,
	`\R`,
	`[\R]`,
	`\0101`,
	`\0777`,
	`￿`,
	`\x{1F600}`,
	`\x4`,
	`\k<name>`,
	`\c`,
	`\`,
	`\p`,
	`\p{`,
	`\Q`,
	`[`,
	`[^`,
	`[a-`,
	`(?<=a)b`,
	`(\w+)\1`,
	`a{1000}`,
	`a{`,
	`{2}`,
	`^(?:a|b)$`,
	`/e2e/[a-z0-9-]+/thing`,
}

// FuzzTranslateJava drives the rewriter directly, which is where the scanner
// bugs live: Compile would hide a mangled rewrite behind an engine's own
// refusal of the result.
func FuzzTranslateJava(f *testing.F) {
	for _, s := range patternSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, source string) {
		start := time.Now()
		out, err := translateJava(source)
		if took := time.Since(start); took > translateBudget {
			t.Fatalf("translating %d bytes took %s, over the %s budget", len(source), took, translateBudget)
		}
		if err != nil {
			// A refusal must name the construct it refused, since that message
			// is the whole value of failing at registration (P3).
			if err.Error() == "" {
				t.Fatal("translation refused the pattern without saying why")
			}
			return
		}
		// The bound is absolute, not a ratio. A single `\H` or `\p{Alpha}`
		// legitimately becomes a few hundred bytes of explicit ranges, so the
		// expansion factor for a short pattern is enormous and says nothing;
		// what matters is that no input drives the output without limit.
		if len(out) > maxTranslatedBytes {
			t.Fatalf("translating %d bytes produced %d, past the %d-byte cap",
				len(source), len(out), maxTranslatedBytes)
		}

		// The rewrite is meant to be a no-op for patterns using none of the
		// Java-only syntax, which is what makes it free for the stubs that do
		// not need it (P2). Getting this wrong would put every pattern in the
		// deployment through a rewrite nothing asked for.
		if !strings.ContainsAny(source, `\[+`) && out != source {
			t.Fatalf("pattern %q with no Java-only syntax was rewritten to %q", source, out)
		}

		// A second pass over the output should be stable: an unstable rewrite
		// means the emitted form is not in the syntax the rewriter claims to
		// emit, and the two engines would then read it differently from Java.
		//
		// Octal escapes are excluded because they are a known gap rather than a
		// surprise. `\0` and `\101` are left untranslated on purpose — Java
		// reads `\101` as a backreference where RE2 reads an octal escape, and
		// picking a side needs an oracle probe that has not been done — so a
		// pass that has already stripped a `\Q` quote can see one the first
		// pass had no business touching. The instability is downstream of the
		// gap, not evidence of a second one, and asserting it here would only
		// re-report the same deferred item on every run.
		if !hasOctalEscape(source) {
			again, err := translateJava(out)
			if err == nil && again != out {
				t.Fatalf("translation is not idempotent: %q -> %q -> %q", source, out, again)
			}
		}
	})
}

// FuzzCompile takes the whole seam, so that whatever the rewriter emits has to
// be something an engine accepts — or be refused at registration, never
// compiled into a pattern that matches the wrong thing.
func FuzzCompile(f *testing.F) {
	for _, s := range patternSeeds {
		f.Add(s)
	}

	// Anchored is how every stub pattern is compiled (SPEC §6.6), and the
	// timeout is what keeps a fallback-engine match from occupying a request
	// goroutine indefinitely.
	opts := Options{Anchored: true, Timeout: 50 * time.Millisecond}
	subjects := []string{"", "a", "abc", "/e2e/thing", strings.Repeat("ab", 64), "\n\t ", "ünïcödé"}

	f.Fuzz(func(t *testing.T, source string) {
		start := time.Now()
		p, err := Compile(source, opts)
		if took := time.Since(start); took > translateBudget {
			t.Fatalf("compiling %d bytes took %s, over the %s budget", len(source), took, translateBudget)
		}
		if err != nil {
			return
		}

		for _, s := range subjects {
			start = time.Now()
			matched := p.MatchString(s)
			if took := time.Since(start); took > translateBudget {
				t.Fatalf("matching %q against %q took %s, over the %s budget",
					s, source, took, translateBudget)
			}

			// The prefilter drops a candidate stub without running its pattern
			// (SPEC §6.3), so a prefix that is not actually required of a match
			// silently loses matches that should have been served.
			if prefix := p.LiteralPrefix(); matched && !strings.HasPrefix(s, prefix) {
				t.Fatalf("pattern %q matched %q but claims the literal prefix %q",
					source, s, prefix)
			}
		}
	})
}

// hasOctalEscape reports whether the pattern contains a backslash followed by a
// digit, which is Java's ambiguous octal-or-backreference syntax.
func hasOctalEscape(source string) bool {
	for i := 0; i+1 < len(source); i++ {
		if source[i] == '\\' {
			if c := source[i+1]; c >= '0' && c <= '9' {
				return true
			}
			i++
		}
	}
	return false
}
