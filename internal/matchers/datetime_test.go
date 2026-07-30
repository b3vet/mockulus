// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"strings"
	"testing"
	"time"
)

// Every expectation in this file is the pinned oracle's own answer, recorded from
// WireMock 3.13.2 during the DT-1 probe stage and reproduced here as a unit
// table. Where mockulus deliberately differs the case says so and names the
// deviation.

// temporal builds a matcher the way the compile step does, failing the test if
// the spec is refused.
func temporal(t *testing.T, op string, spec TemporalSpec) Matcher {
	t.Helper()
	spec = withPresence(spec)
	m, err := CompileTemporal(op, spec)
	if err != nil {
		t.Fatalf("CompileTemporal(%s, %+v): %v", op, spec, err)
	}
	return m
}

// withPresence fills in the flags the compile step would set from the document,
// so a case can name only the fields it cares about.
func withPresence(spec TemporalSpec) TemporalSpec {
	if spec.Location == nil {
		spec.Location = time.UTC
	}
	spec.HasTruncateExpected = spec.HasTruncateExpected || spec.TruncateExpected != ""
	spec.HasTruncateActual = spec.HasTruncateActual || spec.TruncateActual != ""
	spec.HasActualFormat = spec.HasActualFormat || spec.ActualFormat != ""
	return spec
}

// refused asserts a spec is rejected at registration, and returns the message so
// a case can check it names the offending thing.
func refused(t *testing.T, op string, spec TemporalSpec) string {
	t.Helper()
	spec = withPresence(spec)
	_, err := CompileTemporal(op, spec)
	if err == nil {
		t.Fatalf("CompileTemporal(%s, %+v) was accepted, want a refusal", op, spec)
	}
	return err.Error()
}

func TestBeforeAndAfterAreStrict(t *testing.T) {
	const expected = "2021-06-14T12:00:00Z"
	cases := []struct {
		op     string
		actual string
		want   bool
		why    string
	}{
		{"before", "2021-06-14T12:00:00Z", false, "equal satisfies neither bound"},
		{"after", "2021-06-14T12:00:00Z", false, "equal satisfies neither bound"},
		{"before", "2021-06-14T11:59:59Z", true, ""},
		{"after", "2021-06-14T12:00:01Z", true, ""},
		{"before", "2021-06-14T12:00:01Z", false, ""},
		{"after", "2021-06-14T11:59:59Z", false, ""},
		// Strict at millisecond resolution too, which is where an implementation
		// storing seconds would start accepting equality.
		{"before", "2021-06-14T11:59:59.999Z", true, ""},
		{"after", "2021-06-14T12:00:00.001Z", true, ""},
	}

	for _, c := range cases {
		m := temporal(t, c.op, TemporalSpec{Expected: expected})
		if got := m.Match(NewKeyValues(c.actual)); got != c.want {
			t.Errorf("%s %s vs %s = %v, want %v %s", c.op, expected, c.actual, got, c.want, c.why)
		}
	}
}

// TestTheExpectedTypeSelectsTheComparisonMode is the load-bearing case. The two
// halves use the same wall-clock values and differ only in whether the expected
// carries an offset, and they must answer oppositely.
func TestTheExpectedTypeSelectsTheComparisonMode(t *testing.T) {
	// Zoned expected: instants, and the actual's offset is honoured.
	// 12:00+03:00 is 09:00Z, so a 10:00Z actual is after it.
	zoned := temporal(t, "after", TemporalSpec{Expected: "2021-06-14T12:00:00+03:00"})
	if !zoned.Match(NewKeyValues("2021-06-14T10:00:00Z")) {
		t.Error("a zoned expected must compare instants: 10:00Z is after 12:00+03:00 (=09:00Z)")
	}
	if temporal(t, "before", TemporalSpec{Expected: "2021-06-14T12:00:00+03:00"}).
		Match(NewKeyValues("2021-06-14T10:00:00Z")) {
		t.Error("ignoring the expected's offset would make 10:00Z look earlier")
	}

	// Zoneless expected: wall-clock, and the actual's offset is DISCARDED. The
	// actual's instant is 10:00Z, two hours before the expected — and it must
	// still be reported as after, because 13:00 as written is after 12:00.
	local := temporal(t, "after", TemporalSpec{Expected: "2021-06-14T12:00:00"})
	if !local.Match(NewKeyValues("2021-06-14T13:00:00+03:00")) {
		t.Error("a zoneless expected must compare wall-clock fields, discarding the actual's offset")
	}
	if temporal(t, "before", TemporalSpec{Expected: "2021-06-14T12:00:00"}).
		Match(NewKeyValues("2021-06-14T13:00:00+03:00")) {
		t.Error("converting the actual to an instant would make it look earlier — the mode is wrong")
	}
}

