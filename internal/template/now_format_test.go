// SPDX-License-Identifier: Apache-2.0

package template

import (
	"regexp"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/javatime"
)

// Java's XXX is the ISO-8601 offset that collapses to a bare "Z" at UTC, and
// only there. The distinction is invisible in every test written in a zone
// that is never at UTC, which is how a mapping onto Go's always-numeric
// "-07:00" survives: it agrees with XXX at every offset except the one a mock
// clock actually runs at, where it writes "+00:00" for the oracle's "Z".
//
// The layout is exercised through a fixed instant in fixed zones rather than
// through the helper, so the assertion is on the translation itself and does
// not depend on the machine's zone database or on the time of day.
func TestXXXRendersZuluOnlyAtAZeroOffset(t *testing.T) {
	instant := time.Date(2026, time.July, 28, 9, 30, 0, 0, time.UTC)

	cases := []struct {
		zone *time.Location
		want string
		why  string
	}{
		{time.UTC, "Z", "a zero offset is the whole point: Java writes Z, not +00:00"},
		{time.FixedZone("AEST", 10*3600), "+10:00", "east of Greenwich stays numeric"},
		{time.FixedZone("AEDT", 11*3600), "+11:00", "the same zone in its summer, still numeric"},
		{time.FixedZone("EST", -5*3600), "-05:00", "west of Greenwich keeps its sign"},
		{time.FixedZone("NPT", 5*3600+45*60), "+05:45", "an offset that is not a whole hour"},
		{time.FixedZone("UTC+0", 0), "Z", "a zone named for UTC is still a zero offset"},
	}

	layout := javatime.Layout("XXX")
	for _, c := range cases {
		if got := instant.In(c.zone).Format(layout); got != c.want {
			t.Errorf("XXX in %s rendered %q, want %q (%s)", c.zone, got, c.want, c.why)
		}
	}
}

// The control on the same change. XXX is one of three offset tokens WireMock
// accepts and the only one that collapses at UTC; Z and ZZ are Java's RFC-822
// spelling and print "+0000" there. A fix applied to the offset tokens as a
// group, rather than to XXX alone, renders "Z" for all three and breaks a
// pattern that has been agreeing with the oracle all along.
func TestRFC822OffsetTokensStayNumericAtUTC(t *testing.T) {
	instant := time.Date(2026, time.July, 28, 9, 30, 0, 0, time.UTC)

	cases := []struct {
		pattern string
		zone    *time.Location
		want    string
	}{
		{"Z", time.UTC, "+0000"},
		{"ZZ", time.UTC, "+0000"},
		{"Z", time.FixedZone("AEST", 10*3600), "+1000"},
		{"ZZ", time.FixedZone("EST", -5*3600), "-0500"},
	}

	for _, c := range cases {
		if got := instant.In(c.zone).Format(javatime.Layout(c.pattern)); got != c.want {
			t.Errorf("%s in %s rendered %q, want %q", c.pattern, c.zone, got, c.want)
		}
	}
}

// A quoted run is literal text, and "Z" is the letter templates quote most
// often — an ISO pattern that wants a hard-coded Zulu marker writes 'Z' rather
// than trusting the instant to be at UTC. The translation of the XXX token
// must not reach inside the quotes and turn that literal into an offset.
func TestQuotedZuluStaysLiteral(t *testing.T) {
	instant := time.Date(2026, time.July, 28, 9, 30, 0, 0, time.FixedZone("AEST", 10*3600))

	layout := javatime.Layout("yyyy-MM-dd'T'HH:mm:ss'Z'")
	if got := instant.Format(layout); got != "2026-07-28T09:30:00Z" {
		t.Errorf("a quoted Z rendered %q, want the literal letter back", got)
	}
}

// The format a bare `now` falls back to. WireMock does not run its default
// through a Java pattern at all — it formats with an ISO-8601 helper that ends
// in "Z" at a zero offset — so the two implementations agree only if the
// default pattern here ends in the token that spells the same rule.
//
// mockulus reads the clock in UTC unless a timezone says otherwise, so the
// unqualified default is the case that runs on every templated stub that asks
// for the time without saying how.
func TestDefaultFormatEndsInZuluAtUTC(t *testing.T) {
	out, err := nowHelper(nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	iso := regexp.MustCompile(`^20\d\d-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
	if got, ok := out.(string); !ok || !iso.MatchString(got) {
		t.Errorf("the default format rendered %q, want an ISO-8601 instant ending in Z", out)
	}
}

// The other half of the default, and the reason it cannot simply be a hard
// "Z": under a timezone the same default carries that zone's numeric offset.
// A default pinned to a literal Z would render an Australian local time
// labelled as UTC, which is a timestamp that is wrong by ten hours and looks
// entirely well-formed.
func TestDefaultFormatCarriesANonZeroOffset(t *testing.T) {
	out, err := nowHelper(nil, map[string]any{"timezone": "Australia/Sydney"})
	if err != nil {
		t.Fatal(err)
	}

	// Sydney is +10:00 or +11:00 depending on the season, and never at UTC.
	offset := regexp.MustCompile(`^20\d\d-\d\d-\d\dT\d\d:\d\d:\d\d\+1[01]:00$`)
	if got, ok := out.(string); !ok || !offset.MatchString(got) {
		t.Errorf("the default format under a timezone rendered %q, want a numeric +1x:00 offset", out)
	}
}

// The helper end to end, so the default constant and the token table are shown
// to agree with each other: an explicit XXX pattern and no pattern at all
// produce the same offset text for the same zone.
func TestNowHelperOffsetTokenMatchesTheDefault(t *testing.T) {
	explicit, err := nowHelper(nil, map[string]any{"format": "XXX"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit != "Z" {
		t.Errorf("{{now format='XXX'}} at UTC rendered %q, want %q", explicit, "Z")
	}

	sydney, err := nowHelper(nil, map[string]any{"timezone": "Australia/Sydney", "format": "XXX"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := sydney.(string); !ok || !regexp.MustCompile(`^\+1[01]:00$`).MatchString(got) {
		t.Errorf("{{now timezone='Australia/Sydney' format='XXX'}} rendered %q, want +10:00 or +11:00", sydney)
	}
}

// The epoch spellings are not date patterns and never reach the layout
// translation, so the default-format change must leave them alone. They are
// the other two branches of the same switch, and a default moved into the
// wrong place in it turns a millisecond count into a formatted date.
func TestEpochSpellingsAreUnaffectedByTheDefault(t *testing.T) {
	cases := []struct {
		format string
		digits *regexp.Regexp
	}{
		{"epoch", regexp.MustCompile(`^\d{13}$`)},
		{"unix", regexp.MustCompile(`^\d{10}$`)},
	}

	for _, c := range cases {
		out, err := nowHelper(nil, map[string]any{"format": c.format})
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := out.(string); !ok || !c.digits.MatchString(got) {
			t.Errorf("{{now format=%q}} rendered %q, want %s", c.format, out, c.digits)
		}
	}
}
