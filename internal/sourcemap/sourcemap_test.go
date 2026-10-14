package sourcemap

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

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

func target(t *testing.T, ix *Index, ptr string) (Span, string, bool, bool) {
	t.Helper()
	s, part, amb, exact, ok := ix.Target(ptr)
	if !ok {
		t.Fatalf("no target for %q", ptr)
	}
	return s, part, amb, exact
}

func TestTargetPrefersTheMemberName(t *testing.T) {
	body := []byte("{\n  \"name\": \"Ann\",\n  \"tags\": [1, {\"a\": true}],\n  \"a/b\": 2\n}")
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	for ptr, want := range map[string]struct{ text, part string }{
		"/name":     {`"name"`, PartKey},
		"/tags":     {`"tags"`, PartKey},
		"/tags/1/a": {`"a"`, PartKey},   // a member of an object inside an array
		"/a~1b":     {`"a/b"`, PartKey}, // escaped pointer, name as written in the document
		"/tags/0":   {`1`, PartValue},   // array elements have no name
		"/tags/1":   {`{"a": true}`, PartValue},
	} {
		s, part, amb, exact := target(t, ix, ptr)
		if got := string(body[s.Start:s.End]); got != want.text || part != want.part || amb || !exact {
			t.Errorf("%s: %q part=%s amb=%v exact=%v; want %q %s", ptr, got, part, amb, exact, want.text, want.part)
		}
	}
	// The root has no name.
	if s, part, _, _ := target(t, ix, ""); part != PartValue || s.Start != 0 || s.End != len(body) {
		t.Errorf("root: %+v %s", s, part)
	}
	// Line and column of a key.
	if s, _, _, _ := target(t, ix, "/tags"); s.Line != 3 || s.Column != 3 {
		t.Errorf("/tags at line %d column %d", s.Line, s.Column)
	}
}

func TestTargetOfAMissingPropertyIsTheNearestAncestor(t *testing.T) {
	body := []byte(`{"profile": {"x": 1}}`)
	ix, _ := Build(body)
	s, part, _, exact := target(t, ix, "/profile/bio") // does not exist
	if exact || part != PartKey || string(body[s.Start:s.End]) != `"profile"` {
		t.Errorf("nearest ancestor: %q part=%s exact=%v", body[s.Start:s.End], part, exact)
	}
	// A missing property of the root falls back to the whole document.
	s, part, _, exact = target(t, ix, "/nope")
	if exact || part != PartValue || s.Start != 0 || s.End != len(body) {
		t.Errorf("root fallback: %+v %s exact=%v", s, part, exact)
	}
}

func TestTargetKeyColumnsAreUTF8Bytes(t *testing.T) {
	body := []byte(`{"é😀": 1, "z": 2}`)
	ix, _ := Build(body)
	s, _, _, _ := target(t, ix, "/z")
	if got := string(body[s.Start:s.End]); got != `"z"` || s.Column != s.Start+1 {
		t.Errorf("%q column %d start %d (single line: column = byte offset + 1)", got, s.Column, s.Start)
	}
	// The key spans the whole quoted name, including multi-byte characters.
	s, _, _, _ = target(t, ix, "/é😀")
	if got := string(body[s.Start:s.End]); got != `"é😀"` {
		t.Errorf("multi-byte key: %q", got)
	}
}

func TestTargetDuplicateKeysAreAmbiguousAndUseTheLastKey(t *testing.T) {
	body := []byte(`{"a": 1, "a": 22}`)
	ix, _ := Build(body)
	s, part, amb, _ := target(t, ix, "/a")
	if !amb || part != PartKey || s.Start != 9 {
		t.Errorf("start=%d part=%s ambiguous=%v; want the second key at 9", s.Start, part, amb)
	}
}

// ---- regression and resource tests for the tree-based index ----

func allocMiB(f func()) float64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / (1 << 20)
}

