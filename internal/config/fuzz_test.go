// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
	"time"
)

// Configuration is parsed once, before the listeners bind, which is what makes
// a defect here expensive out of proportion to the surface: a pod that panics
// while reading its ConfigMap does not come up at all, and Kubernetes answers a
// crash on startup with CrashLoopBackOff rather than with a message anyone can
// read. So the targets below assert what the loader owes its operator whatever
// the file says — it terminates, it does not panic, and it either produces a
// configuration or names the line it could not read (P3).

// parseBudget bounds one parse-and-bind. The YAML subset is a single pass over
// the lines, so anything near this is a loop that stopped making progress.
const parseBudget = 2 * time.Second

// yamlSeeds are the files the E2E config variants and the deployment chart
// write, plus the shapes that take the hand-rolled subset to its edges: the
// syntax it deliberately refuses, quoting and escapes, comments in every
// position, and indentation that does not line up.
var yamlSeeds = []string{
	"port: 8080\nadmin_port: 9090\n",
	"couchbase:\n  connstr: couchbase://cb\n  bucket: mockulus\n  scope: _default\n",
	"journal_enabled: true\njournal_ttl: 30m\njournal_max_body: 64KiB\n",
	"log:\n  level: debug\n  format: text\n  requests: true\n",
	"# a comment\nport: 8080 # trailing\n\n  \n",
	`admin_auth_token: "a token with spaces"` + "\n",
	`admin_auth_token: 'single quoted'` + "\n",
	`admin_auth_token: "escaped \" quote"` + "\n",
	`admin_auth_token: "tab\there"` + "\n",
	`admin_auth_token: "unterminated` + "\n",
	`admin_auth_token: "bad \q escape"` + "\n",
	`admin_auth_token: "closed" then trailing` + "\n",
	`admin_auth_token: 'it''s quoted'` + "\n",
	"store: file\nfile:\n  root: /mappings\n",
	"max_body_bytes: 0\ntemplate_max_output_bytes: 10MiB\n",
	"max_body_bytes: 9223372036854775807\n",
	"max_body_bytes: 8GiB\n",
	"max_body_bytes: -1\n",
	"max_body_bytes: 4611686018427387904KiB\n",
	"sync_interval: 100ms\nresync_interval: 5m\n",
	"sync_interval: not-a-duration\n",
	"port: not-a-number\n",
	"metrics_enabled: yes\n",
	"---\nport: 8080\n",
	"...\n",
	"- a list item\n",
	"-\n",
	"\tport: 8080\n",
	"port:\n\tadmin_port: 9090\n",
	"couchbase:\n    connstr: x\n  bucket: y\n",
	"a:\n b:\n  c:\n   d: 1\n",
	"port 8080\n",
	": 8080\n",
	"\"quoted key\": 1\n",
	"port: 1\nport: 2\n",
	"unknown_key: 1\n",
	"couchbase:\n",
	"couchbase: # section with only a comment\n  connstr: x\n",
	"port: |\n  block\n",
	"port: &anchor 1\n",
	"port: {a: 1}\n",
	"",
	"\n\n\n",
}

// FuzzParseYAML drives the YAML subset and then binds whatever it produced onto
// a real Config, because a parse that succeeds is only half the path: the
// binding decides types and the validation decides whether the process starts.
func FuzzParseYAML(f *testing.F) {
	for _, s := range yamlSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		start := time.Now()
		doc, err := parseYAML(src)
		if took := time.Since(start); took > parseBudget {
			t.Fatalf("parsing %d bytes took %s, over the %s budget", len(src), took, parseBudget)
		}
		if err != nil {
			// The line number is the whole value of refusing: a file rejected
			// without one leaves an operator reading a ConfigMap by eye.
			if !strings.HasPrefix(err.Error(), "line ") {
				t.Fatalf("a rejected file was refused without naming a line: %v", err)
			}
			return
		}

		// Every key the parser accepted has to be a dotted path the binder can
		// resolve or reject by name. A key holding a newline or a colon would
		// be neither: it could not match a field, and the "unknown key" it
		// earns would quote something the operator never wrote.
		for path := range doc {
			if path == "" || strings.ContainsAny(path, "\n\r:") {
				t.Fatalf("the parser produced the key %q", path)
			}
		}

		cfg := Default()
		start = time.Now()
		bindErr := applyYAML(&cfg, doc)
		if took := time.Since(start); took > parseBudget {
			t.Fatalf("binding %d keys took %s, over the %s budget", len(doc), took, parseBudget)
		}
		if bindErr != nil {
			return
		}

		// Validation runs on whatever bound, which is where a value that is
		// well-typed and still unusable — a negative port, a sync interval
		// below its floor — has to be caught rather than started with.
		_ = cfg.Validate()

		// Dump is what the startup summary logs, so it walks the same values.
		// A secret that survives it is a token in a log aggregator (SPEC §14.2).
		//
		// The check is on the token's OWN line rather than a substring sweep of
		// all of them: a short token is a substring of unrelated values by
		// coincidence — a token of "0" appears in "port=8080" — and a test that
		// reports that as a leak fails for a reason that has nothing to do with
		// the property.
		if cfg.AdminAuthToken != "" {
			for _, line := range cfg.Dump() {
				key, value, ok := strings.Cut(line, "=")
				if !ok || key != "admin_auth_token" {
					continue
				}
				if value != "[redacted]" {
					t.Fatalf("the admin token was dumped in the clear: %q", line)
				}
			}
		}
	})
}

// FuzzScalarTypes fuzzes the two hand-written value parsers directly. They are
// reached from the environment as well as from a file — an env var bypasses the
// YAML subset entirely — so a string neither the file syntax nor a human would
// produce still reaches them.
func FuzzScalarTypes(f *testing.F) {
	seeds := []string{
		"0", "1", "-1", "8192", "9223372036854775807", "9223372036854775808",
		"64KiB", "10MiB", "8GiB", "1B", "kib", "KiB", " 64 KiB ", "64kib",
		"-64KiB", "4611686018427387904KiB", "0x10", "1e9", "",
		"1s", "200ms", "24h", "-1s", "0s", "1h30m", "1", "s", "1d", "  1s  ",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		start := time.Now()

		var b Bytes
		if err := b.parse(raw); err == nil {
			// A negative size would flip the sense of every cap that compares
			// against it: max_body_bytes reads 0 as unbounded, and anything
			// below that is a bound no request can satisfy.
			if b.B() < 0 {
				t.Fatalf("%q parsed to the negative size %d", raw, b.B())
			}
			// The rendering is what the startup dump and the spec table show,
			// so it has to be a spelling the parser takes back.
			var back Bytes
			if err := back.parse(b.String()); err != nil || back != b {
				t.Fatalf("%q rendered as %q, which parses back as %d (%v)", raw, b.String(), back, err)
			}
		}

		var d Duration
		if err := d.parse(raw); err == nil {
			var back Duration
			if err := back.parse(d.String()); err != nil || back != d {
				t.Fatalf("%q rendered as %q, which parses back as %s (%v)", raw, d.String(), back, err)
			}
		}

		if took := time.Since(start); took > parseBudget {
			t.Fatalf("parsing %q took %s, over the %s budget", raw, took, parseBudget)
		}
	})
}
