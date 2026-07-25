// SPDX-License-Identifier: Apache-2.0

// Package stub owns the stub-mapping JSON model, the validation that turns an
// unsupported field into the 422 of SPEC Appendix B, and the compilation of a
// validated mapping into the immutable form the match engine serves from.
//
// Validation is exhaustive by design. A document is walked field by field
// against the support matrix of SPEC §5.2 and every problem is collected, so a
// mapping either registers and then behaves as WireMock would, or is rejected
// naming every offending JSON pointer at once (P3). There is no third outcome:
// mockulus never accepts a stub and quietly ignores part of it.
//
// Everything expensive is resolved here rather than at serve time — regexes and
// path templates compiled, expected JSON parsed, response bodies decoded — so
// the request path only ever evaluates (SPEC §16.3 rule 2).
package stub

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/b3vet/mockulus/internal/handlebars"
	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// DefaultPriority is the effective priority of a mapping that specifies none.
// WireMock treats absent priority as 5.
const DefaultPriority = 5

// Options carry what compilation needs from configuration.
type Options struct {
	// CompileRegex builds patterns for regex criteria, carrying the engine
	// choice, the anchoring policy and the match timeout.
	CompileRegex matchers.RegexCompiler
	// CompileJSONPath builds path evaluators for matchesJsonPath.
	CompileJSONPath matchers.JSONPathCompiler
	// CompileTemplate parses a response template and rejects unknown helpers.
	// Nil disables templating entirely, which is what `templating_enabled: off`
	// means.
	CompileTemplate TemplateCompiler
	// GlobalTemplating forces templating on for every stub, whether or not it
	// declares the transformer.
	//
	// The default is off, because the pinned WireMock requires the per-stub
	// declaration: a stub without it serves `{{request.path}}` literally,
	// verified directly. That makes `wm-compat` the same as off here, and makes
	// a literal `{{` in mock data safe by default (SPEC §10.1).
	GlobalTemplating bool
}

// TemplateCompiler parses a template, returning an error for a parse failure or
// a helper outside the allowlist.
type TemplateCompiler func(source string) (*handlebars.Template, error)

// matcherOptions projects the compile options onto what the matcher package
// needs, so every matcher in the process is built the same way.
//
// allowContent admits the byte-oriented matchers. They compare the subject's
// raw bytes, which only means something where the subject is a body, so a
// criterion over a header or a query parameter is compiled without them.
func (o Options) matcherOptions(allowContent bool) matchers.Options {
	return matchers.Options{
		CompileRegex:         o.CompileRegex,
		CompileJSONPath:      o.CompileJSONPath,
		AllowContentPatterns: allowContent,
	}
}

// supportedTopLevel lists the mapping fields this build accepts.
var supportedTopLevel = map[string]bool{
	"id": true, "uuid": true, "name": true, "priority": true,
	"persistent": true, "metadata": true, "request": true, "response": true,
	"scenarioName": true, "requiredScenarioState": true, "newScenarioState": true,
}

var supportedRequestFields = map[string]bool{
	"method": true, "url": true, "urlPattern": true, "urlPath": true,
	"urlPathPattern": true, "urlPathTemplate": true, "pathParameters": true,
	"queryParameters": true, "headers": true, "cookies": true,
	"formParameters": true, "basicAuthCredentials": true, "bodyPatterns": true,
}

// deferredFields are the fields WireMock defines and this build does not
// implement, mapped to the feature the 422 names so a team learns what it is
// waiting for rather than just "no".
//
// Membership is what separates the two refusals a stub field can earn, so it is
// kept against the pinned version rather than against the roadmap: every field
// listed here registers on WireMock 3.13.2, and every field not listed here and
// not supported is one WireMock itself rejects. The ones with no roadmap entry
// of their own are still WireMock features, and calling them typos would send
// their author looking for a misspelling that is not there.
//
// The rule cuts both ways, which is easy to miss while reading a proxy-mode
// list: `additionalHeaders` sounds like one of these and is not a WireMock
// field at all — ResponseDefinition answers it "Unrecognized field" — so it is
// absent here and earns the schema refusal like any other name nothing defines.
var deferredFields = map[string]string{
	"postServeActions":              "postServeActions (webhooks)",
	"serveEventListeners":           "serveEventListeners",
	"insertionIndex":                "insertionIndex",
	"multipartPatterns":             "multipartPatterns",
	"customMatcher":                 "customMatcher",
	"host":                          "the host request matcher",
	"port":                          "the port request matcher",
	"scheme":                        "the scheme request matcher",
	"proxyBaseUrl":                  "proxyBaseUrl (proxy mode)",
	"additionalProxyRequestHeaders": "additionalProxyRequestHeaders (proxy mode)",
	"removeProxyRequestHeaders":     "removeProxyRequestHeaders (proxy mode)",
	"proxyUrlPrefixToRemove":        "proxyUrlPrefixToRemove (proxy mode)",
	"fromConfiguredStub":            "fromConfiguredStub (proxy mode)",
}

