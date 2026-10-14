package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const versionBody = "info: {title: V, version: '1'}\npaths:\n  /x:\n    get:\n      responses:\n        '204': {description: ok}\n"

func specWith(t *testing.T, header string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(header+versionBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func initPath(id int, path string) string {
	return `{"id":` + itoa(id) + `,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"` + path + `"}}}`
}

// OpenAPI 3.1.0 is the supported version. Any other is rejected at initialize with a specific
// error, because validating it under 3.1 semantics would silently misreport.
func TestOnly310IsSupported(t *testing.T) {
	ok := run(t, initPath(1, specWith(t, "openapi: 3.1.0\n")))
	if res := result(t, ok[0]); res["specification"].(obj)["openapi_version"] != "3.1.0" {
		t.Errorf("3.1.0: %v", res)
	}
	for _, header := range []string{
		"openapi: 3.0.3\n", "openapi: 3.0.0\n", "openapi: 3.1.1\n", "openapi: 3.1.2\n", "openapi: 3.2.0\n",
		"openapi: 4.0.0\n", "openapi: '3.1'\n", "openapi: 3.10.0\n", "openapi: 3.1.0-rc1\n",
	} {
		r := run(t, initPath(1, specWith(t, header)))
		if code := errCode(t, r[0]); code != ErrSpecUnsupportedVer {
			t.Errorf("%s: code %s", strings.TrimSpace(header), code)
		}
		msg := r[0]["error"].(obj)["message"].(string)
		if !strings.Contains(msg, "3.1.0") || !strings.Contains(msg, "not supported") {
			t.Errorf("%s: the message must say what is supported: %q", strings.TrimSpace(header), msg)
		}
	}
}

func TestUnsupportedVersionLeavesTheProcessUninitialized(t *testing.T) {
	good := `{"method":"GET","url":"http://example.test/x"}`
	r := run(t,
		initPath(1, specWith(t, "openapi: 3.0.3\n")),
		validate(2, good, "", ""), // still not initialized
		initPath(3, specWith(t, "openapi: 3.1.0\n")),
		validate(4, good, "", ""),
	)
	if errCode(t, r[0]) != ErrSpecUnsupportedVer || errCode(t, r[1]) != ErrNotInitialized {
		t.Errorf("after a rejected version: %v %v", r[0], r[1])
	}
	if r[2]["type"] != "result" || result(t, r[3])["valid"] != true {
		t.Errorf("a supported spec must still initialize afterwards: %v %v", r[2], r[3])
	}
}

// Swagger 2.0 is another unsupported version. A document with no version at all cannot be
// identified, so it is an invalid spec rather than an unsupported version.
func TestSwaggerAndMissingVersions(t *testing.T) {
	r := run(t, initPath(1, specWith(t, "swagger: '2.0'\n")), initPath(2, specWith(t, "")))
	if code := errCode(t, r[0]); code != ErrSpecUnsupportedVer {
		t.Errorf("swagger 2.0: code %s", code)
	}
	if code := errCode(t, r[1]); code != ErrSpecInvalid {
		t.Errorf("no version: code %s", code)
	}
}
