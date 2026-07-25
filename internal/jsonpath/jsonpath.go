// SPDX-License-Identifier: Apache-2.0

// Package jsonpath evaluates the JSONPath subset WireMock's matchesJsonPath
// uses (SPEC §6.7).
//
// SPEC §6.7 planned to wrap a third-party library. Probing the pinned WireMock
// ruled that out: its semantics hinge on a distinction most libraries erase.
//
// A **definite** path — `$.a.b`, `$[0]` — returns the selected node itself. An
// **indefinite** path — anything with `..`, a wildcard, a slice or a filter —
// returns a *list of hits*. That difference is the whole behavior:
//
//	{"v": null}   with  $.v   does NOT match  (the node is null)
//	{"v": null}   with  $..v  DOES match      (a one-element list of hits)
//
// A library that always returns a list cannot express the first case, and one
// that always returns a node cannot express the second. So the evaluator
// carries definiteness through, and Result reports which kind it produced.
package jsonpath

import (
	"fmt"
	"strconv"
	"strings"
)

// Path is a compiled JSONPath expression.
type Path struct {
	// Source is the expression as written, for diagnostics.
	Source string
	steps  []step
	// definite records that every step selects at most one node, which decides
	// whether evaluation returns a node or a list of hits.
	definite bool
}

// Definite reports whether the path selects at most one node.
func (p *Path) Definite() bool { return p.definite }

// Result is what an evaluation produced.
type Result struct {
	// Found reports whether evaluation selected anything at all.
	Found bool
	// Definite mirrors the path's kind.
	Definite bool
	// Node is the selected value for a definite path.
	Node any
	// Hits are the selected values for an indefinite path.
	Hits []any
}

// Values returns the selected values however the path was shaped, which is what
// a nested matcher iterates.
func (r Result) Values() []any {
	if !r.Found {
		return nil
	}
	if r.Definite {
		return []any{r.Node}
	}
	return r.Hits
}

// Matches implements the bare-form truthiness of matchesJsonPath, verified
// against the pinned WireMock.
//
// The test applies to what the evaluator RETURNED, not to the semantic value
// selected, and the emptiness check is shallow. So an empty string, `false` and
// `0` all match — only a null node, an empty collection, and selecting nothing
// do not. A one-element list whose element is null matches, because the list is
// not empty.
func (r Result) Matches() bool {
	if !r.Found {
		return false
	}
	if r.Definite {
		return nonEmptyNode(r.Node)
	}
	return len(r.Hits) > 0
}

func nonEmptyNode(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		// Strings, numbers and booleans are present values, whatever they hold.
		// An empty string matches; this is the case naive implementations get
		// wrong by applying a generic emptiness test.
		return true
	}
}

// step is one segment of a compiled path.
type step struct {
	kind stepKind
	// name is the child key for a child step.
	name string
	// index is the array index for an index step.
	index int
	// from, to and hasTo bound a slice step.
	from, to int
	hasTo    bool
	// filter is the predicate of a filter step.
	filter *filter
}

type stepKind uint8

const (
	stepChild stepKind = iota
	stepIndex
	stepWildcard
	stepDescend
	stepSlice
	stepFilter
)

// Compile parses an expression. Unsupported syntax is an error here, so a stub
// using it is rejected at registration rather than becoming one that silently
// never matches (SPEC §6.7, P3).
func Compile(expr string) (*Path, error) {
	src := strings.TrimSpace(expr)
	if src == "" {
		return nil, fmt.Errorf("empty JSONPath expression")
	}
	if !strings.HasPrefix(src, "$") {
		return nil, fmt.Errorf("a JSONPath expression must start with $")
	}

	p := &Path{Source: expr, definite: true}
	rest := src[1:]

	for len(rest) > 0 {
		switch {
		case strings.HasPrefix(rest, ".."):
			rest = rest[2:]
			name, remainder, err := readName(rest)
			if err != nil {
				return nil, err
			}
			if name == "*" {
				p.steps = append(p.steps, step{kind: stepDescend, name: "*"})
			} else {
				p.steps = append(p.steps, step{kind: stepDescend, name: name})
			}
			p.definite = false
			rest = remainder

		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			name, remainder, err := readName(rest)
			if err != nil {
				return nil, err
			}
			if name == "*" {
				p.steps = append(p.steps, step{kind: stepWildcard})
				p.definite = false
			} else {
				p.steps = append(p.steps, step{kind: stepChild, name: name})
			}
			rest = remainder

		case strings.HasPrefix(rest, "["):
			end := matchBracket(rest)
			if end < 0 {
				return nil, fmt.Errorf("unclosed [ in %q", expr)
			}
			inner := strings.TrimSpace(rest[1:end])
			st, indefinite, err := parseBracket(inner)
			if err != nil {
				return nil, fmt.Errorf("%w (in %q)", err, expr)
			}
			p.steps = append(p.steps, st)
			if indefinite {
				p.definite = false
			}
			rest = rest[end+1:]

		default:
			return nil, fmt.Errorf("unexpected %q in %q", rest, expr)
		}
	}
	return p, nil
}