// ScenarioRef is a stub's participation in a scenario (SPEC §9).
type ScenarioRef struct {
	Name string
	// RequiredState gates matching; empty means the stub matches in any state.
	RequiredState string
	// NewState is the state to move to after serving; empty means no transition.
	NewState string
}

// KeyCriterion is one named criterion over a header, query parameter, cookie,
// form field or path variable.
type KeyCriterion struct {
	Name    string
	Matcher matchers.Matcher
}

// Compile decodes, validates and compiles one stub-mapping document.
//
// It returns either a serve-ready stub or every problem found, never both. seq
// is the insertion sequence that fixes this stub's precedence relative to every
// other stub in the cluster (SPEC §5.3).
func Compile(raw []byte, seq uint64, opts Options) (*CompiledStub, *wmcompat.ErrorList) {
	errs := &wmcompat.ErrorList{}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "", "stub mapping is not a JSON object: "+err.Error())
		return nil, errs
	}
	if doc == nil {
		// JSON null decodes into a nil map without error, and would otherwise
		// register as a stub made entirely of defaults — an empty catch-all
		// that matches every request.
		errs.Addf(wmcompat.CodeMalformed, "", "stub mapping is null, not an object")
		return nil, errs
	}

	cs := &CompiledStub{
		Raw:      append(json.RawMessage(nil), raw...),
		Priority: DefaultPriority,
		Seq:      seq,
	}

	for _, field := range sortedKeys(doc) {
		if !supportedTopLevel[field] {
			reportUnsupported(errs, "/"+field, field)
		}
	}

	parseIdentity(errs, doc, cs)
	decodeString(errs, doc, "name", "/name", &cs.Name)
	parsePriority(errs, doc, cs)
	decodeBool(errs, doc, "persistent", "/persistent", &cs.Persistent)
	cs.Metadata = metadataOf(doc["metadata"])
	parseScenario(errs, doc, cs)

	parseRequest(errs, doc["request"], cs, opts)
	parseResponse(errs, doc["response"], cs, opts)

	if !errs.Empty() {
		return nil, errs
	}
	return cs, nil
}

// canonicalUUIDLen is the length of the 8-4-4-4-12 spelling, the one form both
// deserializers agree on.
//
// They disagree in both directions. uuid.Parse additionally takes the
// 32-character dashless, `urn:uuid:`-prefixed and brace-wrapped variants, none
// of which WireMock accepts; WireMock additionally takes a 24-character base64
// encoding of the raw 16 bytes, which uuid.Parse never accepts. Requiring this
// length rejects everything in the first group, so nothing registers here under
// an id WireMock would have refused. The second group is the divergence that
// leaves — SPEC §5.5 deviation 24.
const canonicalUUIDLen = 36

// parseIdentity reads the id and uuid aliases, which WireMock treats as two
// spellings of the same field.
func parseIdentity(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub) {
	for _, field := range []string{"id", "uuid"} {
		v, ok := doc[field]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/"+field, field+" must be a string")
			continue
		}
		if s == "" {
			continue
		}
		parsed, err := uuid.Parse(s)
		if err != nil || len(s) != canonicalUUIDLen {
			// WireMock deserializes this field as a UUID, so a non-UUID id is
			// rejected there too. Accepting one here would let a stub register
			// that could never be migrated back — and accepting a spelling
			// mockulus would rewrite silently hands the client back an id it
			// did not choose.
			errs.Addf(wmcompat.CodeMalformed, "/"+field,
				field+" must be a canonical 36-character UUID")
			continue
		}
		// UUID parsing is hex-case-insensitive and canonicalises to lower case,
		// so two spellings of one id resolve to the same stub.
		canonical := parsed.String()
		if cs.ID != "" && cs.ID != canonical {
			errs.Addf(wmcompat.CodeMalformed, "/"+field,
				"id and uuid are aliases and must not disagree")
			continue
		}
		cs.ID = canonical
	}
}

