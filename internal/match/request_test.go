// SPDX-License-Identifier: Apache-2.0

package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRequest(t *testing.T, method, target, body string, headers map[string]string) *ParsedRequest {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	return AcquireRequest(req, []byte(body))
}

func TestRequestSplitsPathAndQuery(t *testing.T) {
	r := newRequest(t, "GET", "/api/orders?a=1&b=2", "", nil)
	defer ReleaseRequest(r)

	if r.Method != http.MethodGet {
		t.Errorf("method = %q", r.Method)
	}
	if r.Path != "/api/orders" {
		t.Errorf("path = %q, want /api/orders", r.Path)
	}
	// The byte-exact `url` criterion compares against this, so query order is
	// preserved exactly as received rather than normalised.
	if r.FullURL != "/api/orders?a=1&b=2" {
		t.Errorf("fullURL = %q", r.FullURL)
	}
}

func TestHeaderNamesAreCaseInsensitive(t *testing.T) {
	r := newRequest(t, "GET", "/x", "", map[string]string{"Content-Type": "application/json"})
	defer ReleaseRequest(r)

	for _, name := range []string{"Content-Type", "content-type", "CONTENT-TYPE", "cOnTeNt-TyPe"} {
		s := r.HeaderSubject(name)
		if !s.Present() {
			t.Errorf("header lookup %q should find the header", name)
			continue
		}
		if got := s.Values(); len(got) != 1 || got[0] != "application/json" {
			t.Errorf("header lookup %q returned %v", name, got)
		}
	}

	if r.HeaderSubject("X-Missing").Present() {
		t.Error("a missing header must not be present")
	}
}

func TestRepeatedHeaderKeepsEveryValue(t *testing.T) {
	r := newRequest(t, "GET", "/x", "", nil)
	defer ReleaseRequest(r)
	r.header.Add("X-Tag", "a")
	r.header.Add("X-Tag", "b")

	got := r.HeaderSubject("X-Tag").Values()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("repeated header values = %v, want [a b]", got)
	}
}

func TestQueryParameters(t *testing.T) {
	r := newRequest(t, "GET", "/x?one=1&many=a&many=b&empty=&flag", "", nil)
	defer ReleaseRequest(r)

	if got := r.QuerySubject("one").Values(); len(got) != 1 || got[0] != "1" {
		t.Errorf("one = %v", got)
	}
	if got := r.QuerySubject("many").Values(); len(got) != 2 {
		t.Errorf("many = %v, want two values", got)
	}

	// Present-with-empty-value and absent are different, and a matcher can
	// tell them apart.
	empty := r.QuerySubject("empty")
	if !empty.Present() {
		t.Error("a parameter given with an empty value is present")
	}
	if got := empty.Values(); len(got) != 1 || got[0] != "" {
		t.Errorf("empty = %v, want one empty string", got)
	}

	if !r.QuerySubject("flag").Present() {
		t.Error("a valueless parameter is still present")
	}
	if r.QuerySubject("nope").Present() {
		t.Error("a missing parameter must not be present")
	}
}

// Query parameter names are case-sensitive, unlike header names.
func TestQueryNamesAreCaseSensitive(t *testing.T) {
	r := newRequest(t, "GET", "/x?Name=1", "", nil)
	defer ReleaseRequest(r)

	if !r.QuerySubject("Name").Present() {
		t.Error("exact name should be found")
	}
	if r.QuerySubject("name").Present() {
		t.Error("query names must not be matched case-insensitively")
	}
}

func TestCookies(t *testing.T) {
	r := newRequest(t, "GET", "/x", "", map[string]string{
		"Cookie": `session=abc123; theme="dark"; empty=`,
	})
	defer ReleaseRequest(r)

	if got := r.CookieSubject("session").Values(); len(got) != 1 || got[0] != "abc123" {
		t.Errorf("session = %v", got)
	}
	// A quoted cookie value is unquoted, which is what a stub author writes.
	if got := r.CookieSubject("theme").Values(); len(got) != 1 || got[0] != "dark" {
		t.Errorf("theme = %v, want [dark] with the quotes removed", got)
	}
	if !r.CookieSubject("empty").Present() {
		t.Error("a cookie with an empty value is present")
	}
	if r.CookieSubject("missing").Present() {
		t.Error("a missing cookie must not be present")
	}
}

