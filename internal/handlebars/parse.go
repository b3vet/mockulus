// SPDX-License-Identifier: Apache-2.0

// Package handlebars implements the Handlebars subset mockulus response
// templates are written in.
//
// SPEC §10.1 called for vendoring an existing Go Handlebars engine and pruning
// it. This is that decision inverted, for the reason the spec gave for it: "we
// own it — only allowlisted helpers registered, no filesystem or partials from
// disk, deterministic behavior". Every one of those properties is stronger in
// an implementation that never had the capability than in one where it was
// removed. A template here cannot read a file, shell out, or resolve a partial,
// because there is no code that does those things — not because that code was
// deleted and might come back on the next upstream merge.
//
// The cost is Handlebars edge semantics, which the differential corpus is there
// to catch. The surface is deliberately small: interpolation, the four block
// helpers WireMock templates actually use, and the helper allowlist of §10.3.
//
// Templates are parsed once at stub registration. A parse error is a 422 there,
// never a surprise at serve time (P3).
package handlebars

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NodeKind distinguishes the three things a template is made of.
type NodeKind uint8

const (
	// NodeText is literal output.
	NodeText NodeKind = iota
	// NodeVar is an interpolation: {{x}} or {{{x}}}.
	NodeVar
	// NodeBlock is a block helper: {{#if x}}…{{else}}…{{/if}}.
	NodeBlock
)

// Node is one element of a parsed template.
type Node struct {
	Kind NodeKind

	// Text is the literal content of a NodeText.
	Text string

	// Expr is what a NodeVar interpolates or a NodeBlock is driven by.
	Expr *Expression
	// Escaped records that a NodeVar used {{…}} rather than {{{…}}}.
	Escaped bool

	// Body and Else are a NodeBlock's branches.
	Body []Node
	Else []Node
}

// Expression is a path, a literal, or a helper call — the three things that can
// appear inside a mustache.
type Expression struct {
	// Helper names the helper being called, empty for a bare path or literal.
	Helper string
	// Path is the dotted lookup for a bare path expression.
	Path []string
	// Literal holds a quoted string, number or boolean.
	Literal any
	// IsLiteral distinguishes a literal from an empty path.
	IsLiteral bool

	// Args are the positional arguments of a helper call.
	Args []*Expression
	// Hash are its named arguments, in source order so rendering is stable.
	Hash []HashArg
}

// HashArg is one named helper argument.
type HashArg struct {
	Key   string
	Value *Expression
}

// Template is a parsed template, ready to render with no further parsing.
type Template struct {
	// Source is the template as written, for diagnostics.
	Source string
	nodes  []Node
}

// Nodes exposes the parsed tree, for rendering.
func (t *Template) Nodes() []Node { return t.nodes }

// blockHelpers are the only helpers that may open a block. Anything else in
// block position is a mistake worth naming rather than a silently empty
// rendering.
var blockHelpers = map[string]bool{
	"if": true, "unless": true, "each": true, "with": true,
}

// Parse compiles a template. It is called at stub registration, so its errors
// become a 422 rather than a broken response later.
func Parse(source string) (*Template, error) {
	p := &parser{src: source}
	nodes, err := p.parseNodes("")
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("unexpected %q at offset %d", p.src[p.pos:], p.pos)
	}
	return &Template{Source: source, nodes: nodes}, nil
}

type parser struct {
	src string
	pos int
	// depth counts the blocks currently open; see maxBlockNesting.
	depth int
}

// maxBlockNesting bounds how deeply block helpers may be nested.
//
// The parser descends once per open block, and a stack overflow in Go is a
// fatal error rather than a panic: nothing recovers it, so the process dies and
// takes every other team's mocks with it. Measured before this bound existed, a
// template of a million nested {{#if}} — 15 MiB, inside the admin body cap —
// did exactly that. A hundred is two orders of magnitude past the deepest
// template anyone writes and three orders below where the stack runs out, so
// the refusal only ever lands on a document that was never going to render.
const maxBlockNesting = 100

