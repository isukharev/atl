package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueWatchersCmd() *cobra.Command {
	group := &cobra.Command{Use: "watchers", Short: "List or safely change issue watchers"}
	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List current issue watchers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.ListWatchers(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !result.Complete {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: Jira did not expose every watcher identity; complete=false")
			}
			return emit(cmd, result, func() string {
				var text strings.Builder
				for _, watcher := range result.Watchers {
					fmt.Fprintf(&text, "%s\t%s\t%t\n", watcher.Name, watcher.DisplayName, watcher.Active)
				}
				return strings.TrimRight(text.String(), "\n")
			})
		},
	}
	group.AddCommand(list, jiraIssueWatcherMutationCmd("add"), jiraIssueWatcherMutationCmd("remove"))
	return group
}

func jiraIssueWatcherMutationCmd(operation string) *cobra.Command {
	var username, expectedProposalHash string
	var me, applyWrite bool
	cmd := &cobra.Command{
		Use:   operation + " <KEY>",
		Short: strings.ToUpper(operation[:1]) + operation[1:] + " one watcher through a reviewed dry-run",
		Long: "Preview by default after resolving one explicit Data Center username and reading complete membership. " +
			"Apply requires the exact proposal hash, sends one write attempt, and reconciles without replay.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identityChoices := 0
			if strings.TrimSpace(username) != "" {
				identityChoices++
			}
			if me {
				identityChoices++
			}
			if identityChoices != 1 {
				return usageErr("pass exactly one of --username or --me")
			}
			if applyWrite && strings.TrimSpace(expectedProposalHash) == "" {
				return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, mutationErr := svc.MutateWatcherGuarded(cmd.Context(), args[0], app.JiraWatcherMutationOpts{
				Operation: operation, Username: username, Me: me,
				ExpectedProposalHash: expectedProposalHash, Apply: applyWrite,
			})
			if result != nil {
				if emitErr := emit(cmd, result, func() string {
					return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", result.Status, result.Key, result.Operation, result.ProposalHash, result.Username)
				}); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "explicit Jira Data Center username")
	cmd.Flags().BoolVar(&me, "me", false, "resolve the authenticated user's Data Center username")
	cmd.Flags().StringVar(&expectedProposalHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")
	cmd.Flags().BoolVar(&applyWrite, "apply", false, "perform the guarded write (default: dry-run)")
	return cmd
}