// The cost of an index must grow with the number of values, not with their nesting depth. The
// earlier implementation keyed every value by its full pointer and allocated 100 MiB at depth
// 10,000, 929 MiB at 30,000 and 6 GB at 60,000.
func TestNestingCostIsLinear(t *testing.T) {
	cases := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{"valid arrays", []byte(strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth)), false},
		{"unterminated arrays", []byte(strings.Repeat("[", MaxDepth)), true},
		{"valid objects", []byte(strings.Repeat(`{"a":`, MaxDepth) + "0" + strings.Repeat("}", MaxDepth)), false},
		{"unterminated objects", []byte(strings.Repeat(`{"a":`, MaxDepth)), true},
	}
	for _, c := range cases {
		var err error
		mib := allocMiB(func() { _, err = Build(c.body) })
		t.Logf("%s: %.2f MiB", c.name, mib)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, want error=%v", c.name, err, c.wantErr)
		}
		if mib > 12 {
			t.Errorf("%s at depth %d allocated %.1f MiB; cost must not be quadratic in depth", c.name, MaxDepth, mib)
		}
	}
}

func TestNestingDeeperThanTheLimitIsRejectedCheaply(t *testing.T) {
	for name, unit := range map[string]string{"arrays": "[", "objects": `{"a":`} {
		over := []byte(strings.Repeat(unit, MaxDepth+1))
		_, err := Build(over)
		pe, ok := err.(*ParseError)
		if !ok || !strings.Contains(pe.Msg, "maximum nesting depth") {
			t.Fatalf("%s: depth %d must be rejected as too deep, got %v", name, MaxDepth+1, err)
		}
		if want := MaxDepth * len(unit); pe.Offset != want {
			t.Errorf("%s: the offset is that of the first bracket past the limit (%d), got %d", name, want, pe.Offset)
		}
	}
	// A 20 MB body of opening brackets is refused after reading MaxDepth of them, without allocating
	// anything proportional to its size.
	huge := []byte(strings.Repeat("[", 20_000_000))
	start := time.Now()
	var err error
	mib := allocMiB(func() { _, err = Build(huge) })
	if err == nil || mib > 12 || time.Since(start) > 5*time.Second {
		t.Errorf("20M brackets: err=%v alloc=%.1f MiB took %v", err, mib, time.Since(start))
	}
	// Exactly at the limit is accepted.
	if _, err := Build([]byte(strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth))); err != nil {
		t.Errorf("depth %d must be accepted: %v", MaxDepth, err)
	}
}

func TestDeepPointersResolve(t *testing.T) {
	const depth = 9000
	body := []byte(strings.Repeat("[", depth) + "7" + strings.Repeat("]", depth))
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	ptr := strings.Repeat("/0", depth)
	s, part, _, exact := target(t, ix, ptr)
	if !exact || part != PartValue || string(body[s.Start:s.End]) != "7" {
		t.Errorf("deepest value: %q part=%s exact=%v", body[s.Start:s.End], part, exact)
	}
	// One level past the end resolves to the nearest ancestor, not exactly.
	s, _, _, exact = target(t, ix, ptr+"/0")
	if exact || string(body[s.Start:s.End]) != "7" {
		t.Errorf("past the end: %q exact=%v", body[s.Start:s.End], exact)
	}
}

func TestWideObjectsAndArraysStayFastAndCorrect(t *testing.T) {
	const n = 200_000
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"k%d":%d`, i, i)
	}
	sb.WriteString(`,"arr":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("1")
	}
	sb.WriteString("]}")
	body := []byte(sb.String())

	start := time.Now()
	ix, err := Build(body)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("building an index of %d members and %d elements took %v (must be linear)", n, n, d)
	}
	for _, c := range []struct{ ptr, want string }{{"/k0", `"k0"`}, {"/k199999", `"k199999"`}, {"/k100000", `"k100000"`}} {
		if s, part, _, exact := target(t, ix, c.ptr); !exact || part != PartKey || string(body[s.Start:s.End]) != c.want {
			t.Errorf("%s: %q part=%s exact=%v", c.ptr, body[s.Start:s.End], part, exact)
		}
	}
	if _, _, ok := ix.Lookup("/k200000"); ok {
		t.Errorf("a member that does not exist was found")
	}
	if s, _, ok := ix.Lookup("/arr/199999"); !ok || body[s.Start] != '1' {
		t.Errorf("last array element not found")
	}
	if _, _, ok := ix.Lookup("/arr/200000"); ok {
		t.Errorf("an element past the end was found")
	}
}

