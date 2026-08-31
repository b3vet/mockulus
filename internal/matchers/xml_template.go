// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
)

// The three SPEC §10.3 helpers that read XML.
//
// They live beside the matchers rather than in internal/template because they
// must share exactly one XML parser with them. ParseXML declares external
// entities away (§17), and a second parser reached through a template would be
// a second place for that to stop being true — the XXE corpus case would still
// pass while the hole was open. The template package receives them as injected
// helpers, the way it already receives `jsonPath` from the matcher engine's
// JSONPath, so there is one definition of what an expression means per language.

// XPathHelper implements `{{xPath <document> <expression>}}`.
//
// The expression is compiled with the library directly rather than through
// CompileXPath, which is deliberate and is not an oversight: CompileXPath
// refuses an expression that returns a value rather than a node-set, because as
// a *matcher* such a criterion can never fail (deviation #59). As a *helper*
// the same expression is meaningful and useful — `count(//item)` renders `2` on
// WireMock — so the guard would refuse something that has a correct answer.
func XPathHelper(args []any, _ map[string]any) (any, error) {
	doc, expr, err := xmlHelperArgs("xPath", args)
	if err != nil {
		return nil, err
	}
	compiled, err := xpath.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("xPath: %s is not a valid XPath expression: %w", quote(expr), err)
	}
	return renderXPathResult(compiled, doc), nil
}

// soapStepFunction matches an expression whose first step is a function call.
var soapStepFunction = regexp.MustCompile(`^\s*/*\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\(`)

// xpathNodeTests are the four calls that are legitimately node tests rather
// than functions, so `text()` as the whole expression stays legal.
var xpathNodeTests = map[string]bool{
	"text": true, "node": true, "comment": true, "processing-instruction": true,
}

// soapFunctionHead returns the name of a leading function call, or "" if the
// expression opens with something that can serve as a location step.
func soapFunctionHead(expr string) string {
	m := soapStepFunction.FindStringSubmatch(expr)
	if m == nil || xpathNodeTests[m[1]] {
		return ""
	}
	return m[1]
}

// soapBodyPath locates the SOAP Body without caring which prefix the document
// binds the envelope namespace to; `soap:`, `S:` and `env:` are all common.
const soapBodyPath = `/*[local-name()='Envelope']/*[local-name()='Body']`

// SOAPXPathHelper implements `{{soapXPath <document> <expression>}}`.
//
// WireMock reaches the payload by *concatenating* the Body's path, a slash and
// the caller's expression, and the seams of that show through in ways worth
// reproducing exactly rather than approximating (PROBE_XML.md P10):
//
//   - a leading `/` becomes a descendant step, so `/id/text()` finds an `id`
//     anywhere below the Body rather than only a direct child;
//   - a leading `//` produces `///`, which does not parse, so those expressions
//     are an error rather than a search;
//   - a function call lands where a step belongs, so `count(...)` is an error
//     here even though it is legal in `xPath`.
//
// Describing this as "an expression relative to the Body's content element" is
// the natural reading and produces the wrong answer for all three. An earlier
// revision of the probe notes said exactly that, and `/id/text()` is what
// disproves it.
func SOAPXPathHelper(args []any, _ map[string]any) (any, error) {
	doc, expr, err := xmlHelperArgs("soapXPath", args)
	if err != nil {
		return nil, err
	}
	if fn := soapFunctionHead(expr); fn != "" {
		// WireMock's concatenation puts this where a location step belongs and
		// Java's parser rejects it. Go's accepts it — as a *name test*, so
		// `count(/req/id)` silently selects an element called `count` and
		// renders the empty string. Reproducing the refusal is what keeps a
		// mistake loud (P3); inheriting the library's tolerance would answer a
		// stub author's arithmetic with blank output and no reason.
		return nil, fmt.Errorf("soapXPath: %s is evaluated as a step below the SOAP Body, "+
			"so it cannot begin with the function call %s; select nodes instead, "+
			"or use xPath for a value expression", quote(expr), quote(fn+"()"))
	}
	compiled, err := xpath.Compile(soapBodyPath + "/" + expr)
	if err != nil {
		return nil, fmt.Errorf("soapXPath: %s is not a valid XPath expression here: %w"+
			"; it is evaluated as a step below the SOAP Body, so it may not begin with // "+
			"or be a function call", quote(expr), err)
	}
	return renderXPathResult(compiled, doc), nil
}

