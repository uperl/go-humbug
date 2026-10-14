package protocol

import "testing"

// initialize says which build answered, with the exact versions of the libraries that decide which
// findings are reported (docs/versioning.md). The block is informational: the schema test validates
// its shape, and this one checks it is filled in.
func TestInitializeReportsTheImplementation(t *testing.T) {
	rs := run(t, initPath(1, "../../testdata/specs/users.yaml"))
	impl, ok := result(t, rs[0])["implementation"].(obj)
	if !ok {
		t.Fatalf("no implementation: %v", rs[0])
	}
	if impl["name"] != "humbug" {
		t.Errorf("name: %v", impl["name"])
	}
	if v, _ := impl["version"].(string); v == "" {
		t.Errorf("version: %v", impl["version"])
	}
	libs, _ := impl["libraries"].(obj)
	for _, k := range []string{"libopenapi", "libopenapi_validator"} {
		if v, _ := libs[k].(string); v == "" || v == "unknown" {
			t.Errorf("library %s: %v", k, libs[k])
		}
	}
}
