// SPDX-License-Identifier: Apache-2.0

package match

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/regexx"
	"github.com/b3vet/mockulus/internal/stub"
)

func testStubOptions() stub.Options {
	return stub.Options{
		CompileRegex: func(pattern string) (matchers.PatternMatcher, error) {
			return regexx.Compile(pattern, regexx.Options{Anchored: true})
		},
	}
}

// mustCompile builds a stub from JSON, failing the test on any problem.
//
// seq fixes precedence, so tests that care about ordering pass it explicitly.
// The id is supplied here rather than in the document because a registered stub
// id must be a UUID, and a readable name makes the assertions legible.
func mustCompile(t *testing.T, seq uint64, id, doc string) *stub.CompiledStub {
	t.Helper()
	cs, errs := stub.Compile([]byte(doc), seq, testStubOptions())
	if errs != nil {
		t.Fatalf("compile %s: %v", doc, errs.Errors())
	}
	cs.ID = id
	return cs
}

// match runs a request against a snapshot and returns the selected stub's id,
// or "" when nothing matched.
func match(t *testing.T, snap *Snapshot, method, target string, body string, headers map[string]string) string {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	pr := AcquireRequest(req, []byte(body))
	defer ReleaseRequest(pr)

	cs := snap.Match(pr, nil, nil)
	if cs == nil {
		return ""
	}
	return cs.ID
}

func TestMethodMatching(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "get", `{"request":{"method":"GET","urlPath":"/x"},"response":{}}`),
		mustCompile(t, 2, "post", `{"request":{"method":"POST","urlPath":"/x"},"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/x", "", nil); got != "get" {
		t.Errorf("GET selected %q, want get", got)
	}
	if got := match(t, snap, "POST", "/x", "", nil); got != "post" {
		t.Errorf("POST selected %q, want post", got)
	}
	if got := match(t, snap, "DELETE", "/x", "", nil); got != "" {
		t.Errorf("DELETE selected %q, want no match", got)
	}
}

// ANY is a wildcard over methods, and must be reachable through the index as
// well as through the pattern list.
func TestAnyMethodIsAWildcard(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "any", `{"request":{"method":"ANY","urlPath":"/x"},"response":{}}`),
	}, 1)

	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		if got := match(t, snap, method, "/x", "", nil); got != "any" {
			t.Errorf("%s selected %q, want any", method, got)
		}
	}
}

// An absent request object matches everything, as WireMock's anyUrl() does.
func TestAbsentRequestMatchesEverything(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "catchall", `{"response":{"status":418}}`),
	}, 1)

	if got := match(t, snap, "GET", "/anything/at/all?x=1", "", nil); got != "catchall" {
		t.Errorf("selected %q, want catchall", got)
	}
}

func TestURLCriteriaDistinguishPathFromQuery(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "full", `{"request":{"url":"/x?a=1"},"response":{}}`),
		mustCompile(t, 2, "path", `{"request":{"urlPath":"/y"},"response":{}}`),
	}, 1)

	// `url` is byte-exact over path and query.
	if got := match(t, snap, "GET", "/x?a=1", "", nil); got != "full" {
		t.Errorf("exact url selected %q", got)
	}
	if got := match(t, snap, "GET", "/x?a=2", "", nil); got != "" {
		t.Errorf("a different query should not match a byte-exact url, got %q", got)
	}
	if got := match(t, snap, "GET", "/x", "", nil); got != "" {
		t.Errorf("no query should not match a url that has one, got %q", got)
	}

	// `urlPath` ignores the query entirely.
	if got := match(t, snap, "GET", "/y", "", nil); got != "path" {
		t.Errorf("urlPath selected %q", got)
	}
	if got := match(t, snap, "GET", "/y?anything=here", "", nil); got != "path" {
		t.Errorf("urlPath should ignore the query, got %q", got)
	}
}

// Query parameter order is part of a byte-exact url criterion, which is
// WireMock's semantics and a real source of surprise, so it is pinned here.
func TestExactURLIsSensitiveToQueryOrder(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "ordered", `{"request":{"url":"/x?a=1&b=2"},"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/x?a=1&b=2", "", nil); got != "ordered" {
		t.Errorf("same order selected %q", got)
	}
	if got := match(t, snap, "GET", "/x?b=2&a=1", "", nil); got != "" {
		t.Errorf("reordered query matched a byte-exact url, got %q", got)
	}
}

