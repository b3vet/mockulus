// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"strings"
	"testing"

	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/regexx"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

func testOptions() Options {
	return Options{
		CompileRegex: func(pattern string) (matchers.PatternMatcher, error) {
			return regexx.Compile(pattern, regexx.Options{Anchored: true})
		},
	}
}

func compileOK(t *testing.T, doc string) *CompiledStub {
	t.Helper()
	cs, errs := Compile([]byte(doc), 1, testOptions())
	if errs != nil {
		t.Fatalf("should compile: %s\nproblems: %v", doc, errs.Errors())
	}
	return cs
}

// compileErrs expects rejection and returns the problems.
func compileErrs(t *testing.T, doc string) []wmcompat.Error {
	t.Helper()
	cs, errs := Compile([]byte(doc), 1, testOptions())
	if errs == nil {
		t.Fatalf("should have been rejected: %s", doc)
	}
	if cs != nil {
		t.Errorf("a rejected mapping must not also produce a stub")
	}
	return errs.Errors()
}

// hasProblem reports whether any problem carries the code and mentions the
// pointer, which is what a caller fixing their stub actually reads.
func hasProblem(problems []wmcompat.Error, code int, pointer string) bool {
	for _, p := range problems {
		if p.Code != code {
			continue
		}
		if pointer == "" {
			return true
		}
		if p.Source != nil && p.Source.Pointer == pointer {
			return true
		}
	}
	return false
}

func TestMinimalMapping(t *testing.T) {
	cs := compileOK(t, `{"request":{"method":"GET","urlPath":"/x"},"response":{"status":200}}`)

	if cs.Method != "GET" || cs.URLKind != URLExactPath || cs.URLLiteral != "/x" {
		t.Errorf("request compiled to method=%q kind=%d literal=%q", cs.Method, cs.URLKind, cs.URLLiteral)
	}
	if cs.Response.Status != 200 {
		t.Errorf("status = %d", cs.Response.Status)
	}
	if cs.Priority != DefaultPriority {
		t.Errorf("priority = %d, want the default %d", cs.Priority, DefaultPriority)
	}
}

func TestResponseDefaultsToStatus200(t *testing.T) {
	cs := compileOK(t, `{"request":{"urlPath":"/x"},"response":{"body":"hi"}}`)
	if cs.Response.Status != 200 {
		t.Errorf("an omitted status should default to 200, got %d", cs.Response.Status)
	}
	// A mapping with no response object at all is still servable.
	cs = compileOK(t, `{"request":{"urlPath":"/x"}}`)
	if cs.Response.Status != 200 {
		t.Errorf("an absent response should default to 200, got %d", cs.Response.Status)
	}
}

func TestIDAndUUIDAreAliases(t *testing.T) {
	const id = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	const other = "3f2504e0-4f89-41d3-9a0c-0305e82c3302"

	cs := compileOK(t, `{"id":"`+id+`","request":{"urlPath":"/x"}}`)
	if cs.ID != id {
		t.Errorf("id = %q", cs.ID)
	}
	cs = compileOK(t, `{"uuid":"`+id+`","request":{"urlPath":"/x"}}`)
	if cs.ID != id {
		t.Errorf("uuid should populate the same field, got %q", cs.ID)
	}
	cs = compileOK(t, `{"id":"`+id+`","uuid":"`+id+`","request":{"urlPath":"/x"}}`)
	if cs.ID != id {
		t.Errorf("agreeing aliases should be accepted, got %q", cs.ID)
	}

	// Disagreeing aliases are ambiguous, and guessing which one the author
	// meant would silently register a stub under an id they cannot predict.
	problems := compileErrs(t, `{"id":"`+id+`","uuid":"`+other+`","request":{"urlPath":"/x"}}`)
	if !hasProblem(problems, wmcompat.CodeMalformed, "") {
		t.Errorf("disagreeing id and uuid should be rejected, got %v", problems)
	}
}

