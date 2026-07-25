// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// The evidence contract is the anti-vacuity backstop: a case asserting only
// `status: 200` must not be able to claim it covers an error code or a config
// knob. These pin what does and does not count as evidence, because widening it
// by accident would quietly hollow out every coverage claim in the catalog.

func TestEvidenceComesFromTheSteps(t *testing.T) {
	c := &Case{Steps: []Step{{
		Admin:  &HTTPStep{Method: "POST", Path: "/__admin/settings", Body: `{"fixedDelay":50}`},
		Expect: &Expect{Status: 422},
	}}}

	if !satisfiesEvidence(c, []string{"/__admin/settings"}) {
		t.Error("a path the case actually requests is evidence")
	}
	if !satisfiesEvidence(c, []string{"fixedDelay", "422"}) {
		t.Error("every token must be found, and these all are")
	}
	if satisfiesEvidence(c, []string{"1005"}) {
		t.Error("a token the case never asserts must not count")
	}
}

// A start-up-only knob has no step that can name it: the naming happened before
// the process existed. Such behaviors carry a variant name as their token, so
// the declaration has to count — otherwise the only way to satisfy them would
// be to write the token into a comment, which proves nothing.
func TestVariantAndCapabilityDeclarationsAreEvidence(t *testing.T) {
	c := &Case{
		Config:   "file-store",
		Requires: []string{"couchbase", "multi-pod"},
		Steps: []Step{{
			Request: &HTTPStep{Method: "GET", Path: "/e2e/x/hello"},
			Expect:  &Expect{Status: 200},
		}},
	}

	if !satisfiesEvidence(c, []string{"file-store"}) {
		t.Error("the variant a case runs under is what proves a start-up-only knob")
	}
	if !satisfiesEvidence(c, []string{"couchbase"}) {
		t.Error("a declared capability is evidence the case ran against that store")
	}
	if satisfiesEvidence(c, []string{"tls"}) {
		t.Error("an undeclared variant must not count")
	}
}

// Widening the rendered text must not let a case claim a behavior by naming it:
// the case id and the behavior list are metadata, not assertions.
func TestCaseMetadataIsNotEvidence(t *testing.T) {
	c := &Case{
		ID:        "errors-code-1005-001",
		Behaviors: []string{"B-ERR-1005"},
		Steps: []Step{{
			Request: &HTTPStep{Method: "GET", Path: "/e2e/x/hello"},
			Expect:  &Expect{Status: 200},
		}},
	}

	if satisfiesEvidence(c, []string{"1005"}) {
		t.Error("naming a code in the case id is not asserting it")
	}
	if satisfiesEvidence(c, []string{"B-ERR-1005"}) {
		t.Error("claiming a behavior is not evidence for it — that is the vacuity this prevents")
	}
}

func TestNoTokensIsSatisfiedByAnything(t *testing.T) {
	// The catalog lint separately requires every entry to declare tokens, so an
	// empty list here means "not applicable", not "unchecked".
	if !satisfiesEvidence(&Case{}, nil) {
		t.Error("a behavior with no declared tokens has nothing to check")
	}
}
