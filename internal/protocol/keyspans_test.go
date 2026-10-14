package protocol

import (
	"strings"
	"testing"
)

type spanInfo struct {
	text, part string
	contextual bool
	pointer    any
	code       string
}

// spansOf validates body against POST /users (or the given path) and returns each body
// diagnostic's highlighted text and span part.
func spansOf(t *testing.T, path, body string) []spanInfo {
	t.Helper()
	req := `{"method":"POST","url":"http://example.test` + path + `","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"` + b64(body) + `"}`
	r := result(t, run(t, init0(), validate(2, req, "", ""))[1])
	var out []spanInfo
	for _, d := range diagsOf(r, "request") {
		inst, _ := d["instance"].(obj)
		sp, _ := inst["span"].(obj)
		if sp == nil {
			continue
		}
		out = append(out, spanInfo{
			text:       body[int(sp["start"].(float64)):int(sp["end"].(float64))],
			part:       sp["part"].(string),
			contextual: sp["contextual"] == true,
			pointer:    inst["pointer"],
			code:       d["code"].(string),
		})
	}
	return out
}

func only(t *testing.T, got []spanInfo) spanInfo {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want one span, got %+v", got)
	}
	return got[0]
}

func TestSpanIsTheMemberNameWhenThePointerAddressesAMember(t *testing.T) {
	g := only(t, spansOf(t, "/users", `{"name":"Ann","age":"thirty","profile":{"bio":"x"}}`))
	if g.text != `"age"` || g.part != "key" || g.pointer != "/age" || g.contextual {
		t.Errorf("top-level member: %+v", g)
	}
	g = only(t, spansOf(t, "/users", `{"name":"Ann","age":3,"profile":{"bio":7}}`))
	if g.text != `"bio"` || g.part != "key" || g.pointer != "/profile/bio" {
		t.Errorf("nested member: %+v", g)
	}
}

func TestSpanIsTheValueForTheRootAndArrayElements(t *testing.T) {
	g := only(t, spansOf(t, "/users", `"oops"`))
	if g.text != `"oops"` || g.part != "value" || g.pointer != nil {
		t.Errorf("root: %+v", g)
	}
	// An array response whose first element has the wrong type (an integer, not an object).
	req := `{"method":"GET","url":"http://example.test/items","headers":[["Cookie","session=abcd"]]}`
	res := `{"status":200,"headers":[["Content-Type","application/json"],["X-Rate-Limit","5"]],"body_base64":"` + b64(`[7,{"id":1,"name":"a"}]`) + `"}`
	r := result(t, run(t, init0(), validate(2, req, res, `{"request":false}`))[1])
	d := findCode(diagsOf(r, "response"), "schema.type")
	inst := d["instance"].(obj)
	sp := inst["span"].(obj)
	if inst["pointer"] != "/0" || sp["part"] != "value" || `[7,{"id":1,"name":"a"}]`[int(sp["start"].(float64)):int(sp["end"].(float64))] != "7" {
		t.Errorf("array element: %v", inst)
	}
}

func TestMissingPropertyHighlightsItsParentsName(t *testing.T) {
	g := only(t, spansOf(t, "/users", `{"name":"Ann","age":3,"profile":{}}`))
	if g.code != "schema.required" || g.text != `"profile"` || g.part != "key" || !g.contextual {
		t.Errorf("nested missing property: %+v", g)
	}
	// The root has no name, so the whole document is highlighted.
	g = only(t, spansOf(t, "/users", `{}`))
	if g.code != "schema.required" || g.text != `{}` || g.part != "value" || !g.contextual {
		t.Errorf("root missing property: %+v", g)
	}
}

func TestAdditionalPropertiesPointAtEachOffendingName(t *testing.T) {
	// Email-based allOf forbids extras: three offenders, one finding each, in pointer order.
	got := spansOf(t, "/orders", `{"email":"a@b","qty":1,"extra":true,"zzz":2}`)
	var names, pointers []string
	for _, g := range got {
		if g.code != "schema.additional_properties" {
			t.Errorf("unexpected finding: %+v", g)
			continue
		}
		if g.part != "key" || g.contextual {
			t.Errorf("must highlight the offending name exactly: %+v", g)
		}
		names = append(names, g.text)
		pointers = append(pointers, g.pointer.(string))
	}
	if strings.Join(names, ",") != `"extra","qty","zzz"` || strings.Join(pointers, ",") != "/extra,/qty,/zzz" {
		t.Errorf("names %v pointers %v", names, pointers)
	}
}

func TestAdditionalPropertiesInsideCompositionFallBackToTheObject(t *testing.T) {
	// Across oneOf branches the rejected names cannot be attributed with certainty: one finding
	// per branch constraint, for the object (which has no name at the root).
	got := spansOf(t, "/contacts", `{"email":"a@b","fax":"1"}`)
	n := 0
	for _, g := range got {
		if g.code == "schema.additional_properties" {
			n++
			if g.part != "value" || g.pointer != nil {
				t.Errorf("composition child: %+v", g)
			}
		}
	}
	if n != 2 {
		t.Errorf("want one object-level finding per branch, got %d: %+v", n, got)
	}
}

func TestParseErrorSpanIsAPosition(t *testing.T) {
	req := `{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"` + b64(`{"name":"Ann",,}`) + `"}`
	d := findCode(diagsOf(result(t, run(t, init0(), validate(2, req, "", ""))[1]), "request"), "body.decode")
	sp := d["instance"].(obj)["span"].(obj)
	if sp["part"] != "position" || sp["start"] != sp["end"] {
		t.Errorf("parse error span: %v", sp)
	}
}

// Two additionalProperties constraints apply to the same object (allOf of two closed schemas):
// each rejects the other's property. The rejected names cannot be attributed to a constraint
// from the library's output, so Humbug does not guess: one finding per constraint, for the object.
func TestAdditionalPropertiesAreNotGuessedWhenConstraintsCollide(t *testing.T) {
	got := spansOf(t, "/exclusive", `{"email":"a@b","phone":"1"}`)
	n := 0
	for _, g := range got {
		if g.code != "schema.additional_properties" {
			continue
		}
		n++
		if g.part != "value" || g.pointer != nil {
			t.Errorf("must fall back to the object, not guess a member: %+v", g)
		}
	}
	if n != 2 {
		t.Errorf("want one finding per colliding constraint, got %d: %+v", n, got)
	}
}
