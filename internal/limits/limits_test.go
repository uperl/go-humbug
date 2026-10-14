package limits

import (
	"encoding/json"
	"flag"
	"io"
	"strings"
	"testing"
)

func TestDefaultsAreValidAndFitTogether(t *testing.T) {
	d := Default()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	// A command carrying a request and a response body, each at the body limit, base64-encoded
	// (4/3 expansion), must fit in one line with room for headers.
	needed := 2*(d.MaxBodyBytes*4/3) + 2*d.MaxHeaderBytes
	if needed > d.MaxLineBytes {
		t.Errorf("two maximum bodies and their headers need %d bytes but a line holds %d", needed, d.MaxLineBytes)
	}
	if d.MaxHeaderValueBytes > d.MaxHeaderBytes {
		t.Errorf("a single value limit above the total limit can never be reached")
	}
}

func TestValidateRejectsUnusableValues(t *testing.T) {
	cases := map[string]func(*Limits){
		"max-line-bytes":         func(l *Limits) { l.MaxLineBytes = 0 },
		"max-body-bytes":         func(l *Limits) { l.MaxBodyBytes = -1 },
		"max-nesting-depth":      func(l *Limits) { l.MaxNestingDepth = 0 },
		"max-headers":            func(l *Limits) { l.MaxHeaders = 0 },
		"max-header-value-bytes": func(l *Limits) { l.MaxHeaderValueBytes = 0 },
		"max-header-bytes":       func(l *Limits) { l.MaxHeaderBytes = 0 },
	}
	for name, mutate := range cases {
		l := Default()
		mutate(&l)
		if err := l.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error %v must name the flag", name, err)
		}
	}
	l := Default()
	l.MaxNestingDepth = NestingCeiling + 1
	if err := l.Validate(); err == nil {
		t.Errorf("a nesting limit above the toolchain's ceiling must be rejected")
	}
	l.MaxNestingDepth = NestingCeiling
	if err := l.Validate(); err != nil {
		t.Errorf("the ceiling itself is allowed: %v", err)
	}
}

func TestFlagsOverrideDefaults(t *testing.T) {
	l := Default()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	Bind(fs, &l)
	if err := fs.Parse([]string{"--max-body-bytes", "100", "-max-headers=5", "--max-nesting-depth", "20"}); err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.MaxBodyBytes, want.MaxHeaders, want.MaxNestingDepth = 100, 5, 20
	if l != want {
		t.Errorf("got %+v want %+v", l, want)
	}
	if err := fs.Parse([]string{"--max-body-bytes", "lots"}); err == nil {
		t.Errorf("a non-numeric value must be a parse error")
	}
}

func TestWireNamesAreStable(t *testing.T) {
	b, _ := json.Marshal(Default())
	var m map[string]int
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"max_line_bytes", "max_body_bytes", "max_nesting_depth", "max_headers", "max_header_value_bytes", "max_header_bytes"} {
		if m[k] < 1 {
			t.Errorf("%s missing or not positive: %v", k, m)
		}
	}
	if len(m) != 6 {
		t.Errorf("unexpected fields: %v", m)
	}
}
