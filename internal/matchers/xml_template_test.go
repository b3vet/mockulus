// SPDX-License-Identifier: Apache-2.0

package matchers

import "testing"

// Every expectation in this file is a byte string recorded from pinned WireMock
// 3.13.2 and written down in internalDocs/PROBE_XML.md P10, not a value derived
// from reading this package's own code. That is the point of them: the printer
// exists precisely because `xmlquery`'s indenting output differs from the
// oracle's in five separate ways, and a test written from the implementation
// would agree with whichever of those the implementation happened to inherit.

func TestFormatXMLMatchesTheOracle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<r><a>1</a><b><c>2</c></b></r>", "<r>\n  <a>1</a>\n  <b>\n    <c>2</c>\n  </b>\n</r>\n"},
		// The declaration is dropped rather than rewritten.
		{"<?xml version='1.0'?><r><a>1</a></r>", "<r>\n  <a>1</a>\n</r>\n"},
		// Re-formatting is idempotent: existing layout is replaced, not added to.
		{"<r>\n  <a>1</a>\n</r>", "<r>\n  <a>1</a>\n</r>\n"},
		// Mixed content puts each text run on its own line, trimmed.
		{"<r>text<a>1</a>tail</r>", "<r>\n  text\n  <a>1</a>\n  tail\n</r>\n"},
		// Empty, explicitly-empty and whitespace-only all self-close.
		{"<x/>", "<x/>\n"},
		{"<x></x>", "<x/>\n"},
		{"<r>   </r>", "<r/>\n"},
		{"<r><x/></r>", "<r>\n  <x/>\n</r>\n"},
		// A comment or a PI forces the parent to expand; a text-only child does not.
		{"<r><!--c--><t><![CDATA[v]]></t></r>", "<r>\n  <!--c-->\n  <t><![CDATA[v]]></t>\n</r>\n"},
		{"<r><!--only--></r>", "<r>\n  <!--only-->\n</r>\n"},
		{"<r><?pi data?><a>1</a></r>", "<r>\n  <?pi data?>\n  <a>1</a>\n</r>\n"},
		// Prefixes and namespace declarations survive; attributes are double-quoted.
		{"<s:a xmlns:s='urn:x'><s:b>1</s:b></s:a>", "<s:a xmlns:s=\"urn:x\">\n  <s:b>1</s:b>\n</s:a>\n"},
		{"<a k='v'>1</a>", "<a k=\"v\">1</a>\n"},
		{"<a b='1' c='2'/>", "<a b=\"1\" c=\"2\"/>\n"},
		// Escaping, in both positions. A raw `>` in text is escaped; an apostrophe
		// in an attribute is not; a newline in an attribute goes numeric.
		{"<a>&lt;&amp;</a>", "<a>&lt;&amp;</a>\n"},
		{"<a>1 &gt; 0</a>", "<a>1 &gt; 0</a>\n"},
		{"<a>1 > 0</a>", "<a>1 &gt; 0</a>\n"},
		{"<a t='&lt;&amp;&quot;'>x</a>", "<a t=\"&lt;&amp;&quot;\">x</a>\n"},
		{"<a t=\"it's\"/>", "<a t=\"it's\"/>\n"},
		{"<a t='x&#10;y'/>", "<a t=\"x&#10;y\"/>\n"},
		{"<a>x\ty</a>", "<a>x\ty</a>\n"},
		// CDATA stays CDATA, including beside text and including markup inside it.
		{"<a><![CDATA[<b>&x]]></a>", "<a><![CDATA[<b>&x]]></a>\n"},
		{"<a>t<![CDATA[c]]></a>", "<a>t<![CDATA[c]]></a>\n"},
		{"<r><a/><b>1</b><c><d/></c></r>", "<r>\n  <a/>\n  <b>1</b>\n  <c>\n    <d/>\n  </c>\n</r>\n"},
	}
	for _, c := range cases {
		doc, ok := ParseXML(c.in)
		if !ok {
			t.Errorf("ParseXML(%q) failed", c.in)
			continue
		}
		if got := FormatXML(doc); got != c.want {
			t.Errorf("formatXml(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// formatXml is idempotent, which the oracle rows imply but none of them states.
// A helper whose output cannot be fed back into it would corrupt a document that
// passes through two stubs.
func TestFormatXMLIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"<r><a>1</a><b><c>2</c></b></r>", "<r>text<a>1</a>tail</r>",
		"<r><!--c--><t><![CDATA[v]]></t></r>", "<s:a xmlns:s='urn:x'><s:b>1</s:b></s:a>",
	} {
		doc, _ := ParseXML(in)
		once := FormatXML(doc)
		again, ok := ParseXML(once)
		if !ok {
			t.Fatalf("formatXml output does not re-parse: %q", once)
		}
		if twice := FormatXML(again); twice != once {
			t.Errorf("formatXml is not idempotent for %q:\n once  %q\n twice %q", in, once, twice)
		}
	}
}

const xpathDoc = `<r><total>10</total><item sku="A">a</item><item sku="B">b</item></r>`

func TestXPathHelperMatchesTheOracle(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"//total/text()", "10"},
		{"//total", "<total>10</total>\n"},
		// A node-set contributes its first node only, never a list.
		{"//item/text()", "a"},
		{"//item", "<item sku=\"A\">a</item>\n"},
		// An attribute renders as its value, not as the element carrying it.
		{"//item/@sku", "A"},
		{"//nope/text()", ""},
		// A value expression is legal here, unlike in matchesXPath (#59).
		{"count(//item)", "2"},
		{"string(//total)", "10"},
		{"/r", "<r>\n  <total>10</total>\n  <item sku=\"A\">a</item>\n  <item sku=\"B\">b</item>\n</r>\n"},
	}
	for _, c := range cases {
		got, err := XPathHelper([]any{xpathDoc, c.expr}, nil)
		if err != nil {
			t.Errorf("xPath %q: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("xPath %q\n got %q\nwant %q", c.expr, got, c.want)
		}
	}
}

