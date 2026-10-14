package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/uperl/go-humbug/internal/diagnostics"
	"github.com/uperl/go-humbug/internal/validation"
)

// capture is a stand-in implementation that records exactly what the protocol layer hands it.
// These tests pin the wire contract independent of any validator: headers arrive as the ordered
// list the client sent, so another implementation can be swapped in behind the same protocol.
type capture struct{ got []validation.Params }

func (c *capture) Validate(p validation.Params) (*diagnostics.Result, error) {
	c.got = append(c.got, p)
	return &diagnostics.Result{Request: diagnostics.Skipped(), Response: diagnostics.Skipped()}, nil
}

func serveCapture(t *testing.T, lines ...string) (*capture, []obj) {
	t.Helper()
	c := &capture{}
	s := &Server{Load: func(string) (Validator, validation.SpecInfo, error) { return c, validation.SpecInfo{}, nil }}
	var out bytes.Buffer
	in := init0() + "\n" + strings.Join(lines, "\n") + "\n"
	if err := s.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var replies []obj
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var o obj
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		replies = append(replies, o)
	}
	return c, replies[1:] // drop the initialize reply
}

func toHeaders(h hdrs) []validation.Header {
	out := make([]validation.Header, len(h))
	for i, p := range h {
		out[i] = validation.Header(p)
	}
	return out
}

func TestWireCarriesHeadersExactlyAsSent(t *testing.T) {
	cases := map[string]hdrs{
		"duplicates stay separate":      {{"Set-Cookie", "a=1"}, {"Set-Cookie", "b=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT"}},
		"casing is not normalized":      {{"x-request-id", "1"}, {"X-REQUEST-ID", "2"}, {"X-Request-Id", "3"}},
		"interleaved order is kept":     {{"A", "1"}, {"B", "2"}, {"A", "3"}, {"C", "4"}, {"B", "5"}},
		"empty value is kept":           {{"X-Empty", ""}, {"X-Space", " "}},
		"value whitespace is kept":      {{"X-Pad", "  padded \t"}},
		"unicode in a value is kept":    {{"X-Name", "Zoë 😀"}},
		"comma list is not split":       {{"Accept", "text/html, application/json;q=0.9"}},
		"unusual names are not judged":  {{"Bad Name", "v"}, {"", "v"}, {"X:Y", "v"}},
		"control characters not judged": {{"X-A", "line1\r\nInjected: 1"}, {"X-B", "nul\u0000byte"}},
		"no headers":                    {},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			req := getReq("/anything", h)
			res := `{"status":200,"headers":` + h.json() + `}`
			c, replies := serveCapture(t, validate(2, req, res, `{"response":false}`))
			if replies[0]["type"] != "result" {
				t.Fatalf("the protocol layer must not reject headers: %v", replies[0])
			}
			if len(c.got) != 1 {
				t.Fatalf("validator called %d times", len(c.got))
			}
			want := toHeaders(h)
			if !reflect.DeepEqual(c.got[0].Request.Headers, want) {
				t.Errorf("request headers\n got %q\nwant %q", c.got[0].Request.Headers, want)
			}
			// Response headers are handed over verbatim too, even when response validation is off.
			if c.got[0].Response == nil || !reflect.DeepEqual(c.got[0].Response.Headers, want) {
				t.Errorf("response headers: %+v", c.got[0].Response)
			}
		})
	}
}

func TestWireAbsentAndEmptyHeaderListsAreBothNoHeaders(t *testing.T) {
	c, _ := serveCapture(t,
		`{"id":2,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x"}}}`,
		`{"id":3,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x","headers":[]}}}`,
	)
	for i, p := range c.got {
		if len(p.Request.Headers) != 0 {
			t.Errorf("call %d: %q", i, p.Request.Headers)
		}
	}
}

// A decoder fixed to two elements would drop a third element and invent a missing value, silently
// changing what was sent. Malformed pairs are rejected instead.
func TestWireRejectsMalformedHeaderPairsInsteadOfAlteringThem(t *testing.T) {
	bad := map[string]string{
		"one element":    `[["X-Only"]]`,
		"three elements": `[["X-A","v","extra"]]`,
		"empty pair":     `[[]]`,
		"null pair":      `[null]`,
		"number value":   `[["X-A",1]]`,
		"not an array":   `["X-A: v"]`,
		"object pair":    `[{"name":"X-A","value":"v"}]`,
	}
	for name, headers := range bad {
		t.Run(name, func(t *testing.T) {
			line := `{"id":2,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x","headers":` + headers + `}}}`
			c, replies := serveCapture(t, line)
			if errCode(t, replies[0]) != ErrInvalidMessage {
				t.Errorf("reply: %v", replies[0])
			}
			if len(c.got) != 0 {
				t.Errorf("a malformed pair must not reach the validator: %+v", c.got)
			}
		})
	}
	// The same applies to response headers.
	line := `{"id":2,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x"},"response":{"status":200,"headers":[["X-A","v","extra"]]}}}`
	if _, replies := serveCapture(t, line); errCode(t, replies[0]) != ErrInvalidMessage {
		t.Errorf("response pair: %v", replies[0])
	}
}

