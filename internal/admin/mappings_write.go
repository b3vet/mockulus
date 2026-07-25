// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Import duplicate policies (SPEC §5.1).
const (
	duplicateOverwrite = "OVERWRITE"
	duplicateIgnore    = "IGNORE"
)

// updateMapping replaces an existing stub in full.
//
// The insertion sequence is deliberately preserved: editing a stub must not
// change which stub a request matches. Drawing a fresh sequence would silently
// promote the edited stub above its equal-priority peers, turning an edit into
// a precedence change (SPEC §5.3, §7.3).
func (h *Handler) updateMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	existing, err := h.store.GetStub(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		h.mappingNotFound(w, id)
		return
	}
	if err != nil {
		h.storeError(w, "get_stub", err)
		return
	}

	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	compiled, errs := stub.Compile(raw, existing.Seq, h.stubOpts)
	if errs != nil {
		errs.WriteList(w)
		return
	}
	if compiled.ID != "" && compiled.ID != id {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeMalformed, "/id",
			"the id in the body does not match the id in the path"))
		return
	}

	doc, err := stub.WithIdentity(raw, id)
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, err.Error()))
		return
	}

	stored := store.StoredStub{
		ID:            id,
		SchemaVersion: store.SchemaVersion,
		Seq:           existing.Seq,
		Persistent:    compiled.Persistent,
		CreatedAt:     existing.CreatedAt,
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
	h.rebuild(r, "updated stub")

	h.log.Info("stub updated", "id", id, "seq", existing.Seq)
	wmcompat.WriteJSON(w, http.StatusOK, json.RawMessage(doc))
}

// resetMappings removes non-persistent stubs, leaving persistent ones in place.
func (h *Handler) resetMappings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.DeleteEphemeralStubs(ctx); err != nil {
		h.storeError(w, "delete_ephemeral_stubs", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "reset mappings")

	h.log.Info("non-persistent stubs reset")
	w.WriteHeader(http.StatusOK)
}

// saveMappings marks every current stub persistent.
//
// WireMock writes its in-memory stubs to files here. Mockulus has no per-node
// filesystem to write to, so the equivalent durable act is clearing the TTL
// that would otherwise expire them — documented deviation #4.
func (h *Handler) saveMappings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.MarkAllPersistent(ctx); err != nil {
		h.storeError(w, "mark_all_persistent", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "saved mappings")

	h.log.Info("all stubs marked persistent")
	w.WriteHeader(http.StatusOK)
}

// importRequest is the body of POST /__admin/mappings/import.
type importRequest struct {
	Mappings      []json.RawMessage `json:"mappings"`
	ImportOptions *struct {
		DuplicatePolicy      string `json:"duplicatePolicy"`
		DeleteAllNotInImport bool   `json:"deleteAllNotInImport"`
	} `json:"importOptions"`
}

// importMappings loads a batch of stubs in one call.
//
// The whole batch is validated before anything is written, so a malformed
// mapping halfway through cannot leave the store half-imported.
func (h *Handler) importMappings(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req importRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed,
			"import body must be an object with a mappings array: "+err.Error()))
		return
	}
	if req.Mappings == nil {
		wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeMalformed, "/mappings",
			"import needs a mappings array"))
		return
	}

	policy := duplicateOverwrite
	deleteAllNotInImport := false
	if req.ImportOptions != nil {
		if p := req.ImportOptions.DuplicatePolicy; p != "" {
			if p != duplicateOverwrite && p != duplicateIgnore {
				wmcompat.WriteError(w, wmcompat.NewFieldError(wmcompat.CodeMalformed,
					"/importOptions/duplicatePolicy",
					fmt.Sprintf("unknown duplicatePolicy %q, want %s or %s",
						p, duplicateOverwrite, duplicateIgnore)))
				return
			}
			policy = p
		}
		deleteAllNotInImport = req.ImportOptions.DeleteAllNotInImport
	}

	ctx := r.Context()

	// Validate everything first: a batch either imports or is rejected whole.
	type pending struct {
		id       string
		doc      []byte
		compiled *stub.CompiledStub
		existing *store.StoredStub
	}
	items := make([]pending, 0, len(req.Mappings))
	problems := &wmcompat.ErrorList{}

	for i, mapping := range req.Mappings {
		compiled, errs := stub.Compile(mapping, 0, h.stubOpts)
		if errs != nil {
			for _, e := range errs.Errors() {
				if e.Source != nil {
					e.Source.Pointer = fmt.Sprintf("/mappings/%d%s", i, e.Source.Pointer)
				}
				problems.Add(e)
			}
			continue
		}

		id := compiled.ID
		var existing *store.StoredStub
		if id != "" {
			if found, err := h.store.GetStub(ctx, id); err == nil {
				existing = &found
			} else if !errors.Is(err, store.ErrNotFound) {
				h.storeError(w, "get_stub", err)
				return
			}
		} else {
			id = uuid.NewString()
		}

		doc, err := stub.WithIdentity(mapping, id)
		if err != nil {
			problems.Addf(wmcompat.CodeMalformed, fmt.Sprintf("/mappings/%d", i), err.Error())
			continue
		}
		items = append(items, pending{id: id, doc: doc, compiled: compiled, existing: existing})
	}

	if !problems.Empty() {
		problems.WriteList(w)
		return
	}

	imported, ignored := 0, 0
	keep := make(map[string]bool, len(items))

	for _, item := range items {
		keep[item.id] = true

		if item.existing != nil && policy == duplicateIgnore {
			ignored++
			continue
		}

		// An overwrite preserves the stub's precedence, exactly as PUT does.
		seq := uint64(0)
		created := time.Now().UTC()
		if item.existing != nil {
			seq, created = item.existing.Seq, item.existing.CreatedAt
		} else {
			next, err := h.store.NextSeq(ctx)
			if err != nil {
				h.storeError(w, "next_seq", err)
				return
			}
			seq = next
		}

		if err := h.store.PutStub(ctx, store.StoredStub{
			ID:            item.id,
			SchemaVersion: store.SchemaVersion,
			Seq:           seq,
			Persistent:    item.compiled.Persistent,
			CreatedAt:     created,
			Mapping:       item.doc,
		}); err != nil {
			h.storeError(w, "put_stub", err)
			return
		}
		imported++
	}

	removed := 0
	if deleteAllNotInImport {
		n, err := h.deleteStubsNotIn(ctx, keep)
		if err != nil {
			h.storeError(w, "delete_stub", err)
			return
		}
		removed = n
	}

	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "imported mappings")

	h.log.Info("mappings imported",
		"imported", imported, "ignored", ignored, "removed", removed, "policy", policy)
	w.WriteHeader(http.StatusOK)
}

