package diagnostics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pinnedCodes is the set of fixed diagnostic codes. `schema.<keyword>` codes are open-ended (one per
// JSON Schema keyword) and are covered by the family row in the documentation.
var pinnedCodes = []string{
	"body.decode", "body.media_type", "body.required", "body.undeclared", "body.xml.namespace",
	"body.xml.prefix", "header.repeated", "operation.method_not_allowed", "operation.not_found",
	"parameter.invalid", "parameter.required", "response.header.invalid", "response.header.required",
	"response.status", "schema.anyOf", "schema.oneOf.ambiguous", "schema.oneOf.no_match",
	"validation.incomplete", "validation.unclassified",
}

// declaredCodes returns every Code* constant's value in diagnostics.go.
func declaredCodes(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "diagnostics.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(v, ".") {
					continue // a prefix for schema.<keyword> codes, not a code
				}
				codes = append(codes, v)
			}
		}
	}
	sort.Strings(codes)
	return codes
}

// TestCodesArePinnedAndDocumented makes removing or renaming a diagnostic code a conscious act. Codes
// are part of the contract (docs/versioning.md): a removal or rename is a breaking change, and an
// addition is not. The list below is the contract; the documentation must describe every code.
func TestCodesArePinnedAndDocumented(t *testing.T) {
	got := declaredCodes(t)
	// Adding a code: add it here and to docs/diagnostics.md. Removing or renaming one is breaking:
	// see docs/versioning.md before touching this list.
	want := pinnedCodes
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the diagnostic codes changed.\n got: %v\nwant: %v\n"+
			"A removed or renamed code is a breaking change (docs/versioning.md).", got, want)
	}
	doc, err := os.ReadFile("../../docs/diagnostics.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if !strings.Contains(string(doc), "`"+c+"`") && !documentedByPrefix(string(doc), c) {
			t.Errorf("code %q is not described in docs/diagnostics.md", c)
		}
	}
}

// documentedByPrefix accepts a family row such as `schema.<keyword>` for schema.* codes and
// `security.*` style rows.
func documentedByPrefix(doc, code string) bool {
	i := strings.LastIndex(code, ".")
	if i < 0 {
		return false
	}
	return strings.Contains(doc, "`"+code[:i]+".<") || strings.Contains(doc, "`"+code[:i]+".*`")
}
