// SPDX-License-Identifier: Apache-2.0

package adminui

// noticePage is served when `dist/` held no `index.html` at compile time.
//
// This is the plain `go build` path (U7): the repository commits only a
// placeholder, so a contributor with no Node installed still gets a binary that
// builds, runs and passes the Go tests. What they must not get is a route that
// looks broken — a blank 200 or a 404 both read as a bug in the server rather
// than as an artifact that was built without its front end.
//
// The markup is inlined rather than embedded from a file so that this page
// exists even when the embedded tree is empty, which is the exact case it is
// for. It carries no script and no external reference, so it satisfies the same
// CSP the real application does.
var noticePage = []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>mockulus — built without the admin UI</title>
<style>
  :root { color-scheme: light dark; }
  body {
    font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
    margin: 0; display: grid; place-items: center; min-height: 100vh; padding: 2rem;
  }
  main { max-width: 34rem; }
  h1 { font-size: 1.25rem; margin: 0 0 .75rem; }
  p { margin: 0 0 .75rem; }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .9em;
    padding: .1em .35em; border-radius: .25rem;
    background: color-mix(in srgb, currentColor 10%, transparent);
  }
  .muted { opacity: .75; }
</style>
</head>
<body>
<main>
  <h1>This build has no admin UI</h1>
  <p>
    The server is running normally and the admin API is fully available — only the
    web interface is missing. The UI is a static bundle compiled into the binary,
    and this binary was built before that bundle existed.
  </p>
  <p>Build it with <code>make ui-build</code>, then rebuild the server:</p>
  <p><code>make ui-build &amp;&amp; make build</code></p>
  <p class="muted">
    Official release binaries and container images always include the UI; this page
    normally appears only after a plain <code>go build</code> on a machine with no
    Node toolchain, which is deliberately still supported.
  </p>
</main>
</body>
</html>
`)
