package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

const (
	specPath  = "../../testdata/specs/users.yaml"
	casesPath = "../../testdata/cases/cases.json"
	goldenDir = "../../testdata/golden"
)

type obj = map[string]any

// run feeds lines to a fresh server and returns one decoded reply per output line.
func run(t *testing.T, lines ...string) []obj {
	t.Helper()
	var out bytes.Buffer
	s := &Server{Load: EngineLoader}
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var replies []obj
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var o obj
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			t.Fatalf("stdout line is not JSON (contamination?): %q", l)
		}
		replies = append(replies, o)
	}
	return replies
}

func init0() string {
	return `{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"` + specPath + `"}}}`
}

func errCode(t *testing.T, r obj) string {
	t.Helper()
	if r["type"] != "error" {
		t.Fatalf("expected error reply, got %v", r)
	}
	return r["error"].(obj)["code"].(string)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestLifecycleAndErrors(t *testing.T) {
	cases := []struct {
		name string
		line string
		code string
	}{
		{"malformed json", `{"id":1,`, ErrInvalidMessage},
		{"not an object", `[1,2]`, ErrInvalidMessage},
		{"trailing data", `{"id":1,"type":"shutdown"} {}`, ErrInvalidMessage},
		{"blank line", ``, ErrInvalidMessage},
		{"float id", `{"id":1.5,"type":"validate"}`, ErrInvalidMessage},
		{"bool id", `{"id":true,"type":"validate"}`, ErrInvalidMessage},
		{"missing id", `{"type":"validate"}`, ErrInvalidMessage},
		{"unknown command", `{"id":"x","type":"frobnicate"}`, ErrUnknownCommand},
		{"missing type", `{"id":2}`, ErrInvalidMessage},
		{"validate before init", `{"id":3,"type":"validate","params":{}}`, ErrNotInitialized},
		{"unsupported version", `{"id":4,"type":"initialize","params":{"protocol_version":2,"spec":{"path":"x"}}}`, ErrUnsupportedVersion},
		{"missing spec", `{"id":5,"type":"initialize","params":{"protocol_version":1}}`, ErrInvalidMessage},
		{"unreadable spec", `{"id":6,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"/no/such/file.yaml"}}}`, ErrSpecInvalid},
		{"unknown param", `{"id":7,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"x"},"bogus":1}}`, ErrInvalidMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := run(t, c.line)
			if got := errCode(t, r[0]); got != c.code {
				t.Errorf("code %s want %s", got, c.code)
			}
		})
	}
}

func TestIDsEchoedWithoutCoercion(t *testing.T) {
	r := run(t, `{"id":"abc","type":"nope"}`, `{"id":42,"type":"nope"}`, `{"id":-7,"type":"nope"}`)
	if r[0]["id"] != "abc" || r[1]["id"] != float64(42) || r[2]["id"] != float64(-7) {
		t.Errorf("ids not echoed: %v", r)
	}
}

func TestInitializeRetryAfterFailureAndNoReinit(t *testing.T) {
	r := run(t,
		`{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"/no/such"}}}`,
		init0(),
		init0(),
		`{"id":9,"type":"shutdown"}`,
		`{"id":10,"type":"validate"}`, // never processed: shutdown ends the process
	)
	if len(r) != 4 {
		t.Fatalf("want 4 replies (shutdown stops processing), got %d", len(r))
	}
	if errCode(t, r[0]) != ErrSpecInvalid || r[1]["type"] != "result" || errCode(t, r[2]) != ErrAlreadyInit || r[3]["type"] != "result" {
		t.Errorf("unexpected sequence: %v", r)
	}
	spec := r[1]["result"].(obj)["specification"].(obj)
	if spec["openapi_version"] != "3.1.0" || spec["title"] != "Users" {
		t.Errorf("spec info: %v", spec)
	}
}

