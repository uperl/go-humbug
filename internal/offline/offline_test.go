package offline

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The OAS 3.1 meta-schema and everything it refers to. The JSON Schema 2020-12 meta-schemas are
// left out: the validator has them built in (see fetchRemote in package validation).
var want = []string{
	"https://spec.openapis.org/oas/3.1/dialect/base",
	"https://spec.openapis.org/oas/3.1/meta/base",
	"https://spec.openapis.org/oas/3.1/schema-base/2022-10-07",
	"https://spec.openapis.org/oas/3.1/schema/2022-10-07",
}

func TestBundledURIs(t *testing.T) {
	got := URIs()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bundled URIs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Each document is stored under its own $id, so a $ref to that URI gets that document.
func TestBundledDocumentsAreNamedByTheirID(t *testing.T) {
	for _, uri := range URIs() {
		b, _ := Lookup(uri)
		var doc struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if doc.ID != uri {
			t.Errorf("%s has $id %q", uri, doc.ID)
		}
	}
}

// Every remote document a bundled one refers to is bundled too, or is a JSON Schema 2020-12
// meta-schema, which the validator has built in.
func TestBundleIsClosed(t *testing.T) {
	for _, uri := range URIs() {
		b, _ := Lookup(uri)
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		base, _ := url.Parse(uri)
		var walk func(v any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				for k, x := range v {
					if s, ok := x.(string); ok && (k == "$ref" || k == "$schema") {
						r, err := base.Parse(s)
						if err != nil {
							t.Errorf("%s: %s %q: %v", uri, k, s, err)
							continue
						}
						r.Fragment = ""
						target := r.String()
						if _, ok := Lookup(target); !ok && !strings.HasPrefix(target, "https://json-schema.org/draft/2020-12/") {
							t.Errorf("%s: %s %q is not bundled", uri, k, s)
						}
					}
					walk(x)
				}
			case []any:
				for _, x := range v {
					walk(x)
				}
			}
		}
		walk(doc)
	}
}

func TestLookupIsExact(t *testing.T) {
	const u = "https://spec.openapis.org/oas/3.1/schema-base/2022-10-07"
	for _, ok := range []string{u, u + "#"} {
		if _, found := Lookup(ok); !found {
			t.Errorf("%s not found", ok)
		}
	}
	for _, miss := range []string{
		"http://spec.openapis.org/oas/3.1/schema-base/2022-10-07",
		u + "/", u + ".json", u + "?x=1", "HTTPS://spec.openapis.org/oas/3.1/schema-base/2022-10-07",
		"https://spec.openapis.org/oas/3.1/schema-base/2024-10-25", "https://example.invalid/",
	} {
		if _, found := Lookup(miss); found {
			t.Errorf("%s found", miss)
		}
		if _, err := Fetch(miss); !errors.As(err, new(*NotBundledError)) {
			t.Errorf("Fetch(%s): %v", miss, err)
		}
	}
}

func TestTransport(t *testing.T) {
	c := &http.Client{Transport: Transport{}}
	res, err := c.Get("https://spec.openapis.org/oas/3.1/dialect/base")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if want, _ := Lookup("https://spec.openapis.org/oas/3.1/dialect/base"); res.StatusCode != 200 || string(b) != string(want) {
		t.Fatalf("status %d, body %q", res.StatusCode, b)
	}
	if _, err := c.Get("https://example.invalid/schema.json"); err == nil {
		t.Error("an unbundled URI was answered")
	}
	if _, err := c.Post("https://spec.openapis.org/oas/3.1/dialect/base", "application/json", strings.NewReader("{}")); err == nil {
		t.Error("a POST was answered")
	}
}
