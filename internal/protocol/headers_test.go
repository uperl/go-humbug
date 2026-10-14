package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

type hdrs [][2]string

func (h hdrs) json() string {
	if h == nil {
		return `[]`
	}
	b, _ := json.Marshal(h)
	return string(b)
}

func getReq(url string, h hdrs) string {
	return `{"method":"GET","url":"http://example.test` + url + `","headers":` + h.json() + `}`
}

func respMsg(h hdrs) string {
	return `{"status":200,"headers":` + h.json() + `}`
}

func sideDiags(r obj, side string) []obj { return diagsOf(r, side) }

func findCode(ds []obj, code string) obj {
	for _, d := range ds {
		if d["code"] == code {
			return d
		}
	}
	return nil
}

func instName(d obj) any {
	if d == nil {
		return nil
	}
	return d["instance"].(obj)["name"]
}

func TestHumbugRejectsHeadersThatCannotExistWithoutEchoingContent(t *testing.T) {
	bad := map[string]hdrs{
		"space in name":     {{"Bad Name", "x"}},
		"empty name":        {{"", "x"}},
		"colon in name":     {{"X:Y", "x"}},
		"newline in name":   {{"X\nY", "x"}},
		"CRLF in value":     {{"X-Any", "secret-token\r\nInjected: 1"}},
		"NUL in value":      {{"X-Any", "secret\x00token"}},
		"DEL in value":      {{"X-Any", "secret\x7ftoken"}},
		"bare LF in value":  {{"X-Any", "secret\ntoken"}},
		"control in value":  {{"X-Any", "secret\x01token"}},
		"non-token unicode": {{"X-Ünï", "x"}},
	}
	for name, h := range bad {
		r := run(t, init0(), validate(2, getReq("/hdr", h), "", ""))
		// An input error from the implementation, not a protocol error: the wire accepts these
		// verbatim (see wire_test.go) and a different implementation may treat them differently.
		if code := errCode(t, r[1]); code != ErrUnsupportedInput {
			t.Errorf("%s: code %s", name, code)
		}
		if msg := r[1]["error"].(obj)["message"].(string); strings.Contains(msg, "secret") || strings.Contains(msg, "Injected") {
			t.Errorf("%s: error message echoes header content: %q", name, msg)
		}
	}
	// A response header is checked the same way.
	r := run(t, init0(), validate(2, getReq("/hdr", hdrs{{"X-Any", "v"}}), respMsg(hdrs{{"X-Rate-Limit", "5\r\n"}}), ""))
	if errCode(t, r[1]) != ErrUnsupportedInput {
		t.Errorf("response header with CRLF: %v", r[1])
	}
	// Response headers are only judged when the response is being validated.
	skipped := run(t, init0(), validate(2, getReq("/hdr", hdrs{{"X-Any", "v"}}), respMsg(hdrs{{"X-Rate-Limit", "5\r\n"}}), `{"response":false}`))
	if skipped[1]["type"] != "result" {
		t.Errorf("unvalidated response headers must not be rejected: %v", skipped[1])
	}
	// Tab is legal in a value, and all token characters are legal in a name.
	ok := hdrs{{"X-Any", "a\tb"}, {"X-A!#$%&'*+.^_`|~z", "v"}}
	if res := result(t, run(t, init0(), validate(2, getReq("/hdr", ok), "", ""))[1]); res["valid"] != true {
		t.Errorf("legal characters rejected: %v", res)
	}
}

