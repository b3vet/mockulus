// SPDX-License-Identifier: Apache-2.0

// Package wmcompat holds the WireMock-compatible wire shapes mockulus speaks:
// the error envelope and catalog of SPEC Appendix B, and the JSON envelopes the
// admin API returns. Keeping them in one package means the compatibility
// surface is reviewable in one place.
package wmcompat

import (
	"encoding/json"
	"net/http"
)

// Catalog codes from SPEC Appendix B. Each constant pairs with the HTTP status
// returned by StatusFor.
const (
	// CodeMalformed marks malformed JSON or a schema violation.
	CodeMalformed = 10
	// CodeUnsupportedFeature marks a stub field mockulus v1 does not support.
	CodeUnsupportedFeature = 1000
	// CodeUnsupportedEndpoint marks an admin endpoint deferred to the roadmap.
	CodeUnsupportedEndpoint = 1001
	// CodeTemplate marks an unknown helper or a template that fails to parse.
	CodeTemplate = 1002
	// CodeRegex marks a regex that compiles on neither engine.
	CodeRegex = 1003
	// CodeUnknownTransformer marks a `transformers` entry other than response-template.
	CodeUnknownTransformer = 1004
	// CodeUnknownSetting marks an unrecognised key in a settings write.
	CodeUnknownSetting = 1005
	// CodeInvalidSchema marks a JSON Schema that does not compile.
	//
	// Its own code rather than the malformed-request one, because the document
	// parses perfectly well as JSON and is only unusable as a schema — the same
	// distinction CodeRegex draws for a pattern that does not compile.
	CodeInvalidSchema = 1006
	// CodeJournalDisabled marks a journal-dependent call while the journal is off.
	CodeJournalDisabled = 1010
	// CodeStoreUnavailable marks an admin write attempted during a store outage.
	CodeStoreUnavailable = 1020
	// CodeScenarioUnavailable marks a scenario read that could not reach the store.
	CodeScenarioUnavailable = 1021
	// CodeBodyFileMissing marks a stub whose bodyFileName has no file.
	CodeBodyFileMissing = 1022
	// CodeBodyTooLarge marks a request body beyond max_body_bytes.
	CodeBodyTooLarge = 1030
	// CodeScenarioInvalid marks an unknown scenario or an invalid target state.
	CodeScenarioInvalid = 1031
	// CodeDuplicateStubID marks a create whose id already exists. The value is
	// WireMock's, so a client that special-cases it keeps working.
	CodeDuplicateStubID = 109
)

// roadmapURL is appended to the detail of every deferred-feature error so a
// team hitting one learns immediately where the feature stands (SPEC §1, P3).
const roadmapURL = "ROADMAP.md"

// catalog maps each code to its HTTP status and title.
var catalog = map[int]struct {
	status int
	title  string
}{
	CodeMalformed:           {http.StatusUnprocessableEntity, "Malformed request"},
	CodeUnsupportedFeature:  {http.StatusUnprocessableEntity, "Unsupported feature"},
	CodeUnsupportedEndpoint: {http.StatusNotFound, "Unsupported endpoint"},
	CodeTemplate:            {http.StatusUnprocessableEntity, "Template error"},
	CodeRegex:               {http.StatusUnprocessableEntity, "Invalid regular expression"},
	CodeUnknownTransformer:  {http.StatusUnprocessableEntity, "Unknown transformer"},
	CodeUnknownSetting:      {http.StatusUnprocessableEntity, "Unknown setting"},
	CodeInvalidSchema:       {http.StatusUnprocessableEntity, "Invalid JSON Schema"},
	CodeJournalDisabled:     {http.StatusInternalServerError, "Journal disabled"},
	CodeStoreUnavailable:    {http.StatusServiceUnavailable, "Store unavailable"},
	CodeScenarioUnavailable: {http.StatusInternalServerError, "Scenario state unavailable"},
	CodeBodyFileMissing:     {http.StatusInternalServerError, "Body file not found"},
	CodeBodyTooLarge:        {http.StatusRequestEntityTooLarge, "Request body too large"},
	CodeScenarioInvalid:     {http.StatusBadRequest, "Invalid scenario"},
	CodeDuplicateStubID:     {http.StatusUnprocessableEntity, "Duplicate stub mapping ID"},
}

