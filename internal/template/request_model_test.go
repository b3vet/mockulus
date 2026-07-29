// SPDX-License-Identifier: Apache-2.0

package template

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/handlebars"
)

// The request model of SPEC §10.2 for the shapes the E2E corpus does not send.
// A corpus request arrives over a loopback listener from a Go client, so it is
// always IPv4, always cleartext, always with a host and a port, and never with
// the path spellings a real caller produces. Those are precisely the inputs the
// splitting functions here get wrong quietly: a host parsed by looking for the
// last colon answers "[" for an IPv6 literal, and nothing downstream of a
// template that echoes request.host can tell that apart from a hostname.

// requestFields returns the `request` map a template sees.
func requestFields(t *testing.T, ctx map[string]any) map[string]any {
	t.Helper()

	req, ok := ctx["request"].(map[string]any)
	if !ok {
		t.Fatalf("the context has no request map: %#v", ctx)
	}
	return req
}

// A host is split at its port, and an IPv6 literal keeps its brackets. The
// bracket test is the one that matters: `hostOnly` looks for the last colon, and
// an address like [::1]:8443 has four of them before the one that separates the
// port. A split at the wrong colon puts "[" or "[::1" into request.host, which
// is a string a template will happily interpolate into a URL nobody can resolve.
func TestHostAndPortAreSplitAtThePortAndNotAtTheLastColon(t *testing.T) {
	cases := []struct {
		host               string
		wantHost, wantPort string
		why                string
	}{
		{"api.example.test:8080", "api.example.test", "8080", "the ordinary case"},
		{"api.example.test", "api.example.test", "", "a host with no port has no port"},
		{"localhost:80", "localhost", "80", "the default port is still written out when it was sent"},
		{"10.0.3.7:9000", "10.0.3.7", "9000", "an IPv4 literal"},
		{"[::1]:8443", "[::1]", "8443", "an IPv6 literal keeps its brackets and loses only the port"},
		{"[2001:db8::1]:443", "[2001:db8::1]", "443", "a longer one, where four colons precede the port"},
		{"[::1]", "[::1]", "", "an IPv6 literal with no port at all keeps every colon"},
		{"[2001:db8::1]", "[2001:db8::1]", "", "and so does a longer one"},
	}

	for _, c := range cases {
		r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/host", nil)
		r.Host = c.host

		req := requestFields(t, BuildContext(r, nil, nil, nil))
		if got := req["host"]; got != c.wantHost {
			t.Errorf("host of %q is %q, want %q (%s)", c.host, got, c.wantHost, c.why)
		}
		if got := req["port"]; got != c.wantPort {
			t.Errorf("port of %q is %q, want %q (%s)", c.host, got, c.wantPort, c.why)
		}
		// baseUrl is built from the unsplit host, so it carries the port back
		// again — a stub writing a Location header needs the authority as it
		// was sent, not the host with the port dropped.
		if got, want := req["baseUrl"], "http://"+c.host; got != want {
			t.Errorf("baseUrl of %q is %q, want %q", c.host, got, want)
		}
	}
}

// The scheme comes off the connection rather than off a header, so a stub
// building an absolute URL cannot be talked into advertising https by a caller
// who merely said so. The corpus runs cleartext throughout, which leaves the TLS
// branch to a unit test or to nothing.
func TestTheSchemeFollowsTheConnectionAndNotAHeader(t *testing.T) {
	plain := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/scheme", nil)
	plain.Host = "api.example.test"
	plain.Header.Set("X-Forwarded-Proto", "https")

	req := requestFields(t, BuildContext(plain, nil, nil, nil))
	if req["scheme"] != "http" {
		t.Errorf("scheme = %v over a cleartext connection carrying X-Forwarded-Proto: https", req["scheme"])
	}
	if req["baseUrl"] != "http://api.example.test" {
		t.Errorf("baseUrl = %v, want the scheme the connection actually had", req["baseUrl"])
	}

	secured := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/scheme", nil)
	secured.Host = "api.example.test"
	secured.TLS = &tls.ConnectionState{}

	req = requestFields(t, BuildContext(secured, nil, nil, nil))
	if req["scheme"] != "https" {
		t.Errorf("scheme = %v over TLS, want https", req["scheme"])
	}
	if req["baseUrl"] != "https://api.example.test" {
		t.Errorf("baseUrl = %v, want the https authority", req["baseUrl"])
	}
}

