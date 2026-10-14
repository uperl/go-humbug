package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"humbug/internal/diagnostics"
	"humbug/internal/validation"
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
