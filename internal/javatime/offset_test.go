// SPDX-License-Identifier: Apache-2.0

package javatime

import (
	"testing"
	"time"
)

// A month is not a fixed span of time and a year is not 365 days, so an offset
// naming either of them cannot be multiplied out. WireMock resolves both on a
// calendar; a fixed 30-day month drifts by up to a day and a half per month and
// a fixed 365-day year by a day per leap year, and every answer it produces is
// a well-formed date that nothing downstream can question.
//
// The expectations are the oracle's own, taken from pinned WireMock 3.13.2 with
// its clock at 2026-07-28 and — for the offsets a live clock cannot reach — from
// its `date`/`parseDate` pair over the same arithmetic. They are asserted here
// against fixed instants rather than through `now`, because the answer to "one
// month from now" changes daily and the rule it follows does not.
func TestMonthAndYearOffsetsFollowTheCalendar(t *testing.T) {
	base := time.Date(2026, time.July, 28, 22, 40, 0, 0, time.UTC)

	cases := []struct {
		offset string
		want   string
		why    string
	}{
		{"1 months", "2026-08-28", "the day of the month survives; a 30-day month lands on the 27th"},
		{"-13 months", "2025-06-28", "more than a year back, and the month still lands where it is named"},
		{"7 months", "2027-02-28", "across a year boundary and into a short month"},
		{"2 years", "2028-07-28", "two years spanning a leap day; 730 days lands on the 27th"},
		{"-4 years", "2022-07-28", "backwards across a leap day, which a fixed 365 puts on the 29th"},
		{"-1200 months", "1926-07-28", "a century of months, where the drift has grown to 17 months"},
		{"0 months", "2026-07-28", "a zero offset is still a legitimate offset"},
	}

	for _, c := range cases {
		offset, err := ParseOffset(c.offset)
		if err != nil {
			t.Fatalf("offset %q: %v", c.offset, err)
		}
		if got := offset.Shift(base).Format("2006-01-02"); got != c.want {
			t.Errorf("%q from %s rendered %s, want %s (%s)",
				c.offset, base.Format("2006-01-02"), got, c.want, c.why)
		}
	}
}

// The day of the month is kept where the month it lands on is long enough to
// hold it, and pulled back to that month's last day where it is not. Java
// clamps, so WireMock does; Go's AddDate carries the surplus into the month
// after — 2026-01-31 offset by one month becomes 2026-03-03, which is not the
// month the template asked for and is the kind of date a billing fixture is
// read straight off.
//
// Every expectation below was measured against the oracle through its `date`
// helper, which offsets a parsed instant with the same arithmetic `now` uses.
func TestMonthArithmeticClampsToTheShorterMonth(t *testing.T) {
	cases := []struct {
		from   string
		offset string
		want   string
		why    string
	}{
		{"2026-01-31", "1 months", "2026-02-28", "31 January has no 31 February to land on"},
		{"2026-03-31", "-1 months", "2026-02-28", "the clamp applies backwards too"},
		{"2026-08-31", "6 months", "2027-02-28", "and across a year boundary"},
		{"2026-01-31", "13 months", "2027-02-28", "a year and a month, clamped once at the end"},
		{"2024-02-29", "1 years", "2025-02-28", "a leap day has no anniversary in a common year"},
		{"2024-02-29", "4 years", "2028-02-29", "and keeps it in the next leap year"},
		{"2026-01-30", "1 months", "2026-02-28", "a day short of the month end clamps the same way"},
		{"2026-01-28", "1 months", "2026-02-28", "the last day February does hold is not clamped at all"},
	}

	for _, c := range cases {
		base, err := time.Parse("2006-01-02", c.from)
		if err != nil {
			t.Fatal(err)
		}
		offset, err := ParseOffset(c.offset)
		if err != nil {
			t.Fatalf("offset %q: %v", c.offset, err)
		}
		if got := offset.Shift(base).Format("2006-01-02"); got != c.want {
			t.Errorf("%s offset by %q rendered %s, want %s (%s)", c.from, c.offset, got, c.want, c.why)
		}
	}
}