// The earlier index kept the key span of a discarded duplicate and returned it for a pointer
// that, in the surviving value, addresses an array element (which has no name).
func TestDuplicateKeysDoNotLeakTheDiscardedKeySpans(t *testing.T) {
	body := []byte(`{"q": {"0": 1}, "q": [7]}`)
	ix, _ := Build(body)
	s, part, amb, exact := target(t, ix, "/q/0")
	if !exact || part != PartValue || string(body[s.Start:s.End]) != "7" {
		t.Errorf("/q/0: %q part=%s exact=%v; want the array element 7 as a value", body[s.Start:s.End], part, exact)
	}
	if _, _, ok := ix.Lookup("/q/1"); ok {
		t.Errorf("/q/1 must not exist")
	}
	_, _, amb, _ = target(t, ix, "/q")
	if !amb {
		t.Errorf("/q is a duplicate and must be ambiguous")
	}
	// The discarded subtree is gone entirely.
	body2 := []byte(`{"a": {"x": 1}, "a": {"y": 2}}`)
	ix2, _ := Build(body2)
	if _, _, ok := ix2.Lookup("/a/x"); ok {
		t.Errorf("a member of the discarded first /a was found")
	}
	if _, _, ok := ix2.Lookup("/a/y"); !ok {
		t.Errorf("/a/y must exist")
	}
}

func TestArrayIndicesMustBeCanonical(t *testing.T) {
	body := []byte(`{"t": [10, 20, 30]}`)
	ix, _ := Build(body)
	for _, c := range []struct {
		ptr   string
		exact bool
		want  string
	}{
		{"/t/0", true, "10"}, {"/t/2", true, "30"},
		{"/t/01", false, `"t"`}, {"/t/3", false, `"t"`}, {"/t/-", false, `"t"`},
		{"/t/1.0", false, `"t"`}, {"/t/+1", false, `"t"`}, {"/t/", false, `"t"`},
		{"/t/99999999999999999999", false, `"t"`},
	} {
		s, _, _, exact := target(t, ix, c.ptr)
		if exact != c.exact || string(body[s.Start:s.End]) != c.want {
			t.Errorf("%s: %q exact=%v; want %q exact=%v", c.ptr, body[s.Start:s.End], exact, c.want, c.exact)
		}
	}
}

// Strings that are not JSON Pointers must not crash (the earlier index panicked on some) and
// must not match anything.
func TestStringsThatAreNotPointersNeverPanic(t *testing.T) {
	ix, _ := Build([]byte(`{"a": {"b": 1}, "": 2, "a~": 3}`))
	for _, p := range []string{"a", "a/b", "~", "x", "  ", "/a/b~2", "/a~", "/~", "/a/~3"} {
		_, _, _, _, ok := ix.Target(p)
		if p[0] != '/' && ok {
			t.Errorf("%q is not a pointer but was found", p)
		}
		ix.Lookup(p)
		ix.Nearest(p)
	}
	// Empty and escaped member names.
	body := []byte(`{"": 2, "a~": 3, "a/b": 4}`)
	ix2, _ := Build(body)
	for ptr, want := range map[string]string{"/": `""`, "/a~0": `"a~"`, "/a~1b": `"a/b"`} {
		if s, part, _, exact := target(t, ix2, ptr); !exact || part != PartKey || string(body[s.Start:s.End]) != want {
			t.Errorf("%q: %q part=%s exact=%v", ptr, body[s.Start:s.End], part, exact)
		}
	}
}
