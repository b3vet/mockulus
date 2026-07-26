// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/b3vet/mockulus/internal/jsonpath"
	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/regexx"
	"github.com/b3vet/mockulus/internal/template"
)

// The stub mapping is the widest untrusted surface in the product: one admin
// POST reaches this compiler and, through it, the regex translator, the
// JSONPath parser, the template parser and every matcher. A panic anywhere
// below here is reachable by anyone who can register a stub, and the deployment
// they are registering it in is shared with other teams (SPEC §1).
//
// So the properties asserted are the ones a shared deployment needs whatever
// the document says — no panic, no unbounded allocation, no hang — plus the two
// structural promises the admin write path relies on: a document either
// compiles or is refused with reasons, and it reaches the same verdict every
// time it is compiled.

// mappingBudget bounds one compile. Registration is synchronous, so a document
// that buys minutes of CPU with one POST is a denial of service against
// everyone else on the instance.
const mappingBudget = 2 * time.Second

// fuzzOptions is the production wiring of SPEC §4.4: the same regex policy, the
// same JSONPath engine and the same template engine the server builds. Stubbing
// any of them out would move the interesting parsers outside the target.
func fuzzOptions() Options {
	engine := template.NewEngine(1<<16, jsonpath.TemplateHelper)
	return Options{
		CompileRegex: func(pattern string) (matchers.PatternMatcher, error) {
			return regexx.Compile(pattern, regexx.Options{
				Anchored: true,
				Timeout:  50 * time.Millisecond,
			})
		},
		CompileJSONPath: func(expr string) (matchers.JSONPathEvaluator, error) {
			return jsonpath.NewEvaluator(expr)
		},
		CompileTemplate: engine.Compile,
	}
}