// `request.clientIp` is the peer address and deliberately not what a forwarding
// header claims. A stub that logged or branched on a client IP taken from
// X-Forwarded-For would be branching on a value the caller chose for itself,
// and the caller below sets every header that would normally be believed.
func TestTheClientIPIsThePeerAndIgnoresForwardingHeaders(t *testing.T) {
	cases := []struct {
		remote string
		want   string
		why    string
	}{
		{"10.0.3.7:54321", "10.0.3.7", "the port is stripped, the address is not"},
		{"127.0.0.1:1", "127.0.0.1", "a low ephemeral port"},
		{"[::1]:54321", "[::1]", "an IPv6 peer keeps its brackets"},
		{"[2001:db8::c0ff:ee]:443", "[2001:db8::c0ff:ee]", "and its inner colons"},
		{"10.0.3.7", "10.0.3.7", "an address with no port is the address"},
	}

	for _, c := range cases {
		r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/ip", nil)
		r.RemoteAddr = c.remote
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Real-Ip", "203.0.113.9")

		req := requestFields(t, BuildContext(r, nil, nil, nil))
		if got := req["clientIp"]; got != c.want {
			t.Errorf("clientIp for peer %q is %q, want %q (%s)", c.remote, got, c.want, c.why)
		}
	}
}

