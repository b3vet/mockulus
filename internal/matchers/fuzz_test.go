// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A matcher document is written by whoever registers a stub and evaluated
// against whatever the caller sent, so both ends of this package face input
// nobody vetted. What the targets below assert is therefore not what a matcher
// means — the corpus and the differential suite own that — but the three
// properties a shared deployment depends on regardless of what the document
// says: it does not panic, it does not allocate without bound, and it finishes.

// compileBudget bounds one compile-and-evaluate. Compilation is a synchronous
// admin call, so an input that buys minutes of CPU with one POST is a denial of
// service against every other team on the instance; evaluation is worse, since
// it is paid again on every request that reaches the stub.
const compileBudget = 2 * time.Second

// fuzzOpts compiles regexes and JSONPaths the way the real seam does — the
// timeout included, so a pattern that only the fallback engine accepts cannot
// hold the target hostage — without depending on either engine's package.
func fuzzOpts() Options {
	return Options{
		CompileRegex: func(pattern string) (PatternMatcher, error) {
			re, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
			if err != nil {
				return nil, err
			}
			return testPattern{re}, nil
		},
		CompileJSONPath: func(expr string) (JSONPathEvaluator, error) {
			if !strings.HasPrefix(expr, "$") {
				return nil, errNotAPath
			}
			return fuzzPath{expr}, nil
		},
		AllowContentPatterns: true,
	}
}

var errNotAPath = &pathError{}

type pathError struct{}

func (*pathError) Error() string { return "a path must start with $" }

// fuzzPath stands in for the JSONPath engine, which has targets of its own, so
// that both forms of MatchesJSONPath are exercised here without this package's
// targets depending on that package's parser.
type fuzzPath struct{ expr string }

func (p fuzzPath) Match(doc any) bool { return doc != nil }
func (p fuzzPath) Select(doc any) ([]any, bool) {
	if doc == nil {
		return nil, false
	}
	return []any{doc}, true
}
func (p fuzzPath) Source() string { return p.expr }

// fuzzSubjects are what a compiled matcher is applied to: the empty and absent
// cases a traversal walks off the end of, and enough real shapes that the
// JSON-parsing matchers reach their comparison rather than bailing.
func fuzzSubjects() []Subject {
	return []Subject{
		AbsentKey(),
		NewKeyValues(),
		NewKeyValues(""),
		NewKeyValues("application/json"),
		NewKeyValues("a", "b", "c"),
		NewBody(nil),
		NewBody([]byte(``)),
		NewBody([]byte(`{"customer":{"id":"c-1","vip":true},"items":[1,2,3]}`)),
		NewBody([]byte(`[]`)),
		NewBody([]byte(`not json`)),
		NewDocument([]byte(`{"suite":"e2e","tags":["a"]}`)),
	}
}