// mappingSeeds are the documents the E2E corpus registers, reduced to one line
// each, plus the shapes that take validation to its edges: every field at the
// wrong type, the mutually exclusive fields together, and the parsers reached
// through a mapping rather than directly.
var mappingSeeds = []string{
	`{"request":{"method":"GET","urlPath":"/x"},"response":{"status":200}}`,
	`{"request":{"url":"/x?a=1"},"response":{"status":204}}`,
	`{"request":{"urlPattern":"/x/[0-9]+"},"response":{"body":"hi"}}`,
	`{"request":{"urlPathPattern":"/x/\\d+"},"response":{"jsonBody":{"a":[1,2]}}}`,
	`{"request":{"urlPathTemplate":"/orders/{id}/items/{itemId}",
	  "pathParameters":{"id":{"matches":"[0-9]+"}}},"response":{"status":200}}`,
	`{"request":{"urlPathTemplate":"/orders/{id}","pathParameters":{"nope":{"equalTo":"1"}}},"response":{}}`,
	`{"id":"aaaa0006-0000-4000-8000-000000000001","request":{"urlPath":"/x"},"response":{"status":200}}`,
	`{"id":"aaaa0006-0000-4000-8000-000000000001","uuid":"bbbb0006-0000-4000-8000-000000000002",
	  "request":{"urlPath":"/x"},"response":{"status":200}}`,
	`{"name":"n","priority":1,"persistent":false,"metadata":{"suite":"e2e"},
	  "request":{"urlPath":"/x"},"response":{"status":200}}`,
	`{"metadata":null,"request":{"urlPath":"/x"},"response":{"status":200}}`,
	`{"scenarioName":"s","requiredScenarioState":"Started","newScenarioState":"next",
	  "request":{"urlPath":"/x"},"response":{"status":200}}`,
	`{"requiredScenarioState":"Started","request":{"urlPath":"/x"},"response":{}}`,
	`{"request":{"headers":{"X-A":{"equalTo":"a"}},"queryParameters":{"q":{"absent":true}},
	  "cookies":{"sid":{"matches":".+"}},"formParameters":{"f":{"contains":"x"}}},"response":{}}`,
	`{"request":{"basicAuthCredentials":{"username":"u","password":"p"}},"response":{}}`,
	`{"request":{"basicAuthCredentials":{"username":"u"}},"response":{}}`,
	`{"request":{"bodyPatterns":[{"equalToJson":{"a":"${json-unit.any-string}"}},
	  {"matchesJsonPath":"$.items[?(@.qty > 1)]"},{"binaryEqualTo":"aGk="}]},"response":{}}`,
	`{"request":{"bodyPatterns":[{"matches":"\\p{Alpha}++"}]},"response":{}}`,
	`{"request":{"bodyPatterns":"nope"},"response":{}}`,
	`{"response":{"status":200,"statusMessage":"Wéird\r\nphrase","headers":{"X-A":["1","2"]}}}`,
	`{"response":{"base64Body":"aGVsbG8="}}`,
	`{"response":{"body":"a","jsonBody":{"b":1}}}`,
	`{"response":{"body":null,"jsonBody":{"b":1}}}`,
	`{"response":{"status":1000}}`,
	`{"response":{"status":-1}}`,
	`{"response":{"fault":"CONNECTION_RESET_BY_PEER"}}`,
	`{"response":{"fault":"CONNECTION_RESET_BY_PEER","body":"x"}}`,
	`{"response":{"fixedDelayMilliseconds":10,"delayDistribution":{"type":"lognormal","median":1,"sigma":0.1}}}`,
	`{"response":{"delayDistribution":{"type":"uniform","lower":1,"upper":0}}}`,
	`{"response":{"chunkedDribbleDelay":{"numberOfChunks":2,"totalDuration":50}}}`,
	`{"response":{"transformers":["response-template"],"body":"{{request.path}} {{request.query.a}}"}}`,
	`{"response":{"transformers":["response-template"],"headers":{"X-A":"{{now offset='1 days'}}"}}}`,
	`{"response":{"transformers":["response-template"],"transformerParameters":{"disableBodyTemplating":true},
	  "body":"{{not-a-helper}}"}}`,
	`{"response":{"transformers":["nope"]}}`,
	`{"response":{"transformers":["response-template"],"body":"{{#if a}}{{/unless}}"}}`,
	`{"request":{"proxyBaseUrl":"http://x"},"response":{}}`,
	`{"postServeActions":[],"request":{},"response":{}}`,
	`{"request":{"url":"/a","urlPath":"/a"},"response":{}}`,
	`{"request":{"url":"no-leading-slash"},"response":{}}`,
	`{"request":{"method":123},"response":{}}`,
	`{}`,
	`null`,
	`[]`,
	`"a string"`,
	`{"request":null,"response":null}`,
	`{"unknownField":1}`,
	// The regression seed for the matcher nesting bound (matchers.maxNesting):
	// before it existed, this chain padded out to the admin body cap cost 73 s
	// of CPU and 16.9 GB of allocation for a single POST.
	`{"request":{"bodyPatterns":[` +
		`{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":` +
		`{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":` +
		`{"not":{"not":{"equalTo":"deep"}}}}}}}}}}}}}}}}}}}}}}}` +
		`]},"response":{}}`,
	// And for the template block bound (handlebars.maxBlockNesting), where the
	// overflow was fatal rather than recoverable.
	`{"response":{"transformers":["response-template"],"body":"{{#if a}}{{#if a}}{{#if a}}x{{/if}}{{/if}}{{/if}}"}}`,
}

