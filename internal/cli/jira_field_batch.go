package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueFieldBatchCmd() *cobra.Command {
	var keys, selectors []string
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Read a bounded qualified issue-field matrix",
		Long: "Resolve an ordered field selection through a complete qualified catalog, then return one compact, " +
			"reconciled row per requested issue key. This command emits JSON only.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			return app.ValidateJiraFieldBatchOpts(app.JiraFieldBatchOpts{Keys: keys, Selectors: selectors})
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := app.JiraFieldBatchOpts{Keys: keys, Selectors: selectors}
			if err := app.ValidateJiraFieldBatchOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.IssueFieldBatch(cmd.Context(), opts)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(result.EncodedJSON())
			if err != nil {
				return fmt.Errorf("write Jira field batch result: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&keys, "key", nil, "canonical Jira issue key (repeatable; 1..25)")
	cmd.Flags().StringArrayVar(&selectors, "field", nil, "exact field id or unambiguous display name (repeatable; 1..8)")
	return cmd
}
