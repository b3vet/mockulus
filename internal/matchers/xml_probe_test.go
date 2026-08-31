// SPDX-License-Identifier: Apache-2.0

package matchers

import "testing"

// Every row here is a WireMock behaviour recorded in PROBE_XML.md, replayed
// against our own comparison. The two marked DEVIATION are where we chose the
// logically correct answer instead.
func TestXMLEqualityMatchesTheProbe(t *testing.T) {
	cases := []struct {
		name, expected, actual string
		want                   bool
	}{
		{"attributes reordered", `<a b="1" c="2"/>`, `<a c="2" b="1"/>`, true},
		{"self-closing vs empty", `<a></a>`, `<a/>`, true},
		{"whitespace-only text", `<a></a>`, `<a>   </a>`, true},
		{"indentation between elements", `<r><x/><y/></r>`, "<r>\n  <x/>\n  <y/>\n</r>", true},
		{"text is trimmed", `<t>hello</t>`, `<t>  hello  </t>`, true},
		{"different text", `<t>hello</t>`, `<t>world</t>`, false},
		{"significant text between elements", `<r><a/><b/></r>`, `<r><a/>junk<b/></r>`, false},

		{"differently-named siblings swapped", `<r><x>1</x><y>2</y></r>`, `<r><y>2</y><x>1</x></r>`, true},
		{"same-named siblings swapped", `<r><x>1</x><x>2</x></r>`, `<r><x>2</x><x>1</x></r>`, false},
		{"same-named, one value changed", `<r><x>1</x><x>2</x></r>`, `<r><x>1</x><x>3</x></r>`, false},

		{"extra child", `<r><x/><y/></r>`, `<r><x/><y/><z/></r>`, false},
		{"missing child", `<r><x/><y/></r>`, `<r><x/></r>`, false},
		{"renamed element", `<r><x/><y/></r>`, `<r><x/><Y/></r>`, false},
		{"changed attribute value", `<a b="1"/>`, `<a b="9"/>`, false},
		{"missing attribute", `<a b="1" c="2"/>`, `<a b="1"/>`, false},

		{"namespace: same URI different prefix",
			`<ns1:a xmlns:ns1="urn:x"><ns1:b/></ns1:a>`, `<z:a xmlns:z="urn:x"><z:b/></z:a>`, true},
		{"namespace: same prefix different URI",
			`<ns1:a xmlns:ns1="urn:x"/>`, `<ns1:a xmlns:ns1="urn:other"/>`, false},
		{"namespace: default vs prefixed, same URI",
			`<ns1:a xmlns:ns1="urn:x"><ns1:b/></ns1:a>`, `<a xmlns="urn:x"><b/></a>`, true},
		{"namespace: none vs namespaced", `<ns1:a xmlns:ns1="urn:x"/>`, `<a/>`, false},

		{"comment ignored", `<r><t>v</t></r>`, `<r><!-- hi --><t>v</t></r>`, true},
		{"xml declaration ignored", `<r><t>v</t></r>`, `<?xml version="1.0"?><r><t>v</t></r>`, true},

		// DEVIATION #61: WireMock answers false for both directions.
		{"DEVIATION CDATA equals text", `<r><t>v</t></r>`, `<r><t><![CDATA[v]]></t></r>`, true},
		{"DEVIATION text equals CDATA", `<r><t><![CDATA[v]]></t></r>`, `<r><t>v</t></r>`, true},
		{"CDATA with a different value", `<r><t><![CDATA[v]]></t></r>`, `<r><t><![CDATA[zzz]]></t></r>`, false},

		// A processing instruction inside the document is significant, unlike the
		// declaration and unlike comments. Guessed wrong the first time — the node
		// type is ProcessingInstruction, not NotationNode — so it is pinned here.
		{"PI inside the document is significant", `<r><t>v</t></r>`, `<r><?pi go?><t>v</t></r>`, false},
		{"the same PI on both sides", `<r><?pi go?><t>v</t></r>`, `<r><?pi go?><t>v</t></r>`, true},
		{"a different PI", `<r><?pi go?><t>v</t></r>`, `<r><?pi stop?><t>v</t></r>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp, ok := ParseXML(c.expected)
			if !ok {
				t.Fatalf("expected document did not parse: %s", c.expected)
			}
			m := &EqualToXML{Expected: exp, Source: c.expected}
			if got := m.Match(NewBody([]byte(c.actual))); got != c.want {
				t.Errorf("equalToXml(%s) against %s = %v, want %v", c.expected, c.actual, got, c.want)
			}
		})
	}
}

// The two shapes that cannot discriminate are refused at registration.
func TestCompileXPathRefusesWhatCannotDiscriminate(t *testing.T) {
	for _, expr := range []string{"count(//item) = 2", "false()", "true()", "string(//total)"} {
		if _, err := CompileXPath(expr); err == nil {
			t.Errorf("%q evaluates to a value and must be refused (deviation #59)", expr)
		}
	}
	if _, err := CompileXPath("//["); err == nil {
		t.Error("a malformed expression must be refused (deviation #60)")
	}
	for _, expr := range []string{"//item", "//total/text()", "//item/@sku", "/order/item"} {
		if _, err := CompileXPath(expr); err != nil {
			t.Errorf("%q selects nodes and must compile, got %v", expr, err)
		}
	}
}
