// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/config"
	"github.com/b3vet/mockulus/internal/match"
	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/store/memory"
	"github.com/b3vet/mockulus/internal/stub"
)

// testHandler is an admin API over a memory store, plus the two collaborators a
// case needs to see the effect of a write: the engine that serves the mock port
// and the store that holds what was written. Both are reachable because the
// interesting claims about an admin write are about the stub that answers
// afterwards and about the precedence it was given, and neither is visible in
// the admin response.
type testHandler struct {
	*Handler
	engine *match.Engine
	store  store.StubStore
}

func newTestHandler(t *testing.T) *testHandler {
	t.Helper()

	cfg := config.Default()
	m := metrics.New("test", "test", false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := memory.New(0)
	engine := match.NewEngine(cfg, m, log, nil)
	builder := match.NewBuilder(st, engine, log, m, stub.Options{})

	h := New(Options{
		Config:  cfg,
		Logger:  log,
		Metrics: m,
		Version: "test",
		Store:   st,
		Engine:  engine,
		Builder: builder,
	})
	return &testHandler{Handler: h, engine: engine, store: st}
}

// admin runs one admin request and returns the recorded response.
func (h *testHandler) admin(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// createStub registers a mapping and fails the test if it was not accepted, so
// a case's setup cannot silently become the thing it asserts.
func (h *testHandler) createStub(t *testing.T, doc string) {
	t.Helper()
	rec := h.admin(t, http.MethodPost, "/__admin/mappings", doc)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registering the stub answered %d: %s", rec.Code, rec.Body.String())
	}
}

// importMappings posts one import payload and fails the test unless it was
// applied whole.
func (h *testHandler) importMappings(t *testing.T, payload string) {
	t.Helper()
	rec := h.admin(t, http.MethodPost, "/__admin/mappings/import", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("import answered %d: %s", rec.Code, rec.Body.String())
	}
}

// serve sends a GET to the mock port and returns the body, which is how a case
// asks which of several competing stubs actually won.
func (h *testHandler) serve(t *testing.T, path string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, req)
	return rec.Body.String()
}

// storedStub returns what the store holds for an id.
func (h *testHandler) storedStub(t *testing.T, id string) store.StoredStub {
	t.Helper()
	stored, err := h.store.GetStub(context.Background(), id)
	if err != nil {
		t.Fatalf("reading stub %s from the store: %v", id, err)
	}
	return stored
}

// countStubs reports how many stubs the deployment holds, which is what tells a
// duplicate that was overwritten apart from one that was added twice.
func (h *testHandler) countStubs(t *testing.T) int {
	t.Helper()
	stubs, _, _, err := h.store.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("loading the store: %v", err)
	}
	return len(stubs)
}

// assertJSONField checks one string member of a response document.
func assertJSONField(t *testing.T, body []byte, field, want string) {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, body)
	}
	var got string
	if err := json.Unmarshal(doc[field], &got); err != nil {
		t.Fatalf("response has no string %q: %s", field, body)
	}
	if got != want {
		t.Fatalf("%s is %q, want %q", field, got, want)
	}
}
