// SPDX-License-Identifier: Apache-2.0

package jsonpath

import (
	"encoding/json"
	"fmt"
)

// The adapters below let one compiled path serve both places a JSONPath
// appears: the `matchesJsonPath` matcher and the `jsonPath` template helper.
// Compiling it once in one package is what keeps a stub's matching and its
// response templating agreeing about what an expression means.

// Evaluator adapts a compiled Path to the matcher package's interface.
type Evaluator struct{ path *Path }

// NewEvaluator compiles an expression for use as a matcher.
func NewEvaluator(expr string) (*Evaluator, error) {
	p, err := Compile(expr)
	if err != nil {
		return nil, err
	}
	return &Evaluator{path: p}, nil
}

// Match implements the bare form.
func (e *Evaluator) Match(doc any) bool { return e.path.Eval(doc).Matches() }

// Select implements the nested form.
func (e *Evaluator) Select(doc any) ([]any, bool) {
	result := e.path.Eval(doc)
	return result.Values(), result.Found
}

// Source returns the expression as written.
func (e *Evaluator) Source() string { return e.path.Source }

// TemplateHelper builds the `jsonPath` template helper (SPEC §10.3).
//
// It shares this package with the matcher deliberately: a stub that matches on
// $.customer.id and then renders {{jsonPath request.body '$.customer.id'}}
// should not be able to disagree with itself about what that path selects.
func TemplateHelper(args []any, _ map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("jsonPath takes a document and an expression")
	}

	expr, ok := args[1].(string)
	if !ok {
		return nil, fmt.Errorf("the jsonPath expression must be a string")
	}
	path, err := Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("jsonPath: %w", err)
	}

	doc, err := asDocument(args[0])
	if err != nil {
		// A template rendering a path over a non-JSON body is a serve-time
		// error, which the response carries as text (SPEC §10.4).
		return nil, err
	}

	result := path.Eval(doc)
	if !result.Found {
		return "", nil
	}
	values := result.Values()
	if len(values) == 1 {
		return values[0], nil
	}
	return values, nil
}

// asDocument accepts either a raw JSON string — which is what request.body is —
// or an already-decoded value.
func asDocument(v any) (any, error) {
	switch t := v.(type) {
	case string:
		var doc any
		if err := json.Unmarshal([]byte(t), &doc); err != nil {
			return nil, fmt.Errorf("jsonPath: the document is not valid JSON")
		}
		return doc, nil
	case nil:
		return nil, fmt.Errorf("jsonPath: no document given")
	default:
		if s, ok := v.(fmt.Stringer); ok {
			return asDocument(s.String())
		}
		return v, nil
	}
}