// StatusFor returns the HTTP status a catalog code is reported with.
func StatusFor(code int) int {
	if e, ok := catalog[code]; ok {
		return e.status
	}
	return http.StatusInternalServerError
}

// TitleFor returns the human-readable title of a catalog code.
func TitleFor(code int) string {
	if e, ok := catalog[code]; ok {
		return e.title
	}
	return "Error"
}

// Source locates the offending element of the request document.
type Source struct {
	Pointer string `json:"pointer"`
}

// Error is one entry of the WireMock-compatible error envelope.
type Error struct {
	Code   int     `json:"code"`
	Source *Source `json:"source,omitempty"`
	Title  string  `json:"title"`
	Detail string  `json:"detail,omitempty"`
}

// ErrorBody is the envelope itself: `{"errors":[...]}`.
type ErrorBody struct {
	Errors []Error `json:"errors"`
}

// NewError builds a catalog error without a source pointer.
func NewError(code int, detail string) Error {
	return Error{Code: code, Title: TitleFor(code), Detail: detail}
}

// NewFieldError builds a catalog error pointing at a JSON element.
func NewFieldError(code int, pointer, detail string) Error {
	return Error{Code: code, Source: &Source{Pointer: pointer}, Title: TitleFor(code), Detail: detail}
}

// UnsupportedFeature builds the 422 raised when a stub uses a deferred feature,
// naming both the field and where its design lives.
func UnsupportedFeature(pointer, feature string) Error {
	return NewFieldError(CodeUnsupportedFeature, pointer,
		feature+" is not supported in mockulus v1 — see "+roadmapURL)
}

// UnsupportedEndpoint builds the 404 returned for a deferred admin endpoint.
func UnsupportedEndpoint(path string) Error {
	return NewError(CodeUnsupportedEndpoint,
		path+" is not supported in mockulus v1 — see "+roadmapURL)
}

// ErrorList accumulates problems so a single response can report all of them,
// which is what SPEC Appendix B requires of every 422: collect, don't fail fast.
type ErrorList struct {
	errs []Error
}

// Add appends an error to the list.
func (l *ErrorList) Add(e Error) { l.errs = append(l.errs, e) }

// Addf appends a catalog error with a source pointer.
func (l *ErrorList) Addf(code int, pointer, detail string) {
	l.Add(NewFieldError(code, pointer, detail))
}

// Unsupported appends the standard deferred-feature error for a field.
func (l *ErrorList) Unsupported(pointer, feature string) {
	l.Add(UnsupportedFeature(pointer, feature))
}

// Empty reports whether any problem was recorded.
func (l *ErrorList) Empty() bool { return len(l.errs) == 0 }

// Errors returns the accumulated problems.
func (l *ErrorList) Errors() []Error { return l.errs }

// Status returns the HTTP status the list should be reported with: the most
// severe status among its entries, with 422 winning ties.
func (l *ErrorList) Status() int {
	status := http.StatusUnprocessableEntity
	for i, e := range l.errs {
		if s := StatusFor(e.Code); i == 0 || s > status {
			status = s
		}
	}
	return status
}

// WriteErrors renders an error envelope with the given status.
func WriteErrors(w http.ResponseWriter, status int, errs ...Error) {
	writeJSON(w, status, ErrorBody{Errors: errs})
}

// WriteError renders a single catalog error using that code's documented status.
func WriteError(w http.ResponseWriter, e Error) {
	WriteErrors(w, StatusFor(e.Code), e)
}

// WriteList renders every accumulated problem in one response.
func (l *ErrorList) WriteList(w http.ResponseWriter) {
	WriteErrors(w, l.Status(), l.errs...)
}

// WriteJSON renders v as the JSON body of an admin response.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// Encoding our own response types cannot fail in practice; degrade to a
		// bare 500 rather than writing a half-serialised body.
		http.Error(w, `{"errors":[{"code":10,"title":"Internal error"}]}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
