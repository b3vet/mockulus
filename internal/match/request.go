// SPDX-License-Identifier: Apache-2.0

package match

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/b3vet/mockulus/internal/matchers"
)

// ParsedRequest is the per-request working set: the request as the matcher
// needs to see it, with every derived form computed at most once and only when
// something asks for it.
//
// The laziness is the point. A stub matched on method and URL alone never
// causes its request's body to be read as a string, parsed as JSON, or split
// into form fields — features are pay-per-use, and a deployment that uses none
// of them pays for none of them (SPEC §6.4, P2).
//
// Instances come from a pool and are reused. Reset drops every reference so no
// request-scoped memory outlives the request that filled it.
type ParsedRequest struct {
	// Method is the request method, upper case.
	Method string
	// Path is the request path with no query string.
	Path string
	// FullURL is path and query exactly as received, which is what a byte-exact
	// `url` criterion compares against.
	FullURL string

	header http.Header
	body   []byte

	query       url.Values
	queryParsed bool

	cookies       map[string][]string
	cookiesParsed bool

	form       url.Values
	formParsed bool

	// contentType is the request's media type, lower-cased, without parameters.
	contentType string

	// bodySubject persists for the whole request so a body examined by several
	// matchers is converted and parsed once.
	bodySubject matchers.Body

	// keyScratch backs the subject handed to each key criterion. One suffices:
	// criteria are evaluated strictly one at a time.
	keyScratch matchers.KeyValues

	// pathVars holds the bindings produced by a urlPathTemplate match, which
	// the pathParameters criteria of that same stub then consume.
	pathVars map[string]string

	// scenarioStates memoizes scenario state reads so a request touching
	// several stubs in one scenario reads its state once (SPEC §9.2).
	scenarioStates map[string]string
}

var requestPool = sync.Pool{
	New: func() any {
		return &ParsedRequest{
			pathVars:       make(map[string]string, 4),
			scenarioStates: make(map[string]string, 2),
		}
	},
}

// AcquireRequest takes a ParsedRequest from the pool and binds it to an
// incoming request and its already-read body.
func AcquireRequest(r *http.Request, body []byte) *ParsedRequest {
	pr, _ := requestPool.Get().(*ParsedRequest)
	pr.bind(r, body)
	return pr
}

// ReleaseRequest returns a ParsedRequest to the pool after clearing it.
func ReleaseRequest(pr *ParsedRequest) {
	pr.Reset()
	requestPool.Put(pr)
}

func (r *ParsedRequest) bind(req *http.Request, body []byte) {
	r.Method = req.Method
	r.FullURL = req.URL.RequestURI()
	r.Path = r.FullURL
	if i := strings.IndexByte(r.FullURL, '?'); i >= 0 {
		r.Path = r.FullURL[:i]
	}
	r.header = req.Header
	r.body = body
	r.bodySubject.Set(body)
	r.contentType = mediaType(req.Header.Get("Content-Type"))
}

// Reset clears every reference the request held. Pooled memory that keeps a
// pointer to a previous request's body is a leak that only shows up under load,
// so this is deliberately exhaustive.
func (r *ParsedRequest) Reset() {
	r.Method, r.Path, r.FullURL, r.contentType = "", "", "", ""
	r.header = nil
	r.body = nil

	r.query, r.queryParsed = nil, false
	r.cookies, r.cookiesParsed = nil, false
	r.form, r.formParsed = nil, false

	r.bodySubject.Reset()
	r.keyScratch.Set(false, nil)

	clear(r.pathVars)
	clear(r.scenarioStates)
}

// Body returns the raw request body.
func (r *ParsedRequest) Body() []byte { return r.body }

// HeaderValues returns every value sent for a header, looked up
// case-insensitively.
func (r *ParsedRequest) HeaderValues(name string) []string { return r.header.Values(name) }

// BodySubject returns the subject for body criteria.
func (r *ParsedRequest) BodySubject() matchers.Subject { return &r.bodySubject }

// HeaderSubject returns the subject for a header, matched case-insensitively
// as WireMock does. Go canonicalises header names on both store and lookup, so
// the case-insensitivity comes for free and correctly.
func (r *ParsedRequest) HeaderSubject(name string) matchers.Subject {
	values := r.header.Values(name)
	r.keyScratch.Set(values != nil, values)
	return &r.keyScratch
}

