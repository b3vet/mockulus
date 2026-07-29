// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/matchers"
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
	_, resp.HasStatusMessage = doc["statusMessage"]
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
	entries, ok := headerEntries(v)
	if !ok {
		errs.Addf(wmcompat.CodeMalformed, "/response/headers", "headers must be a JSON object")
		return
	}

	for _, header := range foldHeaderSpellings(entries) {
		// The pointer of any complaint names the spelling the author used, not
		// the one the fold kept, so the message points at a line of their
		// document rather than at a name that appears elsewhere in it.
		before := len(resp.Headers)

		for _, source := range header.sources {
			var single string
			if err := json.Unmarshal(source.value, &single); err == nil {
				resp.Headers = append(resp.Headers, Header{Name: header.name, Value: single})
				continue
			}
			// A header may carry several values, which WireMock states as an array.
			var multi []string
			if err := json.Unmarshal(source.value, &multi); err == nil {
				if len(multi) == 0 {
					// An empty array names a header and then gives it nothing to
					// say. Read literally the loop below runs zero times, which
					// registers the stub and serves no such header — the stub the
					// author wrote and the stub the server holds differ, and they
					// only find out from the client that was waiting for the header.
					// This is the one place in the response surface where mockulus
					// itself was accept-and-ignore, which P3 forbids. WireMock
					// refuses it too ("No value for X-A"), so no mappings file that
					// registers there stops registering here.
					errs.Addf(wmcompat.CodeMalformed, "/response/headers/"+source.name,
						"a response header value array must not be empty")
					continue
				}
				for _, value := range multi {
					resp.Headers = append(resp.Headers, Header{Name: header.name, Value: value})
				}
				continue
			}
			errs.Addf(wmcompat.CodeMalformed, "/response/headers/"+source.name,
				"a response header value must be a string or an array of strings")
		}

		// A response carries exactly one Content-Type, and its value is one
		// media type, so several of them is a document asking for something the
		// wire has no way to express. Neither answer available is right: joining
		// them sends `application/json, text/plain`, which is not a media type
		// any client can parse, and taking the last — WireMock's answer, with a
		// charset appended that the stub never mentioned — serves neither of the
		// values that were written. Both quietly hand back a header the author
		// did not ask for.
		//
		// So it is refused, and the author finds out at registration rather than
		// from whichever client tried to read it. This is P3's rule applied to
		// an ambiguity rather than to an unsupported feature: there is no
		// reading of two media types that is more likely to be what was meant,
		// and guessing is the thing that rule exists to prevent.
		if len(resp.Headers)-before > 1 && strings.EqualFold(header.name, "Content-Type") {
			errs.Addf(wmcompat.CodeMalformed, "/response/headers/"+header.name,
				"Content-Type takes a single media type, and this response declares "+
					strconv.Itoa(len(resp.Headers)-before))
			resp.Headers = resp.Headers[:before]
		}
	}
}

// headerEntry is one member of a response's `headers` object, kept in the order
// the document wrote it.
type headerEntry struct {
	name  string
	value json.RawMessage
}

// foldedHeader is one response header and every value the document gave it.
type foldedHeader struct {
	// name is the first spelling the document used for this header, which is
	// the spelling that goes on the wire and into the stored document.
	name string
	// sources are the entries that named it, in the order they were written.
	sources []headerEntry
}

