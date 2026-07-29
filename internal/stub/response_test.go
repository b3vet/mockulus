// SPDX-License-Identifier: Apache-2.0

package stub

import (
	"encoding/json"
	"testing"

	"github.com/b3vet/mockulus/internal/wmcompat"
)

// headerDoc wraps a `headers` object in the smallest mapping that carries one.
func headerDoc(headers string) string {
	return `{"request":{"method":"GET","urlPath":"/h"},` +
		`"response":{"status":200,"body":"x","headers":` + headers + `}}`
}

// served renders the compiled headers as they will reach the wire: one entry per
// field line, in the order they will be written under a name.
func served(cs *CompiledStub) [][2]string {
	out := make([][2]string, 0, len(cs.Response.Headers))
	for _, h := range cs.Response.Headers {
		out = append(out, [2]string{h.Name, h.Value})
	}
	return out
}

func sameHeaders(got [][2]string, want [][2]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The order the document wrote is the order the values are sent in, whichever
// spelling came first. Sorting the names, which is what a map walk forced,
// reversed the pair whenever the second spelling sorted ahead of the first —
// so `x-dup` then `X-DUP` went out as second, first, and a stub's two ordered
// Set-Cookie lines arrived the wrong way round with nothing in the document to
// explain it.
func TestResponseHeaderValuesKeepDocumentOrder(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    [][2]string
	}{
		{
			name:    "the folded spelling is the first one written",
			headers: `{"X-DUP":"first","x-dup":"second"}`,
			want:    [][2]string{{"X-DUP", "first"}, {"X-DUP", "second"}},
		},
		{
			name:    "and it is still the first one when it sorts last",
			headers: `{"x-dup":"first","X-DUP":"second"}`,
			want:    [][2]string{{"x-dup", "first"}, {"x-dup", "second"}},
		},
		{
			name:    "arrays and strings under three spellings are one sequence",
			headers: `{"X-Tri":"one","x-TRI":["two","three"],"X-TRI":"four"}`,
			want: [][2]string{
				{"X-Tri", "one"}, {"X-Tri", "two"}, {"X-Tri", "three"}, {"X-Tri", "four"},
			},
		},
		{
			name:    "a name written twice keeps the last value in the first position",
			headers: `{"X-P":"1","x-p":"2","X-P":"3"}`,
			want:    [][2]string{{"X-P", "3"}, {"X-P", "2"}},
		},
		{
			// The control that keeps the fold from being "every header is one
			// header": names that differ by more than case stay apart, keep
			// their own spellings, and keep their own single values.
			name:    "names that differ by more than case are separate headers",
			headers: `{"B-h":"1","A-h":"2","b-H":"3"}`,
			want:    [][2]string{{"B-h", "1"}, {"B-h", "3"}, {"A-h", "2"}},
		},
		{
			// The other control: one name, one value, untouched. Most stubs are
			// this one, and it must come out of the fold exactly as it went in.
			name:    "an ordinary header is unchanged",
			headers: `{"Content-Type":"text/plain"}`,
			want:    [][2]string{{"Content-Type", "text/plain"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := served(compileOK(t, headerDoc(tc.headers)))
			if !sameHeaders(got, tc.want) {
				t.Errorf("headers %s compiled to %v, want %v", tc.headers, got, tc.want)
			}
		})
	}
}

// The fold widens what one header name covers, so the refusals that sit on the
// same field have to survive it. An empty array is still nothing to say, under
// its own name and beside a spelling that does have something to say, and the
// pointer still names the spelling the author wrote rather than the one the
// fold kept.
func TestResponseHeaderRefusalsSurviveTheFold(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		pointer string
	}{
		{
			name:    "an empty array alone",
			headers: `{"X-A":[]}`,
			pointer: "/response/headers/X-A",
		},
		{
			name:    "an empty array under the second spelling of a header that has values",
			headers: `{"X-A":"kept","x-a":[]}`,
			pointer: "/response/headers/x-a",
		},
		{
			name:    "a value that is neither a string nor an array of them",
			headers: `{"X-N":7}`,
			pointer: "/response/headers/X-N",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := compileErrs(t, headerDoc(tc.headers))
			if !hasProblem(problems, wmcompat.CodeMalformed, tc.pointer) {
				t.Errorf("headers %s should be refused at %s, got %v",
					tc.headers, tc.pointer, problems)
			}
		})
	}
}

// storedHeaders is the `headers` object of the document a GET would return.
func storedHeaders(t *testing.T, doc string) string {
	t.Helper()
	out, err := WithIdentity([]byte(doc), "a1000007-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatalf("WithIdentity(%s): %v", doc, err)
	}
	var stored struct {
		Response struct {
			Headers json.RawMessage `json:"headers"`
		} `json:"response"`
	}
	if err := json.Unmarshal(out, &stored); err != nil {
		t.Fatalf("stored document is not readable: %v", err)
	}
	return string(stored.Response.Headers)
}

