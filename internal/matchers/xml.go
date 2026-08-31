// SPDX-License-Identifier: Apache-2.0

package matchers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
)

// EqualToXML compares the subject to an expected document structurally.
//
// "Structurally" is a specific set of rules, and they were probed against
// pinned WireMock 3.13.2 rather than assumed, because most of them are not the
// obvious reading:
//
//   - Attribute order is not significant; self-closing and empty elements are
//     the same; whitespace-only text between elements is ignorable and text is
//     compared trimmed.
//   - Comments and the XML declaration are ignored. A processing instruction
//     inside the document is not.
//   - Namespaces compare by URI. The prefix is a local alias and two documents
//     using different prefixes for the same URI are equal.
//   - **Children are paired by element name, and within a name group by
//     document order.** Differently-named siblings are therefore order-
//     insensitive — `<x/><y/>` equals `<y/><x/>` — while same-named ones are
//     not: `<x>1</x><x>2</x>` does not equal `<x>2</x><x>1</x>`. That is
//     XMLUnit's default pairing, and it is neither "ordered" nor "unordered",
//     which are the two rules an implementer would pick.
//
// One deliberate difference from WireMock, deviation #61: CDATA and text
// compare equal. WireMock treats `<t><![CDATA[v]]></t>` and `<t>v</t>` as
// different in both directions; the XML infoset says they are the same text,
// and an author who asked for `v` and was sent `v` has what they asked for.
// This matches strictly more than WireMock, so nothing that passes there fails
// here.
type EqualToXML struct {
	// Expected is the canonicalised expected document.
	Expected *xmlquery.Node
	// Source is the operand as written, for diagnostics.
	Source string
}

// Match implements Matcher.
func (m *EqualToXML) Match(s Subject) bool {
	if values, split := perValueScope(s); split {
		var view singleValue
		for _, v := range values {
			view.set(v)
			if m.matchOne(&view) {
				return true
			}
		}
		return false
	}
	return m.matchOne(s)
}

func (m *EqualToXML) matchOne(s Subject) bool {
	actual, ok := subjectXML(s)
	if !ok {
		return false
	}
	return xmlEqual(docElement(m.Expected), docElement(actual))
}

// Describe implements Matcher.
func (m *EqualToXML) Describe() string { return "equalToXml " + quote(m.Source) }

// MatchesXPath selects nodes from the subject and, optionally, applies an inner
// matcher to what it selected.
//
// The bare form holds when the expression selects a non-empty node-set. The
// object form is any-of over the selection: it holds when at least one selected
// node satisfies the inner matcher, which is the same rule §5.3 already applies
// to a repeated key.
type MatchesXPath struct {
	// Expr is the compiled expression.
	Expr *xpath.Expr
	// Source is the expression as written, for diagnostics.
	Source string
	// Inner is the object form's matcher, or nil for the bare form.
	Inner Matcher
	// Negate turns this into doesNotMatchXPath, which WireMock does not have —
	// kept nil-valued here so the field cannot be set by accident.
	Negate bool
}

// Match implements Matcher.
func (m *MatchesXPath) Match(s Subject) bool {
	if values, split := perValueScope(s); split {
		var view singleValue
		for _, v := range values {
			view.set(v)
			if m.matchOne(&view) {
				return true
			}
		}
		return false
	}
	return m.matchOne(s)
}

func (m *MatchesXPath) matchOne(s Subject) bool {
	doc, ok := subjectXML(s)
	if !ok {
		return false
	}
	it, ok := m.Expr.Evaluate(xmlquery.CreateXPathNavigator(doc)).(*xpath.NodeIterator)
	if !ok {
		// Unreachable: a non-node-set expression is refused at registration
		// (deviation #59). Answering false rather than panicking is the safe
		// reading of an impossible state.
		return false
	}
	for it.MoveNext() {
		if m.Inner == nil {
			return true
		}
		var view singleValue
		view.set(it.Current().Value())
		if m.Inner.Match(&view) {
			return true
		}
	}
	return false
}

// Describe implements Matcher.
func (m *MatchesXPath) Describe() string {
	if m.Inner == nil {
		return "matchesXPath " + quote(m.Source)
	}
	return "matchesXPath " + quote(m.Source) + " with " + m.Inner.Describe()
}

