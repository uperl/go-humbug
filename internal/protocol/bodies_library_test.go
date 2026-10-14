package protocol

import (
	"sort"
	"testing"
)

// KNOWN LIBRARY LIMITATIONS: XML and URL-encoded bodies in the pinned pb33f version.
//
// Every case records what OpenAPI says (want: "V" valid, "I" invalid, "?" ambiguous) and what the
// pinned library does (got: "V" valid, "I" invalid, "N" not inspected). The test asserts only `got`,
// so it fails when a library upgrade changes behavior in either direction. The groups:
//
//	works            the library is right
//	security         entity and DTD attacks: rejected or ignored, never expanded or fetched
//	lenient          the library accepts something OpenAPI would reject
//	false-violation  the library rejects something valid
//	observed         ambiguous in OpenAPI; pinned so a change is noticed
//	not-inspected    not validated at all (reported incomplete)
//
// If a case fails, decide whether the new behavior is an improvement. If so, update `got` here and
// the matching text in docs/protocol.md ("Known library limitations").
type bodyCase struct {
	group string
	path  string
	ct    string
	body  string
	want  string
	got   string
	note  string
	resp  bool // the body is the response body
}

var bodyCases = []bodyCase{
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><age>30</age><active>true</active><score>1.5</score></user>", "V", "V", "", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><age>x</age></user>", "I", "I", "type is enforced, with the element as the pointer", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><age>-1</age></user>", "I", "I", "minimum", false},
	{"works", "/xml/user", "application/xml", "<user><age>3</age></user>", "I", "I", "required element missing", false},
	{"works", "/xml/user", "application/xml", "<user><name/></user>", "I", "I", "empty required element (minLength 1)", false},
	{"works", "/xml/user", "application/xml", "<user><name></name></user>", "I", "I", "", false},
	{"works", "/xml/user", "application/xml", "<other><name>Ann</name></other>", "I", "I", "the root element must be the declared xml.name", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><other>1</other></user>", "V", "V", "extra elements are allowed", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><score>abc</score></user>", "I", "I", "", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><tags>a</tags><tags>b</tags></user>", "V", "V", "repeated elements form an array", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><tags>a</tags></user>", "V", "V", "one element is an array of one", false},
	{"works", "/xml/user", "application/xml", "<user><name><![CDATA[Ann]]></name></user>", "V", "V", "CDATA", false},
	{"works", "/xml/user", "application/xml", "<?xml version=\"1.0\" encoding=\"UTF-8\"?><user><name>Ann</name></user>", "V", "V", "XML declaration", false},
	{"works", "/xml/user", "application/xml", "<?pi x?><!-- c --><user><name>Ann</name></user>", "V", "V", "comments and processing instructions", false},
	{"works", "/xml/user", "application/xml", "<user id=\"5\"><name>Ann</name></user>", "V", "V", "undeclared attributes are ignored", false},
	{"works", "/xml/user", "application/xml", "<user><name>A&amp;B</name></user>", "V", "V", "predefined entity", false},
	{"works", "/xml/user", "application/xml", "<user><name>&#65;nn</name></user>", "V", "V", "character reference", false},
	{"works", "/xml/user", "application/xml", "<user><name>é€</name></user>", "V", "V", "multi-byte characters", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><age> 30 </age></user>", "V", "V", "surrounding whitespace is trimmed", false},
	{"lenient", "/xml/user", "application/xml", "<user><name>Ann</name><active>1</active></user>", "I", "V", "booleans are true and false only", false},
	{"lenient", "/xml/user", "application/xml", "<user><name>Ann</name><active>TRUE</active></user>", "I", "V", "", false},
	{"observed", "/xml/user", "application/xml", "<user><name>Ann</name><age>30.0</age></user>", "?", "I", "JSON Schema: 30.0 is an integer", false},
	{"observed", "/xml/user", "application/xml", "<user><name>Ann</name><age>007</age></user>", "?", "V", "leading zeros", false},
	{"works", "/xml/user", "application/xml", "<user><name>a</name></user><user><name>b</name></user>", "I", "I", "a second root element is not valid XML", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name></user><!-- trailing -->", "V", "V", "trailing comment", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name>", "I", "I", "malformed: unclosed tag", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</user></name>", "I", "I", "malformed: mismatched tags", false},
	{"works", "/xml/user", "application/xml", "just text", "I", "I", "malformed: not XML", false},
	{"works", "/xml/user", "application/xml", "<user><name>&foo;</name></user>", "I", "I", "malformed: undefined entity", false},
	{"works", "/xml/user", "application/xml", "<user><name>A&B</name></user>", "I", "I", "malformed: bare ampersand", false},
	{"works", "/xml/user", "application/xml", "<user><name>A\u0001B</name></user>", "I", "I", "malformed: control character", false},
	{"works", "/xml/user", "application/xml", "<", "I", "I", "malformed: a lone <", false},
	{"works", "/xml/user", "application/xml", "", "I", "I", "malformed: empty body", false},
	{"works", "/xml/user", "application/xml", "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><user><name>Ann</name></user>", "V", "V", "a declared encoding is accepted for ASCII content", false},
	{"security", "/xml/user", "application/xml", "<!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/hostname\">]><user><name>&x;</name></user>", "I", "I", "external entity (XXE): rejected, nothing read", false},
	{"security", "/xml/user", "application/xml", "<!DOCTYPE d [<!ENTITY x \"Ann\">]><user><name>&x;</name></user>", "I", "I", "internal entity: rejected", false},
	{"security", "/xml/user", "application/xml", "<!DOCTYPE d SYSTEM \"http://127.0.0.1:9/evil.dtd\"><user><name>Ann</name></user>", "V", "V", "an external DTD is ignored, never fetched", false},
	{"security", "/xml/user", "application/xml", "<!DOCTYPE d [<!ENTITY a \"aaaaaaaaaa\"><!ENTITY b \"&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;\"><!ENTITY c \"&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;\">]><user><name>&c;</name></user>", "I", "I", "entity expansion: rejected", false},
	{"works", "/xml/attr", "application/xml", "<User id=\"5\"><name>Ann</name></User>", "V", "V", "xml.attribute", false},
	{"works", "/xml/attr", "application/xml", "<User id=\"x\"><name>Ann</name></User>", "I", "I", "attribute type", false},
	{"works", "/xml/attr", "application/xml", "<User><name>Ann</name></User>", "I", "I", "required attribute missing", false},
	{"lenient", "/xml/attr", "application/xml", "<User><id>5</id></User>", "I", "V", "an element where the schema says attribute", false},
	{"works", "/xml/wrapped", "application/xml", "<r><items><item>a</item><item>b</item></items></r>", "V", "V", "xml.wrapped", false},
	{"works", "/xml/wrapped", "application/xml", "<r><items><thing>a</thing></items></r>", "I", "I", "item elements must be named by items.xml.name", false},
	{"works", "/xml/ns", "application/xml", "<ex:r xmlns:ex=\"http://example.com/schema\"><ex:name>Ann</ex:name></ex:r>", "V", "V", "namespace and prefix as declared", false},
	{"lenient", "/xml/ns", "application/xml", "<ex:r xmlns:ex=\"http://other\"><ex:name>Ann</ex:name></ex:r>", "I", "V", "wrong namespace", false},
	{"lenient", "/xml/ns", "application/xml", "<r><name>Ann</name></r>", "I", "V", "no namespace", false},
	{"works", "/xml/user", "text/xml", "<user><name>Ann</name><age>x</age></user>", "I", "I", "text/xml", false},
	{"works", "/xml/user", "application/xml; charset=utf-8", "<user><name>Ann</name><age>x</age></user>", "I", "I", "media type parameters", false},
	{"works", "/xml/user", "Application/XML", "<user><name>Ann</name><age>x</age></user>", "I", "I", "media type case", false},
	{"not-inspected", "/xml/soap", "application/soap+xml", "<user><name>Ann</name><age>x</age></user>", "I", "N", "a +xml type: no decoder, so not validated (reported incomplete)", false},
	{"works", "/xml/jsononly", "application/xml", "<a/>", "I", "I", "XML where only JSON is declared", false},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name></user>", "V", "V", "response valid", true},
	{"works", "/xml/user", "application/xml", "<user><name>Ann</name><age>x</age></user>", "I", "I", "response: wrong type", true},
	{"works", "/xml/user", "application/xml", "<user><age>3</age></user>", "I", "I", "response: required missing", true},
	{"works", "/xml/user", "application/xml", "<user>", "I", "I", "response: malformed", true},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&age=30&active=true&score=1.5", "V", "V", "", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&age=x", "I", "I", "type, pointer /age", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&age=-1", "I", "I", "minimum", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "age=3", "I", "I", "required field missing", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=", "I", "I", "empty value (minLength 1)", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name", "I", "I", "no \"=\": an empty value", false},
	{"lenient", "/form/user", "application/x-www-form-urlencoded", "", "I", "V", "an empty body (the field is required)", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "=", "I", "I", "", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "&&", "I", "I", "", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&other=1", "V", "V", "unknown fields are allowed", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&tags=a&tags=b", "V", "V", "repeated keys form an array", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&tags=a", "V", "V", "one value is an array of one", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&tags[]=a&tags[]=b", "V", "V", "PHP-style names are just undeclared fields", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=A+B", "V", "V", "\"+\" is a space", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=%41nn", "V", "V", "percent-encoded", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=%C3%A9%E2%82%AC", "V", "V", "multi-byte characters", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&score=1e3", "V", "V", "", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&score=abc", "I", "I", "", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&active=yes", "I", "I", "", false},
	{"lenient", "/form/user", "application/x-www-form-urlencoded", "name=Ann&active=1", "I", "V", "booleans are true and false only", false},
	{"lenient", "/form/user", "application/x-www-form-urlencoded", "name=a&name=b", "I", "V", "a scalar field repeated: not a string", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=%zz", "I", "I", "bad percent escape", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&age=%4", "I", "I", "truncated percent escape", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann;age=3", "I", "I", "a semicolon is not a separator", false},
	{"observed", "/form/user", "application/x-www-form-urlencoded", "name=Ann&tags=a,b", "?", "V", "an unencoded comma: OpenAPI says reserved characters should be encoded, but the library finding is dropped (see below)", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&tags=a%2Cb", "V", "V", "an encoded comma is fine", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=a%40b.com", "V", "V", "an encoded @ (an email address) is fine", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=a%2Fb%3Dc", "V", "V", "encoded / and =", false},
	{"lenient", "/form/user", "application/x-www-form-urlencoded", "name=%FF", "I", "V", "a value that is not valid UTF-8", false},
	{"works", "/form/user", "application/x-www-form-urlencoded; charset=UTF-8", "name=Ann&age=x", "I", "I", "media type parameters", false},
	{"works", "/form/enc", "application/x-www-form-urlencoded", "tags=a,b", "V", "V", "encoding: tags is style form without explode", false},
	{"works", "/form/enc", "application/x-www-form-urlencoded", "profile[bio]=x&profile[n]=3", "V", "V", "encoding: deepObject", false},
	{"works", "/form/enc", "application/x-www-form-urlencoded", "profile[bio]=x&profile[n]=x", "I", "I", "deepObject property type", false},
	{"works", "/form/enc", "application/x-www-form-urlencoded", "profile%5Bbio%5D=x&profile%5Bn%5D=x", "I", "I", "encoded brackets", false},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann", "V", "V", "response valid", true},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=Ann&age=x", "I", "I", "response: wrong type", true},
	{"works", "/form/user", "application/x-www-form-urlencoded", "age=3", "I", "I", "response: required missing", true},
	{"works", "/form/user", "application/x-www-form-urlencoded", "name=%zz", "I", "I", "response: malformed", true},
}

func TestXMLFormLibraryBehavior(t *testing.T) {
	var lines []string
	lines = append(lines, initPath(1, "../../testdata/specs/bodies.yaml"))
	for i, c := range bodyCases {
		msg := `{"method":"POST","url":"http://example.test` + c.path + `","headers":[["Content-Type","` + c.ct + `"]],"body_base64":"` + b64(c.body) + `"}`
		cmd := `"request":` + msg
		if c.resp {
			cmd = `"request":{"method":"POST","url":"http://example.test` + c.path + `","headers":[]},` +
				`"response":{"status":200,"headers":[["Content-Type","` + c.ct + `"]],"body_base64":"` + b64(c.body) + `"},` +
				`"validate":{"request":false,"response":true}`
		}
		lines = append(lines, `{"id":`+itoa(i+2)+`,"type":"validate","params":{`+cmd+`}}`)
	}
	replies := run(t, lines...)

	tally := map[string]int{}
	differing := map[string]int{}
	for i, c := range bodyCases {
		tally[c.group]++
		if c.want != "?" && c.want != c.got && c.group != "not-inspected" {
			differing[c.group]++
		}
		r := replies[i+1]
		if r["type"] == "error" {
			t.Errorf("%s: %v", c.path, r)
			continue
		}
		res := result(t, r)
		side := res["request"].(obj)
		if c.resp {
			side = res["response"].(obj)
		}
		got := "I"
		switch {
		case side["status"] == "incomplete":
			got = "N"
		case res["valid"] == true && res["complete"] == true:
			got = "V"
		}
		if got != c.got {
			t.Errorf("%s %q [%s]: the library now gives %s; it was pinned as %s (OpenAPI says %s). %s\n"+
				"   If this is an improvement, update the case and docs/protocol.md (\"Known library limitations\").",
				c.path, abbreviateBody(c.body), c.group, got, c.got, c.want, c.note)
		}
	}
	for _, g := range []string{"lenient", "false-violation"} {
		if tally[g] > 0 && differing[g] == 0 {
			t.Errorf("table error: group %q has no case that differs from OpenAPI", g)
		}
	}
	var groups []string
	for g := range tally {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		t.Logf("%-16s %3d cases", g, tally[g])
	}
}

func abbreviateBody(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
