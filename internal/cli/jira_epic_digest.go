package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraEpicCmd() *cobra.Command {
	group := &cobra.Command{Use: "epic", Short: "Deterministic epic evidence workflows"}
	var opts app.JiraEpicDigestOpts
	digest := &cobra.Command{
		Use:   "digest <KEY>",
		Short: "Aggregate dated epic evidence without generating management prose",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jira, err := jiraService()
			if err != nil {
				return err
			}
			if opts.ExpandConfluence > 0 {
				confluence, confErr := confService()
				if confErr != nil {
					return confErr
				}
				opts.Confluence = confluence
			}
			result, err := jira.EpicDigest(cmd.Context(), args[0], opts)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraEpicDigestMarkdown(result) })
		},
	}
	digest.Flags().StringVar(&opts.Quarter, "quarter", "", "calendar quarter YYYY-Q1..Q4")
	digest.Flags().StringVar(&opts.Since, "since", "", "explicit inclusive period start (requires --until)")
	digest.Flags().StringVar(&opts.Until, "until", "", "explicit inclusive period end (requires --since)")
	digest.Flags().StringArrayVar(&opts.Include, "include", nil, "evidence source (repeat/comma: identity,status-field,children,comments,links,history,refs,confluence)")
	digest.Flags().StringVar(&opts.StatusField, "status-field", "", "exact id or display name of the narrative status field")
	digest.Flags().StringVar(&opts.DoDField, "dod-field", "", "exact id or display name of an additional DoD/evidence field")
	digest.Flags().StringVar(&opts.EpicField, "epic-field", "", "Epic Link/parent field id or display name")
	digest.Flags().IntVar(&opts.ChildLimit, "child-limit", 1000, "max child rows (cap 1000)")
	digest.Flags().IntVar(&opts.CommentLimit, "comment-limit", 50, "max newest comments (cap 50)")
	digest.Flags().IntVar(&opts.HistoryLimit, "history-limit", 500, "max newest matching history entries (cap 500)")
	digest.Flags().IntVar(&opts.ExpandConfluence, "expand-confluence", 0, "max same-origin Confluence refs to expand (0..10)")
	digest.Flags().StringVar(&opts.ConfluenceHeading, "confluence-heading", "", "exact heading selected from each expanded Confluence page")
	group.AddCommand(digest)
	return group
}
