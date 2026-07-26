// SPDX-License-Identifier: Apache-2.0

package couchbase

import "testing"

// The watermark's two decisions are what stand between a reload and a view that
// predates the write it was triggered by, and both of them go wrong quietly: a
// requirement kept too long pins every later scan to a position the server can
// never reach, and one dropped too early lets the stale read straight back in.
// Neither is reachable from a running cluster on demand, so they are pinned
// here instead.

func TestSupersedesTakesTheNewestPositionInTheSameHistory(t *testing.T) {
	cases := []struct {
		name                               string
		heldUUID, heldSeq, gotUUID, gotSeq uint64
		want                               bool
	}{
		{"a later write in the same history replaces it", 7, 10, 7, 11, true},
		{"an earlier one does not", 7, 10, 7, 9, false},
		{"the same one does not", 7, 10, 7, 10, false},
		// A failover restarts the vbucket's numbering, so a lower sequence
		// number under a new UUID is still the newer position. Comparing the
		// numbers across histories would leave the requirement stuck at a
		// position the surviving branch never had.
		{"a new history wins even with a lower number", 7, 10, 8, 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := supersedes(c.heldUUID, c.heldSeq, c.gotUUID, c.gotSeq); got != c.want {
				t.Fatalf("supersedes(%d,%d,%d,%d) = %v, want %v",
					c.heldUUID, c.heldSeq, c.gotUUID, c.gotSeq, got, c.want)
			}
		})
	}
}

func TestCoveredOnlyForgetsWhatTheReadActuallyObserved(t *testing.T) {
	cases := []struct {
		name                                     string
		heldUUID, heldSeq, provenUUID, provenSeq uint64
		want                                     bool
	}{
		{"the read reached past it", 7, 10, 7, 11, true},
		{"the read reached exactly it", 7, 10, 7, 10, true},
		{"the read stopped short of it", 7, 10, 7, 9, false},
		// A read of a different history says nothing about this one, so the
		// requirement stays and the next scan has to satisfy it.
		{"a different history proves nothing", 7, 10, 8, 99, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := covered(c.heldUUID, c.heldSeq, c.provenUUID, c.provenSeq); got != c.want {
				t.Fatalf("covered(%d,%d,%d,%d) = %v, want %v",
					c.heldUUID, c.heldSeq, c.provenUUID, c.provenSeq, got, c.want)
			}
		})
	}
}
