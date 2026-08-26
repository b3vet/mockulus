// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
)

// multipartSource is the capability a body offers when it can be read as parts,
// in the same shape as the JSON and XML capabilities beside it: the work is done
// once, on demand, and a stub that never asks is never charged for it (P2).
type multipartSource interface {
	Parts() ([]*BodyPart, bool)
}

// BodyPart is one part of a multipart body, presented to matchers as a small
// request of its own: it has headers and it has a body, and both take the
// ordinary matcher vocabulary. A part's body is a full Body subject rather than
// a reduced one, so `matchesJsonPath` inside a part works for the same reason it
// works outside — verified against the oracle (PROBE_MULTIPART.md P7).
type BodyPart struct {
	name    string
	headers textproto.MIMEHeader
	body    Body

	// headerScratch is repointed per lookup and handed out by pointer, the way
	// ParsedRequest hands out its own: a matcher reads its subject during the
	// call and never retains it.
	headerScratch KeyValues
}

// Name is the part's `name` parameter as parsed from Content-Disposition, which
// is empty when the part declares none.
func (p *BodyPart) Name() string { return p.name }

// HeaderSubject returns the subject for one of the part's own headers.
func (p *BodyPart) HeaderSubject(name string) Subject {
	values := p.headers.Values(textproto.CanonicalMIMEHeaderKey(name))
	p.headerScratch.Set(values != nil, values)
	return &p.headerScratch
}

// BodySubject returns the part's body.
func (p *BodyPart) BodySubject() Subject { return &p.body }

// PartCriterion is a header criterion inside one multipart pattern.
type PartCriterion struct {
	Name    string
	Matcher Matcher
}

// MultipartPattern is one element of `multipartPatterns`.
type MultipartPattern struct {
	// Name, when set, requires the part's Content-Disposition `name` parameter
	// to equal it. On WireMock this field is inert — a pattern naming a part
	// that does not exist still matches, and it still matches when it directly
	// contradicts the header criterion beside it. Honouring it is deviation
	// #63, and it is compared against the *parsed* parameter rather than as a
	// substring of the raw header, so it matches whichever of the two legal
	// spellings the client sent.
	Name string

	// Headers are criteria against the part's own headers.
	Headers []PartCriterion

	// BodyMatchers must all match the part's body, ordered cheapest-first by
	// the caller.
	BodyMatchers []Matcher

	// All selects `matchingType: ALL` — every part must satisfy this element
	// — rather than the default `ANY`.
	All bool
}

// MultipartPatterns is the whole `multipartPatterns` criterion.
//
// The two quantifiers are easy to conflate and the oracle separates them: the
// elements of the array are ANDed, while `matchingType` quantifies over the
// *parts* within a single element. An array of two elements each saying `ANY`
// therefore requires two satisfied parts, not one.
type MultipartPatterns struct {
	Patterns []MultipartPattern
}

// Match implements Matcher.
//
// A body that is not multipart is a non-match rather than an error, and so is a
// multipart body carrying no parts at all — including against an element that
// specifies no criteria whatever. That last case is what shows the requirement
// belongs to the request rather than to the element: `[{}]` matches a body with
// one part and refuses a body with none.
func (m *MultipartPatterns) Match(s Subject) bool {
	source, ok := s.(multipartSource)
	if !ok {
		return false
	}
	parts, ok := source.Parts()
	if !ok || len(parts) == 0 {
		return false
	}
	for i := range m.Patterns {
		if !m.Patterns[i].matches(parts) {
			return false
		}
	}
	return true
}

func (p *MultipartPattern) matches(parts []*BodyPart) bool {
	if p.All {
		for _, part := range parts {
			if !p.satisfiedBy(part) {
				return false
			}
		}
		return true
	}
	for _, part := range parts {
		if p.satisfiedBy(part) {
			return true
		}
	}
	return false
}

func (p *MultipartPattern) satisfiedBy(part *BodyPart) bool {
	if p.Name != "" && p.Name != part.Name() {
		return false
	}
	for _, c := range p.Headers {
		if !c.Matcher.Match(part.HeaderSubject(c.Name)) {
			return false
		}
	}
	for _, m := range p.BodyMatchers {
		if !m.Match(part.BodySubject()) {
			return false
		}
	}
	return true
}

// Describe implements Matcher. It names the criterion rather than reproducing
// it, because a multipart pattern nests two more vocabularies inside itself and
// a near-miss line has to stay one line.
func (m *MultipartPatterns) Describe() string {
	var sb strings.Builder
	sb.WriteString("multipartPatterns [")
	for i := range m.Patterns {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(m.Patterns[i].describe())
	}
	sb.WriteString("]")
	return sb.String()
}

func (p *MultipartPattern) describe() string {
	var parts []string
	if p.All {
		parts = append(parts, "ALL")
	} else {
		parts = append(parts, "ANY")
	}
	if p.Name != "" {
		parts = append(parts, "name "+quote(p.Name))
	}
	if len(p.Headers) > 0 {
		parts = append(parts, strconv.Itoa(len(p.Headers))+" header criteria")
	}
	if len(p.BodyMatchers) > 0 {
		parts = append(parts, strconv.Itoa(len(p.BodyMatchers))+" body patterns")
	}
	return strings.Join(parts, " ")
}

// ErrNotMultipart reports a body that cannot be read as parts. It exists so the
// template-free callers of ParseMultipart can tell "not multipart" from "badly
// formed multipart" if they ever need to; matching treats both as a non-match,
// which is the oracle's answer to a text/plain body.
var ErrNotMultipart = errors.New("the body is not a multipart document")

// ParseMultipart splits a body into parts using the boundary declared in its
// Content-Type.
//
// NextRawPart is deliberate: it hands back the bytes as they arrived rather than
// decoding a quoted-printable transfer encoding, so a matcher compares what the
// client actually sent.
func ParseMultipart(raw []byte, contentType string) ([]*BodyPart, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, ErrNotMultipart
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, ErrNotMultipart
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, ErrNotMultipart
	}

	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	var out []*BodyPart
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, ErrNotMultipart
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, ErrNotMultipart
		}
		bp := &BodyPart{headers: part.Header, name: dispositionName(part.Header)}
		bp.body.SetWithContentType(data, part.Header.Get("Content-Type"))
		out = append(out, bp)
	}
}

// dispositionName reads the `name` parameter out of Content-Disposition.
//
// It parses rather than searching for a substring because both `name="meta"`
// and `name=meta` are legal on the wire and a client picks either. WireMock's
// header criteria see the raw text and so distinguish them; this does not, and
// that difference is the whole reason `name` is worth honouring at all.
func dispositionName(header textproto.MIMEHeader) string {
	disposition := header.Get("Content-Disposition")
	if disposition == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}
	return params["name"]
}
