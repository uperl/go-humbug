// Package diagnostics defines the wire-level result and diagnostic types.
// These are independent of pb33f types; see docs/diagnostics.md.
package diagnostics

import (
	"sort"
	"strconv"
	"strings"
)

// Side status values.
const (
	StatusValid      = "valid"
	StatusInvalid    = "invalid"
	StatusIncomplete = "incomplete"
	StatusSkipped    = "skipped"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

const (
	SideRequest  = "request"
	SideResponse = "response"
)

// Stable diagnostic codes. Keep in sync with docs/diagnostics.md.
const (
	CodeOperationNotFound    = "operation.not_found"
	CodeMethodNotAllowed     = "operation.method_not_allowed"
	CodeParameterRequired    = "parameter.required"
	CodeParameterInvalid     = "parameter.invalid"
	CodeBodyMediaType        = "body.media_type"
	CodeBodyDecode           = "body.decode"
	CodeBodyRequired         = "body.required"
	CodeResponseStatus       = "response.status"
	CodeResponseHeaderReq    = "response.header.required"
	CodeSchemaOneOfNoMatch   = "schema.oneOf.no_match"
	CodeSchemaOneOfAmbiguous = "schema.oneOf.ambiguous"
	CodeSchemaAnyOf          = "schema.anyOf"
	CodeSchemaPrefix         = "schema."
	CodeResponseHeaderInv    = "response.header.invalid"
	CodeHeaderRepeated       = "header.repeated"
	CodeIncomplete           = "validation.incomplete"
	CodeUnclassified         = "validation.unclassified"
)

type Span struct {
	Start      int  `json:"start"`
	End        int  `json:"end"`
	Line       int  `json:"line"`
	Column     int  `json:"column"`
	EndLine    int  `json:"end_line"`
	EndColumn  int  `json:"end_column"`
	Contextual bool `json:"contextual,omitempty"` // span is the closest existing parent, not the offending token
	Ambiguous  bool `json:"ambiguous,omitempty"`  // duplicate member names made the mapping ambiguous
}

type Instance struct {
	Location string   `json:"location"`          // body|header|query|path|cookie|status|operation
	Name     string   `json:"name,omitempty"`    // parameter or header name
	Pointer  string   `json:"pointer,omitempty"` // RFC 6901, body only
	Span     *Span    `json:"span,omitempty"`    // body only
	Missing  []string `json:"missing,omitempty"` // absent property names, for schema.required
}

// SchemaRef locates the violated constraint. Pointer is relative to the root
// schema of the validated body (Relative is true); Line/Column locate that root
// schema in the specification source when the library resolved it. The file is
// not exposed by the library, so URI is omitted rather than guessed.
type SchemaRef struct {
	Pointer  string `json:"pointer,omitempty"`
	Relative bool   `json:"relative,omitempty"`
	URI      string `json:"uri,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

type Diagnostic struct {
	ID           string     `json:"id"`
	Code         string     `json:"code"`
	Severity     string     `json:"severity"`
	Side         string     `json:"side"`
	Message      string     `json:"message"`
	Instance     *Instance  `json:"instance,omitempty"`
	Schema       *SchemaRef `json:"schema,omitempty"`
	Expected     any        `json:"expected,omitempty"`
	Actual       any        `json:"actual,omitempty"`
	Related      []string   `json:"related,omitempty"`   // IDs, no causality implied
	CausedBy     []string   `json:"caused_by,omitempty"` // IDs, demonstrable causality
	Children     []string   `json:"children,omitempty"`  // IDs of grouped branch diagnostics (oneOf/anyOf)
	Parent       string     `json:"parent,omitempty"`    // ID of the composition diagnostic this belongs to
	SuppressedBy *string    `json:"suppressed_by"`
	Reason       string     `json:"reason,omitempty"` // for validation.incomplete: machine-readable cause
	Branch       *int       `json:"branch,omitempty"` // oneOf/anyOf branch index for grouped children
	// Detail holds original library classification (unclassified findings) or parser detail (body.decode).
	Detail map[string]string `json:"detail,omitempty"`
}

type Side struct {
	Status        string       `json:"status"`
	Complete      bool         `json:"complete"`
	HasViolations bool         `json:"has_violations"`
	Valid         bool         `json:"valid"` // complete && !has_violations
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

type Operation struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id,omitempty"`
}

type Result struct {
	Complete      bool       `json:"complete"`
	HasViolations bool       `json:"has_violations"`
	Valid         bool       `json:"valid"`
	Operation     *Operation `json:"operation,omitempty"`
	Request       *Side      `json:"request"`
	Response      *Side      `json:"response"`
}

var locOrder = map[string]int{"operation": 0, "path": 1, "query": 2, "header": 3, "cookie": 4, "body": 5, "status": 6}

// Finish sorts diagnostics deterministically and derives the
// status/complete/has_violations fields. incomplete marks a side whose
// requested checks could not all run.
func (s *Side) Finish(incomplete bool) {
	sort.SliceStable(s.Diagnostics, func(i, j int) bool {
		a, b := s.Diagnostics[i], s.Diagnostics[j]
		la, lb := locRank(a), locRank(b)
		if la != lb {
			return la < lb
		}
		if a.Instance != nil && b.Instance != nil {
			if a.Instance.Name != b.Instance.Name {
				return a.Instance.Name < b.Instance.Name
			}
			if a.Instance.Pointer != b.Instance.Pointer {
				return a.Instance.Pointer < b.Instance.Pointer
			}
		}
		// Schema pointer before code so a composition parent (/oneOf) precedes its
		// branch children (/oneOf/0/...).
		if sa, sb := schemaPtr(a), schemaPtr(b); sa != sb {
			return sa < sb
		}
		return a.Code < b.Code
	})
	s.HasViolations = false
	for i := range s.Diagnostics {
		d := &s.Diagnostics[i]
		if d.Severity == SeverityError {
			s.HasViolations = true
		}
	}
	s.Complete = !incomplete
	s.Valid = s.Complete && !s.HasViolations
	switch {
	case incomplete && !s.HasViolations:
		s.Status = StatusIncomplete
	case s.HasViolations:
		s.Status = StatusInvalid // incompleteness is still visible via Complete
	default:
		s.Status = StatusValid
	}
}

func locRank(d Diagnostic) int {
	if d.Instance == nil {
		return -1
	}
	return locOrder[d.Instance.Location]
}

func schemaPtr(d Diagnostic) string {
	if d.Schema == nil {
		return ""
	}
	return d.Schema.Pointer
}

// Skipped returns a side that was not requested.
func Skipped() *Side {
	return &Side{Status: StatusSkipped, Complete: true, Diagnostics: []Diagnostic{}}
}

// AssignIDs replaces the temporary IDs given by normalizers with
// transaction-local d1, d2, ... in final order, remapping all references.
// Call after Finish on every side.
func AssignIDs(sides ...*Side) {
	remap := map[string]string{}
	n := 0
	for _, s := range sides {
		if s == nil {
			continue
		}
		for i := range s.Diagnostics {
			n++
			id := "d" + strconv.Itoa(n)
			remap[s.Diagnostics[i].ID] = id
			s.Diagnostics[i].ID = id
		}
	}
	fix := func(ids []string) {
		for i, id := range ids {
			if v, ok := remap[id]; ok {
				ids[i] = v
			}
		}
		sort.Slice(ids, func(i, j int) bool { return idNum(ids[i]) < idNum(ids[j]) })
	}
	for _, s := range sides {
		if s == nil {
			continue
		}
		for i := range s.Diagnostics {
			d := &s.Diagnostics[i]
			fix(d.Related)
			fix(d.CausedBy)
			fix(d.Children)
			if v, ok := remap[d.Parent]; ok {
				d.Parent = v
			}
			if d.SuppressedBy != nil {
				if v, ok := remap[*d.SuppressedBy]; ok {
					d.SuppressedBy = &v
				}
			}
		}
	}
}

func idNum(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "d"))
	return n
}
