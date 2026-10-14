// Package sourcemap indexes a JSON document so RFC 6901 pointers can be mapped
// to byte spans in the original bytes. Offsets are zero-based, start inclusive,
// end exclusive; lines and columns are one-based and columns count UTF-8 bytes.
package sourcemap

import (
	"fmt"
	"sort"
	"strings"
)

type Span struct {
	Start, End         int
	Line, Column       int
	EndLine, EndColumn int
}

type entry struct {
	span      Span
	ambiguous bool
}

// Index maps JSON pointers to the span of the value they address.
type Index struct {
	src       []byte
	lineStart []int
	values    map[string]entry
	keys      map[string]Span // span of the member name token, by pointer of its value
}

// ParseError carries parser-provided location for malformed JSON.
type ParseError struct {
	Msg          string
	Offset       int
	Line, Column int
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s at line %d column %d (offset %d)", e.Msg, e.Line, e.Column, e.Offset)
}

// Build parses body and returns its index.
func Build(body []byte) (*Index, error) {
	ix := &Index{src: body, values: map[string]entry{}, keys: map[string]Span{}}
	ix.lineStart = []int{0}
	for i, c := range body {
		if c == '\n' {
			ix.lineStart = append(ix.lineStart, i+1)
		}
	}
	p := &parser{ix: ix, b: body}
	p.ws()
	if p.i >= len(body) {
		return nil, p.err("empty document")
	}
	if err := p.value(""); err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(body) {
		return nil, p.err("unexpected data after top-level value")
	}
	return ix, nil
}

// Lookup returns the span for pointer. ambiguous is true when duplicate member
// names make the mapping uncertain (the last occurrence is reported, matching
// encoding/json).
func (ix *Index) Lookup(pointer string) (s Span, ambiguous, ok bool) {
	e, ok := ix.values[pointer]
	return e.span, e.ambiguous, ok
}

// Nearest returns the span of the longest existing ancestor of pointer and
// whether that ancestor is the pointer itself.
func (ix *Index) Nearest(pointer string) (s Span, ambiguous, exact, ok bool) {
	for p := pointer; ; {
		if e, found := ix.values[p]; found {
			return e.span, e.ambiguous, p == pointer, true
		}
		if p == "" {
			return Span{}, false, false, false
		}
		p = p[:strings.LastIndexByte(p, '/')]
	}
}

// Part says which token of the document a span covers.
const (
	PartKey   = "key"   // the member name, quotes included
	PartValue = "value" // the value (the root, an array element, or any non-member)
)

// Target returns the span to highlight for pointer: the member name when the pointer addresses an
// object member, otherwise the value (the root and array elements have no name). When the pointer
// does not exist it walks up to the closest existing ancestor and reports exact as false.
// ambiguous is true when duplicate member names make the mapping uncertain (the last occurrence
// is reported, matching encoding/json).
func (ix *Index) Target(pointer string) (s Span, part string, ambiguous, exact, ok bool) {
	for p := pointer; ; {
		if e, found := ix.values[p]; found {
			if k, isMember := ix.keys[p]; isMember {
				return k, PartKey, e.ambiguous, p == pointer, true
			}
			return e.span, PartValue, e.ambiguous, p == pointer, true
		}
		if p == "" {
			return Span{}, "", false, false, false
		}
		p = p[:strings.LastIndexByte(p, '/')]
	}
}

// Escape encodes one reference token per RFC 6901.
func Escape(tok string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(tok)
}

func (ix *Index) span(start, end int) Span {
	l, c := ix.pos(start)
	el, ec := ix.pos(end)
	return Span{Start: start, End: end, Line: l, Column: c, EndLine: el, EndColumn: ec}
}

func (ix *Index) pos(off int) (line, col int) {
	n := sort.Search(len(ix.lineStart), func(i int) bool { return ix.lineStart[i] > off })
	return n, off - ix.lineStart[n-1] + 1
}

type parser struct {
	ix *Index
	b  []byte
	i  int
}

func (p *parser) err(msg string) error {
	l, c := p.ix.pos(min(p.i, len(p.b)))
	return &ParseError{Msg: msg, Offset: p.i, Line: l, Column: c}
}

func (p *parser) ws() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) record(ptr string, start int) {
	e := entry{span: p.ix.span(start, p.i)}
	if _, dup := p.ix.values[ptr]; dup {
		e.ambiguous = true
	}
	p.ix.values[ptr] = e
}

