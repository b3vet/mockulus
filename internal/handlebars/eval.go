// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

// Rendering walks the parsed tree. Nothing here can reach outside the context
// it is given and the helpers it was registered with, which is what makes the
// sandbox structural rather than a matter of pruning (SPEC §17).

// ErrOutputTooLarge reports that a render exceeded its output cap. A template
// that expands without bound is a memory-exhaustion vector, so the cap is
// enforced during rendering rather than checked afterwards.
//
// The message names the knob because this error reaches an operator as a
// response body: a stub whose expansion depends on the request body can be
// driven over the cap by a caller, and "which setting refused this" is the
// first thing whoever is looking at that 500 needs.
var ErrOutputTooLarge = errors.New("template output exceeds template_max_output_bytes")

// Helper is a template helper. Returning an error aborts the render, which the
// caller turns into WireMock's render-error-in-body behavior.
type Helper func(args []any, hash map[string]any) (any, error)

// Registry holds the allowlisted helpers. A template can call nothing else:
// there is no fallback to a global namespace and no reflection into the
// context (SPEC §10.3).
type Registry struct {
	helpers map[string]Helper
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{helpers: map[string]Helper{}} }

// Register adds a helper.
func (r *Registry) Register(name string, fn Helper) { r.helpers[name] = fn }

// Has reports whether a helper is registered, which is what lets an unknown
// helper be rejected at stub registration rather than at serve time.
func (r *Registry) Has(name string) bool {
	_, ok := r.helpers[name]
	return ok
}

// Names returns the registered helper names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.helpers))
	for name := range r.helpers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RenderOptions configure one render.
type RenderOptions struct {
	// MaxOutput caps the rendered size; zero means unbounded.
	MaxOutput int
}

// Render evaluates a template against a context.
func (t *Template) Render(ctx any, reg *Registry, opts RenderOptions) (string, error) {
	r := &renderer{reg: reg, max: opts.MaxOutput}
	r.push(ctx)
	if err := r.nodes(t.nodes); err != nil {
		return "", err
	}
	return r.out.String(), nil
}

type renderer struct {
	reg   *Registry
	out   strings.Builder
	max   int
	stack []any
}

func (r *renderer) push(ctx any) { r.stack = append(r.stack, ctx) }
func (r *renderer) pop()         { r.stack = r.stack[:len(r.stack)-1] }

func (r *renderer) current() any {
	if len(r.stack) == 0 {
		return nil
	}
	return r.stack[len(r.stack)-1]
}

func (r *renderer) write(s string) error {
	if r.max > 0 && r.out.Len()+len(s) > r.max {
		return fmt.Errorf("%w (%d bytes)", ErrOutputTooLarge, r.max)
	}
	r.out.WriteString(s)
	return nil
}

