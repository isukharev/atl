package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/version"
)

func newCompatibilityCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "compatibility",
		Short: "Manage exact version-pinned Data Center compatibility providers",
		Long: "Compatibility providers are disabled by default. An owner-only setting explicitly binds a compiled protocol profile to one exact version/build. " +
			"Providers never accept configurable endpoints, headers, payload templates, or version ranges.",
	}

	var remote bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Inspect the configured provider and optionally qualify its remote identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings, err := compatibility.Load(config.Dir())
			if err != nil {
				return err
			}
			cfg := &config.Config{}
			if remote {
				cfg, err = loadConfig()
				if err != nil {
					return err
				}
			}
			result := app.NewCompatibility(cfg, settings, version.Version).Status(cmd.Context(), remote)
			return emit(cmd, result, func() string { return app.CompatibilityStatusText(result) })
		},
	}
	status.Flags().BoolVar(&remote, "remote", false, "read and match the configured backend's exact product identity")

	var pinVersion, pinBuild string
	pin := &cobra.Command{
		Use:   "pin confluence",
		Short: "Enable one compiled Confluence provider for an exact version and build",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != compatibility.ProductConfluence.String() {
				return usageErr("unsupported compatibility service %q (want confluence)", args[0])
			}
			parsedVersion, err := compatibility.ParseVersion(pinVersion)
			if err != nil {
				return usageErr("invalid compatibility version")
			}
			parsedBuild, err := compatibility.ParseBuildNumber(pinBuild)
			if err != nil {
				return usageErr("invalid compatibility build number")
			}
			configuredPin := compatibility.Pin{Version: parsedVersion, BuildNumber: parsedBuild}
			settings, err := compatibility.Load(config.Dir())
			if err != nil {
				return err
			}
			settings.Confluence = &compatibility.Activation{
				ProviderID: compatibility.ConfluenceInlineCommentsDCProfileID,
				Version:    configuredPin.Version, BuildNumber: configuredPin.BuildNumber,
			}
			if err := compatibility.Save(config.Dir(), settings); err != nil {
				return err
			}
			result := app.NewCompatibility(&config.Config{}, settings, version.Version).Status(cmd.Context(), false)
			return emit(cmd, result, nil)
		},
	}
	pin.Flags().StringVar(&pinVersion, "version", "", "exact three-component Confluence version")
	pin.Flags().StringVar(&pinBuild, "build-number", "", "exact decimal Confluence build number")
	_ = pin.MarkFlagRequired("version")
	_ = pin.MarkFlagRequired("build-number")

	clear := &cobra.Command{
		Use:   "clear confluence",
		Short: "Disable the configured Confluence compatibility provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != compatibility.ProductConfluence.String() {
				return usageErr("unsupported compatibility service %q (want confluence)", args[0])
			}
			settings, err := compatibility.Load(config.Dir())
			if err != nil {
				return err
			}
			settings.Confluence = nil
			if err := compatibility.Save(config.Dir(), settings); err != nil {
				return err
			}
			result := app.NewCompatibility(&config.Config{}, settings, version.Version).Status(cmd.Context(), false)
			return emit(cmd, result, nil)
		},
	}

	command.AddCommand(status, pin, clear)
	return command
}