// A stub id is deserialized as a UUID by WireMock, so an arbitrary string is
// rejected there and must be rejected here. Parsing is hex-case-insensitive and
// canonicalises to lower case, so two spellings name one stub.
func TestStubIDMustBeAUUID(t *testing.T) {
	for _, bad := range []string{"abc", "not-a-uuid", "12345", "3f2504e0-4f89-41d3-9a0c"} {
		problems := compileErrs(t, `{"id":"`+bad+`","request":{"urlPath":"/x"}}`)
		if !hasProblem(problems, wmcompat.CodeMalformed, "/id") {
			t.Errorf("id %q should be rejected, got %v", bad, problems)
		}
	}

	cs := compileOK(t, `{"id":"3F2504E0-4F89-41D3-9A0C-0305E82C3301","request":{"urlPath":"/x"}}`)
	if cs.ID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" {
		t.Errorf("an upper-case id should canonicalise to lower case, got %q", cs.ID)
	}
}

func TestURLCriteriaAreMutuallyExclusive(t *testing.T) {
	problems := compileErrs(t, `{"request":{"url":"/a","urlPath":"/a"}}`)
	if !hasProblem(problems, wmcompat.CodeMalformed, "/request") {
		t.Errorf("two URL criteria should be rejected, got %v", problems)
	}

	problems = compileErrs(t, `{"request":{"urlPattern":"/a.*","urlPathTemplate":"/a/{id}"}}`)
	if len(problems) == 0 {
		t.Error("two URL criteria of any kind should be rejected")
	}
}

func TestEachURLKindCompiles(t *testing.T) {
	cases := map[string]uint8{
		`{"request":{"url":"/a?b=1"}}`:               URLExactFull,
		`{"request":{"urlPath":"/a"}}`:               URLExactPath,
		`{"request":{"urlPattern":"/a/[0-9]+"}}`:     URLPatternFull,
		`{"request":{"urlPathPattern":"/a/[0-9]+"}}`: URLPatternPath,
		`{"request":{"urlPathTemplate":"/a/{id}"}}`:  URLTemplate,
		`{"request":{"method":"GET"}}`:               URLAny,
	}
	for doc, want := range cases {
		cs := compileOK(t, doc)
		if cs.URLKind != want {
			t.Errorf("%s compiled to kind %d, want %d", doc, cs.URLKind, want)
		}
	}
}

func TestURLMustStartWithSlash(t *testing.T) {
	for _, doc := range []string{
		`{"request":{"url":"api/x"}}`,
		`{"request":{"urlPath":"api/x"}}`,
	} {
		problems := compileErrs(t, doc)
		if !hasProblem(problems, wmcompat.CodeMalformed, "") {
			t.Errorf("%s should be rejected: a URL criterion must be a path", doc)
		}
	}
}

// A pattern that does not compile is rejected at registration, so it can never
// become a stub that silently never matches.
func TestUncompilableRegexIsRejected(t *testing.T) {
	problems := compileErrs(t, `{"request":{"urlPattern":"/a/(unclosed"}}`)
	if !hasProblem(problems, wmcompat.CodeRegex, "/request/urlPattern") {
		t.Errorf("an invalid urlPattern should be code %d, got %v", wmcompat.CodeRegex, problems)
	}

	problems = compileErrs(t, `{"request":{"urlPath":"/a","headers":{"X":{"matches":"(unclosed"}}}}`)
	if !hasProblem(problems, wmcompat.CodeRegex, "/request/headers/X/matches") {
		t.Errorf("an invalid header regex should be code %d with a pointer, got %v",
			wmcompat.CodeRegex, problems)
	}
}

