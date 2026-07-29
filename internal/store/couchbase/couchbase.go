// SPDX-License-Identifier: Apache-2.0

// Package couchbase implements the store interfaces against Couchbase, which is
// the source of truth in a real deployment (SPEC §7.2, D3, D4, D6).
//
// The shape of this driver follows from one constraint: nothing here may be on
// the request path. Stubs are read in bulk at startup and on change, compiled
// once, and served from memory — so this package optimises for a cheap change
// signal and a fast bulk load, not for per-request latency. The two exceptions
// are deliberate and narrow: scenario state, read only by stubs that are in a
// scenario, and journal writes, which are batched and asynchronous.
package couchbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/couchbase/gocb/v2"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/store"
)

// Collection names within the configured scope (SPEC §7.2).
const (
	collMappings  = "mappings"
	collFiles     = "files"
	collScenarios = "scenarios"
	collJournal   = "journal"
	collMeta      = "meta"
)

// Document key prefixes and the fixed keys of the meta collection.
const (
	keyPrefixStub     = "stub::"
	keyPrefixFile     = "file::"
	keyPrefixScenario = "scenario::"
	keyPrefixJournal  = "journal::"

	keyEpoch    = "epoch"
	keySeq      = "seq"
	keySchema   = "schema"
	keySettings = "settings"
	keyWrites   = "writes"
)

// journalIndex is the GSI the journal's time-window queries need.
const journalIndex = "ix_journal_ts"

// loadParallelism bounds the concurrent scans of a bulk load. Sixteen keeps a
// 10k-stub load in the low seconds without turning the boot of one pod into a
// thundering herd against the cluster (SPEC §7.2).
const loadParallelism = 16

// publishAttempts bounds the CAS retries a pod makes against the shared
// watermark document. The contention is one write per admin call across the
// deployment, so eight rounds is far past the point where losing again means
// something other than bad luck — and a bounded failure that says so beats a
// loop that hides a hot document behind an admin call that never returns.
const publishAttempts = 8

// Store is the Couchbase-backed implementation of every store interface.
type Store struct {
	cluster *gocb.Cluster
	bucket  *gocb.Bucket
	scope   *gocb.Scope
	log     *slog.Logger

	mappings  *gocb.Collection
	files     *gocb.Collection
	scenarios *gocb.Collection
	journal   *gocb.Collection
	meta      *gocb.Collection

	bucketName string
	scopeName  string

	durability gocb.DurabilityLevel
	kvTimeout  time.Duration
	// scenarioKVTimeout is the tighter budget scenario reads get, because they
	// are the one store operation on the request path (SPEC §7.2, §9.2).
	scenarioKVTimeout time.Duration
	queryTimeout      time.Duration
	ephemeralTTL      time.Duration
	journalTTL        time.Duration

	// useRangeScan records whether the server supports KV range scan. It is
	// resolved once at boot and falls back to N1QL on older servers.
	useRangeScan bool

	// writes carries this pod's own mutations into the bulk read, so a reload
	// cannot answer from a disk view that predates them.
	writes writeWatermark
	// published is the same guarantee for the writes other pods made.
	published publishedWatermark
}

// writeWatermark holds the mutations this pod has made that no bulk read has
// yet been shown to observe.
//
// It exists because a KV range scan answers from the vbucket's *persisted*
// snapshot. A write acknowledged from memory is simply missing from the scan
// until the disk queue catches up — measured at up to ~100 ms against an idle
// single node, and the delete direction lags the same way — and nothing in the
// answer says so: the scan reports success with the document absent. A reload
// landing in that window builds a snapshot without a stub that was registered
// before it started, stamps it with the epoch that write bumped, and the poller
// of SPEC §8 then reads the two as converged. So the loss is not corrected on
// the next tick; it survives to the unconditional resync, five minutes away by
// default. Against a keyspace that was empty just before the write — a suite
// that resets and re-registers, which is the normal shape of CI usage — that
// same window empties the snapshot outright and every stub in the deployment
// stops matching.
//
// Snapshot requirements are the protocol's own answer: a scan carrying them
// either waits for the vbucket to reach the sequence number or fails, and both
// outcomes are correct here. A failure abandons the rebuild and keeps the
// previous snapshot, which is what SPEC §4.6 already asks for from a store this
// pod cannot read.
type writeWatermark struct {
	mu sync.Mutex
	// One token per vbucket, the newest written. Merging is not an
	// optimisation: gocb refuses a scan outright when two tokens name the same
	// partition with different UUIDs.
	tokens map[uint16]gocb.MutationToken
}

// note records a mutation this pod has just made.
func (w *writeWatermark) note(token *gocb.MutationToken) {
	if token == nil {
		// Only reachable with mutation tokens disabled on the connection, which
		// costs the guarantee rather than the load.
		return
	}
	vb := uint16(token.PartitionID())

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tokens == nil {
		w.tokens = make(map[uint16]gocb.MutationToken, 1)
	}
	previous, held := w.tokens[vb]
	if !held || supersedes(previous.PartitionUUID(), previous.SequenceNumber(),
		token.PartitionUUID(), token.SequenceNumber()) {
		w.tokens[vb] = *token
	}
}

// supersedes reports whether a newly seen mutation replaces the one held for
// its vbucket.
//
// A UUID and a sequence number only mean anything together. A failover starts a
// vbucket's history over and its sequence numbers begin again, so comparing the
// numbers across two UUIDs would leave a requirement pinned to a position that
// never existed on the surviving branch — and every reload after it failing on
// a scan the server can never satisfy. Across UUIDs, newest simply wins.
func supersedes(heldUUID, heldSeq, gotUUID, gotSeq uint64) bool {
	return heldUUID != gotUUID || gotSeq > heldSeq
}

// covered reports whether a read that observed one mutation has also observed
// the one still held for that vbucket, which is what makes the held one safe to
// forget. A different UUID proves nothing either way, so it does not count.
func covered(heldUUID, heldSeq, provenUUID, provenSeq uint64) bool {
	return heldUUID == provenUUID && heldSeq <= provenSeq
}

// pending returns what a bulk read has to observe, and nil when there is
// nothing — the steady state on a replica that only serves, which then pays
// nothing for any of this (P2).
func (w *writeWatermark) pending() map[uint16]gocb.MutationToken {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tokens) == 0 {
		return nil
	}
	out := make(map[uint16]gocb.MutationToken, len(w.tokens))
	for vb, token := range w.tokens {
		out[vb] = token
	}
	return out
}