// subjectXML parses a subject as XML, through the memoized capability where the
// subject has one so a body examined by several XML criteria is parsed once.
func subjectXML(s Subject) (*xmlquery.Node, bool) {
	if !s.Present() {
		return nil, false
	}
	if x, ok := s.(xmlDocument); ok {
		return x.XML()
	}
	values := s.Values()
	if len(values) == 0 {
		return nil, false
	}
	return ParseXML(values[0])
}

// xmlDocument is the optional capability of a subject that can present itself
// as a parsed XML tree, memoizing the parse the way JSON already is.
type xmlDocument interface{ XML() (*xmlquery.Node, bool) }

// ParseXML reads a document the way both sides of a comparison must read it.
//
// Entities are the whole reason this is not one line. Go's decoder errors on any
// entity it does not know, so a document carrying `<!ENTITY ok "v">` — ordinary,
// valid XML — fails to parse outright and therefore matches nothing. WireMock
// expands it. That gap was found by the control step of the XXE corpus case
// rather than by reading the parser's documentation.
//
// So the internal subset is scanned and its declarations are handed to the
// decoder:
//
//   - An **internal** entity — `<!ENTITY ok "value">` — expands to its literal,
//     which is what the XML specification says and what WireMock does.
//   - An **external** entity — `<!ENTITY xxe SYSTEM "file:///etc/passwd">` — is
//     bound to the **empty string**. Nothing is opened, nothing is dialled, and
//     the document still parses, so a stub matching on some *other* part of it
//     still matches. That is also exactly what WireMock does: probed by reading
//     the resolved value back through a template, which is the only way to tell
//     "did not resolve" from "did not match".
//
// Binding rather than omitting is the security-relevant half. Omitting would
// leave the decoder to fail the parse, which looks safe and is: but it also
// throws away every document an attacker merely *touched* with an entity,
// including ones a stub was legitimately matching, and it hides the difference
// between a refusal and a resolution.
func ParseXML(text string) (*xmlquery.Node, bool) {
	doc, err := xmlquery.ParseWithOptions(strings.NewReader(text), xmlquery.ParserOptions{
		Decoder: &xmlquery.DecoderOptions{
			// Strict is the decoder's own default and is restated because
			// DecoderOptions is a plain struct: leaving it zero would quietly
			// turn strict parsing off for every document.
			Strict: true,
			Entity: internalEntities(text),
		},
	})
	if err != nil || doc == nil {
		return nil, false
	}
	return doc, true
}

// entityDecl matches one `<!ENTITY …>` declaration, capturing the name and,
// when the declaration is an internal one, its quoted literal.
var entityDecl = regexp.MustCompile(`<!ENTITY\s+(\S+)\s+(?:"([^"]*)"|'([^']*)'|(SYSTEM|PUBLIC)[^>]*)>`)

// internalEntities reads the declarations out of a document's internal subset.
//
// External declarations are bound to empty rather than dropped; see ParseXML.
// The predefined five (&lt; &gt; &amp; &apos; &quot;) are not returned, because
// the decoder handles those itself and a document is entitled to redeclare
// nothing.
func internalEntities(text string) map[string]string {
	matches := entityDecl.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		name := m[1]
		switch {
		case m[4] != "":
			// SYSTEM or PUBLIC: an external reference, bound to nothing.
			out[name] = ""
		case m[2] != "":
			out[name] = m[2]
		case m[3] != "":
			out[name] = m[3]
		default:
			// A declaration with an empty literal, which is legal and means
			// exactly what it says.
			out[name] = ""
		}
	}
	return out
}

// docElement drops down to the document's root element, so a comparison is
// between two elements rather than between two documents — which is what makes
// the XML declaration and any leading comment ignorable.
func docElement(n *xmlquery.Node) *xmlquery.Node {
	if n == nil {
		return nil
	}
	if n.Type != xmlquery.DocumentNode {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xmlquery.ElementNode {
			return c
		}
	}
	return nil
}

// xmlEqual is the comparison itself.
func xmlEqual(a, b *xmlquery.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Data != b.Data || a.NamespaceURI != b.NamespaceURI {
		return false
	}
	if !attributesEqual(a, b) {
		return false
	}
	if strings.TrimSpace(directText(a)) != strings.TrimSpace(directText(b)) {
		return false
	}
	return childrenEqual(a, b)
}

