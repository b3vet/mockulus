// SPDX-License-Identifier: Apache-2.0

package couchbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/couchbase/gocb/v2"

	"github.com/b3vet/mockulus/internal/store"
)

// The journal is the one write mockulus makes on behalf of a mock request, and
// it is off by default for exactly that reason: always-on journaling at 50k RPS
// is 50k writes/s, which recreates the collapse this project exists to avoid
// (SPEC D3). When it is on, writes are batched and asynchronous, and entries
// carry a TTL so a long-running deployment does not accumulate them forever.

// AppendJournal writes a batch of entries.
//
// Batching is what makes journaling survivable: one round trip per batch rather
// than per request. Entries are written concurrently within the batch, and a
// single failed write does not abandon the rest — a dropped entry is a counted
// loss, never a reason to stall the flusher.
func (s *Store) AppendJournal(ctx context.Context, entries []store.JournalEntry) error {
	if len(entries) == 0 {
		return nil
	}

	opts := &gocb.UpsertOptions{Context: ctx, Timeout: s.kvTimeout}
	if s.journalTTL > 0 {
		opts.Expiry = s.journalTTL
	}

	var firstErr error
	for _, entry := range entries {
		doc := journalDoc{
			ID:   entry.ID,
			TS:   entry.TS,
			Pod:  entry.Pod,
			Data: entry.Data,
		}
		if _, err := s.journal.Upsert(keyPrefixJournal+entry.ID, doc, opts); err != nil && firstErr == nil {
			firstErr = wrap(err)
		}
	}
	return firstErr
}

// journalDoc is the stored shape of a journal entry. The serve event itself is
// held verbatim so the query endpoints return what was recorded.
type journalDoc struct {
	ID   string          `json:"id"`
	TS   int64           `json:"ts"`
	Pod  string          `json:"pod"`
	Data json.RawMessage `json:"data"`
}

// QueryJournal returns entries newest-first within the given bounds.
//
// The time window is served by the GSI on `ts`; the scan limit is applied in
// the statement so a large journal cannot turn a verification call into a table
// scan (SPEC §11.3, deviation #16).
func (s *Store) QueryJournal(ctx context.Context, q store.JournalQuery) ([]store.JournalEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}

	stmt := fmt.Sprintf(
		"SELECT j.* FROM `%s`.`%s`.`%s` AS j WHERE j.ts >= $since ORDER BY j.ts DESC LIMIT $limit",
		s.bucketName, s.scopeName, collJournal)

	since := int64(0)
	if !q.Since.IsZero() {
		since = q.Since.UnixMilli()
	}

	rows, err := s.cluster.Query(stmt, &gocb.QueryOptions{
		Context: ctx,
		Timeout: s.queryTimeout,
		NamedParameters: map[string]any{
			"since": since,
			"limit": limit,
		},
	})
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]store.JournalEntry, 0, limit)
	for rows.Next() {
		var doc journalDoc
		if err := rows.Row(&doc); err != nil {
			return nil, err
		}
		out = append(out, store.JournalEntry{
			ID: doc.ID, TS: doc.TS, Pod: doc.Pod, Data: doc.Data,
		})
	}
	return out, rows.Err()
}

// GetJournalEntry returns one entry.
func (s *Store) GetJournalEntry(ctx context.Context, id string) (store.JournalEntry, error) {
	res, err := s.journal.Get(keyPrefixJournal+id, &gocb.GetOptions{
		Context: ctx, Timeout: s.kvTimeout,
	})
	if err != nil {
		return store.JournalEntry{}, wrap(err)
	}
	var doc journalDoc
	if err := res.Content(&doc); err != nil {
		return store.JournalEntry{}, err
	}
	return store.JournalEntry{ID: doc.ID, TS: doc.TS, Pod: doc.Pod, Data: doc.Data}, nil
}

// DeleteJournalEntry removes one entry.
func (s *Store) DeleteJournalEntry(ctx context.Context, id string) error {
	_, err := s.journal.Remove(keyPrefixJournal+id, &gocb.RemoveOptions{
		Context: ctx, Timeout: s.kvTimeout,
	})
	if isNotFound(err) {
		return nil
	}
	return wrap(err)
}

// ClearJournal removes every entry.
//
// By key rather than by statement, for the reason removeMappings gives: a
// `DELETE FROM` is planned as a scan of the persisted view, so an entry written
// moments earlier is invisible to it and survives a clear that answered 200.
// The journal is the collection where that is most likely — entries are written
// continuously by the request path — and a verification run against a journal
// that was told to be empty is exactly the test that then fails for no reason
// its author can see.
func (s *Store) ClearJournal(ctx context.Context) error {
	raw, err := s.loadCollection(ctx, s.journal, collJournal, s.requireFor(ctx))
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(raw))
	for id := range raw {
		keys = append(keys, id)
	}
	return s.removeKeys(ctx, s.journal, "journal entries", keys)
}

// decodeInto is a small helper so callers do not repeat the unmarshal dance.
func decodeInto(raw json.RawMessage, dst any) error { return json.Unmarshal(raw, dst) }

// isNotFound reports whether an error means the document was absent.
func isNotFound(err error) bool { return errors.Is(err, gocb.ErrDocumentNotFound) }

// Interface checks.
var (
	_ store.ScenarioStore = (*Store)(nil)
	_ store.JournalStore  = (*Store)(nil)
)