// Metadata returns a stored mapping document's metadata, or nil when the stub
// has none. It is how the admin metadata endpoints read a document they have
// not compiled, so that one rule decides what "has metadata" means.
func Metadata(raw []byte) json.RawMessage {
	var doc struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return metadataOf(doc.Metadata)
}

// metadataOf normalises the `metadata` field, treating an explicit null as no
// metadata at all.
//
// WireMock drops a null on deserialization and reports the stub as untagged, so
// this is its behavior — and it is what SPEC §5.5 deviation 20 rests on: only
// stubs that HAVE metadata are candidates for find/remove-by-metadata. A stub
// spelling its absence out, which is exactly what a document round-tripped
// through WireMock does, must not be reachable by a broad cleanup matcher when
// its untagged neighbour is not. The document itself is unchanged: a GET still
// returns the field as it was registered.
func metadataOf(raw json.RawMessage) json.RawMessage {
	if string(raw) == "null" {
		return nil
	}
	return raw
}

func parsePriority(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub) {
	v, ok := doc["priority"]
	if !ok {
		return
	}
	var p int32
	if err := json.Unmarshal(v, &p); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/priority", "priority must be an integer")
		return
	}
	// Priority is an arbitrary signed integer compared numerically, with no
	// clamping — verified against the pinned WireMock, which accepts 0, -1 and
	// int32 max and sorts them purely by value. The common "1 is the highest"
	// phrasing describes convention, not a constraint, and rejecting a stub
	// WireMock accepts would be a compatibility break.
	cs.Priority = p
}

func parseScenario(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub) {
	var name, required, next string
	decodeString(errs, doc, "scenarioName", "/scenarioName", &name)
	decodeString(errs, doc, "requiredScenarioState", "/requiredScenarioState", &required)
	decodeString(errs, doc, "newScenarioState", "/newScenarioState", &next)

	if name == "" {
		// The state fields are meaningless without a scenario, and silently
		// ignoring them would make a stub behave unlike the author expects.
		if required != "" {
			errs.Addf(wmcompat.CodeMalformed, "/requiredScenarioState",
				"requiredScenarioState needs a scenarioName")
		}
		if next != "" {
			errs.Addf(wmcompat.CodeMalformed, "/newScenarioState",
				"newScenarioState needs a scenarioName")
		}
		return
	}
	cs.Scenario = &ScenarioRef{Name: name, RequiredState: required, NewState: next}
}

func parseRequest(errs *wmcompat.ErrorList, raw json.RawMessage, cs *CompiledStub, opts Options) {
	// An absent request object matches everything, as WireMock's anyUrl() does.
	cs.URLKind = URLAny
	if len(raw) == 0 {
		return
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/request", "request must be a JSON object")
		return
	}
	for _, field := range sortedKeys(doc) {
		if !supportedRequestFields[field] {
			reportUnsupported(errs, "/request/"+field, field)
		}
	}

	parseMethod(errs, doc, cs)
	parseURL(errs, doc, cs, opts)

	cs.Headers = parseKeyCriteria(errs, doc, "headers", "/request/headers", opts)
	cs.Query = parseKeyCriteria(errs, doc, "queryParameters", "/request/queryParameters", opts)
	cs.Cookies = parseKeyCriteria(errs, doc, "cookies", "/request/cookies", opts)
	cs.Form = parseKeyCriteria(errs, doc, "formParameters", "/request/formParameters", opts)
	cs.PathParams = parseKeyCriteria(errs, doc, "pathParameters", "/request/pathParameters", opts)

	parseBasicAuth(errs, doc, cs)
	parseBodyPatterns(errs, doc, cs, opts)

	if len(cs.PathParams) > 0 && cs.PathTemplate == nil {
		errs.Addf(wmcompat.CodeMalformed, "/request/pathParameters",
			"pathParameters needs a urlPathTemplate to bind against")
	}
	if cs.PathTemplate != nil {
		validatePathParamNames(errs, cs)
	}
}

func parseMethod(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub) {
	var method string
	decodeString(errs, doc, "method", "/request/method", &method)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "ANY" {
		// ANY is a wildcard, represented as no method criterion at all.
		method = ""
	}
	cs.Method = method
}