func TestEOFWithoutShutdown(t *testing.T) {
	var out bytes.Buffer
	if err := (&Server{Load: EngineLoader}).Serve(strings.NewReader(""), &out); err != nil || out.Len() != 0 {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}

func TestUnresolvedRefFailsInitialization(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "bad.yaml")
	os.WriteFile(spec, []byte("openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get:\n      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/Nope'}\n"), 0o644)
	r := run(t, `{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"`+spec+`"}}}`)
	if errCode(t, r[0]) != ErrSpecInvalid {
		t.Errorf("got %v", r[0])
	}
}

func validate(id int, req, res string, flags string) string {
	p := `"request":` + req
	if res != "" {
		p += `,"response":` + res
	}
	if flags != "" {
		p += `,"validate":` + flags
	}
	return `{"id":` + string(rune('0'+id)) + `,"type":"validate","params":{` + p + `}}`
}

const goodUser = `{"name":"Ann","age":30,"profile":{"bio":"x"}}`

func postUsers(body string) string {
	return `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"` + b64(body) + `"}`
}

func result(t *testing.T, r obj) obj {
	t.Helper()
	if r["type"] != "result" {
		t.Fatalf("want result, got %v", r)
	}
	return r["result"].(obj)
}

func TestValidateInputErrorsAndDefaults(t *testing.T) {
	good := postUsers(goodUser)
	res := `{"status":201,"headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"id":1,"name":"Ann"}`) + `"}`
	r := run(t, init0(),
		validate(2, good, "", `{"response":true}`),                          // response requested, none supplied
		validate(3, good, "", `{"request":false}`),                          // nothing to validate
		validate(4, `{"method":"GET","url":"/relative"}`, "", ""),           // not absolute
		validate(5, `{"method":"GET"}`, "", ""),                             // missing url
		validate(6, strings.Replace(good, b64(goodUser), "!!!", 1), "", ""), // bad base64
		validate(7, good, res, ""),
		validate(8, good, res, `{"request":false}`), // response only
		validate(9, good, "", ""),                   // request only
	)
	for i, want := range []string{ErrInvalidMessage, ErrInvalidMessage, ErrUnsupportedInput, ErrInvalidMessage, ErrInvalidMessage} {
		if got := errCode(t, r[i+1]); got != want {
			t.Errorf("reply %d: %s want %s", i+2, got, want)
		}
	}
	both := result(t, r[6])
	if both["valid"] != true || both["request"].(obj)["status"] != "valid" || both["response"].(obj)["status"] != "valid" {
		t.Errorf("both: %v", both)
	}
	respOnly := result(t, r[7])
	if respOnly["request"].(obj)["status"] != "skipped" || respOnly["response"].(obj)["status"] != "valid" {
		t.Errorf("response-only: %v", respOnly)
	}
	reqOnly := result(t, r[8])
	if reqOnly["request"].(obj)["status"] != "valid" || reqOnly["response"].(obj)["status"] != "skipped" {
		t.Errorf("request-only: %v", reqOnly)
	}
}

func codes(side obj) []string {
	var out []string
	for _, d := range side["diagnostics"].([]any) {
		out = append(out, d.(obj)["code"].(string))
	}
	return out
}

func TestAbsentVersusEmptyBody(t *testing.T) {
	hdr := `"headers":[["Content-Type","application/json"],["X-Request-Id","abc"]]`
	absent := `{"method":"POST","url":"http://example.test/users",` + hdr + `}`
	empty := `{"method":"POST","url":"http://example.test/users",` + hdr + `,"body_base64":""}`
	r := run(t, init0(), validate(2, absent, "", ""), validate(3, empty, "", ""))
	a, e := result(t, r[1]), result(t, r[2])
	// Both must be reported (never valid); log the classification so changes are visible.
	for name, res := range map[string]obj{"absent": a, "empty": e} {
		if res["valid"] == true {
			t.Errorf("%s body reported valid for an operation requiring a JSON body", name)
		}
	}
	if got := codes(a["request"].(obj)); len(got) != 1 || got[0] != "body.required" {
		t.Errorf("absent body: %v", got)
	}
	if got := codes(e["request"].(obj)); len(got) != 1 || got[0] != "body.decode" {
		t.Errorf("empty body: %v", got)
	}
}

