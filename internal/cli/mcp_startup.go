package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/plugincontract"
)

// bindMCPPluginStartup validates markers before pre-runs and dependencies.
func bindMCPPluginStartup(cmd *cobra.Command, binaryProductVersion string) {
	var interfaceContracts, productVersions []string
	cmd.Flags().StringArrayVar(&interfaceContracts, plugincontract.InterfaceFlagName, nil, "generated plugin interface-contract marker")
	cmd.Flags().StringArrayVar(&productVersions, plugincontract.ProductFlagName, nil, "generated plugin product-version marker")
	_ = cmd.Flags().MarkHidden(plugincontract.InterfaceFlagName)
	_ = cmd.Flags().MarkHidden(plugincontract.ProductFlagName)
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		_, err := plugincontract.Evaluate(plugincontract.Markers{
			InterfaceContracts: interfaceContracts,
			ProductVersions:    productVersions,
		}, binaryProductVersion)
		if err == plugincontract.ErrIncompatibleInterface {
			return usageErr("plugin interface contract is incompatible; update the atl plugin or binary")
		}
		if err != nil {
			return usageErr("invalid plugin startup markers")
		}
		return nil
	}
}