// observed drops the requirements a completed read has satisfied.
//
// Dropping them is what stops a failed-over vbucket wedging every later reload
// behind a sequence number its new history does not contain. It is safe because
// a persisted snapshot only moves forward: a mutation one scan returned is
// visible to every scan after it, so the requirement has done its work.
func (w *writeWatermark) observed(proven map[uint16]gocb.MutationToken) {
	if len(proven) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for vb, token := range proven {
		current, held := w.tokens[vb]
		if held && covered(current.PartitionUUID(), current.SequenceNumber(),
			token.PartitionUUID(), token.SequenceNumber()) {
			delete(w.tokens, vb)
		}
	}
}

// consistentWith renders a requirement set in the form a scan takes.
func consistentWith(tokens map[uint16]gocb.MutationToken) *gocb.MutationState {
	if len(tokens) == 0 {
		return nil
	}
	state := gocb.NewMutationState()
	for _, token := range tokens {
		state.Add(token)
	}
	return state
}

// publishedWatermark is the other pod's half of the same problem.
//
// writeWatermark only knows about writes this pod made. A peer's write is
// invisible to it, and the epoch does not stand in for one: LoadAll reads the
// epoch before the scan and stamps the snapshot with it, so a pod rebuilding
// *because* a peer bumped the epoch can install a view that predates that
// peer's write, stamp it with the new epoch, and be read as converged. The stub
// is then missing on that pod until the unconditional resync — five minutes by
// default, against the one second SPEC §8 states.
//
// So the writer publishes what a reader has to observe. BumpEpoch merges its
// outstanding positions into meta::writes, and every pod folds that document
// into its own scan requirement, which makes a peer's write exactly as strong
// as a local one. The ordering carries the whole property: the requirement has
// to be readable before the epoch that sends anyone looking for it, so the
// publish happens before the counter moves.
//
// The struct itself holds only what has to survive between reloads.
type publishedWatermark struct {
	mu sync.Mutex
	// refused names the version of meta::writes a scan would not accept.
	//
	// A published requirement can become unsatisfiable for good: a failover
	// gives the vbucket a new history, the document still names the old one, and
	// the server answers vb-uuid mismatch to that scan forever. The reload
	// degrades to the local-only guarantee and continues either way, so nothing
	// wedges — but without this memo it would pay one doomed scan per reload
	// rather than one per version of the document. Any publish changes the CAS
	// and earns the document its trust back, which is also what heals it: the
	// merge takes the new history for every vbucket a pod writes to.
	refused gocb.Cas
}

// trusts reports whether a version of the document is worth folding in.
func (p *publishedWatermark) trusts(cas gocb.Cas) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refused != cas
}

// refuse records that a scan carrying this version of the document failed and a
// scan without it did not.
func (p *publishedWatermark) refuse(cas gocb.Cas) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused = cas
}

// tokenPosition is one vbucket's position. The two halves only mean anything
// together, which is why supersedes takes both.
type tokenPosition struct {
	uuid uint64
	seq  uint64
}

// publishedWrites is meta::writes as it sits in the keyspace: one position per
// vbucket, keyed by bucket and then by vbucket id.
//
// It is held in gocb's own MutationState encoding —
// {"<bucket>":{"<vbID>":[<seqno>,"<vbuuid>"]}} — so the document stays legible
// to anyone holding the SDK rather than being a private shape only this driver
// can read. Its size is bounded by construction rather than by pruning: a bucket
// has 1024 vbuckets and a vbucket has one entry, measured at 30,809 bytes with
// every one of them written — 30 bytes an entry, and that is the ceiling rather
// than a sample of it. Dropping an entry once every pod has observed it would
// need one pod to know something about all the others; a wall clock would
// approximate it and make the guarantee turn on cross-pod clock agreement, where
// a fast clock drops a live requirement and says nothing.
type publishedWrites map[string]map[uint16]tokenPosition

// localWrites renders this pod's outstanding tokens in the published shape, so
// the merge below is one operation on one type rather than two.
func localWrites(local map[uint16]gocb.MutationToken) publishedWrites {
	out := publishedWrites{}
	for vb, token := range local {
		positions, known := out[token.BucketName()]
		if !known {
			positions = map[uint16]tokenPosition{}
			out[token.BucketName()] = positions
		}
		positions[vb] = tokenPosition{
			uuid: token.PartitionUUID(),
			seq:  token.SequenceNumber(),
		}
	}
	return out
}

// fold merges newer positions into this set and reports whether anything moved.
//
// Merging by gocb.MutationState.Add and marshalling the result is the obvious
// way to do this and is wrong. MarshalJSON walks the token slice in order and
// overwrites each vbucket's entry as it goes, so what survives is whichever
// token came last rather than whichever is newest — and for two states added
// together that is the older one about half the time, which leaves a
// requirement weaker than the code asking for it appears to be. The merge is
// done here instead, under supersedes: within one history the higher sequence
// number wins, and across histories the incoming position wins, because a
// failover restarts a vbucket's numbering and comparing the numbers across
// UUIDs would pin the requirement to a position that never existed.
//
// The incoming side is always the fresher one, in both directions this is used.
// A pod publishing merges what it wrote moments ago into what it just read, and
// a pod reading merges its own outstanding writes into a document that may be
// arbitrarily old — so on a UUID disagreement the position drawn from a live
// connection is the one that names the surviving history.
func (w publishedWrites) fold(newer publishedWrites) bool {
	changed := false
	for bucket, incoming := range newer {
		positions, known := w[bucket]
		if !known {
			positions = map[uint16]tokenPosition{}
			w[bucket] = positions
		}
		for vb, pos := range incoming {
			current, held := positions[vb]
			if held && !supersedes(current.uuid, current.seq, pos.uuid, pos.seq) {
				continue
			}
			positions[vb] = pos
			changed = true
		}
	}
	return changed
}

// count reports how many vbucket positions the set holds.
func (w publishedWrites) count() int {
	n := 0
	for _, positions := range w {
		n += len(positions)
	}
	return n
}

// mutationState renders the set in the form a scan takes. gocb offers no way to
// build a token from parts — the constructor is an operation result, and
// MutationState.Internal is marked unsupported — so the JSON decoder is the
// supported road back in.
func (w publishedWrites) mutationState() (*gocb.MutationState, error) {
	doc, err := marshalWrites(w, sdkUUID)
	if err != nil {
		return nil, err
	}
	state := gocb.NewMutationState()
	if err := json.Unmarshal(doc, state); err != nil {
		return nil, fmt.Errorf("decode the published watermark into a scan requirement: %w", err)
	}
	return state, nil
}

// canonicalUUID renders a vbucket UUID the way gocb marshals one, which is what
// the stored document holds.
func canonicalUUID(uuid uint64) string {
	return strconv.FormatUint(uuid, 10)
}

