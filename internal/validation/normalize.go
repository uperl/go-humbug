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

	d "github.com/uperl/go-humbug/internal/diagnostics"
	"github.com/uperl/go-humbug/internal/sourcemap"
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
	noSpans   bool                  // body is not JSON: findings carry no pointer or span
	sentCT    bool                  // the message carried a Content-Type header
	decoded   bool                  // XML or form body: findings keep their field path as pointer but have no span
	mediaType string                // lower-cased media type of this side's body, for diagnostics
	skipped   *bodySkip             // the body exceeded a limit and was not handed to the library
	decls     map[string]headerDecl // declared headers by lower-cased name
}

func newNormalizer(side string, body []byte, hasBody bool, model *v3.Document) *normalizer {
	return &normalizer{who: side, body: body, hasBody: hasBody, model: model, seen: map[string]bool{}}
}

// tmpID is unique across both sides of a transaction (the side is part of the ID), because
// parent/child and cause references are remapped together in diagnostics.AssignIDs.
func (n *normalizer) tmpID() string { n.seq++; return n.who + "-t" + strconv.Itoa(n.seq) }

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
	n.applySkip()
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
		pname := e.ParameterName
		if loc == "header" {
			pname = n.hname(pname)
		}
		for _, f := range fails {
			code, word := d.CodeParameterInvalid, "is invalid"
			if kw := lastSeg(f.KeywordLocation); kw == "required" {
				code, word = d.CodeParameterRequired, "is required but missing"
			}
			n.addDedup(d.Diagnostic{Code: code, Instance: &d.Instance{Location: loc, Name: pname},
				Message: fmt.Sprintf("%s parameter %q %s", loc, pname, word)}, "")
		}

	case typ == "requestBody" && sub == "contentType":
		n.add(d.Diagnostic{Code: d.CodeBodyMediaType, Instance: &d.Instance{Location: "header", Name: "Content-Type"},
			Message: "request body media type is not defined for this operation"})

	case typ == "response" && sub == "contentType":
		// A response with no body and no Content-Type has no media type to check; the library
		// reports one anyway. Nothing is wrong, so the finding is dropped.
		if !n.sentCT && len(n.body) == 0 && n.skipped == nil {
			return
		}
		n.add(d.Diagnostic{Code: d.CodeBodyMediaType, Instance: &d.Instance{Location: "header", Name: "Content-Type"},
			Message: "response body media type is not defined for this operation"})

	case typ == "response" && (sub == "header" || sub == "headers"):
		// "header" is a missing required header (name in the failure); "headers" is a header
		// that is present but fails its schema (name in ParameterName, lower-cased).
		if sub == "headers" {
			n.addDedup(d.Diagnostic{Code: d.CodeResponseHeaderInv, Instance: &d.Instance{Location: "header", Name: n.hname(e.ParameterName)},
				Message: fmt.Sprintf("response header %q is invalid", n.hname(e.ParameterName))}, "")
			return
		}
		for _, f := range e.SchemaValidationErrors {
			name := f.FieldName
			if name == "" && len(f.InstancePath) > 0 {
				name = f.InstancePath[0]
			}
			name = n.hname(name)
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

	case (typ == "xmlValidation" || typ == "urlEncodedValidation") && sub == "schema":
		n.decodeFailed()

	case typ == "urlEncodedValidation" && sub == "reservedValues":
		// DROPPED ON PURPOSE. The library applies its allowReserved check to the *decoded* value, so
		// it reports every correctly percent-encoded reserved character (a%40b.com, an ordinary email
		// address, is flagged as if the @ were raw). It cannot tell the two apart, so the finding is
		// unreliable and would be a false violation on ordinary data. The cost is that an unencoded
		// reserved character is accepted. TestLibraryStillFlagsEncodedReservedCharacters fails when the
		// library is fixed, which is the cue to remove this case. See docs/protocol.md.
		return

	case typ == "xmlValidation" && sub == "prefix":
		n.addDedup(d.Diagnostic{Code: d.CodeBodyXMLPrefix, Instance: &d.Instance{Location: "body"},
			Message: "an XML namespace prefix does not match the one the schema declares"}, "")

	case typ == "xmlValidation" && sub == "namespace":
		n.addDedup(d.Diagnostic{Code: d.CodeBodyXMLNamespace, Instance: &d.Instance{Location: "body"},
			Message: "an XML namespace does not match the one the schema declares"}, "")

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

// decodeFailed reports a body the library could not decode (malformed XML, a broken form). The
// library's own messages quote the payload, so none of it is used.
func (n *normalizer) decodeFailed() {
	detail := map[string]string{}
	if n.mediaType != "" {
		detail["media_type"] = n.mediaType
	}
	n.addDedup(d.Diagnostic{Code: d.CodeBodyDecode, Instance: &d.Instance{Location: "body"}, Detail: detail,
		Message: "body could not be decoded as " + n.mediaType}, "")
}

func (n *normalizer) decodeFailure(e *verrors.ValidationError) {
	if n.decoded {
		n.decodeFailed() // not JSON: there is no JSON position to report
		return
	}
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
			diag.Instance.Span = &d.Span{Start: pe.Offset, End: pe.Offset, Line: pe.Line, Column: pe.Column, EndLine: pe.Line, EndColumn: pe.Column, Part: "position"}
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
	line, col := schemaPos(e)
	root := &d.SchemaRef{Line: line, Column: col, Relative: true}

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
		if lastSeg(f.KeywordLocation) == "additionalProperties" && n.additionalMembers(f, ptr, root) {
			continue
		}
		n.leaf(f, root)
	}
}