// FuzzCompile drives the whole registration path of one mapping document.
func FuzzCompile(f *testing.F) {
	for _, s := range mappingSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, doc string) {
		opts := fuzzOptions()

		start := time.Now()
		cs, errs := Compile([]byte(doc), 1, opts)
		if took := time.Since(start); took > mappingBudget {
			t.Fatalf("compiling %d bytes took %s, over the %s budget", len(doc), took, mappingBudget)
		}

		// Exactly one outcome, which is what the admin write path assumes when
		// it writes the document to the store on a nil error list.
		if (cs == nil) == (errs == nil) {
			t.Fatalf("compile returned stub=%v and errors=%v", cs != nil, errs != nil)
		}
		if cs == nil {
			if errs.Empty() {
				t.Fatal("the mapping was rejected without a single reason")
			}
			for _, e := range errs.Errors() {
				// A 422 with no code cannot be mapped onto the catalog of
				// Appendix B, which is how a caller learns what to change.
				if e.Code == 0 {
					t.Fatalf("a rejection carries no catalog code: %+v", e)
				}
			}
			return
		}

		// The stored document is returned verbatim by GET, so anything that
		// rewrote it here would hand a client back a stub it did not write.
		if string(cs.Raw) != doc {
			t.Fatalf("Raw = %q, want the document back", cs.Raw)
		}

		exerciseServing(t, cs)

		// The verdict must be stable. Registration compiles the submitted
		// document and every later snapshot rebuild compiles the stored one, so
		// a decision that is not reproducible is a stub that registers and is
		// then quarantined on reload (SPEC §6.9).
		if _, again := Compile([]byte(doc), 1, fuzzOptions()); again != nil {
			t.Fatalf("a mapping that compiled once was rejected the second time: %v", again.Errors())
		}

		// The admin write path stamps the assigned id into the document before
		// storing it, and the store is what every pod reloads from. A stamped
		// document that no longer compiles is a stub that answers 201 and then
		// serves nothing anywhere (SPEC §4.3).
		stamped, err := WithIdentity([]byte(doc), "aaaa0006-0000-4000-8000-00000000ffff")
		if err != nil {
			t.Fatalf("a compiled mapping could not be stamped with its id: %v", err)
		}
		if _, errs := Compile(stamped, 1, fuzzOptions()); errs != nil {
			t.Fatalf("the stamped form of a registered mapping does not compile: %v", errs.Errors())
		}
	})
}

// exerciseServing walks the compiled stub the way the request path does, so a
// node shape only the fuzzer can build has to answer rather than panic. The
// subjects are the ones a traversal walks off the end of plus one real body.
func exerciseServing(t *testing.T, cs *CompiledStub) {
	t.Helper()

	cs.MatchesMethod("GET")
	cs.HasCriteriaBeyondURL()

	if cs.PathTemplate != nil {
		for _, path := range []string{"", "/", "/orders/7/items/9", "//", "/a/b/c/d"} {
			cs.PathTemplate.Match(path, func(string, string) {})
		}
		if prefix := cs.PathTemplate.LiteralPrefix(); prefix == "" {
			// The engine prefilters candidates on this prefix (SPEC §6.3); an
			// empty one for a template that starts with literal text would put
			// every request through the template walk.
			t.Fatalf("template %q reports no literal prefix", cs.PathTemplate.Source)
		}
	}
	if cs.URLRegex != nil {
		cs.URLRegex.MatchString("/e2e/thing")
	}

	body := matchers.NewBody([]byte(`{"a":1,"items":[{"qty":2}]}`))
	for _, m := range cs.BodyMatchers {
		m.Match(body)
		m.Describe()
	}
	for _, group := range [][]KeyCriterion{cs.Headers, cs.Query, cs.Cookies, cs.Form, cs.PathParams} {
		for _, c := range group {
			c.Matcher.Match(matchers.NewKeyValues("v"))
			c.Matcher.Match(matchers.AbsentKey())
		}
	}
}

// FuzzWithIdentity fuzzes the id-stamping rewrite on its own. It runs on
// documents that never reached the compiler — an import batch stamps every
// entry it was given — so it has to survive a document the compiler would have
// refused, and must never turn valid JSON into something the store cannot
// serve back.
func FuzzWithIdentity(f *testing.F) {
	for _, s := range mappingSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, doc string) {
		start := time.Now()
		out, err := WithIdentity([]byte(doc), "aaaa0006-0000-4000-8000-00000000ffff")
		if took := time.Since(start); took > mappingBudget {
			t.Fatalf("stamping %d bytes took %s, over the %s budget", len(doc), took, mappingBudget)
		}
		if err != nil {
			return
		}

		// What comes back is stored and later returned to a client verbatim, so
		// it has to be JSON, and it has to carry the identity that was stamped
		// — a rewrite that dropped it would store a stub under an id no request
		// can address.
		var back map[string]json.RawMessage
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("stamping produced something that is not a JSON object: %v", err)
		}
		for _, field := range []string{"id", "uuid"} {
			var got string
			if err := json.Unmarshal(back[field], &got); err != nil {
				t.Fatalf("stamped %s is not a string: %s", field, back[field])
			}
			if got != "aaaa0006-0000-4000-8000-00000000ffff" {
				t.Fatalf("stamped %s = %q", field, got)
			}
		}
	})
}