// TestEqualityIsInstantValuedNotTextual pins the pair that separates the two.
func TestEqualityIsInstantValuedNotTextual(t *testing.T) {
	cases := []struct {
		expected, actual string
		want             bool
		why              string
	}{
		{"2021-06-14T12:00:00Z", "2021-06-14T15:00:00+03:00", true, "the same instant, written differently"},
		{"2021-06-14T12:00:00Z", "2021-06-14T12:00:00+03:00", false, "the same text, a different instant"},
		{"2021-06-14T12:13:14Z", "2021-06-14T12:13:14.000Z", true, "trailing zeros are the same instant"},
		{"2021-06-14T12:13:14Z", "2021-06-14T12:13:14.001Z", false, "one millisecond apart"},
		{"2021-06-14T12:13:14.000000001Z", "2021-06-14T12:13:14Z", false, "one nanosecond apart"},
	}
	for _, c := range cases {
		m := temporal(t, "equalToDateTime", TemporalSpec{Expected: c.expected})
		if got := m.Match(NewKeyValues(c.actual)); got != c.want {
			t.Errorf("equalToDateTime %s vs %s = %v, want %v (%s)",
				c.expected, c.actual, got, c.want, c.why)
		}
	}
}

// TestAZonelessActualResolvesInTheServerZone covers the one combination where the
// server's zone is observable, and is why the harness pins TZ.
func TestAZonelessActualResolvesInTheServerZone(t *testing.T) {
	plusThree := time.FixedZone("+03", 3*60*60)

	// In a +03:00 server, a zoneless 12:00 actual is 09:00Z.
	m := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T09:00:00Z",
		Location: plusThree,
	})
	if !m.Match(NewKeyValues("2021-06-14T12:00:00")) {
		t.Error("a zoneless actual must resolve in the server's zone against a zoned expected")
	}

	// The same pair in UTC does not match, which is what makes the zone visible.
	utc := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T09:00:00Z",
		Location: time.UTC,
	})
	if utc.Match(NewKeyValues("2021-06-14T12:00:00")) {
		t.Error("in a UTC server the same values are three hours apart")
	}

	// On the wall-clock branch the zone cannot matter, because the offset is
	// discarded before anything is compared.
	for _, loc := range []*time.Location{time.UTC, plusThree} {
		wall := temporal(t, "equalToDateTime", TemporalSpec{
			Expected: "2021-06-14T12:00:00",
			Location: loc,
		})
		if !wall.Match(NewKeyValues("2021-06-14T12:00:00+05:30")) {
			t.Errorf("the wall-clock branch must ignore both zones (loc=%v)", loc)
		}
	}
}

