// Package protocol implements Humbug Protocol v1 over NDJSON. It knows nothing
// about pb33f; validation is delegated to a Validator.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"

	"humbug/internal/diagnostics"
	"humbug/internal/validation"
)

const Version = 1

// MaxLineBytes bounds one command line (provisional default; see docs/protocol.md).
const MaxLineBytes = 256 << 20

// Error codes.
const (
	ErrInvalidMessage     = "protocol.invalid_message"
	ErrUnsupportedVersion = "protocol.unsupported_version"
	ErrUnknownCommand     = "protocol.unknown_command"
	ErrNotInitialized     = "protocol.not_initialized"
	ErrAlreadyInit        = "protocol.already_initialized"
	ErrLineTooLong        = "protocol.line_too_long"
	ErrSpecInvalid        = "spec.invalid"
	ErrUnsupportedInput   = "validator.unsupported_input"
	ErrInternal           = "validator.internal"
)

// Loader loads a specification; it is a seam for tests.
type Loader func(path string) (Validator, validation.SpecInfo, error)

type Validator interface {
	Validate(validation.Params) (*diagnostics.Result, error)
}

// EngineLoader is the production Loader.
func EngineLoader(path string) (Validator, validation.SpecInfo, error) {
	e, err := validation.Load(path)
	if err != nil {
		return nil, validation.SpecInfo{}, err
	}
	return e, e.Info, nil
}

type command struct {
	ID     json.RawMessage `json:"id"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params,omitempty"`
}

