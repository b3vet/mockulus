// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A file name is validated in one place because it is written through two: the
// files API stores one, and a response's bodyFileName addresses one. A rule
// that lived only next to the first would let the second register a name that
// can never resolve — accepted at registration and 1022 at serve time, which
// is precisely the accept-and-fail-later shape P3 exists to prevent.

// MaxFileNameBytes bounds a name so that every driver can address it. The
// Couchbase driver keys a file document on its name behind a short prefix, and
// Couchbase stops at 250-byte keys — an unbounded name is then a write the
// memory store takes and the Couchbase store rejects, which is one deployment
// answering 201 where another answers 500.
// It is exported alongside RejectFileName because a bodyFileName is a name in
// the same store, and one rule with two spellings is a rule with two answers.
const MaxFileNameBytes = 240

// RejectFileName reports why a name cannot be stored, or "" when it can.
//
// Every rule here refuses a name outright rather than repairing it. An absolute
// name used to have its leading slash trimmed, which turned `/etc/passwd` into
// a stored `etc/passwd` and answered 201 — the caller's name and the stored
// name then disagree forever, and the request that looked like traversal was
// the one that got a success. Refusing is what P3 asks for, and it is the only
// answer that keeps a name meaning one thing.
func RejectFileName(name string) string {
	switch {
	case path.IsAbs(name):
		return "a name is relative to the files store, so it cannot begin with /"
	case name == ".." || strings.HasPrefix(name, "../"):
		return "a name cannot climb out of the files store with .. segments"
	case path.Clean(name) != name:
		return "a name must already be in cleaned form: no . or .. segments, no empty ones, no trailing /"
	case len(name) > MaxFileNameBytes:
		return "a name is limited to " + strconv.Itoa(MaxFileNameBytes) + " bytes"
	case !utf8.ValidString(name):
		// A listing is JSON, and JSON encoding replaces invalid bytes with
		// U+FFFD. Accepting one would mean GET /__admin/files reports a name
		// that GET /__admin/files/<that name> cannot fetch.
		return "a name must be valid UTF-8"
	case strings.ContainsFunc(name, isControl):
		// A name reaches the logs and the error catalog. Structured logging
		// escapes a newline today, but a name that can carry one is a name that
		// can forge a log line the first time anything renders it plainly.
		return "a name cannot contain control characters"
	}
	return ""
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
