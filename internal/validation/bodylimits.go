package validation

import (
	"mime"
	"strconv"
	"strings"

	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// bodySkip says why a body is not inspected.
type bodySkip struct {
	reason string            // the validation.incomplete reason
	detail map[string]string // the limit and, for size, the measured size (never content)
}

// bodyLimit returns why a body must not be handed to the library, or nil when it may be. The
// checks run before the work they guard: the library's memory use is a multiple of the body size
// (measured at about 11x for a flat document and about 70x for a structured one).
func (e *Engine) bodyLimit(body *[]byte, headers []Header) *bodySkip {
	if body == nil {
		return nil
	}
	if len(*body) > e.lim.MaxBodyBytes {
		return &bodySkip{"body.too_large", map[string]string{
			"limit_bytes": strconv.Itoa(e.lim.MaxBodyBytes), "body_bytes": strconv.Itoa(len(*body))}}
	}
	if isJSON(contentType(headers)) && exceedsDepth(*body, e.lim.MaxNestingDepth) {
		return &bodySkip{"body.too_deep", map[string]string{"limit_depth": strconv.Itoa(e.lim.MaxNestingDepth)}}
	}
	return nil
}

// exceedsDepth reports whether JSON text nests objects and arrays deeper than max. It scans once,
// without parsing: brackets inside strings (with escapes) are ignored, and it stops at the first
// excess. Malformed input is fine; it only has to be cheap and never wrong about real nesting.
func exceedsDepth(b []byte, max int) bool {
	depth := 0
	inString, escaped := false, false
	for _, c := range b {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			if depth++; depth > max {
				return true
			}
		case ']', '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return false
}

// skipReason combines every reason a body must not be handed to the library: the resource limits,
// and XML it cannot unwrap.
func (e *Engine) skipReason(op *v3.Operation, request bool, status int, body *[]byte, headers []Header) *bodySkip {
	if s := e.bodyLimit(body, headers); s != nil {
		return s
	}
	return xmlRootSkip(op, request, status, body, headers)
}

// xmlRootSkip handles XML bodies whose root schema does not declare xml.name. The library turns an
// XML document into JSON by wrapping it in its root element (`<user>...</user>` becomes
// {"user": {...}}) and removes that wrapper only when the schema's xml.name names the root. Without
// xml.name the document stays wrapped and every body fails (`required` first), so valid XML would
// be reported as a violation. Humbug reports the body as not inspected instead; declaring
// `xml: {name: <root element>}` on the root schema makes it inspectable.
func xmlRootSkip(op *v3.Operation, request bool, status int, body *[]byte, headers []Header) *bodySkip {
	if body == nil || op == nil {
		return nil
	}
	mt := mediaTypeOf(headers)
	if mt != "application/xml" && mt != "text/xml" {
		return nil
	}
	media := matchMedia(contentFor(op, request, status), mt)
	if media == nil || media.Schema == nil {
		return nil // not declared (the library reports it) or declared without a schema
	}
	sch := media.Schema.Schema()
	if sch == nil || (sch.XML != nil && sch.XML.Name != "") {
		return nil
	}
	return &bodySkip{"body.xml_root_unnamed", map[string]string{"media_type": mt}}
}

// mediaTypeOf returns the lower-cased media type of the Content-Type header, without parameters,
// or "" when there is none or it is unusable.
func mediaTypeOf(headers []Header) string {
	mt, _, err := mime.ParseMediaType(contentType(headers))
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}
