// SPDX-License-Identifier: Apache-2.0

// Package journal records served requests so tests can verify what was called
// (SPEC §11).
//
// It is off by default, and that default is the whole point of the design.
// Always-on journaling at 50k RPS is 50k writes per second, which recreates
// exactly the collapse this project exists to avoid (D3). When it is on, the
// request path does the least possible: append to a bounded in-memory queue and
// return. Everything else — batching, writing, failing — happens on a flusher
// goroutine where it cannot slow a response down.
//
// The queue is bounded twice, by entry count and by bytes, because a thousand
// small entries and a thousand 64 KiB entries are very different amounts of
// memory. When either cap is reached the entry is dropped and counted. Dropping
// is the correct behavior, not a compromise: blocking the hot path to record
// what happened on the hot path is a self-inflicted outage.
package journal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
)

// Config carries the journal's tuning.
type Config struct {
	Enabled       bool
	MaxBody       int
	BufferEntries int
	BufferBytes   int64
	FlushWorkers  int
	BatchSize     int
	FlushInterval time.Duration
	// Pod names the replica that recorded an entry, so a cross-pod verification
	// result can be attributed.
	Pod string
}

// Writer batches journal entries to the store.
type Writer struct {
	cfg     Config
	store   store.JournalStore
	log     *slog.Logger
	metrics *metrics.Metrics

	queue chan *Entry

	// bytes tracks the queued payload size, which is the second cap.
	mu    sync.Mutex
	bytes int64

	wg   sync.WaitGroup
	stop chan struct{}
	once sync.Once
}

// NewWriter creates the writer. It does nothing until Start is called.
func NewWriter(cfg Config, st store.JournalStore, log *slog.Logger, m *metrics.Metrics) *Writer {
	return &Writer{
		cfg:     cfg,
		store:   st,
		log:     log,
		metrics: m,
		queue:   make(chan *Entry, cfg.BufferEntries),
		stop:    make(chan struct{}),
	}
}

// Start launches the flushers.
func (w *Writer) Start() {
	workers := w.cfg.FlushWorkers
	if workers < 1 {
		workers = 1
	}
	for range workers {
		w.wg.Add(1)
		go w.flushLoop()
	}
}

// Stop drains what is queued and waits for the flushers. Shutdown flushes the
// batch rather than discarding it, so a verification made just before SIGTERM
// still finds its entry (SPEC §4.5).
func (w *Writer) Stop() {
	w.once.Do(func() { close(w.stop) })
	w.wg.Wait()
}

// Record builds and enqueues an entry for a served request.
//
// It never blocks and never fails the request: a full queue drops the entry and
// increments the drop counter. Blocking the hot path to record what happened on
// the hot path is a self-inflicted outage.
func (w *Writer) Record(r *http.Request, body []byte, matched *stub.CompiledStub, status int) {
	entry := NewEntry(w.cfg, r, body, matched, status)
	if entry == nil {
		return
	}
	w.enqueue(entry)
}

// enqueue puts a built entry on the queue under both caps.
func (w *Writer) enqueue(e *Entry) {
	size := int64(len(e.Payload))

	w.mu.Lock()
	if w.cfg.BufferBytes > 0 && w.bytes+size > w.cfg.BufferBytes {
		w.mu.Unlock()
		w.metrics.JournalDropped.Inc()
		return
	}
	w.bytes += size
	w.mu.Unlock()

	select {
	case w.queue <- e:
		w.metrics.JournalEnqueued.Inc()
	default:
		// The count cap was reached first.
		w.mu.Lock()
		w.bytes -= size
		w.mu.Unlock()
		w.metrics.JournalDropped.Inc()
	}
}

