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
	"errors"
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
	// scannable records that the path can also be evaluated by scanning the
	// undecoded document (scan.go), which is definite minus the one step shape
	// a single forward pass cannot answer.
	scannable bool
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
	// indexes and names carry a union's operands in the order they were
	// written, which is the order its hits come back in.
	indexes []int
	names   []string
	// merge records a name union that nothing selects past, which is the one
	// shape that returns a single merged object rather than a list of hits —
	// see applyNameUnion.
	merge bool
}

type stepKind uint8

const (
	stepChild stepKind = iota
	stepIndex
	stepWildcard
	stepDescend
	stepSlice
	stepFilter
	stepUnionIndex
	stepUnionName
	stepLength
)

// Compile parses an expression. Unsupported syntax is an error here, so a stub
// using it is rejected at registration rather than becoming one that silently
// never matches (SPEC §6.7, P3).
func Compile(expr string) (*Path, error) {
	src := strings.TrimSpace(expr)
	if src == "" {
		return nil, errors.New("empty JSONPath expression")
	}
	if !strings.HasPrefix(src, "$") {
		return nil, errors.New("a JSONPath expression must start with $")
	}

	p := &Path{Source: expr}
	rest := src[1:]

	for len(rest) > 0 {
		switch {
		case strings.HasPrefix(rest, ".."):
			rest = rest[2:]
			name, remainder, err := readName(rest)
			if err != nil {
				return nil, err
			}
			if _, _, isCall := functionCall(name); isCall {
				// `$..length()` is the one placement of a function this
				// evaluator has no answer for: a deep scan collects nodes by
				// name and there is no name here to collect. Refusing it names
				// the gap where compiling it to a member called "length()"
				// would leave a stub that quietly never matches.
				return nil, fmt.Errorf("a function cannot follow .. in %q", expr)
			}
			p.steps = append(p.steps, step{kind: stepDescend, name: name})
			rest = remainder

		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			name, remainder, err := readName(rest)
			if err != nil {
				return nil, err
			}
			st, err := dotSegment(name)
			if err != nil {
				return nil, fmt.Errorf("%w (in %q)", err, expr)
			}
			p.steps = append(p.steps, st)
			rest = remainder

		case strings.HasPrefix(rest, "["):
			end := matchBracket(rest)
			if end < 0 {
				return nil, fmt.Errorf("unclosed [ in %q", expr)
			}
			inner := strings.TrimSpace(rest[1:end])
			st, err := parseBracket(inner)
			if err != nil {
				return nil, fmt.Errorf("%w (in %q)", err, expr)
			}
			p.steps = append(p.steps, st)
			rest = rest[end+1:]

		default:
			return nil, fmt.Errorf("unexpected %q in %q", rest, expr)
		}
	}

	p.markMergingUnions()
	p.definite = p.selectsAtMostOneNode()
	p.scannable = p.definite && p.stepsTheScannerTakes()
	return p, nil
}

// markMergingUnions settles which name unions return a merged object.
//
// A union of names is the one step whose shape depends on what follows it: at
// the end of a path it returns ONE object carrying the members it selected,
// while in the middle it branches, carrying each member on separately. Both
// halves are the pinned WireMock's, and the difference is visible from a stub:
// `$['a','b']` over `{"a":1,"b":2}` gives an inner matcher the text of an
// object, so `equalTo: "1"` does not hold, while `$['a','b'].c` over
// `{"a":{"c":1},"b":{"c":2}}` gives it 1 and then 2, so `equalTo: "1"` does.
//
// A function reads what the path selected rather than selecting in its own
// right, so `$['a','b'].length()` still counts as the end.
func (p *Path) markMergingUnions() {
	for i, st := range p.steps {
		if st.kind != stepUnionName {
			continue
		}
		merge := true
		for _, later := range p.steps[i+1:] {
			if later.kind != stepLength {
				merge = false
				break
			}
		}
		p.steps[i].merge = merge
	}
}

