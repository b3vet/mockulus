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

	// Compilation happens before anything is persisted, so a stub that would
	// fail to compile can never enter the store (SPEC §4.3).
	compiled, errs := stub.Compile(raw, 0, h.stubOpts)
	if errs != nil {
		errs.WriteList(w)
		return
	}

	ctxCheck := r.Context()
	id := compiled.ID
	if id == "" {
		id = uuid.NewString()
	} else if _, err := h.store.GetStub(ctxCheck, id); err == nil {
		// Creating over an existing id is rejected rather than treated as an
		// update: an accidental collision would otherwise silently replace
		// another suite's stub, and PUT exists for the deliberate case.
		// No source pointer: WireMock's 109 carries a bare title and detail.
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeDuplicateStubID,
			"ID of the provided stub mapping '"+id+"' is already taken by another stub mapping"))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		h.storeError(w, "get_stub", err)
		return
	}

	doc, err := stub.WithIdentity(raw, id)
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, err.Error()))
		return
	}

	ctx := r.Context()
	seq, err := h.store.NextSeq(ctx)
	if err != nil {
		h.storeError(w, "next_seq", err)
		return
	}

	stored := store.StoredStub{
		ID:            id,
		SchemaVersion: store.SchemaVersion,
		Seq:           seq,
		Persistent:    compiled.Persistent,
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

	// Splice rather than reload: the stub is already compiled and its position
	// follows from priority and sequence, so this pod serves it before the
	// write returns without a store round trip (SPEC §4.3 step 5).
	//
	// Raw is repointed at the stored document, not the one submitted: the
	// server assigned the identity, and a GET must return what was stored
	// rather than what arrived.
	compiled.ID = id
	compiled.Seq = seq
	compiled.Raw = doc
	h.builder.SpliceStub(ctx, compiled)

	h.log.Info("stub registered", "id", id, "name", compiled.Name, "seq", seq)
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
		h.mappingNotFound(w)
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
			h.mappingNotFound(w)
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
	h.builder.SpliceDelete(id)

	h.log.Info("stub deleted", "id", id)
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
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
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
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

// mappingNotFound answers an unknown stub id the way WireMock does: a bare 404,
// no body and no Content-Type (SPEC §5.1, Appendix C). The error envelope is
// deliberately not used here — this is the one not-found a WireMock client
// library already handles, and giving it a JSON body where it expects none
// makes mockulus the odd server out for no diagnostic gain: the id it asked for
// is the whole story.
func (h *Handler) mappingNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
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
