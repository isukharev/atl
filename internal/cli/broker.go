package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/compose"
	"github.com/isukharev/atl/internal/version"
)

type brokerRunner interface {
	Run(context.Context) error
	Close()
}

var loadBrokerRuntime = func(path, binaryVersion string, command *cobra.Command) (brokerRunner, error) {
	return compose.LoadBrokerRuntime(path, binaryVersion, command.ErrOrStderr())
}

type brokerConfigFlag struct {
	value string
	set   bool
}

func (f *brokerConfigFlag) String() string { return f.value }
func (*brokerConfigFlag) Type() string     { return "file" }

func (f *brokerConfigFlag) Set(value string) error {
	if f.set {
		return fmt.Errorf("--config may only be specified once")
	}
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("--config requires one non-empty file")
	}
	f.value, f.set = value, true
	return nil
}

func newBrokerCommand() *cobra.Command {
	group := &cobra.Command{Use: "broker", Short: "Run the authenticated local Broker"}
	config := &brokerConfigFlag{}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve configured exact reads over bounded loopback TLS",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !config.set {
				return usageErr("--config is required")
			}
			runtime, err := loadBrokerRuntime(config.value, version.Version, cmd)
			if err != nil {
				return err
			}
			defer runtime.Close()
			if err := runtime.Run(cmd.Context()); err != nil {
				return err
			}
			result := struct {
				Status   string `json:"status"`
				Complete bool   `json:"complete"`
			}{Status: "stopped", Complete: true}
			return emit(cmd, result, func() string { return "Broker stopped" })
		},
	}
	serve.Flags().Var(config, "config", "owner-private Broker host configuration file")
	group.AddCommand(serve)
	return group
}