// matcherSeeds are the documents the E2E corpus registers, plus the shapes that
// take compilation to its edges: every operand at the wrong type, the
// combinators empty and nested, and the placeholders spelled wrong.
var matcherSeeds = []string{
	// The date-time matchers, with the modifier shapes that ride along and the
	// operand spellings WireMock accepts but can never match.
	`{"before":"2021-06-14T12:13:14Z"}`,
	`{"after":"2021-06-14T12:13:14+03:00"}`,
	`{"equalToDateTime":"2021-06-14"}`,
	`{"equalToDateTime":"now +3 days"}`,
	`{"before":"now"}`,
	`{"after":"-3 hours"}`,
	`{"equalToDateTime":"2021-06-14T12:13:14Z","actualFormat":"dd/MM/yyyy"}`,
	`{"equalToDateTime":"2021-06-14T12:13:14Z","actualFormat":"unix"}`,
	`{"equalToDateTime":"2021-06-14T12:13:14Z","actualFormat":"epoch"}`,
	`{"equalToDateTime":"2021-06-01T00:00:00Z","truncateActual":"first day of month"}`,
	`{"after":"now +3 days","truncateExpected":"FIRST_DAY_OF_MONTH","applyTruncationLast":true}`,
	`{"before":"2021-06-14T12:13:14+0300"}`,
	`{"before":"now+2days"}`,
	`{"before":"  now  "}`,
	`{"equalToDateTime":"2021-06-14T12:13:14Z","actualFormat":""}`,
	`{"equalTo":"x","actualFormat":"dd/MM/yyyy"}`,
	`{"equalTo":"application/json"}`,
	`{"equalTo":"a","caseInsensitive":true}`,
	`{"binaryEqualTo":"aGVsbG8="}`,
	`{"contains":"a"}`,
	`{"doesNotContain":"a"}`,
	`{"contains":"a","doesNotContain":"b"}`,
	`{"matches":"[a-z]+"}`,
	`{"doesNotMatch":"^x"}`,
	`{"absent":true}`,
	`{"absent":false}`,
	`{"equalToJson":{"a":1}}`,
	`{"equalToJson":"{\"a\":1}"}`,
	`{"equalToJson":{"a":1},"ignoreArrayOrder":true,"ignoreExtraElements":true}`,
	`{"equalToJson":{"a":"${json-unit.ignore}"}}`,
	`{"equalToJson":{"a":"${json-unit.ignore-element}"}}`,
	`{"equalToJson":{"a":"${json-unit.any-string}","b":"${json-unit.any-number}"}}`,
	`{"equalToJson":{"a":"${json-unit.any-boolean}"}}`,
	`{"equalToJson":{"a":"${json-unit.regex}[a-z]+"}}`,
	`{"equalToJson":{"a":"${json-unit.regex}("}}`,
	`{"equalToJson":{"a":"${json-unit.does-not-exist}"}}`,
	`{"equalToJson":{"a":"${json-unit."}}`,
	`{"equalToJson":["${json-unit.ignore}",{"b":"${json-unit.any-string}"}]}`,
	`{"matchesJsonPath":"$.a"}`,
	`{"matchesJsonPath":{"expression":"$.a","contains":"b"}}`,
	`{"matchesJsonPath":{"expression":"$.a"}}`,
	`{"matchesJsonPath":{"contains":"b"}}`,
	`{"doesNotMatchJsonPath":"$.a"}`,
	`{"and":[{"contains":"a"},{"contains":"b"}]}`,
	`{"or":[{"equalTo":"a"},{"equalTo":"b"}]}`,
	`{"not":{"contains":"a"}}`,
	`{"and":[]}`,
	`{"or":[{}]}`,
	`{"not":{}}`,
	`{"not":{"binaryEqualTo":"aGk="}}`,
	`{"before":"2021-01-01"}`,
	`{"equalToXml":"<a/>"}`,
	`{}`,
	`[]`,
	`null`,
	`"equalTo"`,
	`{"equalTo":1}`,
	`{"absent":"true"}`,
	`{"and":"nope"}`,
	`{"caseInsensitive":true}`,
	`{"unknownMatcher":"x"}`,
	// The regression seed for the nesting bound: before it existed, a chain
	// like this one padded out to the admin body cap cost 73 s and 16.9 GB.
	`{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":` +
		`{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":{"not":` +
		`{"not":{"not":{"equalTo":"deep"}}}}}}}}}}}}}}}}}}}}}}}`,
}

