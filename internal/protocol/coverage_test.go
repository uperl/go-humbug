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

// Content-Encoding headers are preserved and passed through but never acted on: body_base64
// is the already-decoded body, so a gzip header over a plain JSON body is validated as JSON.
func TestContentEncodingHeaderIsIgnored(t *testing.T) {
	good := []byte(`{"name":"Ann","age":1,"profile":{"bio":"b"}}`)
	bad := []byte(`{"name":"Ann","age":"x","profile":{"bio":"b"}}`)
	for _, enc := range []string{"gzip", "br", "gzip, br", "identity", "deflate"} {
		h := `,["Content-Encoding","` + enc + `"]`
		r := run(t, init0(), validate(2, postRaw(h, good), "", ""), validate(3, postRaw(h, bad), "", ""))
		if res := result(t, r[1]); res["valid"] != true || res["complete"] != true {
			t.Errorf("%s: decoded body must validate normally: %v", enc, res)
		}
		d := diagsOf(result(t, r[2]), "request")
		if len(d) != 1 || d[0]["code"] != "schema.type" || spanText(t, bad, d[0]["instance"].(obj)) != `"x"` {
			t.Errorf("%s: span must refer to the supplied bytes: %v", enc, d)
		}
	}
}

// If a client sends still-compressed bytes (a contract violation on its side), Humbug does not
// decompress them: the bytes are malformed JSON and are reported as such.
func TestCompressedBytesAreNotDecoded(t *testing.T) {
	body := gz(t, []byte(`{"name":"Ann","age":1,"profile":{"bio":"b"}}`))
	r := result(t, run(t, init0(), validate(2, postRaw(`,["Content-Encoding","gzip"]`, body), "", ""))[1])
	d := diagsOf(r, "request")
	if len(d) != 1 || d[0]["code"] != "body.decode" || d[0]["severity"] != "error" {
		t.Errorf("compressed bytes: %v", d)
	}
	if det, _ := d[0]["detail"].(obj); det["content_encoding"] != nil {
		t.Errorf("Content-Encoding must not influence diagnostics: %v", d[0])
	}
}