func TestURLPatterns(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "pat", `{"request":{"urlPattern":"/orders/[0-9]+\\?full=true"},"response":{}}`),
		mustCompile(t, 2, "pathpat", `{"request":{"urlPathPattern":"/items/[a-z]+"},"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/orders/42?full=true", "", nil); got != "pat" {
		t.Errorf("urlPattern selected %q", got)
	}
	if got := match(t, snap, "GET", "/orders/abc?full=true", "", nil); got != "" {
		t.Errorf("non-matching urlPattern selected %q", got)
	}
	if got := match(t, snap, "GET", "/items/widget", "", nil); got != "pathpat" {
		t.Errorf("urlPathPattern selected %q", got)
	}
	// urlPathPattern matches the path only, so a query does not defeat it.
	if got := match(t, snap, "GET", "/items/widget?x=1", "", nil); got != "pathpat" {
		t.Errorf("urlPathPattern should ignore the query, got %q", got)
	}
	// Regex criteria require a full match, so a longer path does not match.
	if got := match(t, snap, "GET", "/items/widget/extra", "", nil); got != "" {
		t.Errorf("regex matching must be anchored, got %q", got)
	}
}

func TestPathTemplateAndPathParameters(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "tpl", `{"request":{"urlPathTemplate":"/orders/{id}/items/{itemId}",
			           "pathParameters":{"id":{"matches":"[0-9]+"}}},
			"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/orders/42/items/7", "", nil); got != "tpl" {
		t.Errorf("template selected %q", got)
	}
	// The pathParameters criterion gates the match on a bound variable.
	if got := match(t, snap, "GET", "/orders/abc/items/7", "", nil); got != "" {
		t.Errorf("a path parameter failing its matcher should not match, got %q", got)
	}
	if got := match(t, snap, "GET", "/orders/42/items", "", nil); got != "" {
		t.Errorf("a path with too few segments matched, got %q", got)
	}
}

