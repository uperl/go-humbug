package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func b64b(b []byte) string { return b64(string(b)) }

func postRaw(headers string, body []byte) string {
	return `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]` + headers + `],"body_base64":"` + b64b(body) + `"}`
}

func sideOf(r obj, side string) obj { return r[side].(obj) }

func spanText(t *testing.T, body []byte, inst obj) string {
	t.Helper()
	sp := inst["span"].(obj)
	return string(body[int(sp["start"].(float64)):int(sp["end"].(float64))])
}

func TestDuplicateKeysAreMarkedAmbiguous(t *testing.T) {
	body := []byte(`{"name":"Ann","age":5,"age":"x","profile":{"bio":"b"}}`)
	r := result(t, run(t, init0(), validate(2, postRaw("", body), "", ""))[1])
	ds := sideOf(r, "request")["diagnostics"].([]any)
	if len(ds) != 1 {
		t.Fatalf("diagnostics: %v", ds)
	}
	inst := ds[0].(obj)["instance"].(obj)
	sp := inst["span"].(obj)
	if inst["pointer"] != "/age" || sp["ambiguous"] != true || spanText(t, body, inst) != `"x"` {
		t.Errorf("instance: %v", inst)
	}
}

func TestLargeBody(t *testing.T) {
	const size = 8 << 20
	bio := strings.Repeat("a", size)
	good := []byte(`{"name":"Ann","age":1,"profile":{"bio":"` + bio + `"}}`)
	bad := []byte(`{"name":"Ann","profile":{"bio":"` + bio + `"},"age":"x"}`)
	start := time.Now()
	r := run(t, init0(), validate(2, postRaw("", good), "", ""), validate(3, postRaw("", bad), "", ""))
	if result(t, r[1])["valid"] != true {
		t.Errorf("large valid body: %v", result(t, r[1]))
	}
	inst := sideOf(result(t, r[2]), "request")["diagnostics"].([]any)[0].(obj)["instance"].(obj)
	if inst["pointer"] != "/age" || spanText(t, bad, inst) != `"x"` || inst["span"].(obj)["start"].(float64) < size {
		t.Errorf("span in large body: %v", inst)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("large bodies took %v", d)
	}
}

func TestGzipBodies(t *testing.T) {
	enc := `,["Content-Encoding","gzip"]`
	good := []byte(`{"name":"Ann","age":1,"profile":{"bio":"b"}}`)
	bad := []byte(`{"name":"Ann","age":"x","profile":{"bio":"b"}}`)
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	zero := make([]byte, 1<<20)
	for i := 0; i < 65; i++ { // 65 MiB decoded, over the 64 MiB cap
		zw.Write(zero)
	}
	zw.Close()

	r := run(t, init0(),
		validate(2, postRaw(enc, gz(t, good)), "", ""),
		validate(3, postRaw(enc, gz(t, bad)), "", ""),
		validate(4, postRaw(enc, []byte("not gzip at all")), "", ""),
		validate(5, postRaw(`,["Content-Encoding","br"]`, []byte("whatever")), "", ""),
		validate(6, postRaw(`,["content-encoding","identity"]`, good), "", ""),
		validate(7, postRaw(`,["Content-Encoding","gzip, br"]`, gz(t, good)), "", ""),
		validate(8, postRaw(enc, bomb.Bytes()), "", ""),
	)
	if res := result(t, r[1]); res["valid"] != true {
		t.Errorf("gzip good: %v", res)
	}
	// Spans refer to the decoded body.
	inst := sideOf(result(t, r[2]), "request")["diagnostics"].([]any)[0].(obj)["instance"].(obj)
	if inst["pointer"] != "/age" || spanText(t, bad, inst) != `"x"` {
		t.Errorf("gzip bad: %v", inst)
	}
	corrupt := sideOf(result(t, r[3]), "request")["diagnostics"].([]any)
	if len(corrupt) != 1 || corrupt[0].(obj)["code"] != "body.decode" || corrupt[0].(obj)["detail"].(obj)["content_encoding"] != "gzip" {
		t.Errorf("corrupt gzip: %v", corrupt)
	}
	for i, want := range map[int]string{4: "body.content_encoding_unsupported", 6: "body.content_encoding_unsupported", 7: "body.too_large"} {
		side := sideOf(result(t, r[i]), "request")
		ds := side["diagnostics"].([]any)
		if side["status"] != "incomplete" || len(ds) != 1 || ds[0].(obj)["reason"] != want {
			t.Errorf("reply %d: want %s, got %v", i, want, side)
		}
	}
	if res := result(t, r[5]); res["valid"] != true {
		t.Errorf("identity encoding: %v", res)
	}
}

