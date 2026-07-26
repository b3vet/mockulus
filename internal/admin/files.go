// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

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

// maxFileNameBytes bounds a name so that every driver can address it. The
// Couchbase driver keys a file document on its name behind a short prefix, and
// Couchbase stops at 250-byte keys — an unbounded name is then a write the
// memory store takes and the Couchbase store rejects, which is one deployment
// answering 201 where another answers 500.
const maxFileNameBytes = 240

// fileName extracts and validates the name from the path.
//
// Names are used as store keys, and nothing in the tree joins one onto a
// filesystem path today — the file driver builds its map by walking a directory
// and never resolves a caller's name against it. That is what keeps this an
// input-validation rule rather than a live traversal, and it is exactly why the
// rule belongs here: the day a driver does map names onto paths, it inherits
// this instead of having to remember it.
func (h *Handler) fileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if name == "" {
		// The route may carry a nested name, which ServeMux gives as a wildcard.
		name = strings.TrimPrefix(r.URL.Path, "/__admin/files/")
	}

	if name == "" {
		wmcompat.WriteErrors(w, http.StatusNotFound,
			wmcompat.NewError(wmcompat.CodeMalformed, "a file name is required"))
		return "", false
	}
	if reason := rejectFileName(name); reason != "" {
		// Quoted, because half of what gets rejected here is invisible
		// otherwise: an operator reading the error should be able to see the
		// NUL or the newline that caused it. Truncated, because the name that
		// failed the length rule is by definition the one worth not echoing in
		// full — a path can be as long as the header cap allows.
		shown := name
		if len(shown) > 64 {
			shown = shown[:64] + "…"
		}
		wmcompat.WriteError(w, wmcompat.NewError(wmcompat.CodeMalformed,
			"file name "+strconv.Quote(shown)+" is not allowed: "+reason))
		return "", false
	}
	return name, true
}

// rejectFileName reports why a name cannot be stored, or "" when it can.
//
// Every rule here refuses a name outright rather than repairing it. An absolute
// name used to have its leading slash trimmed, which turned `/etc/passwd` into
// a stored `etc/passwd` and answered 201 — the caller's name and the stored
// name then disagree forever, and the request that looked like traversal was
// the one that got a success. Refusing is what P3 asks for, and it is the only
// answer that keeps a name meaning one thing.
func rejectFileName(name string) string {
	switch {
	case path.IsAbs(name):
		return "a name is relative to the files store, so it cannot begin with /"
	case name == ".." || strings.HasPrefix(name, "../"):
		return "a name cannot climb out of the files store with .. segments"
	case path.Clean(name) != name:
		return "a name must already be in cleaned form: no . or .. segments, no empty ones, no trailing /"
	case len(name) > maxFileNameBytes:
		return "a name is limited to " + strconv.Itoa(maxFileNameBytes) + " bytes"
	case !utf8.ValidString(name):
		// A listing is JSON, and JSON encoding replaces invalid bytes with
		// U+FFFD. Accepting one would mean GET /__admin/files reports a name
		// that GET /__admin/files/<that name> cannot fetch.
		return "a name must be valid UTF-8"
	case strings.ContainsFunc(name, isControl):
		// A name reaches the logs and the error catalog. Structured logging
		// escapes a newline today, but a name that can carry one is a name that
		// can forge a log line the first time anything renders it plainly.
		return "a name cannot contain control characters"
	}
	return ""
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
