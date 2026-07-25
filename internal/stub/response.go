// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/wmcompat"
)

var supportedResponseFields = map[string]bool{
	"status": true, "statusMessage": true, "headers": true,
	"body": true, "jsonBody": true, "base64Body": true, "bodyFileName": true,
	"fixedDelayMilliseconds": true, "delayDistribution": true,
	"chunkedDribbleDelay": true, "fault": true,
	"transformers": true, "transformerParameters": true,
}

// bodyFields are the four mutually exclusive ways to state a response body.
var bodyFields = []string{"body", "jsonBody", "base64Body", "bodyFileName"}

func parseResponse(errs *wmcompat.ErrorList, raw json.RawMessage, cs *CompiledStub, opts Options) {
	resp := &cs.Response
	resp.Status = 200

	if len(raw) == 0 {
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response", "response must be a JSON object")
		return
	}
	for _, field := range sortedKeys(doc) {
		if !supportedResponseFields[field] {
			reportUnsupported(errs, "/response/"+field, field)
		}
	}

	parseStatus(errs, doc, resp)
	decodeString(errs, doc, "statusMessage", "/response/statusMessage", &resp.StatusMessage)
	resp.StatusMessage = reasonPhrase(resp.StatusMessage)
	parseResponseHeaders(errs, doc, resp)
	parseBody(errs, doc, resp)
	parseDelays(errs, doc, resp)
	parseFault(errs, doc, resp)
	parseTransformers(errs, doc, resp)

	compileTemplates(errs, cs, opts)

	if resp.Fault != "" && len(resp.Body) > 0 {
		// A fault replaces the response rather than decorating it; a stub
		// asking for both is stating two different intents.
		errs.Addf(wmcompat.CodeMalformed, "/response/fault",
			"a fault replaces the response, so it cannot be combined with a body")
	}
}

// reasonPhrase encodes a statusMessage into the bytes that go on the status
// line, which is not a plain copy of what was registered.
//
// The status line is terminated by CRLF and carries no encoding, so a phrase
// containing either would split the response, and a phrase containing anything
// above Latin-1 has no representation there at all. WireMock 3.13.2 resolves
// both the same way, established by probing it: CR and LF each become '?', a
// rune outside Latin-1 becomes '?', and everything else — tabs and the other
// control characters included — goes out as its Latin-1 byte.
//
// Encoding rather than rejecting is what P3 asks for here: the point of
// rejecting is to avoid accepting a stub and then behaving differently, and
// this behaves identically to the server being mocked. A 422 would instead fail
// stubs that WireMock serves.
//
// Done once at compile time so serving a stub with a reason phrase stays a
// string copy, like every other pre-resolved part of a response.
func reasonPhrase(s string) string {
	// The common phrase is plain ASCII, which is already its own encoding.
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || c == '\r' || c == '\n' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\r' || r == '\n' || r > 0xFF {
			b.WriteByte('?')
			continue
		}
		b.WriteByte(byte(r))
	}
	return b.String()
}

func parseStatus(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	v, ok := doc["status"]
	if !ok {
		return
	}
	if err := json.Unmarshal(v, &resp.Status); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response/status", "status must be an integer")
		return
	}
	if resp.Status <= 0 {
		// Normalised rather than rejected: WireMock treats a non-positive status
		// as unset and serves 200.
		resp.Status = 200
		return
	}
	if resp.Status < 100 || resp.Status > 599 {
		// WireMock writes a positive out-of-range status unvalidated, which
		// produces a malformed status line on the wire (1000 becomes ":00 :00").
		// Refusing it is a deliberate deviation from a defect.
		errs.Addf(wmcompat.CodeMalformed, "/response/status",
			fmt.Sprintf("status %d is outside the valid HTTP range", resp.Status))
	}
}

func parseResponseHeaders(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	v, ok := doc["headers"]
	if !ok {
		return
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(v, &entries); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response/headers", "headers must be a JSON object")
		return
	}

	for _, name := range sortedKeys(entries) {
		value := entries[name]

		var single string
		if err := json.Unmarshal(value, &single); err == nil {
			resp.Headers = append(resp.Headers, Header{Name: name, Value: single})
			continue
		}
		// A header may carry several values, which WireMock states as an array.
		var multi []string
		if err := json.Unmarshal(value, &multi); err == nil {
			for _, v := range multi {
				resp.Headers = append(resp.Headers, Header{Name: name, Value: v})
			}
			continue
		}
		errs.Addf(wmcompat.CodeMalformed, "/response/headers/"+name,
			"a response header value must be a string or an array of strings")
	}
}

