// SPDX-License-Identifier: Apache-2.0

package handlebars

import (
	"encoding/json"
	"errors"
	"fmt"
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
		// Both mustache forms write the value as it stringifies, so {{x}} and
		// {{{x}}} differ only in how many braces were typed. Stock Handlebars
		// escapes the double form for HTML; a mock server renders bodies that
		// are far more often JSON or XML than markup, and there escaping is
		// plain corruption — a name that arrived as `Ben & Jerry <fine>` would
		// be served as `Ben &amp; Jerry &lt;fine&gt;`, which no client parsing
		// the response can turn back into what was sent. The request model is
		// not spared either: `{{request.url}}` on any query with two parameters
		// would hand back a URL nothing can follow, for an ampersand nobody
		// typed.
		//
		// Escaping is also not something this engine could get right by
		// switching it on. WireMock runs its response transformer with escaping
		// off, and the escaper Go ships spells a quote `&#34;` and an apostrophe
		// `&#39;` where Java's writes `&quot;` and `&#x27;` — so the choice is
		// between rendering raw, as the server being matched does, and inventing
		// a third spelling that agrees with neither.
		//
		// Node.Escaped stays on the node: which form was written is still a fact
		// about the template, and the parser is the place that knows it.
		return r.write(Stringify(value))

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
		return r.eachValue(n, items)

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
		// A value that is a scalar and a list at once — the shape a header or
		// query parameter that arrived more than once takes — iterates as its
		// values. Everything else has nothing to walk, and an {{#each}} over it
		// takes the else branch.
		if list, ok := value.(Lister); ok {
			return r.eachValue(n, list.List())
		}
		return r.nodes(n.Else)
	}
}

// eachValue iterates a list of values. Both the plain list and the multi-valued
// scalar arrive here, so the two cannot drift apart in how they number their
// iterations or where they leave @first and @last.
func (r *renderer) eachValue(n *Node, items []any) error {
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
//
// A path may open with any number of `..` segments, each of which starts the
// walk one frame further out. That is the only way a template can name the
// enclosing scope when the inner one carries the same key: two nested {{#with}}
// blocks over objects that both have a `city` want `{{city}}` and
// `{{../city}}` to answer differently, and a reading that dropped the `..` and
// let the ordinary fall-through find the name would answer both with the inner
// value — plausibly, and wrongly, in the one case the syntax exists for.
//
// A climb that outruns the stack resolves to nothing rather than stopping at
// the outermost frame. Clamping would hand `{{../request.method}}` written at
// the top level the root context and render a value there, where the template
// asked for a scope that does not exist; WireMock 3.13.2 renders nothing for
// both that and `{{../../../../x}}` one frame deep, and nothing is also the
// answer that cannot be mistaken for a successful lookup.
func (r *renderer) lookup(path []string) any {
	up := 0
	for up < len(path) && path[up] == ParentSegment {
		up++
	}
	path = path[up:]

	frame := len(r.stack) - 1 - up
	if frame < 0 {
		return nil
	}

	if len(path) == 0 || (len(path) == 1 && (path[0] == "this" || path[0] == ".")) {
		return unwrapScope(r.stack[frame])
	}

	for i := frame; i >= 0; i-- {
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

// Lister is a value that is both a scalar and a list — a query parameter or a
// header that arrived more than once, which renders as its first value and
// indexes to any of them.
//
// It is a third nature on the value rather than a plain list because the two it
// already has are load-bearing: a list would stringify as a list, and every
// stub reading a single-valued parameter would start serving "[gold]" where it
// used to serve "gold". Only {{#each}} asks for this one, and only a value that
// answers it can be walked.
type Lister interface {
	List() []any
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
//
// A list or an object renders as JSON, because the only way one reaches this
// function is a path expression that selected a subtree of the request body,
// and a stub echoing a subtree wants the document back. Go's own spelling of a
// map — `map[city:london name:ada]` — is not a serialisation of anything; it is
// an internal spelled onto the wire, and no client parsing the response can
// read it. WireMock renders the same selection as JSON, so this is the answer
// the oracle gives as well as the one a caller can use.
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
	case []any:
		return stringifyJSON(t)
	case map[string]any:
		return stringifyJSON(t)
	case []string:
		// The model's own segment list, which is the one collection here that
		// did not come out of a document — nothing that decodes a body produces
		// this type — and whose joined spelling cov-tmpl-request-model-001
		// records. WireMock renders `{{request.pathSegments}}` as the path
		// rather than as either spelling of a list, which is a disagreement
		// about what that node is and is not settled by how a list prints.
		return strings.Join(t, ", ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// stringifyJSON encodes a selected subtree.
//
// Escaping is off for the same reason it is off everywhere else in this file: a
// body carrying `Ben & Jerry <fine>` must arrive as it was sent, and unless it
// is told otherwise the encoder writes those two characters as the six-byte
// escapes for them — valid JSON, and not the bytes WireMock writes or the ones
// the stub author put into the request.
//
// A value that will not encode falls back rather than failing the render.
// Nothing a decoded request body holds can reach that branch, and a helper that
// returned something exotic is not a reason to refuse the whole response.
func stringifyJSON(v any) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	// Encode terminates the document with a newline that no interpolation wants.
	return strings.TrimSuffix(buf.String(), "\n")
}

// BindBareHelpers rewrites a lone path that names a registered helper into a
// call of it, which is what `{{now}}` and `{{randomValue}}` are.
//
// One token inside a mustache has two readings — a member of the context called
// `now`, or the `now` helper invoked with no arguments — and the parser cannot
// choose between them, because the registry that settles it lives a package
// away. Handlebars settles it by asking whether a helper of that name exists,
// and so does WireMock 3.13.2: inside a scope carrying its own `now`, the
// helper's timestamp is what renders, not the member. Left as a path, the most
// commonly written expression in any WireMock template renders as nothing at
// all — no registration error and no serve-time error, just an empty span where
// a timestamp should be.
//
// It happens once, here, rather than on the render path, because the answer
// cannot change between requests: which names are helpers is fixed when the
// engine is built. Asking per interpolation would put a map lookup in front of
// every variable in every templated response for a question already answered
// (§16.3 rule 2).
//
// Only the mustache position is rewritten. An argument is a path in Handlebars
// even where a helper shares its name — `{{concat now '!'}}` passes the member,
// not the timestamp — so the walk deliberately does not descend into Args or
// Hash. A subexpression is the other spelling of a call and the parser has
// already resolved that one.
func (t *Template) BindBareHelpers(known func(name string) bool) {
	bindBareHelpers(t.nodes, known)
}

func bindBareHelpers(nodes []Node, known func(name string) bool) {
	for i := range nodes {
		n := &nodes[i]
		if n.Kind == NodeVar && n.Expr != nil && n.Expr.Helper == "" && !n.Expr.IsLiteral &&
			len(n.Expr.Path) == 1 && known(n.Expr.Path[0]) {
			n.Expr = &Expression{Helper: n.Expr.Path[0]}
		}
		bindBareHelpers(n.Body, known)
		bindBareHelpers(n.Else, known)
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
