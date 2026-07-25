// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/b3vet/mockulus/internal/matchers"
)

// URL matching kinds. The kind is resolved once at compile time, so the request
// path never has to work out which URL field a mapping used (SPEC §6.1).
const (
	// URLAny matches every URL, as WireMock's anyUrl() does.
	URLAny uint8 = iota
	// URLExactFull matches path and query byte-exactly, as received — which is
	// why query parameter order matters for this criterion.
	URLExactFull
	// URLExactPath matches the path only, exactly.
	URLExactPath
	// URLPatternFull matches path and query against a regex.
	URLPatternFull
	// URLPatternPath matches the path against a regex.
	URLPatternPath
	// URLTemplate matches the path against a variable-binding template.
	URLTemplate
)

// CompiledStub is the immutable, serve-ready form of a mapping. Snapshots hold
// these and nothing else, so serving is evaluation with no parsing or
// compilation anywhere on the path.
type CompiledStub struct {
	// Raw is the JSON exactly as registered, returned verbatim by GET.
	Raw json.RawMessage

	ID         string
	Name       string
	Priority   int32
	Persistent bool
	Metadata   json.RawMessage
	// Seq is the cluster-global insertion sequence backing newest-wins
	// precedence (SPEC §5.3).
	Seq uint64

	// Method is the HTTP method to match; empty means ANY.
	Method string
	// URLKind selects how the URL criterion is evaluated.
	URLKind uint8
	// URLLiteral is the exact URL or path, or the pattern source.
	URLLiteral string
	// URLRegex is the compiled pattern for the pattern kinds.
	URLRegex matchers.PatternMatcher
	// PathTemplate is the compiled template for the template kind.
	PathTemplate *PathTemplate
	// LiteralPrefix lets the engine skip a pattern candidate without running
	// the pattern at all.
	LiteralPrefix string

	Headers    []KeyCriterion
	Query      []KeyCriterion
	Cookies    []KeyCriterion
	Form       []KeyCriterion
	PathParams []KeyCriterion
	// BasicAuth is the pre-encoded Authorization header value to require.
	BasicAuth string
	// BodyMatchers must all match, and are ordered cheapest first.
	BodyMatchers []matchers.Matcher

	// Scenario is nil for the overwhelming majority of stubs, which is what
	// keeps scenario support free for everyone not using it (P2).
	Scenario *ScenarioRef

	Response CompiledResponse
}

// Fault names, injected below the HTTP layer by hijacking the connection
// (SPEC §12.5).
const (
	FaultConnectionReset = "CONNECTION_RESET_BY_PEER"
	FaultEmptyResponse   = "EMPTY_RESPONSE"
	FaultMalformedChunk  = "MALFORMED_RESPONSE_CHUNK"
	FaultRandomThenClose = "RANDOM_DATA_THEN_CLOSE"
)

// validFaults is the set WireMock defines; anything else is rejected rather
// than served as a normal response.
var validFaults = map[string]bool{
	FaultConnectionReset: true,
	FaultEmptyResponse:   true,
	FaultMalformedChunk:  true,
	FaultRandomThenClose: true,
}

// ResponseTemplateTransformer is the only transformer name mockulus recognises.
const ResponseTemplateTransformer = "response-template"

// DelayKind selects how a random delay is drawn.
type DelayKind uint8

// Delay distribution kinds (SPEC §5.2, §12.4).
const (
	// DelayNone means no distribution was configured.
	DelayNone DelayKind = iota
	// DelayUniform draws uniformly between a lower and upper bound.
	DelayUniform
	// DelayLogNormal draws from a log-normal distribution.
	DelayLogNormal
)

// DelayDistribution is a compiled random-delay specification.
type DelayDistribution struct {
	Kind DelayKind
	// Lower and Upper bound a uniform distribution.
	Lower, Upper time.Duration
	// Median and Sigma parameterise a log-normal distribution.
	Median time.Duration
	Sigma  float64
}

// ChunkedDribble spreads a body across several writes (SPEC §12.6).
type ChunkedDribble struct {
	NumberOfChunks int
	TotalDuration  time.Duration
}

// CompiledResponse is the pre-assembled response of a stub. Serving a static
// stub is a status write, the header writes, and one body write.
type CompiledResponse struct {
	Status int
	// StatusMessage is the HTTP/1.1 reason phrase; HTTP/2 has no such field.
	StatusMessage string
	Headers       []Header
	// Body is the response body in wire form, resolved at compile or snapshot
	// build time so the request path never reads a file (P1).
	Body []byte

	// BodyFileName names a file in the files store. Registering a stub before
	// uploading its file is legal, so an unresolved name is not an error here;
	// it becomes a 500 at serve time until the file appears (SPEC §6.9).
	BodyFileName string
	// BodyFileMissing marks a stub whose file was absent at snapshot build.
	BodyFileMissing bool

	FixedDelay time.Duration
	Delay      DelayDistribution
	Dribble    *ChunkedDribble
	Fault      string

	// Templated records that this stub asked for response templating. The
	// engine that renders it lands in M3; until then the field carries the
	// author's intent through compilation unchanged.
	Templated             bool
	TransformerParameters json.RawMessage
}

// Header is one response header, kept as an ordered pair so repeated names and
// their order survive a round trip.
type Header struct {
	Name  string
	Value string
}

// MatchesMethod reports whether the stub accepts the given method. An empty
// stub method is WireMock's ANY.
func (cs *CompiledStub) MatchesMethod(method string) bool {
	return cs.Method == "" || cs.Method == method
}

// HasCriteriaBeyondURL reports whether anything other than method and URL has
// to be evaluated. The engine uses it to serve the dominant exact-URL case
// without touching headers or the body at all.
func (cs *CompiledStub) HasCriteriaBeyondURL() bool {
	return len(cs.Headers) > 0 || len(cs.Query) > 0 || len(cs.Cookies) > 0 ||
		len(cs.Form) > 0 || len(cs.PathParams) > 0 || len(cs.BodyMatchers) > 0 ||
		cs.BasicAuth != ""
}

// encodeBasicAuth pre-computes the Authorization header value a stub requires,
// so matching is a string comparison rather than a base64 decode per request.
func encodeBasicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// IsTemplated reports whether a value carries template syntax at all. Bodies
// and headers without it are never handed to the template engine, which is what
// makes templating free for stubs that do not use it (SPEC §10.1).
func IsTemplated(s string) bool { return strings.Contains(s, "{{") }