// FormatXMLHelper implements `{{formatXml <document>}}`.
func FormatXMLHelper(args []any, _ map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("formatXml takes a document")
	}
	doc, err := helperDocument("formatXml", args[0])
	if err != nil {
		return nil, err
	}
	return FormatXML(doc), nil
}

// xmlHelperArgs is the shared argument contract: a document then an expression.
func xmlHelperArgs(name string, args []any) (*xmlquery.Node, string, error) {
	if len(args) < 2 {
		return nil, "", fmt.Errorf("%s takes a document and an expression", name)
	}
	expr, ok := args[1].(string)
	if !ok {
		return nil, "", fmt.Errorf("the %s expression must be a string", name)
	}
	if strings.TrimSpace(expr) == "" {
		return nil, "", fmt.Errorf("the %s expression cannot be empty", name)
	}
	doc, err := helperDocument(name, args[0])
	if err != nil {
		return nil, "", err
	}
	return doc, expr, nil
}

// helperDocument parses the first argument, which for `request.body` is the raw
// string the caller sent.
//
// A body that is not XML is a serve-time error, which the response carries as
// text (SPEC §10.4). WireMock echoes the entire body back inside its message;
// the body is the caller's own and is going back to that same caller, so it
// discloses nothing, but repeating a whole document into an error line is noise
// rather than diagnosis and this says what failed instead.
func helperDocument(name string, v any) (*xmlquery.Node, error) {
	text, ok := v.(string)
	if !ok {
		text = fmt.Sprint(v)
	}
	doc, parsed := ParseXML(text)
	if !parsed {
		return nil, fmt.Errorf("%s: the document is not valid XML", name)
	}
	return doc, nil
}

