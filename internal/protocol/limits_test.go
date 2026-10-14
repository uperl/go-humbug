package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/uperl/go-humbug/internal/limits"
)

// runWith is run() with an explicit resource policy.
func runWith(t *testing.T, lim limits.Limits, lines ...string) []obj {
	t.Helper()
	var out bytes.Buffer
	s := &Server{Load: NewEngineLoader(lim), Limits: lim}
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var replies []obj
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var o obj
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			t.Fatalf("stdout line is not JSON: %q", l)
		}
		proto, _ := schemas(t)
		conforms(t, proto, "reply", l)
		replies = append(replies, o)
	}
	return replies
}

func withLimits(f func(*limits.Limits)) limits.Limits {
	l := limits.Default()
	f(&l)
	return l
}

func reasonOf(side obj) string {
	rs := reasonsOf(side)
	if len(rs) != 1 {
		return ""
	}
	return rs[0]
}

// ---- the command line ----

func TestLineLimitIsExactAndDoesNotCorruptTheStream(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxLineBytes = 200 })
	// An unknown command whose id pads the line to an exact length.
	line := func(n int) string {
		base := `{"id":"","type":"nope"}`
		return `{"id":"` + strings.Repeat("x", n-len(base)) + `","type":"nope"}`
	}
	if len(line(200)) != 200 || len(line(201)) != 201 {
		t.Fatal("test setup")
	}
	r := runWith(t, lim, line(200), line(201), `{"id":"after","type":"nope"}`, line(5000))
	if errCode(t, r[0]) != ErrUnknownCommand {
		t.Errorf("a line of exactly the limit is accepted and processed: %v", r[0])
	}
	if errCode(t, r[1]) != ErrLineTooLong || r[1]["id"] != nil {
		t.Errorf("one byte over is refused with a null id: %v", r[1])
	}
	if errCode(t, r[2]) != ErrUnknownCommand || r[2]["id"] != "after" {
		t.Errorf("the line after an over-long one is processed normally: %v", r[2])
	}
	if errCode(t, r[3]) != ErrLineTooLong {
		t.Errorf("a very long line is refused: %v", r[3])
	}
	if msg := r[1]["error"].(obj)["message"].(string); strings.Contains(msg, "xxxx") {
		t.Errorf("the refusal must not echo the line: %q", msg)
	}
}

func TestInitializeReportsTheEffectiveLimits(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 1234; l.MaxHeaders = 9 })
	r := runWith(t, lim, init0())
	got := result(t, r[0])["limits"].(obj)
	if got["max_body_bytes"] != float64(1234) || got["max_headers"] != float64(9) {
		t.Errorf("limits: %v", got)
	}
	// A server with no explicit limits reports the defaults.
	r = run(t, init0())
	def := result(t, r[0])["limits"].(obj)
	if def["max_body_bytes"] != float64(limits.Default().MaxBodyBytes) || len(def) != 6 {
		t.Errorf("defaults: %v", def)
	}
}

// ---- body size ----

func usersPost(body string, headers string) string {
	return `{"method":"POST","url":"http://example.test/users","headers":` + headers + `,"body_base64":"` + b64(body) + `"}`
}

const okHeaders = `[["Content-Type","application/json"],["X-Request-Id","abc"]]`

func TestOversizedBodyIsIncompleteNeverValid(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 100 })
	atLimit := `{"name":"` + strings.Repeat("a", 100-len(`{"name":"","age":1,"profile":{"bio":"b"}}`)) + `","age":1,"profile":{"bio":"b"}}`
	if len(atLimit) != 100 {
		t.Fatalf("setup: %d", len(atLimit))
	}
	over := atLimit + " " // 101 bytes, still valid JSON
	r := runWith(t, lim, init0(),
		validate(2, usersPost(atLimit, okHeaders), "", ""),
		validate(3, usersPost(over, okHeaders), "", ""),
		// Missing X-Request-Id: an independent finding that must survive the skipped body.
		validate(4, usersPost(over, `[["Content-Type","application/json"]]`), "", ""),
		// 101 bytes that are not JSON: would be body.decode if inspected.
		validate(5, usersPost(strings.Repeat("x", 101), okHeaders), "", ""),
	)
	if res := result(t, r[1]); res["valid"] != true || res["complete"] != true {
		t.Errorf("a body of exactly the limit is validated normally: %v", res)
	}
	big := sideOf(result(t, r[2]), "request")
	if big["status"] != "incomplete" || big["complete"] != false || reasonOf(big) != "body.too_large" {
		t.Fatalf("over the limit: %v", big)
	}
	det := big["diagnostics"].([]any)[0].(obj)["detail"].(obj)
	if det["limit_bytes"] != "100" || det["body_bytes"] != "101" {
		t.Errorf("the limit and the size are reported (never the content): %v", det)
	}
	if result(t, r[2])["valid"] == true {
		t.Errorf("an uninspected body must never be valid")
	}
	// Independent findings survive.
	found := false
	for _, c := range codes(sideOf(result(t, r[3]), "request")) {
		found = found || c == "parameter.required"
	}
	if !found {
		t.Errorf("findings unrelated to the body must still be reported: %v", codes(sideOf(result(t, r[3]), "request")))
	}
	// Body findings the library would have produced are dropped (here body.decode).
	for _, c := range codes(sideOf(result(t, r[4]), "request")) {
		if c == "body.decode" || c == "body.required" {
			t.Errorf("a skipped body must not produce body findings: %v", c)
		}
	}
}

