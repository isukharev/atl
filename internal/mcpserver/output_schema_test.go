package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type outputSchemaTestInput struct {
	Invalid bool `json:"invalid,omitempty"`
}

type outputSchemaTestFirst struct {
	SchemaVersion int `json:"schema_version"`
	First         any `json:"first"`
}

type outputSchemaTestSecond struct {
	SchemaVersion int    `json:"schema_version"`
	Second        string `json:"second"`
}

func TestOneOfOutputSchemaNormalizesBooleanPropertiesAndRetainsSDKValidation(t *testing.T) {
	const toolName = "union_output_schema_test"
	outputSchema := oneOfOutputSchema(toolName,
		reflect.TypeFor[outputSchemaTestFirst](),
		reflect.TypeFor[outputSchemaTestSecond](),
	)
	if outputSchema["type"] != "object" {
		t.Fatalf("schema type=%v", outputSchema["type"])
	}
	branches, ok := outputSchema["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("oneOf=%#v", outputSchema["oneOf"])
	}
	for index, raw := range branches {
		branch, _ := raw.(map[string]any)
		if branch["type"] != "object" || branch["additionalProperties"] != false {
			t.Errorf("branch %d is not a closed object: %#v", index, branch)
		}
	}
	if path, found := booleanPropertySchema(outputSchema, "outputSchema"); found {
		t.Fatalf("client-incompatible boolean property schema at %s", path)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "1"}, nil)
	tool := readOnlyTool(toolName, "Union schema test", "Validate oneOf output")
	tool.OutputSchema = outputSchema
	mcp.AddTool(server, tool,
		func(_ context.Context, _ *mcp.CallToolRequest, in outputSchemaTestInput) (*mcp.CallToolResult, any, error) {
			if in.Invalid {
				return nil, map[string]any{"schema_version": 1, "first": true, "second": "invalid"}, nil
			}
			return nil, outputSchemaTestFirst{SchemaVersion: 1, First: true}, nil
		})
	client, closeSessions := connectTestClient(t, server)
	defer closeSessions()
	if result := callToolOK(t, client, toolName, map[string]any{}); result.StructuredContent == nil {
		t.Fatal("valid union branch has no structured content")
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: toolName, Arguments: map[string]any{"invalid": true},
	})
	if err == nil || result != nil || !strings.Contains(err.Error(), "validating tool output") {
		t.Fatalf("invalid output result=%+v err=%v", result, err)
	}
}
