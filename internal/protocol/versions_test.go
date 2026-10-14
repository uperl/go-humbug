package protocol

import (
	"bytes"
	"encoding/json"
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

// The 3.1 line is supported (3.1.0, 3.1.1, 3.1.2, ...). Any other version is rejected at
// initialize with a specific error, because validating it under 3.1 semantics would silently
// misreport.
func TestOnly31xIsSupported(t *testing.T) {
	for _, v := range []string{"3.1.0", "3.1.1", "3.1.2", "3.1.10"} {
		r := run(t, initPath(1, specWith(t, "openapi: "+v+"\n")))
		if res := result(t, r[0]); res["specification"].(obj)["openapi_version"] != v {
			t.Errorf("%s must be accepted: %v", v, res)
		}
	}
	for _, header := range []string{
		"openapi: 3.0.3\n", "openapi: 3.0.0\n", "openapi: 3.2.0\n", "openapi: 4.0.0\n",
		"openapi: '3.1'\n", "openapi: 3.10.0\n", "openapi: 3.1.0-rc1\n", "openapi: 3.1.01\n", "openapi: '3.1.x'\n",
		"openapi: '3.1.'\n", "openapi: '3.1.1.1'\n",
	} {
		r := run(t, initPath(1, specWith(t, header)))
		if code := errCode(t, r[0]); code != ErrSpecUnsupportedVer {
			t.Errorf("%s: code %s", strings.TrimSpace(header), code)
		}
		msg := r[0]["error"].(obj)["message"].(string)
		if !strings.Contains(msg, "3.1.x") || !strings.Contains(msg, "not supported") {
			t.Errorf("%s: the message must say what is supported: %q", strings.TrimSpace(header), msg)
		}
	}
}

// Patch releases are accepted on the strength of this test: every fixture is run against copies
// of the specification that declare each patch version, and the results must be identical to the
// 3.1.0 snapshots (testdata/golden). If a patch release ever behaves differently, this fails.
func TestPatchReleasesBehaveLikeThe310Snapshots(t *testing.T) {
	cases := loadFixtures(t)
	for _, version := range []string{"3.1.1", "3.1.2"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			entries, err := os.ReadDir("../../testdata/specs")
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				data, err := os.ReadFile(filepath.Join("../../testdata/specs", e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if e.Name() == "users.yaml" {
					if !bytes.Contains(data, []byte("openapi: 3.1.0")) {
						t.Fatal("fixture spec does not declare 3.1.0")
					}
					data = bytes.Replace(data, []byte("openapi: 3.1.0"), []byte("openapi: "+version), 1)
				}
				if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			lines := []string{initPath(1, filepath.Join(dir, "users.yaml"))}
			for i, c := range cases {
				req, res := c.messages()
				p := `"request":` + req
				if res != "" {
					p += `,"response":` + res
				}
				lines = append(lines, `{"id":`+itoa(i+2)+`,"type":"validate","params":{`+p+`}}`)
			}
			replies := run(t, lines...)
			if v := result(t, replies[0])["specification"].(obj)["openapi_version"]; v != version {
				t.Fatalf("version %v", v)
			}
			for i, c := range cases {
				got, _ := json.MarshalIndent(result(t, replies[i+1]), "", "  ")
				want, err := os.ReadFile(filepath.Join(goldenDir, c.ID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(bytes.TrimSpace(want), got) {
					t.Errorf("%s under %s differs from the 3.1.0 snapshot", c.ID, version)
				}
			}
		})
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