// TestDateOnlyMeansTheWholeDayForEqualityOnly pins the one deliberate divergence
// in this matcher (§5.5).
//
// WireMock reads a date-only expected as midnight everywhere, so
// `equalToDateTime: "2021-06-14"` matches only 00:00:00 and excludes the rest of
// the day. mockulus widens equality to the whole day, and deliberately does NOT
// widen before/after: widening those would refuse requests WireMock accepts,
// which is the direction that breaks a passing suite.
func TestDateOnlyMeansTheWholeDayForEqualityOnly(t *testing.T) {
	eq := temporal(t, "equalToDateTime", TemporalSpec{Expected: "2021-06-14"})
	for _, actual := range []string{
		"2021-06-14T00:00:00Z", "2021-06-14T13:00:00Z", "2021-06-14T23:59:59Z", "2021-06-14",
	} {
		if !eq.Match(NewKeyValues(actual)) {
			t.Errorf("equalToDateTime on a bare date should match %s — the whole day is ours", actual)
		}
	}
	for _, actual := range []string{"2021-06-13T23:59:59Z", "2021-06-15T00:00:00Z"} {
		if eq.Match(NewKeyValues(actual)) {
			t.Errorf("equalToDateTime on a bare date must not match %s", actual)
		}
	}

	// before/after keep WireMock's midnight reading exactly.
	before := temporal(t, "before", TemporalSpec{Expected: "2021-06-14"})
	if before.Match(NewKeyValues("2021-06-14T13:00:00Z")) {
		t.Error("before on a bare date is midnight, so a same-day afternoon is not before it")
	}
	if !before.Match(NewKeyValues("2021-06-13T23:00:00Z")) {
		t.Error("before on a bare date should match the previous day")
	}
	after := temporal(t, "after", TemporalSpec{Expected: "2021-06-14"})
	if !after.Match(NewKeyValues("2021-06-14T13:00:00Z")) {
		t.Error("after on a bare date is midnight, so a same-day afternoon IS after it — WireMock's answer")
	}
	if after.Match(NewKeyValues("2021-06-14T00:00:00Z")) {
		t.Error("after is strict, so midnight itself is not after midnight")
	}
}

func TestAcceptedOperandSpellings(t *testing.T) {
	// Each of these parses and compares; the actual is chosen to be earlier.
	for _, expected := range []string{
		"2021-06-14T12:13:14Z",
		"2021-06-14T12:13:14+03:00",
		"2021-06-14T12:13:14.123Z",
		"2021-06-14T12:13:14",
		"2021-06-14",
		"Mon, 14 Jun 2021 12:13:14 GMT",
	} {
		m := temporal(t, "before", TemporalSpec{Expected: expected})
		if !m.Match(NewKeyValues("2019-01-01T00:00:00Z")) {
			t.Errorf("operand %q should have compared against an earlier actual", expected)
		}
	}
}

// TestOperandsWireMockAcceptsAndCanNeverMatchAreRefused is the DT2 deviation in
// unit form. WireMock answers 201 to every one of these and then fails every
// request with no diagnostic.
func TestOperandsWireMockAcceptsAndCanNeverMatchAreRefused(t *testing.T) {
	for _, expected := range []string{
		"2021-06-14T12:13:14+0300", // a colon-less offset
		"12:13:14",                 // time only
		"1623672794",               // epoch seconds
		"1623672794000",            // epoch millis
		"",                         // empty
		"not-a-date",
		"2021-13-45T99:99:99Z", // ISO-shaped, impossible fields
		"now+2days",            // no spaces
		"now + 2 days",         // a space after the sign
		"now  +2 days",         // a doubled space
		"now +2 days extra",    // trailing text
		"  now  ",              // padded keyword
		"  2021-06-14T12:13:14Z  ",
		"now +2 day",   // singular unit: WireMock 422s this one too
		"now +1 weeks", // WEEKS is not a DateTimeUnit
	} {
		refused(t, "before", TemporalSpec{Expected: expected})
	}
}

func TestNowRelativeOperands(t *testing.T) {
	// `now` and its offsets are resolved per match, so the assertions are about
	// ordering against a fixed far-past and far-future actual rather than an
	// exact instant.
	for _, expected := range []string{
		"now", "NOW", "now +2 days", "now -3 hours", "now +1 months",
		"now +1 years", "now 2 days", "2 days", "-3 hours",
	} {
		m := temporal(t, "before", TemporalSpec{Expected: expected})
		if !m.Match(NewKeyValues("1990-01-01T00:00:00Z")) {
			t.Errorf("a long-past actual should be before %q", expected)
		}
		if m.Match(NewKeyValues("2999-01-01T00:00:00Z")) {
			t.Errorf("a far-future actual should not be before %q", expected)
		}
	}

	// The offset really moves the threshold, which a zero offset would not.
	forward := temporal(t, "after", TemporalSpec{Expected: "now +10 years"})
	if forward.Match(NewKeyValues(time.Now().Add(24 * time.Hour).Format(time.RFC3339))) {
		t.Error("tomorrow is not after a threshold ten years out")
	}
	back := temporal(t, "after", TemporalSpec{Expected: "now -10 years"})
	if !back.Match(NewKeyValues(time.Now().Add(-24 * time.Hour).Format(time.RFC3339))) {
		t.Error("yesterday is after a threshold ten years back")
	}
}

