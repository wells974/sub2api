package basispoints

import (
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// ArgumentObservation contains no argument values, schema contents, or raw
// validator errors. Path is a conservative JSON Pointer: dynamic/indirect
// properties are redacted when not identifiable in the direct schema.
type ArgumentObservation struct {
	Tool          string   `json:"tool"`
	Path          string   `json:"path"`
	Keyword       string   `json:"keyword"`
	Reason        string   `json:"reason"`
	Missing       []string `json:"missing,omitempty"`
	ExpectedTypes []string `json:"expected_types,omitempty"`
	ActualType    string   `json:"actual_type,omitempty"`
}

// repairArgumentObservation adapts the upstream diagnostic to the fork's
// internal correction contract. The upstream extractor alone chooses the failed
// rule and path: union schemas must never be reduced to an arbitrary branch.
func repairArgumentObservation(d *ToolArgumentsDiagnostic, validation *jsonschema.ValidationError) *ArgumentObservation {
	diagnostic := &ArgumentObservation{Tool: d.Tool, Reason: d.Reason, Path: d.Path, Keyword: d.Rule}
	if validation != nil && d.Reason == "schema_validation" {
		switch failure := validation.ErrorKind.(type) {
		case *kind.Required:
			for i, name := range failure.Missing {
				if i == 8 {
					break
				}
				diagnostic.Missing = append(diagnostic.Missing, safeSchemaName(name))
			}
		case *kind.Type:
			diagnostic.ActualType = failure.Got
			diagnostic.ExpectedTypes = append([]string(nil), failure.Want...)
		}
	}
	return diagnostic
}

// A failed correction keeps the same canonical diagnostic on the JSON/SSE path.
func (d *ArgumentObservation) toolArgumentsDiagnostic() *ToolArgumentsDiagnostic {
	if d == nil {
		return nil
	}
	return &ToolArgumentsDiagnostic{Tool: d.Tool, Reason: d.Reason, Path: d.Path, Rule: d.Keyword}
}