// sdkUUID renders a vbucket UUID for gocb's own decoder, which reads it with
// strconv.Atoi — a signed parse, so a UUID above 2^63 is rejected and takes
// every other vbucket's requirement in the document down with it rather than
// its own. Two's complement survives that parse exactly, because the SDK
// converts the result straight to its unsigned UUID type. Couchbase has only
// been seen to issue 48-bit UUIDs (measured maximum 2.8e14, just under 2^48),
// so this guards a parser rather than a path a cluster reaches today.
func sdkUUID(uuid uint64) string {
	if uuid > math.MaxInt64 {
		return strconv.FormatInt(int64(uuid), 10)
	}
	return strconv.FormatUint(uuid, 10)
}

// marshalWrites renders a set in gocb's MutationState encoding, with the
// vbucket UUID written by the caller's choice of renderer.
func marshalWrites(w publishedWrites, renderUUID func(uint64) string) ([]byte, error) {
	doc := make(map[string]map[string][2]json.RawMessage, len(w))
	for bucket, positions := range w {
		entries := make(map[string][2]json.RawMessage, len(positions))
		for vb, pos := range positions {
			entries[strconv.FormatUint(uint64(vb), 10)] = [2]json.RawMessage{
				json.RawMessage(strconv.FormatUint(pos.seq, 10)),
				json.RawMessage(strconv.Quote(renderUUID(pos.uuid))),
			}
		}
		doc[bucket] = entries
	}
	return json.Marshal(doc)
}

// unmarshalWrites reads the document back.
//
// The parse is done here rather than through gocb's MutationState so a UUID is
// read as the unsigned 64-bit number it is, and so a document that does not
// decode names what was wrong with it instead of failing a whole reload behind
// an SDK error.
func unmarshalWrites(data []byte) (publishedWrites, error) {
	var doc map[string]map[string][]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make(publishedWrites, len(doc))
	for bucket, entries := range doc {
		positions := make(map[uint16]tokenPosition, len(entries))
		for key, pair := range entries {
			vb, err := strconv.ParseUint(key, 10, 16)
			if err != nil {
				return nil, fmt.Errorf("vbucket id %q: %w", key, err)
			}
			if len(pair) != 2 {
				return nil, fmt.Errorf("vbucket %s carries %d fields, want a seqno and a uuid",
					key, len(pair))
			}
			var seq uint64
			if err = json.Unmarshal(pair[0], &seq); err != nil {
				return nil, fmt.Errorf("vbucket %s sequence number: %w", key, err)
			}
			var uuid string
			if err = json.Unmarshal(pair[1], &uuid); err != nil {
				return nil, fmt.Errorf("vbucket %s uuid: %w", key, err)
			}
			n, err := strconv.ParseUint(uuid, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("vbucket %s uuid: %w", key, err)
			}
			positions[uint16(vb)] = tokenPosition{uuid: n, seq: seq}
		}
		out[bucket] = positions
	}
	return out, nil
}

// scanRequirement is what one bulk read has to observe before its answer can be
// believed.
type scanRequirement struct {
	// state is the union handed to the scan: this pod's outstanding writes and
	// the positions its peers published.
	state *gocb.MutationState
	// local is the same requirement with the published half removed, which is
	// both what a refused scan retries with and what the read reports as
	// observed once it succeeds.
	local map[uint16]gocb.MutationToken
	// publishedCas names the version of meta::writes state was built from, and
	// is zero when none was folded in — which is also what says a retry without
	// it would be a different request rather than the same one twice.
	publishedCas gocb.Cas
}

// requireFor builds the requirement for one bulk read.
//
// It must be called after the epoch the snapshot will be stamped with has been
// read, and that is not incidental: a publish precedes the bump that caused it,
// so a document read after the epoch holds every position that epoch accounts
// for. Read the other way round it would hold neither the peer's position nor
// any reason to wait for it.
func (s *Store) requireFor(ctx context.Context) scanRequirement {
	req := scanRequirement{local: s.writes.pending()}
	req.state = consistentWith(req.local)
	if !s.useRangeScan {
		// The N1QL arm reads at RequestPlus, which is cross-pod consistent by
		// construction, so there is nothing here to fold in and no reason to pay
		// the get for it.
		return req
	}
	state, cas, ok := s.foldPublished(ctx, req.local)
	if !ok {
		return req
	}
	req.state = state
	req.publishedCas = cas
	return req
}

// foldPublished merges the published positions into this pod's own.
//
// Every failure here degrades to the local-only guarantee and says so, rather
// than refusing to rebuild. A pod that cannot read one meta document is still a
// pod that can serve the stubs it can read, and the alternative — a reload that
// fails because a watermark was unreadable — turns a weaker guarantee into no
// snapshot at all (SPEC §4.6).
func (s *Store) foldPublished(ctx context.Context, local map[uint16]gocb.MutationToken) (*gocb.MutationState, gocb.Cas, bool) {
	published, cas, err := s.readPublished(ctx)
	switch {
	case errors.Is(err, gocb.ErrDocumentNotFound):
		// The ordinary state of a keyspace that has taken no admin write. A
		// warning here would fire on every reload of every idle deployment.
		s.log.Debug("no cross-pod write watermark has been published yet")
		return nil, 0, false
	case err != nil:
		s.log.Warn("could not read the cross-pod write watermark; this reload "+
			"carries only this pod's own writes", "error", err)
		return nil, 0, false
	case !s.published.trusts(cas):
		return nil, 0, false
	}

	// This bucket's positions only. A sequence number names a position in the
	// vbucket of the bucket it was drawn from, and gocb applies whatever it is
	// handed to the collection being scanned without checking which bucket the
	// token came from.
	for bucket := range published {
		if bucket != s.bucketName {
			delete(published, bucket)
		}
	}
	published.fold(localWrites(local))
	if published.count() == 0 {
		return nil, 0, false
	}

	state, err := published.mutationState()
	if err != nil {
		s.log.Warn("the cross-pod write watermark does not render as a scan "+
			"requirement; this reload carries only this pod's own writes", "error", err)
		return nil, 0, false
	}
	return state, cas, true
}

// errUnreadableWatermark marks a meta::writes document that is present but does
// not decode. It is separated from every other failure because it is the one a
// publisher can repair: a reader can only degrade around it, and left alone it
// would stop every pod publishing for good.
var errUnreadableWatermark = errors.New("the cross-pod write watermark does not decode")