func (r *renderer) nodes(nodes []Node) error {
	for i := range nodes {
		if err := r.node(&nodes[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) node(n *Node) error {
	switch n.Kind {
	case NodeText:
		return r.write(n.Text)

	case NodeVar:
		value, err := r.eval(n.Expr)
		if err != nil {
			return err
		}
		s := Stringify(value)
		if n.Escaped {
			s = html.EscapeString(s)
		}
		return r.write(s)

	case NodeBlock:
		return r.block(n)
	}
	return nil
}

func (r *renderer) block(n *Node) error {
	switch n.Expr.Helper {
	case "if", "unless":
		if len(n.Expr.Args) != 1 {
			return fmt.Errorf("{{#%s}} takes exactly one argument", n.Expr.Helper)
		}
		value, err := r.eval(n.Expr.Args[0])
		if err != nil {
			return err
		}
		truthy := Truthy(value)
		if n.Expr.Helper == "unless" {
			truthy = !truthy
		}
		if truthy {
			return r.nodes(n.Body)
		}
		return r.nodes(n.Else)

	case "each":
		if len(n.Expr.Args) != 1 {
			return errors.New("{{#each}} takes exactly one argument")
		}
		value, err := r.eval(n.Expr.Args[0])
		if err != nil {
			return err
		}
		return r.each(n, value)

	case "with":
		if len(n.Expr.Args) != 1 {
			return errors.New("{{#with}} takes exactly one argument")
		}
		value, err := r.eval(n.Expr.Args[0])
		if err != nil {
			return err
		}
		if !Truthy(value) {
			return r.nodes(n.Else)
		}
		r.push(value)
		defer r.pop()
		return r.nodes(n.Body)

	default:
		return fmt.Errorf("unknown block helper %q", n.Expr.Helper)
	}
}

// each iterates a slice or a map. A map iterates in sorted key order, because a
// rendered response that changes between identical requests is a mock nobody
// can assert on.
func (r *renderer) each(n *Node, value any) error {
	switch items := value.(type) {
	case []any:
		if len(items) == 0 {
			return r.nodes(n.Else)
		}
		for i, item := range items {
			r.push(eachScope(item, i, len(items)))
			err := r.nodes(n.Body)
			r.pop()
			if err != nil {
				return err
			}
		}
		return nil

	case []string:
		if len(items) == 0 {
			return r.nodes(n.Else)
		}
		for i, item := range items {
			r.push(eachScope(item, i, len(items)))
			err := r.nodes(n.Body)
			r.pop()
			if err != nil {
				return err
			}
		}
		return nil

	case map[string]any:
		if len(items) == 0 {
			return r.nodes(n.Else)
		}
		keys := make([]string, 0, len(items))
		for k := range items {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			scope := eachScope(items[k], i, len(keys))
			scope["@key"] = k
			r.push(scope)
			err := r.nodes(n.Body)
			r.pop()
			if err != nil {
				return err
			}
		}
		return nil

	default:
		return r.nodes(n.Else)
	}
}

// eachScope wraps an iteration item with the @-variables Handlebars exposes.
func eachScope(item any, index, total int) map[string]any {
	return map[string]any{
		"this":   item,
		".":      item,
		"@index": index,
		"@first": index == 0,
		"@last":  index == total-1,
	}
}

// eval resolves an expression to a value.
func (r *renderer) eval(e *Expression) (any, error) {
	if e == nil {
		return nil, nil
	}
	if e.IsLiteral {
		return e.Literal, nil
	}
	if e.Helper != "" {
		return r.call(e)
	}
	return r.lookup(e.Path), nil
}

func (r *renderer) call(e *Expression) (any, error) {
	fn, ok := r.reg.helpers[e.Helper]
	if !ok {
		// Registration already rejected unknown helpers, so reaching here means
		// the registry and the validator disagree — worth saying plainly.
		return nil, fmt.Errorf("unknown helper %q", e.Helper)
	}

	args := make([]any, 0, len(e.Args))
	for _, arg := range e.Args {
		value, err := r.eval(arg)
		if err != nil {
			return nil, err
		}
		args = append(args, value)
	}

	var hash map[string]any
	if len(e.Hash) > 0 {
		hash = make(map[string]any, len(e.Hash))
		for _, h := range e.Hash {
			value, err := r.eval(h.Value)
			if err != nil {
				return nil, err
			}
			hash[h.Key] = value
		}
	}

	return fn(args, hash)
}

// lookup walks a dotted path through the scope stack, innermost first, so an
// {{#each}} body can still reach the enclosing context.
func (r *renderer) lookup(path []string) any {
	if len(path) == 0 {
		return r.current()
	}
	if len(path) == 1 && (path[0] == "this" || path[0] == ".") {
		return unwrapScope(r.current())
	}

	for i := len(r.stack) - 1; i >= 0; i-- {
		if value, ok := resolvePath(r.stack[i], path); ok {
			return value
		}
	}
	return nil
}

// unwrapScope returns the item an each-scope wraps, or the scope itself.
func unwrapScope(scope any) any {
	if m, ok := scope.(map[string]any); ok {
		if item, present := m["this"]; present {
			return item
		}
	}
	return scope
}

// Lookuper is a value that is both a scalar and a container — a request path
// that renders as "/a/b" but also indexes to its segments. Declared here rather
// than in the template package so the evaluator does not depend on it.
type Lookuper interface {
	Lookup(key string) (any, bool)
}

// resolvePath walks one context. Reporting whether it resolved, rather than
// returning nil, is what lets the caller fall through to an outer scope instead
// of stopping at the first context that lacks the key.
func resolvePath(ctx any, path []string) (any, bool) {
	current := ctx
	for _, segment := range path {
		if l, ok := current.(Lookuper); ok {
			value, found := l.Lookup(segment)
			if !found {
				return nil, false
			}
			current = value
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = value

		case map[string]string:
			value, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = value

		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			current = node[i]

		case []string:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			current = node[i]

		default:
			return nil, false
		}
	}
	return current, true
}

// Truthy is Handlebars' emptiness rule: false, nil, zero, the empty string and
// empty collections are falsy, everything else is truthy.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	case map[string]string:
		return len(t) > 0
	default:
		return true
	}
}

// Stringify renders a value for output.
//
// Numbers matter here: a JSON body carrying {{someCount}} must not produce
// "3e+06", and an integral float must render without a trailing ".0" or a stub
// author cannot write a JSON integer at all.
func Stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) && t < 1e15 && t > -1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case []string:
		return strings.Join(t, ", ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// Helpers reports every helper name a template calls, so registration can
// reject an unknown one before the stub is stored (SPEC §10.1, P3).
func (t *Template) Helpers() []string {
	seen := map[string]bool{}
	collectHelpers(t.nodes, seen)

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func collectHelpers(nodes []Node, seen map[string]bool) {
	for i := range nodes {
		n := &nodes[i]
		collectExprHelpers(n.Expr, seen)
		collectHelpers(n.Body, seen)
		collectHelpers(n.Else, seen)
	}
}

func collectExprHelpers(e *Expression, seen map[string]bool) {
	if e == nil {
		return
	}
	if e.Helper != "" && !blockHelpers[e.Helper] {
		seen[e.Helper] = true
	}
	for _, arg := range e.Args {
		collectExprHelpers(arg, seen)
	}
	for _, h := range e.Hash {
		collectExprHelpers(h.Value, seen)
	}
}