func TestOversizedRequiredBodyIsNotReportedMissing(t *testing.T) {
	// /users requires a body. The library never sees the oversized one, and would report it missing.
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 10 })
	r := runWith(t, lim, init0(), validate(2, usersPost(goodUser, okHeaders), "", ""))
	for _, c := range codes(sideOf(result(t, r[1]), "request")) {
		if c == "body.required" {
			t.Errorf("an oversized body is not a missing body")
		}
	}
	if reasonOf(sideOf(result(t, r[1]), "request")) != "body.too_large" {
		t.Errorf("%v", sideOf(result(t, r[1]), "request"))
	}
}

func TestOversizedResponseBody(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 20 })
	req := `{"method":"GET","url":"http://example.test/users/7"}`
	res := func(b string) string {
		return `{"status":200,"headers":[["Content-Type","application/json"]],"body_base64":"` + b64(b) + `"}`
	}
	small, big := `{"id":1,"name":"a"}`, `{"id":1,"name":"`+strings.Repeat("a", 30)+`"}`
	r := runWith(t, lim, init0(),
		validate(2, req, res(small), `{"request":false}`),
		validate(3, req, res(big), `{"request":false}`),
		validate(4, req, res(big), ""), // both sides: the request has no body and is unaffected
	)
	if got := result(t, r[1]); got["valid"] != true {
		t.Errorf("within the limit: %v", got)
	}
	if s := sideOf(result(t, r[2]), "response"); s["status"] != "incomplete" || reasonOf(s) != "body.too_large" {
		t.Errorf("response over the limit: %v", s)
	}
	both := result(t, r[3])
	if sideOf(both, "request")["status"] != "valid" || sideOf(both, "response")["status"] != "incomplete" || both["valid"] == true {
		t.Errorf("only the response side is incomplete: %v", both)
	}
}

// ---- nesting depth ----

func TestNestingDepthLimit(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxNestingDepth = 5 })
	nestedArrays := func(d int) string { return strings.Repeat("[", d) + strings.Repeat("]", d) }
	nestedObjects := func(d int) string { return strings.Repeat(`{"a":`, d) + "1" + strings.Repeat("}", d) }
	cases := []struct {
		name    string
		body    string
		tooDeep bool
	}{
		{"arrays at the limit", nestedArrays(5), false},
		{"arrays over the limit", nestedArrays(6), true},
		{"objects at the limit", nestedObjects(5), false},
		{"objects over the limit", nestedObjects(6), true},
		{"unterminated and deep", strings.Repeat("[", 50), true},
		{"brackets inside a string do not count", `{"name":"[[[[[[[[[[[[[[[","age":1,"profile":{"bio":"{{{{{{{{{{"}}`, false},
		{"escaped quotes do not end a string", `{"name":"a\"[[[[[[[[[[","age":1,"profile":{"bio":"b"}}`, false},
		{"a backslash before the closing quote", `{"name":"a\\\\","age":1,"profile":{"bio":"[[[[[[[[["}}`, false},
	}
	var lines []string
	lines = append(lines, init0())
	for i, c := range cases {
		lines = append(lines, validate(2+i, usersPost(c.body, okHeaders), "", ""))
	}
	r := runWith(t, lim, lines...)
	for i, c := range cases {
		side := sideOf(result(t, r[1+i]), "request")
		got := reasonOf(side) == "body.too_deep"
		if got != c.tooDeep {
			t.Errorf("%s: too_deep=%v, want %v (%v)", c.name, got, c.tooDeep, side)
		}
		if c.tooDeep {
			det := side["diagnostics"].([]any)[0].(obj)["detail"].(obj)
			if side["status"] != "incomplete" || det["limit_depth"] != "5" {
				t.Errorf("%s: %v", c.name, side)
			}
		}
	}
}

