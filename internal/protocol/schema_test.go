package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pb33f/jsonschema/v6"
)

const (
	schemaDir      = "../../schema"
	protocolSchema = "https://humbug.invalid/schema/protocol.schema.json"
	diagSchema     = "https://humbug.invalid/schema/diagnostic.schema.json"
)

var (
	schemaOnce sync.Once
	protoSch   *jsonschema.Schema
	diagSch    *jsonschema.Schema
	schemaErr  error
)

func loadSchemas() {
	c := jsonschema.NewCompiler()
	for _, f := range []struct{ file, url string }{
		{"protocol.schema.json", protocolSchema},
		{"diagnostic.schema.json", diagSchema},
	} {
		b, err := os.ReadFile(filepath.Join(schemaDir, f.file))
		if err != nil {
			schemaErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
		if err != nil {
			schemaErr = err
			return
		}
		if err := c.AddResource(f.url, doc); err != nil {
			schemaErr = err
			return
		}
	}
	if protoSch, schemaErr = c.Compile(protocolSchema); schemaErr != nil {
		return
	}
	diagSch, schemaErr = c.Compile(diagSchema)
}

func schemas(t *testing.T) (proto, diag *jsonschema.Schema) {
	t.Helper()
	schemaOnce.Do(loadSchemas)
	if schemaErr != nil {
		t.Fatalf("schemas: %v", schemaErr)
	}
	return protoSch, diagSch
}

// conforms fails the test if the JSON text does not satisfy sch.
func conforms(t *testing.T, sch *jsonschema.Schema, what, text string) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		t.Errorf("%s: not JSON: %v", what, err)
		return
	}
	if err := sch.Validate(inst); err != nil {
		t.Errorf("%s violates its schema: %v\n%s", what, err, text)
	}
}

// TestSchemaCommands checks the command shapes documented for clients, including
// rejection of malformed ones, so the schema and the implementation cannot drift.
func TestSchemaCommands(t *testing.T) {
	proto, _ := schemas(t)
	ok := []string{
		`{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"a.yaml"}}}`,
		`{"id":"x","type":"validate","params":{"request":{"method":"GET","url":"http://h/p","headers":[["A","b"],["A","c"]],"body_base64":""},"response":{"status":200},"validate":{"response":false}}}`,
		`{"id":3,"type":"shutdown"}`,
	}
	bad := []string{
		`{"id":1.5,"type":"shutdown"}`,
		`{"id":1,"type":"initialize","params":{"protocol_version":2,"spec":{"path":"a"}}}`,
		`{"id":1,"type":"validate","params":{"request":{"method":"GET"}}}`,
		`{"id":1,"type":"validate","params":{"request":{"method":"GET","url":"u","headers":[["only-one"]]}}}`,
		`{"id":1,"type":"validate","params":{"request":{"method":"GET","url":"u"},"typo":true}}`,
		`{"id":1,"type":"frobnicate"}`,
	}
	for _, s := range ok {
		conforms(t, proto, "command", s)
	}
	for _, s := range bad {
		inst, _ := jsonschema.UnmarshalJSON(strings.NewReader(s))
		if proto.Validate(inst) == nil {
			t.Errorf("schema accepted an invalid command: %s", s)
		}
	}
}