func parseBody(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	var present []string
	for _, field := range bodyFields {
		raw, ok := doc[field]
		if !ok {
			continue
		}
		// An explicit null is absence, not a competing body form; WireMock
		// treats {"body":null,"jsonBody":{…}} as declaring only jsonBody, and
		// reporting a conflict there would refuse a stub it accepts.
		if string(raw) == "null" {
			continue
		}
		present = append(present, field)
	}
	if len(present) > 1 {
		sort.Strings(present)
		errs.Addf(wmcompat.CodeMalformed, "/response",
			"exactly one body form may be set, found "+strings.Join(present, ", "))
		return
	}
	if len(present) == 0 {
		return
	}

	switch field := present[0]; field {
	case "body":
		var s string
		if err := json.Unmarshal(doc[field], &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/body", "body must be a string")
			return
		}
		resp.Body = []byte(s)

	case "jsonBody":
		// Served compact, as WireMock does: the submitted document's
		// indentation is formatting, not payload.
		var compact bytes.Buffer
		if err := json.Compact(&compact, doc[field]); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/jsonBody",
				"jsonBody is not valid JSON: "+err.Error())
			return
		}
		resp.Body = compact.Bytes()

	case "base64Body":
		var s string
		if err := json.Unmarshal(doc[field], &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/base64Body", "base64Body must be a string")
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/base64Body",
				"base64Body is not valid base64: "+err.Error())
			return
		}
		resp.Body = decoded

	case "bodyFileName":
		var s string
		if err := json.Unmarshal(doc[field], &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/bodyFileName",
				"bodyFileName must be a string")
			return
		}
		if s == "" {
			errs.Addf(wmcompat.CodeMalformed, "/response/bodyFileName",
				"bodyFileName must not be empty")
			return
		}
		// Existence is deliberately NOT checked here: registering a stub before
		// uploading its file is legal, and the reference resolves at snapshot
		// build (SPEC §4.3, §6.9).
		resp.BodyFileName = s
	}
}

func parseDelays(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	if v, ok := doc["fixedDelayMilliseconds"]; ok {
		var ms int64
		if err := json.Unmarshal(v, &ms); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/fixedDelayMilliseconds",
				"fixedDelayMilliseconds must be an integer")
		} else if ms < 0 {
			errs.Addf(wmcompat.CodeMalformed, "/response/fixedDelayMilliseconds",
				"fixedDelayMilliseconds must not be negative")
		} else {
			resp.FixedDelay = time.Duration(ms) * time.Millisecond
			resp.FixedDelaySet = true
		}
	}

	if v, ok := doc["delayDistribution"]; ok {
		resp.Delay = parseDelayDistribution(errs, v, "/response/delayDistribution")
	}

	if v, ok := doc["chunkedDribbleDelay"]; ok {
		var d struct {
			NumberOfChunks *int   `json:"numberOfChunks"`
			TotalDuration  *int64 `json:"totalDuration"`
		}
		if err := json.Unmarshal(v, &d); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/chunkedDribbleDelay",
				"chunkedDribbleDelay must be an object")
			return
		}
		if d.NumberOfChunks == nil || d.TotalDuration == nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/chunkedDribbleDelay",
				"chunkedDribbleDelay needs numberOfChunks and totalDuration")
			return
		}
		if *d.NumberOfChunks < 1 {
			errs.Addf(wmcompat.CodeMalformed, "/response/chunkedDribbleDelay",
				"numberOfChunks must be at least 1")
			return
		}
		if *d.TotalDuration < 0 {
			errs.Addf(wmcompat.CodeMalformed, "/response/chunkedDribbleDelay",
				"totalDuration must not be negative")
			return
		}
		resp.Dribble = &ChunkedDribble{
			NumberOfChunks: *d.NumberOfChunks,
			TotalDuration:  time.Duration(*d.TotalDuration) * time.Millisecond,
		}
	}
}