// A failed template match must not leave bindings behind that a later stub's
// criteria could see.
func TestPathVariablesDoNotLeakBetweenCandidates(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		// Higher priority, evaluated first, binds {id} then fails on its matcher.
		mustCompile(t, 1, "first", `{"priority":1,
			"request":{"urlPathTemplate":"/x/{id}","pathParameters":{"id":{"equalTo":"nope"}}},
			"response":{}}`),
		// Lower priority; its criterion names a variable its own template binds.
		mustCompile(t, 2, "second", `{"priority":2,
			"request":{"urlPathTemplate":"/x/{id}","pathParameters":{"id":{"equalTo":"real"}}},
			"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/x/real", "", nil); got != "second" {
		t.Errorf("selected %q, want second", got)
	}
	if got := match(t, snap, "GET", "/x/nope", "", nil); got != "first" {
		t.Errorf("selected %q, want first", got)
	}
}

func TestHeaderQueryCookieCriteria(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "hdr", `{"request":{"urlPath":"/h",
			"headers":{"Content-Type":{"contains":"json"},"X-Legacy":{"absent":true}}},"response":{}}`),
		mustCompile(t, 2, "qry", `{"request":{"urlPath":"/q",
			"queryParameters":{"dryRun":{"equalTo":"true"}}},"response":{}}`),
		mustCompile(t, 3, "cke", `{"request":{"urlPath":"/c",
			"cookies":{"session":{"matches":"[a-f0-9]+"}}},"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/h", "", map[string]string{"Content-Type": "application/json"}); got != "hdr" {
		t.Errorf("header criteria selected %q", got)
	}
	if got := match(t, snap, "GET", "/h", "", map[string]string{"Content-Type": "text/plain"}); got != "" {
		t.Errorf("a failing header criterion matched, got %q", got)
	}
	if got := match(t, snap, "GET", "/h", "", map[string]string{
		"Content-Type": "application/json", "X-Legacy": "1"}); got != "" {
		t.Errorf("an absent criterion should fail when the header is present, got %q", got)
	}

	if got := match(t, snap, "GET", "/q?dryRun=true", "", nil); got != "qry" {
		t.Errorf("query criteria selected %q", got)
	}
	if got := match(t, snap, "GET", "/q?dryRun=false", "", nil); got != "" {
		t.Errorf("a failing query criterion matched, got %q", got)
	}

	if got := match(t, snap, "GET", "/c", "", map[string]string{"Cookie": "session=deadbeef"}); got != "cke" {
		t.Errorf("cookie criteria selected %q", got)
	}
	if got := match(t, snap, "GET", "/c", "", map[string]string{"Cookie": "session=NOTHEX"}); got != "" {
		t.Errorf("a failing cookie criterion matched, got %q", got)
	}
}

func TestBasicAuthCriterion(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "auth", `{"request":{"urlPath":"/secure",
			"basicAuthCredentials":{"username":"user","password":"pass"}},"response":{}}`),
	}, 1)

	// "user:pass" base64-encoded.
	ok := map[string]string{"Authorization": "Basic dXNlcjpwYXNz"}
	if got := match(t, snap, "GET", "/secure", "", ok); got != "auth" {
		t.Errorf("correct credentials selected %q", got)
	}
	if got := match(t, snap, "GET", "/secure", "", map[string]string{"Authorization": "Basic d3Jvbmc="}); got != "" {
		t.Errorf("wrong credentials matched, got %q", got)
	}
	if got := match(t, snap, "GET", "/secure", "", nil); got != "" {
		t.Errorf("no credentials matched, got %q", got)
	}
}

func TestBodyPatternsAreConjunctive(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "body", `{"request":{"method":"POST","urlPath":"/b",
			"bodyPatterns":[{"contains":"order"},{"doesNotContain":"draft"}]},"response":{}}`),
	}, 1)

	if got := match(t, snap, "POST", "/b", `{"kind":"order"}`, nil); got != "body" {
		t.Errorf("both patterns satisfied selected %q", got)
	}
	if got := match(t, snap, "POST", "/b", `{"kind":"draft order"}`, nil); got != "" {
		t.Errorf("every listed pattern must match, got %q", got)
	}
	if got := match(t, snap, "POST", "/b", `{"kind":"invoice"}`, nil); got != "" {
		t.Errorf("a failing pattern matched, got %q", got)
	}
}

func TestFormParameters(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "form", `{"request":{"method":"POST","urlPath":"/f",
			"formParameters":{"kind":{"equalTo":"order"}}},"response":{}}`),
	}, 1)

	formHeaders := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if got := match(t, snap, "POST", "/f", "kind=order&x=1", formHeaders); got != "form" {
		t.Errorf("form criteria selected %q", got)
	}
	if got := match(t, snap, "POST", "/f", "kind=invoice", formHeaders); got != "" {
		t.Errorf("a failing form criterion matched, got %q", got)
	}
}

