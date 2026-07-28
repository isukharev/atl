package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/mcpserver"
	"github.com/isukharev/atl/internal/version"
)

func newMCPCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the typed read-only agent tool surface",
	}
	service := mcpServiceFlag{profile: mcpserver.ServiceDefault}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the read-only MCP server over JSONL stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpserver.ServeService(cmd.Context(), version.Version, service.profile)
		},
	}
	serve.Flags().Var(&service, "service", "closed service profile: jira|confluence|offline (omit for the default combined profile)")
	_ = serve.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence", "offline"))
	group.AddCommand(serve)
	return group
}

// mcpServiceFlag rejects repeated occurrences instead of accepting pflag's
// usual last-value-wins behavior. This keeps malformed profile selection out of
// server and production-dependency construction.
type mcpServiceFlag struct {
	profile mcpserver.ServiceProfile
	set     bool
}

func (f *mcpServiceFlag) String() string {
	if f == nil || f.profile == mcpserver.ServiceDefault {
		return ""
	}
	return string(f.profile)
}

func (*mcpServiceFlag) Type() string { return "service" }

func (f *mcpServiceFlag) Set(value string) error {
	if f.set {
		return fmt.Errorf("--service may only be specified once")
	}
	profile, err := mcpserver.ParseServiceProfile(value)
	if err != nil {
		return err
	}
	f.profile = profile
	f.set = true
	return nil
}