// readPublished reads meta::writes and the version it was read at. The SDK's
// error is returned unwrapped: the caller distinguishes an absent document from
// an unreadable one, and they degrade differently. A document that does not
// decode still yields its version, so the repair can be made at it rather than
// over the top of a peer who repaired it first.
func (s *Store) readPublished(ctx context.Context) (publishedWrites, gocb.Cas, error) {
	res, err := s.meta.Get(keyWrites, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return nil, 0, err
	}
	var doc json.RawMessage
	if err = res.Content(&doc); err != nil {
		return nil, res.Cas(), fmt.Errorf("%w: %w", errUnreadableWatermark, err)
	}
	published, err := unmarshalWrites(doc)
	if err != nil {
		return nil, res.Cas(), fmt.Errorf("%w: %w", errUnreadableWatermark, err)
	}
	return published, res.Cas(), nil
}

// publishWrites merges this pod's outstanding positions into meta::writes.
//
// The document is shared by every pod, so a plain upsert is not an option: it
// would drop whatever a peer published between this pod's read and its write,
// and the position it dropped is precisely the one some third pod is about to
// need. It is written under CAS instead — read, merge, replace at the version
// read — and gives up after a bounded number of rounds rather than looping
// against a document it is losing to.
func (s *Store) publishWrites(ctx context.Context) error {
	local := s.writes.pending()
	if len(local) == 0 {
		// A bump with nothing of this pod's behind it: a settings change, or
		// writes a reload has already been shown to observe. A deployment that
		// never writes never reaches this at all, and one that only serves never
		// calls it (P2).
		return nil
	}

	for range publishAttempts {
		published, cas, err := s.readPublished(ctx)
		switch {
		case errors.Is(err, gocb.ErrDocumentNotFound):
			published, cas = publishedWrites{}, 0
		case errors.Is(err, errUnreadableWatermark):
			// A document nobody can decode carries no requirement anybody can
			// use, so replacing it gives up nothing — and not replacing it stops
			// every pod in the deployment publishing for as long as it sits
			// there. Written at the version it was read at like any other round,
			// so a peer that repaired it first wins rather than being undone.
			s.log.Warn("replacing an unreadable cross-pod write watermark", "error", err)
			published = publishedWrites{}
		case err != nil:
			return fmt.Errorf("read the cross-pod write watermark: %w", wrap(err))
		}

		if !published.fold(localWrites(local)) {
			// Every position of this pod's is already covered by what is there,
			// which is the normal outcome when a peer wrote the same vbucket
			// later and published first.
			return nil
		}
		doc, err := marshalWrites(published, canonicalUUID)
		if err != nil {
			return err
		}

		if cas == 0 {
			_, err = s.meta.Insert(keyWrites, json.RawMessage(doc), &gocb.InsertOptions{
				Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
			})
			if errors.Is(err, gocb.ErrDocumentExists) {
				continue
			}
		} else {
			_, err = s.meta.Replace(keyWrites, json.RawMessage(doc), &gocb.ReplaceOptions{
				Cas: cas, Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
			})
			if errors.Is(err, gocb.ErrCasMismatch) || errors.Is(err, gocb.ErrDocumentNotFound) {
				continue
			}
		}
		if err != nil {
			return wrap(err)
		}
		return nil
	}
	return fmt.Errorf("the cross-pod write watermark stayed contended for %d attempts",
		publishAttempts)
}

