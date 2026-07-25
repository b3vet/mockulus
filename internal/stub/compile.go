// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"encoding/json"
)

// URL matching kinds. The kind is resolved once at compile time so the request
// path never inspects which URL field a mapping used (SPEC §6.1).
const (
	// URLAny matches every URL, as WireMock's anyUrl() does.
	URLAny uint8 = iota
	// URLExactFull matches path and query byte-exactly, as received.
	URLExactFull
	// URLExactPath matches the path only, exactly.
	URLExactPath
)

// CompiledStub is the immutable, serve-ready form of a mapping. Everything
// expensive — regexes, JSONPath expressions, templates, response bodies — is
// resolved here at registration or snapshot-build time, never at serve time
// (SPEC §16.3 rule 2).
type CompiledStub struct {
	// Raw is the JSON exactly as registered, returned verbatim by GET.
	Raw json.RawMessage

	ID       string
	Name     string
	Priority int32
	// Seq is the cluster-global insertion sequence backing newest-wins
	// precedence (SPEC §5.3).
	Seq uint64

	// Method is the HTTP method to match; empty means ANY.
	Method string
	// URLKind selects how URLLiteral is compared.
	URLKind uint8
	// URLLiteral is the exact URL or path this stub matches.
	URLLiteral string

	Response CompiledResponse
}

// CompiledResponse is the pre-assembled response of a stub. Serving a static
// stub is a status write, the header writes, and one body write.
type CompiledResponse struct {
	Status  int
	Headers []Header
	Body    []byte
}

// Compile turns a validated mapping into its serve-ready form. seq is the
// insertion sequence drawn for the stub, which fixes its precedence relative to
// every other stub in the cluster.
func Compile(m *Mapping, seq uint64) *CompiledStub {
	cs := &CompiledStub{
		Raw:      m.Raw,
		ID:       m.ID,
		Name:     m.Name,
		Priority: m.Priority,
		Seq:      seq,
		Method:   m.Request.Method,
		Response: CompiledResponse{
			Status:  m.Response.Status,
			Headers: m.Response.Headers,
			Body:    m.Response.Body,
		},
	}

	switch {
	case m.Request.URL != "":
		cs.URLKind = URLExactFull
		cs.URLLiteral = m.Request.URL
	case m.Request.URLPath != "":
		cs.URLKind = URLExactPath
		cs.URLLiteral = m.Request.URLPath
	default:
		cs.URLKind = URLAny
	}
	return cs
}

// MatchesMethod reports whether the stub accepts the given method. An empty
// stub method is WireMock's ANY.
func (cs *CompiledStub) MatchesMethod(method string) bool {
	return cs.Method == "" || cs.Method == method
}
