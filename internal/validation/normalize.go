package validation

import (
	"errors"
	"fmt"
	"mime"
	"reflect"
	"strconv"
	"strings"

	"github.com/pb33f/jsonschema/v6"
	"github.com/pb33f/jsonschema/v6/kind"
	verrors "github.com/pb33f/libopenapi-validator/errors"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	d "humbug/internal/diagnostics"
	"humbug/internal/sourcemap"
)

// normalizer converts one side's pb33f errors into diagnostics. Messages are
// generated here from structured data and never echo payload values.
type normalizer struct {
	who     string
	body    []byte
	hasBody bool
	model   *v3.Document

	ix        *sourcemap.Index
	ixErr     error
	ixBuilt   bool
	seq       int
	diags     []d.Diagnostic
	seen      map[string]bool
	incomplet bool
	issue     *bodyIssue // Content-Encoding problem; body findings are not meaningful
}

func newNormalizer(side string, body []byte, hasBody bool, model *v3.Document) *normalizer {
	return &normalizer{who: side, body: body, hasBody: hasBody, model: model, seen: map[string]bool{}}
}

func (n *normalizer) tmpID() string { n.seq++; return "t" + strconv.Itoa(n.seq) }

func (n *normalizer) add(x d.Diagnostic) string {
	x.ID = n.tmpID()
	x.Side = n.who
	if x.Severity == "" {
		x.Severity = d.SeverityError
	}
	n.diags = append(n.diags, x)
	return x.ID
}

func (n *normalizer) index() (*sourcemap.Index, error) {
	if !n.ixBuilt {
		n.ixBuilt = true
		if n.hasBody {
			n.ix, n.ixErr = sourcemap.Build(n.body)
		} else {
			n.ixErr = errors.New("no body")
		}
	}
	return n.ix, n.ixErr
}

func (n *normalizer) result(errs []*verrors.ValidationError) *d.Side {
	for _, e := range errs {
		n.one(e)
	}
	n.applyIssue()
	s := &d.Side{Diagnostics: n.diags}
	if s.Diagnostics == nil {
		s.Diagnostics = []d.Diagnostic{}
	}
	s.Finish(n.incomplet)
	return s
}

func (n *normalizer) one(e *verrors.ValidationError) {
	typ, sub := e.ValidationType, e.ValidationSubType
	switch {
	case typ == "path" && (sub == "missing" || sub == "missingOperation"):
		n.incomplet = true
		code, msg := d.CodeOperationNotFound, "no operation matches the request path"
		if sub == "missingOperation" {
			code, msg = d.CodeMethodNotAllowed, "the matched path has no operation for the request method"
		}
		if n.who == d.SideResponse {
			// The request side reports the finding; the response simply could not be checked.
			n.add(d.Diagnostic{Code: d.CodeIncomplete, Severity: d.SeverityInfo, Reason: code,
				Message: "response not validated: " + msg, Instance: &d.Instance{Location: "operation"}})
			return
		}
		n.add(d.Diagnostic{Code: code, Message: msg, Instance: &d.Instance{Location: "operation"}})

	case typ == "parameter":
		loc := sub
		switch sub {
		case "query", "header", "path", "cookie":
		default:
			n.unclassified(e)
			return
		}
		fails := e.SchemaValidationErrors
		if len(fails) == 0 {
			n.add(d.Diagnostic{Code: d.CodeParameterInvalid, Instance: &d.Instance{Location: loc, Name: e.ParameterName},
				Message: fmt.Sprintf("%s parameter %q is invalid", loc, e.ParameterName)})
			return
		}
		for _, f := range fails {
			code, word := d.CodeParameterInvalid, "is invalid"
			if kw := lastSeg(f.KeywordLocation); kw == "required" {
				code, word = d.CodeParameterRequired, "is required but missing"
			}
			n.addDedup(d.Diagnostic{Code: code, Instance: &d.Instance{Location: loc, Name: e.ParameterName},
				Message: fmt.Sprintf("%s parameter %q %s", loc, e.ParameterName, word)}, "")
		}

	case typ == "requestBody" && sub == "contentType":
		n.add(d.Diagnostic{Code: d.CodeBodyMediaType, Instance: &d.Instance{Location: "header", Name: "Content-Type"},
			Message: "request body media type is not defined for this operation"})

	case typ == "response" && sub == "contentType":
		n.add(d.Diagnostic{Code: d.CodeBodyMediaType, Instance: &d.Instance{Location: "header", Name: "Content-Type"},
			Message: "response body media type is not defined for this operation"})

	case typ == "response" && sub == "header":
		for _, f := range e.SchemaValidationErrors {
			name := f.FieldName
			if name == "" && len(f.InstancePath) > 0 {
				name = f.InstancePath[0]
			}
			code, word := d.CodeResponseHeaderInv, "is invalid"
			if lastSeg(f.KeywordLocation) == "required" {
				code, word = d.CodeResponseHeaderReq, "is required but missing"
			}
			n.addDedup(d.Diagnostic{Code: code, Instance: &d.Instance{Location: "header", Name: name},
				Message: fmt.Sprintf("response header %q %s", name, word)}, "")
		}
		if len(e.SchemaValidationErrors) == 0 {
			n.unclassified(e)
		}

	case typ == "response" && sub == "statusCode":
		n.add(d.Diagnostic{Code: d.CodeResponseStatus, Instance: &d.Instance{Location: "status"},
			Message: "response status code is not defined for this operation"})

	case (typ == "requestBody" || typ == "response") && sub == "schema":
		if len(e.SchemaValidationErrors) == 0 {
			n.decodeFailure(e)
			return
		}
		n.schemaFailures(e)

	default:
		n.unclassified(e)
	}
}

