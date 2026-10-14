package validation

import (
	"fmt"
	"strings"
)

// checkHeaders rejects headers that cannot exist in an HTTP message, as an input error rather
// than a contract finding: names must be RFC 9110 tokens, and values may not contain control
// characters other than tab (so no CR, LF or NUL). This is Humbug's behavior, not the protocol's:
// the wire carries headers verbatim, and another implementation may treat these differently.
// Messages never repeat the offending content.
func checkHeaders(side string, h []Header) error {
	for i, p := range h {
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
