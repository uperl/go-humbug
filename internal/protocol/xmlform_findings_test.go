package protocol

import (
	"sort"
	"strings"
	"testing"
)

// The library checks namespaces on property schemas that declare xml.namespace or xml.prefix.
func TestXMLNamespaceAndPrefixFindings(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"declared", `<r><title xmlns:ex="http://example.com/schema"><text>a</text></title></r>`, nil},
		{"missing", `<r><title><text>a</text></title></r>`, []string{"body.xml.namespace", "body.xml.prefix"}},
		{"wrong namespace", `<r><title xmlns:ex="http://other"><text>a</text></title></r>`, []string{"body.xml.namespace"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/nsprop", xmlCT, c.body)+`}}`)
			got := codes(sideOf(result(t, rs[1]), "request"))
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("want %v, got %v", c.want, got)
			}
		})
	}
}

// A form or XML body that happens to be valid JSON text must still get no span: the pointer is a
// field path, not a position in the body.
func TestDecodedBodiesThatLookLikeJSONGetNoSpan(t *testing.T) {
	rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg("/form/user", formCT, "123")+`}}`)
	d := findCode(diagsOf(result(t, rs[1]), "request"), "schema.required")
	if d == nil || d["instance"].(obj)["span"] != nil {
		t.Errorf("want schema.required without a span: %v", d)
	}
}

// text/xml is decoded like application/xml when the root is named.
func TestTextXMLIsInspected(t *testing.T) {
	rs := run(t, bodiesInit(),
		`{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/user", "text/xml", "<user><name>a</name><age>x</age></user>")+`}}`,
		`{"id":3,"type":"validate","params":{"request":`+bodyMsg("/xml/user", "text/xml", "<user><name>a</name></user>")+`}}`,
	)
	if c := codes(sideOf(result(t, rs[1]), "request")); len(c) != 1 || c[0] != "schema.type" {
		t.Errorf("%v", c)
	}
	if res := result(t, rs[2]); res["valid"] != true || res["complete"] != true {
		t.Errorf("%v", res)
	}
}
