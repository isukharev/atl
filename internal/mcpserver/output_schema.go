package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
)

// oneOfOutputSchema infers each closed object branch through the same schema
// library used by the MCP SDK. Supplying the result to mcp.AddTool with an
// any-typed output retains SDK result validation while allowing one tool to
// return more than one explicitly described object shape.
func oneOfOutputSchema(toolName string, outputTypes ...reflect.Type) map[string]any {
	if len(outputTypes) < 2 {
		panic(fmt.Sprintf("infer MCP union output schema for %s: at least two output types are required", toolName))
	}
	oneOf := make([]any, 0, len(outputTypes))
	for _, outputType := range outputTypes {
		oneOf = append(oneOf, inferredOutputSchema(toolName, outputType))
	}
	return map[string]any{"type": "object", "oneOf": oneOf}
}

func inferredOutputSchema(toolName string, outputType reflect.Type) map[string]any {
	if outputType == nil {
		panic(fmt.Sprintf("infer MCP output schema for %s: nil output type", toolName))
	}
	for outputType.Kind() == reflect.Pointer {
		outputType = outputType.Elem()
	}
	schema, err := jsonschema.ForType(outputType, &jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("infer MCP output schema for %s: %v", toolName, err))
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("marshal MCP output schema for %s: %v", toolName, err))
	}
	var compatible map[string]any
	if err := json.Unmarshal(encoded, &compatible); err != nil {
		panic(fmt.Sprintf("decode MCP output schema for %s: %v", toolName, err))
	}
	if compatible["type"] != "object" {
		panic(fmt.Sprintf("infer MCP output schema for %s: output type %s is not an object", toolName, outputType))
	}
	if compatible["additionalProperties"] != false {
		panic(fmt.Sprintf("infer MCP output schema for %s: output type %s is not closed", toolName, outputType))
	}
	normalizeBooleanPropertySchemas(compatible)
	return compatible
}