// additionalMembers reports each property rejected by additionalProperties as its own finding,
// pointing at that member (so the highlighted span is the offending name). It returns false when
// the rejected names cannot be determined with certainty (for example across composition
// branches, where several additionalProperties constraints apply at one location), and the caller
// falls back to one finding for the object.
func (n *normalizer) additionalMembers(f *verrors.SchemaValidationFailure, ptr string, root *d.SchemaRef) bool {
	names := additionalNames(f.OriginalJsonSchemaError, f.InstancePath)
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		member := ptr + "/" + sourcemap.Escape(name)
		n.addDedup(d.Diagnostic{Code: d.CodeSchemaPrefix + "additional_properties", Instance: n.bodyInstance(member, false, nil),
			Schema:  &d.SchemaRef{Pointer: f.KeywordLocation, Relative: true, Line: root.Line, Column: root.Column},
			Message: fmt.Sprintf("%s is not allowed by the schema", describe(member))}, "")
	}
	return true
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
	x := d.Diagnostic{Code: code, Instance: inst, Message: msg,
		Schema: &d.SchemaRef{Pointer: f.KeywordLocation, Relative: true, Line: root.Line, Column: root.Column}}
	if kw == "type" {
		// Type names only, never values. Omitted when several type constraints apply at this
		// location (e.g. across composition branches) and the match would be a guess.
		if want, got, ok := typeInfo(f.OriginalJsonSchemaError, f.InstancePath); ok {
			x.Expected, x.Actual = want, got
		}
	}
	return n.addDedup(x, "")
}

func (n *normalizer) bodyInstance(ptr string, missingProp bool, miss []string) *d.Instance {
	inst := &d.Instance{Location: "body", Pointer: ptr, Missing: miss}
	if n.noSpans {
		inst.Pointer = ""
		return inst
	}
	if n.decoded {
		return inst
	}
	ix, err := n.index()
	if err != nil {
		return inst
	}
	// The span highlights the member name when the pointer addresses an object member (the
	// value otherwise: the root, an array element). A missing property highlights its parent.
	if s, part, amb, exact, ok := ix.Target(ptr); ok {
		inst.Span = &d.Span{Start: s.Start, End: s.End, Line: s.Line, Column: s.Column, EndLine: s.EndLine, EndColumn: s.EndColumn,
			Part: part, Contextual: !exact || missingProp, Ambiguous: amb}
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

// schemaPos is the position of the root schema in the specification, or 0, 0 when the library did
// not resolve it. For a root schema made of oneOf, anyOf or allOf it reports line 1 and no column,
// which would point at the first line of the document rather than at the schema, so a location is
// given only when both parts are real.
func schemaPos(e *verrors.ValidationError) (line, col int) {
	if e.SpecLine < 1 || e.SpecCol < 1 {
		return 0, 0
	}
	return e.SpecLine, e.SpecCol
}

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

// noteUninspected marks the side incomplete when a non-JSON body could not be checked
// (structured schema under a non-JSON type, or no usable Content-Type): the library only
// schema-validates JSON, so the content must never be reported as conforming.
func (n *normalizer) noteUninspected(mediaType string) {
	if !n.hasBody || isJSON(mediaType) {
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

// incomplete marks the side incomplete with an info-level explanation for the body.
func (n *normalizer) incomplete(reason string, detail map[string]string, msg string) {
	n.incomplet = true
	n.add(d.Diagnostic{Code: d.CodeIncomplete, Severity: d.SeverityInfo, Reason: reason,
		Instance: &d.Instance{Location: "body"}, Detail: detail, Message: msg})
}

// applySkip handles a body that exceeded a limit. The library never saw it, so any finding it
// reported about the body (a "required body is missing", say) is meaningless and is dropped; the
// side is incomplete instead, never valid.
func (n *normalizer) applySkip() {
	if n.skipped == nil {
		return
	}
	kept := n.diags[:0]
	for _, x := range n.diags {
		if x.Instance == nil || x.Instance.Location != "body" {
			kept = append(kept, x)
		}
	}
	n.diags = kept
	msg := "body was not inspected: it exceeds the size limit"
	switch n.skipped.reason {
	case "body.too_deep":
		msg = "body was not inspected: its JSON nesting exceeds the depth limit"
	case "body.xml_root_unnamed":
		msg = "XML body was not inspected: its root schema does not declare xml.name (the root element name)"
	}
	n.incomplete(n.skipped.reason, n.skipped.detail, msg)
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// typeInfo returns the wanted and actual JSON types from the single kind.Type node at the
// given instance location; ok is false when there is none or more than one.
func typeInfo(e *jsonschema.ValidationError, at []string) (want []string, got string, ok bool) {
	var found []*kind.Type
	var walk func(*jsonschema.ValidationError)
	walk = func(x *jsonschema.ValidationError) {
		if x == nil {
			return
		}
		if k, isType := x.ErrorKind.(*kind.Type); isType && reflect.DeepEqual(nilToEmpty(x.InstanceLocation), nilToEmpty(at)) {
			found = append(found, k)
		}
		for _, c := range x.Causes {
			walk(c)
		}
	}
	walk(e)
	if len(found) != 1 {
		return nil, "", false
	}
	return found[0].Want, found[0].Got, true
}

// additionalNames collects the rejected property names from the single kind.AdditionalProperties
// node at the given instance location. Returns nil when there is none or more than one.
func additionalNames(e *jsonschema.ValidationError, at []string) []string {
	var found [][]string
	var walk func(*jsonschema.ValidationError)
	walk = func(x *jsonschema.ValidationError) {
		if x == nil {
			return
		}
		if k, ok := x.ErrorKind.(*kind.AdditionalProperties); ok && reflect.DeepEqual(nilToEmpty(x.InstanceLocation), nilToEmpty(at)) {
			found = append(found, k.Properties)
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
