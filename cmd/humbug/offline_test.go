package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// Issue #5: the humbug executable alone, copied to an empty directory and run from another one,
// resolves the bundled OAS meta-schema with no network access, no companion files and no
// environment. Any HTTP request it made would go to the proxy, which counts it.
func TestCopiedBinaryResolvesBundledSchemasOffline(t *testing.T) {
	var requests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "no network in this test", http.StatusBadGateway)
	}))
	defer proxy.Close()

	spec, err := filepath.Abs("../../testdata/specs/oas-schema.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "humbug")
	if err := os.WriteFile(copied, src, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(copied)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"HTTP_PROXY=" + proxy.URL, "HTTPS_PROXY=" + proxy.URL, "NO_PROXY="}
	p := startCmd(t, cmd)

	init := fmt.Sprintf(`{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":%q}}}`, spec)
	if r := p.call(init); r["type"] != "result" {
		t.Fatalf("initialize: %v", r)
	}
	validate := func(id int, doc string) map[string]any {
		return sideOf(p.call(fmt.Sprintf(`{"id":%d,"type":"validate","params":{"request":{"method":"GET","url":"http://example.test/openapi.json"},`+
			`"response":{"status":200,"headers":[["Content-Type","application/json"]],"body_base64":"%s"}}}`, id, base64Std(doc))), "response")
	}
	if s := validate(2, `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{}}`); s["valid"] != true {
		t.Errorf("valid OpenAPI document: %v", s)
	}
	if s := validate(3, `{"openapi":"3.1.0","info":{"title":"t"},"paths":{}}`); s["valid"] != false || s["complete"] != true {
		t.Errorf("OpenAPI document missing info.version: %v", s)
	}
	p.stdin.Close()
	if err := p.wait(); err != nil {
		t.Errorf("exit: %v", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("humbug made %d HTTP request(s)", n)
	}
	if p.stderr.Len() != 0 {
		t.Errorf("unexpected stderr on a clean run: %q", p.stderr)
	}
}
