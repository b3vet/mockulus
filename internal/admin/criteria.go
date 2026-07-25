// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"github.com/b3vet/mockulus/internal/matchers"
	"github.com/b3vet/mockulus/internal/stub"
	"github.com/b3vet/mockulus/internal/wmcompat"
)

// Verification criteria are a stub's `request` object with no response
// attached, so they are compiled by wrapping them in a mapping and reusing the
// stub compiler outright. That is the point: a `verify()` call and a stub
// declare a request the same way, and if the two diverged, a test could pass
// against a stub that would never have matched.

// criteria is a compiled request pattern, evaluated against journal entries.
type criteria struct {
	stub *stub.CompiledStub
}

// compileCriteria builds a matcher tree from a request-pattern document.
func compileCriteria(raw []byte, opts stub.Options) (*criteria, *wmcompat.ErrorList) {
	// Wrapping rather than re-implementing: every matcher, every validation
	// rule and every 422 comes along unchanged.
	doc := append(append([]byte(`{"request":`), raw...), '}')

	compiled, errs := stub.Compile(doc, 0, opts)
	if errs != nil {
		return nil, errs
	}
	return &criteria{stub: compiled}, nil
}

// matches evaluates the criteria against one recorded request.
func (c *criteria) matches(e *serveEvent) bool {
	cs := c.stub

	if !cs.MatchesMethod(e.Request.Method) {
		return false
	}
	if !matchRecordedURL(cs, e.Request.URL) {
		return false
	}

	for _, crit := range cs.Headers {
		if !crit.Matcher.Match(recordedValues(e.Request.Headers, crit.Name)) {
			return false
		}
	}
	for _, crit := range cs.Cookies {
		if !crit.Matcher.Match(recordedValues(e.Request.Cookies, crit.Name)) {
			return false
		}
	}
	for _, crit := range cs.Query {
		if !crit.Matcher.Match(recordedValues(e.Request.Query, crit.Name)) {
			return false
		}
	}
	for _, m := range cs.BodyMatchers {
		if !m.Match(matchers.NewBody([]byte(e.Request.Body))) {
			return false
		}
	}
	return true
}

// matchRecordedURL applies the criteria's URL condition to a recorded URL.
func matchRecordedURL(cs *stub.CompiledStub, url string) bool {
	path := url
	if i := indexByte(url, '?'); i >= 0 {
		path = url[:i]
	}

	switch cs.URLKind {
	case stub.URLAny:
		return true
	case stub.URLExactFull:
		return cs.URLLiteral == url
	case stub.URLExactPath:
		return cs.URLLiteral == path
	case stub.URLPatternFull:
		return cs.URLRegex != nil && cs.URLRegex.MatchString(url)
	case stub.URLPatternPath:
		return cs.URLRegex != nil && cs.URLRegex.MatchString(path)
	case stub.URLTemplate:
		return cs.PathTemplate != nil && cs.PathTemplate.Match(path, nil)
	default:
		return false
	}
}

// recordedValues adapts a recorded header, cookie or query entry into a matcher
// subject. A recorded entry is a string when single-valued and an array when
// repeated, which is how the serve event stores it.
func recordedValues(source map[string]any, name string) matchers.Subject {
	raw, present := source[name]
	if !present {
		// Header names are case-insensitive, and a recorded event carries
		// whatever spelling arrived on the wire.
		for key, value := range source {
			if equalFold(key, name) {
				raw, present = value, true
				break
			}
		}
	}
	if !present {
		return matchers.AbsentKey()
	}

	switch v := raw.(type) {
	case string:
		return matchers.NewKeyValues(v)
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				values = append(values, s)
			}
		}
		return matchers.NewKeyValues(values...)
	case []string:
		return matchers.NewKeyValues(v...)
	default:
		return matchers.AbsentKey()
	}
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