func (n *normalizer) addDedup(x d.Diagnostic, extra string) string {
	key := fmt.Sprintf("%s|%s|%s|%s|%s", x.Code, locKey(x.Instance), schemaKey(x.Schema), strings.Join(missing(x.Instance), ","), extra)
	if n.seen[key] {
		return ""
	}
	n.seen[key] = true
	return n.add(x)
}

func (n *normalizer) unclassified(e *verrors.ValidationError) {
	n.add(d.Diagnostic{Code: d.CodeUnclassified, Message: "the validator reported a finding that has no specific classification",
		Detail: map[string]string{"validation_type": e.ValidationType, "validation_sub_type": e.ValidationSubType}})
}

func (n *normalizer) decodeFailure(e *verrors.ValidationError) {
	if !n.hasBody {
		n.add(d.Diagnostic{Code: d.CodeBodyRequired, Instance: &d.Instance{Location: "body"},
			Message: n.who + " has no body but the operation requires one"})
		return
	}
	diag := d.Diagnostic{Code: d.CodeBodyDecode, Instance: &d.Instance{Location: "body"}, Message: "body could not be decoded"}
	if _, err := n.index(); err != nil {
		var pe *sourcemap.ParseError
		if errors.As(err, &pe) {
			diag.Message = "body is not well-formed JSON"
			diag.Instance.Span = &d.Span{Start: pe.Offset, End: pe.Offset, Line: pe.Line, Column: pe.Column, EndLine: pe.Line, EndColumn: pe.Column}
			diag.Detail = map[string]string{"parser": pe.Msg}
		}
	}
	n.add(diag)
}

