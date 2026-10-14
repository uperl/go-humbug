// Package sourcemap indexes a JSON document so RFC 6901 pointers can be mapped
// to byte spans in the original bytes. Offsets are zero-based, start inclusive,
// end exclusive; lines and columns are one-based and columns count UTF-8 bytes.
//
// The index is a tree that mirrors the document, so its size is proportional to the number of
// values and does not depend on how deeply they are nested. (An earlier version keyed every
// value by its full pointer string, which made memory grow with the square of the nesting
// depth.) A pointer is resolved by walking the tree one token at a time.
package sourcemap

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MaxDepth is the deepest container nesting Build accepts. It equals the limit of Go's
// encoding/json (which the validation library uses), so any document deeper than this has already
// been rejected as malformed and its index would never be used.
const MaxDepth = 10000

// smallObject is the member count up to which an object is searched linearly; larger objects get
// a name index so lookups and duplicate detection stay linear overall.
const smallObject = 8

type Span struct {
	Start, End         int
	Line, Column       int
	EndLine, EndColumn int
}

type member struct {
	name string
	n    *node
}

// node is one value of the document. Spans are kept as offsets and converted to lines and columns
// only when a lookup returns them.
type node struct {
	start, end   int // the value
	kstart, kend int // the member name, when hasKey
	hasKey       bool
	ambiguous    bool // duplicate member names: this is the last occurrence

	members []member       // object members in document order (a duplicate replaces in place)
	byName  map[string]int // name -> position in members, built for large objects
	elems   []*node        // array elements
}

// Index maps JSON pointers to spans.
type Index struct {
	lineStart []int
	root      *node
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
	ix := &Index{lineStart: []int{0}}
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
	root, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(body) {
		return nil, p.err("unexpected data after top-level value")
	}
	ix.root = root
	return ix, nil
}

// find returns the position of the member with this name, or -1.
func (n *node) find(name string) int {
	if n.byName != nil {
		if i, ok := n.byName[name]; ok {
			return i
		}
		return -1
	}
	for i := range n.members {
		if n.members[i].name == name {
			return i
		}
	}
	return -1
}

// add appends a member, or replaces an earlier member of the same name (the last occurrence
// wins, as in encoding/json) and flags the replacement as ambiguous.
func (n *node) add(name string, child *node) {
	if i := n.find(name); i >= 0 {
		child.ambiguous = true
		n.members[i].n = child
		return
	}
	n.members = append(n.members, member{name, child})
	switch {
	case n.byName != nil:
		n.byName[name] = len(n.members) - 1
	case len(n.members) > smallObject:
		n.byName = make(map[string]int, len(n.members)*2)
		for i, m := range n.members {
			n.byName[m.name] = i
		}
	}
}

// child returns the value at one decoded pointer token: an array element by canonical index, or
// an object member by name. It returns nil when there is none.
func (n *node) child(tok string) *node {
	if len(n.elems) > 0 {
		i, ok := arrayIndex(tok)
		if !ok || i >= len(n.elems) {
			return nil
		}
		return n.elems[i]
	}
	if i := n.find(tok); i >= 0 {
		return n.members[i].n
	}
	return nil
}

// arrayIndex parses a canonical RFC 6901 array index: digits only, no leading zero except "0".
func arrayIndex(tok string) (int, bool) {
	if tok == "" || len(tok) > 18 || (len(tok) > 1 && tok[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, false
		}
	}
	i, err := strconv.Atoi(tok)
	return i, err == nil
}

// walk resolves pointer to the deepest existing node. exact is true when every token matched.
// valid is false when pointer is not a JSON Pointer at all (non-empty without a leading "/").
func (ix *Index) walk(pointer string) (n *node, exact, valid bool) {
	if pointer != "" && pointer[0] != '/' {
		return nil, false, false
	}
	n = ix.root
	for rest := pointer; rest != ""; {
		rest = rest[1:] // the "/"
		tok := rest
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			tok, rest = rest[:j], rest[j:]
		} else {
			rest = ""
		}
		tok, ok := unescape(tok)
		if !ok {
			return n, false, true
		}
		next := n.child(tok)
		if next == nil {
			return n, false, true
		}
		n = next
	}
	return n, true, true
}

// unescape decodes one reference token (~1 is "/", ~0 is "~"). A "~" followed by anything else
// is not a valid escape.
func unescape(tok string) (string, bool) {
	if !strings.Contains(tok, "~") {
		return tok, true
	}
	var sb strings.Builder
	for i := 0; i < len(tok); i++ {
		if tok[i] != '~' {
			sb.WriteByte(tok[i])
			continue
		}
		i++
		switch {
		case i < len(tok) && tok[i] == '0':
			sb.WriteByte('~')
		case i < len(tok) && tok[i] == '1':
			sb.WriteByte('/')
		default:
			return "", false
		}
	}
	return sb.String(), true
}

