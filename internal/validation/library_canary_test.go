package validation

import (
	"os"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
)

// CANARIES. Humbug works around some behaviors of the pinned validation library. These tests call
// the library directly and fail when it stops behaving that way, which is the cue to remove the
// workaround and update docs/protocol.md ("Known library limitations").

func canaryEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Load("../../testdata/specs/bodies.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func bytesPtr(s string) *[]byte { b := []byte(s); return &b }

// The library applies its allowReserved check to the decoded value, so it flags a correctly
// percent-encoded reserved character (an email address, a%40b.com). Humbug drops that finding
// (normalize.go, case reservedValues) so ordinary forms are not reported as violations.
func TestLibraryStillFlagsEncodedReservedCharacters(t *testing.T) {
	e := canaryEngine(t)
	req := Request{Method: "POST", URL: "http://example.test/form/user",
		Headers: []Header{{"Content-Type", "application/x-www-form-urlencoded"}}, Body: bytesPtr("name=a%40b.com")}
	httpReq, err := req.http()
	if err != nil {
		t.Fatal(err)
	}
	_, errs := e.v.ValidateHttpRequest(httpReq)
	flagged := false
	for _, ve := range errs {
		if ve.ValidationType == "urlEncodedValidation" && ve.ValidationSubType == "reservedValues" {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("the library no longer flags a correctly encoded reserved character (%d errors). It may be fixed: "+
			"remove the dropped `reservedValues` case in normalize.go and update docs/protocol.md.", len(errs))
	}
	// Humbug's own answer for the same request is that it is valid.
	res, err := e.Validate(Params{Request: req, ValidateRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || len(res.Request.Diagnostics) != 0 {
		t.Errorf("Humbug must not report a correctly encoded form value: %+v", res.Request.Diagnostics)
	}
}

// The library wraps an XML document in its root element and unwraps it only when the schema declares
// xml.name, so valid XML against a schema without it fails. Humbug does not hand such bodies to the
// library (bodylimits.go, xmlRootSkip) and reports them incomplete instead.
func TestLibraryStillNeedsAnXMLRootName(t *testing.T) {
	e := canaryEngine(t)
	req := Request{Method: "POST", URL: "http://example.test/xml/unnamed",
		Headers: []Header{{"Content-Type", "application/xml"}}, Body: bytesPtr("<user><name>Ann</name></user>")}
	httpReq, err := req.http()
	if err != nil {
		t.Fatal(err)
	}
	if ok, errs := e.v.ValidateHttpRequest(httpReq); ok || len(errs) == 0 {
		t.Fatal("the library now accepts valid XML whose schema has no xml.name. It may be fixed: " +
			"remove xmlRootSkip (bodylimits.go) and update docs/protocol.md.")
	}
	res, err := e.Validate(Params{Request: req, ValidateRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || res.Request.Status != "incomplete" || len(res.Request.Diagnostics) != 1 {
		t.Errorf("Humbug must report it as not inspected, not as a violation: %+v", res.Request)
	}
}

// The library reports a $ref to an extensionless remote document as missing even though it then
// resolves it. Humbug drops that error for bundled URIs (bundled.go, dropBundledRefErrors).
func TestLibraryStillReportsBundledRefsMissing(t *testing.T) {
	raw, err := os.ReadFile(oasSchemaSpec)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocumentWithConfiguration(raw, &datamodel.DocumentConfiguration{
		AllowRemoteReferences:                 true,
		RemoteURLHandler:                      fetchRemote,
		AllowUnknownExtensionContentDetection: true,
		Logger:                                logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err == nil {
		t.Fatal("the library no longer reports the bundled $ref as missing. It may be fixed: remove " +
			"dropBundledRefErrors in bundled.go and update docs/protocol.md.")
	}
	if dropBundledRefErrors(err) != nil {
		t.Fatalf("the library reports something else for the bundled $ref; check dropBundledRefErrors: %v", err)
	}
	if model == nil {
		t.Fatal("no model built")
	}
}