// Deviation #62: a numeric result carries no locale grouping. WireMock renders
// the same expression as `2,000,000` or `2.000.000` depending on the JVM locale
// its container booted in, which is not a wire contract to reproduce.
func TestXPathNumbersCarryNoLocaleGrouping(t *testing.T) {
	for expr, want := range map[string]string{
		"count(//item) * 1000000": "2000000",
		"count(//item) div 4":     "0.5",
		"count(//item)":           "2",
		"0 - count(//item)":       "-2",
		"count(//item) > 1":       "true",
	} {
		got, err := XPathHelper([]any{xpathDoc, expr}, nil)
		if err != nil {
			t.Errorf("xPath %q: %v", expr, err)
			continue
		}
		if got != want {
			t.Errorf("xPath %q got %q want %q", expr, got, want)
		}
	}
}

const soapDoc = `<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">` +
	`<S:Body><req><id>7</id><n><id>9</id></n></req></S:Body></S:Envelope>`

func TestSOAPXPathHelperMatchesTheOracle(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"/req/id/text()", "7"},
		{"req/id/text()", "7"},
		{"/*/id/text()", "7"},
		{"/req/id", "<id>7</id>\n"},
		{"/req/zz/text()", ""},
		// The discriminating rows: the expression is concatenated as a step below
		// Body, so a leading slash becomes a descendant step and `id` is found
		// two levels down. Under the "relative to the Body's content element"
		// reading these two would be empty and `./id/text()` would be `7`.
		{"/id/text()", "7"},
		{"/n/id/text()", "9"},
		{"./id/text()", ""},
		{"/req//id/text()", "7"},
		{"/*/*/text()", "7"},
	}
	for _, c := range cases {
		got, err := SOAPXPathHelper([]any{soapDoc, c.expr}, nil)
		if err != nil {
			t.Errorf("soapXPath %q: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("soapXPath %q got %q want %q", c.expr, got, c.want)
		}
	}
}

func TestSOAPXPathRefusesWhatCannotBeAStep(t *testing.T) {
	for _, expr := range []string{"//id", "//id/text()", "count(/req/id)", "string(/req/id)"} {
		if _, err := SOAPXPathHelper([]any{soapDoc, expr}, nil); err == nil {
			t.Errorf("soapXPath accepted %q, which the oracle refuses", expr)
		}
	}
	// A node test is not a function call, and must survive the refusal above.
	if _, err := SOAPXPathHelper([]any{soapDoc, "req/id/text()"}, nil); err != nil {
		t.Errorf("soapXPath refused a legitimate node test: %v", err)
	}
}

// An envelope is required. A bare Body, or a payload with no SOAP wrapper at
// all, yields nothing rather than falling back to the document root — matching
// the oracle, and the falsifier for a Body-locating path written too loosely.
func TestSOAPXPathNeedsAnEnvelope(t *testing.T) {
	for _, doc := range []string{
		"<Body><req><id>7</id></req></Body>",
		"<req><id>7</id></req>",
	} {
		got, err := SOAPXPathHelper([]any{doc, "/req/id/text()"}, nil)
		if err != nil {
			t.Errorf("soapXPath(%q): %v", doc, err)
			continue
		}
		if got != "" {
			t.Errorf("soapXPath found %q in a document with no SOAP envelope: %q", got, doc)
		}
	}
}

func TestXMLHelpersRefuseBadInput(t *testing.T) {
	for _, c := range []struct {
		name string
		args []any
	}{
		{"xPath: no expression", []any{xpathDoc}},
		{"xPath: empty expression", []any{xpathDoc, "  "}},
		{"xPath: non-string expression", []any{xpathDoc, 7}},
		{"xPath: unparseable expression", []any{xpathDoc, "//["}},
		{"xPath: body is not XML", []any{"not xml at all", "//a"}},
		{"soapXPath: body is not XML", []any{"not xml at all", "/a"}},
	} {
		var err error
		if c.name[0] == 's' {
			_, err = SOAPXPathHelper(c.args, nil)
		} else {
			_, err = XPathHelper(c.args, nil)
		}
		if err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
	if _, err := FormatXMLHelper([]any{"not xml"}, nil); err == nil {
		t.Error("formatXml accepted a non-XML document")
	}
	if _, err := FormatXMLHelper(nil, nil); err == nil {
		t.Error("formatXml accepted no arguments")
	}
}

// The security property that decided where these helpers live: the parser they
// share with the matchers declares external entities away, so a template cannot
// become a second route to XXE. An internal entity still expands, which is the
// control that proves the document is really being parsed.
func TestXMLHelpersDoNotResolveExternalEntities(t *testing.T) {
	external := `<!DOCTYPE r [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><r><a>&xxe;</a></r>`
	got, err := XPathHelper([]any{external, "//a/text()"}, nil)
	if err != nil {
		t.Fatalf("the document should still parse: %v", err)
	}
	if got != "" {
		t.Errorf("an external entity resolved to %q; it must yield nothing", got)
	}
	internal := `<!DOCTYPE r [<!ENTITY ok "expanded">]><r><a>&ok;</a></r>`
	got, err = XPathHelper([]any{internal, "//a/text()"}, nil)
	if err != nil {
		t.Fatalf("internal entity document: %v", err)
	}
	if got != "expanded" {
		t.Errorf("an internal entity rendered %q, want %q — without this control the "+
			"test above would pass on a parser that resolves nothing at all", got, "expanded")
	}
}
