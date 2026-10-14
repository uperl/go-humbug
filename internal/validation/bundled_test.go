package validation

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uperl/go-humbug/internal/offline"
)

const oasSchemaSpec = "../../testdata/specs/oas-schema.yaml"

func validateOpenAPIBody(t *testing.T, e *Engine, body string) []string {
	t.Helper()
	b := []byte(body)
	r, err := e.Validate(Params{
		Request:          Request{Method: "GET", URL: "http://example.test/openapi.json"},
		Response:         &Response{Status: 200, Headers: []Header{{"Content-Type", "application/json"}}, Body: &b},
		ValidateRequest:  true,
		ValidateResponse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Response.Complete {
		t.Fatalf("response not fully validated: %+v", r.Response)
	}
	var codes []string
	for _, d := range r.Response.Diagnostics {
		codes = append(codes, d.Code)
	}
	return codes
}

// Issue #5: a $ref to the canonical OAS 3.1 meta-schema loads from the bundled copy, and the
// meta-schema is applied as what it is, a schema for OpenAPI documents, including the OAS
// dialect for the Schema Objects inside them.
func TestBundledOASSchemaBase(t *testing.T) {
	e, err := Load(oasSchemaSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := http.DefaultTransport.(offline.Transport); !ok {
		t.Fatalf("http.DefaultTransport is %T after Load; the validator could reach the network", http.DefaultTransport)
	}
	doc := func(paths string) string {
		return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{` + paths + `}}`
	}
	op := func(schema string) string {
		return `"/a":{"get":{"responses":{"200":{"description":"d","content":{"application/json":{"schema":` + schema + `}}}}}}`
	}
	for _, c := range []struct {
		name, body string
		want       string // a diagnostic code that must be reported; "" means valid
	}{
		{"minimal document", doc(``), ""},
		{"document with a schema", doc(op(`{"type":"string","minLength":1}`)), ""},
		{"schema using OAS keywords", doc(op(`{"type":"object","discriminator":{"propertyName":"kind"},"xml":{"name":"x"}}`)), ""},
		{"missing info.version", `{"openapi":"3.1.0","info":{"title":"t"},"paths":{}}`, "schema.required"},
		{"wrong openapi version", `{"openapi":"2.0","info":{"title":"t","version":"1"},"paths":{}}`, "schema.pattern"},
		// Only the dialect meta-schema (dialect/base, through $dynamicRef) rejects this.
		{"invalid nested schema", doc(op(`{"type":"strin"}`)), "schema.anyOf"},
		{"not an object", `[1]`, "schema.type"},
	} {
		t.Run(c.name, func(t *testing.T) {
			codes := validateOpenAPIBody(t, e, c.body)
			if c.want == "" {
				if len(codes) != 0 {
					t.Fatalf("want valid, got %v", codes)
				}
				return
			}
			found := false
			for _, code := range codes {
				found = found || code == c.want
			}
			if !found {
				t.Fatalf("want %s, got %v", c.want, codes)
			}
		})
	}
}

// Only the exact bundled URIs resolve. Anything else, including near misses of a bundled URI,
// still fails at load, as every remote reference did before the bundle, and fails at once (see
// fetchRemote).
func TestUnbundledRemoteRefsStillFail(t *testing.T) {
	spec, err := os.ReadFile(oasSchemaSpec)
	if err != nil {
		t.Fatal(err)
	}
	const canonical = "https://spec.openapis.org/oas/3.1/schema-base/2022-10-07"
	dir := t.TempDir()
	for _, ref := range []string{
		"https://example.invalid/schemas/thing.json",
		"https://example.invalid/schemas/thing.json#/$defs/x",
		"https://example.invalid/schemas/thing",
		"http://spec.openapis.org/oas/3.1/schema-base/2022-10-07",
		"https://spec.openapis.org/oas/3.1/schema-base/2022-10-07/",
		"https://spec.openapis.org/oas/3.1/schema-base/2024-10-25",
		"https://spec.openapis.org/oas/3.1/schema-base/2022-10-07.json",
	} {
		t.Run(ref, func(t *testing.T) {
			p := filepath.Join(dir, "spec.yaml")
			if err := os.WriteFile(p, []byte(strings.Replace(string(spec), canonical, ref, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err := Load(p)
			if err == nil {
				t.Fatal("loaded; an unbundled remote reference must fail")
			}
			if d := time.Since(start); d > 500*time.Millisecond {
				t.Errorf("took %s to fail; the library is retrying a refused fetch (see fetchRemote)", d.Round(time.Millisecond))
			}
		})
	}
}

// The workaround in dropBundledRefErrors forgives one stale library error. It must not forgive
// anything else: a spec with a bundled $ref and also a broken local one still fails, naming only
// the broken one.
func TestBundledRefDoesNotHideOtherErrors(t *testing.T) {
	spec, err := os.ReadFile(oasSchemaSpec)
	if err != nil {
		t.Fatal(err)
	}
	broken := string(spec) + `  /broken:
    get:
      responses:
        "200":
          description: d
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Missing'}
`
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(p)
	if err == nil {
		t.Fatal("loaded a spec with a missing component")
	}
	if !strings.Contains(err.Error(), "#/components/schemas/Missing") || strings.Contains(err.Error(), "schema-base") {
		t.Fatalf("want only the missing component reported, got: %v", err)
	}
}
