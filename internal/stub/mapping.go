// SPDX-License-Identifier: Apache-2.0

// Package stub owns the stub-mapping JSON model, the validation that turns an
// unsupported field into the 422 of SPEC Appendix B, and the compilation of a
// validated mapping into the immutable form the match engine serves from.
//
// Validation is exhaustive by design: a document is walked field by field
// against the support matrix of SPEC §5.2, every problem is collected, and a
// mapping either registers and then behaves as WireMock would or is rejected
// naming every offending JSON pointer (P3).
package stub

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/b3vet/mockulus/internal/wmcompat"
)

// DefaultPriority is the effective priority of a mapping that specifies none.
// WireMock treats absent priority as 5 (SPEC §5.2 [DH]).
const DefaultPriority = 5

// Mapping is the decoded, validated form of a stub-mapping document. The raw
// JSON is retained so a GET returns exactly what was registered.
type Mapping struct {
	Raw json.RawMessage

	ID         string
	Name       string
	Priority   int32
	Persistent bool
	Metadata   json.RawMessage

	Request  RequestPattern
	Response ResponseDefinition
}

// RequestPattern is the criteria half of a mapping (SPEC §5.2).
type RequestPattern struct {
	// Method is the HTTP method to match; empty means ANY.
	Method string
	// URL matches path and query byte-exactly, as received.
	URL string
	// URLPath matches the path only, exactly.
	URLPath string
}

// ResponseDefinition is the response half of a mapping (SPEC §5.2).
type ResponseDefinition struct {
	Status  int
	Headers []Header
	// Body is the resolved response body in wire form.
	Body []byte
}

// Header is one response header, kept as an ordered pair so repeated names and
// their order survive a round trip.
type Header struct {
	Name  string
	Value string
}

// supportedTopLevel lists the mapping fields this build accepts. Anything else
// is either deferred to the roadmap or not part of WireMock's format, and both
// cases are reported as a 422 rather than silently ignored.
var supportedTopLevel = map[string]bool{
	"id": true, "uuid": true, "name": true, "priority": true,
	"persistent": true, "metadata": true, "request": true, "response": true,
}

var supportedRequestFields = map[string]bool{
	"method": true, "url": true, "urlPath": true,
}

var supportedResponseFields = map[string]bool{
	"status": true, "body": true, "jsonBody": true, "headers": true,
}

// deferredFeatures maps a field name to the roadmap feature it belongs to, so
// the 422 detail tells a team what they are waiting for rather than just "no".
var deferredFeatures = map[string]string{
	"postServeActions":              "postServeActions (webhooks)",
	"multipartPatterns":             "multipartPatterns",
	"customMatcher":                 "customMatcher",
	"proxyBaseUrl":                  "proxyBaseUrl (proxy mode)",
	"additionalProxyRequestHeaders": "additionalProxyRequestHeaders (proxy mode)",
	"proxyUrlPrefixToRemove":        "proxyUrlPrefixToRemove (proxy mode)",
	"fromConfiguredStub":            "fromConfiguredStub (proxy mode)",
}

// Parse decodes and validates one stub-mapping document. It returns either a
// mapping or every problem found, never both.
func Parse(raw []byte) (*Mapping, *wmcompat.ErrorList) {
	errs := &wmcompat.ErrorList{}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "", "stub mapping is not a JSON object: "+err.Error())
		return nil, errs
	}

	m := &Mapping{
		Raw:      append(json.RawMessage(nil), raw...),
		Priority: DefaultPriority,
	}

	for field := range doc {
		if supportedTopLevel[field] {
			continue
		}
		reportUnsupported(errs, "/"+field, field)
	}

	// `id` and `uuid` are aliases; either may carry the identifier.
	for _, field := range []string{"id", "uuid"} {
		if v, ok := doc[field]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				errs.Addf(wmcompat.CodeMalformed, "/"+field, field+" must be a string")
				continue
			}
			if m.ID != "" && s != "" && m.ID != s {
				errs.Addf(wmcompat.CodeMalformed, "/"+field, "id and uuid must not disagree")
				continue
			}
			if s != "" {
				m.ID = s
			}
		}
	}

	decodeInto(errs, doc, "name", "/name", &m.Name)
	if v, ok := doc["priority"]; ok {
		var p int32
		if err := json.Unmarshal(v, &p); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/priority", "priority must be an integer")
		} else {
			m.Priority = p
		}
	}
	decodeInto(errs, doc, "persistent", "/persistent", &m.Persistent)
	if v, ok := doc["metadata"]; ok {
		m.Metadata = v
	}

	parseRequest(errs, doc["request"], &m.Request)
	parseResponse(errs, doc["response"], &m.Response)

	if !errs.Empty() {
		return nil, errs
	}
	return m, nil
}