// Open connects to Couchbase and prepares the keyspace.
//
// Connection failure is not fatal to the process: SPEC §4.4 has the pod stay
// alive and not-ready, retrying forever, because a Couchbase outage during a
// rollout must not turn into a crash loop. That retry lives in the caller; this
// returns the error.
func Open(ctx context.Context, cfg config.Config, log *slog.Logger) (*Store, error) {
	cb := cfg.Couchbase

	cluster, err := gocb.Connect(cb.ConnStr, gocb.ClusterOptions{
		Authenticator: gocb.PasswordAuthenticator{
			Username: cb.Username,
			Password: cb.Password,
		},
		TimeoutsConfig: gocb.TimeoutsConfig{
			KVTimeout:      cb.KVTimeout.D(),
			QueryTimeout:   cb.QueryTimeout.D(),
			ConnectTimeout: 10 * time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("connect to couchbase: %w", err)
	}

	bucket := cluster.Bucket(cb.Bucket)
	if err := bucket.WaitUntilReady(10*time.Second, nil); err != nil {
		_ = cluster.Close(nil)
		return nil, fmt.Errorf("bucket %s not ready: %w", cb.Bucket, err)
	}

	s := &Store{
		cluster:           cluster,
		bucket:            bucket,
		log:               log,
		bucketName:        cb.Bucket,
		scopeName:         cb.Scope,
		durability:        durabilityLevel(cb.Durability),
		kvTimeout:         cb.KVTimeout.D(),
		scenarioKVTimeout: cfg.ScenarioKVTimeout.D(),
		queryTimeout:      cb.QueryTimeout.D(),
		ephemeralTTL:      cfg.EphemeralStubTTL.D(),
		journalTTL:        cfg.JournalTTL.D(),
	}

	if cb.ManageBucket {
		if err := s.bootstrap(ctx); err != nil {
			_ = cluster.Close(nil)
			return nil, fmt.Errorf("bootstrap keyspace: %w", err)
		}
	}

	s.scope = bucket.Scope(cb.Scope)
	s.mappings = s.scope.Collection(collMappings)
	s.files = s.scope.Collection(collFiles)
	s.scenarios = s.scope.Collection(collScenarios)
	s.journal = s.scope.Collection(collJournal)
	s.meta = s.scope.Collection(collMeta)

	s.useRangeScan = s.probeRangeScan(ctx)
	if !s.useRangeScan {
		log.Info("KV range scan is unavailable; bulk loads will use N1QL",
			"hint", "range scan needs Couchbase 7.6 or newer")
		if cb.ManageBucket {
			if err := s.ensurePrimaryIndex(ctx); err != nil {
				log.Warn("could not create the primary index the N1QL fallback needs", "error", err)
			}
		}
	}

	if err := s.checkSchema(ctx); err != nil {
		_ = cluster.Close(nil)
		return nil, err
	}
	return s, nil
}

func durabilityLevel(name string) gocb.DurabilityLevel {
	if name == "majority" {
		return gocb.DurabilityLevelMajority
	}
	return gocb.DurabilityLevelNone
}

// bootstrap creates any missing collections and indexes.
//
// Doing this from the application keeps the zero-config promise: pointing
// mockulus at an empty bucket is enough. Deployments whose application user
// lacks manager rights set manage_bucket: false and apply the DDL out of band
// (SPEC §7.2).
func (s *Store) bootstrap(ctx context.Context) error {
	mgr := s.bucket.Collections()

	if s.scopeName != "_default" {
		if err := mgr.CreateScope(s.scopeName, nil); err != nil && !errors.Is(err, gocb.ErrScopeExists) {
			return fmt.Errorf("create scope %s: %w", s.scopeName, err)
		}
	}

	for _, name := range []string{collMappings, collFiles, collScenarios, collJournal, collMeta} {
		spec := gocb.CollectionSpec{Name: name, ScopeName: s.scopeName}
		if err := mgr.CreateCollection(spec, nil); err != nil && !errors.Is(err, gocb.ErrCollectionExists) {
			return fmt.Errorf("create collection %s: %w", name, err)
		}
	}

	// Collections take a moment to become usable after creation.
	if err := s.bucket.WaitUntilReady(10*time.Second, nil); err != nil {
		return fmt.Errorf("keyspace not ready after bootstrap: %w", err)
	}

	return s.ensureJournalIndex(ctx)
}

// ensureJournalIndex creates the GSI backing journal time-window queries.
func (s *Store) ensureJournalIndex(ctx context.Context) error {
	stmt := fmt.Sprintf("CREATE INDEX `%s` IF NOT EXISTS ON `%s`.`%s`.`%s`(ts)",
		journalIndex, s.bucketName, s.scopeName, collJournal)
	_, err := s.cluster.Query(stmt, &gocb.QueryOptions{Context: ctx, Timeout: s.queryTimeout})
	if err != nil && !errors.Is(err, gocb.ErrIndexExists) {
		return fmt.Errorf("create journal index: %w", err)
	}
	return nil
}

// ensurePrimaryIndex creates the primary index the N1QL bulk-load fallback
// needs. It is created only in fallback mode: on a modern server the range scan
// makes it dead weight.
func (s *Store) ensurePrimaryIndex(ctx context.Context) error {
	for _, coll := range []string{collMappings, collFiles} {
		stmt := fmt.Sprintf("CREATE PRIMARY INDEX IF NOT EXISTS ON `%s`.`%s`.`%s`",
			s.bucketName, s.scopeName, coll)
		if _, err := s.cluster.Query(stmt, &gocb.QueryOptions{
			Context: ctx, Timeout: s.queryTimeout,
		}); err != nil && !errors.Is(err, gocb.ErrIndexExists) {
			return fmt.Errorf("create primary index on %s: %w", coll, err)
		}
	}
	return nil
}

// probeRangeScan reports whether the server supports KV range scan, by trying
// one. Feature detection by attempt rather than by version string: the version
// is not always what the deployment actually supports.
func (s *Store) probeRangeScan(ctx context.Context) bool {
	scanCtx, cancel := context.WithTimeout(ctx, s.kvTimeout)
	defer cancel()

	res, err := s.mappings.Scan(gocb.RangeScan{}, &gocb.ScanOptions{
		Context: scanCtx,
		IDsOnly: true,
	})
	if err != nil {
		return false
	}
	_ = res.Close()
	return true
}

// checkSchema refuses to start against documents written by a newer build.
//
// A schema guard is cheap and the alternative is bad: an older pod silently
// mis-reading newer documents during a rolling upgrade, which is exactly when
// nobody is watching.
func (s *Store) checkSchema(ctx context.Context) error {
	type schemaDoc struct {
		SchemaVersion int `json:"schemaVersion"`
	}

	res, err := s.meta.Get(keySchema, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			_, writeErr := s.meta.Upsert(keySchema, schemaDoc{SchemaVersion: store.SchemaVersion},
				&gocb.UpsertOptions{Context: ctx, Timeout: s.kvTimeout})
			return writeErr
		}
		return fmt.Errorf("read the schema marker: %w", err)
	}

	var doc schemaDoc
	if err := res.Content(&doc); err != nil {
		return fmt.Errorf("decode the schema marker: %w", err)
	}
	if doc.SchemaVersion > store.SchemaVersion {
		return fmt.Errorf(
			"the keyspace holds schema version %d but this build understands %d; "+
				"upgrade mockulus rather than running an older build against newer documents",
			doc.SchemaVersion, store.SchemaVersion)
	}
	return nil
}

// Close releases the cluster connection.
func (s *Store) Close(ctx context.Context) error {
	if s.cluster == nil {
		return nil
	}
	return s.cluster.Close(nil)
}

// storedDoc is the envelope a mapping is persisted in (SPEC §7.2). The mapping
// is held verbatim so a GET returns exactly what was registered.
type storedDoc struct {
	SchemaVersion int             `json:"schemaVersion"`
	Seq           uint64          `json:"seq"`
	Persistent    bool            `json:"persistent"`
	CreatedAt     time.Time       `json:"createdAt"`
	Mapping       json.RawMessage `json:"mapping"`
}

// LoadAll reads the complete current state.
//
// This is the only read on the reload path, which is what keeps convergence
// level-triggered: any epoch change reloads everything, so there is no delta
// bookkeeping and no missed-event class of bug (SPEC §8).
func (s *Store) LoadAll(ctx context.Context) ([]store.StoredStub, []store.StoredFile, uint64, error) {
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read epoch: %w", err)
	}

	// Taken before the reads rather than after, so a write that lands while they
	// run stays pending and it is the next reload that has to account for it —
	// and after the epoch above, which is what makes the published half of the
	// requirement cover everything that epoch was bumped for. Built once for
	// both scans, so a reload costs one extra KV get rather than one per
	// collection.
	req := s.requireFor(ctx)

	var (
		stubs []store.StoredStub
		files []store.StoredFile
		wg    sync.WaitGroup
		errMu sync.Mutex
		errs  []error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		loaded, err := s.loadStubs(ctx, req)
		errMu.Lock()
		defer errMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("load mappings: %w", err))
			return
		}
		stubs = loaded
	}()
	go func() {
		defer wg.Done()
		loaded, err := s.loadFiles(ctx, req)
		errMu.Lock()
		defer errMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("load files: %w", err))
			return
		}
		files = loaded
	}()
	wg.Wait()

	if len(errs) > 0 {
		return nil, nil, 0, errors.Join(errs...)
	}
	s.writes.observed(req.local)
	return stubs, files, epoch, nil
}

func (s *Store) loadStubs(ctx context.Context, req scanRequirement) ([]store.StoredStub, error) {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings, req)
	if err != nil {
		return nil, err
	}
	return s.decodeStubs(raw), nil
}

