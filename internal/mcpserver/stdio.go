package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/domain"
)

// Serve runs the production server over JSONL stdio until the client
// disconnects or ctx is canceled. Protocol bytes are the only stdout output.
func Serve(ctx context.Context, version string) error {
	return New(version, ProductionDependencies(version)).Run(ctx, &mcp.StdioTransport{})
}

// ServeService runs one validated closed service profile over JSONL stdio.
func ServeService(ctx context.Context, version string, profile ServiceProfile) error {
	return ServeServiceWithRuntime(ctx, version, profile, defaultRuntimeSnapshot())
}

// ServeServiceWithRuntime runs a validated profile with its required startup
// snapshot. The CLI captures that snapshot before opening stdio.
func ServeServiceWithRuntime(ctx context.Context, version string, profile ServiceProfile, runtime RuntimeSnapshot) error {
	if !profile.valid() {
		return fmt.Errorf("%w: invalid MCP service %q (want jira|confluence|offline)", domain.ErrUsage, profile)
	}
	if !runtime.valid() {
		return fmt.Errorf("invalid MCP runtime snapshot")
	}
	return NewForServiceWithRuntime(version, ProductionDependencies(version), profile, runtime).Run(ctx, &mcp.StdioTransport{})
}