// pb33f treats an empty request header value as absent. Humbug judges the empty string itself.
func TestEmptyHeaderParameters(t *testing.T) {
	cases := []struct {
		name  string
		h     hdrs
		codes []string
		who   string
	}{
		{"required, schema allows empty", hdrs{{"X-Any", ""}}, nil, ""},
		{"minLength forbids empty", hdrs{{"X-Any", "v"}, {"X-Min", ""}}, []string{"parameter.invalid"}, "X-Min"},
		{"integer cannot be empty", hdrs{{"X-Any", "v"}, {"X-Num", ""}}, []string{"parameter.invalid"}, "X-Num"},
		{"optional string allows empty", hdrs{{"X-Any", "v"}, {"X-Opt", ""}}, nil, ""},
		{"lower-case name is the same header", hdrs{{"X-Any", "v"}, {"x-min", ""}}, []string{"parameter.invalid"}, "X-Min"},
		{"genuinely missing stays required", hdrs{}, []string{"parameter.required"}, "X-Any"},
	}
	for _, c := range cases {
		r := result(t, run(t, init0(), validate(2, getReq("/hdr", c.h), "", ""))[1])
		got := codes(sideOf(r, "request"))
		if strings.Join(got, ",") != strings.Join(c.codes, ",") {
			t.Errorf("%s: codes %v, want %v", c.name, got, c.codes)
			continue
		}
		if c.who != "" {
			if name := instName(sideDiags(r, "request")[0]); name != c.who {
				t.Errorf("%s: name %v, want %s", c.name, name, c.who)
			}
		}
	}
	// The empty-value finding says so, so a client can tell it from a wrong value.
	r := result(t, run(t, init0(), validate(2, getReq("/hdr", hdrs{{"X-Any", "v"}, {"X-Min", ""}}), "", ""))[1])
	if det, _ := sideDiags(r, "request")[0]["detail"].(obj); det["empty"] != "true" {
		t.Errorf("detail: %v", sideDiags(r, "request")[0])
	}
}

func TestRepeatedHeaderWarnings(t *testing.T) {
	a := [2]string{"X-Any", "v"}
	r := run(t, init0(),
		validate(2, getReq("/hdr", hdrs{a, {"X-Opt", "a"}, {"X-Opt", "b"}}), "", ""),
		validate(3, getReq("/hdr", hdrs{a, {"X-Tags", "a"}, {"X-Tags", "b"}}), "", ""),
		validate(4, getReq("/hdr", hdrs{a, {"Content-Type", "a"}, {"content-type", "b"}}), "", ""),
		validate(5, getReq("/hdr", hdrs{a, {"X-Other", "a"}, {"X-Other", "b"}}), "", ""),
		validate(6, getReq("/items", hdrs{{"Cookie", "x=1"}, {"Cookie", "session=abcd"}}), "", ""),
	)
	w := findCode(sideDiags(result(t, r[1]), "request"), "header.repeated")
	if w == nil || w["severity"] != "warning" || instName(w) != "X-Opt" || w["detail"].(obj)["lines"] != "2" {
		t.Fatalf("single-valued header: %v", w)
	}
	// A warning is a fact about the input, not a violation.
	if res := result(t, r[1]); res["valid"] != true || res["has_violations"] != false || res["complete"] != true {
		t.Errorf("warning must not affect validity: %v", res)
	}
	if findCode(sideDiags(result(t, r[2]), "request"), "header.repeated") != nil {
		t.Errorf("array-typed header may repeat")
	}
	if ct := findCode(sideDiags(result(t, r[3]), "request"), "header.repeated"); ct == nil || instName(ct) != "Content-Type" {
		t.Errorf("repeated Content-Type must warn: %v", sideDiags(result(t, r[3]), "request"))
	}
	if findCode(sideDiags(result(t, r[4]), "request"), "header.repeated") != nil {
		t.Errorf("undeclared headers get no opinion")
	}
	if res := result(t, r[5]); res["valid"] != true || len(sideDiags(res, "request")) != 0 {
		t.Errorf("repeated Cookie lines are exempt: %v", res)
	}
}

