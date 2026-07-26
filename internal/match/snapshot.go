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
	// descending — so the first stub that matches in full is the one WireMock
	// would have picked (SPEC §5.3).
	Ordered []*stub.CompiledStub

	// ByFullURL maps "METHOD\x00/path?query" to indexes into Ordered.
	ByFullURL map[string][]int32
	// ByPath maps "METHOD\x00/path" to indexes into Ordered.
	ByPath map[string][]int32
	// Patterns holds stubs whose URL criterion cannot be hashed: the pattern
	// and template kinds, and stubs with no URL criterion at all.
	Patterns []int32

	// Settings is the deployment's global response delay, or nil when nobody has
	// set one. It rides here rather than in a mutex-guarded field because the
	// serve path reads it on every matched response, and the snapshot pointer it
	// already loads is the cheapest place to put it (P1). Nil rather than a zero
	// value so an unconfigured instance skips the composition outright (P2).
	Settings *stub.Settings

	// byID indexes Ordered by stub id, for admin reads off the hot path.
	byID map[string]*stub.CompiledStub
	// scenarios maps a scenario name to the states its member stubs mention.
	scenarios map[string][]string
}

// EmptySnapshot is the snapshot served before the first load completes.
func EmptySnapshot() *Snapshot {
	return &Snapshot{
		ByFullURL: map[string][]int32{},
		ByPath:    map[string][]int32{},
		byID:      map[string]*stub.CompiledStub{},
		scenarios: map[string][]string{},
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
		scenarios: map[string][]string{},
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

		if cs.Scenario != nil {
			s.recordScenario(cs.Scenario)
		}
	}
	return s
}

// recordScenario collects the states a scenario's stubs mention, which is what
// the admin API reports as possibleStates and what a state write validates
// against (SPEC §9.1, §9.4).
func (s *Snapshot) recordScenario(ref *stub.ScenarioRef) {
	states := s.scenarios[ref.Name]
	add := func(state string) {
		if state == "" {
			return
		}
		for _, existing := range states {
			if existing == state {
				return
			}
		}
		states = append(states, state)
	}
	// Every scenario has the initial state whether or not a stub names it.
	add(ScenarioStarted)
	add(ref.RequiredState)
	add(ref.NewState)
	sort.Strings(states)
	s.scenarios[ref.Name] = states
}

// ScenarioStarted is the initial state of every scenario (SPEC §9.1).
const ScenarioStarted = "Started"

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

// Scenarios returns the scenario definitions derived from the stubs, by name.
func (s *Snapshot) Scenarios() map[string][]string { return s.scenarios }

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
//
// gate, when non-nil, is consulted for stubs in a scenario. Returning false
// treats the stub as non-matching and iteration continues (SPEC §9.2).
func (s *Snapshot) Match(req *ParsedRequest, gate ScenarioGate, evaluated *int) *stub.CompiledStub {
	if len(s.Ordered) == 0 {
		return nil
	}

	var lists [candidateLists][]int32
	lists[0] = req.indexLookup(s.ByFullURL, req.Method, req.FullURL)
	lists[1] = req.indexLookup(s.ByFullURL, anyMethod, req.FullURL)
	lists[2] = req.indexLookup(s.ByPath, req.Method, req.Path)
	lists[3] = req.indexLookup(s.ByPath, anyMethod, req.Path)
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

		if !fullMatch(cs, req) {
			continue
		}
		if cs.Scenario != nil && gate != nil && !gate(cs.Scenario, req) {
			// A scenario-gated stub whose state does not match is treated as
			// non-matching, and iteration continues to the next candidate.
			continue
		}
		if evaluated != nil {
			*evaluated = seen
		}
		return cs
	}

	if evaluated != nil {
		*evaluated = seen
	}
	return nil
}

// ScenarioGate reports whether a scenario-member stub may serve this request.
// It is a function rather than an interface so the engine has no dependency on
// the scenario client, and so a snapshot can be matched against in tests with
// no store at all.
type ScenarioGate func(ref *stub.ScenarioRef, req *ParsedRequest) bool