// selectsAtMostOneNode reports the definiteness of the whole path, which is the
// conjunction of its steps: one indefinite step anywhere makes the result a
// list of hits (SPEC §6.7).
func (p *Path) selectsAtMostOneNode() bool {
	for _, st := range p.steps {
		switch st.kind {
		case stepChild, stepIndex, stepLength:
			// A length is one number however many nodes it counted.
		case stepUnionName:
			if !st.merge {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// stepsTheScannerTakes reports a path the byte scanner can walk, which is a
// path built only from named members and forward array indexes.
//
// Everything else keeps the tree evaluation, where it was already correct: a
// negative index needs the array's length, which one forward pass does not have
// (see Path.Scannable), and a union or a length() produces a node the document
// does not contain, which a scanner reporting byte ranges cannot point at.
func (p *Path) stepsTheScannerTakes() bool {
	for _, st := range p.steps {
		switch st.kind {
		case stepChild:
		case stepIndex:
			if st.index < 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// dotSegment reads a segment of the dot form, which names a member unless it is
// the wildcard or is spelled as a call.
//
// `length()` is the function SPEC §6.7 commits to and the only one implemented.
// Any other call is refused here rather than compiled into a member whose name
// happens to contain brackets: a stub written against Jayway's `sum()` would
// otherwise register and then never match, which is the failure §6.7 rules out
// in so many words. A document really carrying a member spelled like a call is
// still reachable, through the bracket form the same WireMock reads as a
// literal key — `$.xs['length()']`.
func dotSegment(name string) (step, error) {
	if name == "*" {
		return step{kind: stepWildcard}, nil
	}
	fn, args, isCall := functionCall(name)
	if !isCall {
		return step{kind: stepChild, name: name}, nil
	}
	if fn == "length" && args == "" {
		return step{kind: stepLength}, nil
	}
	return step{}, fmt.Errorf("unsupported JSONPath function %q (a member with this name is selectable as ['%s'])", name, name)
}

// functionCall recognises the `name(args)` spelling of a path segment.
func functionCall(segment string) (name, args string, ok bool) {
	open := strings.IndexByte(segment, '(')
	if open <= 0 || !strings.HasSuffix(segment, ")") {
		return "", "", false
	}
	for i := 0; i < open; i++ {
		c := segment[i]
		alpha := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := i > 0 && c >= '0' && c <= '9'
		if !alpha && !digit {
			return "", "", false
		}
	}
	return segment[:open], strings.TrimSpace(segment[open+1 : len(segment)-1]), true
}

// readName reads a bare child name up to the next separator.
func readName(s string) (name, rest string, err error) {
	i := 0
	for i < len(s) && s[i] != '.' && s[i] != '[' {
		i++
	}
	if i == 0 {
		return "", "", errors.New("empty path segment")
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
//
// A filter is settled before the comma split, because a comma inside a
// predicate belongs to the predicate and splitting there would report the
// union's error for an expression that never was one.
func parseBracket(inner string) (step, error) {
	switch {
	case inner == "*":
		return step{kind: stepWildcard}, nil

	case strings.HasPrefix(inner, "?("):
		if !strings.HasSuffix(inner, ")") {
			return step{}, errors.New("unclosed filter expression")
		}
		f, err := parseFilter(strings.TrimSpace(inner[2 : len(inner)-1]))
		if err != nil {
			return step{}, err
		}
		return step{kind: stepFilter, filter: f}, nil
	}

	if members := splitOutsideQuotes(inner, ","); len(members) > 1 {
		return parseUnion(members)
	}

	switch {
	case len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"'):
		name, err := unquote(inner)
		if err != nil {
			return step{}, err
		}
		return step{kind: stepChild, name: name}, nil

	case strings.Contains(inner, ":"):
		from, to, hasTo, err := parseSlice(inner)
		if err != nil {
			return step{}, err
		}
		return step{kind: stepSlice, from: from, to: to, hasTo: hasTo}, nil

	default:
		i, err := strconv.Atoi(inner)
		if err != nil {
			return step{}, fmt.Errorf("unsupported bracket expression %q", inner)
		}
		return step{kind: stepIndex, index: i}, nil
	}
}

// parseUnion reads `[0,2]` and `['a','b']`, the two spellings SPEC §6.7 lists.
//
// The members have to agree about what they are. A mixed union has no meaning —
// a document is an array or an object, not both — and the pinned WireMock
// refuses one at registration too, so refusing it here is the answer a stub
// author already gets from the server this one stands in for.
func parseUnion(members []string) (step, error) {
	var (
		indexes []int
		names   []string
	)
	for _, member := range members {
		member = strings.TrimSpace(member)
		switch {
		case member == "":
			return step{}, errors.New("a union has an empty member")

		case member[0] == '\'' || member[0] == '"':
			name, err := unquote(member)
			if err != nil {
				return step{}, err
			}
			names = append(names, name)

		default:
			i, err := strconv.Atoi(member)
			if err != nil {
				return step{}, fmt.Errorf("a union member is neither a quoted name nor an index: %q", member)
			}
			indexes = append(indexes, i)
		}
	}

	if len(indexes) > 0 && len(names) > 0 {
		return step{}, errors.New("a union selects names or indexes, not both")
	}
	if len(names) > 0 {
		return step{kind: stepUnionName, names: names}, nil
	}
	return step{kind: stepUnionIndex, indexes: indexes}, nil
}

// unquote reads a quoted member name, which is the spelling that reaches a key
// the dot form cannot — a key holding a dot, a comma, or a name spelled like a
// function call.
func unquote(s string) (string, error) {
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return "", errors.New("unterminated quoted key")
	}
	return s[1 : len(s)-1], nil
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

	case stepUnionIndex:
		arr, ok := node.([]any)
		if !ok {
			return nil
		}
		out := make([]any, 0, len(st.indexes))
		for _, i := range st.indexes {
			if i < 0 {
				i += len(arr)
			}
			if i < 0 || i >= len(arr) {
				// An index the array does not reach drops out of the union
				// rather than emptying it, so `$.items[0,9]` still selects the
				// first item. A union is a list of chances, not a demand.
				continue
			}
			out = append(out, arr[i])
		}
		return out

	case stepUnionName:
		return applyNameUnion(st, node)

	case stepLength:
		// Counting is what length() does to a collection and the only thing it
		// does: a string has no length here, which is a Jayway rule rather than
		// an oversight, and a scalar in the way selects nothing exactly as it
		// does for a child step.
		switch t := node.(type) {
		case []any:
			return []any{float64(len(t))}
		case map[string]any:
			return []any{float64(len(t))}
		default:
			return nil
		}

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

// applyNameUnion selects the members a union names, in one of the two shapes
// markMergingUnions decided between.
//
// The merged shape keeps a member holding null, and that is what the bare form
// then turns on: `$['a','b']` over `{"a":null}` matches, because the object
// carrying it is not empty, while `$['a']` over the same document does not,
// because the node it selects IS the null. Dropping the member would collapse
// those two into one answer.
func applyNameUnion(st step, node any) []any {
	obj, ok := node.(map[string]any)
	if !ok {
		return nil
	}

	if !st.merge {
		out := make([]any, 0, len(st.names))
		for _, name := range st.names {
			if value, present := obj[name]; present {
				out = append(out, value)
			}
		}
		return out
	}

	merged := make(map[string]any, len(st.names))
	for _, name := range st.names {
		if value, present := obj[name]; present {
			merged[name] = value
		}
	}
	return []any{merged}
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
