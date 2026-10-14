// Package validation wraps pb33f/libopenapi-validator and converts its output
// into the Humbug diagnostic model. pb33f types never leave this package.
package validation

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/config"
	"github.com/pb33f/libopenapi-validator/radix"
	"github.com/pb33f/libopenapi-validator/router"
	"github.com/pb33f/libopenapi-validator/schema_validation"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/uperl/go-humbug/internal/diagnostics"
	"github.com/uperl/go-humbug/internal/limits"
)

// logger sends library diagnostics to stderr; stdout carries protocol traffic only.
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// SupportedVersions describes the OpenAPI versions Humbug supports: the 3.1 line (3.1.0, 3.1.1,
// 3.1.2, ...). Fixtures are written against 3.1.0, and a test runs every one of them against
// patch-release copies of the specification to show the results are identical. Documents
// declaring any other version are rejected at load: validating them under 3.1 semantics would
// silently misreport (3.0 differs in nullable, exclusiveMinimum and more). Extend this only
// together with fixtures for the new version.
var SupportedVersions = []string{"3.1.x"}

// supportedVersionPattern matches 3.1.<patch>. Pre-release suffixes (3.1.0-rc1) and versions
// without a patch number (3.1) are not OpenAPI release versions and do not match.
var supportedVersionPattern = regexp.MustCompile(`^3\.1\.(0|[1-9][0-9]*)$`)

// UnsupportedVersionError is returned by Load for a document whose `openapi` version is not in
// SupportedVersions.
type UnsupportedVersionError struct{ Version string }

func (e *UnsupportedVersionError) Error() string {
	v := e.Version
	if len(v) > 40 {
		v = v[:40] + "..."
	}
	return fmt.Sprintf("OpenAPI version %q is not supported (supported: %s)", v, strings.Join(SupportedVersions, ", "))
}

func versionSupported(v string) bool { return supportedVersionPattern.MatchString(v) }

// SpecError is returned by Load when the specification cannot be used.
type SpecError struct{ Err error }

func (e *SpecError) Error() string { return e.Err.Error() }
func (e *SpecError) Unwrap() error { return e.Err }

type SpecInfo struct {
	OpenAPIVersion string
	Title          string
}

type Engine struct {
	v     validator.Validator
	sv    schema_validation.SchemaValidator
	rt    router.Router // routes exactly as the validator does; see newRouter
	model *v3.Document
	lim   limits.Limits
	Info  SpecInfo
}

// Load reads and fully resolves the spec at path. Local file references are
// resolved relative to the spec's directory; remote references are not fetched.
func Load(path string) (*Engine, error) { return LoadWithLimits(path, limits.Default()) }

// LoadWithLimits is Load with an explicit resource policy (see package limits).
func LoadWithLimits(path string, lim limits.Limits) (*Engine, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &SpecError{err}
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, &SpecError{err}
	}
	doc, err := libopenapi.NewDocumentWithConfiguration(raw, &datamodel.DocumentConfiguration{
		BasePath:              abs,
		AllowFileReferences:   true,
		AllowRemoteReferences: false,
		// The library's default logger writes JSON to stdout, which is the protocol stream.
		Logger: logger,
	})
	if err != nil {
		return nil, &SpecError{err}
	}
	if v := doc.GetSpecInfo().Version; !versionSupported(v) {
		return nil, &UnsupportedVersionError{Version: v}
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, &SpecError{err}
	}
	if model == nil {
		return nil, &SpecError{errors.New("could not build OpenAPI model")}
	}
	v, verrs := validator.NewValidator(doc, config.WithLogger(logger), config.WithXmlBodyValidation(), config.WithURLEncodedBodyValidation())
	if len(verrs) > 0 {
		return nil, &SpecError{errors.Join(verrs...)}
	}
	e := &Engine{v: v, sv: schema_validation.NewSchemaValidatorWithLogger(logger), rt: newRouter(&model.Model), model: &model.Model, lim: lim}
	e.Info.OpenAPIVersion = doc.GetSpecInfo().Version
	if model.Model.Info != nil {
		e.Info.Title = model.Model.Info.Title
	}
	return e, nil
}

// Header is one ordered name/value pair; duplicates and case are preserved.
type Header [2]string

type Request struct {
	Method  string
	URL     string
	Headers []Header
	Body    *[]byte // nil = absent
}

type Response struct {
	Status  int
	Headers []Header
	Body    *[]byte
}

type Params struct {
	Request  Request
	Response *Response
	// ValidateRequest / ValidateResponse are already defaulted by the caller.
	ValidateRequest  bool
	ValidateResponse bool
}

func (r Request) http() (*http.Request, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(*r.Body)
	}
	req, err := http.NewRequest(r.Method, r.URL, body)
	if err != nil {
		return nil, err
	}
	for _, h := range r.Headers {
		req.Header.Add(h[0], h[1])
	}
	return req, nil
}

func (r Response) http(req *http.Request) *http.Response {
	var body io.Reader = http.NoBody
	if r.Body != nil {
		body = bytes.NewReader(*r.Body)
	}
	res := &http.Response{
		StatusCode: r.Status, Status: fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status)),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(body), Request: req,
	}
	for _, h := range r.Headers {
		res.Header.Add(h[0], h[1])
	}
	return res
}

// InputError reports caller input that cannot be turned into an HTTP message
// (e.g. an unparsable URL); the protocol layer maps it to validator.unsupported_input.
type InputError struct{ Err error }