// Cookie names are case-sensitive, unlike the name of the header carrying them.
// Verified against pinned WireMock, which reports a differently-cased name as
// "Cookie is not present" rather than as a value mismatch.
//
// The oracle can be made to disagree, and the disagreement is not real: Jetty
// reuses a connection's previously parsed cookies when the new Cookie header
// differs only by case, so on a keep-alive connection the preceding request's
// cookies answer this one. The differential harness gives the oracle a fresh
// connection per request so that artefact cannot be mistaken for a rule.
func TestCookieNamesAreCaseSensitive(t *testing.T) {
	r := newRequest(t, "GET", "/x", "", map[string]string{
		"Cookie": "session=abc123",
	})
	defer ReleaseRequest(r)

	if !r.CookieSubject("session").Present() {
		t.Error("exact name should be found")
	}
	for _, name := range []string{"SESSION", "Session", "sEsSiOn"} {
		if r.CookieSubject(name).Present() {
			t.Errorf("cookie %q must not match a differently-cased name", name)
		}
	}
}

// A cookie name that only differs by case leaves the criterion's cookie absent,
// which is what lets an `absent` criterion keep matching. Pinned the same way
// as the positive form, since the two share the lookup.
func TestDifferentlyCasedCookieLeavesTheNameAbsent(t *testing.T) {
	r := newRequest(t, "GET", "/x", "", map[string]string{
		"Cookie": "LEGACY=1; other=2",
	})
	defer ReleaseRequest(r)

	if r.CookieSubject("legacy").Present() {
		t.Error("LEGACY must leave legacy absent")
	}
	if !r.CookieSubject("LEGACY").Present() {
		t.Error("LEGACY itself is present")
	}
}

