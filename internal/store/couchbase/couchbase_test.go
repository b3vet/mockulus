// SPDX-License-Identifier: Apache-2.0

package couchbase

import (
	"encoding/json"
	"testing"

	"github.com/couchbase/gocb/v2"
)

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

// The published watermark is the same pair of decisions taken across pods, and
// it goes wrong the same two quiet ways — except that here the merge is done by
// hand, because the SDK's own one keeps the wrong token, and the document has
// to survive a round trip through a decoder that reads a UUID as a signed
// integer. All three are cheap to get wrong and impossible to notice from a
// running cluster, so they are pinned here.

func TestFoldKeepsTheNewerPositionPerVbucket(t *testing.T) {
	held := publishedWrites{"b": {
		1: {uuid: 7, seq: 10},
		2: {uuid: 7, seq: 10},
		3: {uuid: 7, seq: 10},
		4: {uuid: 7, seq: 10},
	}}
	moved := held.fold(publishedWrites{"b": {
		1: {uuid: 7, seq: 11}, // later in the same history
		2: {uuid: 7, seq: 9},  // earlier in the same history
		3: {uuid: 8, seq: 2},  // a new history, restarted numbering
		5: {uuid: 9, seq: 1},  // a vbucket nobody had published
	}})
	if !moved {
		t.Fatal("fold reported nothing moved, but four vbuckets were offered newer positions")
	}

	want := map[uint16]tokenPosition{
		1: {uuid: 7, seq: 11},
		2: {uuid: 7, seq: 10},
		3: {uuid: 8, seq: 2},
		4: {uuid: 7, seq: 10},
		5: {uuid: 9, seq: 1},
	}
	for vb, expected := range want {
		if got := held["b"][vb]; got != expected {
			t.Errorf("vbucket %d = %+v, want %+v", vb, got, expected)
		}
	}
}

// A merge that moves nothing is what tells the publisher not to write, so it
// answering "moved" for a position already held would put every admin call into
// a CAS round it does not need.
func TestFoldReportsNothingMovedWhenEveryPositionIsAlreadyHeld(t *testing.T) {
	held := publishedWrites{"b": {1: {uuid: 7, seq: 10}}}
	if held.fold(publishedWrites{"b": {1: {uuid: 7, seq: 10}}}) {
		t.Error("folding a position back into itself reported a change")
	}
	if held.fold(publishedWrites{"b": {1: {uuid: 7, seq: 9}}}) {
		t.Error("folding an older position reported a change")
	}
}

// Other buckets are untouched: a sequence number names a position in the
// vbucket of the bucket it came from, and the two are unrelated numbers.
func TestFoldKeepsBucketsApart(t *testing.T) {
	held := publishedWrites{"b": {1: {uuid: 7, seq: 10}}}
	held.fold(publishedWrites{"other": {1: {uuid: 3, seq: 1}}})
	if got := held["b"][1]; got != (tokenPosition{uuid: 7, seq: 10}) {
		t.Errorf("a fold of another bucket moved this one: %+v", got)
	}
	if got := held["other"][1]; got != (tokenPosition{uuid: 3, seq: 1}) {
		t.Errorf("other bucket = %+v, want the folded position", got)
	}
}

func TestPublishedWritesRoundTripThroughTheDocument(t *testing.T) {
	want := publishedWrites{"mockulus": {
		0:    {uuid: 279851321537679, seq: 1},
		7:    {uuid: 1, seq: 0},
		1023: {uuid: 18446744073709551615, seq: 18446744073709551614},
	}}
	doc, err := marshalWrites(want, canonicalUUID)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := unmarshalWrites(doc)
	if err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	for vb, pos := range want["mockulus"] {
		if got["mockulus"][vb] != pos {
			t.Errorf("vbucket %d came back as %+v, want %+v", vb, got["mockulus"][vb], pos)
		}
	}
}