// schemaFailures groups composition (oneOf/anyOf) leaf failures under one parent
// diagnostic per composition keyword instance and emits the rest as plain findings.
func (n *normalizer) schemaFailures(e *verrors.ValidationError) {
	type group struct {
		parent string
		kw     string // "oneOf" | "anyOf"
		prefix string
		inst   string
	}
	groups := map[string]*group{}
	var order []string
	root := &d.SchemaRef{Line: posLine(e), Column: posCol(e), Relative: true}

	for _, f := range e.SchemaValidationErrors {
		ptr := pointerOf(f.InstancePath)
		prefix, kw, branch, inComp := compositionOf(f.KeywordLocation)

		if inComp && branch >= 0 {
			key := prefix + "|" + ptr
			g := groups[key]
			if g == nil {
				g = &group{kw: kw, prefix: prefix, inst: ptr}
				code := d.CodeSchemaAnyOf
				if kw == "oneOf" {
					code = d.CodeSchemaOneOfNoMatch
				}
				g.parent = n.add(d.Diagnostic{Code: code, Instance: n.bodyInstance(ptr, false, nil),
					Schema:  &d.SchemaRef{Pointer: prefix, Relative: true, Line: root.Line, Column: root.Column},
					Message: fmt.Sprintf("value matched none of the %s branches", kw)})
				groups[key] = g
				order = append(order, key)
			}
			child := n.leaf(f, root)
			if child == "" {
				continue
			}
			n.patch(child, func(x *d.Diagnostic) { x.Parent = g.parent; b := branch; x.Branch = &b })
			n.patch(g.parent, func(x *d.Diagnostic) { x.Children = append(x.Children, child) })
			continue
		}

		if lastSeg(f.KeywordLocation) == "oneOf" && oneOfMatches(f.OriginalJsonSchemaError) > 1 {
			n.addDedup(d.Diagnostic{Code: d.CodeSchemaOneOfAmbiguous, Instance: n.bodyInstance(ptr, false, nil),
				Schema:  &d.SchemaRef{Pointer: f.KeywordLocation, Relative: true, Line: root.Line, Column: root.Column},
				Message: "value matched more than one oneOf branch"}, "")
			continue
		}
		n.leaf(f, root)
	}
}

func (n *normalizer) patch(id string, fn func(*d.Diagnostic)) {
	for i := range n.diags {
		if n.diags[i].ID == id {
			fn(&n.diags[i])
			return
		}
	}
}

// leaf emits one keyword-level finding, returning its temporary ID ("" if deduplicated).
func (n *normalizer) leaf(f *verrors.SchemaValidationFailure, root *d.SchemaRef) string {
	ptr := pointerOf(f.InstancePath)
	kw := lastSeg(f.KeywordLocation)
	code := d.CodeSchemaPrefix + kwName(kw)
	var miss []string
	if kw == "required" {
		miss = requiredNames(f.OriginalJsonSchemaError, f.InstancePath)
	}
	inst := n.bodyInstance(ptr, kw == "required", miss)
	msg := fmt.Sprintf("%s violates schema keyword %q", describe(ptr), kw)
	if kw == "required" {
		msg = fmt.Sprintf("%s is missing required properties", describe(ptr))
	}
	return n.addDedup(d.Diagnostic{Code: code, Instance: inst, Message: msg,
		Schema: &d.SchemaRef{Pointer: f.KeywordLocation, Relative: true, Line: root.Line, Column: root.Column}}, "")
}

func (n *normalizer) bodyInstance(ptr string, missingProp bool, miss []string) *d.Instance {
	inst := &d.Instance{Location: "body", Pointer: ptr, Missing: miss}
	ix, err := n.index()
	if err != nil {
		return inst
	}
	if s, amb, exact, ok := ix.Nearest(ptr); ok {
		inst.Span = &d.Span{Start: s.Start, End: s.End, Line: s.Line, Column: s.Column, EndLine: s.EndLine, EndColumn: s.EndColumn,
			Contextual: !exact || missingProp, Ambiguous: amb}
	}
	return inst
}

// compositionOf finds the first oneOf/anyOf segment in a keyword location such as
// /properties/x/oneOf/1/$ref/required, returning the prefix up to and including the
// keyword and the branch index (-1 when the location ends at the keyword itself).
func compositionOf(loc string) (prefix, kw string, branch int, ok bool) {
	parts := strings.Split(loc, "/")
	for i, p := range parts {
		if p == "oneOf" || p == "anyOf" {
			prefix = strings.Join(parts[:i+1], "/")
			branch = -1
			if i+1 < len(parts) {
				if b, err := strconv.Atoi(parts[i+1]); err == nil {
					branch = b
				}
			}
			return prefix, p, branch, true
		}
	}
	return "", "", -1, false
}

// kwName converts a JSON Schema keyword to its code suffix: additionalProperties
// becomes additional_properties; others (minLength, oneOf) keep their spelling.
func kwName(kw string) string {
	if kw == "additionalProperties" {
		return "additional_properties"
	}
	if kw == "" {
		return "unknown"
	}
	return kw
}

func lastSeg(loc string) string {
	if i := strings.LastIndexByte(loc, '/'); i >= 0 {
		return loc[i+1:]
	}
	return loc
}