func TestDeferredFeaturesAreRejectedWithPointers(t *testing.T) {
	cases := []struct {
		doc     string
		pointer string
	}{
		{`{"postServeActions":[{"name":"webhook"}],"request":{"urlPath":"/x"}}`, "/postServeActions"},
		{`{"request":{"urlPath":"/x","multipartPatterns":[{}]}}`, "/request/multipartPatterns"},
		{`{"request":{"urlPath":"/x","customMatcher":{"name":"x"}}}`, "/request/customMatcher"},
		{`{"request":{"urlPath":"/x"},"response":{"proxyBaseUrl":"http://x"}}`, "/response/proxyBaseUrl"},
		{`{"request":{"urlPath":"/x","bodyPatterns":[{"matchesXPath":"//a"}]}}`,
			"/request/bodyPatterns/0/matchesXPath"},
		{`{"request":{"urlPath":"/x","bodyPatterns":[{"equalToXml":"<a/>"}]}}`,
			"/request/bodyPatterns/0/equalToXml"},
		{`{"request":{"urlPath":"/x","bodyPatterns":[{"matchesJsonSchema":{}}]}}`,
			"/request/bodyPatterns/0/matchesJsonSchema"},
	}
	for _, c := range cases {
		problems := compileErrs(t, c.doc)
		if !hasProblem(problems, wmcompat.CodeUnsupportedFeature, c.pointer) {
			t.Errorf("%s should be code %d at %s, got %v",
				c.doc, wmcompat.CodeUnsupportedFeature, c.pointer, problems)
		}
		// The detail has to name where the feature stands, or a team hitting it
		// has no idea whether to wait or work around it.
		for _, p := range problems {
			if p.Code == wmcompat.CodeUnsupportedFeature && !strings.Contains(p.Detail, "ROADMAP") {
				t.Errorf("%s: detail should point at the roadmap, got %q", c.doc, p.Detail)
			}
		}
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	for _, c := range []struct{ doc, pointer string }{
		{`{"nonsense":1,"request":{"urlPath":"/x"}}`, "/nonsense"},
		{`{"request":{"urlPath":"/x","nonsense":1}}`, "/request/nonsense"},
		{`{"request":{"urlPath":"/x"},"response":{"nonsense":1}}`, "/response/nonsense"},
	} {
		problems := compileErrs(t, c.doc)
		if !hasProblem(problems, wmcompat.CodeUnsupportedFeature, c.pointer) {
			t.Errorf("%s should name %s, got %v", c.doc, c.pointer, problems)
		}
	}
}

// Every problem in one document is reported together, so a CI user fixes them
// all in one round rather than one restart per mistake (SPEC Appendix B).
func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	problems := compileErrs(t, `{
		"postServeActions": [{"name":"a"}],
		"request": {"urlPath":"/x","multipartPatterns":[{}],"bodyPatterns":[{"matchesXPath":"//a"}]},
		"response": {"proxyBaseUrl":"http://x","status":999}
	}`)
	if len(problems) < 5 {
		t.Fatalf("expected every problem at once, got %d: %v", len(problems), problems)
	}
}

// Priority is an arbitrary signed integer compared numerically. WireMock
// accepts 0, negatives and int32 max and sorts them purely by value, so
// rejecting any of them would refuse a stub WireMock takes — the "1 is the
// highest" phrasing in the documentation is convention, not a constraint.
func TestPriorityAcceptsAnySignedInteger(t *testing.T) {
	for _, c := range []struct {
		doc  string
		want int32
	}{
		{`{"priority":1,"request":{}}`, 1},
		{`{"priority":0,"request":{}}`, 0},
		{`{"priority":-1,"request":{}}`, -1},
		{`{"priority":2147483647,"request":{}}`, 2147483647},
		{`{"request":{}}`, DefaultPriority},
	} {
		if got := compileOK(t, c.doc).Priority; got != c.want {
			t.Errorf("%s: priority = %d, want %d", c.doc, got, c.want)
		}
	}

	if problems := compileErrs(t, `{"priority":"high","request":{}}`); !hasProblem(problems, wmcompat.CodeMalformed, "/priority") {
		t.Errorf("a non-integer priority should still be rejected, got %v", problems)
	}
}

func TestScenarioFields(t *testing.T) {
	cs := compileOK(t, `{"scenarioName":"flow","requiredScenarioState":"Started",
		"newScenarioState":"next","request":{"urlPath":"/x"}}`)
	if cs.Scenario == nil {
		t.Fatal("scenario should be compiled")
	}
	if cs.Scenario.Name != "flow" || cs.Scenario.RequiredState != "Started" || cs.Scenario.NewState != "next" {
		t.Errorf("scenario = %+v", *cs.Scenario)
	}

	// A stub not in a scenario has no scenario at all, which is what keeps
	// scenario support free for everyone else.
	cs = compileOK(t, `{"request":{"urlPath":"/x"}}`)
	if cs.Scenario != nil {
		t.Error("a stub without scenarioName should carry no scenario")
	}

	// State without a scenario name is meaningless; ignoring it would make the
	// stub behave unlike the author expects.
	for _, doc := range []string{
		`{"requiredScenarioState":"Started","request":{}}`,
		`{"newScenarioState":"next","request":{}}`,
	} {
		if problems := compileErrs(t, doc); len(problems) == 0 {
			t.Errorf("%s should be rejected", doc)
		}
	}
}