// parseDelayDistribution compiles a WireMock delay distribution. The pointer is
// a parameter because the same document appears under `/response` on a stub and
// at the root of a settings write, and two parsers would be two answers to the
// question of what a valid distribution is.
func parseDelayDistribution(errs *wmcompat.ErrorList, raw json.RawMessage, pointer string) DelayDistribution {
	var d struct {
		Type   string   `json:"type"`
		Lower  *int64   `json:"lower"`
		Upper  *int64   `json:"upper"`
		Median *int64   `json:"median"`
		Sigma  *float64 `json:"sigma"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		errs.Addf(wmcompat.CodeMalformed, pointer, "delayDistribution must be an object")
		return DelayDistribution{}
	}

	switch d.Type {
	case "uniform":
		if d.Lower == nil || d.Upper == nil {
			errs.Addf(wmcompat.CodeMalformed, pointer,
				"a uniform delayDistribution needs lower and upper")
			return DelayDistribution{}
		}
		if *d.Lower < 0 || *d.Upper < *d.Lower {
			errs.Addf(wmcompat.CodeMalformed, pointer,
				"a uniform delayDistribution needs 0 <= lower <= upper")
			return DelayDistribution{}
		}
		return DelayDistribution{
			Kind:  DelayUniform,
			Lower: time.Duration(*d.Lower) * time.Millisecond,
			Upper: time.Duration(*d.Upper) * time.Millisecond,
		}

	case "lognormal":
		if d.Median == nil || d.Sigma == nil {
			errs.Addf(wmcompat.CodeMalformed, pointer,
				"a lognormal delayDistribution needs median and sigma")
			return DelayDistribution{}
		}
		if *d.Median < 0 || *d.Sigma < 0 {
			errs.Addf(wmcompat.CodeMalformed, pointer,
				"a lognormal delayDistribution needs a non-negative median and sigma")
			return DelayDistribution{}
		}
		return DelayDistribution{
			Kind:   DelayLogNormal,
			Median: time.Duration(*d.Median) * time.Millisecond,
			Sigma:  *d.Sigma,
		}

	case "":
		errs.Addf(wmcompat.CodeMalformed, pointer,
			"delayDistribution needs a type of uniform or lognormal")

	default:
		errs.Addf(wmcompat.CodeMalformed, pointer,
			fmt.Sprintf("unknown delayDistribution type %q, want uniform or lognormal", d.Type))
	}
	return DelayDistribution{}
}

func parseFault(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	v, ok := doc["fault"]
	if !ok {
		return
	}
	var name string
	if err := json.Unmarshal(v, &name); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/response/fault", "fault must be a string")
		return
	}
	if !validFaults[name] {
		errs.Addf(wmcompat.CodeMalformed, "/response/fault",
			fmt.Sprintf("unknown fault %q, want one of %s", name, strings.Join(faultNames(), ", ")))
		return
	}
	resp.Fault = name
}

func faultNames() []string {
	out := make([]string, 0, len(validFaults))
	for name := range validFaults {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func parseTransformers(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, resp *CompiledResponse) {
	if v, ok := doc["transformers"]; ok {
		var names []string
		if err := json.Unmarshal(v, &names); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/transformers",
				"transformers must be an array of strings")
			return
		}
		for i, name := range names {
			if name != ResponseTemplateTransformer {
				// An unrecognised transformer would silently do nothing, which
				// is exactly the accept-and-ignore failure mode P3 rules out.
				errs.Addf(wmcompat.CodeUnknownTransformer,
					fmt.Sprintf("/response/transformers/%d", i),
					fmt.Sprintf("unknown transformer %q; only %q is supported",
						name, ResponseTemplateTransformer))
				continue
			}
			resp.Templated = true
		}
	}

	if v, ok := doc["transformerParameters"]; ok {
		var params map[string]any
		if err := json.Unmarshal(v, &params); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/response/transformerParameters",
				"transformerParameters must be a JSON object")
			return
		}
		resp.TransformerParameters = params
	}
}

// disableBodyTemplating is the per-stub opt-out of SPEC §10.1.
const disableBodyTemplating = "disableBodyTemplating"

// compileTemplates parses the response's templated parts at registration, so a
// parse error or an unknown helper is a 422 here rather than a broken response
// on every request afterwards (P3, deviation #13).
func compileTemplates(errs *wmcompat.ErrorList, cs *CompiledStub, opts Options) {
	resp := &cs.Response

	active := resp.Templated || opts.GlobalTemplating
	if !active || opts.CompileTemplate == nil {
		return
	}
	resp.Templated = true

	// The opt-out covers the body only; headers stay templated, which is what
	// the field name says and what makes it useful for a stub whose body is
	// binary or already carries braces.
	bodyDisabled := false
	if raw, ok := resp.TransformerParameters[disableBodyTemplating]; ok {
		if b, isBool := raw.(bool); isBool {
			bodyDisabled = b
		}
	}

	// A value with no braces never reaches the engine, which is what keeps
	// templating free for the parts of a stub that do not use it.
	if !bodyDisabled && IsTemplated(string(resp.Body)) {
		tpl, err := opts.CompileTemplate(string(resp.Body))
		if err != nil {
			errs.Addf(wmcompat.CodeTemplate, "/response/body", err.Error())
		} else {
			resp.BodyTemplate = tpl
		}
	}

	for i, h := range resp.Headers {
		if !IsTemplated(h.Value) {
			continue
		}
		tpl, err := opts.CompileTemplate(h.Value)
		if err != nil {
			errs.Addf(wmcompat.CodeTemplate, "/response/headers/"+h.Name, err.Error())
			continue
		}
		resp.HeaderTemplates = append(resp.HeaderTemplates, HeaderTemplate{Index: i, Template: tpl})
	}
}
