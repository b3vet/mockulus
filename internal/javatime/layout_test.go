// SPDX-License-Identifier: Apache-2.0

package javatime

import (
	"testing"
	"time"
)

// reference is the instant every case below renders, chosen so that no two
// fields share a value: a layout that swapped month for day, or minute for
// second, would produce a visibly wrong answer rather than an accidentally
// right one.
var reference = time.Date(2021, time.June, 14, 12, 13, 14, 123000000, time.UTC)

// TestLayoutRendersEveryTokenInTheTable is the coverage floor. Each entry in the
// translation table is exercised through Format, because a table entry that maps
// to a string Go does not recognise as a layout element renders as literal text
// — silently, and looking like a formatting choice rather than a bug.
func TestLayoutRendersEveryTokenInTheTable(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"yyyy", "2021"},
		{"yy", "21"},
		{"MMMM", "June"},
		{"MMM", "Jun"},
		{"MM", "06"},
		{"dd", "14"},
		{"d", "14"},
		{"HH", "12"},
		{"hh", "12"},
		{"mm", "13"},
		{"ss", "14"},
		{"a", "PM"},
		{"EEEE", "Monday"},
		{"EEE", "Mon"},
		{"XXX", "Z"},
		{"ZZ", "+0000"},
		{"Z", "+0000"},
	}

	for _, c := range cases {
		if got := reference.Format(Layout(c.pattern)); got != c.want {
			t.Errorf("Java %q -> Go %q rendered %q, want %q",
				c.pattern, Layout(c.pattern), got, c.want)
		}
	}
}

// TestFractionalSecondsSurviveTheDot pins the one token whose correctness
// depends on its neighbour.
//
// `SSS` maps to "000", which is not a layout element on its own — Go spells a
// fractional second as a dot followed by zeroes. It works because the dot comes
// from the Java pattern, so `.SSS` becomes `.000`. A bare `SSS` therefore
// renders the literal text "000", which is the degenerate case: it is asserted
// here so that anyone who changes this table knows the difference is deliberate
// and knows which of the two forms callers actually write.
func TestFractionalSecondsSurviveTheDot(t *testing.T) {
	if got := reference.Format(Layout("HH:mm:ss.SSS")); got != "12:13:14.123" {
		t.Errorf(".SSS rendered %q, want 12:13:14.123", got)
	}
	if got := reference.Format(Layout("SSS")); got != "000" {
		t.Errorf("a bare SSS rendered %q; it is literal text, which is the known degenerate case", got)
	}
}

// TestLayoutRoundTripsWhatItRenders is what makes this table usable for parsing
// as well as rendering, which is the whole reason it moved into its own package:
// `{{now format=...}}` renders with it and the `actualFormat` matcher parameter
// parses with it. A layout that renders correctly but cannot read its own output
// back would pass every rendering test and fail every match.
func TestLayoutRoundTripsWhatItRenders(t *testing.T) {
	for _, pattern := range []string{
		"yyyy-MM-dd'T'HH:mm:ss'Z'",
		"yyyy-MM-dd'T'HH:mm:ss.SSS'Z'",
		"yyyy-MM-dd HH:mm:ss",
		"dd/MM/yyyy",
		"yyyy-MM-dd",
		"HH:mm:ss",
		"EEE, dd MMM yyyy HH:mm:ss",
	} {
		layout := Layout(pattern)
		rendered := reference.Format(layout)
		if _, err := time.Parse(layout, rendered); err != nil {
			t.Errorf("Java %q -> Go %q rendered %q but cannot parse it back: %v",
				pattern, layout, rendered, err)
		}
	}
}

// TestPaddedHourParsesUnpaddedInput records why Java's `H` needs no entry of its
// own for the parsing caller.
//
// Java distinguishes `HH` (zero-padded) from `H` (not), and Go has only `15`,
// which pads on output. For parsing that distinction does not matter: `15`
// accepts both spellings on input, so a pattern written with `H` still reads the
// value. It is the *rendering* caller that would see a difference, and no
// template in the corpus asks for an unpadded hour.
func TestPaddedHourParsesUnpaddedInput(t *testing.T) {
	for _, in := range []string{"09:30", "9:30"} {
		if _, err := time.Parse(Layout("HH:mm"), in); err != nil {
			t.Errorf("Go's padded hour layout should accept %q on input: %v", in, err)
		}
	}
}

