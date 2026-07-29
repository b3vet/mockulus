// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// scoreAgainst measures one stub against a request built from the given target,
// body and headers.
func scoreAgainst(t *testing.T, cs *stub.CompiledStub, method, target, body string,
	headers map[string]string) wmcompat.NearMiss {

	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	pr := AcquireRequest(req, []byte(body))
	defer ReleaseRequest(pr)
	return ScoreRequest(cs, pr)
}

// differenceFor returns the recorded difference of a given kind and name, so a
// test can assert on one criterion without depending on the order the others
// were scored in.
func differenceFor(m wmcompat.NearMiss, kind, name string) (wmcompat.Difference, bool) {
	for _, d := range m.Differences {
		if d.Kind == kind && d.Name == name {
			return d, true
		}
	}
	return wmcompat.Difference{}, false
}

// The one ranking claim SPEC §6.8 makes and probing confirmed: a method
// mismatch is total, and outranks a URL that is one character out. Get this
// backwards and the diagnostic leads with the stub that is furthest away.
func TestAMethodMismatchRanksBelowAOneCharacterURLDifference(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "one-char-off",
			`{"request":{"method":"GET","urlPath":"/api/orders"},"response":{}}`),
		mustCompile(t, 2, "wrong-method",
			`{"request":{"method":"POST","urlPath":"/api/order"},"response":{}}`),
	}, 1)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/order", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	misses := snap.NearMisses(pr, wmcompat.NearMissCount)
	if len(misses) != 2 {
		t.Fatalf("scored %d candidates, want 2", len(misses))
	}
	if misses[0].StubID != "one-char-off" {
		t.Errorf("closest stub is %q, want one-char-off — a method mismatch outranked one character of URL",
			misses[0].StubID)
	}
	if misses[0].Distance >= misses[1].Distance {
		t.Errorf("distances %v and %v do not separate the two candidates",
			misses[0].Distance, misses[1].Distance)
	}
}

// Every URL criterion has to be scorable, matched or not. A kind that falls
// through unscored contributes nothing to the mean and makes a stub that cannot
// possibly serve the request look like the closest thing to it.
func TestEveryURLKindIsScoredInBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping string
		hit     string
		miss    string
	}{
		{
			name:    "no URL criterion at all",
			mapping: `{"request":{"method":"GET"},"response":{}}`,
			hit:     "/anything?a=1",
			// A stub with no URL criterion cannot miss on the URL, so its only
			// miss is the one the other rows share.
			miss: "",
		},
		{
			name:    "an exact URL including the query",
			mapping: `{"request":{"url":"/a?b=1"},"response":{}}`,
			hit:     "/a?b=1",
			miss:    "/a?b=2",
		},
		{
			name:    "an exact path",
			mapping: `{"request":{"urlPath":"/a/b"},"response":{}}`,
			hit:     "/a/b",
			miss:    "/a/c",
		},
		{
			name:    "a pattern over the whole URL",
			mapping: `{"request":{"urlPattern":"/a\\?b=[0-9]+"},"response":{}}`,
			hit:     "/a?b=42",
			miss:    "/a?b=x",
		},
		{
			name:    "a pattern over the path",
			mapping: `{"request":{"urlPathPattern":"/a/[0-9]+"},"response":{}}`,
			hit:     "/a/42",
			miss:    "/a/x",
		},
		{
			name:    "a path template",
			mapping: `{"request":{"urlPathTemplate":"/a/{id}/items"},"response":{}}`,
			hit:     "/a/42/items",
			miss:    "/a/42/things",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := mustCompile(t, 1, "candidate", tc.mapping)

			if got := scoreAgainst(t, cs, http.MethodGet, tc.hit, "", nil); got.Distance != 0 {
				t.Errorf("a matching request scored %v with differences %+v, want 0",
					got.Distance, got.Differences)
			}
			if tc.miss == "" {
				return
			}

			got := scoreAgainst(t, cs, http.MethodGet, tc.miss, "", nil)
			if got.Distance <= 0 {
				t.Fatalf("a non-matching request scored %v, want more than 0", got.Distance)
			}
			d, ok := differenceFor(got, "url", "")
			if !ok {
				t.Fatalf("no url difference was recorded: %+v", got.Differences)
			}
			if d.Actual == "" {
				t.Error("the url difference does not say what the request carried")
			}
			if d.Expected == "" {
				t.Error("the url difference does not say what the stub asked for")
			}
		})
	}
}

// A stub with an unrecognised URL kind must be scored as a total miss rather
// than silently counted as exact. The kind is a bare uint8 on the compiled
// stub, so nothing but this default stands between a future kind added to one
// switch and a diagnostic that ranks it top of the list.
func TestAnUnrecognisedURLKindScoresAsATotalMiss(t *testing.T) {
	cs := mustCompile(t, 1, "odd", `{"request":{"method":"GET","urlPath":"/a"},"response":{}}`)
	cs.URLKind = 250

	got := scoreAgainst(t, cs, http.MethodGet, "/a", "", nil)
	if got.Distance == 0 {
		t.Fatalf("an unrecognised URL kind scored as an exact match: %+v", got)
	}
	if _, ok := differenceFor(got, "url", ""); !ok {
		t.Errorf("no url difference was recorded: %+v", got.Differences)
	}
}

