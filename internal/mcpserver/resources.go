package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/capability"
)

const (
	CapabilitiesResourceURI      = "atl://capabilities"
	capabilitiesResourceMIMEType = "application/json"
	capabilitiesResourceSchema   = 1
)

type capabilitiesResource struct {
	SchemaVersion int                       `json:"schema_version"`
	Capabilities  []capabilityResourceEntry `json:"capabilities"`
}

type capabilityResourceEntry struct {
	ID         string `json:"id"`
	TaskClass  string `json:"task_class"`
	Service    string `json:"service"`
	Role       string `json:"role"`
	Priority   int    `json:"priority"`
	CLICommand string `json:"cli_command"`
	MCPTool    string `json:"mcp_tool,omitempty"`
	MCPScope   string `json:"mcp_scope,omitempty"`
	CLIOnly    bool   `json:"cli_only"`
}

func registerCapabilitiesResource(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         CapabilitiesResourceURI,
		Name:        "atl-capabilities",
		Title:       "atl capability routes",
		Description: "Static content-free CLI and MCP capability routing metadata.",
		MIMEType:    capabilitiesResourceMIMEType,
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		encoded, err := json.Marshal(staticCapabilitiesResource())
		if err != nil {
			return nil, fmt.Errorf("encode static MCP capabilities resource: %w", err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: CapabilitiesResourceURI, MIMEType: capabilitiesResourceMIMEType, Text: string(encoded),
		}}}, nil
	})
}

func staticCapabilitiesResource() capabilitiesResource {
	definitions := capability.Definitions()
	entries := make([]capabilityResourceEntry, 0, len(definitions))
	for _, definition := range definitions {
		entries = append(entries, capabilityResourceEntry{
			ID: definition.ID, TaskClass: definition.TaskClass, Service: definition.Service,
			Role: definition.Role, Priority: definition.Priority, CLICommand: definition.CLICommand,
			MCPTool: definition.MCPTool, MCPScope: definition.MCPScope, CLIOnly: definition.MCPTool == "",
		})
	}
	return capabilitiesResource{SchemaVersion: capabilitiesResourceSchema, Capabilities: entries}
}