// parseNodes reads until end of input, or until the closing tag of openBlock.
func (p *parser) parseNodes(openBlock string) ([]Node, error) {
	var nodes []Node

	for p.pos < len(p.src) {
		open := strings.Index(p.src[p.pos:], "{{")
		if open < 0 {
			nodes = append(nodes, Node{Kind: NodeText, Text: p.src[p.pos:]})
			p.pos = len(p.src)
			break
		}
		if open > 0 {
			nodes = append(nodes, Node{Kind: NodeText, Text: p.src[p.pos : p.pos+open]})
			p.pos += open
		}

		// {{{x}}} interpolates without HTML escaping.
		triple := strings.HasPrefix(p.src[p.pos:], "{{{")
		openLen, closeTag := 2, "}}"
		if triple {
			openLen, closeTag = 3, "}}}"
		}

		rest := p.src[p.pos+openLen:]
		close := strings.Index(rest, closeTag)
		if close < 0 {
			return nil, fmt.Errorf("unclosed {{ at offset %d", p.pos)
		}
		inner := strings.TrimSpace(rest[:close])
		next := p.pos + openLen + close + len(closeTag)

		switch {
		case inner == "":
			return nil, fmt.Errorf("empty expression at offset %d", p.pos)

		case strings.HasPrefix(inner, "!"):
			// A comment. Dropped entirely, including its surrounding mustache.
			p.pos = next

		case strings.HasPrefix(inner, "#"):
			p.pos = next
			node, err := p.parseBlock(strings.TrimSpace(inner[1:]))
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, *node)

		case strings.HasPrefix(inner, "/"):
			name := strings.TrimSpace(inner[1:])
			if openBlock == "" {
				return nil, fmt.Errorf("{{/%s}} closes a block that was never opened", name)
			}
			if name != openBlock {
				return nil, fmt.Errorf("{{/%s}} does not close {{#%s}}", name, openBlock)
			}
			p.pos = next
			return nodes, nil

		case inner == "else":
			if openBlock == "" {
				return nil, fmt.Errorf("{{else}} outside a block at offset %d", p.pos)
			}
			// Left for parseBlock to see.
			return nodes, nil

		default:
			expr, err := parseExpression(inner)
			if err != nil {
				return nil, fmt.Errorf("%w (in %q)", err, inner)
			}
			nodes = append(nodes, Node{Kind: NodeVar, Expr: expr, Escaped: !triple})
			p.pos = next
		}
	}

	if openBlock != "" {
		return nil, fmt.Errorf("unclosed {{#%s}}", openBlock)
	}
	return nodes, nil
}

// parseBlock reads a block helper's arguments and both branches.
func (p *parser) parseBlock(header string) (*Node, error) {
	if p.depth >= maxBlockNesting {
		return nil, fmt.Errorf("blocks may not nest more than %d deep", maxBlockNesting)
	}
	p.depth++
	defer func() { p.depth-- }()

	expr, err := parseExpression(header)
	if err != nil {
		return nil, fmt.Errorf("%w (in block {{#%s}})", err, header)
	}
	name := expr.Helper
	if name == "" && len(expr.Path) > 0 {
		// {{#x}} with no helper is a shorthand; treat the path as `with`.
		name = "with"
		expr = &Expression{Helper: "with", Args: []*Expression{{Path: expr.Path}}}
	}
	if !blockHelpers[name] {
		return nil, fmt.Errorf("%q cannot open a block; block helpers are if, unless, each and with", name)
	}

	body, err := p.parseNodes(name)
	if err != nil {
		return nil, err
	}

	node := &Node{Kind: NodeBlock, Expr: expr, Body: body}

	// parseNodes stops at {{else}} without consuming it.
	if strings.HasPrefix(strings.TrimSpace(p.remaining()), "{{else}}") {
		p.consumeElse()
		elseBody, err := p.parseNodes(name)
		if err != nil {
			return nil, err
		}
		node.Else = elseBody
	}
	return node, nil
}

func (p *parser) remaining() string { return p.src[p.pos:] }

func (p *parser) consumeElse() {
	if i := strings.Index(p.src[p.pos:], "{{else}}"); i >= 0 {
		p.pos += i + len("{{else}}")
	}
}

// parseExpression reads the inside of a mustache: a path, a literal, or a
// helper call with positional and named arguments.
func parseExpression(src string) (*Expression, error) { return parseCall(src, false) }

// parseCall is parseExpression plus the one thing that tells a subexpression
// apart from a mustache: parentheses are a call whatever stands inside them.
//
// `{{now}}` is genuinely ambiguous — a variable named `now` is a legal reading
// of it — and only the helper registry can settle which was meant, so that
// decision is deferred to BindBareHelpers at compile time. `(now)` is not
// ambiguous: Handlebars gives a parenthesised name no other meaning, and the
// name resolves against the helper registry alone. Settling it in the parser is
// what makes a subexpression naming a helper mockulus does not have a 422 at
// registration rather than an expression that renders as nothing on every
// request (P3).
func parseCall(src string, parenthesised bool) (*Expression, error) {
	tokens, err := tokenizeArgs(src)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("empty expression")
	}

	// A single token is a bare path or literal in a mustache, and the name of a
	// no-argument helper inside parentheses.
	if len(tokens) == 1 && !strings.Contains(tokens[0], "=") {
		if lit, isLit := literalValue(tokens[0]); isLit {
			return &Expression{Literal: lit, IsLiteral: true}, nil
		}
		if parenthesised {
			return &Expression{Helper: tokens[0]}, nil
		}
		return parseOperand(tokens[0])
	}

	expr := &Expression{Helper: tokens[0]}
	if lit, isLit := literalValue(tokens[0]); isLit {
		// A literal in head position is not a call.
		return &Expression{Literal: lit, IsLiteral: true}, nil
	}

	for _, tok := range tokens[1:] {
		key, value, isHash := splitHashArg(tok)
		operand, err := parseOperand(value)
		if err != nil {
			return nil, err
		}
		if isHash {
			expr.Hash = append(expr.Hash, HashArg{Key: key, Value: operand})
			continue
		}
		expr.Args = append(expr.Args, operand)
	}
	return expr, nil
}

