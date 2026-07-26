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

	// bodySubject persists for the whole request so a body examined by several
	// matchers is converted and parsed once.
	bodySubject matchers.Body

	// keyScratch backs the subject handed to each key criterion. One suffices:
	// criteria are evaluated strictly one at a time.
	keyScratch matchers.KeyValues

	// keyBuf assembles the snapshot index keys, whose shape is
	// "METHOD\x00url" (SPEC §6.1). See indexLookup for why they are not simply
	// concatenated.
	keyBuf []byte

	// pathVars holds the bindings produced by a urlPathTemplate match, which
	// the pathParameters criteria of that same stub then consume.
	pathVars map[string]string

	// scenarioStates memoizes scenario state reads so a request touching
	// several stubs in one scenario reads its state once (SPEC §9.2).
	scenarioStates map[string]string
	// scenarioErr holds a state read that could not be answered. It is carried
	// out of matching rather than returned from it because the gate is a
	// predicate: without somewhere to put the failure, an unreachable store
	// reads as "this stub does not match" and the request is answered from the
	// wrong side of the state machine (SPEC §9.2).
	scenarioErr error
}

var requestPool = sync.Pool{
	New: func() any {
		return &ParsedRequest{
			pathVars:       make(map[string]string, 4),
			scenarioStates: make(map[string]string, 2),
			keyBuf:         make([]byte, 0, initialKeyBuf),
		}
	},
}

// initialKeyBuf comfortably holds a method and a path of ordinary length, so a
// pooled request assembles its index keys without growing the buffer.
const initialKeyBuf = 128

// maxPooledKeyBuf bounds what a pooled request keeps. The buffer grows to the
// longest URL that instance has seen, and net/http will accept a request line
// close to a megabyte; without this, one such request permanently inflates a
// pool entry that every later request reuses.
const maxPooledKeyBuf = 4 << 10

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
	r.FullURL = requestTarget(req)
	r.Path = r.FullURL
	if i := strings.IndexByte(r.FullURL, '?'); i >= 0 {
		r.Path = r.FullURL[:i]
	}
	r.header = req.Header
	r.body = body
	r.bodySubject.Set(body)
}

// requestTarget returns the request line's target: path and query exactly as
// the client wrote them, which is what a byte-exact `url` criterion compares
// against.
//
// net/http keeps that string verbatim in RequestURI, and re-deriving it from
// the parsed URL instead is not free — URL.RequestURI re-escapes the path,
// scanning it byte by byte with a per-byte function call, which the serve
// profile showed costing as much as the whole match. For an origin-form target
// the two agree exactly, because url.setPath keeps the raw form in RawPath
// whenever the default encoding would differ from it;
// TestRequestTargetIsTheTargetAsReceived holds them to that over the encodings
// where it would be easiest to be wrong.
//
// The leading-slash guard is what limits the shortcut to the origin form. A
// proxy client's absolute target is the case where the two genuinely disagree —
// RequestURI carries the scheme and host, and a stub's URL criterion is written
// against neither — so that one goes the long way round.
func requestTarget(req *http.Request) string {
	if uri := req.RequestURI; uri != "" && uri[0] == '/' {
		return uri
	}
	return req.URL.RequestURI()
}

// Reset clears every reference the request held. Pooled memory that keeps a
// pointer to a previous request's body is a leak that only shows up under load,
// so this is deliberately exhaustive.
func (r *ParsedRequest) Reset() {
	r.Method, r.Path, r.FullURL = "", "", ""
	r.header = nil
	r.body = nil

	r.query, r.queryParsed = nil, false
	r.cookies, r.cookiesParsed = nil, false
	r.form, r.formParsed = nil, false

	r.bodySubject.Reset()
	r.keyScratch.Set(false, nil)

	// Truncated rather than dropped: it holds bytes copied out of the request
	// rather than a reference into it, and keeping the capacity is the whole
	// reason index lookups allocate nothing. An outsized one is dropped.
	if cap(r.keyBuf) > maxPooledKeyBuf {
		r.keyBuf = nil
	} else {
		r.keyBuf = r.keyBuf[:0]
	}

	clear(r.pathVars)
	clear(r.scenarioStates)
	r.scenarioErr = nil
}

// Body returns the raw request body.
func (r *ParsedRequest) Body() []byte { return r.body }

// indexLookup probes one of the snapshot's URL indexes for this request.
//
// The key is assembled into the request's own buffer rather than written as
// method+methodSep+url, because Go keeps a concatenated string off the heap
// only while it fits a 32-byte stack buffer. A method plus any realistic path
// exceeds that, and every request probes four keys — so the obvious spelling
// costs four heap allocations on the exact-URL hit that most traffic is, while
// a benchmark using a short path reports none of them (SPEC §16.3 rule 1). The
// map lookup takes a string view of the buffer, which the compiler does not
// copy.
func (r *ParsedRequest) indexLookup(index map[string][]int32, method, url string) []int32 {
	r.keyBuf = append(r.keyBuf[:0], method...)
	r.keyBuf = append(r.keyBuf, methodSep...)
	r.keyBuf = append(r.keyBuf, url...)
	return index[string(r.keyBuf)]
}

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
//
// The media type is read here rather than in bind, because this is the only
// thing that wants it: deriving it up front would charge every request for a
// header lookup and a case fold on behalf of the one stub in a thousand that
// matches on form parameters (P2).
func (r *ParsedRequest) FormSubject(name string) matchers.Subject {
	if !r.formParsed {
		r.formParsed = true
		if mediaType(r.header.Get("Content-Type")) == "application/x-www-form-urlencoded" {
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

// FailScenarioRead records a state read the store could not answer.
//
// The first failure is kept: it is the one that made the request unanswerable,
// and a later candidate's error says nothing new about why.
func (r *ParsedRequest) FailScenarioRead(err error) {
	if r.scenarioErr == nil {
		r.scenarioErr = err
	}
}

// ScenarioError returns the state read that failed, or nil.
func (r *ParsedRequest) ScenarioError() error { return r.scenarioErr }

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
