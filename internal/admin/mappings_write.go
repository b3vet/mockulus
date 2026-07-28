// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
	id, ok := h.mappingID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	existing, err := h.store.GetStub(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		h.mappingNotFound(w)
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
	// A body id that disagrees with the path is ignored rather than rejected:
	// the path names the stub being replaced, and WireMock resolves the conflict
	// the same way. WithIdentity stamps the path id into the stored document, so
	// the disagreement cannot survive into the store.
	_ = compiled.ID

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
	compiled.ID = id
	compiled.Seq = existing.Seq
	compiled.Raw = doc
	h.builder.SpliceStub(ctx, compiled)

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
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
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
	wmcompat.WriteJSON(w, http.StatusOK, struct{}{})
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
	}
	items := make([]pending, 0, len(req.Mappings))
	problems := &wmcompat.ErrorList{}
	// What the deployment held before the batch, looked up once per id: an id
	// may appear more than once in one payload, and every occurrence of it has
	// to get the same answer to "was this stub already here". A nil entry
	// records an id that was looked up and not found, so a repeated id costs one
	// store round trip whether or not it resolves.
	before := make(map[string]*store.StoredStub, len(req.Mappings))

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
		if id == "" {
			id = uuid.NewString()
		} else if _, probed := before[id]; !probed {
			stored, err := h.store.GetStub(ctx, id)
			switch {
			case err == nil:
				before[id] = &stored
			case errors.Is(err, store.ErrNotFound):
				before[id] = nil
			default:
				h.storeError(w, "get_stub", err)
				return
			}
		}

		doc, err := stub.WithIdentity(mapping, id)
		if err != nil {
			problems.Addf(wmcompat.CodeMalformed, fmt.Sprintf("/mappings/%d", i), err.Error())
			continue
		}
		items = append(items, pending{id: id, doc: doc, compiled: compiled})
	}

	if !problems.Empty() {
		problems.WriteList(w)
		return
	}

	imported, ignored := 0, 0
	keep := make(map[string]bool, len(items))
	// applied carries what this batch has already written, so an id repeated
	// inside one payload collides with its own earlier occurrence exactly as it
	// would with a stub that was already in the store.
	applied := make(map[string]store.StoredStub, len(items))

	// The batch is applied back to front, so that the FIRST element of the array
	// wins a tie against the last. Selection breaks equal priority on insertion
	// sequence (SPEC §5.3), which means the element written last is the one that
	// serves; walking the array in order therefore hands that decision to
	// whichever of two overlapping stubs a fixture file happens to list last.
	// Nothing states which end should win, and a mappings file where two stubs
	// can answer one request is the ordinary shape of one — so the end that wins
	// has to be the end WireMock's importer picks, or the same file moved
	// between the two servers quietly serves a different stub and reports
	// nothing at all.
	//
	// Duplicate ids are resolved against the batch as it is applied rather than
	// against a snapshot taken before it, and that pairing is not optional: an
	// element can be the reason a later one is a duplicate. Reversing the walk
	// while deciding duplicates up front would change what IGNORE does to a
	// payload that repeats an id — the occurrence that survives there is the one
	// with no predecessor to be ignored in favour of, which is the last, and
	// deciding up front would make every occurrence look new and leave the
	// first.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		keep[item.id] = true

		existing, found := applied[item.id]
		if !found {
			if prior := before[item.id]; prior != nil {
				existing, found = *prior, true
			}
		}

		if found && policy == duplicateIgnore {
			ignored++
			continue
		}

		// An overwrite preserves the stub's precedence, exactly as PUT does.
		var seq uint64
		created := time.Now().UTC()
		if found {
			seq, created = existing.Seq, existing.CreatedAt
		} else {
			next, err := h.store.NextSeq(ctx)
			if err != nil {
				h.storeError(w, "next_seq", err)
				return
			}
			seq = next
		}

		stored := store.StoredStub{
			ID:            item.id,
			SchemaVersion: store.SchemaVersion,
			Seq:           seq,
			Persistent:    item.compiled.Persistent,
			CreatedAt:     created,
			Mapping:       item.doc,
		}
		if err := h.store.PutStub(ctx, stored); err != nil {
			h.storeError(w, "put_stub", err)
			return
		}
		applied[item.id] = stored
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
	for _, s := range matched {
		mappings = append(mappings, s.Mapping)
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
	for _, s := range matched {
		if err := h.store.DeleteStub(ctx, s.ID); err != nil {
			h.storeError(w, "delete_stub", err)
			return
		}
		removed = append(removed, s.Mapping)
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
// to the metadata of every stub in the deployment.
//
// The candidates come from the store rather than this pod's snapshot. A cleanup
// call that only saw the stubs this replica happens to hold would report an
// empty removal for stubs another replica registered a moment earlier — a
// silent no-op on the one path teams rely on to leave a shared deployment
// clean. Reading every document costs a store round trip, which is affordable
// because this is an admin call and not the hot path (P1); it is the same trade
// getMapping and deleteStubsNotIn already make.
//
// It also means a store outage answers 503 rather than a local subset. The
// search exists to decide what to delete, so an answer that silently covers
// one pod is the wrong kind of available — and find and remove have to agree
// on the candidate set, or "found 3, removed 0" becomes the report.
func (h *Handler) metadataMatch(w http.ResponseWriter, r *http.Request) ([]store.StoredStub, bool) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return nil, false
	}

	matcher, problems := matchers.Compile(raw, "", matchers.Options{
		CompileRegex:    h.stubOpts.CompileRegex,
		CompileJSONPath: h.stubOpts.CompileJSONPath,
	})
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

	stubs, _, _, err := h.store.LoadAll(r.Context())
	if err != nil {
		h.storeError(w, "load_all", err)
		return nil, false
	}

	var matched []store.StoredStub
	for _, s := range stubs {
		// A stub with no metadata has nothing for the matcher to match, and
		// must never be swept up by a cleanup call (SPEC §5.5 deviation 20).
		// stub.Metadata is what decides that, so an explicit null reads as the
		// absence it spells.
		meta := stub.Metadata(s.Mapping)
		if len(meta) == 0 {
			continue
		}
		if matcher.Match(matchers.NewDocument(meta)) {
			matched = append(matched, s)
		}
	}
	// Registration order, so the report is stable whichever store answered:
	// LoadAll is only required to return the set, and the couchbase collection
	// scan returns it unordered.
	sort.Slice(matched, func(i, j int) bool { return matched[i].Seq < matched[j].Seq })
	return matched, true
}