func pointerOf(path []string) string {
	var sb strings.Builder
	for _, t := range path {
		sb.WriteByte('/')
		sb.WriteString(sourcemap.Escape(t))
	}
	return sb.String()
}

func describe(ptr string) string {
	if ptr == "" {
		return "body"
	}
	return "body at " + ptr
}

func posLine(e *verrors.ValidationError) int { return max(e.SpecLine, 0) }
func posCol(e *verrors.ValidationError) int  { return max(e.SpecCol, 0) }

func locKey(i *d.Instance) string {
	if i == nil {
		return ""
	}
	return i.Location + "|" + i.Name + "|" + i.Pointer
}

func schemaKey(s *d.SchemaRef) string {
	if s == nil {
		return ""
	}
	return s.Pointer
}

func missing(i *d.Instance) []string {
	if i == nil {
		return nil
	}
	return i.Missing
}

// oneOfMatches returns the largest number of matched subschemas reported by any
// kind.OneOf node in the error tree (0 when none, or no OneOf node exists).
func oneOfMatches(e *jsonschema.ValidationError) int {
	if e == nil {
		return 0
	}
	best := 0
	if k, ok := e.ErrorKind.(*kind.OneOf); ok {
		best = len(k.Subschemas)
	}
	for _, c := range e.Causes {
		best = max(best, oneOfMatches(c))
	}
	return best
}

// requiredNames collects missing property names from kind.Required nodes at the
// given instance location. Returns nil when the match is ambiguous (several
// distinct Required nodes at one location, e.g. across composition branches).
func requiredNames(e *jsonschema.ValidationError, at []string) []string {
	var found [][]string
	var walk func(*jsonschema.ValidationError)
	walk = func(x *jsonschema.ValidationError) {
		if x == nil {
			return
		}
		if k, ok := x.ErrorKind.(*kind.Required); ok && reflect.DeepEqual(nilToEmpty(x.InstanceLocation), nilToEmpty(at)) {
			found = append(found, k.Missing)
		}
		for _, c := range x.Causes {
			walk(c)
		}
	}
	walk(e)
	if len(found) != 1 {
		return nil
	}
	return found[0]
}

func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// noteUninspected marks the side incomplete when a body is present but is not a
// JSON media type: the library only schema-validates JSON bodies, so any other
// content has not been checked and must never be reported as conforming.
func (n *normalizer) noteUninspected(mediaType string) {
	if !n.hasBody || n.issue != nil || isJSON(mediaType) {
		return
	}
	n.incomplet = true
	detail := map[string]string{}
	if mediaType != "" {
		detail["media_type"] = mediaType
	}
	n.add(d.Diagnostic{Code: d.CodeIncomplete, Severity: d.SeverityInfo, Reason: "body.not_inspected",
		Instance: &d.Instance{Location: "body"}, Detail: detail,
		Message: "body was not schema-validated: only JSON bodies are inspected"})
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// applyIssue handles a Content-Encoding problem. The library saw the undecoded
// bytes, so its body findings are discarded in favor of one explicit finding.
func (n *normalizer) applyIssue() {
	if n.issue == nil {
		return
	}
	kept := n.diags[:0]
	for _, x := range n.diags {
		if x.Instance == nil || x.Instance.Location != "body" {
			kept = append(kept, x)
		}
	}
	n.diags = kept
	detail := map[string]string{"content_encoding": n.issue.encoding}
	inst := &d.Instance{Location: "body"}
	switch n.issue.kind {
	case issueCorrupt:
		n.add(d.Diagnostic{Code: d.CodeBodyDecode, Instance: inst, Detail: detail,
			Message: "body could not be decoded with its declared Content-Encoding"})
	case issueTooLarge:
		n.incomplet = true
		n.add(d.Diagnostic{Code: d.CodeIncomplete, Severity: d.SeverityInfo, Reason: "body.too_large", Instance: inst, Detail: detail,
			Message: "decoded body exceeds the size limit and was not inspected"})
	default:
		n.incomplet = true
		n.add(d.Diagnostic{Code: d.CodeIncomplete, Severity: d.SeverityInfo, Reason: "body.content_encoding_unsupported", Instance: inst, Detail: detail,
			Message: "body has an unsupported Content-Encoding and was not inspected"})
	}
}
