package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// providerResponseSchema keeps the retained response schema authoritative while
// adapting only assertions that a provider cannot represent. Every removed
// assertion must have a stronger local deterministic check over the same value.
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