// Selection order is priority ascending, then insertion sequence descending —
// so among equal priorities the most recently added stub wins (SPEC §5.3).
func TestSelectionOrder(t *testing.T) {
	t.Run("lower priority number wins", func(t *testing.T) {
		snap := BuildSnapshot([]*stub.CompiledStub{
			mustCompile(t, 1, "low", `{"priority":5,"request":{"urlPath":"/p"},"response":{}}`),
			mustCompile(t, 2, "high", `{"priority":1,"request":{"urlPath":"/p"},"response":{}}`),
		}, 1)
		if got := match(t, snap, "GET", "/p", "", nil); got != "high" {
			t.Errorf("selected %q, want high (priority 1)", got)
		}
	})

	t.Run("newest wins among equal priorities", func(t *testing.T) {
		snap := BuildSnapshot([]*stub.CompiledStub{
			mustCompile(t, 1, "older", `{"request":{"urlPath":"/p"},"response":{}}`),
			mustCompile(t, 2, "newer", `{"request":{"urlPath":"/p"},"response":{}}`),
		}, 1)
		if got := match(t, snap, "GET", "/p", "", nil); got != "newer" {
			t.Errorf("selected %q, want newer", got)
		}
	})

	t.Run("priority beats recency", func(t *testing.T) {
		snap := BuildSnapshot([]*stub.CompiledStub{
			mustCompile(t, 1, "old-high", `{"priority":1,"request":{"urlPath":"/p"},"response":{}}`),
			mustCompile(t, 9, "new-low", `{"priority":9,"request":{"urlPath":"/p"},"response":{}}`),
		}, 1)
		if got := match(t, snap, "GET", "/p", "", nil); got != "old-high" {
			t.Errorf("selected %q, want old-high", got)
		}
	})

	t.Run("an absent priority behaves as 5", func(t *testing.T) {
		snap := BuildSnapshot([]*stub.CompiledStub{
			mustCompile(t, 9, "default", `{"request":{"urlPath":"/p"},"response":{}}`),
			mustCompile(t, 1, "four", `{"priority":4,"request":{"urlPath":"/p"},"response":{}}`),
		}, 1)
		// Priority 4 beats the default 5 even though the default was added later.
		if got := match(t, snap, "GET", "/p", "", nil); got != "four" {
			t.Errorf("selected %q, want four — the default priority should be 5", got)
		}
	})
}

// A stub whose criteria fail must not shadow a lower-precedence stub that
// matches: iteration continues rather than stopping at the first candidate.
func TestNonMatchingHigherPrecedenceStubDoesNotShadow(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 2, "specific", `{"request":{"urlPath":"/s",
			"headers":{"X-Want":{"equalTo":"yes"}}},"response":{}}`),
		mustCompile(t, 1, "general", `{"request":{"urlPath":"/s"},"response":{}}`),
	}, 1)

	if got := match(t, snap, "GET", "/s", "", map[string]string{"X-Want": "yes"}); got != "specific" {
		t.Errorf("selected %q, want specific", got)
	}
	if got := match(t, snap, "GET", "/s", "", nil); got != "general" {
		t.Errorf("selected %q, want general — a failing candidate must not shadow", got)
	}
}

// An exact-URL stub and a pattern stub are held in different indexes; the merge
// must interleave them by precedence rather than preferring one index.
func TestExactAndPatternCandidatesMergeByPrecedence(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "pattern", `{"priority":1,"request":{"urlPathPattern":"/m/.*"},"response":{}}`),
		mustCompile(t, 2, "exact", `{"priority":2,"request":{"urlPath":"/m/x"},"response":{}}`),
	}, 1)
	if got := match(t, snap, "GET", "/m/x", "", nil); got != "pattern" {
		t.Errorf("selected %q, want pattern — priority must win across indexes", got)
	}

	snap2 := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "pattern", `{"priority":2,"request":{"urlPathPattern":"/m/.*"},"response":{}}`),
		mustCompile(t, 2, "exact", `{"priority":1,"request":{"urlPath":"/m/x"},"response":{}}`),
	}, 1)
	if got := match(t, snap2, "GET", "/m/x", "", nil); got != "exact" {
		t.Errorf("selected %q, want exact", got)
	}
}