func TestUnparseableActualIsANonMatchNotAnError(t *testing.T) {
	m := temporal(t, "before", TemporalSpec{Expected: "2030-01-01T00:00:00Z"})
	for _, actual := range []string{
		"not-a-date", "", "   ", "1623672794", "12:13:14", "2021-06-14T12:13:14+0300",
	} {
		if m.Match(NewKeyValues(actual)) {
			t.Errorf("actual %q should be a non-match", actual)
		}
	}
	// Absence is a non-match too, not a special case.
	if m.Match(AbsentKey()) {
		t.Error("an absent value must not satisfy a date-time matcher")
	}
}

func TestActualFormatReplacesTheDefaultParsing(t *testing.T) {
	m := temporal(t, "equalToDateTime", TemporalSpec{
		Expected:     "2021-06-14T00:00:00Z",
		ActualFormat: "dd/MM/yyyy",
	})
	if !m.Match(NewKeyValues("14/06/2021")) {
		t.Error("actualFormat should read the pattern it was given")
	}
	if m.Match(NewKeyValues("15/06/2021")) {
		t.Error("a different date must not match")
	}
	// Exclusive, not additive: once a format is set, an ISO actual stops
	// matching. This is WireMock's behaviour and it surprises people.
	if m.Match(NewKeyValues("2021-06-14T00:00:00Z")) {
		t.Error("actualFormat replaces ISO parsing rather than adding to it")
	}

	// Unpadded patterns work, which is what the javatime table gained for this.
	unpadded := temporal(t, "equalToDateTime", TemporalSpec{
		Expected:     "2021-06-14T00:00:00Z",
		ActualFormat: "d/M/yyyy",
	})
	if !unpadded.Match(NewKeyValues("14/6/2021")) {
		t.Error("an unpadded pattern should parse an unpadded actual")
	}
}

// TestUnixIsSecondsAndEpochIsMillis pins the pair most likely to be assumed
// equivalent. They differ by a factor of a thousand.
func TestUnixIsSecondsAndEpochIsMillis(t *testing.T) {
	const (
		seconds = "1623672794"
		millis  = "1623672794000"
		instant = "2021-06-14T12:13:14Z"
	)
	unix := temporal(t, "equalToDateTime", TemporalSpec{Expected: instant, ActualFormat: "unix"})
	if !unix.Match(NewKeyValues(seconds)) {
		t.Error("unix must read the value as epoch seconds")
	}
	if unix.Match(NewKeyValues(millis)) {
		t.Error("unix must not read a millisecond value as seconds")
	}

	epoch := temporal(t, "equalToDateTime", TemporalSpec{Expected: instant, ActualFormat: "epoch"})
	if !epoch.Match(NewKeyValues(millis)) {
		t.Error("epoch must read the value as epoch milliseconds")
	}
	if epoch.Match(NewKeyValues(seconds)) {
		t.Error("epoch must not read a second value as milliseconds")
	}

	// Both spellings are case-insensitive.
	for _, spelling := range []string{"UNIX", "Unix"} {
		m := temporal(t, "equalToDateTime", TemporalSpec{Expected: instant, ActualFormat: spelling})
		if !m.Match(NewKeyValues(seconds)) {
			t.Errorf("actualFormat %q should behave as unix", spelling)
		}
	}

	// A non-numeric actual is a non-match. WireMock throws an unguarded
	// NumberFormatException here and answers 500 (§5.5 deviation).
	for _, actual := range []string{"notanumber", "", "1623672794.5"} {
		if unix.Match(NewKeyValues(actual)) {
			t.Errorf("a non-numeric actual %q must be a non-match", actual)
		}
	}
}

