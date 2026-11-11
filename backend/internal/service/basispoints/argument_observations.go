package basispoints

import (
	"errors"

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

func observedArgumentError(info tool, err error) error {
	diagnostic := &ArgumentObservation{Tool: safeSchemaName(info.Name), Reason: "resource_limit"}
	if info.Namespace != "" {
		diagnostic.Tool = safeSchemaName(info.Namespace + "." + info.Name)
	}
	var validation *jsonschema.ValidationError
	if errors.As(err, &validation) {
		diagnostic.Reason = "schema_validation"
		// Report the first leaf only. Union validation can have many branches;
		// do not log the tree or stringify errors (which embed argument values).
		for depth := 0; len(validation.Causes) > 0 && depth < 128; depth++ {
			validation = validation.Causes[0]
		}
		if validation.ErrorKind != nil {
			if keyword := validation.ErrorKind.KeywordPath(); len(keyword) > 0 {
				diagnostic.Keyword = safeSchemaName(keyword[0])
			}
		}
		switch failure := validation.ErrorKind.(type) {
		case *kind.Required:
			for i, name := range failure.Missing {
				if i == 8 {
					break
				}
				diagnostic.Missing = append(diagnostic.Missing, safeSchemaName(name))
			}
		case *kind.Type:
			// These are library-generated JSON type names, never values.
			diagnostic.ActualType = failure.Got
			diagnostic.ExpectedTypes = append([]string(nil), failure.Want...)
		}
		var path []string
		node := info.Parameters
		for _, part := range validation.InstanceLocation {
			properties, _ := node["properties"].(object)
			if property, exists := properties[part]; exists {
				path = append(path, safeSchemaName(part))
				node, _ = property.(object)
			} else {
				// Even numeric unknown property names may be user data. Redact
				// them as well; no instance values are needed for diagnostics.
				path = append(path, "[property]")
				node = nil
			}
			if len(path) == 128 {
				break
			}
		}
		diagnostic.Path = schemaPointer(path, true)
	}
	return toolArgumentsSchemaError{diagnostic: diagnostic}
}
