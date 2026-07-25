// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/b3vet/mockulus/internal/store"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// The files API backs `bodyFileName`: a response body can be uploaded once and
// referenced by many stubs (SPEC §5.1, §7.3). Bodies are inlined into the
// snapshot at build time, so a file write bumps the epoch like any other
// mutation and the next rebuild picks it up.

// listFiles returns every stored file name.
func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	names, err := h.store.ListFiles(r.Context())
	if err != nil {
		h.storeError(w, "list_files", err)
		return
	}
	if names == nil {
		names = []string{}
	}
	wmcompat.WriteJSON(w, http.StatusOK, names)
}

// getFile returns a file's bytes.
func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	name, ok := h.fileName(w, r)
	if !ok {
		return
	}
	file, err := h.store.GetFile(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		wmcompat.WriteErrors(w, http.StatusNotFound,
			wmcompat.NewError(wmcompat.CodeMalformed, "no file named "+name))
		return
	}
	if err != nil {
		h.storeError(w, "get_file", err)
		return
	}
	// Files hold arbitrary response bodies, so the bytes go back as-is rather
	// than being guessed at.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Data)
}

// putFile stores a file and bumps the epoch, so a stub already referencing it
// resolves on the next rebuild.
func (h *Handler) putFile(w http.ResponseWriter, r *http.Request) {
	name, ok := h.fileName(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxAdminBody+1))
	if err != nil {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed, "could not read the file body"))
		return
	}
	if len(data) > maxAdminBody {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeBodyTooLarge, "file is too large"))
		return
	}

	ctx := r.Context()
	if err := h.store.PutFile(ctx, store.StoredFile{Name: name, Data: data}); err != nil {
		h.storeError(w, "put_file", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "stored file")

	h.log.Info("file stored", "name", name, "bytes", len(data))
	w.WriteHeader(http.StatusCreated)
}

// deleteFile removes a file. Stubs referencing it start serving the
// body-file-missing error on the next rebuild rather than failing to load.
func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	name, ok := h.fileName(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := h.store.DeleteFile(ctx, name); err != nil {
		h.storeError(w, "delete_file", err)
		return
	}
	if _, err := h.store.BumpEpoch(ctx); err != nil {
		h.storeError(w, "bump_epoch", err)
		return
	}
	h.rebuild(r, "deleted file")

	h.log.Info("file deleted", "name", name)
	w.WriteHeader(http.StatusOK)
}

// fileName extracts and validates the name from the path.
//
// Names are used as store keys, and a name containing traversal segments would
// be a path-traversal vector the moment a driver maps them onto a filesystem.
// Rejecting them here means no driver has to remember to.
func (h *Handler) fileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if name == "" {
		// The route may carry a nested name, which ServeMux gives as a wildcard.
		name = strings.TrimPrefix(r.URL.Path, "/__admin/files/")
	}
	name = strings.TrimPrefix(name, "/")

	if name == "" {
		wmcompat.WriteErrors(w, http.StatusNotFound,
			wmcompat.NewError(wmcompat.CodeMalformed, "a file name is required"))
		return "", false
	}
	if cleaned := path.Clean(name); cleaned != name || strings.HasPrefix(cleaned, "..") || path.IsAbs(cleaned) {
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed,
			"file name "+name+" is not allowed: names must be relative and free of . and .. segments"))
		return "", false
	}
	return name, true
}