func TestBodyFormsAreMutuallyExclusive(t *testing.T) {
	problems := compileErrs(t, `{"request":{},"response":{"body":"a","jsonBody":{"b":1}}}`)
	if !hasProblem(problems, wmcompat.CodeMalformed, "/response") {
		t.Errorf("two body forms should be rejected, got %v", problems)
	}
}

func TestBodyForms(t *testing.T) {
	if got := string(compileOK(t, `{"response":{"body":"hello"}}`).Response.Body); got != "hello" {
		t.Errorf("body = %q", got)
	}
	if got := string(compileOK(t, `{"response":{"jsonBody":{"a":1}}}`).Response.Body); got != `{"a":1}` {
		t.Errorf("jsonBody should be served as written, got %q", got)
	}
	// "hello" base64-encoded.
	if got := string(compileOK(t, `{"response":{"base64Body":"aGVsbG8="}}`).Response.Body); got != "hello" {
		t.Errorf("base64Body = %q", got)
	}

	// A bodyFileName is NOT resolved at registration: registering a stub before
	// uploading its file is legal (SPEC §4.3).
	cs := compileOK(t, `{"response":{"bodyFileName":"greeting.txt"}}`)
	if cs.Response.BodyFileName != "greeting.txt" {
		t.Errorf("bodyFileName = %q", cs.Response.BodyFileName)
	}
	if len(cs.Response.Body) != 0 {
		t.Error("bodyFileName must not resolve at registration time")
	}

	if problems := compileErrs(t, `{"response":{"base64Body":"not!valid!"}}`); len(problems) == 0 {
		t.Error("invalid base64 should be rejected")
	}
}

func TestStatusValidation(t *testing.T) {
	for _, doc := range []string{
		`{"response":{"status":99}}`,
		`{"response":{"status":600}}`,
		`{"response":{"status":"200"}}`,
	} {
		if problems := compileErrs(t, doc); !hasProblem(problems, wmcompat.CodeMalformed, "/response/status") {
			t.Errorf("%s should be rejected, got %v", doc, problems)
		}
	}
}

func TestDelayParsing(t *testing.T) {
	cs := compileOK(t, `{"response":{"fixedDelayMilliseconds":250}}`)
	if cs.Response.FixedDelay.Milliseconds() != 250 {
		t.Errorf("fixed delay = %s", cs.Response.FixedDelay)
	}

	cs = compileOK(t, `{"response":{"delayDistribution":{"type":"uniform","lower":100,"upper":300}}}`)
	if cs.Response.Delay.Kind != DelayUniform || cs.Response.Delay.Upper.Milliseconds() != 300 {
		t.Errorf("uniform delay = %+v", cs.Response.Delay)
	}

	cs = compileOK(t, `{"response":{"delayDistribution":{"type":"lognormal","median":80,"sigma":0.4}}}`)
	if cs.Response.Delay.Kind != DelayLogNormal || cs.Response.Delay.Sigma != 0.4 {
		t.Errorf("lognormal delay = %+v", cs.Response.Delay)
	}

	for _, doc := range []string{
		`{"response":{"fixedDelayMilliseconds":-1}}`,
		`{"response":{"delayDistribution":{"type":"uniform","lower":300,"upper":100}}}`,
		`{"response":{"delayDistribution":{"type":"uniform","lower":100}}}`,
		`{"response":{"delayDistribution":{"type":"weibull","lower":1,"upper":2}}}`,
		`{"response":{"delayDistribution":{"median":80,"sigma":0.4}}}`,
	} {
		if problems := compileErrs(t, doc); len(problems) == 0 {
			t.Errorf("%s should be rejected", doc)
		}
	}
}

func TestChunkedDribbleValidation(t *testing.T) {
	cs := compileOK(t, `{"response":{"body":"abcdef","chunkedDribbleDelay":{"numberOfChunks":3,"totalDuration":300}}}`)
	if cs.Response.Dribble == nil || cs.Response.Dribble.NumberOfChunks != 3 {
		t.Errorf("dribble = %+v", cs.Response.Dribble)
	}

	for _, doc := range []string{
		`{"response":{"chunkedDribbleDelay":{"numberOfChunks":0,"totalDuration":100}}}`,
		`{"response":{"chunkedDribbleDelay":{"numberOfChunks":3}}}`,
		`{"response":{"chunkedDribbleDelay":{"numberOfChunks":3,"totalDuration":-1}}}`,
	} {
		if problems := compileErrs(t, doc); len(problems) == 0 {
			t.Errorf("%s should be rejected", doc)
		}
	}
}

