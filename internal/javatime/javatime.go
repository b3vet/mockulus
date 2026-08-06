// SPDX-License-Identifier: Apache-2.0

// Package javatime translates the Java date-and-time vocabulary WireMock is
// configured in — pattern strings and offset expressions — into what Go's `time`
// package works with.
//
// It lives in its own package because two features need the same translation and
// a stub must not be able to disagree with itself about what a pattern means.
// `{{now format='...'}}` renders with it (SPEC §10.3), and the `actualFormat`
// parameter of the date-time matchers parses with it (§5.2). A second
// implementation of either would be a place for the two to drift, which is the
// reasoning `SplitQuery` and `DecodeBase64` are already shared under.
//
// Rendering and parsing are not quite symmetric. Go's `15` pads on output but
// accepts an unpadded hour on input, so one entry serves both directions and
// Java's `H` needs no separate mapping. Everything else does map both ways, and
// TestLayoutRoundTripsWhatItRenders is what holds that: a layout that formats
// correctly and cannot read its own output back would pass every rendering test
// and fail every match.
package javatime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Offset is a `now` offset held apart into the two kinds of quantity an
// offset can name.
//
// Seconds through days are spans of elapsed time and add as one. Months and
// years are not: how much time a month is depends on which month the instant
// falls in, and WireMock resolves them on a calendar. Multiplying out a fixed
// 30- or 365-day month is off by up to a day and a half per month and the
// result is still a well-formed date, so nothing downstream can notice — an
// expiry "one month out" from 2026-07-28 lands on the 27th here and the 28th
// there, and both look like an answer somebody meant.
type Offset struct {
	months int
	span   time.Duration
}

// Shift resolves the offset against an instant. The calendar part goes first,
// so a span of hours is measured from the day the calendar landed on rather
// than displacing the day the months were counted from.
func (o Offset) Shift(t time.Time) time.Time {
	if o.months != 0 {
		t = AddMonths(t, o.months)
	}
	return t.Add(o.span)
}

// ParseOffset reads WireMock's offset syntax: a signed count and a unit, as in
// "3 days" or "-1 hours".
func ParseOffset(spec string) (Offset, error) {
	fields := strings.Fields(spec)
	if len(fields) != 2 {
		return Offset{}, fmt.Errorf("offset %q should look like \"3 days\"", spec)
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return Offset{}, fmt.Errorf("offset %q does not start with a number", spec)
	}

	unit := strings.ToLower(strings.TrimSuffix(fields[1], "s"))
	switch unit {
	case "second":
		return Offset{span: time.Duration(n) * time.Second}, nil
	case "minute":
		return Offset{span: time.Duration(n) * time.Minute}, nil
	case "hour":
		return Offset{span: time.Duration(n) * time.Hour}, nil
	case "day":
		return Offset{span: time.Duration(n) * 24 * time.Hour}, nil
	case "month":
		return Offset{months: int(n)}, nil
	case "year":
		return Offset{months: int(n) * 12}, nil
	default:
		return Offset{}, fmt.Errorf("unknown offset unit %q", fields[1])
	}
}

// ParseOffsetStrict is ParseOffset under the stricter rule WireMock applies on
// its matcher side: the unit must be written in the plural.
//
// The two spellings really are governed differently there. A template offset is
// read by the `now` helper, which takes `1 month`; a date-time matcher's operand
// goes through a `DateTimeUnit` enum lookup that answers
// `No enum constant …DateTimeUnit.DAY` for the singular. Rather than pick one and
// be wrong on one side, the lenient reading stays with the helper and the strict
// one is named here — and both share the unit table below, so a new unit cannot
// reach one caller and miss the other.
func ParseOffsetStrict(spec string) (Offset, error) {
	fields := strings.Split(strings.TrimSpace(spec), " ")
	if len(fields) == 2 && !strings.HasSuffix(strings.ToLower(fields[1]), "s") {
		return Offset{}, fmt.Errorf("offset unit %q must be written in the plural", fields[1])
	}
	return ParseOffset(spec)
}

