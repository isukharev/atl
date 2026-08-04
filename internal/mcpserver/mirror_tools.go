package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
)

func registerMirrorTools(server *mcp.Server, deps Dependencies) {
	registerJiraMirrorTool(server, deps)
	registerConfluenceMirrorTool(server, deps)
}

func registerJiraMirrorTool(server *mcp.Server, deps Dependencies) {
	addReadOnlyTool(server, readOnlyTool("jira_mirror_snapshot", "Inspect Jira mirror health", "Return fixed-shape, content-free local Jira mirror health counts from the owner-configured root. This tool is offline and accepts no path."),
		func(_ context.Context, _ *mcp.CallToolRequest, _ MirrorSnapshotInput) (*mcp.CallToolResult, *app.JiraMirrorSnapshot, error) {
			root, err := mirrorRoot(deps)
			if err != nil {
				return nil, nil, classifiedMirrorRead(err)
			}
			out, snapshotErr := app.SnapshotJiraMirror(root)
			if out != nil {
				// Incomplete local evidence is itself useful content-free health
				// evidence. The fixed-shape contract carries Complete=false.
				return nil, out, nil
			}
			return nil, nil, classifiedMirrorRead(snapshotErr)
		})
}

func registerConfluenceMirrorTool(server *mcp.Server, deps Dependencies) {
	addReadOnlyTool(server, readOnlyTool("confluence_mirror_snapshot", "Inspect Confluence mirror health", "Return fixed-shape, content-free local Confluence mirror health counts from the owner-configured root. This tool is offline and accepts no path."),
		func(_ context.Context, _ *mcp.CallToolRequest, _ MirrorSnapshotInput) (*mcp.CallToolResult, *app.ConfluenceMirrorSnapshot, error) {
			root, err := mirrorRoot(deps)
			if err != nil {
				return nil, nil, classifiedMirrorRead(err)
			}
			out, snapshotErr := app.SnapshotConfluenceMirror(root)
			if out != nil {
				return nil, out, nil
			}
			return nil, nil, classifiedMirrorRead(snapshotErr)
		})
}
