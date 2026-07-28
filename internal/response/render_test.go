// SPDX-License-Identifier: Apache-2.0

package response

import (
	"net/http"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/stub"
)

// fixedDist is a distribution with no spread, so a composition test asserts the
// arithmetic rather than sampling luck.
func fixedDist(ms int64) stub.DelayDistribution {
	d := time.Duration(ms) * time.Millisecond
	return stub.DelayDistribution{Kind: stub.DelayUniform, Lower: d, Upper: d}
}

// The composition rule was re-derived from pinned WireMock 3.13.2 by probe: the
// fixed and the sampled part are summed, and within each part a value the stub
// declared replaces the global one. It cannot be asserted by the E2E gate, which
// never makes claims about elapsed time, so it is pinned here.
func TestComposeDelay(t *testing.T) {
	global := &stub.Settings{FixedDelay: 300 * time.Millisecond, Delay: fixedDist(100)}

	cases := []struct {
		name   string
		resp   stub.CompiledResponse
		global *stub.Settings
		want   time.Duration
	}{
		{
			name: "no settings leaves the stub alone",
			resp: stub.CompiledResponse{FixedDelay: 50 * time.Millisecond, FixedDelaySet: true},
			want: 50 * time.Millisecond,
		},
		{
			name:   "a stub with no delay inherits both parts",
			global: global,
			want:   400 * time.Millisecond,
		},
		{
			name:   "the stub's fixed delay replaces the global one, the sampled part still adds",
			resp:   stub.CompiledResponse{FixedDelay: 50 * time.Millisecond, FixedDelaySet: true},
			global: global,
			want:   150 * time.Millisecond,
		},
		{
			name:   "an explicit zero opts out of the global fixed delay",
			resp:   stub.CompiledResponse{FixedDelaySet: true},
			global: global,
			want:   100 * time.Millisecond,
		},
		{
			name:   "the stub's distribution replaces the global one",
			resp:   stub.CompiledResponse{Delay: fixedDist(20)},
			global: global,
			want:   320 * time.Millisecond,
		},
		{
			name:   "settings that ask for nothing cost nothing",
			resp:   stub.CompiledResponse{},
			global: &stub.Settings{},
			want:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDelay(&tc.resp, tc.global); got != tc.want {
				t.Errorf("composeDelay = %s, want %s", got, tc.want)
			}
		})
	}
}

// A stub that declares its own Content-Length is declaring the framing of its
// response, and net/http enforces it by refusing the write rather than cutting
// it short: a stub promising three bytes over eight sent a response framed for
// three and carrying none, which a client reads as a hung server or a broken
// connection. Trimming makes the message describe itself, and puts the same
// bytes in front of the client that WireMock's declared length puts in front of
// its own. The control cases are the ones that must not lose a byte, because a
// rule written slightly too wide here silently truncates ordinary responses.
func TestClampToDeclaredLength(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		body     string
		want     string
	}{
		{
			name:     "a declared length under the body trims it",
			declared: "3",
			body:     "abcdefgh",
			want:     "abc",
		},
		{
			name:     "a declared zero sends the headers and nothing else",
			declared: "0",
			body:     "abcdefgh",
			want:     "",
		},
		{
			name:     "a declared length equal to the body keeps all of it",
			declared: "8",
			body:     "abcdefgh",
			want:     "abcdefgh",
		},
		{
			name:     "a declared length over the body keeps all of it",
			declared: "20",
			body:     "abcdefgh",
			want:     "abcdefgh",
		},
		{
			name:     "no declared length keeps all of it",
			declared: "",
			body:     "abcdefgh",
			want:     "abcdefgh",
		},
		{
			// Not a length, so there is nothing to trim to. net/http drops the
			// header and frames the response for what it holds; inventing a
			// number here would put a length on the wire nobody wrote.
			name:     "a declared length that is not a number keeps all of it",
			declared: "three",
			body:     "abcdefgh",
			want:     "abcdefgh",
		},
		{
			name:     "a negative declared length keeps all of it",
			declared: "-1",
			body:     "abcdefgh",
			want:     "abcdefgh",
		},
		{
			name:     "an empty body is unaffected by a declared length",
			declared: "5",
			body:     "",
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.declared != "" {
				header.Set("Content-Length", tc.declared)
			}
			got := clampToDeclaredLength(header, []byte(tc.body))
			if string(got) != tc.want {
				t.Errorf("Content-Length %q over %q served %q, want %q",
					tc.declared, tc.body, got, tc.want)
			}
		})
	}
}

// The lookup has to find the header however the stub spelled it, because a
// mappings file written by hand spells it whichever way its author felt and the
// name is case-insensitive on the wire. A comparison written against the
// canonical spelling would leave a lower-case declaration to net/http, which is
// where the hang was.
func TestClampToDeclaredLengthIgnoresNameCase(t *testing.T) {
	header := http.Header{}
	header.Add("content-length", "2")
	if got := clampToDeclaredLength(header, []byte("abcdefgh")); string(got) != "ab" {
		t.Errorf("a lower-case content-length served %q, want %q", got, "ab")
	}
}
