package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"humbug/internal/limits"
)

func TestExceedsDepthTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		max  int
		want bool
	}{
		{"empty", "", 0, false},
		{"scalar", "123", 0, false},
		{"one array, limit 1", "[]", 1, false},
		{"one array, limit 0", "[]", 0, true},
		{"nested at the limit", "[[[]]]", 3, false},
		{"nested over the limit", "[[[]]]", 2, true},
		{"mixed objects and arrays", `{"a":[{"b":[1]}]}`, 4, false},
		{"mixed over", `{"a":[{"b":[1]}]}`, 3, true},
		{"siblings do not add up", `[[],[],[],[],[]]`, 2, false},
		{"brackets in a string", `["[[[[[[[[["]`, 1, false},
		{"braces in a string", `{"k":"{{{{{{{{"}`, 1, false},
		{"escaped quote keeps the string open", `["a\"[[[["]`, 1, false},
		{"escaped backslash then a quote ends it", `["a\\"][[[`, 1, true},
		{"brackets in a key", `{"[[[[":1}`, 1, false},
		{"unterminated string", `["[[[[`, 1, false},
		{"closing brackets alone", `]]]]]]`, 1, false},
		{"unbalanced opens", strings.Repeat("[", 20), 19, true},
		{"deep valid", strings.Repeat("[", 1000) + strings.Repeat("]", 1000), 1000, false},
		{"one too deep", strings.Repeat("[", 1001) + strings.Repeat("]", 1001), 1000, true},
	}
	for _, c := range cases {
		if got := exceedsDepth([]byte(c.body), c.max); got != c.want {
			t.Errorf("%s: exceedsDepth(%q, %d) = %v, want %v", c.name, abbreviate(c.body), c.max, got, c.want)
		}
	}
}

func abbreviate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// trueDepth is the nesting depth of a valid JSON document, from Go's own tokenizer.
func trueDepth(t *testing.T, doc []byte) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	depth, max := 0, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' || d == '{' {
				if depth++; depth > max {
					max = depth
				}
			} else {
				depth--
			}
		}
	}
	return max
}

func randomDoc(r *rand.Rand, depth int, sb *strings.Builder) {
	strs := []string{`"x"`, `"[[[["`, `"]]]]"`, `"{{{"`, `"}}}"`, `"a\"[[["`, `"a\\"`, `"\\\\["`, `"[\"]"`, `""`}
	k := r.Intn(10)
	if depth > 12 {
		k = 6 + r.Intn(4)
	}
	switch {
	case k < 3:
		sb.WriteByte('{')
		for i, n := 0, r.Intn(4); i < n; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strs[r.Intn(len(strs))])
			sb.WriteByte(':')
			randomDoc(r, depth+1, sb)
		}
		sb.WriteByte('}')
	case k < 6:
		sb.WriteByte('[')
		for i, n := 0, r.Intn(4); i < n; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			randomDoc(r, depth+1, sb)
		}
		sb.WriteByte(']')
	case k < 8:
		sb.WriteString(strs[r.Intn(len(strs))])
	default:
		sb.WriteString(fmt.Sprint(r.Intn(1000)))
	}
}

// For valid JSON the scan must agree with the tokenizer at every limit.
func TestExceedsDepthAgreesWithTheTokenizer(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		var sb strings.Builder
		randomDoc(r, 0, &sb)
		doc := []byte(sb.String())
		if !json.Valid(doc) {
			t.Fatalf("generator produced invalid JSON: %s", doc)
		}
		d := trueDepth(t, doc)
		for max := 0; max <= d+1; max++ {
			if got, want := exceedsDepth(doc, max), d > max; got != want {
				t.Fatalf("depth %d, limit %d: exceedsDepth=%v, want %v\n%s", d, max, got, want, doc)
			}
		}
	}
}

func TestBodyLimitDecisions(t *testing.T) {
	e := &Engine{lim: limits.Limits{MaxBodyBytes: 10, MaxNestingDepth: 3}}
	json := []Header{{"Content-Type", "application/json"}}
	body := func(s string) *[]byte { b := []byte(s); return &b }
	if e.bodyLimit(nil, json) != nil {
		t.Errorf("no body, no limit")
	}
	if s := e.bodyLimit(body("[[[]]]"), json); s != nil {
		t.Errorf("depth 3 is at the limit and must pass: %v", s)
	}
	if s := e.bodyLimit(body("[[[[]]]]"), json); s == nil || s.reason != "body.too_deep" {
		t.Errorf("depth 4: %v", s)
	}
	if s := e.bodyLimit(body("01234567890"), json); s == nil || s.reason != "body.too_large" {
		t.Errorf("11 bytes: %v", s)
	}
	if e.bodyLimit(body("0123456789"), json) != nil {
		t.Errorf("exactly the size limit must pass")
	}
	// Size is checked before depth, and depth only for JSON.
	if s := e.bodyLimit(body("[[[[[[[[[[[[[[["), json); s == nil || s.reason != "body.too_large" {
		t.Errorf("size is checked first: %v", s)
	}
	if e.bodyLimit(body("[[[[[[[["), []Header{{"Content-Type", "text/plain"}}) != nil {
		t.Errorf("depth is not checked for non-JSON bodies")
	}
	if s := e.bodyLimit(body("[[[[]]]]"), []Header{{"Content-Type", "application/vnd.api+json"}}); s == nil || s.reason != "body.too_deep" {
		t.Errorf("+json counts as JSON: %v", s)
	}
}
