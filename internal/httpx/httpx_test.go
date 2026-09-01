// SPDX-License-Identifier: Apache-2.0

package httpx

import "testing"

// The splitter is shared by the `host`/`port` criteria and by the
// `{{request.host}}` template, which is the point of it living here: the two
// cannot disagree about the same header if they read it through one function.
// The rows are the ones SPEC §5.2 states on those rows.
func TestSplitHostPort(t *testing.T) {
	for _, c := range []struct{ in, host, port string }{
		{"api.example.com:8080", "api.example.com", "8080"},
		// No port named is the ordinary case, and the answer is the empty string
		// rather than the scheme's default: the request did not carry one, and
		// inventing 80 would answer a question nobody asked.
		{"api.example.com", "api.example.com", ""},
		{"", "", ""},
		// An IPv6 literal keeps its brackets, and the colons inside one are not
		// port separators. Without the bracket test the first row here would
		// split on the address's own final colon.
		{"[::1]", "[::1]", ""},
		{"[::1]:9090", "[::1]", "9090"},
		{"[2001:db8::1]:443", "[2001:db8::1]", "443"},
		// A trailing colon names an empty port, which is what was written.
		{"host:", "host", ""},
	} {
		host, port := SplitHostPort(c.in)
		if host != c.host || port != c.port {
			t.Errorf("SplitHostPort(%q) = (%q, %q), want (%q, %q)", c.in, host, port, c.host, c.port)
		}
	}
}

func TestSchemeOf(t *testing.T) {
	if got := SchemeOf(true); got != "https" {
		t.Errorf("SchemeOf(true) = %q, want https", got)
	}
	if got := SchemeOf(false); got != "http" {
		t.Errorf("SchemeOf(false) = %q, want http", got)
	}
}