func TestRepeatedHeadersAndMissingContentType(t *testing.T) {
	noCT := `{"method":"POST","url":"http://example.test/users","headers":[["X-Request-Id","abc"]],"body_base64":"` + b64(goodUser) + `"}`
	dup := `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","ab"],["x-request-id","abcd"]],"body_base64":"` + b64(goodUser) + `"}`
	r := run(t, init0(), validate(2, noCT, "", ""), validate(3, dup, "", ""))
	if result(t, r[1])["valid"] == true {
		t.Errorf("missing Content-Type must not be reported valid")
	}
	t.Logf("noCT=%v dup=%v", codes(result(t, r[1])["request"].(obj)), codes(result(t, r[2])["request"].(obj)))
}

func TestUnmatchedOperationIsIncompleteNeverValid(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/nope"}`
	res := `{"status":200,"headers":[]}`
	r := run(t, init0(), validate(2, req, res, ""), validate(3, req, res, `{"request":false}`))
	both := result(t, r[1])
	if both["valid"] == true || both["complete"] == true || both["has_violations"] != true {
		t.Errorf("unmatched both: %v", both)
	}
	rs := both["response"].(obj)
	if rs["status"] != "incomplete" || codes(rs)[0] != "validation.incomplete" {
		t.Errorf("response side: %v", rs)
	}
	only := result(t, r[2])
	if only["valid"] == true || only["complete"] == true || only["response"].(obj)["status"] != "incomplete" {
		t.Errorf("response-only unmatched must be incomplete: %v", only)
	}
}

func TestInvalidRequestDoesNotSuppressResponse(t *testing.T) {
	req := postUsers(`{"name":"Ann","age":"x","profile":{"bio":"x"}}`)
	res := `{"status":201,"headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"id":"nope"}`) + `"}`
	r := result(t, run(t, init0(), validate(2, req, res, ""))[1])
	if len(codes(r["request"].(obj))) == 0 || len(codes(r["response"].(obj))) == 0 {
		t.Errorf("both sides must report: %v", r)
	}
}

// TestGolden runs every fixture, asserts determinism over repeated runs, and
// compares to the snapshot in testdata/golden (rewrite with: go test ./internal/protocol -update).
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID  string `json:"id"`
		Req struct {
			Method, URL string
			Headers     map[string]string
			Body        *string
		}
		Res *struct {
			Status  int
			Headers map[string]string
			Body    *string
		}
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	hdrs := func(m map[string]string) string {
		var parts []string
		for k, v := range m { // single-entry order irrelevant for these fixtures' semantics
			kb, _ := json.Marshal(k)
			vb, _ := json.Marshal(v)
			parts = append(parts, `[`+string(kb)+`,`+string(vb)+`]`)
		}
		return `[` + strings.Join(parts, ",") + `]`
	}
	body := func(b *string) string {
		if b == nil {
			return ""
		}
		return `,"body_base64":"` + b64(*b) + `"`
	}
	os.MkdirAll(goldenDir, 0o755)
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			req := `{"method":"` + c.Req.Method + `","url":"http://example.test` + c.Req.URL + `","headers":` + hdrs(c.Req.Headers) + body(c.Req.Body) + `}`
			res := ""
			if c.Res != nil {
				res = `{"status":` + string(rune('0'+c.Res.Status/100)) + `00,"headers":` + hdrs(c.Res.Headers) + body(c.Res.Body) + `}`
				res = strings.Replace(res, string(rune('0'+c.Res.Status/100))+`00`, itoa(c.Res.Status), 1)
			}
			var first []byte
			for i := 0; i < 5; i++ {
				r := run(t, init0(), validate(2, req, res, ""))[1]
				b, _ := json.MarshalIndent(r["result"], "", "  ")
				if i == 0 {
					first = b
				} else if !bytes.Equal(first, b) {
					t.Fatalf("non-deterministic output on run %d", i)
				}
			}
			path := filepath.Join(goldenDir, c.ID+".json")
			if *update {
				os.WriteFile(path, append(first, '\n'), 0o644)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), first) {
				t.Errorf("golden mismatch for %s; run with -update and review the diff", c.ID)
			}
		})
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