// The diagnostic is only worth reading if it names the criterion, what the stub
// asked for and what the request actually carried — for every criterion class,
// not just the URL.
func TestEveryCriterionClassReportsWhatDifferedAndWhatArrived(t *testing.T) {
	cs := mustCompile(t, 1, "picky", `{"request":{
		"method":"POST",
		"urlPath":"/api/thing",
		"headers":{"X-Tenant":{"equalTo":"acme"}},
		"queryParameters":{"page":{"equalTo":"1"}},
		"cookies":{"session":{"equalTo":"good"}},
		"bodyPatterns":[{"contains":"needle"}]
	},"response":{}}`)

	got := scoreAgainst(t, cs, http.MethodGet, "/api/thing?page=9", "haystack", map[string]string{
		"X-Tenant": "other",
		"Cookie":   "session=stale",
	})

	for _, want := range []struct{ kind, name, actual string }{
		{"method", "", http.MethodGet},
		{"header", "X-Tenant", "other"},
		{"query", "page", "9"},
		{"cookie", "session", "stale"},
		{"body", "", "haystack"},
	} {
		d, ok := differenceFor(got, want.kind, want.name)
		if !ok {
			t.Errorf("no %s difference for %q was recorded: %+v", want.kind, want.name, got.Differences)
			continue
		}
		if d.Actual != want.actual {
			t.Errorf("%s %q reports actual %q, want %q", want.kind, want.name, d.Actual, want.actual)
		}
		if d.Expected == "" {
			t.Errorf("%s %q does not say what the stub asked for", want.kind, want.name)
		}
	}

	// The URL matched, so it must not appear as a difference — a scorer that
	// reports criteria that were satisfied is noise rather than diagnosis.
	if d, ok := differenceFor(got, "url", ""); ok {
		t.Errorf("the matching URL was reported as a difference: %+v", d)
	}
}

// An absent header and a header with the wrong value are both misses, and the
// diagnostic has to distinguish them: "you sent nothing" and "you sent the
// wrong thing" send a reader to different places.
func TestAnAbsentCriterionIsReportedWithAnEmptyActual(t *testing.T) {
	cs := mustCompile(t, 1, "needs-header",
		`{"request":{"urlPath":"/h","headers":{"X-Tenant":{"equalTo":"acme"}}},"response":{}}`)

	got := scoreAgainst(t, cs, http.MethodGet, "/h", "", nil)
	d, ok := differenceFor(got, "header", "X-Tenant")
	if !ok {
		t.Fatalf("an absent header was not reported: %+v", got.Differences)
	}
	if d.Actual != "" {
		t.Errorf("actual = %q, want empty for a header that was never sent", d.Actual)
	}
}

// Bodies are truncated so a near miss over a megabyte payload does not become
// the largest thing in the response it is explaining. The boundary is the whole
// rule: one byte either side of it decides whether the reader sees an ellipsis
// that is not there.
func TestABodyIsTruncatedOnlyOnceItIsOverTheLimit(t *testing.T) {
	const limit = 200
	cs := mustCompile(t, 1, "needs-needle",
		`{"request":{"method":"POST","urlPath":"/b","bodyPatterns":[{"contains":"needle"}]},"response":{}}`)

	for _, tc := range []struct {
		name  string
		size  int
		trunc bool
	}{
		{"one byte under the limit", limit - 1, false},
		{"exactly at the limit", limit, false},
		{"one byte over the limit", limit + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("h", tc.size)
			got := scoreAgainst(t, cs, http.MethodPost, "/b", body, nil)

			d, ok := differenceFor(got, "body", "")
			if !ok {
				t.Fatalf("no body difference was recorded: %+v", got.Differences)
			}
			if tc.trunc {
				if d.Actual != strings.Repeat("h", limit)+"…" {
					t.Errorf("actual is %d bytes and does not end in an ellipsis", len(d.Actual))
				}
			} else if d.Actual != body {
				t.Errorf("actual was truncated at %d bytes, want the whole %d-byte body",
					len(d.Actual), tc.size)
			}
		})
	}
}