func TestTruncationVocabularyAndSemantics(t *testing.T) {
	// truncateActual, against an actual of 2021-06-14T12:13:14Z. The expected is
	// the predicted truncation, so a match proves the truncation happened.
	const actual = "2021-06-14T12:13:14Z"
	cases := map[string]string{
		"first second of minute":  "2021-06-14T12:13:00Z",
		"first minute of hour":    "2021-06-14T12:00:00Z",
		"first hour of day":       "2021-06-14T00:00:00Z",
		"first day of month":      "2021-06-01T00:00:00Z",
		"first day of next month": "2021-07-01T00:00:00Z",
		"first day of year":       "2021-01-01T00:00:00Z",
		"first day of next year":  "2022-01-01T00:00:00Z",
		"last day of month":       "2021-06-30T00:00:00Z",
		"last day of year":        "2021-12-31T00:00:00Z",
	}
	for value, expected := range cases {
		m := temporal(t, "equalToDateTime", TemporalSpec{
			Expected: expected, TruncateActual: value,
		})
		if !m.Match(NewKeyValues(actual)) {
			t.Errorf("truncateActual %q should reduce %s to %s", value, actual, expected)
		}
		// The falsifier: without truncation the same pair must not match.
		plain := temporal(t, "equalToDateTime", TemporalSpec{Expected: expected})
		if plain.Match(NewKeyValues(actual)) {
			t.Errorf("without truncation, %s must not equal %s", actual, expected)
		}
	}

	// Both spellings of every value are accepted; WireMock uppercases and swaps
	// spaces for underscores before its enum lookup.
	for _, value := range []string{"FIRST_DAY_OF_MONTH", "first day of month", "First Day Of Month"} {
		temporal(t, "equalToDateTime", TemporalSpec{
			Expected: "2021-06-01T00:00:00Z", TruncateActual: value,
		})
	}
	for _, value := range []string{"BANANA", "first day of banana", "", "FIRST_DAY_OF_MONTH_X"} {
		msg := refused(t, "equalToDateTime", TemporalSpec{
			Expected: "2021-06-01T00:00:00Z", TruncateActual: value,
			HasTruncateActual: true,
		})
		if !strings.Contains(msg, "truncateActual") {
			t.Errorf("the refusal of %q should name truncateActual, got %q", value, msg)
		}
	}
}

// TestTruncationSkippedForAZonelessActual mirrors WireMock: truncation applies
// only to an actual that parsed to a zoned instant. It cannot be refused at
// registration because it depends on the request, and truncating anyway would
// match where WireMock does not.
func TestTruncationSkippedForAZonelessActual(t *testing.T) {
	m := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-01T00:00:00", TruncateActual: "first day of month",
	})
	// A zoneless actual is not truncated, so it does not collapse onto the first.
	if m.Match(NewKeyValues("2021-06-14T12:13:14")) {
		t.Error("a zoneless actual must not be truncated — WireMock skips it")
	}
	// A zoned one is.
	zoned := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-01T00:00:00Z", TruncateActual: "first day of month",
	})
	if !zoned.Match(NewKeyValues("2021-06-14T12:13:14Z")) {
		t.Error("a zoned actual must be truncated")
	}
}

// TestInertCombinationsAreRefused covers CHK-DT decision 2: a parameter that
// could not take effect is refused at registration rather than accepted and
// ignored.
func TestInertCombinationsAreRefused(t *testing.T) {
	// truncateExpected does nothing on a literal expected; WireMock applies it
	// only to a now-relative operand.
	msg := refused(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T12:13:14Z", TruncateExpected: "first day of month",
	})
	if !strings.Contains(msg, "truncateExpected") || !strings.Contains(msg, "now-relative") {
		t.Errorf("the refusal should name the parameter and what it does apply to, got %q", msg)
	}
	// On a now-relative operand it is accepted.
	temporal(t, "after", TemporalSpec{Expected: "now +3 days", TruncateExpected: "first day of month"})

	// truncateActual does nothing when the actual is read through a pattern.
	msg = refused(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T00:00:00Z", ActualFormat: "dd/MM/yyyy",
		TruncateActual: "first day of month",
	})
	if !strings.Contains(msg, "truncateActual") {
		t.Errorf("the refusal should name truncateActual, got %q", msg)
	}
	// With unix/epoch it is accepted, because those do yield a zoned instant.
	temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T00:00:00Z", ActualFormat: "unix",
		TruncateActual: "first hour of day",
	})

	// applyTruncationLast orders a truncation against an offset, so it needs one.
	msg = refused(t, "after", TemporalSpec{Expected: "now +3 days", ApplyTruncLast: true})
	if !strings.Contains(msg, "applyTruncationLast") {
		t.Errorf("the refusal should name applyTruncationLast, got %q", msg)
	}
}