// `request.id` is the caller's correlation id where one was sent and a fresh
// UUID where none was. Both halves matter: a stub echoing the id back is how a
// trace is stitched together, so reusing an id the caller did not send would
// join two unrelated requests, and failing to generate one would leave the
// field empty on every request that omitted the header.
// TestTheRequestIDIsDrawnAndNeverTakenFromTheCaller pins `request.id` as the id
// this server gave the request, which is what WireMock's is.
//
// An inbound X-Request-Id is deliberately ignored. Honouring it reads as a
// courtesy — the mock joining your tracing — but it lets the caller choose what
// a template renders, and a caller can choose a value that collides with
// another request's. `clientIp` was asked the same question and answered the
// same way: the model reports what the server observed, not what the request
// asserted about itself.
func TestTheRequestIDIsDrawnAndNeverTakenFromTheCaller(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	for _, spelling := range []string{"X-Request-Id", "x-request-id"} {
		withHeader := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/id", nil)
		withHeader.Header.Set(spelling, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")

		got, _ := requestFields(t, BuildContext(withHeader, nil, nil, nil))["id"].(string)
		if got == "3f2504e0-4f89-11d3-9a0c-0305e82c3301" {
			t.Errorf("%s: id echoed the caller's header", spelling)
		}
		if !uuid.MatchString(got) {
			t.Errorf("%s: id = %q, want a drawn version 4 UUID", spelling, got)
		}
	}

	bare := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/id", nil)

	first, _ := requestFields(t, BuildContext(bare, nil, nil, nil))["id"].(string)
	second, _ := requestFields(t, BuildContext(bare, nil, nil, nil))["id"].(string)
	if !uuid.MatchString(first) {
		t.Errorf("the drawn id %q is not a version 4 UUID", first)
	}
	// Two requests sharing an id is the failure a per-request draw exists to
	// prevent, and it is invisible in any test that builds one model.
	if first == second {
		t.Errorf("two requests without the header both got id %q", first)
	}
}

// The path splits into segments the way a URL is written rather than the way a
// string is, which is the difference between an empty leading segment and none.
// A root request is the boundary: it has a path and no segments, and code that
// split it naively would hand a template one empty segment to iterate.
func TestThePathSeparatesItsSegmentsWithoutInventingEmptyOnes(t *testing.T) {
	cases := []struct {
		target   string
		path     string
		segments []string
		why      string
	}{
		{"/", "/", nil, "the root path has no segments at all"},
		{"/orders", "/orders", []string{"orders"}, "one segment, no empty one before it"},
		{"/orders/", "/orders/", []string{"orders"}, "a trailing slash does not add an empty segment"},
		{"/a/b/c", "/a/b/c", []string{"a", "b", "c"}, "the ordinary case"},
		{"/a//c", "/a//c", []string{"a", "", "c"}, "an empty segment in the middle was really sent and is kept"},
		{"/orders?x=1", "/orders", []string{"orders"}, "the query is not part of the path"},
		{"/orders?", "/orders", []string{"orders"}, "nor is a question mark with nothing after it"},
		{"/a%20b/c", "/a%20b/c", []string{"a%20b", "c"}, "the path keeps its escapes, unlike the query"},
	}

	for _, c := range cases {
		r := httptest.NewRequestWithContext(context.Background(), "GET", c.target, nil)
		req := requestFields(t, BuildContext(r, nil, nil, nil))

		if got := handlebars.Stringify(req["path"]); got != c.path {
			t.Errorf("path of %q is %q, want %q (%s)", c.target, got, c.path, c.why)
		}
		got, _ := req["pathSegments"].([]string)
		if strings.Join(got, "\x00") != strings.Join(c.segments, "\x00") || len(got) != len(c.segments) {
			t.Errorf("segments of %q are %#v, want %#v (%s)", c.target, got, c.segments, c.why)
		}
		// request.url is the path and the query together, which is what
		// separates it from request.path.
		if got, want := req["url"], c.target; got != want {
			t.Errorf("url of %q is %q, want the target as it was sent", c.target, got)
		}
	}
}

// A urlPathTemplate stub binds its variables into the same node the segments
// live in, so `{{request.path.orderId}}` and `{{request.path.[1]}}` both resolve
// against one value (§10.2). The variables are the half the corpus reaches
// through a registered stub only, which leaves the binding itself untested here
// unless it is exercised directly.
func TestPathTemplateVariablesResolveBesideTheSegmentIndices(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/orders/A-1/items/9", nil)
	ctx := BuildContext(r, nil, map[string]string{"orderId": "A-1", "itemId": "9"}, nil)

	engine := NewEngine(1<<20, nil)
	cases := map[string]string{
		`{{request.path}}`:              "/orders/A-1/items/9",
		`{{request.path.orderId}}`:      "A-1",
		`{{request.path.itemId}}`:       "9",
		`{{request.path.[1]}}`:          "A-1",
		`{{request.path.[3]}}`:          "9",
		`[{{request.path.[9]}}]`:        "[]",
		`[{{request.path.customerId}}]`: "[]",
	}
	for source, want := range cases {
		tpl, err := engine.Compile(source)
		if err != nil {
			t.Fatalf("compile %q: %v", source, err)
		}
		out, err := engine.Render(tpl, ctx)
		if err != nil {
			t.Fatalf("render %q: %v", source, err)
		}
		if out != want {
			t.Errorf("%s = %q, want %q", source, out, want)
		}
	}
}

// The query string is decoded by the model rather than by net/http, so the
// escapes a caller writes are the model's problem. A `+` that stayed a plus and
// a `%2C` that stayed an escape are both values a stub would compare against and
// never match; a malformed escape is the case that must not take the whole
// render down with it.
func TestTheQueryIsDecodedAndAMalformedEscapeIsLeftAsItArrived(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET",
		"/e2e/model/query?plus=a+b&pct=a%20b&comma=x%2Cy&amp=a%26b&bad=%zz&flag&empty=&uni=%E2%9C%93&k%65y=folded", nil)

	engine := NewEngine(1<<20, nil)
	ctx := BuildContext(r, nil, nil, nil)

	cases := []struct {
		source string
		want   string
		why    string
	}{
		{`{{request.query.plus}}`, "a b", "a plus is a space in a query string"},
		{`{{request.query.pct}}`, "a b", "and so is %20"},
		{`{{request.query.comma}}`, "x,y", "an escaped separator decodes to the separator"},
		{`{{request.query.amp}}`, "a&b", "an escaped ampersand does not split the pair"},
		{`{{request.query.uni}}`, "✓", "a multibyte escape decodes to its rune"},
		// An escape that is not one cannot be decoded, and serving the raw text
		// is the only answer that keeps the rest of the query readable.
		{`{{request.query.bad}}`, "%zz", "a malformed escape is left exactly as it arrived"},
		{`[{{request.query.flag}}]`, "[]", "a key with no equals sign is present and empty"},
		{`[{{request.query.empty}}]`, "[]", "and so is one with an equals sign and nothing after"},
		// Presence is what a branch keys on, because `?flag` is a filter the
		// caller asked for even though it carries no value.
		{`{{#if request.query.flag}}on{{else}}off{{/if}}`, "on", "an empty value is still a value that was sent"},
		{`{{#if request.query.absent}}on{{else}}off{{/if}}`, "off", "and a key that was never sent is not"},
		// The name is unescaped too, so a caller who escaped a letter in the
		// key reaches the same parameter.
		{`{{request.query.key}}`, "folded", "an escape in the name decodes before the name is used"},
	}

	for _, c := range cases {
		tpl, err := engine.Compile(c.source)
		if err != nil {
			t.Fatalf("compile %q: %v", c.source, err)
		}
		out, err := engine.Render(tpl, ctx)
		if err != nil {
			t.Fatalf("render %q: %v", c.source, err)
		}
		if out != c.want {
			t.Errorf("%s = %q, want %q (%s)", c.source, out, c.want, c.why)
		}
	}

	// A request with no query at all still has the map, so a template reading
	// one renders nothing rather than failing on a nil lookup.
	bare := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/query", nil)
	query, ok := requestFields(t, BuildContext(bare, nil, nil, nil))["query"].(map[string]any)
	if !ok || len(query) != 0 {
		t.Errorf("a request with no query has query = %#v, want an empty map", query)
	}
}