func (e *InputError) Error() string { return e.Err.Error() }
func (e *InputError) Unwrap() error { return e.Err }

// Validate runs the requested checks and returns one transaction result.
func (e *Engine) Validate(p Params) (*diagnostics.Result, error) {
	res := &diagnostics.Result{Request: diagnostics.Skipped(), Response: diagnostics.Skipped()}

	if err := e.checkHeaders("request", p.Request.Headers); err != nil {
		return nil, err
	}
	if p.ValidateResponse && p.Response != nil {
		if err := e.checkHeaders("response", p.Response.Headers); err != nil {
			return nil, err
		}
	}

	// Bodies are the already-decoded HTTP bodies; Content-Encoding headers are preserved and passed
	// through but never acted on, since Humbug does not decompress anything.

	// Routing, and response validation, need only the request line and headers, never its body.
	routeMsg := p.Request
	routeMsg.Body = nil
	req, err := routeMsg.http()
	if err != nil {
		return nil, &InputError{err}
	}

	var op *v3.Operation
	var pi *v3.PathItem
	if route, _ := e.rt.FindRoute(req); route != nil {
		// A method mismatch returns a partial route with a nil Operation.
		pi, op = route.PathItem, route.Operation
		if op != nil {
			res.Operation = &diagnostics.Operation{Method: req.Method, Path: route.Path, OperationID: op.OperationId}
		}
	}

	// A body the library must not see (over a limit, or XML it cannot unwrap) is never handed to
	// it: its side is reported incomplete instead (see skipReason).
	reqSkip := e.skipReason(op, true, 0, p.Request.Body, p.Request.Headers)
	var resSkip *bodySkip
	if p.ValidateResponse && p.Response != nil {
		resSkip = e.skipReason(op, false, p.Response.Status, p.Response.Body, p.Response.Headers)
	}

	if p.ValidateRequest {
		msg := p.Request
		if reqSkip != nil {
			msg.Body = nil
		}
		vreq, err := msg.http()
		if err != nil {
			return nil, &InputError{err}
		}
		_, errs := e.v.ValidateHttpRequest(vreq)
		n := newNormalizer(diagnostics.SideRequest, bodyBytes(msg.Body), msg.Body != nil, e.model)
		n.skipped = reqSkip
		n.mediaType = mediaTypeOf(p.Request.Headers)
		if op != nil {
			n.decls = requestHeaderDecls(pi, op)
			errs = e.emptyHeaderParams(n, p.Request.Headers, errs)
			n.warnRepeated(p.Request.Headers)
		}
		_, n.sentCT = firstLine(p.Request.Headers, "content-type")
		e.warnUndeclaredBody(n, op, 0, p.Request.Headers, bodyBytes(msg.Body))
		e.inspectNonJSON(n, op, 0, p.Request.Headers, bodyBytes(msg.Body))
		res.Request = n.result(errs)
	}

	if p.ValidateResponse && p.Response != nil {
		msg := *p.Response
		if resSkip != nil {
			msg.Body = nil
		}
		req2, _ := routeMsg.http() // a fresh request: validators may consume it
		resp := msg.http(req2)
		_, errs := e.v.ValidateHttpResponse(req2, resp)
		n := newNormalizer(diagnostics.SideResponse, bodyBytes(msg.Body), msg.Body != nil, e.model)
		n.skipped = resSkip
		n.mediaType = mediaTypeOf(p.Response.Headers)
		if op != nil {
			n.decls = responseHeaderDecls(op, p.Response.Status)
			n.warnRepeated(p.Response.Headers)
		}
		_, n.sentCT = firstLine(p.Response.Headers, "content-type")
		e.warnUndeclaredBody(n, op, p.Response.Status, p.Response.Headers, bodyBytes(msg.Body))
		e.inspectNonJSON(n, op, p.Response.Status, p.Response.Headers, bodyBytes(msg.Body))
		res.Response = n.result(errs)
	}

	diagnostics.Relate(sideOrNil(res.Request), sideOrNil(res.Response))
	diagnostics.AssignIDs(res.Request, res.Response)
	res.Complete = res.Request.Complete && res.Response.Complete
	res.HasViolations = res.Request.HasViolations || res.Response.HasViolations
	res.Valid = res.Complete && !res.HasViolations
	return res, nil
}

func bodyBytes(b *[]byte) []byte {
	if b == nil {
		return nil
	}
	return *b
}

// contentType returns the first Content-Type value (case-insensitive), or "".
func contentType(h []Header) string {
	for _, p := range h {
		if strings.EqualFold(p[0], "content-type") {
			return p[1]
		}
	}
	return ""
}

// sideOrNil hides skipped sides from relationship rules.
func sideOrNil(s *diagnostics.Side) *diagnostics.Side {
	if s == nil || s.Status == diagnostics.StatusSkipped {
		return nil
	}
	return s
}

// newRouter builds the router the validator itself uses (see validator.NewValidatorFromV3Model:
// a radix path tree, and path-only matching because strict server matching is off). The library
// does not expose its router, and routing the request a second way (paths.FindPath with no
// options) disagrees with it: it rejects `/users/` or `//users`, which the validator routes to
// `/users`, so a validated request would be reported with no operation. TestRoutingMatchesTheValidator
// guards against the two drifting apart.
func newRouter(model *v3.Document) router.Router {
	return router.NewRouter(model, router.WithPathLookup(radix.BuildPathTree(model)), router.WithPathOnlyMatching())
}