// Lookup returns the span of the value at pointer. ambiguous is true when duplicate member
// names make the mapping uncertain (the last occurrence is reported, matching encoding/json).
func (ix *Index) Lookup(pointer string) (s Span, ambiguous, ok bool) {
	n, exact, valid := ix.walk(pointer)
	if !valid || !exact {
		return Span{}, false, false
	}
	return ix.span(n.start, n.end), n.ambiguous, true
}

// Nearest returns the span of the longest existing ancestor of pointer and
// whether that ancestor is the pointer itself.
func (ix *Index) Nearest(pointer string) (s Span, ambiguous, exact, ok bool) {
	n, exact, valid := ix.walk(pointer)
	if !valid {
		return Span{}, false, false, false
	}
	return ix.span(n.start, n.end), n.ambiguous, exact, true
}

// Part says which token of the document a span covers.
const (
	PartKey   = "key"   // the member name, quotes included
	PartValue = "value" // the value (the root, an array element, or any non-member)
)

// Target returns the span to highlight for pointer: the member name when the pointer addresses an
// object member, otherwise the value (the root and array elements have no name). When the pointer
// does not exist it uses the closest existing ancestor and reports exact as false.
// ambiguous is true when duplicate member names make the mapping uncertain (the last occurrence
// is reported, matching encoding/json).
func (ix *Index) Target(pointer string) (s Span, part string, ambiguous, exact, ok bool) {
	n, exact, valid := ix.walk(pointer)
	if !valid {
		return Span{}, "", false, false, false
	}
	if n.hasKey {
		return ix.span(n.kstart, n.kend), PartKey, n.ambiguous, exact, true
	}
	return ix.span(n.start, n.end), PartValue, n.ambiguous, exact, true
}

// Escape encodes one reference token per RFC 6901.
func Escape(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~", "~0"), "/", "~1")
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
	ix    *Index
	b     []byte
	i     int
	depth int
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

// enter records one level of container nesting and fails past MaxDepth. The offset reported is
// that of the offending opening bracket.
func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return p.err(fmt.Sprintf("exceeds the maximum nesting depth of %d", MaxDepth))
	}
	return nil
}

func (p *parser) value() (*node, error) {
	if p.i >= len(p.b) {
		return nil, p.err("unexpected end of input")
	}
	n := &node{start: p.i}
	switch c := p.b[p.i]; {
	case c == '{':
		if err := p.object(n); err != nil {
			return nil, err
		}
	case c == '[':
		if err := p.array(n); err != nil {
			return nil, err
		}
	case c == '"':
		if _, err := p.str(false); err != nil {
			return nil, err
		}
	case c == '-' || (c >= '0' && c <= '9'):
		if err := p.number(); err != nil {
			return nil, err
		}
	default:
		if !p.literal() {
			return nil, p.err(fmt.Sprintf("invalid character %q looking for beginning of value", c))
		}
	}
	n.end = p.i
	return n, nil
}

func (p *parser) object(n *node) error {
	if err := p.enter(); err != nil {
		return err
	}
	defer func() { p.depth-- }()
	p.i++ // {
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return nil
	}
	for {
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return p.err("expected object key string")
		}
		ks := p.i
		key, err := p.str(true)
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
		child, err := p.value()
		if err != nil {
			return err
		}
		child.hasKey, child.kstart, child.kend = true, ks, ke
		n.add(key, child)
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

func (p *parser) array(n *node) error {
	if err := p.enter(); err != nil {
		return err
	}
	defer func() { p.depth-- }()
	p.i++ // [
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return nil
	}
	for {
		p.ws()
		child, err := p.value()
		if err != nil {
			return err
		}
		n.elems = append(n.elems, child)
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

// str scans a string token. It returns the decoded content only when decode is true (member
// names need it; values do not, so a large string value is validated without being copied).
func (p *parser) str(decode bool) (string, error) {
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
				if decode {
					sb.WriteByte(e)
				}
			case 'b':
				if decode {
					sb.WriteByte('\b')
				}
			case 'f':
				if decode {
					sb.WriteByte('\f')
				}
			case 'n':
				if decode {
					sb.WriteByte('\n')
				}
			case 'r':
				if decode {
					sb.WriteByte('\r')
				}
			case 't':
				if decode {
					sb.WriteByte('\t')
				}
			case 'u':
				r, n, err := p.unicode()
				if err != nil {
					return "", err
				}
				if decode {
					sb.WriteRune(r)
				}
				p.i += n
				continue
			default:
				return "", p.err("invalid escape character")
			}
			p.i++
		default:
			if decode {
				sb.WriteByte(c)
			}
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
	for _, lit := range [...]string{"true", "false", "null"} {
		if bytes.HasPrefix(p.b[p.i:], []byte(lit)) {
			p.i += len(lit)
			return true
		}
	}
	return false
}
