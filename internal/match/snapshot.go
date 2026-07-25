// SPDX-License-Identifier: Apache-2.0

// Package match holds the serving engine: the immutable snapshot every mock
// request is matched against, the RCU swap that replaces it, and the matching
// algorithm itself.
//
// The engine is the reason mockulus can scale horizontally: a request performs
// exactly one atomic pointer load and then runs against memory that no writer
// will ever mutate. Snapshots are built off the request path and swapped whole
// (SPEC §6.2), so there are no locks and no I/O between accepting a request and
// writing its response (P1).
package match

import (
	"sort"
	"strings"

	"github.com/b3vet/mockulus/internal/stub"
)

// methodSep separates method from URL in the index keys. A NUL byte cannot
// occur in either, so keys are unambiguous without escaping.
const methodSep = "\x00"

// anyMethod is the index key component used for stubs that accept any method.
const anyMethod = "ANY"

// Snapshot is the immutable serving state: an ordered stub list whose iteration
// order *is* selection order, plus the indexes that avoid scanning it.
type Snapshot struct {
	// Epoch is the store change counter this snapshot was built from.
	Epoch uint64

	// Ordered is sorted by priority ascending, then insertion sequence
	// descending — so the first match found is the one WireMock would pick
	// (SPEC §5.3).
	Ordered []*stub.CompiledStub

	// ByFullURL maps "METHOD\x00/path?query" to indexes into Ordered.
	ByFullURL map[string][]int32
	// ByPath maps "METHOD\x00/path" to indexes into Ordered.
	ByPath map[string][]int32
	// Patterns holds stubs whose URL criterion cannot be hashed: pattern and
	// template forms, and stubs with no URL criterion at all.
	Patterns []int32

	// byID indexes Ordered by stub id, for admin reads off the hot path.
	byID map[string]*stub.CompiledStub
}

// EmptySnapshot is the snapshot served before the first load completes.
func EmptySnapshot() *Snapshot {
	return &Snapshot{
		ByFullURL: map[string][]int32{},
		ByPath:    map[string][]int32{},
		byID:      map[string]*stub.CompiledStub{},
	}
}

// BuildSnapshot orders the given stubs into selection order and indexes them.
// The input slice is not retained.
func BuildSnapshot(stubs []*stub.CompiledStub, epoch uint64) *Snapshot {
	ordered := make([]*stub.CompiledStub, len(stubs))
	copy(ordered, stubs)
	sortSelectionOrder(ordered)

	s := &Snapshot{
		Epoch:     epoch,
		Ordered:   ordered,
		ByFullURL: make(map[string][]int32, len(ordered)),
		ByPath:    make(map[string][]int32, len(ordered)),
		byID:      make(map[string]*stub.CompiledStub, len(ordered)),
	}

	for i, cs := range ordered {
		idx := int32(i)
		s.byID[cs.ID] = cs

		method := cs.Method
		if method == "" {
			method = anyMethod
		}
		switch cs.URLKind {
		case stub.URLExactFull:
			key := method + methodSep + cs.URLLiteral
			s.ByFullURL[key] = append(s.ByFullURL[key], idx)
		case stub.URLExactPath:
			key := method + methodSep + cs.URLLiteral
			s.ByPath[key] = append(s.ByPath[key], idx)
		default:
			s.Patterns = append(s.Patterns, idx)
		}
	}
	return s
}

// sortSelectionOrder applies SPEC §5.3: priority ascending, then insertion
// sequence descending so the most recently added stub wins ties.
func sortSelectionOrder(stubs []*stub.CompiledStub) {
	sort.SliceStable(stubs, func(i, j int) bool {
		if stubs[i].Priority != stubs[j].Priority {
			return stubs[i].Priority < stubs[j].Priority
		}
		return stubs[i].Seq > stubs[j].Seq
	})
}

// Len reports how many stubs the snapshot serves.
func (s *Snapshot) Len() int { return len(s.Ordered) }

// ByID returns a stub by its identifier.
func (s *Snapshot) ByID(id string) (*stub.CompiledStub, bool) {
	cs, ok := s.byID[id]
	return cs, ok
}

// candidateLists is the fixed number of pre-sorted index lists a lookup merges:
// the exact-URL and exact-path indexes probed under both the request's method
// and ANY, plus the pattern list.
const candidateLists = 5

// Match returns the stub that should serve the request, or nil.
//
// Candidate index lists are merged in ascending Ordered index — which is
// already priority-then-recency order — so the first candidate that matches in
// full is the stub WireMock would have selected (SPEC §6.3). Candidates are
// evaluated lazily: the merge stops at the first hit rather than collecting
// every match.
func (s *Snapshot) Match(method, path, fullURL string, evaluated *int) *stub.CompiledStub {
	if len(s.Ordered) == 0 {
		return nil
	}

	var lists [candidateLists][]int32
	lists[0] = s.ByFullURL[method+methodSep+fullURL]
	lists[1] = s.ByFullURL[anyMethod+methodSep+fullURL]
	lists[2] = s.ByPath[method+methodSep+path]
	lists[3] = s.ByPath[anyMethod+methodSep+path]
	lists[4] = s.Patterns

	var cursor [candidateLists]int
	seen := 0
	for {
		next := int32(-1)
		from := -1
		for i := range lists {
			if cursor[i] >= len(lists[i]) {
				continue
			}
			if idx := lists[i][cursor[i]]; next == -1 || idx < next {
				next, from = idx, i
			}
		}
		if from == -1 {
			break
		}
		cursor[from]++

		cs := s.Ordered[next]
		seen++
		if s.fullMatch(cs, method) {
			if evaluated != nil {
				*evaluated = seen
			}
			return cs
		}
	}

	if evaluated != nil {
		*evaluated = seen
	}
	return nil
}

// fullMatch evaluates every criterion a candidate specifies, cheapest first
// (SPEC §6.5). Candidates drawn from the URL indexes have already satisfied
// their URL criterion by construction; those drawn from the pattern list have
// not, and neither has the method for an index probed under ANY.
func (s *Snapshot) fullMatch(cs *stub.CompiledStub, method string) bool {
	return cs.MatchesMethod(method)
}

// SplitURL separates a request target into its path and its full path+query
// form, both of which the indexes key on.
func SplitURL(requestURI string) (path, full string) {
	if i := strings.IndexByte(requestURI, '?'); i >= 0 {
		return requestURI[:i], requestURI
	}
	return requestURI, requestURI
}