// Header names resolve under the spelling the wire carried and under a
// lowercased alias, because a template author writes the name as they saw it and
// not as Go canonicalised it. The awkward spellings are the ones worth pinning:
// net/http canonicalises `x-tenant-id` to `X-Tenant-Id`, so a stub written
// against `X-TENANT-ID` reaches the model through neither spelling.
func TestHeadersResolveUnderTheWireSpellingAndItsLowercaseAlias(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/headers", nil)
	r.Header.Set("x-tenant-id", "acme")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("Accept", "application/json")

	req := requestFields(t, BuildContext(r, nil, nil, nil))
	headers, ok := req["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers is %T, want a map", req["headers"])
	}

	for _, name := range []string{"X-Tenant-Id", "x-tenant-id"} {
		if got, ok := headers[name]; !ok || handlebars.Stringify(got) != "acme" {
			t.Errorf("headers[%q] = %v, want acme under both spellings", name, got)
		}
	}
	for _, name := range []string{"Content-Type", "content-type"} {
		if got, ok := headers[name]; !ok || handlebars.Stringify(got) != "application/json" {
			t.Errorf("headers[%q] = %v", name, got)
		}
	}
	// The spelling that is neither the canonical one nor its lowercase is a
	// miss, which renders as nothing rather than failing.
	if _, ok := headers["X-TENANT-ID"]; ok {
		t.Error("headers carries an all-caps alias, which the model does not build")
	}

	// A name carrying no values at all cannot arrive over the wire, but the
	// model is handed an http.Header rather than a request, and a middleware
	// that emptied a key would otherwise leave a node whose first value does
	// not exist — an index panic on the serve path.
	empty := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/headers", nil)
	empty.Header["X-Emptied"] = []string{}
	headers, _ = requestFields(t, BuildContext(empty, nil, nil, nil))["headers"].(map[string]any)
	if got := headers["X-Emptied"]; got != "" {
		t.Errorf("an emptied header is %#v, want the empty string rather than an unindexable node", got)
	}
}

