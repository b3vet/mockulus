// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// The runner is the thing that decides whether everything else is correct, so
// the rules it decides by need to be pinned themselves. A diff that quietly
// stops comparing bodies would turn the whole compatibility claim into a green
// tick with nothing behind it, and nothing downstream would notice.

func resp(status int, body string, headers map[string]string) *NormalizedResponse {
	h := map[string]string{}
	for k, v := range headers {
		h[strings.ToLower(k)] = v
	}
	return &NormalizedResponse{Status: status, Header: h, Body: []byte(body)}
}

// Subset semantics: WireMock's fields must all be present and equal in ours,
// and extra fields on our side are the catalogued-extension case (SPEC §5.6),
// not a diff.
func TestDiffBodiesUsesSubsetSemantics(t *testing.T) {
	cases := []struct {
		name     string
		theirs   string
		ours     string
		wantDiff bool
		why      string
	}{
		{
			name:   "identical",
			theirs: `{"status":"healthy"}`, ours: `{"status":"healthy"}`,
			wantDiff: false, why: "the same document cannot differ",
		},
		{
			name:   "additive field on our side",
			theirs: `{"status":"healthy"}`, ours: `{"status":"healthy","stubs":3}`,
			wantDiff: false,
			why:      "mockulus detail alongside every WireMock-defined field is an extension",
		},
		{
			name:   "field WireMock returned is missing",
			theirs: `{"status":"healthy","uptime":1}`, ours: `{"status":"healthy"}`,
			wantDiff: true,
			why:      "a client reading a field WireMock sends must keep working",
		},
		{
			name:   "same field, different value",
			theirs: `{"status":"healthy"}`, ours: `{"status":"degraded"}`,
			wantDiff: true, why: "a value difference is the diff this exists to catch",
		},
		{
			name:   "nested field missing",
			theirs: `{"meta":{"total":2}}`, ours: `{"meta":{}}`,
			wantDiff: true, why: "subset semantics apply at every depth, not just the root",
		},
		{
			name:   "one side is not JSON",
			theirs: `{"a":1}`, ours: `plain text`,
			wantDiff: true, why: "a shape change is a compatibility difference",
		},
		{
			name:   "both non-JSON and equal",
			theirs: `hello`, ours: `hello`,
			wantDiff: false, why: "identical text bodies agree",
		},
		{
			name:   "both non-JSON and different",
			theirs: `hello`, ours: `goodbye`,
			wantDiff: true, why: "differing text bodies disagree",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diffs := DiffResponses(resp(200, c.theirs, nil), resp(200, c.ours, nil), nil, true)
			if got := len(diffs) > 0; got != c.wantDiff {
				t.Errorf("diffs=%v, want difference=%v (%s)", diffs, c.wantDiff, c.why)
			}
		})
	}
}

func TestDiffReportsStatusAndHeaders(t *testing.T) {
	diffs := DiffResponses(
		resp(200, `{}`, map[string]string{"Content-Type": "application/json"}),
		resp(404, `{}`, map[string]string{"Content-Type": "text/plain"}),
		nil, true)

	if len(diffs) != 2 {
		t.Fatalf("expected a status diff and a header diff, got %v", diffs)
	}
	joined := strings.Join(diffs, " ")
	for _, want := range []string{"status", "content-type"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff %q does not mention %s", joined, want)
		}
	}
}

// A header WireMock does not send but mockulus does is additive, exactly as an
// additive body field is.
func TestExtraHeaderOnOurSideIsNotADiff(t *testing.T) {
	diffs := DiffResponses(
		resp(200, `{}`, map[string]string{"Content-Type": "application/json"}),
		resp(200, `{}`, map[string]string{"Content-Type": "application/json", "X-Mockulus": "1"}),
		nil, true)
	if len(diffs) > 0 {
		t.Errorf("an extra header on our side should not be a diff, got %v", diffs)
	}
}