func (w *Writer) flushLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]*Entry, 0, w.cfg.BatchSize)

	for {
		select {
		case <-w.stop:
			// Drain whatever is queued before leaving.
			for {
				select {
				case e := <-w.queue:
					batch = append(batch, e)
					if len(batch) >= w.cfg.BatchSize {
						w.flush(batch)
						batch = batch[:0]
					}
				default:
					w.flush(batch)
					return
				}
			}

		case e := <-w.queue:
			batch = append(batch, e)
			if len(batch) >= w.cfg.BatchSize {
				w.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (w *Writer) flush(batch []*Entry) {
	if len(batch) == 0 {
		return
	}
	start := time.Now()

	entries := make([]store.JournalEntry, 0, len(batch))
	var size int64
	for _, e := range batch {
		size += int64(len(e.Payload))
		entries = append(entries, store.JournalEntry{
			ID:   e.ID,
			TS:   e.TS,
			Pod:  e.Pod,
			Data: e.Payload,
		})
	}

	// The write gets its own budget: a slow store must not wedge the flusher,
	// because the queue behind it is what protects the request path.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := w.store.AppendJournal(ctx, entries); err != nil {
		w.metrics.JournalDropped.Add(float64(len(entries)))
		w.log.Warn("journal batch failed; entries dropped",
			"count", len(entries), "error", err)
	}

	w.mu.Lock()
	w.bytes -= size
	w.mu.Unlock()

	w.metrics.JournalFlushDuration.Observe(time.Since(start).Seconds())
}

// Entry is one queued serve event.
type Entry struct {
	ID      string
	TS      int64
	Pod     string
	Payload json.RawMessage
}

// NewEntry builds a serve event from a request and what was served.
//
// The shape mirrors WireMock's ServeEvent so a client library can read it
// (SPEC §11.2). Bodies are capped and the truncation is flagged rather than
// silent: a verification that matched a truncated body would be reporting
// something that did not happen.
func NewEntry(cfg Config, r *http.Request, body []byte, matched *stub.CompiledStub, status int) *Entry {
	now := time.Now().UTC()

	// One identifier, used both as the serve event's `id` and as the store key.
	// That is how a client uses it: it reads an id out of a listing and then
	// asks for or deletes that entry by it. Two independently minted ids would
	// make every such follow-up a 404 against an entry that is plainly there.
	id := ksuid.New().String()

	recorded, truncated := capBody(body, cfg.MaxBody)

	request := map[string]any{
		"method":           r.Method,
		"url":              r.URL.RequestURI(),
		"absoluteUrl":      absoluteURL(r),
		"clientIp":         clientIP(r),
		"headers":          headerMap(r.Header),
		"cookies":          cookieMap(r),
		"queryParams":      queryMap(r),
		"body":             string(recorded),
		"loggedDate":       now.UnixMilli(),
		"loggedDateString": now.Format(time.RFC3339),
	}
	if truncated {
		request["bodyTruncated"] = true
	}

	event := map[string]any{
		"id":         id,
		"request":    request,
		"wasMatched": matched != nil,
		"responseDefinition": map[string]any{
			"status": status,
		},
	}
	if matched != nil {
		event["stubMapping"] = map[string]any{
			"id":   matched.ID,
			"name": matched.Name,
		}
	}

	payload, err := json.Marshal(event)
	if err != nil {
		// Marshalling our own map cannot fail in practice; recording nothing is
		// better than recording a half-serialised entry.
		return nil
	}

	return &Entry{
		// A time-ordered key makes recency queries cheap without a secondary
		// sort (SPEC §11.2).
		ID:      id,
		TS:      now.UnixMilli(),
		Pod:     cfg.Pod,
		Payload: payload,
	}
}

// capBody truncates a recorded body to the configured cap.
func capBody(body []byte, max int) ([]byte, bool) {
	if max <= 0 || len(body) <= max {
		return body, false
	}
	return body[:max], true
}

func absoluteURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}

func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 && !strings.Contains(addr[i:], "]") {
		return addr[:i]
	}
	return addr
}

func headerMap(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for name, values := range h {
		if len(values) == 1 {
			out[name] = values[0]
			continue
		}
		out[name] = values
	}
	return out
}

func cookieMap(r *http.Request) map[string]any {
	out := map[string]any{}
	for _, c := range r.Cookies() {
		out[c.Name] = c.Value
	}
	return out
}

// queryMap renders the query the way WireMock's logged request does: every
// parameter is an object naming itself and listing every value it was given,
// single-valued ones included.
//
// Headers a line above collapse to a bare string when they have one value and
// this deliberately does not, because the two fields are typed differently on
// the other side. A client deserializes `queryParams` into a map of
// QueryParameter, which is constructed from `key` and `values`; a bare string
// there is not a shorter spelling of the same thing but a value the mapping
// cannot be built from, and the failure takes down the whole verification call
// rather than one field of it. Measured against pinned WireMock 3.13.2.
func queryMap(r *http.Request) map[string]any {
	out := map[string]any{}
	for name, values := range r.URL.Query() {
		out[name] = map[string]any{"key": name, "values": values}
	}
	return out
}