// Cookies are their own node, parsed off the Cookie header, and a request with
// none still has the map. The pair matters together: a template reading
// `{{request.cookies.session}}` on a request that sent no cookies at all must
// render nothing rather than fail.
func TestCookiesAreTheirOwnNodeAndAbsentOnesRenderAsNothing(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/cookies", nil)
	r.Header.Set("Cookie", "session=abc123; tenant=acme; empty=")

	engine := NewEngine(1<<20, nil)
	ctx := BuildContext(r, nil, nil, nil)

	cases := map[string]string{
		`{{request.cookies.session}}`:   "abc123",
		`{{request.cookies.tenant}}`:    "acme",
		`[{{request.cookies.empty}}]`:   "[]",
		`[{{request.cookies.missing}}]`: "[]",
		`{{size request.cookies}}`:      "3",
	}
	for source, want := range cases {
		tpl, err := engine.Compile(source)
		if err != nil {
			t.Fatalf("compile %q: %v", source, err)
		}
		out, err := engine.Render(tpl, ctx)
		if err != nil {
			t.Fatalf("render %q: %v", source, err)
		}
		if out != want {
			t.Errorf("%s = %q, want %q", source, out, want)
		}
	}

	bare := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/cookies", nil)
	cookies, ok := requestFields(t, BuildContext(bare, nil, nil, nil))["cookies"].(map[string]any)
	if !ok || len(cookies) != 0 {
		t.Errorf("a request with no cookies has cookies = %#v, want an empty map", cookies)
	}
}

// `request.bodyAsBase64` is not encoded until a template reads it, because
// encoding on the way in charges every templated response a second copy of the
// body at four-thirds its size whether or not the template mentions it.
//
// The empty body is the exception the deferral has to make: an encoder value
// wrapping no bytes is not one of the shapes Handlebars' emptiness rule knows,
// so it would be truthy, and `{{#if request.bodyAsBase64}}` would take the
// present branch on a request that carried no body at all.
func TestTheBase64BodyIsDeferredAndAnEmptyOneStaysFalsy(t *testing.T) {
	withBody := requestFields(t, BuildContext(
		httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/model/body", nil),
		[]byte(`{"k":"v"}`), nil, nil))

	if _, isString := withBody["bodyAsBase64"].(string); isString {
		t.Error("bodyAsBase64 is already a string, so the encoding was paid for before a template asked for it")
	}
	if got := handlebars.Stringify(withBody["bodyAsBase64"]); got != "eyJrIjoidiJ9" {
		t.Errorf("bodyAsBase64 renders %q, want the encoding of the body", got)
	}
	if !handlebars.Truthy(withBody["bodyAsBase64"]) {
		t.Error("a body that is there is falsy under bodyAsBase64")
	}
	if got := withBody["body"]; got != `{"k":"v"}` {
		t.Errorf("body = %v, want the bytes as they were sent", got)
	}

	empty := requestFields(t, BuildContext(
		httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/model/body", nil),
		nil, nil, nil))

	if got := empty["bodyAsBase64"]; got != "" {
		t.Errorf("the absent body encodes to %#v, want the empty string", got)
	}
	if handlebars.Truthy(empty["bodyAsBase64"]) {
		t.Error("an absent body is truthy under bodyAsBase64, so {{#if}} takes the present branch on nothing")
	}
	if got := empty["body"]; got != "" {
		t.Errorf("body = %v for an absent body, want the empty string", got)
	}

	// A body that is not valid UTF-8 is still a body: the encoded form is the
	// reason bodyAsBase64 exists, and it must survive bytes that request.body
	// cannot represent.
	binary := requestFields(t, BuildContext(
		httptest.NewRequestWithContext(context.Background(), "POST", "/e2e/model/body", nil),
		[]byte{0x00, 0xff, 0xfe}, nil, nil))
	if got := handlebars.Stringify(binary["bodyAsBase64"]); got != "AP/+" {
		t.Errorf("a binary body encodes to %q, want AP/+", got)
	}
}

