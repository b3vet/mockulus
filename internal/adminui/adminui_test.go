// SPDX-License-Identifier: Apache-2.0

package adminui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtFS stands in for a real `make ui-build` output. The committed dist/ holds
// only a placeholder, so without this every test here would exercise the notice
// page and none would reach the code that actually serves the application.
func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>mockulus</title>")},
		"assets/app-a1b2c3.js":  {Data: []byte("export const x = 1")},
		"assets/app-d4e5f6.css": {Data: []byte(".a{color:red}")},
		"favicon.svg":           {Data: []byte("<svg/>")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

func TestServesTheApplicationDocumentAtTheRoot(t *testing.T) {
	h := newFromFS(builtFS())
	for _, path := range []string{Prefix, strings.TrimSuffix(Prefix, "/"), Prefix + "index.html"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title>mockulus</title>") {
			t.Errorf("GET %s did not serve index.html", path)
		}
	}
}

func TestAssetsAreServedAndCachedForever(t *testing.T) {
	h := newFromFS(builtFS())
	rec := get(t, h, Prefix+"assets/app-a1b2c3.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q, want the immutable form", got)
	}
	if body := rec.Body.String(); body != "export const x = 1" {
		t.Errorf("asset body = %q", body)
	}
}

// The document must never be cached, or a deploy leaves browsers booting the
// previous build's asset names for as long as the cache lives.
func TestTheDocumentIsRevalidatedEveryTime(t *testing.T) {
	h := newFromFS(builtFS())
	if got := get(t, h, Prefix).Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", got)
	}
}

// A deep link is a path the server has never heard of, and it has to boot the
// router rather than 404 — this is what makes history-mode routing work.
func TestUnknownRoutesFallBackToTheDocument(t *testing.T) {
	h := newFromFS(builtFS())
	for _, path := range []string{Prefix + "stubs", Prefix + "stubs/7f3a", Prefix + "journal/near-misses"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the SPA fallback", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<title>mockulus</title>") {
			t.Errorf("GET %s did not fall back to index.html", path)
		}
	}
}

// The fallback stops at anything with an extension. A bundle that half-loads —
// a missing .js answered with HTML — fails somewhere far from the cause, and
// the browser console reports a syntax error in a file that is really a 404.
func TestAMissingAssetIsNotFedTheDocument(t *testing.T) {
	h := newFromFS(builtFS())
	for _, path := range []string{Prefix + "assets/gone.js", Prefix + "assets/gone.css", Prefix + "missing.svg"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestEveryDocumentCarriesTheSecurityHeaders(t *testing.T) {
	h := newFromFS(builtFS())
	for _, path := range []string{Prefix, Prefix + "deep/link", Prefix + "assets/app-a1b2c3.js"} {
		rec := get(t, h, path)
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("GET %s: script-src missing from CSP %q", path, csp)
		}
		// The whole point of the CSP here: an injected script tag must not run
		// even if some field escapes unescaped, so inline script cannot be
		// allowed anywhere in the policy.
		if strings.Contains(csp, "'unsafe-inline'") && !strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
			t.Errorf("GET %s: unsafe-inline outside style-src: %q", path, csp)
		}
		if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
			t.Errorf("GET %s: inline script is allowed, which defeats the policy", path)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q", path, got)
		}
	}
}

// A binary built with a plain `go build` and no Node serves a page that says so.
// A 404 would be indistinguishable from ui_enabled:false and from a typo, and
// those three want three different fixes.
func TestAnUnbuiltBinaryExplainsItself(t *testing.T) {
	h := newFromFS(fstest.MapFS{".gitkeep": {Data: []byte("")}})
	if h.Built() {
		t.Fatal("a tree with no index.html must not report itself built")
	}
	rec := get(t, h, Prefix)
	if rec.Code != http.StatusOK {
		t.Errorf("notice page = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no admin UI") || !strings.Contains(body, "make ui-build") {
		t.Errorf("the notice page should name the fix, got %q", body)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("the notice page must carry the same CSP as the application")
	}
}

func TestWriteMethodsAreRefused(t *testing.T) {
	h := newFromFS(builtFS())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, Prefix+"index.html", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, rec.Code)
		}
	}
}