func TestScenarioGateSkipsAndContinues(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 2, "gated", `{"scenarioName":"flow","requiredScenarioState":"Started",
			"request":{"urlPath":"/g"},"response":{}}`),
		mustCompile(t, 1, "plain", `{"request":{"urlPath":"/g"},"response":{}}`),
	}, 1)

	req := httptest.NewRequest("GET", "/g", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	// The gate accepts: the newer, gated stub wins.
	allow := ScenarioGate(func(*stub.ScenarioRef, *ParsedRequest) bool { return true })
	if cs := snap.Match(pr, allow, nil); cs == nil || cs.ID != "gated" {
		t.Errorf("with the gate open, selected %v, want gated", idOf(cs))
	}

	// The gate refuses: iteration continues to the ungated stub rather than
	// falling through to a 404.
	deny := ScenarioGate(func(*stub.ScenarioRef, *ParsedRequest) bool { return false })
	if cs := snap.Match(pr, deny, nil); cs == nil || cs.ID != "plain" {
		t.Errorf("with the gate closed, selected %v, want plain", idOf(cs))
	}
}

func idOf(cs *stub.CompiledStub) string {
	if cs == nil {
		return "<no match>"
	}
	return cs.ID
}

func TestSnapshotDerivesScenarioStates(t *testing.T) {
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "a", `{"scenarioName":"order","requiredScenarioState":"Started",
			"newScenarioState":"created","request":{"urlPath":"/o"},"response":{}}`),
		mustCompile(t, 2, "b", `{"scenarioName":"order","requiredScenarioState":"created",
			"newScenarioState":"shipped","request":{"urlPath":"/o"},"response":{}}`),
	}, 1)

	states := snap.Scenarios()["order"]
	want := map[string]bool{"Started": true, "created": true, "shipped": true}
	if len(states) != len(want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	for _, s := range states {
		if !want[s] {
			t.Errorf("unexpected state %q", s)
		}
	}
}

func TestEmptySnapshotMatchesNothing(t *testing.T) {
	if got := match(t, EmptySnapshot(), "GET", "/anything", "", nil); got != "" {
		t.Errorf("an empty snapshot selected %q", got)
	}
}

// The candidate counter feeds mockulus_match_candidates, so it has to reflect
// work actually done rather than being a constant.
func TestCandidateCountReflectsWork(t *testing.T) {
	// Priorities force the evaluation order: the two stubs that cannot match are
	// tried before the one that can.
	snap := BuildSnapshot([]*stub.CompiledStub{
		mustCompile(t, 1, "a", `{"priority":1,"request":{"urlPath":"/c","headers":{"X":{"equalTo":"1"}}},"response":{}}`),
		mustCompile(t, 2, "b", `{"priority":2,"request":{"urlPath":"/c","headers":{"X":{"equalTo":"2"}}},"response":{}}`),
		mustCompile(t, 3, "c", `{"priority":3,"request":{"urlPath":"/c"},"response":{}}`),
	}, 1)

	req := httptest.NewRequest("GET", "/c", nil)
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	evaluated := 0
	cs := snap.Match(pr, nil, &evaluated)
	if cs == nil || cs.ID != "c" {
		t.Fatalf("selected %v, want c", idOf(cs))
	}
	if evaluated != 3 {
		t.Errorf("evaluated = %d, want 3 — two failing candidates then the match", evaluated)
	}
}

func BenchmarkMatchExactURL(b *testing.B) {
	stubs := make([]*stub.CompiledStub, 0, 1000)
	for i := range 1000 {
		cs, errs := stub.Compile([]byte(
			`{"request":{"method":"GET","urlPath":"/api/resource/`+itoa(i)+
				`"},"response":{"status":200,"body":"x"}}`), uint64(i), testStubOptions())
		if errs != nil {
			b.Fatal(errs.Errors())
		}
		cs.ID = "s" + itoa(i)
		stubs = append(stubs, cs)
	}
	snap := BuildSnapshot(stubs, 1)

	req := httptest.NewRequest("GET", "/api/resource/500", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pr := AcquireRequest(req, nil)
		if cs := snap.Match(pr, nil, nil); cs == nil {
			b.Fatal("expected a match")
		}
		ReleaseRequest(pr)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