// TestApplyTruncationLastChoosesTheOrder pins the observable difference between
// the two orderings.
func TestApplyTruncationLastChoosesTheOrder(t *testing.T) {
	// Anchored on a fixed clock this would be exact; `now` moves, so the
	// assertion is the relationship the two orders produce. Truncating first
	// collapses to the first of this month and then adds three days; offsetting
	// first adds three days and then collapses to the first of whatever month
	// that lands in. The two differ whenever the offset crosses a month
	// boundary, and are otherwise equal — so the test asserts that the pair is
	// well-formed rather than a specific date, and the corpus pins the dates.
	def := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "now +3 days", TruncateExpected: "first day of month",
	})
	last := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "now +3 days", TruncateExpected: "first day of month", ApplyTruncLast: true,
	})

	now := time.Now().UTC()
	truncFirst := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 3)
	offsetFirst := now.AddDate(0, 0, 3)
	offsetFirst = time.Date(offsetFirst.Year(), offsetFirst.Month(), 1, 0, 0, 0, 0, time.UTC)

	if !def.Match(NewKeyValues(truncFirst.Format(time.RFC3339Nano))) {
		t.Errorf("by default the truncation lands first; expected the matcher to accept %s", truncFirst)
	}
	if !last.Match(NewKeyValues(offsetFirst.Format(time.RFC3339Nano))) {
		t.Errorf("with applyTruncationLast the offset lands first; expected %s", offsetFirst)
	}
}

// TestActualFormatPatternsThatCanNeverMatchAreRefused covers CHK-DT decision 6.
func TestActualFormatPatternsThatCanNeverMatchAreRefused(t *testing.T) {
	for _, pattern := range []string{"", "qqqq", "'yyyy'", "///"} {
		msg := refused(t, "equalToDateTime", TemporalSpec{
			Expected: "2021-06-14T00:00:00Z", ActualFormat: pattern,
			HasActualFormat: true,
		})
		if !strings.Contains(msg, "actualFormat") {
			t.Errorf("the refusal of pattern %q should name actualFormat, got %q", pattern, msg)
		}
	}
}

func TestRepeatedValuesFollowAnyOf(t *testing.T) {
	m := temporal(t, "before", TemporalSpec{Expected: "2020-01-01T00:00:00Z"})
	// One satisfying value out of two is enough, in either order.
	if !m.Match(NewKeyValues("2019-01-01T00:00:00Z", "2021-01-01T00:00:00Z")) {
		t.Error("any-of: one satisfying value is enough")
	}
	if !m.Match(NewKeyValues("2021-01-01T00:00:00Z", "2019-01-01T00:00:00Z")) {
		t.Error("any-of is order-independent")
	}
	if m.Match(NewKeyValues("2021-01-01T00:00:00Z", "2022-01-01T00:00:00Z")) {
		t.Error("no satisfying value must not match")
	}
}

func TestDescribeNamesTheMatcherAndOperand(t *testing.T) {
	m := temporal(t, "before", TemporalSpec{Expected: "2021-06-14T12:13:14Z"})
	got := m.Describe()
	if !strings.Contains(got, "before") || !strings.Contains(got, "2021-06-14T12:13:14Z") {
		t.Errorf("Describe() = %q, want it to name the matcher and its operand", got)
	}
	withFormat := temporal(t, "equalToDateTime", TemporalSpec{
		Expected: "2021-06-14T00:00:00Z", ActualFormat: "dd/MM/yyyy",
	})
	if !strings.Contains(withFormat.Describe(), "dd/MM/yyyy") {
		t.Errorf("Describe() should mention the actualFormat, got %q", withFormat.Describe())
	}
}