// QuerySubject returns the subject for a query parameter. Parameter names are
// case-sensitive, unlike header names.
func (r *ParsedRequest) QuerySubject(name string) matchers.Subject {
	if !r.queryParsed {
		r.query = parseQuery(r.FullURL)
		r.queryParsed = true
	}
	values, present := r.query[name]
	r.keyScratch.Set(present, values)
	return &r.keyScratch
}

// CookieSubject returns the subject for a cookie.
func (r *ParsedRequest) CookieSubject(name string) matchers.Subject {
	if !r.cookiesParsed {
		r.cookies = parseCookies(r.header.Values("Cookie"))
		r.cookiesParsed = true
	}
	values, present := r.cookies[name]
	// An absent cookie fails a negative matcher too, unlike an absent header.
	r.keyScratch.SetStrictAbsence(present, values)
	return &r.keyScratch
}

// FormSubject returns the subject for a form field, parsing the body as
// urlencoded form data on first use. A request that is not form-encoded has no
// form fields rather than an error: the criterion simply does not match.
func (r *ParsedRequest) FormSubject(name string) matchers.Subject {
	if !r.formParsed {
		r.formParsed = true
		if r.contentType == "application/x-www-form-urlencoded" {
			if parsed, err := url.ParseQuery(string(r.body)); err == nil {
				r.form = parsed
			}
		}
	}
	values, present := r.form[name]
	r.keyScratch.Set(present, values)
	return &r.keyScratch
}

// PathVarSubject returns the subject for a path-template variable bound by the
// URL match of the stub currently being evaluated.
func (r *ParsedRequest) PathVarSubject(name string) matchers.Subject {
	value, present := r.pathVars[name]
	if !present {
		r.keyScratch.Set(false, nil)
		return &r.keyScratch
	}
	r.keyScratch.Set(true, []string{value})
	return &r.keyScratch
}

// PathVars exposes the bindings of the stub that matched, which a response
// template reads as request.path.<name>.
func (r *ParsedRequest) PathVars() map[string]string { return r.pathVars }

// BindPathVar records a path-template binding for the stub being evaluated.
func (r *ParsedRequest) BindPathVar(name, value string) { r.pathVars[name] = value }

// ClearPathVars drops the bindings of the previously evaluated stub, so one
// stub's variables can never leak into another's criteria.
func (r *ParsedRequest) ClearPathVars() { clear(r.pathVars) }

// ScenarioState returns a memoized scenario state read.
func (r *ParsedRequest) ScenarioState(name string) (string, bool) {
	state, ok := r.scenarioStates[name]
	return state, ok
}

// MemoizeScenarioState records a state read so the same request does not read
// it again (SPEC §9.2).
func (r *ParsedRequest) MemoizeScenarioState(name, state string) {
	r.scenarioStates[name] = state
}

// parseQuery extracts the query string from a request URI and parses it.
// Values that fail to unescape are kept in raw form rather than dropped: a stub
// matching on an odd-looking parameter should still see it.
func parseQuery(requestURI string) url.Values {
	i := strings.IndexByte(requestURI, '?')
	if i < 0 {
		return url.Values{}
	}
	values, err := url.ParseQuery(requestURI[i+1:])
	if err != nil && values == nil {
		return url.Values{}
	}
	return values
}

// parseCookies splits Cookie header values into name/value pairs. A name may
// legitimately appear more than once, so values accumulate.
func parseCookies(headers []string) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string][]string, 4)
	for _, header := range headers {
		for _, pair := range strings.Split(header, ";") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			name, value, found := strings.Cut(pair, "=")
			if !found {
				continue
			}
			name = strings.TrimSpace(name)
			value = strings.Trim(strings.TrimSpace(value), `"`)
			out[name] = append(out[name], value)
		}
	}
	return out
}

// mediaType strips parameters and case from a Content-Type header, so
// "application/json; charset=utf-8" compares as "application/json".
func mediaType(header string) string {
	if header == "" {
		return ""
	}
	if i := strings.IndexByte(header, ';'); i >= 0 {
		header = header[:i]
	}
	return strings.ToLower(strings.TrimSpace(header))
}
