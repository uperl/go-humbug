package sourcemap

import "testing"

func span(t *testing.T, ix *Index, ptr string) Span {
	t.Helper()
	s, _, ok := ix.Lookup(ptr)
	if !ok {
		t.Fatalf("no span for %q", ptr)
	}
	return s
}

func TestNestedAndMultiline(t *testing.T) {
	body := []byte("{\n  \"name\": \"Ann\",\n  \"age\": \"thirty\",\n  \"tags\": [1, {\"a\": true}]\n}")
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	s := span(t, ix, "/age")
	if got := string(body[s.Start:s.End]); got != `"thirty"` || s.Line != 3 || s.Column != 10 {
		t.Errorf("/age: %q line %d col %d", got, s.Line, s.Column)
	}
	if s := span(t, ix, "/tags/1/a"); string(body[s.Start:s.End]) != "true" {
		t.Errorf("/tags/1/a: %q", body[s.Start:s.End])
	}
	if s := span(t, ix, ""); s.Start != 0 || s.End != len(body) {
		t.Errorf("root span %+v", s)
	}
}

func TestEscapedKeysAndUnicode(t *testing.T) {
	body := []byte(`{"a/b": 1, "m~n": 2, "é😀": 3, "é": 4}`)
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	for ptr, want := range map[string]string{"/a~1b": "1", "/m~0n": "2", "/é😀": "3", "/é": "4"} {
		if s := span(t, ix, ptr); string(body[s.Start:s.End]) != want {
			t.Errorf("%s: %q want %q", ptr, body[s.Start:s.End], want)
		}
	}
	// columns are UTF-8 bytes: the value of "é" starts after multi-byte chars on the same line.
	s := span(t, ix, "/é")
	if s.Column != s.Start+1 {
		t.Errorf("column %d want %d (single line, byte columns)", s.Column, s.Start+1)
	}
}

func TestMissingPropertyNearest(t *testing.T) {
	ix, _ := Build([]byte(`{"profile": {}}`))
	s, _, exact, ok := ix.Nearest("/profile/bio")
	if !ok || exact || s.Start != 12 || s.End != 14 {
		t.Errorf("nearest: %+v exact=%v ok=%v", s, exact, ok)
	}
}

func TestDuplicateKeysAmbiguous(t *testing.T) {
	body := []byte(`{"a": 1, "a": 22}`)
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	s, amb, _ := ix.Lookup("/a")
	if !amb || string(body[s.Start:s.End]) != "22" {
		t.Errorf("dup: amb=%v span=%q", amb, body[s.Start:s.End])
	}
}

func TestParseErrorLocation(t *testing.T) {
	_, err := Build([]byte("{\n  \"a\": 1,\n  ,\n}"))
	pe, ok := err.(*ParseError)
	if !ok || pe.Line != 3 || pe.Column != 3 {
		t.Fatalf("got %#v", err)
	}
	for _, bad := range []string{"", "{", `{"a":}`, `[1,]`, `{"a":1} x`, `"abc`, `01`} {
		if _, err := Build([]byte(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