// urlFields are the five mutually exclusive ways to state a URL criterion.
var urlFields = []struct {
	field string
	kind  uint8
}{
	{"url", URLExactFull},
	{"urlPath", URLExactPath},
	{"urlPattern", URLPatternFull},
	{"urlPathPattern", URLPatternPath},
	{"urlPathTemplate", URLTemplate},
}

func parseURL(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub, opts Options) {
	var (
		found []string
		kind  uint8 = URLAny
		value string
		field string
	)
	for _, uf := range urlFields {
		raw, ok := doc[uf.field]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/request/"+uf.field, uf.field+" must be a string")
			continue
		}
		found = append(found, uf.field)
		kind, value, field = uf.kind, s, uf.field
	}

	if len(found) > 1 {
		errs.Addf(wmcompat.CodeMalformed, "/request",
			"only one URL criterion may be given, found "+strings.Join(found, ", "))
		return
	}
	if len(found) == 0 {
		cs.URLKind = URLAny
		return
	}

	cs.URLKind = kind
	cs.URLLiteral = value

	switch kind {
	case URLExactFull, URLExactPath:
		if !strings.HasPrefix(value, "/") {
			errs.Addf(wmcompat.CodeMalformed, "/request/"+field, field+" must start with /")
		}

	case URLPatternFull, URLPatternPath:
		if opts.CompileRegex == nil {
			errs.Addf(wmcompat.CodeRegex, "/request/"+field, "no regex engine is configured")
			return
		}
		pattern, err := opts.CompileRegex(value)
		if err != nil {
			errs.Addf(wmcompat.CodeRegex, "/request/"+field,
				field+" does not compile: "+err.Error())
			return
		}
		cs.URLRegex = pattern
		cs.LiteralPrefix = literalPrefixOf(pattern)

	case URLTemplate:
		tpl, err := ParsePathTemplate(value)
		if err != nil {
			errs.Addf(wmcompat.CodeMalformed, "/request/"+field, err.Error())
			return
		}
		cs.PathTemplate = tpl
		cs.LiteralPrefix = tpl.LiteralPrefix()
	}
}

// prefixer is implemented by compiled patterns that can report a literal
// prefix; declared at the point of use so this package does not depend on the
// regex engine seam.
type prefixer interface{ LiteralPrefix() string }

func literalPrefixOf(p matchers.PatternMatcher) string {
	if lp, ok := p.(prefixer); ok {
		return lp.LiteralPrefix()
	}
	return ""
}

// validatePathParamNames rejects a criterion naming a variable the template
// does not bind, which would otherwise be a criterion that can never match.
func validatePathParamNames(errs *wmcompat.ErrorList, cs *CompiledStub) {
	bound := make(map[string]bool, len(cs.PathTemplate.Vars()))
	for _, v := range cs.PathTemplate.Vars() {
		bound[v] = true
	}
	for _, c := range cs.PathParams {
		if !bound[c.Name] {
			errs.Addf(wmcompat.CodeMalformed, "/request/pathParameters/"+c.Name,
				fmt.Sprintf("the template %q binds no variable named %q",
					cs.PathTemplate.Source, c.Name))
		}
	}
}

func parseKeyCriteria(errs *wmcompat.ErrorList, doc map[string]json.RawMessage,
	field, pointer string, opts Options) []KeyCriterion {

	raw, ok := doc[field]
	if !ok {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		errs.Addf(wmcompat.CodeMalformed, pointer, field+" must be a JSON object")
		return nil
	}

	out := make([]KeyCriterion, 0, len(entries))
	for _, name := range sortedKeys(entries) {
		m, problems := matchers.Compile(entries[name], pointer+"/"+name, opts.matcherOptions(false))
		if len(problems) > 0 {
			addMatcherProblems(errs, problems)
			continue
		}
		out = append(out, KeyCriterion{Name: name, Matcher: m})
	}
	return out
}

func parseBasicAuth(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, cs *CompiledStub) {
	raw, ok := doc["basicAuthCredentials"]
	if !ok {
		return
	}
	var creds struct {
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/request/basicAuthCredentials",
			"basicAuthCredentials must be an object with username and password")
		return
	}
	if creds.Username == nil || creds.Password == nil {
		errs.Addf(wmcompat.CodeMalformed, "/request/basicAuthCredentials",
			"basicAuthCredentials needs both username and password")
		return
	}
	cs.BasicAuth = encodeBasicAuth(*creds.Username, *creds.Password)
}

