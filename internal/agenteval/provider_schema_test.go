package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderResponseSchemaProjectsCodexUniqueItemsWithExactLocalCheck(t *testing.T) {
	spec := validRunSpec()
	spec.Checks = append(spec.Checks, RunCheck{
		Name:     "labels_exact",
		Kind:     "json_equals",
		Pointer:  "/labels",
		Expected: json.RawMessage(`["alpha","beta"]`),
	})
	original := []byte(`{"type":"object","properties":{"labels":{"type":"array","items":{"type":"string"},"uniqueItems":true}},"required":["labels"],"additionalProperties":false}`)
	originalCopy := bytes.Clone(original)

	projected, err := providerResponseSchema(spec, original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, originalCopy) {
		t.Fatal("projection mutated the retained response schema")
	}
	if bytes.Contains(projected, []byte("uniqueItems")) {
		t.Fatalf("projected schema retained unsupported uniqueItems: %s", projected)
	}
	var decoded map[string]any
	if err := json.Unmarshal(projected, &decoded); err != nil {
		t.Fatal(err)
	}
	properties := decoded["properties"].(map[string]any)
	labels := properties["labels"].(map[string]any)
	if labels["type"] != "array" || labels["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("projection changed the supported array contract: %#v", labels)
	}

	checks, err := evaluateRunChecks(spec.Checks, []byte(`{"answer":"ok","labels":["alpha","alpha"]}`), "", 0, 0, 0, 0, nil, 0, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if checks["labels_exact"] {
		t.Fatal("the local exact check accepted a duplicate array")
	}
}

func TestProviderResponseSchemaRequiresStrongUniqueItemsReplacement(t *testing.T) {
	tests := []struct {
		name     string
		pointer  string
		expected json.RawMessage
		schema   string
	}{
		{name: "missing check", schema: `{"type":"array","uniqueItems":true}`},
		{name: "non array expected", pointer: "/labels", expected: json.RawMessage(`"alpha"`), schema: `{"type":"object","properties":{"labels":{"type":"array","uniqueItems":true}}}`},
		{name: "duplicate expected", pointer: "/labels", expected: json.RawMessage(`["alpha","alpha"]`), schema: `{"type":"object","properties":{"labels":{"type":"array","uniqueItems":true}}}`},
		{name: "equivalent number expected", pointer: "/labels", expected: json.RawMessage(`[1,1.0]`), schema: `{"type":"object","properties":{"labels":{"type":"array","uniqueItems":true}}}`},
		{name: "signed zero expected", pointer: "/labels", expected: json.RawMessage(`[-0,0]`), schema: `{"type":"object","properties":{"labels":{"type":"array","uniqueItems":true}}}`},
		{name: "nested signed zero expected", pointer: "/labels", expected: json.RawMessage(`[{"value":-0},{"value":0}]`), schema: `{"type":"object","properties":{"labels":{"type":"array","uniqueItems":true}}}`},
		{name: "nested item", pointer: "/groups/*", expected: json.RawMessage(`["alpha"]`), schema: `{"type":"array","items":{"type":"array","uniqueItems":true}}`},
		{name: "definition pointer unknown", pointer: "/labels", expected: json.RawMessage(`["alpha"]`), schema: `{"type":"object","$defs":{"labels":{"type":"array","uniqueItems":true}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validRunSpec()
			if test.pointer != "" {
				spec.Checks = append(spec.Checks, RunCheck{Name: "exact", Kind: "json_equals", Pointer: test.pointer, Expected: test.expected})
			}
			_, err := providerResponseSchema(spec, []byte(test.schema))
			if err == nil || !strings.Contains(err.Error(), "requires an exact local json_equals check") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestProviderResponseSchemaRemovesNoOpUniqueItemsFalse(t *testing.T) {
	spec := validRunSpec()
	projected, err := providerResponseSchema(spec, []byte(`{"type":"array","uniqueItems":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(projected, []byte("uniqueItems")) {
		t.Fatalf("projected schema retained no-op uniqueItems: %s", projected)
	}
}

func TestProviderResponseSchemaEscapesPropertyPointer(t *testing.T) {
	spec := validRunSpec()
	spec.Checks = append(spec.Checks, RunCheck{Name: "exact", Kind: "json_equals", Pointer: "/a~1b~0c", Expected: json.RawMessage(`[1,2]`)})
	projected, err := providerResponseSchema(spec, []byte(`{"type":"object","properties":{"a/b~c":{"type":"array","uniqueItems":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(projected, []byte("uniqueItems")) {
		t.Fatalf("projected schema retained uniqueItems: %s", projected)
	}
}

func TestProviderResponseSchemaRejectsUnsupportedCodexStructure(t *testing.T) {
	for _, keyword := range []string{
		"allOf", "oneOf", "not", "dependentRequired", "dependentSchemas", "dependencies",
		"if", "then", "else", "patternProperties", "propertyNames", "unevaluatedProperties",
		"prefixItems", "contains", "minContains", "maxContains", "unevaluatedItems",
		"additionalItems", "contentSchema", "$dynamicRef", "$recursiveRef",
	} {
		t.Run(keyword, func(t *testing.T) {
			spec := validRunSpec()
			schema := []byte(`{"type":"object","` + keyword + `":{}}`)
			if keyword == "allOf" || keyword == "oneOf" || keyword == "prefixItems" {
				schema = []byte(`{"type":"object","` + keyword + `":[]}`)
			}
			_, err := providerResponseSchema(spec, schema)
			if err == nil || !strings.Contains(err.Error(), `unsupported structural keyword "`+keyword+`"`) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestProviderResponseSchemaRequiresClosedAdditionalProperties(t *testing.T) {
	for _, value := range []string{"true", `{"type":"string"}`} {
		spec := validRunSpec()
		_, err := providerResponseSchema(spec, []byte(`{"type":"object","additionalProperties":`+value+`}`))
		if err == nil || !strings.Contains(err.Error(), "additionalProperties must be false") {
			t.Fatalf("value=%s err=%v", value, err)
		}
	}
}

func TestProviderResponseSchemaLeavesCompatibleCodexBytesUnchanged(t *testing.T) {
	spec := validRunSpec()
	original := []byte("{\n  \"type\": \"object\",\n  \"additionalProperties\": false\n}\n")
	projected, err := providerResponseSchema(spec, original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, original) {
		t.Fatalf("compatible Codex schema changed:\n%s", projected)
	}
}

func TestProviderResponseSchemaRejectsDuplicateKeys(t *testing.T) {
	spec := validRunSpec()
	_, err := providerResponseSchema(spec, []byte(`{"type":"object","type":"array"}`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestProviderResponseSchemaLeavesClaudeBytesUnchanged(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	original := []byte("{\n  \"type\": \"array\",\n  \"uniqueItems\": true\n}\n")
	projected, err := providerResponseSchema(spec, original)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, original) {
		t.Fatalf("Claude schema changed:\n%s", projected)
	}
}
