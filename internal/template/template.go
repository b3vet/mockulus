// SPDX-License-Identifier: Apache-2.0

package template

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/b3vet/mockulus/internal/handlebars"
)

// Engine compiles and renders response templates.
//
// Compilation happens once, at stub registration. Rendering is a walk of the
// parsed tree against a request model built lazily — a stub whose body carries
// no `{{` never reaches this package at all.
type Engine struct {
	registry  *handlebars.Registry
	maxOutput int
}

// NewEngine builds the engine with the allowlist of SPEC §10.3.
func NewEngine(maxOutput int, jsonPath handlebars.Helper) *Engine {
	return &Engine{registry: NewRegistry(jsonPath), maxOutput: maxOutput}
}

// HelperNames lists the registered helpers, which the 422 for an unknown one
// quotes so the author can see what is available.
func (e *Engine) HelperNames() []string { return e.registry.Names() }

// Compile parses a template and rejects anything it cannot serve.
//
// Both failure modes are registration-time by design: a parse error, and a
// reference to a helper outside the allowlist. WireMock defers both to serve
// time; deferring them would mean a stub that registers cleanly and then fails
// on every request, which is the opposite of the fail-loud contract (P3,
// deviation #13).
func (e *Engine) Compile(source string) (*handlebars.Template, error) {
	tpl, err := handlebars.Parse(source)
	if err != nil {
		return nil, err
	}
	// `{{now}}` is a helper called with no arguments, and a mustache holding one
	// token is only distinguishable from a variable by asking the registry.
	// This is where the registry is, and compile time is where the question
	// belongs: the answer is the same for every request the stub will ever
	// serve, so the render path never has to ask it (§16.3 rule 2).
	tpl.BindBareHelpers(e.registry.Has)

	for _, name := range tpl.Helpers() {
		if !e.registry.Has(name) {
			return nil, fmt.Errorf(
				"unknown helper %q; mockulus supports %s",
				name, strings.Join(e.registry.Names(), ", "))
		}
	}
	return tpl, nil
}

// Render evaluates a compiled template against a request.
func (e *Engine) Render(tpl *handlebars.Template, ctx map[string]any) (string, error) {
	return tpl.Render(ctx, e.registry, handlebars.RenderOptions{MaxOutput: e.maxOutput})
}

// HasTemplate reports whether a value carries template syntax at all. Values
// without it skip the engine entirely, which is what makes templating free for
// stubs that do not use it (SPEC §10.1).
func HasTemplate(s string) bool { return strings.Contains(s, "{{") }

// BuildContext assembles the model a template can see (SPEC §10.2).
//
// This is the whole surface: the request, and the stub's own transformer
// parameters. There is deliberately no environment, no filesystem and no
// clock beyond the `now` helper — a template cannot reach anything the request
// did not bring with it.
func BuildContext(r *http.Request, body []byte, pathVars map[string]string,
	parameters map[string]any) map[string]any {

	fullURL := r.URL.RequestURI()
	path := fullURL
	if i := strings.IndexByte(fullURL, '?'); i >= 0 {
		path = fullURL[:i]
	}

	segments := pathSegments(path)

	// `request.path` is both the path string and an indexable segment list, so
	// {{request.path}} and {{request.path.[0]}} both work. A map that also
	// carries the string under a key the stringifier prefers gives both.
	//
	// Index keys are spelled with strconv rather than fmt here and in the two
	// models below: this runs once per segment of every templated request, and
	// fmt on the request path is what SPEC §16.3 rule 4 forbids.
	pathModel := map[string]any{
		"segments": segments,
	}
	for i, seg := range segments {
		pathModel[strconv.Itoa(i)] = seg
	}
	for name, value := range pathVars {
		pathModel[name] = value
	}

	request := map[string]any{
		"id":           requestID(r),
		"url":          fullURL,
		"path":         pathValue{text: path, model: pathModel},
		"pathSegments": segments,
		"method":       r.Method,
		"host":         hostOnly(r.Host),
		"port":         portOnly(r.Host),
		"scheme":       schemeOf(r),
		"baseUrl":      baseURL(r),
		"clientIp":     clientIP(r),
		"headers":      headerModel(r.Header),
		"cookies":      cookieModel(r),
		"query":        queryModel(fullURL),
		"body":         string(body),
		"bodyAsBase64": base64Body(body),
	}

	ctx := map[string]any{"request": request}
	if parameters != nil {
		ctx["parameters"] = parameters
	}
	return ctx
}