// decodeStubs turns the documents a bulk read returned into stored stubs.
//
// Every key the read produced comes back as a stub, whatever state its document
// was in. That is the property the caller depends on: a load reports the store's
// contents or it fails, and it never quietly reports less. A document dropped
// here would be indistinguishable from one that was deleted, and the rebuild
// would take the stub out of the serving snapshot on the strength of a decode
// error (SPEC §4.6). Carried through with no mapping it reaches the builder's
// quarantine instead, where it is counted and says so (SPEC §6.9).
func (s *Store) decodeStubs(raw map[string][]byte) []store.StoredStub {
	out := make([]store.StoredStub, 0, len(raw))
	for id, content := range raw {
		var doc storedDoc
		if err := json.Unmarshal(content, &doc); err != nil {
			s.log.Warn("stored mapping does not decode; it will be quarantined",
				"id", id, "error", err)
			out = append(out, store.StoredStub{ID: strings.TrimPrefix(id, keyPrefixStub)})
			continue
		}
		out = append(out, store.StoredStub{
			ID:            strings.TrimPrefix(id, keyPrefixStub),
			SchemaVersion: doc.SchemaVersion,
			Seq:           doc.Seq,
			Persistent:    doc.Persistent,
			CreatedAt:     doc.CreatedAt,
			Mapping:       doc.Mapping,
		})
	}
	return out
}

func (s *Store) loadFiles(ctx context.Context, req scanRequirement) ([]store.StoredFile, error) {
	raw, err := s.loadCollection(ctx, s.files, collFiles, req)
	if err != nil {
		return nil, err
	}

	out := make([]store.StoredFile, 0, len(raw))
	for id, content := range raw {
		var data []byte
		// Files are stored base64-wrapped in JSON so any byte sequence
		// round-trips through a JSON document store unharmed.
		if err := json.Unmarshal(content, &data); err != nil {
			s.log.Warn("stored file does not decode; skipping", "name", id, "error", err)
			continue
		}
		out = append(out, store.StoredFile{
			Name: strings.TrimPrefix(id, keyPrefixFile),
			Data: data,
		})
	}
	return out, nil
}

// loadCollection reads every document of a collection, by range scan where the
// server supports it and by N1QL otherwise.
func (s *Store) loadCollection(ctx context.Context, coll *gocb.Collection, name string, req scanRequirement) (map[string][]byte, error) {
	if s.useRangeScan {
		return s.scanCollection(ctx, coll, req)
	}
	return s.queryCollection(ctx, name)
}

// scanCollection reads a collection by range scan, carrying the requirement and
// degrading if the cluster will not accept it.
//
// The retry is not a retry of a flaky call: it is the answer to a requirement
// that has stopped being satisfiable. A failover leaves a published position
// naming a history the vbucket no longer has, and every scan carrying it is
// refused for as long as the document says so. Falling back to this pod's own
// writes gives up the cross-pod half of the guarantee and keeps the reload,
// which is the right way round for *that* failure — a stale-by-one-resync
// snapshot still serves, and no snapshot does not.
//
// Which failure it was decides whether the fallback is allowed at all, and only
// the server can say: a vb-uuid mismatch is status 168 and reaches gocb as
// ErrMutationTokenOutdated. Every other failure is refused the fallback on
// purpose. A scan carrying a requirement *waits* for the vbucket to reach the
// position, so the failure this rules out is the ordinary one under load — the
// wait outruns the scan budget and the read comes back a timeout. Retrying that
// without the requirement asks the same question with the guarantee removed, and
// it answers: a full collection read, no error, quietly missing the write the
// reload was triggered by, which is stamped with the new epoch and read as
// converged — which is the whole of what this watermark exists to prevent,
// reached through the watermark's own recovery path. Measured under eight-way
// write pressure it happened about once in 120 reloads (D-OPEN-10).
//
// So a timeout fails the reload instead, which keeps the previous snapshot and
// says so (SPEC §4.6). The pod is then behind and visibly behind, and the
// poller's next tick retries — where the fallback would have left it behind and
// looking converged until the next resync.
func (s *Store) scanCollection(ctx context.Context, coll *gocb.Collection, req scanRequirement) (map[string][]byte, error) {
	out, err := s.scanOnce(ctx, coll, req.state)
	if err == nil || req.publishedCas == 0 || !errors.Is(err, gocb.ErrMutationTokenOutdated) {
		return out, err
	}

	local, retryErr := s.scanOnce(ctx, coll, consistentWith(req.local))
	if retryErr != nil {
		return nil, err
	}
	s.published.refuse(req.publishedCas)
	s.log.Warn("the cluster refused the cross-pod write watermark; reloads carry "+
		"only this pod's own writes until the watermark is republished",
		"collection", coll.Name(), "error", err)
	return local, nil
}