// The stored document has to describe the response the server will serve. Two
// spellings of one name are one header with two values there, so the document
// says so — and a document that names each of its headers once is stored with
// its values in exactly the form they were written, down to a one-element array
// staying an array.
func TestStoredResponseHeadersFoldSpellings(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    string
	}{
		{
			name:    "two spellings become one name and an array",
			headers: `{"X-DUP":"first","x-dup":"second"}`,
			want:    `{"X-DUP":["first","second"]}`,
		},
		{
			name:    "the surviving spelling is the first one written",
			headers: `{"x-dup":"first","X-DUP":"second"}`,
			want:    `{"x-dup":["first","second"]}`,
		},
		{
			name:    "an array among the spellings contributes its values in place",
			headers: `{"X-Tri":"one","x-TRI":["two","three"],"X-TRI":"four"}`,
			want:    `{"X-Tri":["one","two","three","four"]}`,
		},
		{
			name:    "a name written twice contributes its last value only",
			headers: `{"X-P":"1","x-p":"2","X-P":"3"}`,
			want:    `{"X-P":["3","2"]}`,
		},
		{
			name:    "the headers that did not fold keep their place and their form",
			headers: `{"B-h":"1","A-h":"2","b-H":"3"}`,
			want:    `{"B-h":["1","3"],"A-h":"2"}`,
		},
		{
			// The control against a rewrite that reaches everything it touches:
			// nothing here folds, so nothing is rewritten — a lone array stays
			// an array rather than being collapsed to the string inside it, and
			// a lone string stays a string rather than being wrapped.
			name:    "a document with nothing to fold is stored as it was written",
			headers: `{"X-One":["solo"],"X-Two":"2"}`,
			want:    `{"X-One":["solo"],"X-Two":"2"}`,
		},
		{
			// A value the parser would refuse is a value this must not silently
			// rewrite into something that looks fine.
			name:    "a value that is not a header value is left alone",
			headers: `{"X-N":7,"x-n":"8"}`,
			want:    `{"X-N":7,"x-n":"8"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storedHeaders(t, headerDoc(tc.headers)); got != tc.want {
				t.Errorf("headers %s were stored as %s, want %s", tc.headers, got, tc.want)
			}
		})
	}
}

// A mapping with no response, or a response with no headers, reaches this on
// the file driver's path before anything has looked at it, so the fold has to
// be a no-op on documents it has nothing to say about rather than an error or a
// panic.
func TestStoredMappingWithoutResponseHeaders(t *testing.T) {
	for _, doc := range []string{
		`{"request":{"urlPath":"/x"}}`,
		`{"request":{"urlPath":"/x"},"response":{"status":204}}`,
		`{"request":{"urlPath":"/x"},"response":null}`,
		`{"request":{"urlPath":"/x"},"response":{"status":200,"headers":[]}}`,
	} {
		out, err := WithIdentity([]byte(doc), "a1000007-0000-4000-8000-000000000000")
		if err != nil {
			t.Errorf("WithIdentity(%s): %v", doc, err)
			continue
		}
		var stored map[string]json.RawMessage
		if err := json.Unmarshal(out, &stored); err != nil {
			t.Errorf("stored document for %s is not readable: %v", doc, err)
		}
	}
}

// TestAMultiValuedContentTypeIsRefused pins the one header a stub may not
// declare twice.
//
// A response carries exactly one Content-Type and its value is one media type,
// so two of them describes a message the wire cannot carry. Both available
// answers hand back something the author did not write — joining produces
// `application/json, text/plain`, which no client can parse as a media type,
// and taking the last is WireMock's answer and silently drops the other — so
// the document is refused and the author hears about it at registration
// instead of from whichever client tried to read the result.
func TestAMultiValuedContentTypeIsRefused(t *testing.T) {
	refused := []struct{ name, headers, pointer string }{
		{"an array of two", `{"Content-Type":["application/json","text/plain"]}`, "/response/headers/Content-Type"},
		{"an array of three", `{"Content-Type":["a/b","c/d","e/f"]}`, "/response/headers/Content-Type"},
		{"two spellings of the name", `{"Content-Type":"application/json","content-type":"text/plain"}`, "/response/headers/Content-Type"},
		{"a lower-case name", `{"content-type":["application/json","text/plain"]}`, "/response/headers/content-type"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			problems := compileErrs(t, headerDoc(tc.headers))
			if !hasProblem(problems, wmcompat.CodeMalformed, tc.pointer) {
				t.Errorf("headers %s should be refused at %s, got %v",
					tc.headers, tc.pointer, problems)
			}
		})
	}

	// The controls. Refusing two must not refuse one, must not refuse the
	// one-element array spelling of one, and must not reach any other header —
	// a repeated header is ordinary and stays ordinary.
	for _, headers := range []string{
		`{"Content-Type":"application/json"}`,
		`{"Content-Type":["application/json"]}`,
		`{"X-Multi":["a","b"],"Content-Type":"application/json"}`,
		`{"X-Multi":["a","b"]}`,
	} {
		if cs := compileOK(t, headerDoc(headers)); cs == nil {
			t.Errorf("headers %s were refused", headers)
		}
	}
}
