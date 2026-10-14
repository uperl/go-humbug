package diagnostics

import "strings"

// Relate records demonstrable relationships between diagnostics. Nothing is removed:
// a suppressed diagnostic keeps all of its fields and still counts toward has_violations
// (its primary is itself an error). Callers that present findings may hide diagnostics
// whose suppressed_by is set; machine consumers can ignore the field.
//
// Rules are deliberately narrow. Sharing a path or wording never suppresses anything.
//
//  1. Routing: when the operation cannot be matched, the response side's "could not be routed"
//     note is caused by the request side's routing finding. Without a request-side finding
//     (request validation disabled) it stands alone.
//  2. Wrong type: when a value has the wrong type for an object or array schema, findings that
//     only make sense inside that type (object keywords at the same pointer, array keywords at
//     the same pointer, anything below the pointer) are caused by the type finding. Sibling
//     properties are never touched, and neither is anything in a different composition branch.
//
// Call after Finish (so order is final) and before AssignIDs (it works on temporary IDs).
func Relate(req, res *Side) {
	for _, s := range []*Side{req, res} {
		if s != nil {
			relateWrongType(s)
		}
	}
	relateRouting(req, res)
}

var objectKeywords = map[string]bool{
	"required": true, "additional_properties": true, "properties": true, "patternProperties": true,
	"minProperties": true, "maxProperties": true, "propertyNames": true, "dependentRequired": true,
	"dependentSchemas": true, "unevaluatedProperties": true,
}

var arrayKeywords = map[string]bool{
	"items": true, "prefixItems": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"contains": true, "minContains": true, "maxContains": true, "unevaluatedItems": true,
}

func relateWrongType(s *Side) {
	for i := range s.Diagnostics {
		t := &s.Diagnostics[i]
		if t.Code != CodeSchemaPrefix+"type" || t.Instance == nil || t.Instance.Location != "body" || t.SuppressedBy != nil {
			continue
		}
		want, ok := t.Expected.([]string)
		if !ok {
			continue
		}
		got, _ := t.Actual.(string)
		if len(want) == 0 || got == "" || typeMatches(want, got) {
			continue // not a demonstrable mismatch; stay conservative
		}
		kws := map[string]bool{}
		for _, w := range want {
			switch w {
			case "object":
				for k := range objectKeywords {
					kws[k] = true
				}
			case "array":
				for k := range arrayKeywords {
					kws[k] = true
				}
			}
		}
		p := t.Instance.Pointer
		for j := range s.Diagnostics {
			dep := &s.Diagnostics[j]
			if j == i || dep.Instance == nil || dep.Instance.Location != "body" || dep.SuppressedBy != nil ||
				dep.Parent != t.Parent || !sameBranch(dep.Branch, t.Branch) {
				continue
			}
			dp := dep.Instance.Pointer
			below := strings.HasPrefix(dp, p+"/")
			here := dp == p && kws[strings.TrimPrefix(dep.Code, CodeSchemaPrefix)] && strings.HasPrefix(dep.Code, CodeSchemaPrefix)
			if below || here {
				id := t.ID
				dep.SuppressedBy = &id
				dep.CausedBy = append(dep.CausedBy, id)
			}
		}
	}
}

// typeMatches reports whether got satisfies any wanted JSON Schema type (an integer is a number).
func typeMatches(want []string, got string) bool {
	for _, w := range want {
		if w == got || (w == "number" && got == "integer") {
			return true
		}
	}
	return false
}

func sameBranch(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func relateRouting(req, res *Side) {
	if req == nil || res == nil {
		return
	}
	for i := range res.Diagnostics {
		x := &res.Diagnostics[i]
		if x.Code != CodeIncomplete || (x.Reason != CodeOperationNotFound && x.Reason != CodeMethodNotAllowed) {
			continue
		}
		for _, r := range req.Diagnostics {
			if r.Code == x.Reason && r.Severity == SeverityError {
				id := r.ID
				x.SuppressedBy = &id
				x.CausedBy = append(x.CausedBy, id)
				break
			}
		}
	}
}