// TestUnpaddedLettersTranslate covers the field letters Java writes without
// padding.
//
// They were absent while this table only ever rendered `{{now format=...}}`,
// whose patterns all pad, and an absent letter passes through as literal text
// rather than failing — so `d/M/yyyy` became `2/M/2006`, rendered an "M" where
// the month belongs, and could not parse "14/6/2021" at all. `actualFormat`
// takes its pattern from a stub author instead of from this repo, so the gap
// became reachable and is closed here.
func TestUnpaddedLettersTranslate(t *testing.T) {
	cases := map[string]string{
		"d/M/yyyy":    "2/1/2006",
		"yyyy-M-d":    "2006-1-2",
		"H:m:s":       "15:4:5",
		"h:m a":       "3:4 PM",
		"yyyy-MM-dd":  "2006-01-02",
		"yyyy-MMM-dd": "2006-Jan-02",
	}
	for pattern, want := range cases {
		if got := Layout(pattern); got != want {
			t.Errorf("Layout(%q) = %q, want %q", pattern, got, want)
		}
	}

	// The consequence, which is the reason the letters were added: an unpadded
	// date now parses.
	for _, in := range []string{"14/6/2021", "4/6/2021"} {
		if _, err := time.Parse(Layout("d/M/yyyy"), in); err != nil {
			t.Errorf("Layout(d/M/yyyy) should parse %q: %v", in, err)
		}
	}
	// A doubled letter must not be broken by the single-letter entries sitting
	// beside it in the table, which is what the longest-first scan guarantees.
	if _, err := time.Parse(Layout("dd/MM/yyyy"), "14/06/2021"); err != nil {
		t.Errorf("the padded spelling must keep working: %v", err)
	}
}

// TestTranslateCountsRecognisedFields is what lets a caller refuse a pattern
// that cannot resolve an instant.
//
// WireMock validates only that a pattern compiles, so `qqqq` and the empty
// string register there and then match nothing ever. A zero count is how this
// repo tells those apart from a pattern that is merely punctuation-heavy.
func TestTranslateCountsRecognisedFields(t *testing.T) {
	withFields := map[string]int{
		"yyyy":       1,
		"dd/MM/yyyy": 3,
		"d/M/yyyy":   3,
	}
	for pattern, want := range withFields {
		if _, got := Translate(pattern); got != want {
			t.Errorf("Translate(%q) recognised %d fields, want %d", pattern, got, want)
		}
	}

	for _, pattern := range []string{"", "qqqq", "'yyyy'", "///", "!!"} {
		if _, got := Translate(pattern); got != 0 {
			t.Errorf("Translate(%q) recognised %d fields, want 0 — it resolves no instant",
				pattern, got)
		}
	}
}

// TestQuotedRunsAreLiteral covers the escape Java uses for the T and Z of an
// ISO timestamp, including the unterminated form.
func TestQuotedRunsAreLiteral(t *testing.T) {
	cases := map[string]string{
		"yyyy'T'MM":   "2006T01",
		"yyyy''MM":    "200601",
		"yyyy'T":      "2006T",
		"'literal":    "literal",
		"yyyy/MM@dd":  "2006/01@02",
		"'yyyy'-yyyy": "yyyy-2006",
	}
	for pattern, want := range cases {
		if got := Layout(pattern); got != want {
			t.Errorf("Layout(%q) = %q, want %q", pattern, got, want)
		}
	}
}

// TestLongestTokenWins guards the ordering the table depends on. Matching "yy"
// first would turn "yyyy" into two two-digit years, and "MM" before "MMMM"
// would turn a month name into a number followed by stray letters.
func TestLongestTokenWins(t *testing.T) {
	cases := map[string]string{
		"yyyy": "2006",
		"MMMM": "January",
		"MMM":  "Jan",
		"EEEE": "Monday",
		"dd":   "02",
	}
	for pattern, want := range cases {
		if got := Layout(pattern); got != want {
			t.Errorf("Layout(%q) = %q, want %q — the table is matched shortest-first", pattern, got, want)
		}
	}
}