func (p *parser) value(ptr string) error {
	if p.i >= len(p.b) {
		return p.err("unexpected end of input")
	}
	start := p.i
	switch c := p.b[p.i]; {
	case c == '{':
		if err := p.object(ptr); err != nil {
			return err
		}
	case c == '[':
		if err := p.array(ptr); err != nil {
			return err
		}
	case c == '"':
		if _, err := p.str(); err != nil {
			return err
		}
	case c == '-' || (c >= '0' && c <= '9'):
		if err := p.number(); err != nil {
			return err
		}
	default:
		if !p.literal() {
			return p.err(fmt.Sprintf("invalid character %q looking for beginning of value", c))
		}
	}
	p.record(ptr, start)
	return nil
}

func (p *parser) object(ptr string) error {
	p.i++ // {
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return nil
	}
	seen := map[string]bool{}
	for {
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return p.err("expected object key string")
		}
		ks := p.i
		key, err := p.str()
		if err != nil {
			return err
		}
		ke := p.i
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return p.err("expected ':' after object key")
		}
		p.i++
		p.ws()
		child := ptr + "/" + Escape(key)
		if seen[key] {
			// reset so the later occurrence's subtree replaces the earlier one, flagged ambiguous
			p.dropSubtree(child)
			p.ix.values[child] = entry{ambiguous: true}
		}
		seen[key] = true
		p.ix.keys[child] = p.ix.span(ks, ke)
		wasAmbig := p.ix.values[child].ambiguous
		if err := p.value(child); err != nil {
			return err
		}
		if wasAmbig {
			e := p.ix.values[child]
			e.ambiguous = true
			p.ix.values[child] = e
		}
		p.ws()
		if p.i >= len(p.b) {
			return p.err("unexpected end of input in object")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return nil
		default:
			return p.err("expected ',' or '}' after object member")
		}
	}
}

func (p *parser) dropSubtree(ptr string) {
	for k := range p.ix.values {
		if k == ptr || strings.HasPrefix(k, ptr+"/") {
			delete(p.ix.values, k)
		}
	}
}

func (p *parser) array(ptr string) error {
	p.i++ // [
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return nil
	}
	for n := 0; ; n++ {
		p.ws()
		if err := p.value(fmt.Sprintf("%s/%d", ptr, n)); err != nil {
			return err
		}
		p.ws()
		if p.i >= len(p.b) {
			return p.err("unexpected end of input in array")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return nil
		default:
			return p.err("expected ',' or ']' after array element")
		}
	}
}

// str scans a string token and returns its decoded content.
func (p *parser) str() (string, error) {
	p.i++ // opening quote
	var sb strings.Builder
	for p.i < len(p.b) {
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return sb.String(), nil
		case c < 0x20:
			return "", p.err("invalid control character in string")
		case c == '\\':
			p.i++
			if p.i >= len(p.b) {
				return "", p.err("unterminated escape")
			}
			switch e := p.b[p.i]; e {
			case '"', '\\', '/':
				sb.WriteByte(e)
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, n, err := p.unicode()
				if err != nil {
					return "", err
				}
				sb.WriteRune(r)
				p.i += n
				continue
			default:
				return "", p.err("invalid escape character")
			}
			p.i++
		default:
			sb.WriteByte(c)
			p.i++
		}
	}
	return "", p.err("unterminated string")
}

// unicode decodes \uXXXX (with surrogate pairs); p.i is on the 'u'. It returns
// the rune and the number of bytes to advance from the 'u'.
func (p *parser) unicode() (rune, int, error) {
	hex := func(at int) (rune, bool) {
		if at+4 > len(p.b) {
			return 0, false
		}
		var r rune
		for _, c := range p.b[at : at+4] {
			r <<= 4
			switch {
			case c >= '0' && c <= '9':
				r |= rune(c - '0')
			case c >= 'a' && c <= 'f':
				r |= rune(c-'a') + 10
			case c >= 'A' && c <= 'F':
				r |= rune(c-'A') + 10
			default:
				return 0, false
			}
		}
		return r, true
	}
	r, ok := hex(p.i + 1)
	if !ok {
		return 0, 0, p.err("invalid \\u escape")
	}
	n := 5
	if r >= 0xD800 && r < 0xDC00 && p.i+6 < len(p.b) && p.b[p.i+5] == '\\' && p.b[p.i+6] == 'u' {
		if lo, ok := hex(p.i + 7); ok && lo >= 0xDC00 && lo < 0xE000 {
			r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
			n = 11
		}
	}
	return r, n, nil
}

func (p *parser) number() error {
	s := p.i
	if p.b[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.i < len(p.b) && p.b[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		p.i = s
		return p.err("invalid number")
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return p.err("invalid number fraction")
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return p.err("invalid number exponent")
		}
	}
	return nil
}

func (p *parser) literal() bool {
	for _, lit := range []string{"true", "false", "null"} {
		if strings.HasPrefix(string(p.b[p.i:min(p.i+len(lit), len(p.b))]), lit) {
			p.i += len(lit)
			return true
		}
	}
	return false
}