// The narrow ignore is the point: it gives up the unmatched 404 body and
// nothing else. A case declaring it must still have its 200 bodies compared,
// which is what makes the ignore safe to use widely.
func TestIgnoreUnmatchedBodyIsScopedToTheUnmatched404(t *testing.T) {
	ignore := []string{IgnoreUnmatchedBody}

	if diffs := DiffResponses(
		resp(404, "WireMock near-miss table…", nil),
		resp(404, "No response could be served…", nil),
		ignore, true); len(diffs) > 0 {
		t.Errorf("the unmatched 404 body is a documented deviation, got %v", diffs)
	}

	if diffs := DiffResponses(
		resp(200, `{"v":"theirs"}`, nil),
		resp(200, `{"v":"ours"}`, nil),
		ignore, true); len(diffs) == 0 {
		t.Error("a 200 body difference must still be reported: that is what the narrow ignore buys")
	}

	// The deviation is a property of the mock port. An admin 404 body is not
	// covered by it, so it stays compared.
	if diffs := DiffResponses(
		resp(404, `{"errors":[{"code":10}]}`, nil),
		resp(404, ``, nil),
		ignore, false); len(diffs) == 0 {
		t.Error("an admin 404 body is not the deviation and must still be compared")
	}

	// A status difference is never ignorable, whatever the bodies say.
	if diffs := DiffResponses(
		resp(404, "x", nil), resp(200, "y", nil), ignore, true); len(diffs) == 0 {
		t.Error("a status difference must survive every ignore")
	}
}

// The listing rule is the narrow one again: it gives up the order and the size
// of a deployment-global collection, because the shared instance and the
// per-case oracle hold different collections — and nothing else. The entries
// themselves stay compared, which is where the compatibility claim lives.
func TestCompareListingByIdentity(t *testing.T) {
	ignore := []string{CompareListingByIdentity}
	const oneStub = `{"mappings":[{"id":"a","response":{"body":"world"}}],"meta":{"total":1}}`

	if diffs := DiffResponses(
		resp(200, oneStub, nil),
		resp(200, `{"mappings":[{"id":"z"},{"id":"a","response":{"body":"world"}}],`+
			`"meta":{"total":2}}`, nil),
		ignore, false); len(diffs) > 0 {
		t.Errorf("another case's stub ahead of ours in a shared listing is not a diff, got %v", diffs)
	}

	if diffs := DiffResponses(
		resp(200, oneStub, nil),
		resp(200, `{"mappings":[{"id":"z"}],"meta":{"total":1}}`, nil),
		ignore, false); len(diffs) == 0 {
		t.Error("a mapping WireMock listed and mockulus did not is the diff this must still catch")
	}

	if diffs := DiffResponses(
		resp(200, oneStub, nil),
		resp(200, `{"mappings":[{"id":"a","response":{"body":"goodbye"}}],"meta":{"total":1}}`, nil),
		ignore, false); len(diffs) == 0 {
		t.Error("the listed document must still be compared: that is what the rule buys")
	}

	// Dropping the envelope entirely is a shape change, not a scheduling artifact.
	if diffs := DiffResponses(
		resp(200, oneStub, nil),
		resp(200, `{"mappings":[{"id":"a","response":{"body":"world"}}]}`, nil),
		ignore, false); len(diffs) == 0 {
		t.Error("a listing that stopped carrying meta must still be a diff")
	}

	// The rule is scoped to listings, so declaring it on a case leaves that
	// case's other steps under the ordinary comparison.
	if diffs := DiffResponses(
		resp(404, `{"errors":[{"code":10,"title":"theirs"}]}`, nil),
		resp(404, `{"errors":[{"code":10,"title":"ours"}]}`, nil),
		ignore, false); len(diffs) == 0 {
		t.Error("an error body carrying an array is not a listing and must stay compared")
	}
}

func TestIgnoreWholeBodySkipsEveryBody(t *testing.T) {
	diffs := DiffResponses(
		resp(200, `{"v":"theirs"}`, nil),
		resp(200, `{"v":"ours"}`, nil),
		[]string{IgnoreWholeBody}, true)
	if len(diffs) > 0 {
		t.Errorf("$body suppresses body comparison entirely, got %v", diffs)
	}
}

// Named JSON fields are dropped from WireMock's document before the subset
// check, so they are neither required nor compared — that is how a case ignores
// a server-identifying field without giving up the rest of the body.
func TestNamedFieldIgnoreDropsOnlyThatField(t *testing.T) {
	ignore := []string{"version"}

	if diffs := DiffResponses(
		resp(200, `{"version":"3.13.2","status":"healthy"}`, nil),
		resp(200, `{"version":"0.1.0","status":"healthy"}`, nil),
		ignore, true); len(diffs) > 0 {
		t.Errorf("the ignored field should not be compared, got %v", diffs)
	}

	if diffs := DiffResponses(
		resp(200, `{"version":"3.13.2","status":"healthy"}`, nil),
		resp(200, `{"version":"0.1.0","status":"degraded"}`, nil),
		ignore, true); len(diffs) == 0 {
		t.Error("ignoring one field must not stop the others being compared")
	}
}
