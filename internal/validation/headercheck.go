package validation

import (
	"fmt"
	"strings"
)

// checkHeaders rejects headers that cannot exist in an HTTP message, as an input error rather
// than a contract finding: names must be RFC 9110 tokens, and values may not contain control
// characters other than tab (so no CR, LF or NUL). This is Humbug's behavior, not the protocol's:
// the wire carries headers verbatim, and another implementation may treat these differently.
// The count and size limits (package limits) are checked first, in the same way.
// Messages never repeat the offending content.
func (e *Engine) checkHeaders(side string, h []Header) error {
	if len(h) > e.lim.MaxHeaders {
		return &InputError{fmt.Errorf("%s.headers: %d entries exceeds the limit of %d", side, len(h), e.lim.MaxHeaders)}
	}
	total := 0
	for i, p := range h {
		if len(p[1]) > e.lim.MaxHeaderValueBytes {
			return &InputError{fmt.Errorf("%s.headers[%d]: value is %d bytes, over the limit of %d", side, i, len(p[1]), e.lim.MaxHeaderValueBytes)}
		}
		if total += len(p[0]) + len(p[1]); total > e.lim.MaxHeaderBytes {
			return &InputError{fmt.Errorf("%s.headers: names and values total more than the limit of %d bytes", side, e.lim.MaxHeaderBytes)}
		}
		if !validHeaderName(p[0]) {
			return &InputError{fmt.Errorf("%s.headers[%d]: header name is not a valid HTTP token", side, i)}
		}
		if !validHeaderValue(p[1]) {
			return &InputError{fmt.Errorf("%s.headers[%d]: header value contains a control character", side, i)}
		}
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
