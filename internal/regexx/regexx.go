// SPDX-License-Identifier: Apache-2.0

// Package regexx is the regex seam of SPEC §6.6. Stub patterns are written for
// Java's regex engine, which has constructs RE2 deliberately lacks — lookaround,
// backreferences, possessive quantifiers — so one engine cannot serve both
// compatibility and safety.
//
// The strategy is two engines with an explicit trade. Go's RE2 handles the
// overwhelming majority of patterns in guaranteed linear time, which is what
// keeps the hot path safe against a pattern supplied by whoever wrote the stub.
// A pattern RE2 refuses falls back to a backtracking engine with .NET-style
// semantics, close enough to Java's for compatibility, and that engine is given
// a match timeout: a pathological pattern fails closed, as a non-match plus a
// counter, rather than occupying a request goroutine indefinitely.
//
// Compilation happens at stub registration, never at serve time (SPEC §16.3).
package regexx

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// Engine names, reported on a compiled pattern so operators can see which
// engine a stub ended up on.
const (
	// EngineRE2 is Go's linear-time engine, and the one almost every pattern uses.
	EngineRE2 = "re2"
	// EngineBacktracking is the fallback for patterns RE2 cannot express.
	EngineBacktracking = "regexp2"
)

// ErrTimeout reports that a fallback-engine match exceeded its budget. The
// caller treats it as a non-match: failing closed is the only safe answer when
// the alternative is stalling a request.
var ErrTimeout = errors.New("regex match timed out")

// Pattern is a compiled regular expression, ready to evaluate with no further
// compilation work.
type Pattern struct {
	// Source is the pattern exactly as it was written in the stub.
	Source string
	// Engine names the engine that compiled it.
	Engine string

	re2 *regexp.Regexp
	bt  *regexp2.Regexp

	// literalPrefix is the leading literal text every match must start with,
	// which the match engine uses to skip candidates without running the regex
	// at all (SPEC §6.3).
	literalPrefix string
	prefixIsWhole bool

	// onTimeout is called when a fallback match exceeds its budget, so the
	// engine can count it and name the offending stub.
	onTimeout func(source string)
}

// Options configure compilation.
type Options struct {
	// Timeout bounds a single match on the fallback engine (`regex_timeout`).
	Timeout time.Duration
	// Anchored wraps the pattern so it must match the entire subject, which is
	// what WireMock's regex matchers require.
	Anchored bool
	// OnTimeout is invoked when a match is abandoned; used for metrics and logs.
	OnTimeout func(source string)
}

// Compile builds a pattern, trying RE2 first and falling back only when it
// refuses. A pattern neither engine accepts is an error, reported at
// registration as a 422 — never a silent non-match (SPEC §6.6, P3).
func Compile(source string, opts Options) (*Pattern, error) {
	p := &Pattern{Source: source, onTimeout: opts.OnTimeout}

	expr := source
	if opts.Anchored {
		// \A and \z rather than ^ and $ so that a subject containing a newline
		// cannot satisfy the anchor at a line boundary.
		expr = `\A(?:` + source + `)\z`
	}

	if re, err := regexp.Compile(expr); err == nil {
		p.Engine = EngineRE2
		p.re2 = re
		p.literalPrefix, p.prefixIsWhole = re.LiteralPrefix()
		return p, nil
	}

	bt, err := regexp2.Compile(expr, regexp2.RE2)
	if err != nil {
		// Retry without the RE2 compatibility flag: the pattern may use a
		// construct that only the full .NET syntax accepts.
		bt, err = regexp2.Compile(expr, regexp2.None)
		if err != nil {
			return nil, fmt.Errorf("pattern does not compile on either engine: %w", err)
		}
	}
	if opts.Timeout > 0 {
		bt.MatchTimeout = opts.Timeout
	}
	p.Engine = EngineBacktracking
	p.bt = bt
	p.literalPrefix = backtrackingLiteralPrefix(source)
	return p, nil
}

// MustCompile is Compile for patterns known good at build time; it panics on
// failure and is for tests and fixtures only.
func MustCompile(source string, opts Options) *Pattern {
	p, err := Compile(source, opts)
	if err != nil {
		panic("regexx: " + err.Error())
	}
	return p
}

// MatchString reports whether the subject matches. A fallback-engine timeout is
// reported as a non-match, after calling OnTimeout.
func (p *Pattern) MatchString(s string) bool {
	if p.re2 != nil {
		return p.re2.MatchString(s)
	}
	ok, err := p.bt.MatchString(s)
	if err != nil {
		if p.onTimeout != nil {
			p.onTimeout(p.Source)
		}
		return false
	}
	return ok
}

// LiteralPrefix returns the leading literal text a subject must start with for
// a match to be possible, or the empty string when nothing can be concluded.
// The match engine uses it to prefilter pattern stubs cheaply.
func (p *Pattern) LiteralPrefix() string { return p.literalPrefix }

// backtrackingLiteralPrefix extracts a conservative literal prefix from a
// pattern the fallback engine compiled, since regexp2 exposes no equivalent of
// RE2's LiteralPrefix. It stops at the first character that could introduce
// alternation, repetition or a class, and returns nothing at all when the
// pattern opens with one — a wrong prefix would silently drop matching stubs,
// so this errs entirely toward returning less.
func backtrackingLiteralPrefix(source string) string {
	var sb strings.Builder
	for i := 0; i < len(source); i++ {
		c := source[i]
		if c == '\\' {
			// An escape may be a literal (\.) or a class (\d); either way the
			// prefix ends here rather than guessing.
			break
		}
		if strings.IndexByte(`.*+?()[]{}|^$`, c) >= 0 {
			// A quantifier applies to the character before it, so that
			// character is not guaranteed to be present.
			if (c == '*' || c == '?') && sb.Len() > 0 {
				s := sb.String()
				return s[:len(s)-1]
			}
			break
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
