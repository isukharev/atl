package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/mcpserver"
	"github.com/isukharev/atl/internal/version"
)

func newMCPCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the typed remote-read-only agent tool surface",
	}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the read-only MCP server over JSONL stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpserver.Serve(cmd.Context(), version.Version)
		},
	}
	group.AddCommand(serve)
	return group
}