// A traversal cannot reach outside the embedded tree. fs.FS refuses an escaping
// name on its own, so this pins that the refusal is an answer rather than a
// panic or a leak of whatever the path resolved to.
func TestTraversalCannotEscapeTheBundle(t *testing.T) {
	h := newFromFS(builtFS())
	for _, path := range []string{
		Prefix + "../../../etc/passwd",
		Prefix + "..%2f..%2fetc%2fpasswd",
		Prefix + "assets/../../go.mod",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want the document or a 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "module github.com") {
			t.Errorf("GET %s escaped the bundle", path)
		}
	}
}

func TestHeadIsServedLikeGet(t *testing.T) {
	h := newFromFS(builtFS())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodHead, Prefix, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD = %d, want 200", rec.Code)
	}
}

// TestTheRealBundleSatisfiesItsOwnPolicy checks the embedded build against the
// CSP the handler sends with it.
//
// This is the one failure the rest of the tests here cannot see: every other
// case supplies its own fixture, so they prove the handler's contract and say
// nothing about whether the *actual* bundle can run under it. A build config
// that starts inlining a bootstrap script — an option several bundler plugins
// turn on for their own reasons — produces a page that passes every test above
// and is blank in a browser, with the explanation only in a console nobody is
// watching from CI.
//
// It skips when the binary was built without a UI, which is the ordinary
// `go test` on a machine with no Node. CI builds the UI before the gate runs,
// so the assertion is live exactly where a broken bundle would ship from.
func TestTheRealBundleSatisfiesItsOwnPolicy(t *testing.T) {
	h := New()
	if !h.Built() {
		t.Skip("no UI in this build; run `make ui-build` to exercise the real bundle")
	}

	index := string(h.index)

	// script-src 'self' with no nonce and no hash: an inline script block does
	// not execute. Only <script src=…> may appear.
	for _, tag := range scriptTags(index) {
		if !strings.Contains(tag, "src=") {
			t.Errorf("the bundle carries an inline <script>, which script-src 'self' blocks: %s", tag)
		}
	}

	// Every referenced resource has to be same-origin and under the UI prefix,
	// or connect/script/style-src 'self' refuses it and the base the bundle was
	// built with is wrong.
	refs := resourceRefs(index)
	if len(refs) == 0 {
		t.Fatal("the bundle references no assets at all, which cannot be a working build")
	}
	for _, ref := range refs {
		if strings.HasPrefix(ref, "data:") {
			continue
		}
		if !strings.HasPrefix(ref, Prefix) {
			t.Errorf("the bundle references %q, which is not under %s — the build's base is wrong "+
				"or the resource is off-origin, and 'self' blocks it either way", ref, Prefix)
			continue
		}
		// …and it has to actually be there, or the page 404s on boot.
		if _, err := h.assets.Open(strings.TrimPrefix(ref, Prefix)); err != nil {
			t.Errorf("the bundle references %q, which is not in the embedded tree", ref)
		}
	}
}

func scriptTags(html string) []string {
	var out []string
	rest := html
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j+1])
		rest = rest[j+1:]
	}
}

// resourceRefs pulls the src= and href= values out of the document.
func resourceRefs(html string) []string {
	var out []string
	for _, attr := range []string{`src="`, `href="`} {
		rest := html
		for {
			i := strings.Index(rest, attr)
			if i < 0 {
				break
			}
			rest = rest[i+len(attr):]
			j := strings.Index(rest, `"`)
			if j < 0 {
				break
			}
			out = append(out, rest[:j])
			rest = rest[j:]
		}
	}
	return out
}