func TestContentEncodingOnResponseIsIgnored(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/users/7"}`
	mk := func(body string) string {
		return `{"status":200,"headers":[["Content-Type","application/json"],["Content-Encoding","gzip"]],"body_base64":"` + b64(body) + `"}`
	}
	r := run(t, init0(),
		validate(2, req, mk(`{"id":1,"name":"Ann"}`), `{"request":false}`),
		validate(3, req, mk(`{"id":"1","name":"Ann"}`), `{"request":false}`),
	)
	if result(t, r[1])["valid"] != true {
		t.Errorf("response: %v", result(t, r[1]))
	}
	if c := codes(sideOf(result(t, r[2]), "response")); len(c) != 1 || c[0] != "schema.type" {
		t.Errorf("bad response: %v", c)
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

// ---- non-JSON bodies: validated by schema shape, never by media-type name ----

func putRaw(url, ct string, body []byte, method string) string {
	h := `[]`
	if ct != "" {
		h = `[["Content-Type","` + ct + `"]]`
	}
	return `{"method":"` + method + `","url":"http://example.test` + url + `","headers":` + h + `,"body_base64":"` + b64b(body) + `"}`
}

func reasonsOf(side obj) []string {
	var out []string
	for _, d := range side["diagnostics"].([]any) {
		if r, ok := d.(obj)["reason"].(string); ok {
			out = append(out, r)
		}
	}
	return out
}

func TestApplicationVCLIsValidatedAsAString(t *testing.T) {
	r := run(t, init0(),
		validate(2, putRaw("/vcl", "application/vcl", []byte("sub vcl_recv { }"), "PUT"), "", ""),
		validate(3, putRaw("/vcl", "application/vcl", []byte("sub"), "PUT"), "", ""),
		validate(4, putRaw("/vcl", "Application/VCL; charset=UTF-8", []byte("sub vcl_recv { }"), "PUT"), "", ""),
		validate(5, putRaw("/vcl", "application/vcl", []byte("sub \xff\xfe"), "PUT"), "", ""),
		validate(6, putRaw("/vcl", "application/vcl; charset=iso-8859-1", []byte("sub vcl_recv { }"), "PUT"), "", ""),
		// Not VCL at all, but it is a conforming string: Humbug does not check the format.
		validate(7, putRaw("/vcl", "application/vcl", []byte(`{"this":"is not VCL"}`), "PUT"), "", ""),
	)
	if res := result(t, r[1]); res["valid"] != true || res["complete"] != true {
		t.Errorf("valid UTF-8 VCL: %v", res)
	}

	short := diagsOf(result(t, r[2]), "request")
	if len(short) != 1 || short[0]["code"] != "schema.minLength" || short[0]["severity"] != "error" {
		t.Fatalf("too short: %v", short)
	}
	inst := short[0]["instance"].(obj)
	if inst["location"] != "body" || inst["pointer"] != nil || inst["span"] != nil {
		t.Errorf("a non-JSON body has no pointer or span: %v", inst)
	}

	if res := result(t, r[3]); res["valid"] != true {
		t.Errorf("media type and charset are case-insensitive: %v", res)
	}

	bad := diagsOf(result(t, r[4]), "request")
	if len(bad) != 1 || bad[0]["code"] != "body.decode" || bad[0]["detail"].(obj)["utf8_offset"] != "4" {
		t.Errorf("invalid UTF-8: %v", bad)
	}

	cs := sideOf(result(t, r[5]), "request")
	if cs["status"] != "incomplete" || len(reasonsOf(cs)) != 1 || reasonsOf(cs)[0] != "body.charset_unsupported" {
		t.Errorf("non-UTF-8 charset must be incomplete, not valid: %v", cs)
	}

	if res := result(t, r[6]); res["valid"] != true {
		t.Errorf("content that is not VCL still conforms to a string schema: %v", res)
	}
}

func TestNonJSONResponseBodies(t *testing.T) {
	req := putRaw("/vcl", "application/vcl", []byte("sub vcl_recv { }"), "PUT")
	mk := func(status int, body string) string {
		return `{"status":` + itoa(status) + `,"headers":[["Content-Type","application/vcl"]],"body_base64":"` + b64(body) + `"}`
	}
	r := run(t, init0(),
		validate(2, req, mk(201, "ok"), `{"request":false}`), // matched via the 2XX wildcard
		validate(3, req, mk(201, ""), `{"request":false}`),   // explicitly empty body, minLength 1
	)
	if res := result(t, r[1]); res["valid"] != true {
		t.Errorf("2XX response: %v", res)
	}
	d := diagsOf(result(t, r[2]), "response")
	if len(d) != 1 || d[0]["code"] != "schema.minLength" || d[0]["side"] != "response" {
		t.Errorf("empty response body: %v", d)
	}
}

func TestNonJSONSchemaShapes(t *testing.T) {
	r := run(t, init0(),
		validate(2, putRaw("/form", "application/x-www-form-urlencoded", []byte("a=1"), "POST"), "", ""),
		validate(3, putRaw("/anycsv", "text/csv", []byte("a,b\n1,2"), "POST"), "", ""),
		validate(4, putRaw("/wild", "text/vcl", []byte("abcde"), "POST"), "", ""),
		validate(5, putRaw("/wild", "text/vcl", []byte("abcdef"), "POST"), "", ""),
		validate(6, putRaw("/wild", "application/vcl", []byte("abc"), "POST"), "", ""),
		validate(7, putRaw("/notes", "", []byte("hello"), "POST"), "", ""),
	)
	form := sideOf(result(t, r[1]), "request")
	if form["status"] != "incomplete" || len(reasonsOf(form)) != 1 || reasonsOf(form)[0] != "body.not_inspected" {
		t.Errorf("structured schema under a non-JSON type must stay incomplete: %v", form)
	}
	if res := result(t, r[2]); res["valid"] != true || res["complete"] != true {
		t.Errorf("declared without a schema, any content conforms: %v", res)
	}
	if res := result(t, r[3]); res["valid"] != true {
		t.Errorf("text/* wildcard, within limit: %v", res)
	}
	if c := codes(sideOf(result(t, r[4]), "request")); len(c) != 1 || c[0] != "schema.maxLength" {
		t.Errorf("text/* wildcard, too long: %v", c)
	}
	if c := codes(sideOf(result(t, r[5]), "request")); len(c) != 1 || c[0] != "body.media_type" {
		t.Errorf("undeclared media type is a media-type finding only: %v", c)
	}
	// No Content-Type: cannot tell what the body is, so it is never reported valid.
	none := result(t, r[6])
	if none["valid"] == true || sideOf(none, "request")["status"] == "valid" {
		t.Errorf("body without Content-Type must not be valid: %v", none)
	}
}

func TestTextBodyIsNotParsedAsJSON(t *testing.T) {
	// These look like JSON (or a bare number) but are strings under a text media type.
	for _, body := range []string{`{"a":1}`, `123`, `null`, `[1,2`} {
		r := result(t, run(t, init0(), validate(2, putRaw("/notes", "text/plain", []byte(body), "POST"), "", ""))[1])
		if r["valid"] != true || r["complete"] != true {
			t.Errorf("%q must be a conforming string: %v", body, r)
		}
	}
}

// A violating text body that happens to be valid JSON must still get no pointer or span:
// positions would point into a structure the sender never meant.
func TestViolatingTextBodyThatParsesAsJSONHasNoSpan(t *testing.T) {
	r := result(t, run(t, init0(), validate(2, putRaw("/notes", "text/plain", []byte(`12`), "POST"), "", ""))[1])
	d := diagsOf(r, "request")
	if len(d) != 1 || d[0]["code"] != "schema.minLength" {
		t.Fatalf("diagnostics: %v", d)
	}
	if inst := d[0]["instance"].(obj); inst["span"] != nil || inst["pointer"] != nil {
		t.Errorf("text body must have no span or pointer: %v", inst)
	}
}

func TestBinaryLengthCountsBytesTextCountsCharacters(t *testing.T) {
	// "ééé" is 3 characters but 6 bytes. maxLength is 4 on /blob (binary) and 5 on /wild (text).
	r := run(t, init0(),
		validate(2, putRaw("/blob", "application/octet-stream", []byte("ééé"), "POST"), "", ""),
		validate(3, putRaw("/wild", "text/plain", []byte("ééé"), "POST"), "", ""),
	)
	if c := codes(sideOf(result(t, r[1]), "request")); len(c) != 1 || c[0] != "schema.maxLength" {
		t.Errorf("binary counts bytes: %v", c)
	}
	if res := result(t, r[2]); res["valid"] != true {
		t.Errorf("text counts characters: %v", res)
	}
}

// The operation in the result comes from the same routing the validator uses, so a request the
// validator routes (and validates) is never reported without an operation.
func TestOperationIsReportedForEveryRoutedPath(t *testing.T) {
	bad := `{"name":"Ann","age":"x","profile":{"bio":"b"}}`
	hd := `[["Content-Type","application/json"],["X-Request-Id","abc"]]`
	post := func(path string) string {
		return `{"method":"POST","url":"http://example.test` + path + `","headers":` + hd + `,"body_base64":"` + b64(bad) + `"}`
	}
	paths := []string{"/users", "/users/", "//users", "/users//", "/users/?x=1"}
	var lines []string
	for i, p := range paths {
		lines = append(lines, validate(2+i, post(p), "", ""))
	}
	r := run(t, append([]string{init0()}, lines...)...)
	for i, p := range paths {
		res := result(t, r[1+i])
		op, _ := res["operation"].(obj)
		if op == nil || op["path"] != "/users" || op["operation_id"] != "createUser" {
			t.Errorf("%s: operation %v (the request was validated, so it must be reported)", p, res["operation"])
		}
		if c := codes(sideOf(res, "request")); len(c) != 1 || c[0] != "schema.type" {
			t.Errorf("%s: %v", p, c)
		}
	}
}

// A body sent where the operation declares no content has nothing to be checked against, so it
// is not "uninspected": the side stays complete.
func TestUndeclaredBodyWithoutContentTypeIsNotIncomplete(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/users/7","headers":[],"body_base64":"` + b64("junk") + `"}`
	res := `{"status":204,"headers":[],"body_base64":"` + b64("junk") + `"}`
	postContacts := `{"method":"POST","url":"http://example.test/contacts","headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"email":"a@b"}`) + `"}`
	r := run(t, init0(),
		validate(2, req, "", ""),
		validate(3, postContacts, res, `{"request":false}`), // 204 declares no content
	)
	for i, label := range map[int]string{1: "request", 2: "response"} {
		got := result(t, r[i])
		if got["complete"] != true || got["valid"] != true {
			t.Errorf("%s: an undeclared body is not 'uninspected': %v", label, got)
		}
	}
}

// Declared content with no usable Content-Type is still never reported valid.
func TestDeclaredContentWithoutContentTypeStaysIncomplete(t *testing.T) {
	req := `{"method":"POST","url":"http://example.test/notes","headers":[],"body_base64":"` + b64("hello") + `"}`
	r := result(t, run(t, init0(), validate(2, req, "", ""))[1])
	if r["valid"] == true || r["complete"] == true {
		t.Errorf("declared content, no Content-Type: %v", r)
	}
}

// ---- undeclared bodies (warning) and bodiless responses ----

func respRaw(status int, h hdrs, body *string) string {
	b := ""
	if body != nil {
		b = `,"body_base64":"` + b64(*body) + `"`
	}
	return `{"status":` + itoa(status) + `,"headers":` + h.json() + b + `}`
}

func sp(s string) *string { return &s }

func TestUndeclaredBodiesAreWarnings(t *testing.T) {
	j, tx := hdrs{{"Content-Type", "application/json"}}, hdrs{{"Content-Type", "text/plain"}}
	post := `{"method":"POST","url":"http://example.test/contacts","headers":[["Content-Type","application/json"]],"body_base64":"` + b64(`{"email":"a@b"}`) + `"}`
	only := `{"response":true,"request":false}`
	r := run(t, init0(),
		validate(2, post, respRaw(204, j, sp(`{"x":1}`)), only), // 204 declares no content
		validate(3, post, respRaw(204, nil, sp(`junk`)), only),  // no Content-Type
		validate(4, post, respRaw(204, tx, sp(`junk`)), only),   // non-JSON
		validate(5, post, respRaw(204, j, sp(``)), only),        // explicitly empty body: nothing sent
		validate(6, post, respRaw(204, j, nil), only),           // no body
		validate(7, `{"method":"GET","url":"http://example.test/users/7","headers":[["Content-Type","application/json"]],"body_base64":"`+b64(`{"x":1}`)+`"}`, "", ""), // request, no requestBody declared
		validate(8, `{"method":"GET","url":"http://example.test/users/7","headers":[],"body_base64":"`+b64(`junk`)+`"}`, "", ""),
		validate(9, `{"method":"GET","url":"http://example.test/users/7","headers":[["Content-Type","application/json"]],"body_base64":""}`, "", ""),
	)
	for i, label := range map[int]string{1: "json", 2: "no Content-Type", 3: "text"} {
		res := result(t, r[i])
		d := findCode(diagsOf(res, "response"), "body.undeclared")
		if d == nil || d["severity"] != "warning" || d["instance"].(obj)["location"] != "body" {
			t.Errorf("%s: response body on a 204: %v", label, diagsOf(res, "response"))
			continue
		}
		// A warning is a fact, not a violation.
		if res["valid"] != true || res["complete"] != true || res["has_violations"] != false {
			t.Errorf("%s: warning must not affect validity: %v", label, res)
		}
	}
	if det := findCode(diagsOf(result(t, r[1]), "response"), "body.undeclared")["detail"].(obj); det["media_type"] != "application/json" {
		t.Errorf("media type detail: %v", det)
	}
	if det, _ := findCode(diagsOf(result(t, r[2]), "response"), "body.undeclared")["detail"].(obj); det["media_type"] != nil {
		t.Errorf("no media type when none was sent: %v", det)
	}
	for _, i := range []int{4, 5} {
		if len(diagsOf(result(t, r[i]), "response")) != 0 {
			t.Errorf("an empty or absent body is not an undeclared body: %v", diagsOf(result(t, r[i]), "response"))
		}
	}
	for _, i := range []int{6, 7} {
		res := result(t, r[i])
		if d := findCode(diagsOf(res, "request"), "body.undeclared"); d == nil || d["severity"] != "warning" || res["valid"] != true || res["complete"] != true {
			t.Errorf("request body with no requestBody declared: %v", res)
		}
	}
	if len(diagsOf(result(t, r[8]), "request")) != 0 {
		t.Errorf("empty request body: %v", diagsOf(result(t, r[7]), "request"))
	}
}

func TestUndeclaredBodyNotReportedWhenThereIsNothingToCompareTo(t *testing.T) {
	j := hdrs{{"Content-Type", "application/json"}}
	post := `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"` + b64(goodUser) + `"}`
	r := run(t, init0(),
		// Status 418 is not defined for the operation: that is its own finding, and whether a body is
		// declared is unknowable.
		validate(2, post, respRaw(418, j, sp(`{"x":1}`)), `{"request":false}`),
		// Unmatched operation.
		validate(3, `{"method":"GET","url":"http://example.test/nope","headers":[],"body_base64":"`+b64(`x`)+`"}`, "", ""),
		// Declared content: a body is expected.
		validate(4, post, "", ""),
	)
	if c := codes(sideOf(result(t, r[1]), "response")); len(c) != 1 || c[0] != "response.status" {
		t.Errorf("undeclared status: %v", c)
	}
	if findCode(diagsOf(result(t, r[2]), "request"), "body.undeclared") != nil {
		t.Errorf("unmatched operation: %v", diagsOf(result(t, r[2]), "request"))
	}
	if len(diagsOf(result(t, r[3]), "request")) != 0 {
		t.Errorf("declared body: %v", diagsOf(result(t, r[3]), "request"))
	}
}

// A response with no body and no Content-Type has no media type to check, so the library's
// "media type" finding is dropped. Anything else about the media type still counts.
func TestBodilessResponseWithoutContentTypeIsValid(t *testing.T) {
	req := `{"method":"GET","url":"http://example.test/users/7"}`
	only := `{"request":false}`
	j, tx := hdrs{{"Content-Type", "application/json"}}, hdrs{{"Content-Type", "text/plain"}}
	r := run(t, init0(),
		validate(2, req, respRaw(200, nil, nil), only),                       // nothing sent at all
		validate(3, req, respRaw(200, nil, sp(``)), only),                    // empty body, still no Content-Type
		validate(4, req, respRaw(200, nil, sp(`{"id":1,"name":"a"}`)), only), // body with no Content-Type: still a finding
		validate(5, req, respRaw(200, tx, nil), only),                        // an undeclared Content-Type is still wrong
		validate(6, req, respRaw(200, j, nil), only),                         // declared Content-Type, no body
	)
	for _, i := range []int{1, 2} {
		if res := result(t, r[i]); res["valid"] != true || res["complete"] != true || len(diagsOf(res, "response")) != 0 {
			t.Errorf("reply %d: bodiless, no Content-Type must be clean: %v", i+1, res)
		}
	}
	withBody := result(t, r[3])
	if withBody["valid"] == true || findCode(diagsOf(withBody, "response"), "body.media_type") == nil {
		t.Errorf("a body with no Content-Type is still reported: %v", diagsOf(withBody, "response"))
	}
	if c := codes(sideOf(result(t, r[4]), "response")); len(c) != 1 || c[0] != "body.media_type" {
		t.Errorf("an undeclared Content-Type is still reported: %v", c)
	}
	if res := result(t, r[5]); res["valid"] != true {
		t.Errorf("declared Content-Type, no body: %v", res)
	}
}
