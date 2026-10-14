package protocol

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uperl/go-humbug/internal/limits"
)

const (
	xmlCT  = "application/xml"
	formCT = "application/x-www-form-urlencoded"
	// secret appears only in payloads. No diagnostic may ever contain it: the library's own messages
	// quote the payload, so Humbug writes its own.
	secret = "SENTINEL-PAYLOAD-7f3a91"
)

func bodyMsg(path, ct, body string) string {
	return `{"method":"POST","url":"http://example.test` + path + `","headers":[["Content-Type","` + ct + `"]],"body_base64":"` + b64(body) + `"}`
}

func bodiesInit() string { return initPath(1, "../../testdata/specs/bodies.yaml") }

func firstDiag(t *testing.T, r obj, side string) obj {
	t.Helper()
	ds := diagsOf(r, side)
	if len(ds) == 0 {
		t.Fatalf("no %s diagnostics: %v", side, r)
	}
	return ds[0]
}

func TestXMLAndFormDecodeFindingsNeverEchoThePayload(t *testing.T) {
	cases := []struct {
		name, path, ct, body string
		response             bool
	}{
		{"xml request", "/xml/user", xmlCT, "<user><name>" + secret, false},
		{"xml mismatched", "/xml/user", xmlCT, "<user><name>" + secret + "</user></name>", false},
		{"xml entity", "/xml/user", xmlCT, "<user><name>&" + secret + ";</name></user>", false},
		{"form request", "/form/user", formCT, "name=%zz&x=" + secret, false},
		{"form semicolon", "/form/user", formCT, "name=" + secret + ";age=3", false},
		{"xml response", "/xml/user", xmlCT, "<user><name>" + secret, true},
		{"form response", "/form/user", formCT, "name=%zz&x=" + secret, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cmd string
			if c.response {
				cmd = `{"id":2,"type":"validate","params":{"request":{"method":"POST","url":"http://example.test` + c.path + `","headers":[]},` +
					`"response":{"status":200,"headers":[["Content-Type","` + c.ct + `"]],"body_base64":"` + b64(c.body) + `"},"validate":{"request":false}}}`
			} else {
				cmd = `{"id":2,"type":"validate","params":{"request":` + bodyMsg(c.path, c.ct, c.body) + `}}`
			}
			rs := run(t, bodiesInit(), cmd)
			if text := jsonText(rs[1]); strings.Contains(text, secret) {
				t.Fatalf("a payload value reached the reply: %s", text)
			}
			side := "request"
			if c.response {
				side = "response"
			}
			d := firstDiag(t, result(t, rs[1]), side)
			inst := d["instance"].(obj)
			if d["code"] != "body.decode" || d["severity"] != "error" || inst["location"] != "body" {
				t.Errorf("diagnostic: %v", d)
			}
			if det := d["detail"].(obj); det["media_type"] != c.ct {
				t.Errorf("detail must name the media type: %v", det)
			}
			if inst["span"] != nil || inst["pointer"] != nil {
				t.Errorf("a decode failure has no JSON position: %v", inst)
			}
			if got := sideOf(result(t, rs[1]), side); got["status"] != "invalid" || got["complete"] != true {
				t.Errorf("%v", got)
			}
		})
	}
}

