// SPDX-License-Identifier: Apache-2.0

// Package adminui serves the embedded administration UI (SPEC §5.7).
//
// The UI is a static single-page application built by `make ui-build` into
// `dist/` and compiled into the binary from there. It is mockulus' own surface,
// not WireMock's, so it lives entirely under the reserved
// `/__admin/mockulus/**` extension namespace and talks to the same public admin
// API any other client uses — there are no private endpoints behind it and no
// server-side session state.
//
// Two things here are deliberate and load-bearing.
//
// The assets are served *outside* the admin token check (§17, amended in §5.7).
// A browser cannot attach an `Authorization` header to a page load, so a token
// on the assets would make the UI unusable on exactly the hardened deployments
// that set one. What the assets contain is code, not data: every request that
// reads or writes a stub, a scenario or the journal goes through the admin API
// and is checked normally, and the corpus pins that pair — assets 200 without a
// token while the API 401s without one.
//
// And `dist/` is generated, so the tree carries only a `.gitkeep` placeholder.
// A plain `go build` with no Node installed therefore still produces a working
// binary; it serves the notice page below instead of the app, which is a state
// a contributor can read rather than a link that 404s.
package adminui

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Prefix is the path the UI is served under, on both listeners.
//
// The trailing slash is part of it: everything below is either an asset or a
// client route, and a request for the bare directory is redirected to it so the
// relative base the bundle was built with resolves the same way either way.
const Prefix = "/__admin/mockulus/ui/"

//go:embed all:dist
var embedded embed.FS

// Handler serves the UI, or the notice page when the binary was built without
// one. It is safe for concurrent use and holds no per-request state.
type Handler struct {
	assets fs.FS
	// built records whether dist/ carried a real index.html at compile time.
	// It is resolved once here rather than per request: the answer cannot
	// change while the process runs, and the fallback is a static page.
	built bool
	index []byte
}

// New builds the handler from the embedded assets.
func New() *Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is
		// a build-time fact rather than a runtime one.
		return newFromFS(emptyFS{})
	}
	return newFromFS(sub)
}

// newFromFS is New over an arbitrary tree, so the tests can exercise the built
// path on a binary whose own dist/ is the committed placeholder — which is what
// every `go test` run without a prior `make ui-build` is.
func newFromFS(assets fs.FS) *Handler {
	h := &Handler{assets: assets}
	if index, err := fs.ReadFile(assets, "index.html"); err == nil {
		h.built, h.index = true, index
	}
	return h
}

// Built reports whether a real UI was compiled in. The admin handler uses it to
// decide what to say about the UI in diagnostics, not whether to route: an
// unbuilt binary still answers under the prefix, with the notice page.
func (h *Handler) Built() bool { return h.built }

// ServeHTTP serves an asset, or index.html for anything that is a client route.
//
// The SPA fallback is what makes a deep link work: the router runs in history
// mode, so `…/ui/stubs/abc` is a path the server has never heard of and must
// answer with the document that boots the router. The fallback is deliberately
// *not* extended to paths that look like assets — a missing `.js` answers 404
// rather than silently returning HTML, because a bundle that half-loads is far
// harder to diagnose than one that reports the file it wanted.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, Prefix)
	// The bare prefix without its trailing slash arrives here too; both mean
	// the application root.
	rest = strings.TrimPrefix(rest, "/")

	if !h.built {
		h.serveNotice(w, r)
		return
	}

	if rest == "" || rest == "index.html" {
		h.serveIndex(w, r)
		return
	}

	// Clean the path before it reaches the file system. fs.FS rejects an
	// escaping path anyway, so this is about answering the same way for every
	// spelling of one rather than about containment.
	name := path.Clean(rest)
	if name == "." || strings.HasPrefix(name, "../") {
		h.serveIndex(w, r)
		return
	}

	file, err := h.assets.Open(name)
	if err != nil {
		if looksLikeAsset(name) {
			http.NotFound(w, r)
			return
		}
		h.serveIndex(w, r)
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		h.serveIndex(w, r)
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		h.serveIndex(w, r)
		return
	}

	setSecurityHeaders(w)
	// Vite fingerprints everything it emits, so a hashed name identifies its
	// content for ever and may be cached as long as anything is allowed to be.
	// index.html carries no hash and is revalidated every time, which is what
	// lets a new build take effect without anyone clearing a cache.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

// serveNotice answers when the binary was built without a UI.
//
// It is a 200 rather than a 404 because the route exists and the answer is
// accurate: this build has no UI. A 404 would be indistinguishable from
// `ui_enabled: false` and from a typo in the path, and all three want different
// fixes.
func (h *Handler) serveNotice(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(noticePage))
}

// looksLikeAsset reports whether a path should 404 rather than fall back to the
// application document. Anything with a file extension is a request for a file.
func looksLikeAsset(name string) bool {
	return path.Ext(name) != ""
}

// setSecurityHeaders applies the headers every UI document carries (§5.7).
//
// The CSP is the second layer under Svelte's escaping, and it matters because
// what the UI renders is untrusted: stub names, URLs, header values and request
// bodies all arrive from whoever is driving the mock. `default-src 'self'` with
// no `unsafe-inline` for scripts means an injected `<script>` does not run even
// if something upstream forgets to escape it.
//
// Styles keep 'unsafe-inline', and this was re-examined rather than inherited.
//
// The tightening that looked available was `style-src-elem 'self'`, which would
// have refused an injected `<style>` element while leaving the style attributes
// the interface uses. That is worth wanting: a stylesheet cannot execute script,
// but it can read a document through attribute selectors and exfiltrate what it
// reads through background-image URLs, which is a real technique against a page
// rendering somebody else's request bodies.
//
// It does not hold. Driven in a real browser with violation reporting on, the
// editor page reports `Applying inline style violates … 'style-src-elem 'self”`
// — CodeMirror injects a stylesheet element to theme itself. Blocking it leaves
// an editor that renders and is unstyled, which is worse than the exposure it
// closes. A hash would pin one version of a third-party stylesheet and break on
// upgrade; a nonce needs a per-response render on a path that is otherwise a
// static file.
//
// So the directive stays wide, and the reason is now measured rather than
// assumed. What actually protects this page is the layer above: `script-src`
// admits no inline script, Svelte escapes every field, and nothing in the
// interface uses `{@html}` — checked, and the exposure a permissive `style-src`
// leaves needs an injection point that none of those three allow.
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"font-src 'self' data:; "+
			"connect-src 'self'; "+
			"object-src 'none'; "+
			"base-uri 'none'; "+
			"form-action 'none'; "+
			"frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// emptyFS stands in when the embedded tree cannot be opened at all.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