func TestFaultValidation(t *testing.T) {
	for _, fault := range []string{
		FaultConnectionReset, FaultEmptyResponse, FaultMalformedChunk, FaultRandomThenClose,
	} {
		cs := compileOK(t, `{"response":{"fault":"`+fault+`"}}`)
		if cs.Response.Fault != fault {
			t.Errorf("fault = %q, want %q", cs.Response.Fault, fault)
		}
	}

	// An unknown fault would otherwise be silently served as a normal response.
	if problems := compileErrs(t, `{"response":{"fault":"KABOOM"}}`); !hasProblem(problems, wmcompat.CodeMalformed, "/response/fault") {
		t.Errorf("an unknown fault should be rejected, got %v", problems)
	}

	// A fault replaces the response, so asking for both states two intents.
	if problems := compileErrs(t, `{"response":{"fault":"EMPTY_RESPONSE","body":"x"}}`); len(problems) == 0 {
		t.Error("a fault combined with a body should be rejected")
	}
}

func TestTransformerValidation(t *testing.T) {
	cs := compileOK(t, `{"response":{"transformers":["response-template"],"body":"{{now}}"}}`)
	if !cs.Response.Templated {
		t.Error("response-template should mark the response as templated")
	}

	// An unrecognised transformer would silently do nothing.
	problems := compileErrs(t, `{"response":{"transformers":["my-transformer"]}}`)
	if !hasProblem(problems, wmcompat.CodeUnknownTransformer, "/response/transformers/0") {
		t.Errorf("an unknown transformer should be code %d, got %v",
			wmcompat.CodeUnknownTransformer, problems)
	}
}

func TestResponseHeadersAcceptSingleAndMultipleValues(t *testing.T) {
	cs := compileOK(t, `{"response":{"headers":{"Content-Type":"application/json"}}}`)
	if len(cs.Response.Headers) != 1 || cs.Response.Headers[0].Value != "application/json" {
		t.Errorf("headers = %+v", cs.Response.Headers)
	}

	cs = compileOK(t, `{"response":{"headers":{"Set-Cookie":["a=1","b=2"]}}}`)
	if len(cs.Response.Headers) != 2 {
		t.Fatalf("an array header should produce one entry per value, got %+v", cs.Response.Headers)
	}
	for _, h := range cs.Response.Headers {
		if h.Name != "Set-Cookie" {
			t.Errorf("header name = %q", h.Name)
		}
	}
}

func TestPathParametersRequireATemplate(t *testing.T) {
	problems := compileErrs(t, `{"request":{"urlPath":"/x","pathParameters":{"id":{"equalTo":"1"}}}}`)
	if !hasProblem(problems, wmcompat.CodeMalformed, "/request/pathParameters") {
		t.Errorf("pathParameters without a template should be rejected, got %v", problems)
	}

	// A criterion naming a variable the template does not bind can never match,
	// so it is a mistake rather than a criterion.
	problems = compileErrs(t, `{"request":{"urlPathTemplate":"/x/{id}",
		"pathParameters":{"other":{"equalTo":"1"}}}}`)
	if !hasProblem(problems, wmcompat.CodeMalformed, "/request/pathParameters/other") {
		t.Errorf("an unbound path parameter should be rejected, got %v", problems)
	}
}

func TestBasicAuthCompilesToAHeaderValue(t *testing.T) {
	cs := compileOK(t, `{"request":{"basicAuthCredentials":{"username":"user","password":"pass"}}}`)
	if cs.BasicAuth != "Basic dXNlcjpwYXNz" {
		t.Errorf("basic auth = %q", cs.BasicAuth)
	}

	if problems := compileErrs(t, `{"request":{"basicAuthCredentials":{"username":"u"}}}`); len(problems) == 0 {
		t.Error("basic auth without a password should be rejected")
	}
}