// readName reads a bare child name up to the next separator.
func readName(s string) (name, rest string, err error) {
	i := 0
	for i < len(s) && s[i] != '.' && s[i] != '[' {
		i++
	}
	if i == 0 {
		return "", "", fmt.Errorf("empty path segment")
	}
	return s[:i], s[i:], nil
}

// matchBracket finds the ] closing the [ at position 0, honouring quotes and
// nesting so a filter expression containing brackets does not confuse it.
func matchBracket(s string) int {
	depth := 0
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseBracket reads the inside of a [] step.
func parseBracket(inner string) (step, bool, error) {
	switch {
	case inner == "*":
		return step{kind: stepWildcard}, true, nil

	case strings.HasPrefix(inner, "?("):
		if !strings.HasSuffix(inner, ")") {
			return step{}, false, fmt.Errorf("unclosed filter expression")
		}
		f, err := parseFilter(strings.TrimSpace(inner[2 : len(inner)-1]))
		if err != nil {
			return step{}, false, err
		}
		return step{kind: stepFilter, filter: f}, true, nil

	case len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"'):
		q := inner[0]
		if inner[len(inner)-1] != q {
			return step{}, false, fmt.Errorf("unterminated quoted key")
		}
		return step{kind: stepChild, name: inner[1 : len(inner)-1]}, false, nil

	case strings.Contains(inner, ":"):
		from, to, hasTo, err := parseSlice(inner)
		if err != nil {
			return step{}, false, err
		}
		return step{kind: stepSlice, from: from, to: to, hasTo: hasTo}, true, nil

	default:
		i, err := strconv.Atoi(inner)
		if err != nil {
			return step{}, false, fmt.Errorf("unsupported bracket expression %q", inner)
		}
		return step{kind: stepIndex, index: i}, false, nil
	}
}

func parseSlice(inner string) (from, to int, hasTo bool, err error) {
	parts := strings.SplitN(inner, ":", 2)
	if parts[0] != "" {
		from, err = strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid slice start %q", parts[0])
		}
	}
	if strings.TrimSpace(parts[1]) != "" {
		to, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid slice end %q", parts[1])
		}
		hasTo = true
	}
	return from, to, hasTo, nil
}

// Eval walks the document.
//
// A path that traverses through a scalar, or names a key that is not there, is
// a plain non-match rather than an error — WireMock swallows evaluation errors
// into a silent no-match, never a 5xx (SPEC §6.7).
func (p *Path) Eval(doc any) Result {
	current := []any{doc}

	for _, st := range p.steps {
		next := make([]any, 0, len(current))
		for _, node := range current {
			next = append(next, apply(st, node)...)
		}
		current = next
		if len(current) == 0 {
			break
		}
	}

	if p.definite {
		if len(current) == 0 {
			return Result{Definite: true}
		}
		return Result{Found: true, Definite: true, Node: current[0]}
	}
	return Result{Found: len(current) > 0, Definite: false, Hits: current}
}

func apply(st step, node any) []any {
	switch st.kind {
	case stepChild:
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		value, present := obj[st.name]
		if !present {
			return nil
		}
		return []any{value}

	case stepIndex:
		arr, ok := node.([]any)
		if !ok {
			return nil
		}
		i := st.index
		if i < 0 {
			i += len(arr)
		}
		if i < 0 || i >= len(arr) {
			return nil
		}
		return []any{arr[i]}

	case stepWildcard:
		return children(node)

	case stepSlice:
		arr, ok := node.([]any)
		if !ok {
			return nil
		}
		from := st.from
		if from < 0 {
			from += len(arr)
		}
		to := len(arr)
		if st.hasTo {
			to = st.to
			if to < 0 {
				to += len(arr)
			}
		}
		from = clamp(from, 0, len(arr))
		to = clamp(to, from, len(arr))
		return append([]any(nil), arr[from:to]...)

	case stepDescend:
		var out []any
		descend(node, st.name, &out)
		return out

	case stepFilter:
		arr, ok := node.([]any)
		if !ok {
			// A filter over an object tests the object itself, which is what
			// makes $[?(@.x)] work on a single document.
			if st.filter.eval(node) {
				return []any{node}
			}
			return nil
		}
		var out []any
		for _, item := range arr {
			if st.filter.eval(item) {
				out = append(out, item)
			}
		}
		return out

	default:
		return nil
	}
}

// children returns a node's direct values.
func children(node any) []any {
	switch t := node.(type) {
	case map[string]any:
		// Sorted, so a wildcard produces a stable order and a template or a
		// nested matcher sees the same thing every time.
		keys := sortedKeys(t)
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, t[k])
		}
		return out
	case []any:
		return append([]any(nil), t...)
	default:
		return nil
	}
}

// descend collects every value reachable under a name, at any depth. A name of
// "*" collects every node.
func descend(node any, name string, out *[]any) {
	switch t := node.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			v := t[k]
			if name == "*" || k == name {
				*out = append(*out, v)
			}
			descend(v, name, out)
		}
	case []any:
		for _, item := range t {
			if name == "*" {
				*out = append(*out, item)
			}
			descend(item, name, out)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: these maps are small and this avoids pulling in sort for
	// one call on a path that may run per request.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