// headerEntries reads a `headers` object as a token stream rather than into a
// map, because a map cannot answer either of the questions this object asks.
//
// The order the author wrote is part of what the object means: the values of a
// repeated header are a sequence, and `{"x-dup": "first", "X-DUP": "second"}`
// asks for first and then second. Decoding into a map loses that order, and the
// sorted walk that stood in for it reversed the pair whenever the second
// spelling sorted ahead of the first — a stub whose two Set-Cookie lines then
// arrived the wrong way round, with nothing in the document to explain it.
//
// A name written twice is the other question. JSON does not say what that
// means, so it is worth stating what both deserializers do rather than
// inheriting it: the last value wins, and it wins in the position the first one
// claimed. Reporting false means the value was not an object at all.
func headerEntries(raw json.RawMessage) ([]headerEntry, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		return nil, false
	}

	var entries []headerEntry
	at := make(map[string]int)
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, isName := key.(string)
		if !isName {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		if i, written := at[name]; written {
			entries[i].value = value
			continue
		}
		at[name] = len(entries)
		entries = append(entries, headerEntry{name: name, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	return entries, true
}

// foldHeaderSpellings groups the entries that name one header.
//
// Header names are case-insensitive on the wire, so `X-DUP` and `x-dup` are one
// header carrying two values and not two headers carrying one each. WireMock
// resolves them the same way and keeps the first spelling the document used,
// for the served field lines as well as for the document it echoes back.
//
// The fold is over ASCII only. A field name is an RFC 9110 token, so a name
// that folds anywhere else is a name that cannot be written to the wire, and
// folding beyond ASCII would merge two headers on the strength of a rule
// neither server will ever get to apply.
func foldHeaderSpellings(entries []headerEntry) []foldedHeader {
	folded := make([]foldedHeader, 0, len(entries))
	at := make(map[string]int, len(entries))
	for _, entry := range entries {
		key := asciiLower(entry.name)
		if i, seen := at[key]; seen {
			folded[i].sources = append(folded[i].sources, entry)
			continue
		}
		at[key] = len(folded)
		folded = append(folded, foldedHeader{name: entry.name, sources: []headerEntry{entry}})
	}
	return folded
}

func asciiLower(s string) string {
	lowered := []byte(s)
	changed := false
	for i, c := range lowered {
		if c >= 'A' && c <= 'Z' {
			lowered[i] = c + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(lowered)
}

// foldResponseHeaders rewrites a mapping document's response headers so that the
// document the server stores says what the response it will serve says.
//
// Two spellings of one name are one header, and the document has to spell that
// out or it is not a description of the response any more: a client that reads
// its mapping back sees two headers where the server holds one, and a client
// that diffs the document it sent against the document WireMock stored sees a
// difference that is not there. WireMock folds them into the first spelling
// with the values in an array, and this is that document.
//
// It rewrites only when there is something to fold, so every mapping that names
// each of its headers once is stored exactly as it was registered — including
// the single-element array, which WireMock collapses to a string and this
// deliberately does not, because that is a change to a document with no
// behaviour behind it.
//
// Anything it does not fully understand it leaves alone. It runs on the way
// into the store, ahead of the compile step on the paths that have one, so it
// cannot be the thing that decides whether a document is well formed.
func foldResponseHeaders(doc map[string]json.RawMessage) {
	rawResponse, ok := doc["response"]
	if !ok {
		return
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rawResponse, &response); err != nil || response == nil {
		return
	}
	rawHeaders, ok := response["headers"]
	if !ok {
		return
	}
	entries, ok := headerEntries(rawHeaders)
	if !ok {
		return
	}
	headers, folded := foldedHeadersJSON(foldHeaderSpellings(entries))
	if !folded {
		return
	}
	response["headers"] = headers
	rewritten, err := json.Marshal(response)
	if err != nil {
		return
	}
	doc["response"] = rewritten
}

// foldedHeadersJSON renders the folded headers back into a `headers` object,
// reporting false when nothing needed folding or a value was not one this
// package would have accepted anyway.
//
// The object is assembled by hand rather than marshalled from a map, because
// the order of the names is the order they will be sent in and a map has none.
// A header with one source keeps the exact bytes it was written with, so the
// only values that change form are the ones that had to.
func foldedHeadersJSON(headers []foldedHeader) (json.RawMessage, bool) {
	folded := false
	var out bytes.Buffer
	out.WriteByte('{')
	for i, header := range headers {
		if i > 0 {
			out.WriteByte(',')
		}
		name, err := json.Marshal(header.name)
		if err != nil {
			return nil, false
		}
		out.Write(name)
		out.WriteByte(':')

		if len(header.sources) == 1 {
			out.Write(header.sources[0].value)
			continue
		}
		folded = true

		out.WriteByte('[')
		values := 0
		for _, source := range header.sources {
			parts, ok := headerValues(source.value)
			if !ok {
				return nil, false
			}
			for _, part := range parts {
				if values > 0 {
					out.WriteByte(',')
				}
				out.Write(part)
				values++
			}
		}
		out.WriteByte(']')
	}
	out.WriteByte('}')
	if !folded {
		return nil, false
	}
	return out.Bytes(), true
}

// headerValues splits one entry's value into the values it contributes. A
// string contributes itself and an array contributes its elements, both
// verbatim, so a value survives the rewrite with the escapes it was written
// with. Anything else reports false and stops the rewrite.
func headerValues(raw json.RawMessage) ([]json.RawMessage, bool) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []json.RawMessage{raw}, true
	}
	var multi []json.RawMessage
	if err := json.Unmarshal(raw, &multi); err != nil {
		return nil, false
	}
	for _, value := range multi {
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return nil, false
		}
	}
	return multi, true
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
		// Decoded through the matcher package's reader rather than a second
		// call to encoding/base64: a response body and a binaryEqualTo operand
		// are the same question about the same alphabet, and two decoders here
		// would eventually disagree about which of them a mappings file can use.
		decoded, err := matchers.DecodeBase64(s)
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
		// The name has to be one the files store could hold. Existence is
		// deliberately NOT checked — registering a stub before uploading its
		// file is legal, and the reference resolves at snapshot build (SPEC
		// §4.3, §6.9) — but a name that is malformed can never resolve at all,
		// and letting it through means a stub that registers cleanly and then
		// answers 1022 on the request someone is debugging at the time.
		if reason := RejectFileName(s); reason != "" {
			errs.Addf(wmcompat.CodeMalformed, "/response/bodyFileName",
				"bodyFileName "+strconv.Quote(s)+" is not allowed: "+reason)
			return
		}
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
	// A value that is not a boolean is refused rather than ignored. Reading
	// `{"disableBodyTemplating": "true"}` as absent and templating the body is
	// accept-and-behave-differently, which is what P3 exists to prevent and the
	// same shape deviation #23 already rejects for `{"absent": false}`. The
	// author of that stub asked for the opposite of what they would have got.
	bodyDisabled := false
	if raw, ok := resp.TransformerParameters[disableBodyTemplating]; ok {
		b, isBool := raw.(bool)
		if !isBool {
			errs.Addf(wmcompat.CodeMalformed,
				"/response/transformerParameters/"+disableBodyTemplating,
				disableBodyTemplating+" takes a boolean")
		}
		bodyDisabled = b
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