// splitHashArg separates key=value, respecting quotes so an `=` inside a string
// is not mistaken for the separator.
func splitHashArg(tok string) (key, value string, isHash bool) {
	inQuote := byte(0)
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '\'' || c == '"':
			inQuote = c
		case c == '=':
			return tok[:i], tok[i+1:], true
		}
	}
	return "", tok, false
}

func parseOperand(tok string) (*Expression, error) {
	if tok == "" {
		return nil, errors.New("empty operand")
	}
	// A subexpression: {{#each (range 1 3)}} passes one helper's result to
	// another, which is how Handlebars composes without temporaries.
	if len(tok) >= 2 && tok[0] == '(' && tok[len(tok)-1] == ')' {
		return parseCall(strings.TrimSpace(tok[1:len(tok)-1]), true)
	}
	if lit, ok := literalValue(tok); ok {
		return &Expression{Literal: lit, IsLiteral: true}, nil
	}
	return &Expression{Path: splitPath(tok)}, nil
}

// literalValue recognises the literal forms Handlebars allows as arguments.
func literalValue(tok string) (any, bool) {
	if len(tok) >= 2 {
		if (tok[0] == '\'' && tok[len(tok)-1] == '\'') || (tok[0] == '"' && tok[len(tok)-1] == '"') {
			return unescapeQuoted(tok[1 : len(tok)-1]), true
		}
	}
	switch tok {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null", "undefined":
		return nil, true
	}
	if n, err := strconv.ParseFloat(tok, 64); err == nil {
		return n, true
	}
	return nil, false
}

// unescapeQuoted handles the backslash escapes a quoted argument may carry.
// WireMock templates use these heavily in `now` format strings.
func unescapeQuoted(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			sb.WriteByte(s[i])
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// ParentSegment is how a path spells a step out of the current scope. It is a
// segment rather than a prefix the parser strips, because how many of them a
// path opens with is the whole meaning of `../../x` and nothing downstream can
// recover the count once they are gone.
const ParentSegment = ".."

// splitPath breaks a dotted path into segments, honouring the [n] index form
// and the [literal segment] escape.
//
// Both separators Handlebars accepts are separators here: `a.b` and `a/b` name
// the same two segments. The slash form only ever appears in real templates as
// part of `../`, and treating it as an ordinary character is what used to make
// `{{../request.method}}` a lookup of a member called "/request" — a name no
// model has, so the expression rendered as nothing rather than reaching the
// enclosing scope it names.
func splitPath(tok string) []string {
	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}

	for i := 0; i < len(tok); i++ {
		// `..` is a segment of its own wherever a segment may start, and the
		// separator that follows it may be written either way. Anything else
		// beginning with two dots — a member genuinely named "..x" — is left to
		// the ordinary rules below.
		if cur.Len() == 0 && strings.HasPrefix(tok[i:], ParentSegment) &&
			(len(tok) == i+2 || tok[i+2] == '/' || tok[i+2] == '.') {
			out = append(out, ParentSegment)
			// Two for the dots; the loop's own increment steps over the
			// separator that ends the segment.
			i += 2
			continue
		}

		switch tok[i] {
		case '.', '/':
			flush()
		case '[':
			flush()
			end := strings.IndexByte(tok[i:], ']')
			if end < 0 {
				cur.WriteByte(tok[i])
				continue
			}
			out = append(out, tok[i+1:i+end])
			i += end
		default:
			cur.WriteByte(tok[i])
		}
	}
	flush()
	return out
}

// tokenizeArgs splits a mustache body on whitespace, keeping quoted runs and
// bracketed path segments together.
func tokenizeArgs(src string) ([]string, error) {
	var (
		tokens []string
		cur    strings.Builder
		quote  byte
		depth  int
	)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				cur.WriteByte(src[i])
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '[' || c == '(':
			depth++
			cur.WriteByte(c)
		case c == ']' || c == ')':
			depth--
			cur.WriteByte(c)
		case (c == ' ' || c == '\t' || c == '\n' || c == '\r') && depth == 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted argument")
	}
	flush()
	return tokens, nil
}