// renderXPathResult turns an evaluated expression into the text a template
// renders, following WireMock: a node-set contributes its *first* node only, an
// element serializes through the same printer `formatXml` uses, and an empty
// node-set is the empty string rather than an error.
func renderXPathResult(compiled *xpath.Expr, doc *xmlquery.Node) string {
	switch v := compiled.Evaluate(xmlquery.CreateXPathNavigator(doc)).(type) {
	case *xpath.NodeIterator:
		if !v.MoveNext() {
			return ""
		}
		return renderXPathNode(v.Current())
	case float64:
		return formatXPathNumber(v)
	case bool:
		return strconv.FormatBool(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// renderXPathNode renders a selected node. An element becomes serialized XML;
// everything else — text, CDATA, an attribute — becomes its value.
//
// The element test is made on the *navigator*, not on the node behind it: when
// an expression selects an attribute the navigator sits on the attribute while
// the node behind it is still the owning element, so testing the node would
// serialize the whole element where `//item/@sku` must render `A`.
func renderXPathNode(nav xpath.NodeNavigator) string {
	if nav.NodeType() != xpath.ElementNode {
		return nav.Value()
	}
	q, ok := nav.(*xmlquery.NodeNavigator)
	if !ok {
		return nav.Value()
	}
	return FormatXML(q.Current())
}

// formatXPathNumber renders a numeric XPath result without locale grouping.
//
// This is deviation #62. WireMock formats through Java's default NumberFormat,
// which follows the *JVM's* locale rather than anything about the document:
// the same stub and the same request render `2,000,000` on one container and
// `2.000.000` on the same image started with `-Duser.language=de`. There is no
// stable wire behaviour to match, only whichever locale the oracle happened to
// boot in, so this renders the digits and nothing else.
func formatXPathNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// FormatXML renders a parsed document the way WireMock's `formatXml` helper
// does, which `antchfx/xmlquery`'s own indenting printer does not: it emits an
// XML declaration, omits the trailing newline, leaves comments on the parent's
// line, doubles pre-existing whitespace and keeps mixed text inline
// (PROBE_XML.md P10). Every rule below is a recorded row of that table.
func FormatXML(n *xmlquery.Node) string {
	var sb strings.Builder
	if n.Type == xmlquery.DocumentNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xmlquery.DeclarationNode || insignificant(c) {
				continue
			}
			writeXMLNode(&sb, c, 0)
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	writeXMLNode(&sb, n, 0)
	sb.WriteByte('\n')
	return sb.String()
}

// insignificant reports whether a node contributes nothing to the output.
// Whitespace-only text is layout from the source document rather than content,
// which is why re-formatting an already-indented document does not double its
// indentation.
func insignificant(n *xmlquery.Node) bool {
	return n.Type == xmlquery.TextNode && strings.TrimSpace(n.Data) == ""
}

func writeXMLNode(sb *strings.Builder, n *xmlquery.Node, depth int) {
	indent := strings.Repeat("  ", depth)
	switch n.Type {
	case xmlquery.TextNode:
		sb.WriteString(indent)
		sb.WriteString(escapeXMLText(strings.TrimSpace(n.Data)))
	case xmlquery.CharDataNode:
		sb.WriteString(indent)
		sb.WriteString("<![CDATA[" + n.Data + "]]>")
	case xmlquery.CommentNode:
		sb.WriteString(indent)
		sb.WriteString("<!--" + n.Data + "-->")
	case xmlquery.ProcessingInstruction:
		sb.WriteString(indent)
		sb.WriteString("<?" + procInstText(n) + "?>")
	case xmlquery.ElementNode:
		writeXMLElement(sb, n, depth, indent)
	default:
		sb.WriteString(indent)
		sb.WriteString(escapeXMLText(strings.TrimSpace(n.Data)))
	}
}

func writeXMLElement(sb *strings.Builder, n *xmlquery.Node, depth int, indent string) {
	name := elementName(n)
	sb.WriteString(indent + "<" + name)
	for _, a := range n.Attr {
		sb.WriteString(" " + attrName(a) + `="` + escapeXMLAttr(a.Value) + `"`)
	}

	kids := significantChildren(n)
	if len(kids) == 0 {
		// An element with no content self-closes, and so does one holding only
		// whitespace: `<x></x>` and `<r>   </r>` both render `<x/>`-style.
		sb.WriteString("/>")
		return
	}
	if allCharacterData(kids) {
		// Text-only content stays on the element's own line, untrimmed, so a
		// tab inside a value survives a round trip through the helper.
		sb.WriteString(">")
		for _, c := range kids {
			if c.Type == xmlquery.CharDataNode {
				sb.WriteString("<![CDATA[" + c.Data + "]]>")
				continue
			}
			sb.WriteString(escapeXMLText(c.Data))
		}
		sb.WriteString("</" + name + ">")
		return
	}
	sb.WriteString(">\n")
	for _, c := range kids {
		writeXMLNode(sb, c, depth+1)
		sb.WriteByte('\n')
	}
	sb.WriteString(indent + "</" + name + ">")
}

func significantChildren(n *xmlquery.Node) []*xmlquery.Node {
	var out []*xmlquery.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if insignificant(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// allCharacterData reports whether every child is text or CDATA, which is what
// decides between a one-line element and an expanded one.
func allCharacterData(kids []*xmlquery.Node) bool {
	for _, c := range kids {
		if c.Type != xmlquery.TextNode && c.Type != xmlquery.CharDataNode {
			return false
		}
	}
	return true
}

func elementName(n *xmlquery.Node) string {
	if n.Prefix != "" {
		return n.Prefix + ":" + n.Data
	}
	return n.Data
}

func attrName(a xmlquery.Attr) string {
	if a.Name.Space != "" {
		return a.Name.Space + ":" + a.Name.Local
	}
	return a.Name.Local
}

func procInstText(n *xmlquery.Node) string {
	if n.ProcInst == nil {
		return n.Data
	}
	if n.ProcInst.Inst == "" {
		return n.ProcInst.Target
	}
	return n.ProcInst.Target + " " + n.ProcInst.Inst
}

// escapeXMLText and escapeXMLAttr reproduce the oracle's escaping exactly: text
// escapes `&`, `<` and `>` and leaves tabs alone; an attribute escapes `&`, `<`
// and `"`, leaves `'` alone, and renders a newline numerically so the value
// survives a line-oriented reader.
func escapeXMLText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func escapeXMLAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;", "\n", "&#10;").Replace(s)
}