func TestGzipResponseBody(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/users/7"}`
	mk := func(body []byte) string {
		return `{"status":200,"headers":[["Content-Type","application/json"],["Content-Encoding","gzip"]],"body_base64":"` + b64b(gz(t, body)) + `"}`
	}
	r := run(t, init0(),
		validate(2, req, mk([]byte(`{"id":1,"name":"Ann"}`)), `{"request":false}`),
		validate(3, req, mk([]byte(`{"id":"1","name":"Ann"}`)), `{"request":false}`),
	)
	if result(t, r[1])["valid"] != true {
		t.Errorf("good gzip response: %v", result(t, r[1]))
	}
	if c := codes(sideOf(result(t, r[2]), "response")); len(c) != 1 || c[0] != "schema.type" {
		t.Errorf("bad gzip response: %v", c)
	}
}

// Cross-file $ref: the schema line/column come from the referenced file
// (components.yaml), not the root document; the library does not expose the file name.
func TestCrossFileSchemaLocation(t *testing.T) {
	req := `{"method":"POST","url":"http://example.test/addresses","headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"street":"s","zip":"abc"}`) + `"}`
	r := result(t, run(t, init0(), validate(2, req, "", ""))[1])
	d := sideOf(r, "request")["diagnostics"].([]any)[0].(obj)
	sch := d["schema"].(obj)
	if d["code"] != "schema.pattern" || sch["line"] != float64(4) || sch["column"] != float64(7) {
		t.Errorf("schema location: %v", d)
	}
	if _, has := sch["uri"]; has {
		t.Errorf("uri must be omitted, not guessed: %v", sch)
	}
}

