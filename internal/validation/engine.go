// Package validation wraps pb33f/libopenapi-validator and converts its output
// into the Humbug diagnostic model. pb33f types never leave this package.
package validation

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/paths"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"humbug/internal/diagnostics"
)

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
	model *v3.Document
	Info  SpecInfo
}

// Load reads and fully resolves the spec at path. Local file references are
// resolved relative to the spec's directory; remote references are not fetched.
func Load(path string) (*Engine, error) {
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
	})
	if err != nil {
		return nil, &SpecError{err}
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, &SpecError{err}
	}
	if model == nil {
		return nil, &SpecError{errors.New("could not build OpenAPI model")}
	}
	v, verrs := validator.NewValidator(doc)
	if len(verrs) > 0 {
		return nil, &SpecError{errors.Join(verrs...)}
	}
	e := &Engine{v: v, model: &model.Model}
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

	req, err := p.Request.http()
	if err != nil {
		return nil, &InputError{err}
	}

	if pi, _, tmpl := paths.FindPath(req, e.model, nil); pi != nil && tmpl != "" {
		if id, ok := operationID(pi, req.Method); ok {
			res.Operation = &diagnostics.Operation{Method: req.Method, Path: tmpl, OperationID: id}
		}
	}

	if p.ValidateRequest {
		_, errs := e.v.ValidateHttpRequest(req)
		var body []byte
		if p.Request.Body != nil {
			body = *p.Request.Body
		}
		n := newNormalizer(diagnostics.SideRequest, body, p.Request.Body != nil, e.model)
		n.noteUninspected(contentType(p.Request.Headers))
		res.Request = n.result(errs)
	}

	if p.ValidateResponse && p.Response != nil {
		req2, _ := p.Request.http() // fresh request: validators may consume the body
		resp := p.Response.http(req2)
		_, errs := e.v.ValidateHttpResponse(req2, resp)
		var body []byte
		if p.Response.Body != nil {
			body = *p.Response.Body
		}
		n := newNormalizer(diagnostics.SideResponse, body, p.Response.Body != nil, e.model)
		n.noteUninspected(contentType(p.Response.Headers))
		res.Response = n.result(errs)
	}

	diagnostics.AssignIDs(res.Request, res.Response)
	res.Complete = res.Request.Complete && res.Response.Complete
	res.HasViolations = res.Request.HasViolations || res.Response.HasViolations
	res.Valid = res.Complete && !res.HasViolations
	return res, nil
}

func operationID(pi *v3.PathItem, method string) (string, bool) {
	ops := map[string]*v3.Operation{
		http.MethodGet: pi.Get, http.MethodPost: pi.Post, http.MethodPut: pi.Put,
		http.MethodDelete: pi.Delete, http.MethodPatch: pi.Patch, http.MethodHead: pi.Head,
		http.MethodOptions: pi.Options, http.MethodTrace: pi.Trace,
	}
	op := ops[method]
	if op == nil {
		return "", false
	}
	return op.OperationId, true
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
