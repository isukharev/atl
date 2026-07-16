package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueFieldGetCmd() *cobra.Command {
	var selector string
	var maxBytes int
	cmd := &cobra.Command{
		Use:   "get <KEY>",
		Short: "Read one bounded compact field value with snapshot provenance",
		Long: "Resolve one exact field id or unambiguous display name, then read only that value and Jira's updated timestamp. " +
			"The compact projection removes user transport/PII noise and reports explicit byte completeness.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.IssueFieldEvidence(cmd.Context(), args[0], app.JiraIssueFieldEvidenceOpts{
				Selector: selector, MaxBytes: maxBytes,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraIssueFieldEvidenceMarkdown(result) })
		},
	}
	cmd.Flags().StringVar(&selector, "field", "", "exact field id or unambiguous display name (required)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", app.JiraIssueFieldEvidenceDefaultMaxBytes, "maximum encoded compact value bytes")
	_ = cmd.MarkFlagRequired("field")
	return cmd
}
