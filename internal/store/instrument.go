// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"time"
)

// Store latency is the first thing to move when a deployment starts to hurt,
// and the last thing anyone can see.
//
// The request path serves from an immutable snapshot and never touches the
// store (SPEC §16.3), so a Couchbase that has become slow shows up in nothing a
// user measures until a write finally times out. By then the deployment has
// been degrading for a while. Timing every operation here is what turns that
// into a graph an operator can watch, and it is why §14.1 lists the histogram
// as an operational metric rather than a debugging one.
//
// A decorator rather than a call inside each driver: the drivers would each
// have to remember to time every method, and the one that gets forgotten is the
// one that breaks. Wrapping the interface makes it structural — a new method
// cannot be added without passing through here, because the compiler will not
// let the decorator satisfy the interface otherwise.
//
// Error counting moves here for the same reason. It was previously incremented
// at three call sites that happened to think of it, which counted a failing
// epoch poll and a failing scenario transition but not a failing file write.

// Recorder is the metrics surface this needs, kept narrow so the store package
// does not depend on the whole metrics registry.
type Recorder interface {
	// ObserveStoreOperation records one operation's duration and whether it
	// failed, labelled by operation name.
	ObserveStoreOperation(op string, d time.Duration, err error)
}

// Instrumented wraps a store so every operation is timed and every failure
// counted. A nil recorder returns the store unchanged, so a deployment with
// metrics off pays nothing (P2).
func Instrumented(st StubStore, rec Recorder) StubStore {
	if rec == nil {
		return st
	}
	return &instrumentedStore{inner: st, rec: rec}
}

type instrumentedStore struct {
	inner StubStore
	rec   Recorder
}

// observe times one call. ErrNotFound is deliberately not an error here: a GET
// for a stub that was never registered is an ordinary answer, and counting it
// would make the error rate track how often clients ask for absent things
// rather than how often the store is failing.
func (s *instrumentedStore) observe(op string, start time.Time, err error) error {
	if errors.Is(err, ErrNotFound) {
		s.rec.ObserveStoreOperation(op, time.Since(start), nil)
		return err
	}
	s.rec.ObserveStoreOperation(op, time.Since(start), err)
	return err
}

func (s *instrumentedStore) LoadAll(ctx context.Context) ([]StoredStub, []StoredFile, uint64, error) {
	start := time.Now()
	stubs, files, epoch, err := s.inner.LoadAll(ctx)
	return stubs, files, epoch, s.observe("load_all", start, err)
}

func (s *instrumentedStore) PutStub(ctx context.Context, stub StoredStub) error {
	start := time.Now()
	return s.observe("put_stub", start, s.inner.PutStub(ctx, stub))
}

func (s *instrumentedStore) GetStub(ctx context.Context, id string) (StoredStub, error) {
	start := time.Now()
	stub, err := s.inner.GetStub(ctx, id)
	return stub, s.observe("get_stub", start, err)
}

func (s *instrumentedStore) DeleteStub(ctx context.Context, id string) error {
	start := time.Now()
	return s.observe("delete_stub", start, s.inner.DeleteStub(ctx, id))
}

func (s *instrumentedStore) DeleteAllStubs(ctx context.Context) error {
	start := time.Now()
	return s.observe("delete_all_stubs", start, s.inner.DeleteAllStubs(ctx))
}

func (s *instrumentedStore) DeleteEphemeralStubs(ctx context.Context) error {
	start := time.Now()
	return s.observe("delete_ephemeral_stubs", start, s.inner.DeleteEphemeralStubs(ctx))
}

func (s *instrumentedStore) MarkAllPersistent(ctx context.Context) error {
	start := time.Now()
	return s.observe("mark_all_persistent", start, s.inner.MarkAllPersistent(ctx))
}

func (s *instrumentedStore) PutFile(ctx context.Context, file StoredFile) error {
	start := time.Now()
	return s.observe("put_file", start, s.inner.PutFile(ctx, file))
}

func (s *instrumentedStore) GetFile(ctx context.Context, name string) (StoredFile, error) {
	start := time.Now()
	file, err := s.inner.GetFile(ctx, name)
	return file, s.observe("get_file", start, err)
}

func (s *instrumentedStore) DeleteFile(ctx context.Context, name string) error {
	start := time.Now()
	return s.observe("delete_file", start, s.inner.DeleteFile(ctx, name))
}

func (s *instrumentedStore) ListFiles(ctx context.Context) ([]string, error) {
	start := time.Now()
	names, err := s.inner.ListFiles(ctx)
	return names, s.observe("list_files", start, err)
}

func (s *instrumentedStore) NextSeq(ctx context.Context) (uint64, error) {
	start := time.Now()
	seq, err := s.inner.NextSeq(ctx)
	return seq, s.observe("next_seq", start, err)
}

func (s *instrumentedStore) Epoch(ctx context.Context) (uint64, error) {
	start := time.Now()
	epoch, err := s.inner.Epoch(ctx)
	return epoch, s.observe("epoch", start, err)
}

func (s *instrumentedStore) BumpEpoch(ctx context.Context) (uint64, error) {
	start := time.Now()
	epoch, err := s.inner.BumpEpoch(ctx)
	return epoch, s.observe("bump_epoch", start, err)
}

func (s *instrumentedStore) GetSettings(ctx context.Context) (StoredSettings, error) {
	start := time.Now()
	settings, err := s.inner.GetSettings(ctx)
	return settings, s.observe("get_settings", start, err)
}

func (s *instrumentedStore) PutSettings(ctx context.Context, settings StoredSettings) error {
	start := time.Now()
	return s.observe("put_settings", start, s.inner.PutSettings(ctx, settings))
}

func (s *instrumentedStore) Close(ctx context.Context) error {
	return s.inner.Close(ctx)
}

// Scope note: a driver also implements optional interfaces — ChangeSignal for
// epoch polling, ScenarioStore, JournalStore — which a wrapper cannot carry,
// because Go has no way to implement an interface conditionally on what the
// wrapped value implements. So the caller keeps the driver itself for those
// assertions and passes the wrapped store to the components that take a
// StubStore. Turning epoch polling or scenarios off to gain a metric would be a
// bad trade, and this way the choice is visible at the call site rather than
// hidden in a failed type assertion.
