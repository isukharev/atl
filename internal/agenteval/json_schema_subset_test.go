package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"testing"
)

func TestValidateJSONSchemaSubsetInstanceParity(t *testing.T) {
	validate := validateJSONSchemaSubsetInstance
	tests := []struct {
		name     string
		schema   string
		instance string
		wantErr  string
	}{
		{name: "empty schema", schema: `{}`, instance: `{}`},
		{name: "malformed schema JSON", schema: `{`, instance: `{}`, wantErr: "decode schema: unexpected EOF"},
		{name: "trailing schema JSON", schema: `{} {}`, instance: `{}`, wantErr: "decode schema: trailing JSON data"},
		{name: "malformed instance JSON", schema: `{}`, instance: `{`, wantErr: "decode instance: unexpected EOF"},
		{name: "trailing instance JSON", schema: `{}`, instance: `{} {}`, wantErr: "decode instance: trailing JSON data"},
		{name: "non-object schema node", schema: `[]`, instance: `null`, wantErr: "/: schema node is not an object"},
		{name: "single type", schema: `{"type":"string"}`, instance: `"value"`},
		{name: "single type mismatch", schema: `{"type":"string"}`, instance: `1`, wantErr: "/: value type number does not match [string]"},
		{name: "union string", schema: `{"type":["null","string"]}`, instance: `"value"`},
		{name: "union null", schema: `{"type":["null","string"]}`, instance: `null`},
		{name: "union mismatch", schema: `{"type":["null","string"]}`, instance: `false`, wantErr: "/: value type boolean does not match [null string]"},
		{name: "union contains non-string", schema: `{"type":["string",1]}`, instance: `"value"`, wantErr: "/: schema type union contains a non-string"},
		{name: "invalid type shape", schema: `{"type":1}`, instance: `1`, wantErr: "/: schema type is neither a string nor an array"},
		{name: "integer literal", schema: `{"type":"integer"}`, instance: `1`},
		{name: "integer decimal notation", schema: `{"type":"integer"}`, instance: `1.0`},
		{name: "integer exponent notation", schema: `{"type":"integer"}`, instance: `1e3`},
		{name: "fractional number is not integer", schema: `{"type":"integer"}`, instance: `1.5`, wantErr: "/: value type number does not match [integer]"},
		{name: "number", schema: `{"type":"number"}`, instance: `1.5`},
		{name: "boolean", schema: `{"type":"boolean"}`, instance: `true`},
		{name: "boolean mismatch", schema: `{"type":"boolean"}`, instance: `"true"`, wantErr: "/: value type string does not match [boolean]"},
		{name: "enum match", schema: `{"enum":["red","blue"]}`, instance: `"blue"`},
		{name: "enum mismatch", schema: `{"enum":["red","blue"]}`, instance: `"green"`, wantErr: "/: value is outside enum"},
		{name: "required present", schema: `{"type":"object","required":["name"]}`, instance: `{"name":"value"}`},
		{name: "required missing", schema: `{"type":"object","required":["name"]}`, instance: `{}`, wantErr: "/: missing required property \"name\""},
		{name: "required non-string entry", schema: `{"type":"object","required":[1]}`, instance: `{}`, wantErr: "/: required member is not a string"},
		{name: "additional properties allowed by default", schema: `{"type":"object"}`, instance: `{"extra":true}`},
		{name: "additional properties explicitly allowed", schema: `{"type":"object","additionalProperties":true}`, instance: `{"extra":true}`},
		{name: "additional properties rejected", schema: `{"type":"object","additionalProperties":false}`, instance: `{"extra":true}`, wantErr: "/: unexpected property \"extra\""},
		{
			name:     "nested object path",
			schema:   `{"type":"object","properties":{"outer":{"type":"object","properties":{"leaf":{"type":"integer"}}}}}`,
			instance: `{"outer":{"leaf":"value"}}`,
			wantErr:  "/outer/leaf: value type string does not match [integer]",
		},
		{
			name:     "nested array path",
			schema:   `{"type":"object","properties":{"items":{"type":"array","items":{"type":"integer"}}}}`,
			instance: `{"items":[1,"value"]}`,
			wantErr:  "/items/1: value type string does not match [integer]",
		},
		{name: "unknown schema type", schema: `{"type":"mystery"}`, instance: `"value"`, wantErr: "/: value type string does not match [mystery]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validate([]byte(test.schema), []byte(test.instance))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate succeeded; want error %q", test.wantErr)
			}
			if got := err.Error(); got != test.wantErr {
				t.Fatalf("error = %q; want %q", got, test.wantErr)
			}
		})
	}
}

func validateJSONSchemaSubsetInstance(schemaBytes, instanceBytes []byte) error {
	var schema, instance any
	if err := decodeJSONDocument(schemaBytes, &schema); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	if err := decodeJSONDocument(instanceBytes, &instance); err != nil {
		return fmt.Errorf("decode instance: %w", err)
	}
	return validateJSONSchemaSubsetNode(schema, instance, "")
}

func decodeJSONDocument(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func validateJSONSchemaSubsetNode(rawSchema, value any, path string) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node is not an object", jsonSchemaSubsetPath(path))
	}
	types, err := jsonSchemaSubsetTypes(schema["type"])
	if err != nil {
		return fmt.Errorf("%s: %w", jsonSchemaSubsetPath(path), err)
	}
	if len(types) > 0 {
		matched := false
		for _, candidate := range types {
			if jsonSchemaSubsetTypeMatches(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value type %s does not match %v", jsonSchemaSubsetPath(path), jsonSchemaSubsetValueType(value), types)
		}
	}
	if value == nil {
		return nil
	}

	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is outside enum", jsonSchemaSubsetPath(path))
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name, ok := rawName.(string)
				if !ok {
					return fmt.Errorf("%s: required member is not a string", jsonSchemaSubsetPath(path))
				}
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s: missing required property %q", jsonSchemaSubsetPath(path), name)
				}
			}
		}
		additional, hasAdditional := schema["additionalProperties"].(bool)
		for name, child := range typed {
			childSchema, exists := properties[name]
			if !exists {
				if hasAdditional && !additional {
					return fmt.Errorf("%s: unexpected property %q", jsonSchemaSubsetPath(path), name)
				}
				continue
			}
			if err := validateJSONSchemaSubsetNode(childSchema, child, path+"/"+name); err != nil {
				return err
			}
		}
	case []any:
		itemSchema, exists := schema["items"]
		if !exists {
			return nil
		}
		for index, child := range typed {
			if err := validateJSONSchemaSubsetNode(itemSchema, child, fmt.Sprintf("%s/%d", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonSchemaSubsetTypes(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, value := range typed {
			name, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("schema type union contains a non-string")
			}
			out = append(out, name)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("schema type is neither a string nor an array")
	}
}

func jsonSchemaSubsetTypeMatches(schemaType string, value any) bool {
	switch schemaType {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		if _, err := number.Int64(); err == nil {
			return true
		}
		parsed, err := number.Float64()
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) && math.Trunc(parsed) == parsed
	case "number":
		_, ok := value.(json.Number)
		return ok
	default:
		return false
	}
}

func jsonSchemaSubsetValueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func jsonSchemaSubsetPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