// fullMatch evaluates every criterion a stub specifies, cheapest first
// (SPEC §6.5). A candidate drawn from an exact-URL index has already satisfied
// its URL criterion, but re-checking it costs one string comparison and keeps
// this function correct for every caller, including near-miss scoring.
func fullMatch(cs *stub.CompiledStub, req *ParsedRequest) bool {
	if !cs.MatchesMethod(req.Method) {
		return false
	}
	// Bindings belong to the stub being evaluated; clearing here means a
	// previous candidate's variables can never leak into this one's criteria.
	if cs.URLKind == stub.URLTemplate {
		req.ClearPathVars()
	}
	if !matchURL(cs, req) {
		return false
	}

	// Most stubs stop here. Checking once is cheaper than walking five empty
	// slices, and it keeps the dominant exact-URL case free of any further work.
	if !cs.HasCriteriaBeyondURL() {
		return true
	}

	for _, c := range cs.Headers {
		if !c.Matcher.Match(req.HeaderSubject(c.Name)) {
			return false
		}
	}
	for _, c := range cs.Query {
		if !c.Matcher.Match(req.QuerySubject(c.Name)) {
			return false
		}
	}
	for _, c := range cs.Cookies {
		if !c.Matcher.Match(req.CookieSubject(c.Name)) {
			return false
		}
	}
	if cs.BasicAuth != "" && !matchesBasicAuth(req.HeaderValues("Authorization"), cs.BasicAuth) {
		return false
	}
	for _, c := range cs.PathParams {
		if !c.Matcher.Match(req.PathVarSubject(c.Name)) {
			return false
		}
	}
	// Form parsing reads the body, so it comes after everything that does not.
	for _, c := range cs.Form {
		if !c.Matcher.Match(req.FormSubject(c.Name)) {
			return false
		}
	}
	// Body matchers were ordered cheapest-first at compile time.
	for _, m := range cs.BodyMatchers {
		if !m.Match(req.BodySubject()) {
			return false
		}
	}
	return true
}

func matchURL(cs *stub.CompiledStub, req *ParsedRequest) bool {
	switch cs.URLKind {
	case stub.URLAny:
		return true

	case stub.URLExactFull:
		// Byte-exact against path and query as received, which is why query
		// parameter order matters for this criterion.
		return cs.URLLiteral == req.FullURL

	case stub.URLExactPath:
		return cs.URLLiteral == req.Path

	case stub.URLPatternFull:
		if !strings.HasPrefix(req.FullURL, cs.LiteralPrefix) {
			return false
		}
		return cs.URLRegex.MatchString(req.FullURL)

	case stub.URLPatternPath:
		if !strings.HasPrefix(req.Path, cs.LiteralPrefix) {
			return false
		}
		return cs.URLRegex.MatchString(req.Path)

	case stub.URLTemplate:
		if !strings.HasPrefix(req.Path, cs.LiteralPrefix) {
			return false
		}
		return cs.PathTemplate.Match(req.Path, req.BindPathVar)

	default:
		return false
	}
}

// matchesBasicAuth reports whether any Authorization header satisfies a
// basicAuthCredentials criterion, whose want is the canonical header value the
// stub compiled to.
//
// Only the scheme token folds. RFC 7235 defines it as case-insensitive and
// WireMock honours that — it serves "basic YWxpY2U6czNjcmV0" — but what follows
// the space is the base64 of "user:password" and is compared byte for byte,
// because base64 is case-significant and folding it would admit credentials the
// stub never declared. The space is part of the fixed prefix: WireMock rejects
// a tab or a second one there, so this does too.
//
// Every value is tried, not just the first, which is how a header criterion
// behaves everywhere else and how WireMock behaves here.
func matchesBasicAuth(values []string, want string) bool {
	n := len(stub.BasicAuthPrefix)
	for _, got := range values {
		// Equal lengths reject most non-candidates before any comparison, and
		// leave both slices below in range.
		if len(got) != len(want) {
			continue
		}
		if strings.EqualFold(got[:n], want[:n]) && got[n:] == want[n:] {
			return true
		}
	}
	return false
}

// SplitURL separates a request target into its path and its full path+query
// form, both of which the indexes key on.
func SplitURL(requestURI string) (path, full string) {
	if i := strings.IndexByte(requestURI, '?'); i >= 0 {
		return requestURI[:i], requestURI
	}
	return requestURI, requestURI
}
