// SPDX-License-Identifier: Apache-2.0

package response

import (
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
