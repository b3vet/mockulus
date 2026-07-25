// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// getSettings returns the deployment's global settings, in the envelope
// WireMock wraps them in.
//
// The answer comes from the store rather than from this pod's snapshot. There
// is one settings document for the whole deployment, and a replica that has not
// polled since another replica's write would otherwise report the value it is
// about to stop serving — a read that contradicts a write the caller already
// got a 200 for. This is an admin call, so the round trip is affordable; the
// serve path reads the snapshot instead (P1).
func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	doc, err := h.store.GetSettings(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		// Nothing was ever written, which is what zero-config looks like — an
		// empty settings object, not a 404 and not an invented default (P4).
		wmcompat.WriteJSON(w, http.StatusOK, map[string]any{"settings": struct{}{}})
		return
	}
	if err != nil {
		h.storeError(w, "get_settings", err)
		return
	}
	wmcompat.WriteJSON(w, http.StatusOK, map[string]json.RawMessage{"settings": doc.Settings})
}

// putSettings replaces the deployment's global settings.
//
// Replace, not merge, because that is what WireMock does and because a merge
// would leave no way to clear a key: posting `{}` is how a suite that set a
// global delay puts the deployment back the way it found it.
//
// The document is validated before anything is written, so a key mockulus does
// not implement is refused with a pointer at it rather than accepted and
// quietly ignored — a global delay that silently did not apply would look like
// a mock server that is merely fast (SPEC §5.1, Appendix B 1005, P3).
func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}
	if _, errs := stub.CompileSettings(raw); errs != nil {
		errs.WriteList(w)
		return
	}

	ctx := r.Context()
	// Stored verbatim, as a mapping is: a GET returns what was written rather
	// than this build's re-rendering of it.
	if err := h.store.PutSettings(ctx, store.StoredSettings{
		SchemaVersion: store.SchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Settings:      raw,
	}); err != nil {
		h.storeError(w, "put_settings", err)
		return
	}
	// The epoch is the whole of the cluster-wide part: other replicas poll the
	// counter, not this document, so a settings change that did not bump it
	// would apply on one pod and nowhere else (SPEC §8).
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	// A full rebuild rather than a splice: settings are not a stub, and the
	// reload is what compiles the new delay onto this pod's snapshot before the
	// write returns (SPEC §4.3 step 5).
	h.rebuild(r, "settings updated")

	h.log.Info("global settings updated")
	w.WriteHeader(http.StatusOK)
}