// Body matchers are ordered cheapest-first at compile time, so the criterion
// most likely to reject a candidate cheaply runs before one that parses JSON.
func TestBodyMatchersAreOrderedCheapestFirst(t *testing.T) {
	cs := compileOK(t, `{"request":{"bodyPatterns":[
		{"equalToJson":"{\"a\":1}"},
		{"matches":"[0-9]+"},
		{"equalTo":"exact"}
	]}}`)
	if len(cs.BodyMatchers) != 3 {
		t.Fatalf("got %d matchers", len(cs.BodyMatchers))
	}
	if _, ok := cs.BodyMatchers[0].(*matchers.EqualTo); !ok {
		t.Errorf("cheapest matcher should be first, got %T", cs.BodyMatchers[0])
	}
	if _, ok := cs.BodyMatchers[2].(*matchers.EqualToJSON); !ok {
		t.Errorf("the JSON matcher should be last, got %T", cs.BodyMatchers[2])
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	for _, doc := range []string{`{`, `[]`, `"a string"`, `null`, ``} {
		_, errs := Compile([]byte(doc), 1, testOptions())
		if errs == nil {
			t.Errorf("%q should be rejected", doc)
		}
	}
}

func TestWithIdentitySetsBothAliases(t *testing.T) {
	out, err := WithIdentity([]byte(`{"request":{"urlPath":"/x"}}`), "the-id")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"id":"the-id"`) || !strings.Contains(s, `"uuid":"the-id"`) {
		t.Errorf("both aliases should be set, got %s", s)
	}
	if !strings.Contains(s, `"urlPath":"/x"`) {
		t.Errorf("the rest of the document should be preserved, got %s", s)
	}
}

func BenchmarkCompileTypicalStub(b *testing.B) {
	doc := []byte(`{
		"name":"create order","priority":3,
		"request":{"method":"POST","urlPath":"/api/orders",
			"headers":{"Content-Type":{"contains":"application/json"}},
			"queryParameters":{"dryRun":{"matches":"true|false"}},
			"bodyPatterns":[{"equalToJson":"{\"channel\":\"web\"}","ignoreExtraElements":true}]},
		"response":{"status":201,"jsonBody":{"orderId":"x"},"headers":{"Content-Type":"application/json"}}
	}`)
	opts := testOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, errs := Compile(doc, 1, opts); errs != nil {
			b.Fatal(errs.Errors())
		}
	}
}

// An explicit null is absence, not a competing body form: WireMock reads
// {"body":null,"jsonBody":{…}} as declaring only jsonBody.
func TestNullBodyFieldIsNotAConflict(t *testing.T) {
	cs := compileOK(t, `{"response":{"body":null,"jsonBody":{"a":1}}}`)
	if string(cs.Response.Body) != `{"a":1}` {
		t.Errorf("jsonBody should win over an explicit null body, got %q", cs.Response.Body)
	}

	// Two real forms are still a conflict.
	if problems := compileErrs(t, `{"response":{"body":"x","jsonBody":{"a":1}}}`); len(problems) == 0 {
		t.Error("two non-null body forms should still be rejected")
	}
}

// jsonBody is served compact: the submitted document's indentation is
// formatting, not payload.
func TestJSONBodyIsServedCompact(t *testing.T) {
	cs := compileOK(t, `{"response":{"jsonBody":{"a":   1,
		"b":  [1,  2]}}}`)
	if got := string(cs.Response.Body); got != `{"a":1,"b":[1,2]}` {
		t.Errorf("jsonBody should be compacted, got %q", got)
	}
}

// WireMock treats a non-positive status as unset and serves 200, so rejecting
// it would refuse a stub it accepts.
func TestNonPositiveStatusNormalisesTo200(t *testing.T) {
	for _, doc := range []string{
		`{"response":{"status":0}}`,
		`{"response":{"status":-1}}`,
		`{"response":{"status":null}}`,
	} {
		if got := compileOK(t, doc).Response.Status; got != 200 {
			t.Errorf("%s: status = %d, want 200", doc, got)
		}
	}
	// A positive out-of-range status is still refused: WireMock writes it
	// unvalidated and produces a malformed status line.
	if problems := compileErrs(t, `{"response":{"status":1000}}`); len(problems) == 0 {
		t.Error("a positive out-of-range status should be rejected")
	}
}