// deleteStubsNotIn removes every stub outside the given set, backing
// deleteAllNotInImport.
func (h *Handler) deleteStubsNotIn(ctx context.Context, keep map[string]bool) (int, error) {
	stubs, _, _, err := h.store.LoadAll(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, s := range stubs {
		if keep[s.ID] {
			continue
		}
		if err := h.store.DeleteStub(ctx, s.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// findByMetadata returns stubs whose metadata satisfies a content matcher. The
// matcher set is the same one request criteria use, so there is one definition
// of what "matches" means across the whole product.
func (h *Handler) findByMetadata(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.metadataMatch(w, r)
	if !ok {
		return
	}

	mappings := make([]json.RawMessage, 0, len(matched))
	for _, cs := range matched {
		mappings = append(mappings, json.RawMessage(cs.Raw))
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"mappings": mappings,
		"meta":     map[string]any{"total": len(mappings)},
	})
}

// removeByMetadata deletes stubs whose metadata satisfies a content matcher.
// This is the cleanup path a CI runner sharing a deployment uses, so it returns
// what it removed rather than only a count.
func (h *Handler) removeByMetadata(w http.ResponseWriter, r *http.Request) {
	matched, ok := h.metadataMatch(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	removed := make([]json.RawMessage, 0, len(matched))
	for _, cs := range matched {
		if err := h.store.DeleteStub(ctx, cs.ID); err != nil {
			h.storeError(w, "delete_stub", err)
			return
		}
		removed = append(removed, json.RawMessage(cs.Raw))
	}

	if len(removed) > 0 {
		if _, err := h.store.BumpEpoch(ctx); err != nil {
			h.storeError(w, "bump_epoch", err)
			return
		}
		h.rebuild(r, "removed stubs by metadata")
	}

	h.log.Info("stubs removed by metadata", "count", len(removed))
	wmcompat.WriteJSON(w, http.StatusOK, map[string]any{
		"mappings": removed,
		"meta":     map[string]any{"total": len(removed)},
	})
}

// metadataMatch compiles the request body as a content matcher and applies it
// to every stub's metadata.
func (h *Handler) metadataMatch(w http.ResponseWriter, r *http.Request) ([]*stub.CompiledStub, bool) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return nil, false
	}

	matcher, problems := matchers.Compile(raw, "", matchers.Options{CompileRegex: h.stubOpts.CompileRegex})
	if len(problems) > 0 {
		errs := &wmcompat.ErrorList{}
		for _, p := range problems {
			if p.Deferred {
				errs.Unsupported(p.Pointer, p.Feature)
				continue
			}
			errs.Addf(wmcompat.CodeMalformed, p.Pointer, p.Detail)
		}
		errs.WriteList(w)
		return nil, false
	}

	var matched []*stub.CompiledStub
	for _, cs := range h.engine.Snapshot().Ordered {
		// A stub with no metadata has nothing for the matcher to match, and
		// must never be swept up by a cleanup call.
		if len(cs.Metadata) == 0 {
			continue
		}
		if matcher.Match(matchers.NewDocument(cs.Metadata)) {
			matched = append(matched, cs)
		}
	}
	return matched, true
}