// The control on the calendar change. Days and hours were already agreeing with
// the oracle as fixed spans, and they have to keep agreeing: an offset of 30
// days is 30 days even when the month it crosses is shorter than that, and one
// converted into "about a month" would land on the last day of February.
//
// The seconds and minutes rows are the same claim at the units a token expiry
// is written in, where a calendar rule applied to a span offset would be
// invisible in any format coarse enough to read.
func TestSpanOffsetsStayFixedSpansOfTime(t *testing.T) {
	cases := []struct {
		from   string
		offset string
		want   string
		why    string
	}{
		{"2026-07-28", "3 days", "2026-07-31T00:00:00", "the unchanged day case, from the oracle's live clock"},
		{"2026-07-28", "-48 hours", "2026-07-26T00:00:00", "hours, likewise unchanged"},
		{"2026-01-31", "30 days", "2026-03-02T00:00:00", "30 days over a 28-day February is not one month"},
		{"2026-01-31", "1 days", "2026-02-01T00:00:00", "a day at a month boundary crosses it"},
		{"2026-07-28", "90 seconds", "2026-07-28T00:01:30", "seconds move the clock and not the calendar"},
		{"2026-07-28", "-30 minutes", "2026-07-27T23:30:00", "minutes carry into the previous day"},
	}

	for _, c := range cases {
		base, err := time.Parse("2006-01-02", c.from)
		if err != nil {
			t.Fatal(err)
		}
		offset, err := ParseOffset(c.offset)
		if err != nil {
			t.Fatalf("offset %q: %v", c.offset, err)
		}
		if got := offset.Shift(base).Format("2006-01-02T15:04:05"); got != c.want {
			t.Errorf("%s offset by %q rendered %s, want %s (%s)", c.from, c.offset, got, c.want, c.why)
		}
	}
}

// A month offset moves the calendar and leaves the clock where it was, which is
// what an expiry rendered to the second depends on. Arithmetic that reached the
// time of day would shift a token's lifetime by however far the two instants
// happen to sit apart within their days.
func TestAMonthOffsetLeavesTheTimeOfDayAlone(t *testing.T) {
	base := time.Date(2026, time.July, 28, 22, 40, 13, 512, time.UTC)

	offset, err := ParseOffset("-3 months")
	if err != nil {
		t.Fatal(err)
	}
	got := offset.Shift(base)

	if want := "2026-04-28T22:40:13"; got.Format("2006-01-02T15:04:05") != want {
		t.Errorf("three months back rendered %s, want %s", got.Format("2006-01-02T15:04:05"), want)
	}
	if got.Nanosecond() != base.Nanosecond() {
		t.Errorf("the nanosecond moved to %d, want %d", got.Nanosecond(), base.Nanosecond())
	}
}

// The refusals are part of the offset contract and cov-tmpl-now-offsets-001
// serves them to a caller over HTTP, so a rework of how an offset is
// represented must not quietly turn one of them into a rendered date. The
// unit is read the same way for the calendar units as for the span ones.
func TestUnusableOffsetsAreStillRefused(t *testing.T) {
	for _, spec := range []string{"soon", "many months", "3 fortnights", "", "1 months ago"} {
		if _, err := ParseOffset(spec); err == nil {
			t.Errorf("offset %q was accepted, want a refusal", spec)
		}
	}

	for _, spec := range []string{"1 months", "1 month", "-2 years", "0 years"} {
		if _, err := ParseOffset(spec); err != nil {
			t.Errorf("offset %q was refused (%v), want it accepted", spec, err)
		}
	}
}

// TestParseOffsetStrictRequiresThePlural pins the difference between WireMock's
// two offset readers. The `now` helper takes `1 month`; a date-time matcher's
// operand does not, because the unit goes through an enum lookup there.
func TestParseOffsetStrictRequiresThePlural(t *testing.T) {
	for _, spec := range []string{"1 month", "-2 year", "3 day", "1 hour"} {
		if _, err := ParseOffsetStrict(spec); err == nil {
			t.Errorf("offset %q was accepted by the strict reader, want a refusal", spec)
		}
		// The lenient reader still takes it, which is what the template needs.
		if _, err := ParseOffset(spec); err != nil {
			t.Errorf("offset %q must stay acceptable to the helper: %v", spec, err)
		}
	}

	for _, spec := range []string{"1 months", "-2 years", "0 seconds", "12 hours"} {
		if _, err := ParseOffsetStrict(spec); err != nil {
			t.Errorf("offset %q was refused by the strict reader (%v), want it accepted", spec, err)
		}
	}

	// An unknown unit is still an unknown unit, plural or not.
	if _, err := ParseOffsetStrict("3 fortnights"); err == nil {
		t.Error("an unknown plural unit must still be refused")
	}
}
