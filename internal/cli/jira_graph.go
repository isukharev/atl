package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueGraphCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "graph <KEY>",
		Short: "Build a qualified direct work-artifact graph for one issue",
		Long: "Build a deterministic read-only depth-zero graph from one Jira issue and its requested stable evidence sources. " +
			"Discovered Jira, Confluence, and external targets remain unfetched stubs. Source status qualifies every absence or partial result.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := jiraService()
			if err != nil {
				return err
			}
			result, err := service.IssueGraph(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraIssueGraphMarkdown(result) })
		},
	}
}
