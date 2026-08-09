package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

func readOnlyTool(name, title, description string) *mcp.Tool {
	closed := false
	nondestructive := false
	return &mcp.Tool{
		Name: name, Title: title, Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title: title, ReadOnlyHint: true, IdempotentHint: true,
			DestructiveHint: &nondestructive, OpenWorldHint: &closed,
		},
	}
}

func jiraStructureGetInputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"structure_id": {
				Description: "positive Jira Structure id as an integer or canonical decimal string",
				OneOf: []*jsonschema.Schema{
					{Type: "integer", Minimum: jsonschema.Ptr(1.0)},
					{Type: "string", Pattern: `^[1-9][0-9]{0,18}$`, MaxLength: jsonschema.Ptr(19)},
				},
			},
		},
		Required:             []string{"structure_id"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func parseStructureIDInput(raw json.RawMessage) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("%w: structure_id must be a positive integer or canonical decimal string", domain.ErrUsage)
	}
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return invalid()
	}
	value := string(raw)
	if raw[0] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil || decoded == "" || decoded[0] < '1' || decoded[0] > '9' {
			return invalid()
		}
		for index := 1; index < len(decoded); index++ {
			if decoded[index] < '0' || decoded[index] > '9' {
				return invalid()
			}
		}
		value = decoded
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return invalid()
	}
	return id, nil
}

// addReadOnlyTool keeps the SDK's validated output contract while spelling
// unrestricted property schemas as {} instead of the equivalent JSON Schema
// boolean true. Most tools infer one output type; callers with a reviewed
// closed union may provide tool.OutputSchema before registration. Some current
// MCP clients reject boolean schemas in a tool's properties map and otherwise
// discard the server's entire tool list.
func addReadOnlyTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	if tool.OutputSchema == nil {
		tool.OutputSchema = inferredOutputSchema(tool.Name, reflect.TypeFor[Out]())
	}
	normalizeBooleanPropertySchemas(tool.OutputSchema)
	mcp.AddTool(server, tool, handler)
}

func normalizeBooleanPropertySchemas(value any) {
	switch current := value.(type) {
	case map[string]any:
		if properties, ok := current["properties"].(map[string]any); ok {
			for name, property := range properties {
				if unrestricted, ok := property.(bool); ok {
					if unrestricted {
						properties[name] = map[string]any{}
					} else {
						properties[name] = map[string]any{"not": map[string]any{}}
					}
					continue
				}
				normalizeBooleanPropertySchemas(property)
			}
		}
		for keyword, child := range current {
			if keyword != "properties" {
				normalizeBooleanPropertySchemas(child)
			}
		}
	case []any:
		for _, child := range current {
			normalizeBooleanPropertySchemas(child)
		}
	}
}

var sdkSchemaValidationToolError = toolError{
	Kind:        "usage_error",
	Remediation: "fix_request",
	Message:     "MCP tool arguments do not match the declared schema",
	Recovery:    diagnostic.Recover(domain.ErrUsage, diagnostic.OperationRead),
}

// normalizeSDKSchemaValidationErrors adapts the SDK's tool-result validation
// semantics to atl's privacy boundary. SDK 1.6 records schema and argument
// decoding errors for receiving middleware, so their caller-derived prose can
// be replaced before the result crosses the wire. ATL handlers return the
// closed toolError type; protocol errors return through the separate error
// channel. This type allowlist avoids coupling the boundary to SDK error text.
func normalizeSDKSchemaValidationErrors(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if err != nil || method != "tools/call" {
			return result, err
		}
		callResult, ok := result.(*mcp.CallToolResult)
		if !ok || !callResult.IsError {
			return result, nil
		}
		sdkErr := callResult.GetError()
		if sdkErr == nil {
			return result, nil
		}
		var atlErr toolError
		if errors.As(sdkErr, &atlErr) {
			return result, nil
		}

		// Construct a fresh result so validator content, structured output, and
		// metadata cannot survive even if a future SDK release adds them.
		normalized := &mcp.CallToolResult{}
		normalized.SetError(sdkSchemaValidationToolError)
		return normalized, nil
	}
}