func parseBodyPatterns(errs *wmcompat.ErrorList, doc map[string]json.RawMessage,
	cs *CompiledStub, opts Options) {

	raw, ok := doc["bodyPatterns"]
	if !ok {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		errs.Addf(wmcompat.CodeMalformed, "/request/bodyPatterns",
			"bodyPatterns must be an array of matchers")
		return
	}

	for i, item := range items {
		m, problems := matchers.Compile(item, fmt.Sprintf("/request/bodyPatterns/%d", i),
			opts.matcherOptions(true))
		if len(problems) > 0 {
			addMatcherProblems(errs, problems)
			continue
		}
		cs.BodyMatchers = append(cs.BodyMatchers, m)
	}

	// Evaluation order is chosen once, here: cheap comparisons before regex
	// before anything that has to parse the body (SPEC §6.5).
	sort.SliceStable(cs.BodyMatchers, func(i, j int) bool {
		return matcherCost(cs.BodyMatchers[i]) < matcherCost(cs.BodyMatchers[j])
	})
}

// matcherCost ranks matchers by how much work evaluating one costs, so the
// cheapest criterion gets the chance to reject the candidate first.
func matcherCost(m matchers.Matcher) int {
	switch t := m.(type) {
	case *matchers.EqualTo, *matchers.BinaryEqualTo, *matchers.Absent:
		return 0
	case *matchers.Contains:
		return 1
	case *matchers.Regex:
		return 2
	case *matchers.EqualToJSON:
		return 3
	case *matchers.Not:
		return matcherCost(t.Matcher)
	case *matchers.And:
		return maxCost(t.Matchers)
	case *matchers.Or:
		return maxCost(t.Matchers)
	default:
		return 4
	}
}

func maxCost(ms []matchers.Matcher) int {
	worst := 0
	for _, m := range ms {
		if c := matcherCost(m); c > worst {
			worst = c
		}
	}
	return worst
}

// addMatcherProblems maps matcher-compilation problems onto the error catalog.
//
// The mapping is on the problem's kind rather than its text: an expression that
// "does not compile" could be a regex or a JSONPath, and reporting a bad
// JSONPath as an invalid regular expression sends the author looking in the
// wrong place.
func addMatcherProblems(errs *wmcompat.ErrorList, problems []matchers.Problem) {
	for _, p := range problems {
		switch p.Kind {
		case matchers.ProblemDeferred:
			errs.Unsupported(p.Pointer, p.Feature)
		case matchers.ProblemRegex:
			errs.Addf(wmcompat.CodeRegex, p.Pointer, p.Detail)
		default:
			errs.Addf(wmcompat.CodeMalformed, p.Pointer, p.Detail)
		}
	}
}

// reportUnsupported refuses a field this build does not serve, choosing between
// the two refusals by whether the field is a feature at all.
//
// A deferred WireMock feature earns 1000 and a pointer at the roadmap: the
// document is well-formed, mockulus is behind, and the author wants to know
// when it catches up. A name that is in neither vocabulary earns 10, because it
// is a schema violation and nothing more — WireMock answers a typo'd field with
// exactly that code and pointer, and sending its author to the roadmap to look
// for `postServeActionz` would waste the trip.
func reportUnsupported(errs *wmcompat.ErrorList, pointer, field string) {
	if feature, deferred := deferredFields[field]; deferred {
		errs.Unsupported(pointer, feature)
		return
	}
	errs.Addf(wmcompat.CodeMalformed, pointer, "unknown field "+field)
}

func decodeString(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, field, pointer string, dst *string) {
	v, ok := doc[field]
	if !ok {
		return
	}
	if err := json.Unmarshal(v, dst); err != nil {
		errs.Addf(wmcompat.CodeMalformed, pointer, field+" must be a string")
	}
}

func decodeBool(errs *wmcompat.ErrorList, doc map[string]json.RawMessage, field, pointer string, dst *bool) {
	v, ok := doc[field]
	if !ok {
		return
	}
	if err := json.Unmarshal(v, dst); err != nil {
		errs.Addf(wmcompat.CodeMalformed, pointer, field+" must be a boolean")
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WithIdentity returns the mapping document with its `id` and `uuid` fields set
// to the given identifier, which is what a GET must return once the server has
// assigned one. The rest of the document is preserved as registered.
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