// The document is written in gocb's own encoding rather than a private one, so
// that claim is worth checking against the SDK rather than against this file:
// what marshalWrites produces is what MutationState produces.
func TestTheDocumentIsInTheSDKsOwnEncoding(t *testing.T) {
	doc, err := marshalWrites(publishedWrites{"mockulus": {7: {uuid: 279851321537679, seq: 42}}},
		canonicalUUID)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"mockulus":{"7":[42,"279851321537679"]}}`
	if string(doc) != want {
		t.Fatalf("document = %s, want %s", doc, want)
	}

	state := gocb.NewMutationState()
	if err := json.Unmarshal(doc, state); err != nil {
		t.Fatalf("the SDK could not read its own encoding back: %v", err)
	}
	back, err := state.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal the state: %v", err)
	}
	if string(back) != want {
		t.Fatalf("the SDK re-encoded it as %s, want %s", back, want)
	}
}

// The reason the merge is done by hand at all: gocb aggregates by keeping
// whichever token for a vbucket it saw *last*, not whichever is newest. Adding
// two states together and marshalling the result therefore publishes the older
// position about half the time — a requirement that reads like one and is weaker
// than one, which is the failure that leaves a peer's reload believing it has
// waited for a write it has not.
//
// The behaviour is pinned against the SDK rather than left to the comment: if a
// later gocb makes this newest-wins, the hand-rolled fold is redundant and this
// is what says so.
func TestTheSDKsOwnMergeKeepsTheLastPositionRatherThanTheNewest(t *testing.T) {
	// Two positions for one vbucket, newer first — the order a pod publishing
	// after a peer produces, and the order that loses the newer one.
	state := gocb.NewMutationState()
	if err := json.Unmarshal([]byte(`{"mockulus":{"7":[11,"1"]}}`), state); err != nil {
		t.Fatalf("read the newer position: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"mockulus":{"7":[5,"1"]}}`), state); err != nil {
		t.Fatalf("read the older position: %v", err)
	}
	merged, err := state.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal the state: %v", err)
	}
	if string(merged) != `{"mockulus":{"7":[5,"1"]}}` {
		t.Fatalf("the SDK aggregated the two positions as %s; if it now keeps the "+
			"newest, publishedWrites.fold no longer has to", merged)
	}

	// The same two positions through the driver's merge, which keeps the newer.
	held := publishedWrites{"mockulus": {7: {uuid: 1, seq: 11}}}
	held.fold(publishedWrites{"mockulus": {7: {uuid: 1, seq: 5}}})
	if got := held["mockulus"][7]; got.seq != 11 {
		t.Errorf("fold kept sequence number %d, want the newer 11", got.seq)
	}
}

// gocb reads the UUID with strconv.Atoi, so a UUID past the signed range takes
// the whole document's requirements down rather than its own. Couchbase has not
// been seen to issue one, which is exactly why nothing would notice.
func TestAHighUUIDStillReachesTheScanRequirement(t *testing.T) {
	const uuid = uint64(1) << 63
	naive, err := marshalWrites(publishedWrites{"mockulus": {7: {uuid: uuid, seq: 42}}}, canonicalUUID)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(naive, gocb.NewMutationState()); err == nil {
		t.Fatal("the SDK now reads an unsigned UUID; the two renderings can collapse into one")
	}

	state, err := (publishedWrites{"mockulus": {7: {uuid: uuid, seq: 42}}}).mutationState()
	if err != nil {
		t.Fatalf("mutationState: %v", err)
	}
	back, err := state.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal the state: %v", err)
	}
	const want = `{"mockulus":{"7":[42,"9223372036854775808"]}}`
	if string(back) != want {
		t.Fatalf("the requirement reached the SDK as %s, want %s", back, want)
	}
}

// A document that does not decode has to say so rather than being read as an
// empty requirement, which is the difference between degrading loudly and
// degrading in silence.
func TestUnmarshalWritesRefusesADocumentItCannotRead(t *testing.T) {
	cases := map[string]string{
		"a truncated entry":            `{"mockulus":{"7":[42]}}`,
		"a vbucket id that is not one": `{"mockulus":{"seven":[42,"1"]}}`,
		"a vbucket id past 16 bits":    `{"mockulus":{"70000":[42,"1"]}}`,
		"a uuid that is not a number":  `{"mockulus":{"7":[42,"abc"]}}`,
		"a uuid written as a number":   `{"mockulus":{"7":[42,1]}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalWrites([]byte(doc)); err == nil {
				t.Fatalf("%s decoded without complaint", doc)
			}
		})
	}
}

// The memo exists so a requirement the cluster will never satisfy costs one
// failed scan per version of the document rather than one per reload, and so
// that a republish earns it back.
func TestRefusedWatermarkIsDistrustedOnlyAtTheVersionThatFailed(t *testing.T) {
	var p publishedWatermark
	if !p.trusts(11) {
		t.Fatal("a watermark nothing has refused was distrusted")
	}
	p.refuse(11)
	if p.trusts(11) {
		t.Error("the refused version is still trusted")
	}
	if !p.trusts(12) {
		t.Error("a republished version did not earn its trust back")
	}
}
