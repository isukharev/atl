package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func confPageLabelsCmd() *cobra.Command {
	group := &cobra.Command{Use: "labels", Short: "List or safely change page labels"}
	list := &cobra.Command{
		Use:   "list <ID>",
		Short: "List all labels on a page",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, err := svc.ListLabels(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if result.Truncated {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: Confluence label pagination hit its safety cap; complete=false")
			}
			return emit(cmd, result, func() string {
				var text strings.Builder
				for _, label := range result.Labels {
					fmt.Fprintf(&text, "%s\t%s\n", label.Prefix, label.Name)
				}
				return strings.TrimRight(text.String(), "\n")
			})
		},
	}
	group.AddCommand(list, confPageLabelMutationCmd("add"), confPageLabelMutationCmd("remove"))
	return group
}

func confPageLabelMutationCmd(operation string) *cobra.Command {
	var applyWrite bool
	var expectedProposalHash string
	cmd := &cobra.Command{
		Use:   operation + " <ID> <LABEL>...",
		Short: strings.ToUpper(operation[:1]) + operation[1:] + " page labels through a reviewed dry-run",
		Long: "Preview by default after reading the complete current label set. " +
			"Apply requires the exact proposal hash from that preview, sends each write once, " +
			"and reconciles the result without automatic replay.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, mutationErr := svc.MutateLabelsGuarded(cmd.Context(), args[0], app.ConfluenceLabelMutationOpts{
				Operation: operation, Labels: args[1:], ExpectedProposalHash: expectedProposalHash, Apply: applyWrite,
			})
			if result != nil {
				if emitErr := emit(cmd, result, func() string {
					return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", result.Status, result.ID, result.Operation, result.ProposalHash, strings.Join(result.Requested, ","))
				}); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&expectedProposalHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")
	cmd.Flags().BoolVar(&applyWrite, "apply", false, "perform the guarded write (default: dry-run)")
	return cmd
}