func TestNestingDepthOnlyAppliesToJSONBodies(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxNestingDepth = 3 })
	text := `{"method":"POST","url":"http://example.test/notes","headers":[["Content-Type","text/plain"]],"body_base64":"` + b64(strings.Repeat("[", 50)) + `"}`
	res := result(t, runWith(t, lim, init0(), validate(2, text, "", ""))[1])
	if res["valid"] != true {
		t.Errorf("a text body is a string, not nested JSON: %v", res)
	}
}

// ---- headers ----

func TestHeaderLimits(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxHeaders = 3; l.MaxHeaderValueBytes = 10; l.MaxHeaderBytes = 40 })
	get := func(h hdrs) string { return validate(2, getReq("/hdr", h), "", "") }
	v10, v11 := strings.Repeat("v", 10), strings.Repeat("v", 11)
	cases := []struct {
		name string
		h    hdrs
		ok   bool
	}{
		{"three entries, a value of exactly 10 bytes, total exactly 40", hdrs{{"X-Any", v10}, {"X-A", v10}, {"X-B", v10}}, true}, // 5+10 + 3+10 + 3+10 = 41? see below
		{"four entries", hdrs{{"X-Any", "v"}, {"A", "1"}, {"B", "2"}, {"C", "3"}}, false},
		{"a value of 11 bytes", hdrs{{"X-Any", v11}}, false},
		{"names and values total over the limit", hdrs{{"X-Any", v10}, {"X-Aaaaaaaaaa", v10}, {"X-Bbbbbbbbb", v10}}, false},
	}
	// X-Any(5)+10 + X-A(3)+10 + X-B(3)+10 = 41 > 40, so the first case would fail the total limit;
	// use a 9-byte value in one of them to land exactly on 40.
	cases[0].h = hdrs{{"X-Any", v10}, {"X-A", v10}, {"X-B", strings.Repeat("v", 9)}}
	for _, c := range cases {
		r := runWith(t, lim, init0(), get(c.h))
		if c.ok {
			if r[1]["type"] != "result" {
				t.Errorf("%s: must be accepted: %v", c.name, r[1])
			}
			continue
		}
		if errCode(t, r[1]) != ErrUnsupportedInput {
			t.Errorf("%s: %v", c.name, r[1])
		}
		if msg := r[1]["error"].(obj)["message"].(string); strings.Contains(msg, "vvvvv") {
			t.Errorf("%s: the message must not echo header content: %q", c.name, msg)
		}
	}
}

func TestHeaderLimitsApplyToDecodedValuesAndOnlyValidatedResponses(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxHeaderValueBytes = 10 })
	big := strings.Repeat("v", 11)
	// The limit is on the decoded value, whichever way it was sent.
	enc := `{"method":"GET","url":"http://example.test/hdr","headers":[["X-Any","` + b64(big) + `",true]]}`
	if errCode(t, runWith(t, lim, init0(), validate(2, enc, "", ""))[1]) != ErrUnsupportedInput {
		t.Errorf("a base64 value is limited after decoding")
	}
	short := `{"method":"GET","url":"http://example.test/hdr","headers":[["X-Any","` + b64("vvvvvvvvvv") + `",true]]}`
	if r := runWith(t, lim, init0(), validate(2, short, "", "")); r[1]["type"] != "result" {
		t.Errorf("a decoded value of exactly the limit is accepted (the encoded text is longer): %v", r[1])
	}
	// A response that is not being validated is not judged.
	req := getReq("/hdr", hdrs{{"X-Any", "v"}})
	res := `{"status":200,"headers":[["X-Big","` + big + `"]]}`
	if r := runWith(t, lim, init0(), validate(2, req, res, `{"response":false}`)); r[1]["type"] != "result" {
		t.Errorf("response headers are only judged when the response is validated: %v", r[1])
	}
	if r := runWith(t, lim, init0(), validate(2, req, res, "")); errCode(t, r[1]) != ErrUnsupportedInput {
		t.Errorf("a validated response is judged: %v", r[1])
	}
}

// A response body that was skipped for size was still sent, so a missing Content-Type is still
// worth reporting: the "bodiless response needs no Content-Type" allowance must not apply.
func TestSkippedResponseBodyWithoutContentTypeKeepsTheMediaTypeFinding(t *testing.T) {
	lim := withLimits(func(l *limits.Limits) { l.MaxBodyBytes = 20 })
	req := `{"method":"GET","url":"http://example.test/users/7"}`
	big := `{"id":1,"name":"` + strings.Repeat("a", 30) + `"}`
	res := `{"status":200,"headers":[],"body_base64":"` + b64(big) + `"}`
	side := sideOf(result(t, runWith(t, lim, init0(), validate(2, req, res, `{"request":false}`))[1]), "response")
	got := strings.Join(codes(side), ",")
	if !strings.Contains(got, "body.media_type") || reasonOf(side) != "body.too_large" {
		t.Errorf("want body.media_type and body.too_large, got %v / %v", got, side)
	}
}