// FuzzCompile drives matcher compilation and then evaluates whatever compiled.
// Evaluation belongs in the target because a tree the compiler built is a tree
// the request path will walk: a node shape only the fuzzer can produce still
// has to answer or refuse, not panic.
func FuzzCompile(f *testing.F) {
	for _, s := range matcherSeeds {
		f.Add(s)
	}

	subjects := fuzzSubjects()

	f.Fuzz(func(t *testing.T, doc string) {
		opts := fuzzOpts()

		start := time.Now()
		m, problems := Compile(json.RawMessage(doc), "", opts)
		if took := time.Since(start); took > compileBudget {
			t.Fatalf("compiling %d bytes took %s, over the %s budget", len(doc), took, compileBudget)
		}

		// Exactly one outcome. A document that yields both would register a
		// stub and report it rejected; one that yields neither would put a nil
		// matcher into a criterion the request path then calls.
		if (m == nil) == (len(problems) == 0) {
			t.Fatalf("compile returned matcher=%v with %d problems", m != nil, len(problems))
		}
		if m == nil {
			for _, p := range problems {
				// The detail is the whole value of refusing at registration: a
				// 422 that does not say what is wrong sends its author guessing.
				if p.Detail == "" {
					t.Fatalf("a problem at %q was reported without a reason", p.Pointer)
				}
			}
			return
		}

		// Describe feeds the near-miss diagnostics, which run against the same
		// fuzzer-built trees the matcher does.
		if m.Describe() == "" {
			t.Fatal("a compiled matcher describes itself as nothing")
		}

		for _, s := range subjects {
			start = time.Now()
			m.Match(s)
			if took := time.Since(start); took > compileBudget {
				t.Fatalf("matching %s took %s, over the %s budget", doc, took, compileBudget)
			}
		}

		// Compiling the same document twice must reach the same verdict.
		// Registration and every later snapshot rebuild compile the stored
		// document independently, so a decision that is not stable is a stub
		// that registers and is then quarantined on reload (SPEC §6.9).
		again, againProblems := Compile(json.RawMessage(doc), "", fuzzOpts())
		if (again == nil) != (m == nil) || len(againProblems) != len(problems) {
			t.Fatalf("compiling %s twice reached different verdicts", doc)
		}
	})
}

// FuzzEqualToJSON fuzzes both sides of the structural comparison at once: the
// expected document a stub declares, placeholders and all, and the body a
// caller sends. Fixing either side would leave the half of the walk that only
// runs when the two disagree in shape untested.
func FuzzEqualToJSON(f *testing.F) {
	seeds := []struct{ expected, actual string }{
		{`{"a":1}`, `{"a":1}`},
		{`{"a":1}`, `{"a":1,"b":2}`},
		{`{"a":"${json-unit.ignore}"}`, `{"a":{"deep":[1,2]}}`},
		{`{"a":"${json-unit.ignore-element}"}`, `{}`},
		{`{"a":"${json-unit.any-string}"}`, `{"a":null}`},
		{`{"a":"${json-unit.any-number}"}`, `{"a":1e309}`},
		{`{"a":"${json-unit.any-boolean}"}`, `{"a":false}`},
		{`{"a":"${json-unit.regex}[a-z]+"}`, `{"a":"abc1"}`},
		{`["${json-unit.any-number}",2]`, `[5,2,9]`},
		{`[1,2,3]`, `[3,2,1]`},
		{`[]`, `[]`},
		{`{}`, `[]`},
		{`null`, `null`},
		{`"x"`, `"x"`},
		{`0`, `-0`},
		{`1`, `1.0`},
		{`{"a":{"b":{"c":[{"d":1}]}}}`, `{"a":{"b":{"c":[{"d":1}]}}}`},
		{`not json`, `not json`},
	}
	for _, s := range seeds {
		f.Add(s.expected, s.actual)
	}

	f.Fuzz(func(t *testing.T, expected, actual string) {
		// Both flag combinations, because the subset semantics of deviation #25
		// only exist when both are on and the maximum matching only runs there.
		for _, flags := range []string{"", `,"ignoreArrayOrder":true`,
			`,"ignoreExtraElements":true`, `,"ignoreArrayOrder":true,"ignoreExtraElements":true`} {

			operand, err := json.Marshal(expected)
			if err != nil {
				return
			}
			doc := `{"equalToJson":` + string(operand) + flags + `}`

			start := time.Now()
			m, problems := Compile(json.RawMessage(doc), "", fuzzOpts())
			if len(problems) > 0 {
				continue
			}
			m.Match(NewBody([]byte(actual)))
			if took := time.Since(start); took > compileBudget {
				t.Fatalf("comparing %d against %d bytes took %s, over the %s budget",
					len(expected), len(actual), took, compileBudget)
			}
		}
	})
}
