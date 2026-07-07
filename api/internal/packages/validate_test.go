package packages

import "testing"

const classifySchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "classify_output",
  "type": "object",
  "required": ["case_type", "confidence", "evidence"],
  "properties": {
    "case_type": { "type": "string" },
    "confidence": { "type": "string", "enum": ["alto", "medio", "bajo"] },
    "evidence": {
      "type": "array",
      "items": { "type": "string" },
      "minItems": 1
    }
  },
  "additionalProperties": false
}`

func TestValidate_Valid(t *testing.T) {
	data := `{"case_type": "despido injustificado", "confidence": "alto", "evidence": ["cita 1"]}`
	errs, err := Validate([]byte(classifySchema), []byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no validation errors, got %v", errs)
	}
}

func TestValidate_MissingRequiredField(t *testing.T) {
	data := `{"case_type": "despido injustificado", "confidence": "alto"}`
	errs, err := Validate([]byte(classifySchema), []byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected validation errors for missing 'evidence'")
	}
}

func TestValidate_WrongEnumValue(t *testing.T) {
	data := `{"case_type": "x", "confidence": "muy alto", "evidence": ["cita"]}`
	errs, err := Validate([]byte(classifySchema), []byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected validation errors for invalid confidence enum value")
	}
}

func TestValidate_AdditionalPropertyRejected(t *testing.T) {
	data := `{"case_type": "x", "confidence": "alto", "evidence": ["cita"], "extra": true}`
	errs, err := Validate([]byte(classifySchema), []byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected validation errors for additional property")
	}
}

func TestValidate_MalformedResponseJSON(t *testing.T) {
	_, err := Validate([]byte(classifySchema), []byte(`not json`))
	if err == nil {
		t.Fatal("expected an error for malformed response JSON")
	}
}

func TestValidate_MalformedSchema(t *testing.T) {
	_, err := Validate([]byte(`not json`), []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error for malformed schema JSON")
	}
}
