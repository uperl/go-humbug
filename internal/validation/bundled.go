package validation

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/pb33f/libopenapi/index"

	"github.com/uperl/go-humbug/internal/offline"
)

// missingComponent is the library's message for a $ref it could not find.
var missingComponent = regexp.MustCompile("^component `([^`]+)` does not exist in the specification$")

// dropBundledRefErrors removes, from the errors of building the model, the ones the library
// reports for a $ref to a bundled schema, and returns the rest (nil if none are left).
//
// KNOWN LIBRARY LIMITATION (pb33f libopenapi v0.41.6): a $ref to a whole remote document whose
// URI has no file extension (https://spec.openapis.org/oas/3.1/schema-base/2022-10-07) is first
// looked up before that document is loaded, which fails and records "component ... does not
// exist". The library then loads the document and resolves the $ref through its $id, so the model
// is complete, but the stale error is still returned. Only bundled URIs are forgiven: anything
// else really was not found. TestLibraryStillReportsBundledRefsMissing fails once the library
// stops doing this.
func dropBundledRefErrors(err error) error {
	if err == nil {
		return nil
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	var kept []error
	for _, e := range errs {
		if !isBundledRefError(e) {
			kept = append(kept, e)
		}
	}
	return errors.Join(kept...)
}

func isBundledRefError(err error) bool {
	var ie *index.IndexingError
	if !errors.As(err, &ie) || ie.Err == nil {
		return false
	}
	m := missingComponent.FindStringSubmatch(ie.Err.Error())
	if m == nil {
		return false
	}
	_, ok := offline.Lookup(m[1])
	return ok
}

// fetchRemote is the library's RemoteURLHandler: offline.Fetch, except for how it refuses a URI
// with no file extension.
//
// KNOWN LIBRARY LIMITATION (pb33f libopenapi v0.41.6): before fetching a remote document whose
// URI has no file extension, the library fetches it once to sniff JSON or YAML, retrying a failed
// fetch three times with a one-second sleep between tries. Refusing such a URI with an error costs
// two seconds, and the bundled OAS dialect refers to one (https://json-schema.org/draft/2020-12/schema;
// the validator has the JSON Schema meta-schemas built in, and bundling them for the library breaks
// it, since it cannot resolve their relative refs). An empty body is neither JSON nor YAML, so
// answering it fails the sniff at once; nothing is fetched. A refused URI with an extension is not
// sniffed and gets the error, which is not retried. TestUnbundledRemoteRefsStillFail times this.
func fetchRemote(uri string) (*http.Response, error) {
	res, err := offline.Fetch(uri)
	if err == nil || hasDocumentExtension(uri) {
		return res, err
	}
	return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// hasDocumentExtension reports whether the library takes uri's type from its extension, the way
// RemoteFS does, rather than sniffing it.
func hasDocumentExtension(uri string) bool {
	u, err := url.Parse(uri)
	return err != nil || index.ExtractFileType(u.Path) != index.UNSUPPORTED
}