func TestFormParametersOnlyForFormEncodedBodies(t *testing.T) {
	form := newRequest(t, "POST", "/x", "a=1&b=2&b=3",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	defer ReleaseRequest(form)

	if got := form.FormSubject("a").Values(); len(got) != 1 || got[0] != "1" {
		t.Errorf("a = %v", got)
	}
	if got := form.FormSubject("b").Values(); len(got) != 2 {
		t.Errorf("b = %v, want two values", got)
	}
	if form.FormSubject("c").Present() {
		t.Error("a missing field must not be present")
	}

	// The same bytes with a JSON content type are not form fields.
	json := newRequest(t, "POST", "/x", "a=1&b=2",
		map[string]string{"Content-Type": "application/json"})
	defer ReleaseRequest(json)
	if json.FormSubject("a").Present() {
		t.Error("form parsing must not apply to a non-form body")
	}
}

// The charset parameter must not defeat the content-type comparison.
func TestFormContentTypeIgnoresParameters(t *testing.T) {
	r := newRequest(t, "POST", "/x", "a=1",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"})
	defer ReleaseRequest(r)

	if !r.FormSubject("a").Present() {
		t.Error("a charset parameter should not prevent form parsing")
	}
}

func TestPathVariablesAreScopedToOneStub(t *testing.T) {
	r := newRequest(t, "GET", "/api/orders/42", "", nil)
	defer ReleaseRequest(r)

	r.BindPathVar("id", "42")
	s := r.PathVarSubject("id")
	if !s.Present() || s.Values()[0] != "42" {
		t.Fatalf("bound variable not visible: present=%v values=%v", s.Present(), s.Values())
	}

	// Bindings belong to the stub being evaluated. Leaking them into the next
	// candidate would make matching depend on evaluation order.
	r.ClearPathVars()
	if r.PathVarSubject("id").Present() {
		t.Error("path variables must not survive into the next candidate stub")
	}
}

func TestBodyIsSharedAcrossCriteria(t *testing.T) {
	r := newRequest(t, "POST", "/x", `{"a":1}`, nil)
	defer ReleaseRequest(r)

	first := r.BodySubject()
	second := r.BodySubject()
	if first != second {
		t.Error("every body criterion should see the same subject, so parsing is shared")
	}
	if _, ok := first.JSON(); !ok {
		t.Error("valid JSON body should parse")
	}
}

// Pooled instances must not carry anything across requests: a leaked body or
// header map is a correctness bug that only appears under concurrency.
func TestReleaseClearsEverything(t *testing.T) {
	r := newRequest(t, "POST", "/api/x?q=1", `{"a":1}`, map[string]string{
		"Content-Type": "application/json",
		"Cookie":       "s=1",
	})
	r.BindPathVar("id", "9")
	r.MemoizeScenarioState("flow", "Started")
	_ = r.QuerySubject("q")
	_ = r.CookieSubject("s")
	_ = r.FormSubject("nope")
	_, _ = r.BodySubject().JSON()

	r.Reset()

	if r.Method != "" || r.Path != "" || r.FullURL != "" {
		t.Error("request line not cleared")
	}
	if r.header != nil || r.body != nil {
		t.Error("header or body reference survived the reset")
	}
	if r.query != nil || r.cookies != nil || r.form != nil {
		t.Error("a parsed form survived the reset")
	}
	if r.queryParsed || r.cookiesParsed || r.formParsed {
		t.Error("parse flags survived the reset, so the next request would see stale results")
	}
	if len(r.pathVars) != 0 || len(r.scenarioStates) != 0 {
		t.Error("per-request maps survived the reset")
	}
	if r.BodySubject().Present() {
		t.Error("body subject survived the reset")
	}
}

// Reuse from the pool must behave exactly as a fresh instance.
func TestPoolReuseIsClean(t *testing.T) {
	first := newRequest(t, "POST", "/first?a=1", `{"x":1}`, map[string]string{"X-A": "1"})
	_ = first.QuerySubject("a")
	_ = first.HeaderSubject("X-A")
	_, _ = first.BodySubject().JSON()
	ReleaseRequest(first)

	second := newRequest(t, "GET", "/second", "", nil)
	defer ReleaseRequest(second)

	if second.Path != "/second" {
		t.Errorf("path = %q, want /second", second.Path)
	}
	if second.QuerySubject("a").Present() {
		t.Error("a query parameter from the previous request is visible")
	}
	if second.HeaderSubject("X-A").Present() {
		t.Error("a header from the previous request is visible")
	}
	if v, ok := second.BodySubject().JSON(); ok {
		t.Errorf("a parsed body from the previous request is visible: %v", v)
	}
}

// The request target is taken from RequestURI rather than re-derived from the
// parsed URL, so what a stub's `url` criterion compares against has to be shown
// to be the same string for every target shape a client can send.
func TestRequestTargetIsTheTargetAsReceived(t *testing.T) {
	targets := []string{
		"/api/orders",
		"/api/orders?a=1&b=2",
		"/api/orders?",
		"/api/orders?a=%20b&c=d%2Fe",
		// Percent-encoding the parsed URL would decode and re-encode: a space
		// as %20, a slash as %2F, and a plus that must survive unchanged.
		"/api/orders/a%20b/c%2Fd",
		"/api/orders/a+b",
		"/api/orders/caf%C3%A9",
		// Not valid UTF-8, so nothing can normalise it into something else.
		"/api/orders/%FF%FE",
		"/api/orders//double//slashes",
		"/api/orders/.%2E/parent",
	}

	for _, target := range targets {
		req := httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
		pr := AcquireRequest(req, nil)
		if pr.FullURL != target {
			t.Errorf("FullURL for %q = %q, want the target as received", target, pr.FullURL)
		}
		// The parsed URL is the other route to the same string, and the two
		// agreeing is what makes the cheap one safe to prefer.
		if got := req.URL.RequestURI(); got != pr.FullURL {
			t.Errorf("target %q: RequestURI gives %q but the parsed URL gives %q",
				target, pr.FullURL, got)
		}
		ReleaseRequest(pr)
	}
}

// A target that is not a path — the absolute form a proxy client sends — is the
// case RequestURI cannot be used verbatim for, because a stub's URL criterion
// is written against the origin form.
func TestAbsoluteRequestTargetFallsBackToTheOriginForm(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/orders?a=1", nil)
	req.RequestURI = "http://mocks.example.internal/api/orders?a=1"

	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	if pr.FullURL != "/api/orders?a=1" {
		t.Errorf("FullURL = %q, want the origin form", pr.FullURL)
	}
	if pr.Path != "/api/orders" {
		t.Errorf("path = %q, want /api/orders", pr.Path)
	}
}

func BenchmarkAcquireRelease(b *testing.B) {
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/orders/42", nil)
	body := []byte(nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pr := AcquireRequest(req, body)
		_ = pr.Path
		ReleaseRequest(pr)
	}
}

func BenchmarkHeaderLookup(b *testing.B) {
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/x", nil)
	req.Header.Set("Content-Type", "application/json")
	pr := AcquireRequest(req, nil)
	defer ReleaseRequest(pr)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !pr.HeaderSubject("content-type").Present() {
			b.Fatal("expected the header")
		}
	}
}
