package diagnostics

import "testing"

func typeDiag(id, ptr string, want []string, got string) Diagnostic {
	return Diagnostic{ID: id, Code: "schema.type", Severity: SeverityError, Side: SideRequest,
		Instance: &Instance{Location: "body", Pointer: ptr}, Expected: want, Actual: got}
}

func diag(id, code, ptr string) Diagnostic {
	return Diagnostic{ID: id, Code: code, Severity: SeverityError, Side: SideRequest, Instance: &Instance{Location: "body", Pointer: ptr}}
}

func byID(s *Side, id string) *Diagnostic {
	for i := range s.Diagnostics {
		if s.Diagnostics[i].ID == id {
			return &s.Diagnostics[i]
		}
	}
	return nil
}

func suppressedBy(s *Side, id string) string {
	if d := byID(s, id); d != nil && d.SuppressedBy != nil {
		return *d.SuppressedBy
	}
	return ""
}

func TestWrongTypeSuppressesOnlyContingentFindings(t *testing.T) {
	s := &Side{Diagnostics: []Diagnostic{
		typeDiag("a", "/profile", []string{"object"}, "string"),
		diag("b", "schema.required", "/profile"),              // object keyword at the same pointer
		diag("c", "schema.type", "/profile/bio"),              // below the mismatched value
		diag("d", "schema.type", "/age"),                      // independent sibling
		diag("e", "schema.minLength", "/profile"),             // not an object keyword: stays visible
		diag("f", "schema.required", "/profileX"),             // shares a string prefix, not a path
		diag("g", "schema.additional_properties", "/profile"), // object keyword
	}}
	Relate(s, nil)

	for _, id := range []string{"b", "c", "g"} {
		if got := suppressedBy(s, id); got != "a" {
			t.Errorf("%s: suppressed_by %q, want a", id, got)
		}
		if cb := byID(s, id).CausedBy; len(cb) != 1 || cb[0] != "a" {
			t.Errorf("%s: caused_by %v", id, cb)
		}
	}
	for _, id := range []string{"a", "d", "e", "f"} {
		if got := suppressedBy(s, id); got != "" {
			t.Errorf("%s must stay visible, suppressed by %q", id, got)
		}
	}
}

func TestWrongTypeIsConservative(t *testing.T) {
	// Integer satisfies number: not a mismatch. Unknown expectations never suppress.
	s := &Side{Diagnostics: []Diagnostic{
		typeDiag("a", "/x", []string{"number"}, "integer"),
		diag("b", "schema.required", "/x"),
		{ID: "c", Code: "schema.type", Severity: SeverityError, Instance: &Instance{Location: "body", Pointer: "/y"}}, // no expected/actual
		diag("d", "schema.required", "/y"),
	}}
	Relate(s, nil)
	if suppressedBy(s, "b") != "" || suppressedBy(s, "d") != "" {
		t.Errorf("suppressed without a demonstrable mismatch: %+v", s.Diagnostics)
	}
}

func TestWrongTypeStaysInsideItsCompositionBranch(t *testing.T) {
	zero, one := 0, 1
	ty := typeDiag("a", "", []string{"object"}, "string")
	ty.Parent, ty.Branch = "p", &zero
	same := diag("b", "schema.required", "")
	same.Parent, same.Branch = "p", &zero
	other := diag("c", "schema.required", "")
	other.Parent, other.Branch = "p", &one
	top := diag("d", "schema.required", "")
	s := &Side{Diagnostics: []Diagnostic{ty, same, other, top}}
	Relate(s, nil)
	if suppressedBy(s, "b") != "a" {
		t.Errorf("same branch should be suppressed")
	}
	if suppressedBy(s, "c") != "" || suppressedBy(s, "d") != "" {
		t.Errorf("other branch or top level must not be touched")
	}
}

func TestWrongTypeArray(t *testing.T) {
	s := &Side{Diagnostics: []Diagnostic{
		typeDiag("a", "/ids", []string{"array"}, "string"),
		diag("b", "schema.minItems", "/ids"),
		diag("c", "schema.required", "/ids"), // an object keyword is not contingent on array-ness
	}}
	Relate(s, nil)
	if suppressedBy(s, "b") != "a" || suppressedBy(s, "c") != "" {
		t.Errorf("array family: %+v", s.Diagnostics)
	}
}

func routing(side, id, code string) Diagnostic {
	return Diagnostic{ID: id, Code: code, Side: side, Severity: SeverityError, Instance: &Instance{Location: "operation"}}
}

func TestRoutingFindingSuppressesResponseNote(t *testing.T) {
	req := &Side{Diagnostics: []Diagnostic{routing(SideRequest, "r1", CodeOperationNotFound)}}
	note := Diagnostic{ID: "s1", Code: CodeIncomplete, Side: SideResponse, Severity: SeverityInfo, Reason: CodeOperationNotFound}
	res := &Side{Diagnostics: []Diagnostic{note}}
	Relate(req, res)
	if suppressedBy(res, "s1") != "r1" || suppressedBy(req, "r1") != "" {
		t.Errorf("req=%+v res=%+v", req.Diagnostics, res.Diagnostics)
	}
	if cb := res.Diagnostics[0].CausedBy; len(cb) != 1 || cb[0] != "r1" {
		t.Errorf("caused_by %v", cb)
	}
}

func TestRoutingNoteStandsAloneWithoutRequestFinding(t *testing.T) {
	note := Diagnostic{ID: "s1", Code: CodeIncomplete, Side: SideResponse, Severity: SeverityInfo, Reason: CodeOperationNotFound}
	res := &Side{Diagnostics: []Diagnostic{note}}
	Relate(nil, res) // request validation disabled
	if suppressedBy(res, "s1") != "" {
		t.Errorf("response note must stay visible when there is no request-side primary")
	}
	// A different routing cause is not matched.
	req := &Side{Diagnostics: []Diagnostic{routing(SideRequest, "r1", CodeMethodNotAllowed)}}
	res2 := &Side{Diagnostics: []Diagnostic{note}}
	Relate(req, res2)
	if suppressedBy(res2, "s1") != "" {
		t.Errorf("mismatched causes must not be related")
	}
}

func TestAssignIDsRemapsRelationshipsAcrossSides(t *testing.T) {
	req := &Side{Diagnostics: []Diagnostic{routing(SideRequest, "req-t1", CodeOperationNotFound)}}
	note := Diagnostic{ID: "res-t1", Code: CodeIncomplete, Side: SideResponse, Severity: SeverityInfo, Reason: CodeOperationNotFound}
	res := &Side{Diagnostics: []Diagnostic{note}}
	Relate(req, res)
	AssignIDs(req, res)
	if res.Diagnostics[0].ID != "d2" || res.Diagnostics[0].SuppressedBy == nil || *res.Diagnostics[0].SuppressedBy != "d1" {
		t.Errorf("%+v", res.Diagnostics[0])
	}
}
