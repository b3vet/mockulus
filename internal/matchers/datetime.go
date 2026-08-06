// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/javatime"
)

// The date-time matchers of SPEC §5.2: `before`, `after` and `equalToDateTime`.
//
// One thing here is worth reading before the code, because it is the opposite of
// what a reasonable implementation would do. **The expected value's type selects
// the comparison mode**, and WireMock 3.13.2 was probed to establish it:
//
//   - An expected value carrying a zone or offset compares **instants**. The
//     actual's offset is honoured, and an actual with no offset is resolved in
//     the server's own zone (the JVM default there, `loc` here).
//   - An expected value with no zone compares **wall-clock fields**, and the
//     actual's offset is *discarded rather than converted*.
//
// So the same two values answer differently depending only on whether the
// expected carries a `Z`: expected `2021-06-14T12:00:00` reports an actual of
// `2021-06-14T13:00:00+03:00` as *later*, even though that instant is two hours
// *earlier*. Normalising everything to instants — the obvious design —
// reproduces the first bullet and silently breaks the second.
//
// `before` and `after` are strict: an actual equal to the expected satisfies
// neither. Equality is instant-valued and exact to the nanosecond, so
// `12:13:14Z` equals `12:13:14.000Z` and not `12:13:14.001Z`.

// temporalOp is which of the three matchers this is.
type temporalOp uint8

const (
	temporalBefore temporalOp = iota
	temporalAfter
	temporalEqual
)

func (op temporalOp) String() string {
	switch op {
	case temporalBefore:
		return "before"
	case temporalAfter:
		return "after"
	default:
		return "equalToDateTime"
	}
}

// temporalKind is what a parsed value carried, which is what decides the
// comparison mode when it is the expected side.
type temporalKind uint8

const (
	// kindZoned carried an offset, so it names an instant.
	kindZoned temporalKind = iota
	// kindLocal carried a time but no offset.
	kindLocal
	// kindDate carried only a date.
	kindDate
)

// temporalValue is a parsed date-time.
//
// For kindZoned, `t` is the instant. For the other two it is a carrier: the
// wall-clock fields as written, held in UTC because they name no zone of their
// own and UTC does no arithmetic to them.
type temporalValue struct {
	kind temporalKind
	t    time.Time
}

// instant resolves the value to a point in time, reading a zoneless value in the
// server's zone — which is what WireMock does with the JVM default, established
// by probing a second oracle in a non-UTC zone.
func (v temporalValue) instant(loc *time.Location) time.Time {
	if v.kind == kindZoned {
		return v.t
	}
	y, mo, d := v.t.Date()
	h, mi, s := v.t.Clock()
	return time.Date(y, mo, d, h, mi, s, v.t.Nanosecond(), loc)
}

// wall reads the value's wall-clock fields, discarding any offset. A zoned
// `13:00:00+03:00` yields 13:00, not the 10:00 its instant would convert to.
func (v temporalValue) wall() time.Time {
	y, mo, d := v.t.Date()
	h, mi, s := v.t.Clock()
	return time.Date(y, mo, d, h, mi, s, v.t.Nanosecond(), time.UTC)
}

// Temporal matches a date-time value. Everything expensive is resolved at
// registration; matching parses the actual and compares (SPEC §16.3 rule 2).
type Temporal struct {
	op temporalOp

	// literal is the expected value when the operand named one outright.
	literal temporalValue
	// offset is set when the operand was now-relative, and is resolved against
	// the clock on every match. relative distinguishes a zero offset (`now`)
	// from a literal.
	offset   javatime.Offset
	relative bool

	// loc is the zone a zoneless value resolves in.
	loc *time.Location

	truncExpected  truncation
	truncActual    truncation
	applyTruncLast bool

	// format is nil unless `actualFormat` was given, in which case it replaces
	// the default parsing entirely rather than adding to it.
	format *actualFormat

	source string
}

// Match implements Matcher.
//
// Repeated values follow the any-of rule every other matcher uses, which for
// `before` and `after` is a deliberate divergence rather than parity — see
// deviation #29. WireMock picks one value and matches that, and where it cannot
// rank them it takes the first that parses, so `?when=13:00Z&when=11:00Z`
// refuses there and the same pair reversed matches. `equalToDateTime` does
// behave as any-of there, because an equality can report a distance to rank by.
// Reproducing the split would put value selection on the matching hot path
// against §16.3 rule 1, and any-of matches strictly more, so nothing that
// passes on WireMock fails here.
func (m *Temporal) Match(s Subject) bool { return matchAnyValue(s, m.matchOne) }

