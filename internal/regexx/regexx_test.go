// SPDX-License-Identifier: Apache-2.0

package regexx

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRE2HandlesOrdinaryPatterns(t *testing.T) {
	p, err := Compile(`/api/orders/[0-9]+`, Options{Anchored: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != EngineRE2 {
		t.Errorf("engine = %s, want %s — ordinary patterns must not pay for backtracking", p.Engine, EngineRE2)
	}
	if !p.MatchString("/api/orders/42") {
		t.Error("should match")
	}
	if p.MatchString("/api/orders/x") {
		t.Error("should not match")
	}
}

func TestAnchoringRequiresAFullMatch(t *testing.T) {
	anchored, err := Compile(`/api/orders`, Options{Anchored: true})
	if err != nil {
		t.Fatal(err)
	}
	if anchored.MatchString("/api/orders/42") {
		t.Error("an anchored pattern must not match a longer subject")
	}
	if !anchored.MatchString("/api/orders") {
		t.Error("an anchored pattern must match the exact subject")
	}

	partial, err := Compile(`/api/orders`, Options{Anchored: false})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.MatchString("/api/orders/42") {
		t.Error("an unanchored pattern should match a substring")
	}
}

// Anchoring uses \A and \z rather than ^ and $, so a newline in the subject
// cannot satisfy the anchor at a line boundary and smuggle a match through.
func TestAnchoringIsNotFooledByNewlines(t *testing.T) {
	p, err := Compile(`abc`, Options{Anchored: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"abc\ndef", "def\nabc", "abc\n"} {
		if p.MatchString(subject) {
			t.Errorf("anchored pattern matched %q across a newline", subject)
		}
	}
}

func TestFallbackEngineTakesJavaOnlyConstructs(t *testing.T) {
	// A lookahead: valid in Java and .NET, rejected by RE2.
	p, err := Compile(`(?=.*bar)foo.*`, Options{Anchored: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("lookahead should compile on the fallback engine: %v", err)
	}
	if p.Engine != EngineBacktracking {
		t.Errorf("engine = %s, want %s", p.Engine, EngineBacktracking)
	}
	if !p.MatchString("foobar") {
		t.Error("foobar should match")
	}
	if p.MatchString("foobaz") {
		t.Error("foobaz should not match")
	}
}

func TestBackreferencesCompileOnTheFallback(t *testing.T) {
	p, err := Compile(`(a+)b\1`, Options{Anchored: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("backreference should compile on the fallback engine: %v", err)
	}
	if p.Engine != EngineBacktracking {
		t.Errorf("engine = %s, want backtracking", p.Engine)
	}
	if !p.MatchString("aabaa") {
		t.Error("aabaa should match (a+)b\\1")
	}
	if p.MatchString("aaba") {
		t.Error("aaba should not match")
	}
}

// A pattern that neither engine accepts is rejected at registration rather than
// becoming a stub that silently never matches (P3).
func TestUncompilablePatternIsAnError(t *testing.T) {
	if _, err := Compile(`(unclosed`, Options{}); err == nil {
		t.Fatal("an unbalanced group must be rejected")
	}
}

// A catastrophically backtracking pattern must fail closed, not hang: the
// alternative is one stub taking a request goroutine down with it.
func TestPathologicalPatternTimesOutAndFailsClosed(t *testing.T) {
	// This pattern needs both properties: RE2 rejects it (backreference), and it
	// backtracks catastrophically on a subject that cannot match.
	var timeouts atomic.Int64
	p, err := Compile(`(a+)+\1b`, Options{
		Anchored:  true,
		Timeout:   50 * time.Millisecond,
		OnTimeout: func(string) { timeouts.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != EngineBacktracking {
		t.Skip("this pattern compiled on RE2, so there is nothing to time out")
	}

	subject := strings.Repeat("a", 40) + "!"
	start := time.Now()
	matched := p.MatchString(subject)
	took := time.Since(start)

	if matched {
		t.Error("a timed-out match must be reported as a non-match")
	}
	if took > 2*time.Second {
		t.Errorf("match took %s; the timeout did not bound it", took)
	}
	if timeouts.Load() == 0 {
		t.Error("the timeout callback should have fired, so the event is countable")
	}
}

func TestLiteralPrefix(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{`/api/orders/[0-9]+`, "/api/orders/"},
		{`/api/.*`, "/api/"},
		{`[0-9]+`, ""},
		{`/exact/path`, "/exact/path"},
		{`/api/v[12]/x`, "/api/v"},
	}
	for _, c := range cases {
		p, err := Compile(c.pattern, Options{})
		if err != nil {
			t.Errorf("compile %q: %v", c.pattern, err)
			continue
		}
		if got := p.LiteralPrefix(); got != c.want {
			t.Errorf("LiteralPrefix(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// The prefilter is only sound if every subject that matches also starts with
// the reported prefix. A wrong prefix silently drops matching stubs, so this
// property is worth asserting directly on the fallback engine's extractor,
// which cannot lean on RE2 for the answer.
func TestBacktrackingLiteralPrefixNeverOverclaims(t *testing.T) {
	cases := []struct {
		pattern  string
		subjects []string
	}{
		{`(?=.*x)/api/.*`, []string{"/api/x", "/api/foo/x"}},
		{`/a(b|c)d`, []string{"/abd", "/acd"}},
		{`/a?bc`, []string{"/bc", "/abc"}},
		{`/x*y`, []string{"/y", "/xxy"}},
	}
	for _, c := range cases {
		prefix := backtrackingLiteralPrefix(c.pattern)
		for _, s := range c.subjects {
			if !strings.HasPrefix(s, prefix) {
				t.Errorf("pattern %q reported prefix %q, but matching subject %q does not start with it",
					c.pattern, prefix, s)
			}
		}
	}
}

func BenchmarkMatchRE2(b *testing.B) {
	p := MustCompile(`/api/orders/[0-9]+`, Options{Anchored: true})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !p.MatchString("/api/orders/12345") {
			b.Fatal("expected a match")
		}
	}
}

// Anchoring wraps the pattern, and the prefilter depends on the wrapper not
// destroying the literal prefix — a lost prefix silently costs a regex
// evaluation on every pattern candidate.
func TestAnchoringPreservesTheLiteralPrefix(t *testing.T) {
	for _, pattern := range []string{`/api/orders/[0-9]+`, `/api/.*`, `/exact`} {
		anchored := MustCompile(pattern, Options{Anchored: true})
		plain := MustCompile(pattern, Options{Anchored: false})
		if anchored.LiteralPrefix() != plain.LiteralPrefix() {
			t.Errorf("pattern %q: anchored prefix %q differs from unanchored %q",
				pattern, anchored.LiteralPrefix(), plain.LiteralPrefix())
		}
		if anchored.LiteralPrefix() == "" {
			t.Errorf("pattern %q lost its literal prefix when anchored", pattern)
		}
	}
}
