package validation

import (
	"fmt"
	"strings"

	verrors "github.com/pb33f/libopenapi-validator/errors"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	d "humbug/internal/diagnostics"
)

// headerDecl is a header the specification declares for a request (a header parameter) or a
// response (a response header).
type headerDecl struct {
	name   string // as declared in the specification
	schema *base.SchemaProxy
	array  bool // the schema is an array, so repeated lines are legitimate
}

func declOf(name string, sp *base.SchemaProxy) headerDecl {
	decl := headerDecl{name: name, schema: sp}
	if sp != nil {
		if s := sp.Schema(); s != nil {
			for _, t := range s.Type {
				if t == "array" {
					decl.array = true
				}
			}
		}
	}
	return decl
}

// requestHeaderDecls lists header parameters, keyed by lower-cased name. Operation-level
// parameters override path-level ones with the same name.
func requestHeaderDecls(pi *v3.PathItem, op *v3.Operation) map[string]headerDecl {
	out := map[string]headerDecl{}
	var lists [][]*v3.Parameter
	if pi != nil {
		lists = append(lists, pi.Parameters)
	}
	if op != nil {
		lists = append(lists, op.Parameters)
	}
	for _, params := range lists {
		for _, p := range params {
			if p != nil && p.In == "header" {
				out[strings.ToLower(p.Name)] = declOf(p.Name, p.Schema)
			}
		}
	}
	return out
}

func responseHeaderDecls(op *v3.Operation, status int) map[string]headerDecl {
	out := map[string]headerDecl{}
	if resp := responseFor(op, status); resp != nil && resp.Headers != nil {
		for name, h := range resp.Headers.FromOldest() {
			if h != nil {
				out[strings.ToLower(name)] = declOf(name, h.Schema)
			}
		}
	}
	return out
}

// hname returns the name as declared in the specification when known (the library's own
// spelling varies: it lower-cases some response header names), else the name it reported.
func (n *normalizer) hname(name string) string {
	if decl, ok := n.decls[strings.ToLower(name)]; ok {
		return decl.name
	}
	return name
}

// firstLine returns the first value sent for a header (case-insensitive) and whether any line exists.
func firstLine(headers []Header, name string) (string, bool) {
	for _, h := range headers {
		if strings.EqualFold(h[0], name) {
			return h[1], true
		}
	}
	return "", false
}

// warnRepeated reports headers sent on more than one line when repeating is not legitimate: a
// header the specification declares as a single value, or Content-Type. Set-Cookie and Cookie
// are exempt (their lines are never combined). Nothing is joined or dropped; this states a fact
// about the input, whichever line the library validated.
func (n *normalizer) warnRepeated(headers []Header) {
	count := map[string]int{}
	var order []string
	for _, h := range headers {
		k := strings.ToLower(h[0])
		if count[k] == 0 {
			order = append(order, k)
		}
		count[k]++
	}
	for _, k := range order {
		// Set-Cookie lines are never combined. (Request cookies are `in: cookie` parameters, not
		// header declarations, so repeated Cookie lines never reach this rule.)
		if count[k] < 2 || k == "set-cookie" {
			continue
		}
		name := ""
		if decl, ok := n.decls[k]; ok {
			if decl.array {
				continue
			}
			name = decl.name
		} else if k == "content-type" {
			name = "Content-Type"
		} else {
			continue // undeclared: Humbug has no opinion
		}
		n.add(d.Diagnostic{Code: d.CodeHeaderRepeated, Severity: d.SeverityWarning,
			Instance: &d.Instance{Location: "header", Name: name},
			Detail:   map[string]string{"lines": fmt.Sprint(count[k])},
			Message:  fmt.Sprintf("header %q was sent on %d lines but is a single-valued header", name, count[k])})
	}
}

// emptyHeaderParams handles request header parameters sent with an empty value. The library
// treats an empty value as absent, which is wrong in both directions: a required header whose
// schema allows "" is reported missing, and a schema that forbids "" is never checked. For those
// headers Humbug discards the library's finding and judges the empty string itself. Array and
// object schemas are left to the library. It returns the remaining library errors.
func (e *Engine) emptyHeaderParams(n *normalizer, headers []Header, errs []*verrors.ValidationError) []*verrors.ValidationError {
	for key, decl := range n.decls {
		if v, sent := firstLine(headers, key); !sent || v != "" {
			continue
		}
		s := decl.schema
		if s == nil || s.Schema() == nil {
			continue
		}
		sch := s.Schema()
		var conforms bool
		switch {
		case hasType(sch, "array") || hasType(sch, "object"):
			continue
		case hasType(sch, "string") || len(sch.Type) == 0:
			ok, _ := e.sv.ValidateSchemaObject(sch, "")
			conforms = ok
		default:
			conforms = false // "" is not a valid number, integer, boolean or null
		}
		kept := errs[:0:0]
		for _, ve := range errs {
			if ve.ValidationType == "parameter" && ve.ValidationSubType == "header" && strings.EqualFold(ve.ParameterName, key) {
				continue // the library's "missing" claim is false: the header was sent
			}
			kept = append(kept, ve)
		}
		errs = kept
		if !conforms {
			n.add(d.Diagnostic{Code: d.CodeParameterInvalid, Instance: &d.Instance{Location: "header", Name: decl.name},
				Detail:  map[string]string{"empty": "true"},
				Message: fmt.Sprintf("header %q was sent empty, which its schema does not allow", decl.name)})
		}
	}
	return errs
}

func hasType(s *base.Schema, t string) bool {
	for _, x := range s.Type {
		if x == t {
			return true
		}
	}
	return false
}