func (m *Temporal) matchOne(raw string) bool {
	actual, ok := m.parseActual(raw)
	if !ok {
		// An actual that does not parse is a plain non-match, never an error —
		// the same answer §6.7 gives a body that is not JSON. WireMock throws an
		// unguarded NumberFormatException for a non-numeric `unix`/`epoch`
		// actual and answers 500; reproducing a crash is not a compatibility
		// goal (deviation, §5.5).
		return false
	}

	expected := m.resolveExpected()

	// Truncating the actual is skipped unless it parsed to a zoned value, which
	// is what WireMock does. The combinations that are statically inert are
	// refused at registration instead; this one depends on the request, so it
	// cannot be. Truncating anyway would match where WireMock does not.
	if m.truncActual != truncNone && actual.kind == kindZoned {
		actual.t = m.truncActual.apply(actual.t)
	}

	return m.compare(expected, actual)
}

// resolveExpected produces the expected value for this comparison, reading the
// clock when the operand was now-relative.
func (m *Temporal) resolveExpected() temporalValue {
	if !m.relative {
		return m.literal
	}

	now := time.Now().In(m.loc)
	// Order matters and is observable. By default the truncation lands first and
	// the offset is measured from the truncated instant; `applyTruncationLast`
	// swaps them. With `now +3 days` and a first-day-of-month truncation the two
	// give different answers, which is how the flag was found.
	if m.applyTruncLast {
		return temporalValue{kind: kindZoned, t: m.truncExpected.apply(m.offset.Shift(now))}
	}
	return temporalValue{kind: kindZoned, t: m.offset.Shift(m.truncExpected.apply(now))}
}

// compare is the dispatch the package comment describes.
func (m *Temporal) compare(expected, actual temporalValue) bool {
	if expected.kind == kindZoned {
		a, e := actual.instant(m.loc), expected.t
		switch m.op {
		case temporalBefore:
			return a.Before(e)
		case temporalAfter:
			return a.After(e)
		default:
			return a.Equal(e)
		}
	}

	// The wall-clock branch. Both sides are compared as written.
	a, e := actual.wall(), expected.wall()

	// A date-only expected under `equalToDateTime` means the whole day, which is
	// a deliberate deviation (§5.5): WireMock reads it as midnight, so
	// `equalToDateTime: "2021-06-14"` matches only that one instant and excludes
	// almost all of the day it names. The divergence is one-directional — every
	// request WireMock matches here is matched identically — so no suite that
	// passes there can fail here. `before` and `after` keep midnight, because
	// widening them would refuse requests WireMock accepts.
	if expected.kind == kindDate && m.op == temporalEqual {
		ay, am, ad := a.Date()
		ey, em, ed := e.Date()
		return ay == ey && am == em && ad == ed
	}

	switch m.op {
	case temporalBefore:
		return a.Before(e)
	case temporalAfter:
		return a.After(e)
	default:
		return a.Equal(e)
	}
}

// Describe implements Matcher.
func (m *Temporal) Describe() string {
	out := m.op.String() + " " + quote(m.source)
	if m.format != nil {
		out += " (actualFormat " + quote(m.format.source) + ")"
	}
	return out
}

// expectedLayouts are the spellings WireMock accepts for an expected operand and
// for an actual value, in the order they are tried.
//
// The offset form must carry a colon: WireMock takes `+03:00` and silently never
// matches `+0300`, so the colon-less spelling is refused at registration here
// rather than accepted into a stub that cannot match (§5.5).
var expectedLayouts = []struct {
	layout string
	kind   temporalKind
}{
	{time.RFC3339Nano, kindZoned},
	{"2006-01-02T15:04:05.999999999", kindLocal},
	{"2006-01-02T15:04:05", kindLocal},
	{"2006-01-02", kindDate},
	{time.RFC1123, kindZoned},
	{time.RFC1123Z, kindZoned},
}

// parseDateTime reads one of the accepted spellings.
func parseDateTime(s string) (temporalValue, bool) {
	for _, c := range expectedLayouts {
		if t, err := time.Parse(c.layout, s); err == nil {
			return temporalValue{kind: c.kind, t: t}, true
		}
	}
	return temporalValue{}, false
}