func TestXMLAndFormFindingsKeepThePointerButHaveNoSpan(t *testing.T) {
	cases := []struct{ name, path, ct, body, code, pointer string }{
		{"xml type", "/xml/user", xmlCT, "<user><name>Ann</name><age>" + secret + "</age></user>", "schema.type", "/age"},
		{"xml attribute type", "/xml/attr", xmlCT, `<User id="` + secret + `"><name>Ann</name></User>`, "schema.type", "/id"},
		{"form type", "/form/user", formCT, "name=Ann&age=" + secret, "schema.type", "/age"},
		{"form minLength", "/form/user", formCT, "name=", "schema.minLength", "/name"},
		{"deepObject property", "/form/enc", formCT, "profile[bio]=x&profile[n]=" + secret, "schema.type", "/profile/n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg(c.path, c.ct, c.body)+`}}`)
			if strings.Contains(jsonText(rs[1]), secret) {
				t.Fatalf("a payload value reached the reply: %s", jsonText(rs[1]))
			}
			d := findCode(diagsOf(result(t, rs[1]), "request"), c.code)
			if d == nil {
				t.Fatalf("want %s: %v", c.code, diagsOf(result(t, rs[1]), "request"))
			}
			inst := d["instance"].(obj)
			if inst["pointer"] != c.pointer || inst["span"] != nil {
				t.Errorf("want pointer %s and no span, got %v", c.pointer, inst)
			}
		})
	}
	// A missing required property has the (empty) pointer of the document and no span either.
	rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/user", xmlCT, "<user><age>3</age></user>")+`}}`)
	d := findCode(diagsOf(result(t, rs[1]), "request"), "schema.required")
	if d == nil || d["instance"].(obj)["span"] != nil {
		t.Errorf("required: %v", d)
	}
}

// The library turns XML into JSON by wrapping the document in its root element, and unwraps it only
// when the schema declares xml.name. Without it every body fails (valid ones included), so such a
// body is not handed to the library at all and its side is incomplete.
func TestXMLWithoutARootNameIsNotInspected(t *testing.T) {
	good, bad := "<user><name>Ann</name></user>", "<user><name><oops></name></user>"
	cases := []struct {
		name, path, ct, body string
		response             bool
	}{
		{"request, valid", "/xml/unnamed", xmlCT, good, false},
		{"request, malformed", "/xml/unnamed", xmlCT, bad, false},
		{"response, valid", "/xml/unnamed", xmlCT, good, true},
		{"response, malformed", "/xml/unnamed", xmlCT, bad, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cmd string
			side := "request"
			if c.response {
				side = "response"
				cmd = `{"id":2,"type":"validate","params":{"request":{"method":"POST","url":"http://example.test` + c.path + `","headers":[]},` +
					`"response":{"status":200,"headers":[["Content-Type","` + c.ct + `"]],"body_base64":"` + b64(c.body) + `"},"validate":{"request":false}}}`
			} else {
				cmd = `{"id":2,"type":"validate","params":{"request":` + bodyMsg(c.path, c.ct, c.body) + `}}`
			}
			res := result(t, run(t, bodiesInit(), cmd)[1])
			s := sideOf(res, side)
			ds := diagsOf(res, side)
			if s["status"] != "incomplete" || s["complete"] != false || res["valid"] == true {
				t.Fatalf("must be incomplete, never valid: %v", s)
			}
			if len(ds) != 1 || ds[0]["code"] != "validation.incomplete" || ds[0]["reason"] != "body.xml_root_unnamed" ||
				ds[0]["severity"] != "info" || ds[0]["detail"].(obj)["media_type"] != xmlCT {
				t.Errorf("one explanatory note and nothing else (no false violation): %v", ds)
			}
		})
	}
	// text/xml is the same.
	res := result(t, run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/unnamed", "text/xml", good)+`}}`)[1])
	if reasonOf(sideOf(res, "request")) != "body.xml_root_unnamed" {
		t.Errorf("text/xml: %v", sideOf(res, "request"))
	}
	// And the library's "required body missing" finding for the withheld body is not reported.
	for _, d := range diagsOf(res, "request") {
		if d["code"] == "body.required" {
			t.Errorf("an uninspected body is not a missing body")
		}
	}
}

func TestXMLWithARootNameIsInspected(t *testing.T) {
	req := func(body string) string {
		return `{"method":"POST","url":"http://example.test/xml","headers":[["Content-Type","application/xml"]],"body_base64":"` + b64(body) + `"}`
	}
	rs := run(t, init0(),
		validate(2, req("<user><name>Ann</name></user>"), "", ""),
		validate(3, req("<user><name>Ann</name><age>x</age></user>"), "", ""),
	)
	if res := result(t, rs[1]); res["valid"] != true || res["complete"] != true {
		t.Errorf("valid XML with a named root: %v", res)
	}
	if c := codes(sideOf(result(t, rs[2]), "request")); len(c) != 1 || c[0] != "schema.type" {
		t.Errorf("%v", c)
	}
}