func TestUnresolvableReferencesFailInitialization(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte("type: string\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	write := func(name, ref string) string {
		p := filepath.Join(dir, name)
		doc := "openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get:\n      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '" + ref + "'}\n"
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, ref := range map[string]string{
		"missing-file.yaml": "./does-not-exist.yaml#/components/schemas/X",
		"remote.yaml":       srv.URL + "/schema.yaml",
	} {
		spec := write(name, ref)
		r := run(t, `{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"`+spec+`"}}}`)
		if errCode(t, r[0]) != ErrSpecInvalid {
			t.Errorf("%s: %v", name, r[0])
		}
	}
	if hits != 0 {
		t.Errorf("remote reference was fetched %d time(s); remote fetching must be off", hits)
	}
}

// KNOWN LIBRARY LIMITATION (pb33f v0.16.1): primitive label- and matrix-style path
// parameters keep their prefix (".5", ";id=5") when validated against the schema, so
// valid requests are reported invalid. Humbug does not mask this (no heuristic
// suppression). If a library upgrade fixes it, this test fails: update the fixtures and docs.
func TestPathParameterStyles(t *testing.T) {
	get := func(path string) string { return `{"method":"GET","url":"http://example.test` + path + `"}` }
	r := run(t, init0(),
		validate(2, get("/list/1,2,3"), "", ""),
		validate(3, get("/list/1,x,3"), "", ""),
		validate(4, get("/label/.5"), "", ""),
		validate(5, get("/matrix/;id=5"), "", ""),
	)
	if result(t, r[1])["valid"] != true {
		t.Errorf("simple array: %v", result(t, r[1]))
	}
	if c := codes(sideOf(result(t, r[2]), "request")); len(c) != 1 || c[0] != "parameter.invalid" {
		t.Errorf("simple array bad item: %v", c)
	}
	for i, name := range map[int]string{3: "label", 4: "matrix"} {
		if c := codes(sideOf(result(t, r[i]), "request")); len(c) != 1 || c[0] != "parameter.invalid" {
			t.Errorf("%s style: library behavior changed (%v); see the KNOWN LIBRARY LIMITATION note", name, c)
		}
	}
}

func diagsOf(r obj, side string) []obj {
	var out []obj
	for _, d := range r[side].(obj)["diagnostics"].([]any) {
		out = append(out, d.(obj))
	}
	return out
}

func TestTypeDiagnosticsCarryExpectedAndActual(t *testing.T) {
	req := postUsers(`{"name":"Ann","age":"thirty","profile":{"bio":"x"}}`)
	d := diagsOf(result(t, run(t, init0(), validate(2, req, "", ""))[1]), "request")[0]
	exp, _ := d["expected"].([]any)
	if d["code"] != "schema.type" || len(exp) != 1 || exp[0] != "integer" || d["actual"] != "string" {
		t.Errorf("type metadata: %v", d)
	}
	if strings.Contains(strings.ToLower(jsonText(d)), "thirty") {
		t.Errorf("payload value leaked into a diagnostic: %v", d)
	}
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestRoutingNoteIsRelatedToRequestFinding(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/nope"}`
	res := `{"status":200,"headers":[]}`
	r := run(t, init0(), validate(2, req, res, ""), validate(3, req, res, `{"request":false}`))

	both := result(t, r[1])
	primary := diagsOf(both, "request")[0]
	note := diagsOf(both, "response")[0]
	if primary["suppressed_by"] != nil {
		t.Errorf("primary must never be suppressed: %v", primary)
	}
	if note["suppressed_by"] != primary["id"] {
		t.Errorf("response note suppressed_by %v, want %v", note["suppressed_by"], primary["id"])
	}
	if cb, _ := note["caused_by"].([]any); len(cb) != 1 || cb[0] != primary["id"] {
		t.Errorf("caused_by: %v", note["caused_by"])
	}
	// Suppressed findings are preserved and the transaction is still not valid or complete.
	if both["valid"] == true || both["complete"] == true || both["has_violations"] != true {
		t.Errorf("transaction flags: %v", both)
	}

	// Response-only: no request-side primary exists, so the note must stay visible.
	only := diagsOf(result(t, r[2]), "response")[0]
	if only["suppressed_by"] != nil {
		t.Errorf("note suppressed without a primary: %v", only)
	}
}

// Both sides have composition groups; their parent/child references must resolve within
// their own side after IDs are assigned.
func TestCompositionOnBothSidesKeepsReferencesApart(t *testing.T) {
	req := `{"method":"POST","url":"http://example.test/both","headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"fax":1}`) + `"}`
	res := `{"status":200,"headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"pager":2}`) + `"}`
	r := result(t, run(t, init0(), validate(2, req, res, ""))[1])

	seen := map[string]string{} // id -> side
	for _, side := range []string{"request", "response"} {
		for _, d := range diagsOf(r, side) {
			id := d["id"].(string)
			if prev, dup := seen[id]; dup {
				t.Fatalf("id %s used on both %s and %s", id, prev, side)
			}
			seen[id] = side
		}
	}
	groups := 0
	for _, side := range []string{"request", "response"} {
		for _, d := range diagsOf(r, side) {
			refs, _ := d["children"].([]any)
			if p, ok := d["parent"].(string); ok {
				refs = append(refs, p)
			}
			for _, ref := range refs {
				if seen[ref.(string)] != side {
					t.Errorf("%s diagnostic %v references %v from the other side", side, d["id"], ref)
				}
			}
			if len(refs) > 0 {
				groups++
			}
		}
	}
	if groups == 0 {
		t.Errorf("expected composition groups on both sides: %v", r)
	}
}