// parseActual reads the request's value, through `actualFormat` when one was
// given. The format replaces the default spellings rather than extending them,
// which is WireMock's behaviour: once set, an ISO actual stops matching.
func (m *Temporal) parseActual(raw string) (temporalValue, bool) {
	if m.format == nil {
		return parseDateTime(raw)
	}
	return m.format.parse(raw)
}

// actualFormatKind is which of the three readings `actualFormat` asked for.
type actualFormatKind uint8

const (
	formatLayout actualFormatKind = iota
	// formatUnixSeconds is the `unix` keyword, and formatEpochMillis is `epoch`.
	// They are not synonyms — probed and bracketed three ways — and treating
	// them as such is wrong by a factor of a thousand.
	formatUnixSeconds
	formatEpochMillis
)

type actualFormat struct {
	kind   actualFormatKind
	layout string
	source string
}

func (f *actualFormat) parse(raw string) (temporalValue, bool) {
	switch f.kind {
	case formatUnixSeconds, formatEpochMillis:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return temporalValue{}, false
		}
		if f.kind == formatUnixSeconds {
			return temporalValue{kind: kindZoned, t: time.Unix(n, 0).UTC()}, true
		}
		return temporalValue{kind: kindZoned, t: time.UnixMilli(n).UTC()}, true
	default:
		t, err := time.Parse(f.layout, raw)
		if err != nil {
			return temporalValue{}, false
		}
		// Whether the value carries a zone is a property of the pattern, not of
		// the result: time.Parse answers UTC for a zoneless layout, which is
		// indistinguishable from a parsed +00:00.
		kind := kindLocal
		if layoutHasZone(f.layout) {
			kind = kindZoned
		}
		return temporalValue{kind: kind, t: t}, true
	}
}

// truncation is one of WireMock's `DateTimeTruncation` values.
type truncation uint8

const (
	truncNone truncation = iota
	truncFirstSecondOfMinute
	truncFirstMinuteOfHour
	truncFirstHourOfDay
	truncFirstDayOfMonth
	truncFirstDayOfNextMonth
	truncFirstDayOfYear
	truncFirstDayOfNextYear
	truncLastDayOfMonth
	truncLastDayOfYear
)

// truncationNames maps the accepted spellings. WireMock uppercases the value and
// turns spaces into underscores before an enum lookup, so both
// `FIRST_DAY_OF_MONTH` and `first day of month` are accepted and the stored
// document canonicalises to the spaced form.
var truncationNames = map[string]truncation{
	"FIRST_SECOND_OF_MINUTE":  truncFirstSecondOfMinute,
	"FIRST_MINUTE_OF_HOUR":    truncFirstMinuteOfHour,
	"FIRST_HOUR_OF_DAY":       truncFirstHourOfDay,
	"FIRST_DAY_OF_MONTH":      truncFirstDayOfMonth,
	"FIRST_DAY_OF_NEXT_MONTH": truncFirstDayOfNextMonth,
	"FIRST_DAY_OF_YEAR":       truncFirstDayOfYear,
	"FIRST_DAY_OF_NEXT_YEAR":  truncFirstDayOfNextYear,
	"LAST_DAY_OF_MONTH":       truncLastDayOfMonth,
	"LAST_DAY_OF_YEAR":        truncLastDayOfYear,
}

// parseTruncation reads a truncation value, applying WireMock's own
// normalisation before the lookup.
func parseTruncation(s string) (truncation, error) {
	key := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))
	tr, ok := truncationNames[key]
	if !ok {
		return truncNone, fmt.Errorf("%q is not a truncation; expected one of %s",
			s, truncationList())
	}
	return tr, nil
}

