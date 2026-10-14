package validation

import "strconv"

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
