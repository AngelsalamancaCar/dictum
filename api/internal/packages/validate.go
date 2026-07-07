package packages

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validate checks dataJSON against the JSON Schema in schemaJSON (a
// package's stored output_schema, per plan.md §5). A nil/empty errs slice
// with a nil error means dataJSON is valid; a non-nil error means schemaJSON
// or dataJSON itself is malformed, which is distinct from a validation
// failure.
func Validate(schemaJSON, dataJSON []byte) (errs []string, err error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("output_schema.json", bytes.NewReader(schemaJSON)); err != nil {
		return nil, fmt.Errorf("invalid output_schema: %w", err)
	}
	schema, err := compiler.Compile("output_schema.json")
	if err != nil {
		return nil, fmt.Errorf("invalid output_schema: %w", err)
	}

	var data any
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		return nil, fmt.Errorf("invalid response JSON: %w", err)
	}

	if err := schema.Validate(data); err != nil {
		ve, ok := err.(*jsonschema.ValidationError)
		if !ok {
			return []string{err.Error()}, nil
		}
		return flattenValidationError(ve), nil
	}
	return nil, nil
}

// flattenValidationError walks a jsonschema.ValidationError's cause tree and
// returns one human-readable line per leaf failure (the root error is just
// "jsonschema validation failed" and carries no useful detail on its own).
func flattenValidationError(ve *jsonschema.ValidationError) []string {
	if len(ve.Causes) == 0 {
		loc := ve.InstanceLocation
		if loc == "" {
			loc = "(root)"
		}
		return []string{fmt.Sprintf("%s: %s", loc, ve.Message)}
	}
	var out []string
	for _, cause := range ve.Causes {
		out = append(out, flattenValidationError(cause)...)
	}
	return out
}