func reportUnsupported(errs *wmcompat.ErrorList, pointer, field string) {
	if feature, deferred := deferredFeatures[field]; deferred {
		errs.Unsupported(pointer, feature)
		return
	}
	errs.Unsupported(pointer, field)
}

func decodeInto[T any](errs *wmcompat.ErrorList, doc map[string]json.RawMessage, field, pointer string, dst *T) {
	v, ok := doc[field]
	if !ok {
		return
	}
	if err := json.Unmarshal(v, dst); err != nil {
		errs.Addf(wmcompat.CodeMalformed, pointer, fmt.Sprintf("%s: %v", field, err))
	}
}

func parseRequest(errs *wmcompat.ErrorList, raw json.RawMessage, out *RequestPattern) {
	if len(raw) == 0 {
		// An absent request object matches everything, as WireMock's anyUrl() does.
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/request", "request must be a JSON object")
		return
	}
	for field := range doc {
		if !supportedRequestFields[field] {
			reportUnsupported(errs, "/request/"+field, field)
		}
	}

	decodeInto(errs, doc, "method", "/request/method", &out.Method)
	decodeInto(errs, doc, "url", "/request/url", &out.URL)
	decodeInto(errs, doc, "urlPath", "/request/urlPath", &out.URLPath)

	if out.URL != "" && out.URLPath != "" {
		errs.Addf(wmcompat.CodeMalformed, "/request",
			"url and urlPath are mutually exclusive")
	}
	if out.Method != "" {
		out.Method = strings.ToUpper(out.Method)
		if out.Method == "ANY" {
			out.Method = ""
		}
	}
}

func parseResponse(errs *wmcompat.ErrorList, raw json.RawMessage, out *ResponseDefinition) {
	out.Status = 200
	if len(raw) == 0 {
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response", "response must be a JSON object")
		return
	}
	for field := range doc {
		if !supportedResponseFields[field] {
			reportUnsupported(errs, "/response/"+field, field)
		}
	}

	if v, ok := doc["status"]; ok {
		if err := json.Unmarshal(v, &out.Status); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/status", "status must be an integer")
		} else if out.Status < 100 || out.Status > 599 {
			errs.Addf(wmcompat.CodeMalformed, "/response/status",
				fmt.Sprintf("status %d is outside the valid HTTP range", out.Status))
		}
	}

	bodyForms := 0
	if v, ok := doc["body"]; ok {
		bodyForms++
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/body", "body must be a string")
		} else {
			out.Body = []byte(s)
		}
	}
	if v, ok := doc["jsonBody"]; ok {
		bodyForms++
		out.Body = append([]byte(nil), v...)
	}
	if bodyForms > 1 {
		errs.Addf(wmcompat.CodeMalformed, "/response",
			"exactly one of body, jsonBody, base64Body or bodyFileName may be set")
	}

	if v, ok := doc["headers"]; ok {
		parseHeaders(errs, v, out)
	}
}

func parseHeaders(errs *wmcompat.ErrorList, raw json.RawMessage, out *ResponseDefinition) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response/headers", "headers must be a JSON object")
		return
	}
	for name, value := range doc {
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/headers/"+name,
				"response header values must be strings")
			continue
		}
		out.Headers = append(out.Headers, Header{Name: name, Value: s})
	}
}

// WithIdentity returns the mapping document with its `id` and `uuid` fields set
// to the given identifier, which is what a GET must return after the server
// assigns one. The rest of the document is preserved as registered.
func WithIdentity(raw []byte, id string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("stub mapping is not a JSON object: %w", err)
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	doc["id"] = encoded
	doc["uuid"] = encoded
	return json.Marshal(doc)
}