// ---- base64 header values: [name, value, base64?] ----

// rawReq builds a validate command whose request headers are the given raw JSON entries.
func rawReq(entries ...string) string {
	return `{"id":2,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x","headers":[` + strings.Join(entries, ",") + `]}}}`
}

func TestWireBase64HeaderValues(t *testing.T) {
	c, replies := serveCapture(t, rawReq(
		`["X-Decoded","Zm9v",true]`,         // base64 of "foo"
		`["X-Explicit-False","Zm9v",false]`, // not decoded
		`["X-Default","Zm9v"]`,              // default is false: not decoded
		`["X-Binary","//4=",true]`,          // 0xff 0xfe: not valid UTF-8
		`["X-Empty","",true]`,               // base64 of the empty value
		`["X-Control","DQo=",true]`,         // CR LF: delivered; judging it is the implementation's job
		`["Zm9v","dg==",true]`,              // names are never encoded
		`["X-Decoded","YmFy",true]`,         // a duplicate name stays a separate entry, in order
	))
	if replies[0]["type"] != "result" {
		t.Fatalf("reply: %v", replies[0])
	}
	want := []validation.Header{
		{"X-Decoded", "foo"},
		{"X-Explicit-False", "Zm9v"},
		{"X-Default", "Zm9v"},
		{"X-Binary", "\xff\xfe"},
		{"X-Empty", ""},
		{"X-Control", "\r\n"},
		{"Zm9v", "v"},
		{"X-Decoded", "bar"},
	}
	if len(c.got) != 1 || !reflect.DeepEqual(c.got[0].Request.Headers, want) {
		t.Errorf("headers\n got %q\nwant %q", c.got[0].Request.Headers, want)
	}
}

func TestWireBase64AppliesToResponseHeadersToo(t *testing.T) {
	line := `{"id":2,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/x"},"response":{"status":200,"headers":[["X-Binary","//4=",true],["X-Text","hi"]]}}}`
	c, _ := serveCapture(t, line)
	want := []validation.Header{{"X-Binary", "\xff\xfe"}, {"X-Text", "hi"}}
	if len(c.got) != 1 || c.got[0].Response == nil || !reflect.DeepEqual(c.got[0].Response.Headers, want) {
		t.Errorf("response headers: %+v", c.got)
	}
}

func TestWireRejectsMalformedHeaderEntriesInsteadOfAlteringThem(t *testing.T) {
	bad := map[string]string{
		"base64 as string":      `["X-A","v","true"]`,
		"base64 as number":      `["X-A","v",1]`,
		"base64 as null":        `["X-A","v",null]`,
		"four elements":         `["X-A","v",true,"extra"]`,
		"invalid base64":        `["X-A","!!!",true]`,
		"unpadded base64":       `["X-A","Zm9",true]`,
		"base64 with newline":   `["X-A","Zm9v\n",true]`,
		"base64 with CR":        `["X-A","Zm9v\r",true]`,
		"base64 with space":     `["X-A","Zm 9v",true]`,
		"url-safe base64":       `["X-A","__4=",true]`,
		"null value":            `["X-A",null]`,
		"null name":             `[null,"v"]`,
		"numeric name":          `[1,"v"]`,
		"boolean value":         `["X-A",true]`,
		"object entry":          `{"name":"X-A","value":"v"}`,
		"string entry":          `"X-A: v"`,
		"null entry":            `null`,
		"empty entry":           `[]`,
		"one element":           `["X-A"]`,
		"non-boolean flag only": `["X-A","v","yes"]`,
	}
	for name, entry := range bad {
		t.Run(name, func(t *testing.T) {
			c, replies := serveCapture(t, rawReq(entry))
			if errCode(t, replies[0]) != ErrInvalidMessage {
				t.Errorf("reply: %v", replies[0])
			}
			if len(c.got) != 0 {
				t.Errorf("must not reach the validator: %+v", c.got)
			}
			if msg := replies[0]["error"].(obj)["message"].(string); strings.Contains(msg, "!!!") {
				t.Errorf("error echoes content: %q", msg)
			}
		})
	}
}