func TestSizeLimitAppliesToXMLAndFormBodies(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 40 })
	long := strings.Repeat("a", 60)
	rs := runWith(t, lim, bodiesInit(),
		`{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/user", xmlCT, "<user><name>"+long+"</name></user>")+`}}`,
		`{"id":3,"type":"validate","params":{"request":`+bodyMsg("/form/user", formCT, "name="+long)+`}}`,
		`{"id":4,"type":"validate","params":{"request":`+bodyMsg("/form/user", formCT, "name=Ann")+`}}`,
	)
	for i := 1; i <= 2; i++ {
		s := sideOf(result(t, rs[i]), "request")
		if s["status"] != "incomplete" || reasonOf(s) != "body.too_large" {
			t.Errorf("reply %d: %v", i, s)
		}
	}
	if res := result(t, rs[3]); res["valid"] != true {
		t.Errorf("a small form body is inspected: %v", res)
	}
}

// Entity declarations must be neither expanded nor resolved: a file named by an external entity must
// not be read, an external DTD must not be fetched, and nothing may be echoed back.
func TestXMLExternalEntitiesAreNeitherReadNorFetched(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(file, []byte(secret+"-FROM-A-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`<!ENTITY x "` + secret + `-FROM-THE-NETWORK">`))
	}))
	defer srv.Close()

	bodies := map[string]string{
		"file entity":      `<!DOCTYPE d [<!ENTITY x SYSTEM "file://` + file + `">]><user><name>&x;</name></user>`,
		"network entity":   `<!DOCTYPE d [<!ENTITY x SYSTEM "` + srv.URL + `/e">]><user><name>&x;</name></user>`,
		"external DTD":     `<!DOCTYPE d SYSTEM "` + srv.URL + `/d.dtd"><user><name>Ann</name></user>`,
		"parameter entity": `<!DOCTYPE d [<!ENTITY % p SYSTEM "` + srv.URL + `/p">%p;]><user><name>Ann</name></user>`,
		"public DTD":       `<!DOCTYPE d PUBLIC "-//X//Y" "` + srv.URL + `/pub.dtd"><user><name>Ann</name></user>`,
		"expansion":        `<!DOCTYPE d [<!ENTITY a "` + strings.Repeat("a", 1000) + `"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;"><!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]><user><name>&c;</name></user>`,
	}
	for name, body := range bodies {
		rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg("/xml/user", xmlCT, body)+`}}`)
		text := jsonText(rs[1])
		if strings.Contains(text, secret) || strings.Contains(text, "aaaaaaaaaa") {
			t.Errorf("%s: an entity was expanded or echoed: %s", name, text)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the validator made %d network request(s); it must make none", n)
	}
}

func TestNestedXMLAndHugeFormsGetAReplyQuickly(t *testing.T) {
	cases := map[string]string{
		"xml nested 100,000 deep":       "<a>" + strings.Repeat("<a>", 100_000) + strings.Repeat("</a>", 100_000) + "</a>",
		"xml unterminated, deep":        strings.Repeat("<a>", 300_000),
		"form with 100,000 fields":      "name=Ann&" + strings.Repeat("k=v&", 100_000),
		"form with 200,000 array items": "name=a&" + strings.Repeat("tags=x&", 200_000),
	}
	for name, body := range cases {
		path, ct := "/xml/user", xmlCT
		if strings.HasPrefix(name, "form") {
			path, ct = "/form/user", formCT
		}
		start := time.Now()
		rs := run(t, bodiesInit(), `{"id":2,"type":"validate","params":{"request":`+bodyMsg(path, ct, body)+`}}`)
		if rs[1]["type"] != "result" {
			t.Errorf("%s: %v", name, rs[1])
		}
		if d := time.Since(start); d > 15*time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}
}
