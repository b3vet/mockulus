// SPDX-License-Identifier: Apache-2.0

package wmcompat

import (
	"sort"
	"strings"
)

// Near-miss scoring answers the question a failing test actually has: not "no
// stub matched" but "which stub *nearly* matched, and what was different".
//
// SPEC §6.8 sets the bar deliberately: helpful and stable, not bit-identical to
// WireMock. Diagnostic text sits outside the strict-compat surface, and probing
// established only that a method mismatch outranks a one-character URL
// difference — not a formula. So this scores on a principle rather than a
// reverse-engineered constant: a criterion that is wholly absent costs a full
// unit, and a criterion that is close costs proportionally less.
//
// Scoring never runs on the request path unless diagnostics_on_unmatched is on
// (deviation #2). The endpoints compute it on demand, where the cost is a
// person waiting for an answer rather than a request waiting to be served.

// NearMissCount is how many candidates are reported. Three is enough to show a
// pattern and few enough to read.
const NearMissCount = 3

// Difference is one criterion that did not line up.
type Difference struct {
	// Kind names the criterion class: method, url, header, query, cookie, body.
	Kind string `json:"kind"`
	// Name is the header or parameter name, empty for method and url.
	Name string `json:"name,omitempty"`
	// Expected is what the stub asked for.
	Expected string `json:"expected"`
	// Actual is what the request carried.
	Actual string `json:"actual"`
}

// NearMiss is one candidate stub and how far it was from matching.
type NearMiss struct {
	StubID   string `json:"stubId"`
	StubName string `json:"stubName,omitempty"`
	// Distance is 0 for an exact match and 1 for nothing in common.
	Distance    float64      `json:"distance"`
	Differences []Difference `json:"differences"`
}

// Scorer accumulates per-criterion distances for one candidate.
//
// Each criterion contributes to the mean, so a stub differing in one of six
// criteria scores closer than one differing in four of six — which is what
// makes the ranking useful rather than arbitrary.
type Scorer struct {
	total       float64
	count       int
	differences []Difference
}

// NewScorer starts scoring a candidate.
func NewScorer() *Scorer { return &Scorer{} }

// Exact records a criterion that matched.
func (s *Scorer) Exact() {
	s.count++
}

// Missed records a criterion that did not match, at full distance. This is the
// binary case: a method or a header that is simply not what was asked for.
func (s *Scorer) Missed(kind, name, expected, actual string) {
	s.count++
	s.total += 1
	s.differences = append(s.differences, Difference{
		Kind: kind, Name: name, Expected: expected, Actual: actual,
	})
}

// Near records a criterion that did not match but was close, scoring it by
// normalized edit distance.
//
// This is what separates a URL that differs by one character from one that
// differs entirely, and it is the difference that makes the output worth
// reading: "you asked for /api/order, the request was /api/orders" is an
// actionable diagnostic in a way that "url did not match" is not.
func (s *Scorer) Near(kind, name, expected, actual string) {
	s.count++
	d := normalizedEditDistance(expected, actual)
	s.total += d
	if d > 0 {
		s.differences = append(s.differences, Difference{
			Kind: kind, Name: name, Expected: expected, Actual: actual,
		})
	}
}

// Result returns the candidate's normalized distance and its differences.
func (s *Scorer) Result(stubID, stubName string) NearMiss {
	distance := 0.0
	if s.count > 0 {
		distance = s.total / float64(s.count)
	}
	return NearMiss{
		StubID:      stubID,
		StubName:    stubName,
		Distance:    distance,
		Differences: s.differences,
	}
}

// TopNearMisses ranks candidates and keeps the closest few.
//
// Ties break on stub id so the same unmatched request produces the same
// diagnostic every time — a diagnostic that reorders between runs is one nobody
// can paste into a bug report.
func TopNearMisses(all []NearMiss, limit int) []NearMiss {
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Distance != all[j].Distance {
			return all[i].Distance < all[j].Distance
		}
		return all[i].StubID < all[j].StubID
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

// maxEditDistanceInput bounds the strings compared. Levenshtein is O(n·m), and
// a near-miss diagnostic over two megabyte bodies would cost more than the
// request it is explaining.
const maxEditDistanceInput = 512

// normalizedEditDistance returns 0 for identical strings and 1 for entirely
// different ones.
func normalizedEditDistance(a, b string) float64 {
	if a == b {
		return 0
	}
	if a == "" || b == "" {
		return 1
	}

	if len(a) > maxEditDistanceInput {
		a = a[:maxEditDistanceInput]
	}
	if len(b) > maxEditDistanceInput {
		b = b[:maxEditDistanceInput]
	}

	longest := len(a)
	if len(b) > longest {
		longest = len(b)
	}
	d := editDistance(a, b)
	if d >= longest {
		return 1
	}
	return float64(d) / float64(longest)
}

// editDistance is Levenshtein with two rows rather than a full matrix, since
// only the previous row is ever needed.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				curr[j-1]+1,    // insertion
				prev[j]+1,      // deletion
				prev[j-1]+cost, // substitution
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// RenderNearMisses formats near misses as the plain-text diagnostic body an
// unmatched 404 carries when diagnostics are enabled.
func RenderNearMisses(misses []NearMiss) string {
	if len(misses) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\nClosest stubs:\n")
	for _, m := range misses {
		sb.WriteString("\n  ")
		if m.StubName != "" {
			sb.WriteString(m.StubName)
			sb.WriteString(" (")
			sb.WriteString(m.StubID)
			sb.WriteString(")")
		} else {
			sb.WriteString(m.StubID)
		}
		sb.WriteString("\n")
		for _, d := range m.Differences {
			sb.WriteString("    ")
			sb.WriteString(d.Kind)
			if d.Name != "" {
				sb.WriteString(" ")
				sb.WriteString(d.Name)
			}
			sb.WriteString(": expected ")
			sb.WriteString(truncateForDiagnostic(d.Expected))
			sb.WriteString(", got ")
			sb.WriteString(truncateForDiagnostic(d.Actual))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func truncateForDiagnostic(s string) string {
	const max = 120
	if s == "" {
		return "<absent>"
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