func truncationList() string {
	names := make([]string, 0, len(truncationNames))
	for name := range truncationNames {
		names = append(names, strings.ToLower(strings.ReplaceAll(name, "_", " ")))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// apply truncates an instant, keeping its zone. `LAST_DAY_OF_*` zeroes the time
// of day as the `FIRST_` values do — it names a day, not the end of one.
func (tr truncation) apply(t time.Time) time.Time {
	y, mo, d := t.Date()
	h, mi, _ := t.Clock()
	loc := t.Location()

	switch tr {
	case truncFirstSecondOfMinute:
		return time.Date(y, mo, d, h, mi, 0, 0, loc)
	case truncFirstMinuteOfHour:
		return time.Date(y, mo, d, h, 0, 0, 0, loc)
	case truncFirstHourOfDay:
		return time.Date(y, mo, d, 0, 0, 0, 0, loc)
	case truncFirstDayOfMonth:
		return time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	case truncFirstDayOfNextMonth:
		return time.Date(y, mo, 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
	case truncFirstDayOfYear:
		return time.Date(y, time.January, 1, 0, 0, 0, 0, loc)
	case truncFirstDayOfNextYear:
		return time.Date(y+1, time.January, 1, 0, 0, 0, 0, loc)
	case truncLastDayOfMonth:
		return time.Date(y, mo+1, 0, 0, 0, 0, 0, loc)
	case truncLastDayOfYear:
		return time.Date(y, time.December, 31, 0, 0, 0, 0, loc)
	default:
		return t
	}
}

// zoneLayoutTokens are the Go layout elements that carry a zone.
var zoneLayoutTokens = []string{"Z07:00", "Z0700", "Z07", "-07:00", "-0700", "-07", "MST"}

// layoutHasZone reports whether a layout reads an offset or zone name.
func layoutHasZone(layout string) bool {
	for _, tok := range zoneLayoutTokens {
		if strings.Contains(layout, tok) {
			return true
		}
	}
	return false
}

// TemporalSpec is one date-time matcher document, already separated into the
// matcher key and the modifiers that rode along with it.
type TemporalSpec struct {
	// Expected is the operand as written.
	Expected string
	// TruncateExpected, TruncateActual and ActualFormat are empty when absent.
	TruncateExpected string
	TruncateActual   string
	ActualFormat     string
	ApplyTruncLast   bool
	// The Has* flags tell an explicitly empty modifier value apart from an
	// absent key. WireMock refuses `truncateActual: ""` with its enum error and
	// `actualFormat: ""` registers as a matcher that can never match; neither is
	// the same as not writing the key at all, and a bare string cannot say
	// which happened.
	HasTruncateExpected bool
	HasTruncateActual   bool
	HasActualFormat     bool
	// Location is the zone a zoneless value resolves in — the server's, which is
	// what WireMock uses. Nil means time.Local.
	Location *time.Location
}

// FieldError is a refusal that belongs to one modifier key rather than to the
// matcher as a whole, so the 422 can point at the field the author actually
// wrote. Without it every refusal below lands on the matcher name, and a stub
// author who wrote `truncateExpected` is told something is wrong with their
// `equalToDateTime` — which is both true and useless for finding the typo.
type FieldError struct {
	// Field is the modifier key the refusal belongs to.
	Field string
	// Err is the refusal itself.
	Err error
}

func (e *FieldError) Error() string { return e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

func fieldErrf(field, format string, args ...any) error {
	return &FieldError{Field: field, Err: fmt.Errorf(format, args...)}
}

// CompileTemporal builds a date-time matcher, refusing at registration anything
// that could not take effect at match time.
//
// That refusal is the deliberate part. WireMock accepts thirteen operand
// spellings that can never match — `now+2days`, a colon-less `+0300`, a bare
// epoch, a whitespace-padded keyword — answering 201 and then failing every
// request with no diagnostic. It also accepts truncation parameters in
// combinations where they do nothing at all. Every one of those is the
// accept-and-ignore failure mode P3 exists to prevent, so each is a 422 here
// (§5.5).
func CompileTemporal(op string, spec TemporalSpec) (Matcher, error) {
	m := &Temporal{loc: spec.Location, source: spec.Expected}
	if m.loc == nil {
		m.loc = time.Local
	}

	switch op {
	case "before":
		m.op = temporalBefore
	case "after":
		m.op = temporalAfter
	case "equalToDateTime":
		m.op = temporalEqual
	default:
		return nil, fmt.Errorf("%q is not a date-time matcher", op)
	}

	offset, relative, err := parseNowRelative(spec.Expected)
	if err != nil {
		return nil, err
	}
	m.offset, m.relative = offset, relative
	if !relative {
		value, ok := parseDateTime(spec.Expected)
		if !ok {
			return nil, fmt.Errorf("%q is not a date-time this matcher can compare against; "+
				"expected an ISO-8601 instant (an offset must be written with a colon), "+
				"a date, an RFC 1123 date, or a now-relative expression such as \"now +3 days\"",
				spec.Expected)
		}
		m.literal = value
	}

	if spec.HasActualFormat {
		format, err := compileActualFormat(spec.ActualFormat)
		if err != nil {
			return nil, &FieldError{Field: "actualFormat", Err: err}
		}
		m.format = format
	}

	if spec.HasTruncateExpected {
		tr, err := parseTruncation(spec.TruncateExpected)
		if err != nil {
			return nil, fieldErrf("truncateExpected", "truncateExpected: %w", err)
		}
		// Inert on a literal expected, which is a statically detectable case and
		// so a refusal rather than a silence. WireMock applies this parameter
		// only to a now-relative operand.
		if !relative {
			return nil, fieldErrf("truncateExpected",
				"truncateExpected has no effect on the literal date-time %q; "+
					"it applies only to a now-relative expected value such as \"now +3 days\"",
				spec.Expected)
		}
		m.truncExpected = tr
	}

	if spec.HasTruncateActual {
		tr, err := parseTruncation(spec.TruncateActual)
		if err != nil {
			return nil, fieldErrf("truncateActual", "truncateActual: %w", err)
		}
		// Inert whenever the actual is read through a custom pattern: WireMock
		// truncates only a value that parsed to a zoned instant, and a pattern
		// yields one only for `unix`/`epoch`. Also statically detectable.
		if m.format != nil && m.format.kind == formatLayout {
			return nil, fieldErrf("truncateActual",
				"truncateActual has no effect when actualFormat reads the value "+
					"with a pattern; it applies to an ISO-8601 actual carrying an offset, or to unix/epoch")
		}
		m.truncActual = tr
	}

	if spec.ApplyTruncLast && !spec.HasTruncateExpected {
		return nil, fieldErrf("applyTruncationLast",
			"applyTruncationLast has no effect without truncateExpected; "+
				"it chooses whether the truncation or the offset is applied first")
	}
	m.applyTruncLast = spec.ApplyTruncLast

	return m, nil
}

// compileActualFormat reads the `actualFormat` parameter.
func compileActualFormat(source string) (*actualFormat, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "unix":
		return &actualFormat{kind: formatUnixSeconds, source: source}, nil
	case "epoch":
		return &actualFormat{kind: formatEpochMillis, source: source}, nil
	}

	layout, fields := javatime.Translate(source)
	// A pattern that resolves no field is a matcher that can never match.
	// WireMock validates only that a pattern compiles, so `qqqq` and the empty
	// string register there and then fail every request.
	if fields == 0 {
		return nil, fmt.Errorf("actualFormat %q names no date or time field, so it can never match; "+
			"use a Java date pattern such as \"dd/MM/yyyy\", or unix/epoch for a numeric value",
			source)
	}
	return &actualFormat{kind: formatLayout, layout: layout, source: source}, nil
}

// parseNowRelative reads the now-relative operand forms, reporting whether the
// operand was one at all.
//
// The accepted shapes are exactly WireMock's, and the spacing is exact: `now`,
// `now <±n> <units>`, `now <n> <units>`, and a bare `<±n> <units>` with the
// keyword left off. A unit must be plural. Anything close but not equal —
// `now+2days`, `now + 2 days`, a doubled space, trailing text, surrounding
// whitespace — registers on WireMock and then never matches, so it is reported
// here as an error rather than accepted (§5.5).
func parseNowRelative(spec string) (javatime.Offset, bool, error) {
	fields := strings.Split(spec, " ")
	hasNow := len(fields) > 0 && strings.EqualFold(fields[0], "now")

	switch {
	case hasNow && len(fields) == 1:
		return javatime.Offset{}, true, nil
	case hasNow && len(fields) == 3:
		offset, err := javatime.ParseOffsetStrict(fields[1] + " " + fields[2])
		if err != nil {
			return javatime.Offset{}, true, err
		}
		return offset, true, nil
	case hasNow:
		return javatime.Offset{}, true, fmt.Errorf(
			"%q is not a now-relative expression; write it as \"now +3 days\" with single spaces", spec)
	}

	// A bare offset with no keyword is now-relative too, but only when it really
	// is one: two fields, the first a number. Anything else is a literal
	// date-time and is parsed as one.
	if len(fields) == 2 {
		if _, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			offset, err := javatime.ParseOffsetStrict(spec)
			if err != nil {
				return javatime.Offset{}, true, err
			}
			return offset, true, nil
		}
	}
	return javatime.Offset{}, false, nil
}
