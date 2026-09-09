package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/compose"
)

var initializeBrokerJournal = compose.InitializeBrokerJournal

func newBrokerJournalCommand() *cobra.Command {
	group := &cobra.Command{Use: "journal", Short: "Manage explicit local Broker journal storage"}
	config := &brokerConfigFlag{}
	initialize := &cobra.Command{
		Use: "initialize", Short: "Create an absent owner-private journal without credentials or network access",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !config.set {
				return usageErr("--config is required")
			}
			if err := initializeBrokerJournal(config.value); err != nil {
				return err
			}
			result := struct {
				Status   string `json:"status"`
				Complete bool   `json:"complete"`
			}{Status: "initialized", Complete: true}
			return emit(cmd, result, func() string { return "Broker journal initialized" })
		},
	}
	initialize.Flags().Var(config, "config", "owner-private Broker host configuration file")
	group.AddCommand(initialize)
	return group
}

func brokerOperatorCommand(cmd *cobra.Command) bool {
	path := commandRegistryPath(cmd.Root(), cmd)
	return path == "broker serve" || path == "broker journal initialize"
}