type reply struct {
	ID     json.RawMessage `json:"id"`
	Type   string          `json:"type"`
	Result any             `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Server struct {
	Load Loader
	v    Validator
}

var intID = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// Serve processes commands until EOF or shutdown. A write error ends the loop.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 64<<10)
	bw := bufio.NewWriter(w)
	enc := func(rep reply) error {
		b, err := json.Marshal(rep)
		if err != nil {
			b, _ = json.Marshal(reply{ID: rep.ID, Type: "error", Error: &Error{ErrInternal, "reply could not be encoded"}})
		}
		if _, err := bw.Write(append(b, '\n')); err != nil {
			return err
		}
		return bw.Flush()
	}
	for {
		line, tooLong, err := readLine(br)
		if err != nil && len(line) == 0 && !tooLong {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if tooLong {
			if e := enc(errReply(nil, ErrLineTooLong, "command line exceeds the maximum size")); e != nil {
				return e
			}
			continue
		}
		rep, stop := s.handle(line)
		if e := enc(rep); e != nil {
			return e
		}
		if stop {
			return nil
		}
	}
}

// readLine returns one line without its terminator. When the line exceeds
// MaxLineBytes the remainder is drained and tooLong is true.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return line, tooLong, e
		}
		if !tooLong {
			if len(line)+len(chunk) > MaxLineBytes {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

func errReply(id json.RawMessage, code, msg string) reply {
	if id == nil {
		id = json.RawMessage("null")
	}
	return reply{ID: id, Type: "error", Error: &Error{code, msg}}
}

func (s *Server) handle(line []byte) (rep reply, stop bool) {
	var cmd command
	dec := json.NewDecoder(bytes.NewReader(line))
	if err := dec.Decode(&cmd); err != nil || dec.More() {
		return errReply(nil, ErrInvalidMessage, "command is not a single JSON object"), false
	}
	if rest, _ := io.ReadAll(dec.Buffered()); len(bytes.TrimSpace(rest)) > 0 {
		return errReply(nil, ErrInvalidMessage, "trailing data after command"), false
	}
	id := cmd.ID
	if !validID(id) {
		return errReply(nil, ErrInvalidMessage, "id must be a string or an integer"), false
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "humbug: internal panic: %v\n", r)
			rep, stop = errReply(id, ErrInternal, "validation could not be completed"), false
		}
	}()
	switch cmd.Type {
	case "initialize":
		return s.initialize(id, cmd.Params), false
	case "validate":
		return s.validate(id, cmd.Params), false
	case "shutdown":
		return reply{ID: id, Type: "result", Result: struct{}{}}, true
	case "":
		return errReply(id, ErrInvalidMessage, "missing command type"), false
	default:
		return errReply(id, ErrUnknownCommand, fmt.Sprintf("unknown command type %q", cmd.Type)), false
	}
}

func validID(id json.RawMessage) bool {
	id = bytes.TrimSpace(id)
	if len(id) == 0 {
		return false
	}
	if id[0] == '"' {
		var s string
		return json.Unmarshal(id, &s) == nil
	}
	return intID.Match(id)
}

func strict(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return errors.New("params are required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func (s *Server) initialize(id json.RawMessage, raw json.RawMessage) reply {
	if s.v != nil {
		return errReply(id, ErrAlreadyInit, "already initialized; start another process to use a different specification")
	}
	var p struct {
		ProtocolVersion *int `json:"protocol_version"`
		Spec            *struct {
			Path string `json:"path"`
		} `json:"spec"`
	}
	if err := strict(raw, &p); err != nil || p.ProtocolVersion == nil || p.Spec == nil || p.Spec.Path == "" {
		return errReply(id, ErrInvalidMessage, "initialize requires protocol_version and spec.path")
	}
	if *p.ProtocolVersion != Version {
		return errReply(id, ErrUnsupportedVersion, fmt.Sprintf("protocol version %d is not supported; this sidecar speaks %d", *p.ProtocolVersion, Version))
	}
	v, info, err := s.Load(p.Spec.Path)
	if err != nil {
		// A failed initialization leaves the process uninitialized; the client may retry.
		return errReply(id, ErrSpecInvalid, "specification could not be loaded: "+err.Error())
	}
	s.v = v
	return reply{ID: id, Type: "result", Result: map[string]any{
		"protocol_version": Version,
		"specification":    map[string]string{"openapi_version": info.OpenAPIVersion, "title": info.Title},
	}}
}

type wireRequest struct {
	Method     string     `json:"method"`
	URL        string     `json:"url"`
	Headers    [][]string `json:"headers"`
	BodyBase64 *string    `json:"body_base64"`
}

type wireResponse struct {
	Status     int        `json:"status"`
	Headers    [][]string `json:"headers"`
	BodyBase64 *string    `json:"body_base64"`
}

func (s *Server) validate(id json.RawMessage, raw json.RawMessage) reply {
	if s.v == nil {
		return errReply(id, ErrNotInitialized, "initialize must succeed before validate")
	}
	var p struct {
		Request  *wireRequest  `json:"request"`
		Response *wireResponse `json:"response"`
		Validate *struct {
			Request  *bool `json:"request"`
			Response *bool `json:"response"`
		} `json:"validate"`
	}
	bad := func(msg string) reply { return errReply(id, ErrInvalidMessage, msg) }
	if err := strict(raw, &p); err != nil {
		return bad("invalid validate params: " + err.Error())
	}
	if p.Request == nil || p.Request.Method == "" || p.Request.URL == "" {
		return bad("request.method and request.url are required")
	}
	if u, err := url.Parse(p.Request.URL); err != nil || u.Scheme == "" || u.Host == "" {
		return errReply(id, ErrUnsupportedInput, "request.url must be an absolute URL")
	}
	// Defaults: validate each supplied message.
	params := validation.Params{ValidateRequest: true, ValidateResponse: p.Response != nil}
	if p.Validate != nil {
		if p.Validate.Request != nil {
			params.ValidateRequest = *p.Validate.Request
		}
		if p.Validate.Response != nil {
			params.ValidateResponse = *p.Validate.Response
		}
	}
	if params.ValidateResponse && p.Response == nil {
		return bad("validate.response is true but no response was supplied")
	}
	if !params.ValidateRequest && !params.ValidateResponse {
		return bad("nothing to validate: both checks are disabled")
	}
	reqHeaders, msg := headerPairs("request", p.Request.Headers)
	if msg != "" {
		return bad(msg)
	}
	var resHeaders []validation.Header
	if p.Response != nil {
		if resHeaders, msg = headerPairs("response", p.Response.Headers); msg != "" {
			return bad(msg)
		}
	}
	rb, err := decodeBody(p.Request.BodyBase64)
	if err != nil {
		return bad("request.body_base64 is not valid base64")
	}
	params.Request = validation.Request{Method: p.Request.Method, URL: p.Request.URL, Headers: reqHeaders, Body: rb}
	if p.Response != nil {
		if p.Response.Status < 100 || p.Response.Status > 599 {
			return bad("response.status must be an HTTP status code")
		}
		sb, err := decodeBody(p.Response.BodyBase64)
		if err != nil {
			return bad("response.body_base64 is not valid base64")
		}
		params.Response = &validation.Response{Status: p.Response.Status, Headers: resHeaders, Body: sb}
	}
	res, err := s.v.Validate(params)
	if err != nil {
		var ie *validation.InputError
		if errors.As(err, &ie) {
			return errReply(id, ErrUnsupportedInput, "input could not be converted to an HTTP message: "+err.Error())
		}
		fmt.Fprintf(stderr, "humbug: validation failed: %v\n", err)
		return errReply(id, ErrInternal, "validation could not be completed")
	}
	return reply{ID: id, Type: "result", Result: res}
}

// decodeBody maps absent/null to nil (no body) and "" to an explicit empty body.
func decodeBody(s *string) (*[]byte, error) {
	if s == nil {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(*s)
	if err != nil {
		return nil, err
	}
	if b == nil {
		b = []byte{}
	}
	return &b, nil
}

// headerPairs converts the wire's ordered header list without altering it: order, duplicates,
// casing and values are carried exactly as sent. The only check is structural: every entry must
// be a two-element [name, value] array (a decoder fixed to two elements would silently drop or
// invent a value). What a name or value may contain is not the protocol's business; that is up to
// the implementation.
func headerPairs(side string, h [][]string) ([]validation.Header, string) {
	out := make([]validation.Header, len(h))
	for i, p := range h {
		if len(p) != 2 {
			return nil, fmt.Sprintf("%s.headers[%d]: must be a [name, value] pair", side, i)
		}
		out[i] = validation.Header{p[0], p[1]}
	}
	return out, ""
}
