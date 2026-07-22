package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
)

// providerResponseSchema keeps the retained response schema authoritative while
// adapting only constraints that a provider cannot represent. Every removed
// assertion must have a stronger local deterministic check over the same value,
// and every added assertion must already be logically implied by the original.
func providerResponseSchema(spec RunSpec, original []byte) ([]byte, error) {
	if !json.Valid(original) {
		return nil, fmt.Errorf("response schema is not valid JSON")
	}
	if spec.Provider != "codex" {
		return append([]byte(nil), original...), nil
	}
	if err := validateJSONNoDuplicateKeys(original); err != nil {
		return nil, fmt.Errorf("response schema is invalid: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil || schema == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("response schema must be one JSON object")
	}
	changed := false
	if err := projectCodexSchemaNode(schema, "", spec.Checks, false, &changed); err != nil {
		return nil, err
	}
	if !changed {
		return append([]byte(nil), original...), nil
	}
	projected, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode provider response schema: %w", err)
	}
	return projected, nil
}

func projectCodexSchemaNode(schema map[string]any, pointer string, checks []RunCheck, ambiguous bool, changed *bool) error {
	if constant, present := schema["const"]; present {
		inferred, integer, scalar := scalarConstSchemaType(constant)
		if !scalar {
			return fmt.Errorf("codex response schema const at %q must be scalar", pointer)
		}
		if rawType, typed := schema["type"]; typed {
			if !schemaTypeAllowsConst(rawType, inferred, integer) {
				return fmt.Errorf("codex response schema const at %q conflicts with its type", pointer)
			}
		} else {
			schema["type"] = inferred
			*changed = true
		}
	}
	for _, keyword := range []string{
		"allOf", "oneOf", "not", "dependentRequired", "dependentSchemas", "dependencies",
		"if", "then", "else", "patternProperties", "propertyNames", "unevaluatedProperties",
		"prefixItems", "contains", "minContains", "maxContains", "unevaluatedItems",
		"additionalItems", "contentSchema", "$dynamicRef", "$recursiveRef",
	} {
		if _, present := schema[keyword]; present {
			return fmt.Errorf("codex response schema uses unsupported structural keyword %q", keyword)
		}
	}
	if raw, present := schema["additionalProperties"]; present {
		allowed, ok := raw.(bool)
		if !ok || allowed {
			return fmt.Errorf("codex response schema additionalProperties must be false")
		}
	}
	if raw, present := schema["uniqueItems"]; present {
		value, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("codex response schema uniqueItems must be boolean")
		}
		if value && (ambiguous || !hasExactUniqueArrayCheck(checks, pointer)) {
			return fmt.Errorf("codex response schema uniqueItems at %q requires an exact local json_equals check", pointer)
		}
		delete(schema, "uniqueItems")
		*changed = true
	}
	if raw, present := schema["properties"]; present {
		properties, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("codex response schema properties must be an object")
		}
		for name, rawProperty := range properties {
			property, ok := rawProperty.(map[string]any)
			if !ok {
				return fmt.Errorf("codex response schema property %q must be an object", name)
			}
			if err := projectCodexSchemaNode(property, pointer+"/"+escapeJSONPointerToken(name), checks, ambiguous, changed); err != nil {
				return err
			}
		}
	}
	if raw, present := schema["items"]; present {
		item, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("codex response schema items must be an object")
		}
		if err := projectCodexSchemaNode(item, pointer+"/*", checks, true, changed); err != nil {
			return err
		}
	}
	if raw, present := schema["anyOf"]; present {
		variants, ok := raw.([]any)
		if !ok || len(variants) == 0 {
			return fmt.Errorf("codex response schema anyOf must be a non-empty array")
		}
		for _, rawVariant := range variants {
			variant, ok := rawVariant.(map[string]any)
			if !ok {
				return fmt.Errorf("codex response schema anyOf entries must be objects")
			}
			if err := projectCodexSchemaNode(variant, pointer, checks, true, changed); err != nil {
				return err
			}
		}
	}
	for _, definitionsKey := range []string{"$defs", "definitions"} {
		if raw, present := schema[definitionsKey]; present {
			definitions, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("codex response schema %s must be an object", definitionsKey)
			}
			for name, rawDefinition := range definitions {
				definition, ok := rawDefinition.(map[string]any)
				if !ok {
					return fmt.Errorf("codex response schema definition %q must be an object", name)
				}
				if err := projectCodexSchemaNode(definition, pointer, checks, true, changed); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func scalarConstSchemaType(value any) (schemaType string, integer bool, ok bool) {
	switch value := value.(type) {
	case nil:
		return "null", false, true
	case bool:
		return "boolean", false, true
	case string:
		return "string", false, true
	case json.Number:
		return "number", jsonNumberIsInteger(value), true
	default:
		return "", false, false
	}
}

func jsonNumberIsInteger(value json.Number) bool {
	text := value.String()
	if !strings.ContainsAny(text, ".eE") {
		return true
	}
	var number big.Rat
	if _, ok := number.SetString(text); !ok {
		return false
	}
	return number.IsInt()
}

func schemaTypeAllowsConst(rawType any, inferred string, integer bool) bool {
	allows := func(candidate string) bool {
		return candidate == inferred || inferred == "number" && integer && candidate == "integer"
	}
	validType := func(candidate string) bool {
		switch candidate {
		case "null", "boolean", "object", "array", "number", "string", "integer":
			return true
		default:
			return false
		}
	}
	switch value := rawType.(type) {
	case string:
		return validType(value) && allows(value)
	case []any:
		if len(value) == 0 {
			return false
		}
		matched := false
		seen := map[string]bool{}
		for _, raw := range value {
			candidate, ok := raw.(string)
			if !ok || !validType(candidate) || seen[candidate] {
				return false
			}
			seen[candidate] = true
			if allows(candidate) {
				matched = true
			}
		}
		return matched
	}
	return false
}

func hasExactUniqueArrayCheck(checks []RunCheck, pointer string) bool {
	if pointer == "" || strings.Contains(pointer, "/*") {
		return false
	}
	for _, check := range checks {
		if check.Kind != "json_equals" || check.Pointer != pointer {
			continue
		}
		var values []json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(check.Expected))
		if err := decoder.Decode(&values); err != nil || values == nil || decoder.Decode(new(any)) != io.EOF {
			return false
		}
		seen := make([]any, 0, len(values))
		for _, value := range values {
			// Match evaluateRunChecks semantics. Standard JSON decoding also makes
			// mathematically equal spellings such as 1/1.0 and -0/0 equal. Loss of
			// precision can only reject a safe projection, never admit a weak one.
			var semanticValue any
			if err := json.Unmarshal(value, &semanticValue); err != nil {
				return false
			}
			for _, previous := range seen {
				if reflect.DeepEqual(previous, semanticValue) {
					return false
				}
			}
			seen = append(seen, semanticValue)
		}
		return true
	}
	return false
}

func escapeJSONPointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}
