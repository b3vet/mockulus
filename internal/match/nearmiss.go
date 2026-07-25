// SPDX-License-Identifier: Apache-2.0

package match

import (
	"strings"

	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// NearMisses scores every stub in the snapshot against a request and returns
// the closest few (SPEC §6.8).
//
// This walks all stubs, unlike matching, which stops at the first hit and is
// index-driven. That is the right trade here: near misses are computed on an
// admin call or behind a debugging flag, never on the request path by default,
// so the cost lands on someone waiting for a diagnostic rather than on a
// request waiting to be served.
func (s *Snapshot) NearMisses(req *ParsedRequest, limit int) []wmcompat.NearMiss {
	if len(s.Ordered) == 0 {
		return nil
	}

	all := make([]wmcompat.NearMiss, 0, len(s.Ordered))
	for _, cs := range s.Ordered {
		all = append(all, scoreStub(cs, req))
	}
	return wmcompat.TopNearMisses(all, limit)
}

// scoreStub measures one stub against the request, criterion by criterion.
func scoreStub(cs *stub.CompiledStub, req *ParsedRequest) wmcompat.NearMiss {
	sc := wmcompat.NewScorer()

	if cs.Method == "" || cs.Method == req.Method {
		sc.Exact()
	} else {
		// A method mismatch is total: there is no sense in which GET is close
		// to POST, and probing confirmed it outranks a one-character URL
		// difference.
		sc.Missed("method", "", cs.Method, req.Method)
	}

	scoreURL(sc, cs, req)

	for _, c := range cs.Headers {
		subject := req.HeaderSubject(c.Name)
		if c.Matcher.Match(subject) {
			sc.Exact()
			continue
		}
		sc.Near("header", c.Name, c.Matcher.Describe(), firstValue(subject.Values()))
	}
	for _, c := range cs.Query {
		subject := req.QuerySubject(c.Name)
		if c.Matcher.Match(subject) {
			sc.Exact()
			continue
		}
		sc.Near("query", c.Name, c.Matcher.Describe(), firstValue(subject.Values()))
	}
	for _, c := range cs.Cookies {
		subject := req.CookieSubject(c.Name)
		if c.Matcher.Match(subject) {
			sc.Exact()
			continue
		}
		sc.Near("cookie", c.Name, c.Matcher.Describe(), firstValue(subject.Values()))
	}
	for _, m := range cs.BodyMatchers {
		if m.Match(req.BodySubject()) {
			sc.Exact()
			continue
		}
		sc.Missed("body", "", m.Describe(), truncateBody(req.Body()))
	}

	return sc.Result(cs.ID, cs.Name)
}

// scoreURL compares the URL criterion, using edit distance for the literal
// forms so a near-identical path ranks above an unrelated one.
func scoreURL(sc *wmcompat.Scorer, cs *stub.CompiledStub, req *ParsedRequest) {
	switch cs.URLKind {
	case stub.URLAny:
		sc.Exact()

	case stub.URLExactFull:
		if cs.URLLiteral == req.FullURL {
			sc.Exact()
			return
		}
		sc.Near("url", "", cs.URLLiteral, req.FullURL)

	case stub.URLExactPath:
		if cs.URLLiteral == req.Path {
			sc.Exact()
			return
		}
		sc.Near("url", "", cs.URLLiteral, req.Path)

	case stub.URLPatternFull:
		if cs.URLRegex != nil && cs.URLRegex.MatchString(req.FullURL) {
			sc.Exact()
			return
		}
		// A pattern has no meaningful edit distance to a subject, so the miss
		// is total rather than guessed at.
		sc.Missed("url", "", cs.URLLiteral, req.FullURL)

	case stub.URLPatternPath:
		if cs.URLRegex != nil && cs.URLRegex.MatchString(req.Path) {
			sc.Exact()
			return
		}
		sc.Missed("url", "", cs.URLLiteral, req.Path)

	case stub.URLTemplate:
		if cs.PathTemplate != nil && cs.PathTemplate.Match(req.Path, nil) {
			sc.Exact()
			return
		}
		sc.Near("url", "", cs.PathTemplate.Source, req.Path)

	default:
		sc.Missed("url", "", cs.URLLiteral, req.Path)
	}
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func truncateBody(body []byte) string {
	const max = 200
	if len(body) > max {
		return string(body[:max]) + "…"
	}
	return string(body)
}

// DiagnosticBody renders the near-miss detail an unmatched 404 carries when
// diagnostics_on_unmatched is enabled (SPEC §5.4, deviation #2).
func DiagnosticBody(snap *Snapshot, req *ParsedRequest) string {
	misses := snap.NearMisses(req, wmcompat.NearMissCount)
	if len(misses) == 0 {
		return UnmatchedBody
	}
	return UnmatchedBody + "\n" + strings.TrimPrefix(wmcompat.RenderNearMisses(misses), "\n")
}