// base64Body defers `request.bodyAsBase64` until a template reads it.
//
// Encoding on the way in charges every templated response for a second copy of
// the request body at four-thirds its size, whether or not the template
// mentions it — and the bodies a mock server is handed are as large as the API
// it stands in for, so that cost has no ceiling (P2). Everything that consumes
// a context value goes through handlebars.Stringify or toFloat, both of which
// take a Stringer, so deferring is invisible to templates.
//
// The empty body stays a string: Truthy has no case for a Stringer and would
// report the encoding of nothing as true, which would flip {{#if}} on a body
// that is not there.
func base64Body(body []byte) any {
	if len(body) == 0 {
		return ""
	}
	return base64Value(body)
}

type base64Value []byte

// String implements fmt.Stringer.
func (b base64Value) String() string { return base64.StdEncoding.EncodeToString(b) }

// pathValue lets `request.path` be both a string and a container, which is what
// {{request.path}} and {{request.path.[1]}} each need.
//
// It is deliberately not a list, unlike the multi-valued keys below: its
// container nature is a set of named parts — the segments, and a path
// template's variables — rather than a sequence, and a path is one thing that
// happens to have slashes in it.
type pathValue struct {
	text  string
	model map[string]any
}

// String makes the stringifier render the path itself.
func (p pathValue) String() string { return p.text }

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return randomUUID()
}

func headerModel(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for name, values := range h {
		// Both spellings resolve, because a template author writes the header
		// name as they saw it on the wire, not as Go canonicalised it.
		entry := headerValues(values)
		out[name] = entry
		out[strings.ToLower(name)] = entry
	}
	return out
}

// headerValues renders as the first value, indexes as the full list and
// iterates as all of them, so {{request.headers.X}}, {{request.headers.X.[1]}}
// and {{#each request.headers.X}} each answer the question they were asked.
//
// The three answers are three different things and one value has to give all
// of them. Carrying the values as a list beside the scalar is what adds the
// third: with only the scalar and the index map, `{{#each request.query.tag}}`
// had nothing to walk and rendered nothing — no error, no empty element, just a
// block that never ran, which is how a response silently drops the repeated
// values a caller sent.
func headerValues(values []string) any {
	if len(values) == 0 {
		return ""
	}
	return multiValue(values)
}

// multiValue is a name the wire carried one or more values under.
//
// It is a list that also reads as a scalar, rather than a scalar that also
// indexes, and which way round that sits is the whole of it: a stub written for
// the ordinary single-valued case must keep serving `gold` and never `[gold]`,
// while a stub written for the repeated case must be able to walk the values.
// The scalar reading is therefore a String method and the list is the type
// itself, so a helper that wants the values asks for them by type and
// everything else gets the first one through the stringifier.
type multiValue []string

// String renders the first value, which is what {{request.query.tag}} means on
// a key that arrived more than once — the reading the oracle gives, and the one
// every stub written before the key was ever repeated depends on.
//
// A key with no values is not one of these — headerValues answers that with the
// empty string before a node is built — but the check stays, because the cost
// of being wrong about that is an index panic on the serve path and the cost of
// keeping it is a compare.
func (m multiValue) String() string {
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// Lookup implements the index form, {{request.query.tag.[1]}}. A key that is
// not an index, or one past the end, selects nothing rather than failing the
// render: a template reaching for the second value of a key that arrived once
// is asking a question the request simply does not answer.
func (m multiValue) Lookup(key string) (any, bool) {
	i, err := strconv.Atoi(key)
	if err != nil || i < 0 || i >= len(m) {
		return nil, false
	}
	return m[i], true
}

// List implements handlebars.Lister, the iteration form.
func (m multiValue) List() []any {
	out := make([]any, len(m))
	for i, v := range m {
		out[i] = v
	}
	return out
}

func cookieModel(r *http.Request) map[string]any {
	out := map[string]any{}
	for _, c := range r.Cookies() {
		out[c.Name] = c.Value
	}
	return out
}

func queryModel(requestURI string) map[string]any {
	out := map[string]any{}
	i := strings.IndexByte(requestURI, '?')
	if i < 0 {
		return out
	}
	for _, pair := range strings.Split(requestURI[i+1:], "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		name, value = unescape(name), unescape(value)
		// A repeated parameter grows the values it already has rather than
		// replacing them, so no spelling of the key quietly wins over another.
		if existing, repeated := out[name].(multiValue); repeated {
			out[name] = append(existing, value)
			continue
		}
		out[name] = headerValues([]string{value})
	}
	return out
}

func unescape(s string) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}

func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

func portOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[i+1:]
	}
	return ""
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func baseURL(r *http.Request) string { return schemeOf(r) + "://" + r.Host }

// clientIP reports the peer address, without consulting forwarding headers: a
// template that trusted them would be reporting whatever the caller claimed.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 && !strings.Contains(addr[i:], "]") {
		return addr[:i]
	}
	return addr
}

// resolvePathValue lets the evaluator look inside a pathValue.
func (p pathValue) Lookup(key string) (any, bool) {
	v, ok := p.model[key]
	return v, ok
}