// AddMonths moves an instant by whole calendar months, keeping the time of day
// and the day of the month wherever the month it lands on is long enough to
// hold it.
//
// Where it is not, the day is pulled back to that month's last day. That is
// Java's rule and so WireMock's: 2026-01-31 offset by one month is 2026-02-28
// there, and 2024-02-29 offset by a year is 2025-02-28. Go's AddDate carries
// the surplus into the following month instead and answers 2026-03-03 — a date
// outside the month the template asked for, which is the one answer a caller
// saying "next month" cannot use.
func AddMonths(t time.Time, months int) time.Time {
	year, month, day := t.Date()
	hour, minute, sec := t.Clock()

	// Anchored on the first of the month, the month arithmetic has no day to
	// overflow, so the month and year it lands on are exactly the ones asked
	// for and the day is decided below rather than by normalisation.
	target := time.Date(year, month, 1, hour, minute, sec, t.Nanosecond(), t.Location()).
		AddDate(0, months, 0)
	if last := daysInMonth(target.Year(), target.Month()); day > last {
		day = last
	}
	return time.Date(target.Year(), target.Month(), day, hour, minute, sec, t.Nanosecond(), t.Location())
}

// daysInMonth reads a month's length off the calendar, by asking for the day
// before the first of the next one.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// javaPatternOrder is the translation table, longest pattern first: a naive
// replacement would turn "yyyy" into four copies of the "yy" replacement.
//
// A letter absent from this table passes through as literal text rather than
// failing, so an omission is silent — which is why the unpadded forms are here
// beside the padded ones, and why Translate reports a field count a caller can
// refuse on.
var javaPatternOrder = []struct{ java, golang string }{
	{"yyyy", "2006"}, {"yy", "06"},
	{"MMMM", "January"}, {"MMM", "Jan"}, {"MM", "01"}, {"M", "1"},
	{"dd", "02"}, {"d", "2"},
	// Go has no unpadded 24-hour hour, and does not need one: "15" pads on
	// output but accepts an unpadded hour on input, which is the only direction
	// `actualFormat` uses. A rendering caller asking for Java's `H` gets the
	// padded form, and no template in the corpus asks for one.
	{"HH", "15"}, {"H", "15"},
	{"hh", "03"}, {"h", "3"},
	{"mm", "04"}, {"m", "4"},
	{"ss", "05"}, {"s", "5"},
	{"SSS", "000"},
	// "Z07:00" rather than "-07:00" because Java's XXX writes a bare "Z" at a
	// zero offset and a numeric offset everywhere else, and Go's layout has the
	// same split. "-07:00" is numeric always, so it renders "+00:00" for a UTC
	// instant — a timestamp that is still ISO-8601 but not the one the oracle
	// wrote, and the difference only shows up at UTC, which is where a mock
	// clock spends nearly all of its time. ZZ and Z stay numeric: those are
	// Java's RFC-822 patterns and they print "+0000" at UTC.
	{"XXX", "Z07:00"}, {"ZZ", "-0700"}, {"Z", "-0700"},
	{"a", "PM"},
	{"EEEE", "Monday"}, {"EEE", "Mon"},
}

// Layout translates a Java date pattern into Go's reference-time layout, so the
// result can be handed to either time.Format or time.Parse.
//
// A token the table does not carry is copied through a character at a time,
// which is what lets a pattern's punctuation survive — and is also why an
// untranslated field letter becomes literal text rather than an error.
func Layout(pattern string) string {
	layout, _ := Translate(pattern)
	return layout
}

// Translate reports the Go layout and how many pattern fields it recognised.
//
// The count is what lets a caller refuse a pattern that cannot resolve an
// instant. WireMock validates only that a pattern compiles, so `qqqq` and the
// empty string register there and then never match anything; a count of zero is
// how this repo tells that apart from a pattern that merely has punctuation in
// it (SPEC §5.2).
func Translate(pattern string) (layout string, fields int) {
	var sb strings.Builder
	for i := 0; i < len(pattern); {
		// A quoted run is literal text, which is how templates write the T and
		// Z of an ISO timestamp.
		if pattern[i] == '\'' {
			end := strings.IndexByte(pattern[i+1:], '\'')
			if end < 0 {
				sb.WriteString(pattern[i+1:])
				break
			}
			sb.WriteString(pattern[i+1 : i+1+end])
			i += end + 2
			continue
		}

		matched := false
		for _, p := range javaPatternOrder {
			if strings.HasPrefix(pattern[i:], p.java) {
				sb.WriteString(p.golang)
				i += len(p.java)
				matched = true
				break
			}
		}
		if matched {
			fields++
			continue
		}
		sb.WriteByte(pattern[i])
		i++
	}
	return sb.String(), fields
}
