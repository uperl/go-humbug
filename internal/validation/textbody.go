package validation

import (
	"fmt"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	d "github.com/uperl/go-humbug/internal/diagnostics"
)

// Non-JSON bodies are validated by the shape of the schema declared for their media type, not
// by the media type's name:
//
//   - no schema declared: any content is allowed; nothing to check
//   - string schema (type string, or untyped and unstructured): the body is a string and the
//     string keywords apply. UTF-8 text by default; `format: binary` means raw bytes.
//   - anything structured (object/array, composition) under a non-JSON type, e.g. form, XML or
//     multipart: not parsed by Humbug, so the side stays incomplete.
//
// Humbug never checks that content is valid for its format (VCL, CSV, ...). A schema cannot say
// that, so "valid" means "conforms to the OpenAPI contract".

// operationFor returns the operation for method on a path item, or nil.
func operationFor(pi *v3.PathItem, method string) *v3.Operation {
	return map[string]*v3.Operation{
		http.MethodGet: pi.Get, http.MethodPost: pi.Post, http.MethodPut: pi.Put,
		http.MethodDelete: pi.Delete, http.MethodPatch: pi.Patch, http.MethodHead: pi.Head,
		http.MethodOptions: pi.Options, http.MethodTrace: pi.Trace,
	}[method]
}

// inspectNonJSON validates (or marks incomplete) a present, non-JSON body. op is nil when the
// operation could not be matched, in which case routing findings already cover the side.
func (e *Engine) inspectNonJSON(n *normalizer, op *v3.Operation, status int, headers []Header, body []byte) {
	ct := contentType(headers)
	if !n.hasBody || isJSON(ct) || op == nil {
		return
	}
	content := contentFor(op, n.who == d.SideRequest, status)
	if content == nil || content.Len() == 0 {
		return // the operation declares no content for this message: there is nothing to check it against
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		if len(body) > 0 {
			n.noteUninspected(ct) // content is declared but there is no usable Content-Type
		}
		return // an empty body has nothing to inspect (with a Content-Type it is validated below)
	}
	media := matchMedia(content, mt)
	if media == nil {
		return // not declared: the library reports body.media_type
	}
	if media.Schema == nil {
		return // declared without a schema: any content conforms
	}
	sch := media.Schema.Schema()
	if sch == nil || !stringLike(sch) {
		n.noteUninspected(ct)
		return
	}

	binary := sch.Format == "binary"
	var payload string
	switch {
	case binary:
		// Raw bytes: one rune per byte, so length keywords count bytes.
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		payload = string(runes)
	default:
		if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "us-ascii" {
			n.incomplete("body.charset_unsupported", map[string]string{"charset": cs},
				"body charset is not UTF-8 and was not inspected")
			return
		}
		if !utf8.Valid(body) {
			off := firstInvalidUTF8(body)
			n.add(d.Diagnostic{Code: d.CodeBodyDecode, Instance: &d.Instance{Location: "body"},
				Detail:  map[string]string{"utf8_offset": fmt.Sprint(off)},
				Message: "body is not valid UTF-8"})
			return
		}
		payload = string(body)
	}

	n.noSpans = true // no JSON structure to point into
	_, errs := e.sv.ValidateSchemaObject(sch, payload)
	for _, ve := range errs {
		if len(ve.SchemaValidationErrors) == 0 {
			n.unclassified(ve)
			continue
		}
		n.schemaFailures(ve)
	}
}

func firstInvalidUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return len(b)
}

func contentFor(op *v3.Operation, request bool, status int) *orderedmap.Map[string, *v3.MediaType] {
	if request {
		if op.RequestBody == nil {
			return nil
		}
		return op.RequestBody.Content
	}
	resp := responseFor(op, status)
	if resp == nil {
		return nil
	}
	return resp.Content
}

// responseFor finds the response definition for a status: exact code, then NXX, then default.
func responseFor(op *v3.Operation, status int) *v3.Response {
	if op == nil || op.Responses == nil {
		return nil
	}
	var resp *v3.Response
	if op.Responses.Codes != nil {
		if r, ok := op.Responses.Codes.Get(fmt.Sprint(status)); ok {
			resp = r
		} else {
			wild := fmt.Sprintf("%dXX", status/100)
			for k, r := range op.Responses.Codes.FromOldest() {
				if strings.EqualFold(k, wild) {
					resp = r
					break
				}
			}
		}
	}
	if resp == nil {
		resp = op.Responses.Default
	}
	return resp
}

// matchMedia finds the content entry for a media type: exact, then type/*, then */*.
func matchMedia(content *orderedmap.Map[string, *v3.MediaType], mt string) *v3.MediaType {
	if content == nil {
		return nil
	}
	typ := mt
	if i := strings.IndexByte(mt, '/'); i >= 0 {
		typ = mt[:i]
	}
	var exact, typeWild, anyWild *v3.MediaType
	for k, m := range content.FromOldest() {
		key, _, err := mime.ParseMediaType(k)
		if err != nil {
			key = strings.ToLower(strings.TrimSpace(k))
		}
		switch key {
		case mt:
			exact = m
		case typ + "/*":
			typeWild = m
		case "*/*":
			anyWild = m
		}
	}
	for _, m := range []*v3.MediaType{exact, typeWild, anyWild} {
		if m != nil {
			return m
		}
	}
	return nil
}

// stringLike reports whether a schema describes a plain string (or places no structural
// requirement on the value), so a non-JSON body can be checked as a string.
func stringLike(s *base.Schema) bool {
	for _, t := range s.Type {
		if t == "string" {
			return true
		}
	}
	if len(s.Type) > 0 {
		return false
	}
	structured := len(s.AllOf)+len(s.OneOf)+len(s.AnyOf) > 0 || s.Not != nil || s.Items != nil ||
		(s.Properties != nil && s.Properties.Len() > 0) || len(s.PrefixItems) > 0
	return !structured
}

// warnUndeclaredBody reports a body sent where the matched operation declares no content for it:
// a request with no requestBody, or a response whose definition has no content (a 204 carrying a
// body, say). It is a warning, not a violation. Nothing is said when the operation could not be
// matched, when the response status is not defined at all (that is its own finding), or when the
// body is absent or empty.
func (e *Engine) warnUndeclaredBody(n *normalizer, op *v3.Operation, status int, headers []Header, body []byte) {
	if op == nil || len(body) == 0 {
		return
	}
	var known, declared bool
	if n.who == d.SideRequest {
		known = true
		declared = op.RequestBody != nil && op.RequestBody.Content != nil && op.RequestBody.Content.Len() > 0
	} else {
		resp := responseFor(op, status)
		known = resp != nil
		declared = resp != nil && resp.Content != nil && resp.Content.Len() > 0
	}
	if !known || declared {
		return
	}
	detail := map[string]string{}
	if mt, _, err := mime.ParseMediaType(contentType(headers)); err == nil {
		detail["media_type"] = mt
	}
	n.add(d.Diagnostic{Code: d.CodeBodyUndeclared, Severity: d.SeverityWarning, Detail: detail,
		Instance: &d.Instance{Location: "body"},
		Message:  "a body was sent but the operation declares no content for it"})
}
