// SPDX-License-Identifier: Apache-2.0

// Package response assembles what goes back on the wire: status, headers and
// body, plus the delay, dribble and fault behaviors a stub can ask for.
//
// Everything expensive was resolved when the stub was compiled, so rendering a
// static stub is a header write and a single body write (SPEC §12.3).
package response

import (
	"net/http"

	"github.com/b3vet/mockulus/internal/stub"
)

// Write emits a compiled response.
func Write(w http.ResponseWriter, resp *stub.CompiledResponse) {
	h := w.Header()
	for _, hdr := range resp.Headers {
		h.Add(hdr.Name, hdr.Value)
	}
	w.WriteHeader(resp.Status)
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}
}