// `parameters` is the stub's own transformer parameters and it is absent from
// the model entirely when the stub declared none — not an empty map. A template
// reading one either way must render nothing rather than fail, because a stub
// body written for a parameterised variant is often served by a variant that
// sets nothing.
func TestTransformerParametersAreAbsentRatherThanEmptyWhenTheStubSetNone(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/e2e/model/parameters", nil)

	none := BuildContext(r, nil, nil, nil)
	if _, present := none["parameters"]; present {
		t.Error("a stub with no transformer parameters still got a parameters node")
	}

	engine := NewEngine(1<<20, nil)
	tpl, err := engine.Compile(`[{{parameters.tier}}]`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if out, err := engine.Render(tpl, none); err != nil || out != "[]" {
		t.Errorf("reading an absent parameter gave %q (%v), want it to render as nothing", out, err)
	}

	// The values are handed through as they were declared, including the
	// non-string ones a JSON stub definition can carry.
	some := BuildContext(r, nil, nil, map[string]any{
		"tier": "gold", "retries": 3.0, "beta": true,
		"nested": map[string]any{"region": "eu-west-1"},
	})
	cases := map[string]string{
		`{{parameters.tier}}`:               "gold",
		`{{parameters.retries}}`:            "3",
		`{{parameters.beta}}`:               "true",
		`{{parameters.nested.region}}`:      "eu-west-1",
		`{{math parameters.retries '+' 1}}`: "4",
	}
	for source, want := range cases {
		tpl, err := engine.Compile(source)
		if err != nil {
			t.Fatalf("compile %q: %v", source, err)
		}
		out, err := engine.Render(tpl, some)
		if err != nil {
			t.Fatalf("render %q: %v", source, err)
		}
		if out != want {
			t.Errorf("%s = %q, want %q", source, out, want)
		}
	}
}

// The guard on a repeated node that carries no values at all. `headerValues`
// answers the empty string before such a node is built, so BuildContext cannot
// produce one today and no request can reach this — which is the point of
// writing it down. The guard is one compare standing between a future caller
// that builds the node directly and an index panic on the serve path, and a
// guard nothing exercises is a guard the next reader deletes as dead code.
func TestARepeatedNodeWithNoValuesPrintsNothingRatherThanPanicking(t *testing.T) {
	for _, node := range []multiValue{nil, {}} {
		if got := node.String(); got != "" {
			t.Errorf("multiValue(%#v).String() = %q, want the empty string", node, got)
		}
		if got := handlebars.Stringify(node); got != "" {
			t.Errorf("stringifying %#v gave %q, want the empty string", node, got)
		}
		if _, ok := node.Lookup("0"); ok {
			t.Errorf("%#v resolved an index, but it has no values to index", node)
		}
		if got := node.List(); len(got) != 0 {
			t.Errorf("%#v iterates over %d elements, want none", node, len(got))
		}
	}
}

// The method reaches the model unchanged, including the ones a mock server is
// asked to stand in for that no corpus case sends.
func TestTheMethodReachesTheModelAsItWasSent(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "PROPFIND"} {
		r := httptest.NewRequestWithContext(context.Background(), method, "/e2e/model/method", nil)
		if got := requestFields(t, BuildContext(r, nil, nil, nil))["method"]; got != method {
			t.Errorf("method = %v, want %q", got, method)
		}
	}
}