// The two near-miss endpoints rank in opposite directions but must agree about
// any one pair, which they only do while there is a single scorer. Two would
// drift, and the same endpoint family would answer the same question two ways.
func TestScoringOneStubMatchesWhatTheSnapshotWideScorerSaysAboutIt(t *testing.T) {
	cs := mustCompile(t, 1, "candidate",
		`{"request":{"method":"POST","urlPath":"/api/orders","headers":{"X-Tenant":{"equalTo":"acme"}}},
		  "response":{}}`)
	snap := BuildSnapshot([]*stub.CompiledStub{cs}, 1)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/order", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	fromSnapshot := snap.NearMisses(pr, wmcompat.NearMissCount)
	if len(fromSnapshot) != 1 {
		t.Fatalf("scored %d candidates, want 1", len(fromSnapshot))
	}
	direct := ScoreRequest(cs, pr)

	if direct.Distance != fromSnapshot[0].Distance {
		t.Errorf("ScoreRequest says %v, the snapshot scorer says %v",
			direct.Distance, fromSnapshot[0].Distance)
	}
	if len(direct.Differences) != len(fromSnapshot[0].Differences) {
		t.Errorf("the two scorers disagree about the differences: %+v vs %+v",
			direct.Differences, fromSnapshot[0].Differences)
	}
}

// Three is the reported count: enough to show a pattern, few enough to read.
// Without the cap a deployment with ten thousand stubs answers every unmatched
// request with ten thousand diagnostics.
func TestOnlyTheClosestFewCandidatesAreReported(t *testing.T) {
	stubs := make([]*stub.CompiledStub, 0, 6)
	for i, path := range []string{"/a", "/ab", "/abc", "/abcd", "/abcde", "/abcdef"} {
		stubs = append(stubs, mustCompile(t, uint64(i+1), "s"+path,
			`{"request":{"method":"GET","urlPath":"`+path+`"},"response":{}}`))
	}
	snap := BuildSnapshot(stubs, 1)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/abc", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	if got := snap.NearMisses(pr, wmcompat.NearMissCount); len(got) != wmcompat.NearMissCount {
		t.Errorf("reported %d candidates, want %d", len(got), wmcompat.NearMissCount)
	}
	if got := snap.NearMisses(pr, 0); len(got) != len(stubs) {
		t.Errorf("an unlimited request reported %d candidates, want all %d", len(got), len(stubs))
	}
}

// There is nothing to be near when there is nothing registered, and the
// diagnostic must degrade to the plain message rather than an empty heading
// promising stubs that do not exist.
func TestAnEmptySnapshotHasNoNearMissesAndNoDiagnosticHeading(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	if got := EmptySnapshot().NearMisses(pr, wmcompat.NearMissCount); got != nil {
		t.Errorf("near misses over an empty snapshot = %+v, want nil", got)
	}
	if got := DiagnosticBody(EmptySnapshot(), pr); got != UnmatchedBody {
		t.Errorf("diagnostic body = %q, want the plain %q", got, UnmatchedBody)
	}
}

// The rendered body is what a developer reads when their test 404s, so it has
// to carry the stub's name as well as its id: an id alone sends them to the
// admin API to find out which stub it is.
func TestTheDiagnosticBodyNamesTheClosestStubsAndWhatDiffered(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "id-orders",
			`{"name":"list orders","request":{"method":"GET","urlPath":"/api/orders"},"response":{}}`),
	}, 1)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/order", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	body := DiagnosticBody(snap, pr)
	for _, want := range []string{
		UnmatchedBody, "Closest stubs:", "list orders", "id-orders",
		"url: expected /api/orders, got /api/order",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the diagnostic body does not contain %q:\n%s", want, body)
		}
	}
	// The heading is joined to the unmatched message by exactly one newline;
	// the renderer's own leading blank line is trimmed off.
	if strings.Contains(body, UnmatchedBody+"\n\n") {
		t.Errorf("the diagnostic body has a blank line between the message and the heading:\n%q", body)
	}
}

// `/near-misses/request-pattern` ranks recorded requests against one pattern,
// and the request that actually satisfies it has to come out at zero. A scorer
// that only counted the criteria that failed would give the same score to a
// stub matching six criteria and a stub matching none.
func TestAStubTheRequestFullySatisfiesScoresZeroOnEveryCriterion(t *testing.T) {
	cs := mustCompile(t, 1, "satisfied", `{"request":{
		"method":"POST",
		"urlPath":"/api/thing",
		"headers":{"X-Tenant":{"equalTo":"acme"}},
		"queryParameters":{"page":{"equalTo":"1"}},
		"cookies":{"session":{"equalTo":"good"}},
		"bodyPatterns":[{"contains":"needle"}]
	},"response":{}}`)

	got := scoreAgainst(t, cs, http.MethodPost, "/api/thing?page=1", "a needle in it", map[string]string{
		"X-Tenant": "acme",
		"Cookie":   "session=good",
	})

	if got.Distance != 0 {
		t.Errorf("a request satisfying every criterion scored %v, want 0", got.Distance)
	}
	if len(got.Differences) != 0 {
		t.Errorf("differences = %+v, want none", got.Differences)
	}
	if got.StubID != "satisfied" {
		t.Errorf("stub id = %q, want satisfied", got.StubID)
	}
}