func (s *Store) scanOnce(ctx context.Context, coll *gocb.Collection, require *gocb.MutationState) (map[string][]byte, error) {
	res, err := coll.Scan(gocb.RangeScan{}, &gocb.ScanOptions{
		Context:     ctx,
		Concurrency: loadParallelism,
		// The scan's own budget, because gocb's default for it is a separate
		// number from the KV one this driver configures — and a bulk read left
		// on that default is bounded by something no operator chose. The
		// N1QL arm of this same call already runs on the query budget, and it
		// is the same operation: one read of one collection, sized for 10k
		// stubs in low seconds (SPEC §7.2), not for a point lookup. Unbounded
		// by configuration it can also outlast resync_interval, which leaves
		// ticks queueing behind a rebuild — and the write path's splice takes
		// the same lock, so an admin registration waits it out too.
		Timeout: s.queryTimeout,
		// Sequence numbers are per vbucket and shared by every collection in the
		// bucket, so one watermark covers both scans: a token drawn from a
		// mapping write is a valid requirement for the file scan of the same
		// partition, and asking for it costs nothing extra.
		ConsistentWith: require,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Close() }()

	out := map[string][]byte{}
	for {
		row := res.Next()
		if row == nil {
			break
		}
		var content json.RawMessage
		if err := row.Content(&content); err != nil {
			// Recorded with no content rather than left out: the document is in
			// the collection, and a bulk read that omitted it would be reporting
			// a deletion that did not happen. Its decoders answer for the
			// content; this loop answers for the set of keys.
			s.log.Warn("could not read a scanned document", "id", row.ID(), "error", err)
			out[row.ID()] = nil
			continue
		}
		out[row.ID()] = content
	}
	return out, res.Err()
}

// queryCollection is the fallback for servers without KV range scan.
func (s *Store) queryCollection(ctx context.Context, name string) (map[string][]byte, error) {
	stmt := fmt.Sprintf("SELECT META().id AS id, * FROM `%s`.`%s`.`%s`",
		s.bucketName, s.scopeName, name)
	rows, err := s.cluster.Query(stmt, &gocb.QueryOptions{
		Context: ctx,
		Timeout: s.queryTimeout,
		// The index behind this statement is maintained asynchronously, so the
		// default consistency would drop a just-registered stub exactly the way
		// an unpersisted range scan does — same silent loss, same five-minute
		// wait for the resync to undo it. This is the reload; it is allowed to
		// wait for the index to catch up.
		ScanConsistency: gocb.QueryScanConsistencyRequestPlus,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]byte{}
	for rows.Next() {
		var row map[string]json.RawMessage
		if err := rows.Row(&row); err != nil {
			return nil, err
		}
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			// Nothing to record it under, and skipping it would report a
			// document the collection holds as absent — the one answer a bulk
			// read must never give (SPEC §4.6).
			return nil, fmt.Errorf("a row of %s carries no readable id: %w", name, err)
		}
		// Present with no content for the same reason a scanned row that would
		// not decode is: the key is what this call answers for.
		out[id] = row[name]
	}
	return out, rows.Err()
}

// PutStub writes one mapping, applying the ephemeral TTL when it is not
// persistent (SPEC §7.4, deviation #3).
func (s *Store) PutStub(ctx context.Context, stub store.StoredStub) error {
	doc := storedDoc{
		SchemaVersion: stub.SchemaVersion,
		Seq:           stub.Seq,
		Persistent:    stub.Persistent,
		CreatedAt:     stub.CreatedAt,
		Mapping:       stub.Mapping,
	}
	opts := &gocb.UpsertOptions{
		Context:         ctx,
		Timeout:         s.kvTimeout,
		DurabilityLevel: s.durability,
	}
	if !stub.Persistent && s.ephemeralTTL > 0 {
		opts.Expiry = s.ephemeralTTL
	}
	res, err := s.mappings.Upsert(keyPrefixStub+stub.ID, doc, opts)
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// GetStub reads one mapping.
func (s *Store) GetStub(ctx context.Context, id string) (store.StoredStub, error) {
	res, err := s.mappings.Get(keyPrefixStub+id, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredStub{}, wrap(err)
	}
	var doc storedDoc
	if err := res.Content(&doc); err != nil {
		return store.StoredStub{}, err
	}
	return store.StoredStub{
		ID:            id,
		SchemaVersion: doc.SchemaVersion,
		Seq:           doc.Seq,
		Persistent:    doc.Persistent,
		CreatedAt:     doc.CreatedAt,
		Mapping:       doc.Mapping,
	}, nil
}

// DeleteStub removes one mapping. Removing an absent id is not an error.
func (s *Store) DeleteStub(ctx context.Context, id string) error {
	res, err := s.mappings.Remove(keyPrefixStub+id, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	// Noted for the same reason a write is: a scan reads the persisted view, so
	// a stub deleted a moment ago is still in it and would be reinstated by the
	// reload the deletion itself triggers.
	s.writes.note(res.MutationToken())
	return nil
}

// DeleteAllStubs removes every mapping.
func (s *Store) DeleteAllStubs(ctx context.Context) error {
	return s.removeMappings(ctx, func(storedDoc) bool { return true })
}

// DeleteEphemeralStubs removes only the non-persistent mappings.
func (s *Store) DeleteEphemeralStubs(ctx context.Context) error {
	return s.removeMappings(ctx, func(doc storedDoc) bool { return !doc.Persistent })
}

// removeMappings deletes the mappings a predicate selects, key by key.
//
// One `DELETE FROM` statement is a single round trip instead of N, which is
// the obvious way to do this and is wrong in both directions.
//
// The statement's own scan is a KV sequential scan of the persisted view, so a
// stub registered milliseconds earlier is not there to be deleted and survives
// the reset outright — 7 of 20 against an idle single node. Nothing corrects
// that afterwards: the document really does exist, so every later reload keeps
// serving it, and the caller was told 200. And a DML reports no mutation
// tokens, so the reload the delete triggers has nothing holding the persisted
// view past the removals and can read back the documents it did remove.
//
// Removing by key fixes both with one mechanism rather than two. The read that
// chooses the keys is the watermarked bulk read, which is exactly the guarantee
// the statement lacked — it sees this pod's own writes or it fails — and each
// removal produces the token that stops the reload resurrecting it. The cost is
// N round trips on an admin call that is already allowed to be slow, bounded by
// the same concurrency a bulk load uses.
//
// A document that will not decode has no persistent flag to read, and a missing
// flag is what ephemeral means (SPEC §7.4), so a reset takes it rather than
// leaving behind a document no reload will ever serve.
func (s *Store) removeMappings(ctx context.Context, remove func(storedDoc) bool) error {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings, s.requireFor(ctx))
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(raw))
	for id, content := range raw {
		var doc storedDoc
		_ = json.Unmarshal(content, &doc)
		if remove(doc) {
			keys = append(keys, id)
		}
	}
	return s.removeKeys(ctx, s.mappings, "mappings", keys)
}

// removeKeys removes a set of documents from one collection, noting every token.
//
// A failure is reported rather than swallowed, so an incomplete reset reaches
// the operator as the 503 of SPEC §4.6 instead of as a keyspace that quietly
// still holds stubs. The removals that did land stay removed: a bulk delete has
// no transaction either way, and reporting the failure is what lets the caller
// retry an operation that is idempotent by construction.
func (s *Store) removeKeys(ctx context.Context, coll *gocb.Collection, what string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	workers := min(loadParallelism, len(keys))
	work := make(chan string)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
		first  error
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range work {
				res, err := coll.Remove(key, &gocb.RemoveOptions{
					Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
				})
				if errors.Is(err, gocb.ErrDocumentNotFound) {
					// Another pod's reset reached it first, which is the outcome
					// this call wanted.
					continue
				}
				if err != nil {
					mu.Lock()
					failed++
					if first == nil {
						first = err
					}
					mu.Unlock()
					continue
				}
				s.writes.note(res.MutationToken())
			}
		}()
	}
	for _, key := range keys {
		work <- key
	}
	close(work)
	wg.Wait()

	if first != nil {
		return fmt.Errorf("%d of %d %s could not be removed: %w",
			failed, len(keys), what, wrap(first))
	}
	return nil
}

