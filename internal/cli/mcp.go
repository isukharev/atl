package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mcpserver"
	"github.com/isukharev/atl/internal/plugincontract"
	"github.com/isukharev/atl/internal/version"
)

type mcpServiceRunner func(context.Context, string, mcpserver.ServiceProfile, mcpserver.RuntimeSnapshot) error

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
	}
	serve.Flags().Var(&service, "service", "closed service profile: jira|confluence|offline (omit for the default combined profile)")
	pluginStartupStatus := bindMCPPluginStartup(serve, version.Version)
	serve.RunE = func(cmd *cobra.Command, _ []string) error {
		return runMCPService(cmd, version.Version, service.profile, pluginStartupStatus(), mcpserver.ServeServiceWithRuntime)
	}
	_ = serve.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence", "offline"))
	group.AddCommand(serve)
	return group
}

func runMCPService(cmd *cobra.Command, binaryVersion string, profile mcpserver.ServiceProfile, plugin plugincontract.StartupStatus, serve mcpServiceRunner) error {
	cfg, err := config.LoadPersistedForEdit()
	if err != nil {
		return fmt.Errorf("%w: persisted MCP safety configuration is unavailable", domain.ErrConfig)
	}
	readOnly := app.ProjectReadOnly(cfg.ReadOnly, invocationRuntimeFor(cmd).readOnly, envReadOnly())
	runtime := mcpserver.RuntimeSnapshot{
		GlobalReadOnlyPolicy: mcpserver.RuntimeReadOnlyPolicy{
			ConfiguredReadOnly: cfg.ReadOnly,
			EffectiveReadOnly:  readOnly.EffectiveReadOnly,
			ReadOnlySource:     mcpserver.RuntimeReadOnlySource(readOnly.ReadOnlySource),
		},
		Plugin: mcpserver.RuntimePluginStatus{
			InterfaceContract: mcpserver.RuntimeInterfaceContract(plugin.InterfaceContract),
			ProductVersion:    mcpserver.RuntimeProductVersion(plugin.ProductVersion),
		},
	}
	return serve(cmd.Context(), binaryVersion, profile, runtime)
}

// mcpServiceFlag rejects repeats before server/dependency construction.
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
