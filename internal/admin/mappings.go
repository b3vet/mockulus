// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/b3vet/mockulus/internal/metrics"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// maxAdminBody caps an admin request body. Stub documents are small; an
// unbounded read here would be a trivial memory-exhaustion vector.
const maxAdminBody = 32 << 20

// createMapping registers a stub. The write order matters: a document that
// fails validation or compilation is rejected before anything is persisted, so
// an invalid stub can never enter the store (SPEC §4.3).
func (h *Handler) createMapping(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	mapping, errs := stub.Parse(raw)
	if errs != nil {
		errs.WriteList(w)
		return
	}

	if mapping.ID == "" {
		mapping.ID = uuid.NewString()
	}
	doc, err := stub.WithIdentity(raw, mapping.ID)
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, err.Error()))
		return
	}
	mapping.Raw = doc

	ctx := r.Context()
	seq, err := h.store.NextSeq(ctx)
	if err != nil {
		h.storeError(w, "next_seq", err)
		return
	}

	stored := store.StoredStub{
		ID:            mapping.ID,
		SchemaVersion: store.SchemaVersion,
		Seq:           seq,
		Persistent:    mapping.Persistent,
		CreatedAt:     time.Now().UTC(),
		Mapping:       doc,
	}
	if err := h.store.PutStub(ctx, stored); err != nil {
		h.storeError(w, "put_stub", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "created stub")

	h.log.Info("stub registered", "id", mapping.ID, "name", mapping.Name, "seq", seq)
	wmcompat.WriteJSON(w, http.StatusCreated, json.RawMessage(doc))
}

// listMappings returns the WireMock list envelope, honouring limit and offset.
func (h *Handler) listMappings(w http.ResponseWriter, r *http.Request) {
	snap := h.engine.Snapshot()
	all := snap.Ordered

	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", len(all))

	page := all
	if offset > 0 {
		if offset >= len(page) {
			page = nil
		} else {
			page = page[offset:]
		}
	}
	if limit >= 0 && limit < len(page) {
		page = page[:limit]
	}

	mappings := make([]json.RawMessage, 0, len(page))
	for _, cs := range page {
		mappings = append(mappings, json.RawMessage(cs.Raw))
	}

	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"mappings": mappings,
		"meta":     map[string]any{"total": len(all)},
	})
}

// getMapping returns one stub by id.
func (h *Handler) getMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if cs, ok := h.engine.Snapshot().ByID(id); ok {
		wmcompat.WriteJSON(w, http.StatusOK, json.RawMessage(cs.Raw))
		return
	}

	// Fall back to the store: a stub registered on another replica may not be
	// in this pod's snapshot yet (SPEC §8).
	stored, err := h.store.GetStub(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		h.mappingNotFound(w, id)
		return
	}
	if err != nil {
		h.storeError(w, "get_stub", err)
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, json.RawMessage(stored.Mapping))
}

// deleteMapping removes one stub.
func (h *Handler) deleteMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if _, err := h.store.GetStub(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.mappingNotFound(w, id)
			return
		}
		h.storeError(w, "get_stub", err)
		return
	}
	if err := h.store.DeleteStub(ctx, id); err != nil {
		h.storeError(w, "delete_stub", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "deleted stub")

	h.log.Info("stub deleted", "id", id)
	w.WriteHeader(http.StatusOK)
}

// deleteAllMappings removes every stub, persistent or not (SPEC §5.1).
func (h *Handler) deleteAllMappings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.DeleteAllStubs(ctx); err != nil {
		h.storeError(w, "delete_all_stubs", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "deleted all stubs")

	h.log.Info("all stubs deleted")
	w.WriteHeader(http.StatusOK)
}

// readBody reads and size-caps an admin request body.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAdminBody+1))
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, "could not read request body"))
		return nil, false
	}
	if len(raw) > maxAdminBody {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeBodyTooLarge, "admin request body is too large"))
		return nil, false
	}
	if len(raw) == 0 {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, "request body is empty"))
		return nil, false
	}
	return raw, true
}

// rebuild refreshes this pod's snapshot after an admin write, so a single-pod
// test flow sees zero staleness (SPEC §4.3 step 5).
func (h *Handler) rebuild(r *http.Request, because string) {
	if err := h.builder.Rebuild(r.Context(), metrics.TriggerAdmin); err != nil {
		h.log.Error("snapshot rebuild after admin write failed",
			"reason", because, "error", err)
	}
}

// storeError reports a store failure as the 503 of SPEC §4.6.
func (h *Handler) storeError(w http.ResponseWriter, op string, err error) {
	h.metrics.StoreErrors.WithLabelValues(op).Inc()
	h.log.Error("store operation failed", "op", op, "error", err)
	wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeStoreUnavailable,
		"the stub store is unavailable; the admin write was not applied"))
}

func (h *Handler) mappingNotFound(w http.ResponseWriter, id string) {
	wmcompat.WriteErrors(w, http.StatusNotFound,
		wmcompat.NewError(wmcompat.CodeMalformed, "stub mapping "+id+" was not found"))
}

func queryInt(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