// attributesEqual compares attributes as a set keyed by namespace and name, so
// the order they were written in does not matter.
func attributesEqual(a, b *xmlquery.Node) bool {
	if len(a.Attr) != len(b.Attr) {
		return false
	}
	for _, want := range a.Attr {
		// A namespace declaration is not a value the document carries; it binds
		// a prefix, and prefixes are not compared.
		if want.Name.Space == "xmlns" || want.Name.Local == "xmlns" {
			continue
		}
		found := false
		for _, got := range b.Attr {
			if got.Name.Local == want.Name.Local && got.Name.Space == want.Name.Space &&
				got.Value == want.Value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// directText is an element's own text, CDATA included and comments excluded.
//
// Folding CDATA in with text is deviation #61: the infoset says they are the
// same, and only the parser distinguishes them.
func directText(n *xmlquery.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xmlquery.TextNode || c.Type == xmlquery.CharDataNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

// childrenEqual pairs child elements by name, and within a name group by
// document order — which is what makes `<x/><y/>` equal `<y/><x/>` while
// `<x>1</x><x>2</x>` does not equal `<x>2</x><x>1</x>`.
//
// Processing instructions are significant and are compared in order alongside
// the elements they sit among; comments are dropped before any of this.
func childrenEqual(a, b *xmlquery.Node) bool {
	groupsA, piA := childGroups(a)
	groupsB, piB := childGroups(b)
	if len(groupsA) != len(groupsB) || len(piA) != len(piB) {
		return false
	}
	for i := range piA {
		if piA[i] != piB[i] {
			return false
		}
	}
	for key, listA := range groupsA {
		listB, ok := groupsB[key]
		if !ok || len(listA) != len(listB) {
			return false
		}
		for i := range listA {
			if !xmlEqual(listA[i], listB[i]) {
				return false
			}
		}
	}
	return true
}

// childGroups buckets an element's children by (namespace, name), preserving
// document order inside each bucket, and collects processing instructions
// separately because those are compared positionally.
func childGroups(n *xmlquery.Node) (map[string][]*xmlquery.Node, []string) {
	groups := map[string][]*xmlquery.Node{}
	var pis []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case xmlquery.ElementNode:
			key := c.NamespaceURI + " " + c.Data
			groups[key] = append(groups[key], c)
		case xmlquery.ProcessingInstruction:
			// Significant, and compared in order. The XML *declaration* is not
			// one of these — it is dropped with the document node before any of
			// this — so `<?xml …?>` stays ignorable while `<?pi …?>` does not.
			//
			// Target *and* instruction: Data carries only the target, so
			// comparing it alone would make `<?pi go?>` and `<?pi stop?>` the
			// same instruction.
			pi := c.Data
			if c.ProcInst != nil {
				pi = c.ProcInst.Target + " " + c.ProcInst.Inst
			}
			pis = append(pis, pi)
		}
	}
	return groups, pis
}

// CompileXPath builds an expression, refusing the two shapes that cannot
// discriminate.
//
// A malformed expression is refused by the library itself. A well-formed one
// whose result is not a node-set is refused here, which is deviation #59:
// WireMock evaluates `count(//item) = 2` and `false()` to something it treats
// as a match unconditionally, so a criterion that reads as an assertion is a
// no-op there. The result kind is a property of the expression rather than of
// the document, so a single probe document settles it at registration.
func CompileXPath(expr string) (*xpath.Expr, error) {
	compiled, err := xpath.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid XPath expression: %w", quote(expr), err)
	}
	probe, ok := ParseXML(`<probe/>`)
	if !ok {
		return nil, errors.New("could not build the probe document")
	}
	if _, isNodeSet := compiled.Evaluate(xmlquery.CreateXPathNavigator(probe)).(*xpath.NodeIterator); !isNodeSet {
		return nil, fmt.Errorf("%s evaluates to a value rather than selecting nodes, so it can "+
			"never fail to match; write it as a node selection such as %s",
			quote(expr), quote("//item[2]"))
	}
	return compiled, nil
}