func TestSetCookieLinesStaySeparate(t *testing.T) {
	req := getReq("/items", hdrs{{"Cookie", "session=abcd"}})
	json200 := func(h hdrs) string {
		h = append(hdrs{{"Content-Type", "application/json"}, {"X-Rate-Limit", "5"}}, h...)
		return `{"status":200,"headers":` + h.json() + `,"body_base64":"` + b64(`[]`) + `"}`
	}
	r := run(t, init0(),
		validate(2, req, json200(hdrs{{"Set-Cookie", "sid=1"}, {"Set-Cookie", "sid=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT"}}), ""),
		validate(3, req, json200(hdrs{{"Set-Cookie", "sid=1"}, {"Set-Cookie", "other=2"}}), ""),
		validate(4, req, json200(hdrs{{"Set-Cookie", "other=2"}, {"Set-Cookie", "sid=1"}}), ""),
	)
	if res := result(t, r[1]); res["valid"] != true || len(sideDiags(res, "response")) != 0 {
		t.Errorf("two matching Set-Cookie lines (one with a comma): %v", res)
	}
	for i, label := range map[int]string{2: "non-matching second line", 3: "non-matching first line"} {
		d := findCode(sideDiags(result(t, r[i]), "response"), "response.header.invalid")
		if d == nil || instName(d) != "Set-Cookie" {
			t.Errorf("%s must be reported, with the declared name: %v", label, sideDiags(result(t, r[i]), "response"))
		}
		if findCode(sideDiags(result(t, r[i]), "response"), "header.repeated") != nil {
			t.Errorf("%s: Set-Cookie must never be reported as repeated", label)
		}
	}
}

func TestResponseHeaderDiagnostics(t *testing.T) {
	a := hdrs{{"X-Any", "v"}}
	r := run(t, init0(),
		validate(2, getReq("/hdr", a), respMsg(hdrs{{"x-rate-limit", "abc"}}), `{"request":false}`),
		validate(3, getReq("/hdr", a), respMsg(hdrs{{"X-Rate-Limit", "5"}, {"X-Rate-Limit", "abc"}}), `{"request":false}`),
		validate(4, getReq("/hdr", a), respMsg(hdrs{{"X-Rate-Limit", ""}}), `{"request":false}`),
		validate(5, getReq("/hdr", a), respMsg(hdrs{}), `{"request":false}`),
		validate(6, getReq("/hdr", a), respMsg(hdrs{{"X-Rate-Limit", "5"}, {"X-Note", ""}}), `{"request":false}`),
	)
	for i, label := range map[int]string{1: "lower-case name, bad value", 2: "second line bad", 3: "sent empty for an integer"} {
		d := findCode(sideDiags(result(t, r[i]), "response"), "response.header.invalid")
		if d == nil || instName(d) != "X-Rate-Limit" || d["severity"] != "error" {
			t.Errorf("%s: want response.header.invalid for the declared name: %v", label, sideDiags(result(t, r[i]), "response"))
		}
	}
	if d := findCode(sideDiags(result(t, r[4]), "response"), "response.header.required"); d == nil || instName(d) != "X-Rate-Limit" {
		t.Errorf("missing required response header: %v", sideDiags(result(t, r[4]), "response"))
	}
	if res := result(t, r[5]); res["valid"] != true {
		t.Errorf("empty optional string response header: %v", res)
	}
	for _, x := range r[1:] {
		if strings.Contains(strings.ToLower(jsonText(x)), "abc") {
			t.Errorf("header value leaked into a diagnostic: %s", jsonText(x))
		}
	}
}

func TestRequestHeaderDiagnosticNameIsAlwaysTheDeclaredName(t *testing.T) {
	for _, sent := range []string{"x-request-id", "X-REQUEST-ID", "X-Request-Id"} {
		req := `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["` + sent + `","ab"]],"body_base64":"` + b64(goodUser) + `"}`
		r := result(t, run(t, init0(), validate(2, req, "", ""))[1])
		d := findCode(sideDiags(r, "request"), "parameter.invalid")
		if d == nil || instName(d) != "X-Request-Id" {
			t.Errorf("sent as %q: %v", sent, sideDiags(r, "request"))
		}
	}
}