// MarkAllPersistent makes every mapping durable and clears its expiry, backing
// `POST /__admin/mappings/save` (deviation #4).
//
// The TTL has to be cleared document by document: N1QL can set the field but
// not the expiry, and a document that still expires has not been saved.
func (s *Store) MarkAllPersistent(ctx context.Context) error {
	raw, err := s.loadCollection(ctx, s.mappings, collMappings, s.requireFor(ctx))
	if err != nil {
		return err
	}
	for id, content := range raw {
		var doc storedDoc
		if err := json.Unmarshal(content, &doc); err != nil {
			continue
		}
		if doc.Persistent {
			continue
		}
		mapping, err := store.PersistentMapping(doc.Mapping)
		if err != nil {
			// Undecodable documents are quarantined from serving anyway, and
			// failing the save for one would block it for every other stub.
			continue
		}
		doc.Mapping = mapping
		doc.Persistent = true
		res, err := s.mappings.Upsert(id, doc, &gocb.UpsertOptions{
			Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
			// Expiry zero clears the TTL.
			Expiry: 0,
		})
		if err != nil {
			return wrap(err)
		}
		s.writes.note(res.MutationToken())
	}
	return nil
}

// PutFile stores a response body file.
//
// The bytes are wrapped in JSON here rather than handed over raw, because gocb's
// default transcoder refuses a []byte outright: it cannot tell a document from a
// payload, so it declines to guess and the write fails before it leaves the
// process. Marshalling first produces the base64 string loadFiles and GetFile
// already read back, which is what makes any byte sequence survive a JSON
// document store (SPEC §7.2).
func (s *Store) PutFile(ctx context.Context, file store.StoredFile) error {
	body, err := json.Marshal(file.Data)
	if err != nil {
		return err
	}
	res, err := s.files.Upsert(keyPrefixFile+file.Name, json.RawMessage(body), &gocb.UpsertOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// GetFile reads one file.
func (s *Store) GetFile(ctx context.Context, name string) (store.StoredFile, error) {
	res, err := s.files.Get(keyPrefixFile+name, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredFile{}, wrap(err)
	}
	var data []byte
	if err := res.Content(&data); err != nil {
		return store.StoredFile{}, err
	}
	return store.StoredFile{Name: name, Data: data}, nil
}

// DeleteFile removes one file.
func (s *Store) DeleteFile(ctx context.Context, name string) error {
	res, err := s.files.Remove(keyPrefixFile+name, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	if errors.Is(err, gocb.ErrDocumentNotFound) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	s.writes.note(res.MutationToken())
	return nil
}

// ListFiles returns every stored file name.
func (s *Store) ListFiles(ctx context.Context) ([]string, error) {
	raw, err := s.loadCollection(ctx, s.files, collFiles, s.requireFor(ctx))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw))
	for id := range raw {
		names = append(names, strings.TrimPrefix(id, keyPrefixFile))
	}
	return names, nil
}

// NextSeq draws the next cluster-global insertion sequence.
//
// An atomic counter document is what makes newest-wins precedence work across
// replicas: two pods creating stubs at the same moment still get a total order
// (SPEC §5.3, §7.3).
func (s *Store) NextSeq(ctx context.Context) (uint64, error) {
	res, err := s.meta.Binary().Increment(keySeq, &gocb.IncrementOptions{
		Context: ctx, Timeout: s.kvTimeout, Initial: 1, Delta: 1,
	})
	if err != nil {
		return 0, wrap(err)
	}
	return res.Content(), nil
}

// Epoch reads the change counter. This is the one call every pod makes every
// sync interval, so it stays a single KV get of a counter document.
func (s *Store) Epoch(ctx context.Context) (uint64, error) {
	res, err := s.meta.Get(keyEpoch, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			// No writes have happened yet.
			return 0, nil
		}
		return 0, wrap(err)
	}
	var epoch uint64
	if err := res.Content(&epoch); err != nil {
		return 0, err
	}
	return epoch, nil
}

// BumpEpoch records that mappings, files or settings changed. Convergence keys
// off inequality rather than counting steps, so a lost increment costs nothing.
func (s *Store) BumpEpoch(ctx context.Context) (uint64, error) {
	// Before the counter moves, never after. The epoch is what sends every other
	// pod to the store, so a peer that saw the new epoch first would scan
	// without the requirement this write needs — which is the whole of what the
	// watermark exists to prevent.
	if err := s.publishWrites(ctx); err != nil {
		// The write itself already landed, so failing here would report a change
		// that happened as one that did not. What is lost is the strength of the
		// guarantee, not the change: other pods still converge, on
		// resync_interval instead of sync_interval, which is the behaviour this
		// deployment had before the watermark existed.
		s.log.Warn("could not publish this write's position for the other pods; "+
			"they may take until the next full resync to see it", "error", err)
	}

	res, err := s.meta.Binary().Increment(keyEpoch, &gocb.IncrementOptions{
		Context: ctx, Timeout: s.kvTimeout, Initial: 1, Delta: 1,
	})
	if err != nil {
		return 0, wrap(err)
	}
	return res.Content(), nil
}

// GetSettings reads the global settings document.
func (s *Store) GetSettings(ctx context.Context) (store.StoredSettings, error) {
	res, err := s.meta.Get(keySettings, &gocb.GetOptions{Context: ctx, Timeout: s.kvTimeout})
	if err != nil {
		return store.StoredSettings{}, wrap(err)
	}
	var doc store.StoredSettings
	if err := res.Content(&doc); err != nil {
		return store.StoredSettings{}, err
	}
	return doc, nil
}

// PutSettings replaces the global settings document. It is written at the
// configured durability like every other admin write: a settings change every
// replica is about to converge on must survive the node that accepted it.
func (s *Store) PutSettings(ctx context.Context, settings store.StoredSettings) error {
	_, err := s.meta.Upsert(keySettings, settings, &gocb.UpsertOptions{
		Context: ctx, Timeout: s.kvTimeout, DurabilityLevel: s.durability,
	})
	return wrap(err)
}

// wrap translates SDK errors into the store package's sentinel errors, so
// callers never have to import gocb to tell "missing" from "broken".
func wrap(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gocb.ErrDocumentNotFound):
		return store.ErrNotFound
	// A lost compare-and-swap is a sentinel rather than a message, so a caller
	// deciding whether to retry never has to read driver text. gocb reports the
	// two shapes separately: a stale CAS on a replace, and a document already
	// there on an insert.
	case errors.Is(err, gocb.ErrCasMismatch),
		errors.Is(err, gocb.ErrDocumentExists):
		return fmt.Errorf("%w: %w", store.ErrCASConflict, err)
	case errors.Is(err, gocb.ErrTimeout),
		errors.Is(err, gocb.ErrUnambiguousTimeout),
		errors.Is(err, gocb.ErrAmbiguousTimeout),
		errors.Is(err, gocb.ErrServiceNotAvailable),
		errors.Is(err, gocb.ErrTemporaryFailure):
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	default:
		return err
	}
}

// Interface check.
var _ store.StubStore = (*Store)(nil)
