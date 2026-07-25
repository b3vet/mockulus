// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"testing"

	"github.com/b3vet/mockulus/internal/wmcompat"
)

func settingsOK(t *testing.T, doc string) Settings {
	t.Helper()
	set, errs := CompileSettings([]byte(doc))
	if errs != nil {
		t.Fatalf("should compile: %s\nproblems: %v", doc, errs.Errors())
	}
	return set
}

func settingsErrs(t *testing.T, doc string) []wmcompat.Error {
	t.Helper()
	set, errs := CompileSettings([]byte(doc))
	if errs == nil {
		t.Fatalf("should have been rejected: %s", doc)
	}
	if !set.IsZero() {
		t.Errorf("a rejected settings document must not also produce settings")
	}
	return errs.Errors()
}

func TestSettingsDelayCompiles(t *testing.T) {
	set := settingsOK(t, `{"fixedDelay":250,"delayDistribution":{"type":"uniform","lower":10,"upper":30}}`)
	if set.FixedDelay.Milliseconds() != 250 {
		t.Errorf("fixed delay = %s", set.FixedDelay)
	}
	if set.Delay.Kind != DelayUniform || set.Delay.Upper.Milliseconds() != 30 {
		t.Errorf("distribution = %+v", set.Delay)
	}
	if set.IsZero() {
		t.Error("settings that ask for a delay are not zero")
	}
}

// An empty document is how a suite gives the deployment back, so it has to be
// accepted and has to read as "nothing configured" rather than as a value.
func TestSettingsEmptyIsZero(t *testing.T) {
	if !settingsOK(t, `{}`).IsZero() {
		t.Error("an empty settings document asks for nothing")
	}
	// An explicit null clears one key without touching the other, which is what
	// WireMock reads it as too.
	set := settingsOK(t, `{"fixedDelay":40,"delayDistribution":null}`)
	if set.Delay.Kind != DelayNone || set.FixedDelay.Milliseconds() != 40 {
		t.Errorf("settings = %+v", set)
	}
}

// The keys WireMock has and mockulus does not are the ones a migrating team is
// most likely to send, and accepting one silently would look like a setting
// that did not work rather than one that does not exist.
func TestSettingsUnknownKeyRejected(t *testing.T) {
	problems := settingsErrs(t, `{"proxyPassThrough":true}`)
	if !hasProblem(problems, wmcompat.CodeUnknownSetting, "/proxyPassThrough") {
		t.Errorf("want 1005 at /proxyPassThrough, got %v", problems)
	}
	if got := wmcompat.StatusFor(wmcompat.CodeUnknownSetting); got != 422 {
		t.Errorf("1005 is reported with %d, want 422", got)
	}

	// Every problem in one response, not just the first.
	problems = settingsErrs(t, `{"extended":{},"snapshotRecord":true}`)
	if len(problems) != 2 {
		t.Errorf("want both unknown keys reported, got %v", problems)
	}
}

func TestSettingsMalformedRejected(t *testing.T) {
	for _, doc := range []string{
		`{"fixedDelay":-1}`,
		`{"fixedDelay":"250"}`,
		`{"delayDistribution":{"type":"weibull","lower":1,"upper":2}}`,
		`{"delayDistribution":{"type":"uniform","lower":30,"upper":10}}`,
		`[]`,
	} {
		if problems := settingsErrs(t, doc); !hasProblem(problems, wmcompat.CodeMalformed, "") {
			t.Errorf("%s: want a malformed-request problem, got %v", doc, problems)
		}
	}
}

// The global delay composes by replacement, so a stub that says nothing has to
// be distinguishable from one that says zero (SPEC §12.4).
func TestFixedDelayDeclarationIsRecorded(t *testing.T) {
	if cs := compileOK(t, `{"response":{"status":200}}`); cs.Response.FixedDelaySet {
		t.Error("a stub that named no fixed delay must not read as having declared one")
	}
	cs := compileOK(t, `{"response":{"status":200,"fixedDelayMilliseconds":0}}`)
	if !cs.Response.FixedDelaySet || cs.Response.FixedDelay != 0 {
		t.Errorf("an explicit zero is a declaration: set=%v delay=%s",
			cs.Response.FixedDelaySet, cs.Response.FixedDelay)
	}
}
