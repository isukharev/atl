package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueFieldsCmd() *cobra.Command {
	var selectors []string
	var includeEmpty, raw, metadataOnly bool
	cmd := &cobra.Command{
		Use:   "fields <KEY>",
		Short: "Inspect non-empty issue fields with compact named values",
		Long: "Return non-empty fields by default with user/option/version transport noise removed. " +
			"Repeat --field with an exact id or display name; ambiguous names fail closed. " +
			"Use --metadata-only for a value-free first-pass inventory, --include-empty for the full catalog, " +
			"or --raw for private unprojected values.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if metadataOnly && raw {
				return usageErr("--metadata-only conflicts with --raw")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.IssueFields(cmd.Context(), args[0], app.JiraIssueFieldsOpts{
				Selectors: selectors, IncludeEmpty: includeEmpty, Raw: raw, MetadataOnly: metadataOnly,
			})
			if err != nil {
				return err
			}
			if raw {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: raw Jira field values may contain private user/contact/transport data")
			}
			return emit(cmd, result, func() string { return app.JiraIssueFieldsMarkdown(result) })
		},
	}
	cmd.Flags().StringArrayVar(&selectors, "field", nil, "exact field id or display name (repeatable; default: all non-empty)")
	cmd.Flags().BoolVar(&includeEmpty, "include-empty", false, "include missing/null/empty fields from the Jira field catalog")
	cmd.Flags().BoolVar(&raw, "raw", false, "emit unprojected private Jira values")
	cmd.Flags().BoolVar(&metadataOnly, "metadata-only", false, "omit values and emit only ids, names, schemas, emptiness, and coarse value types")
	return cmd
}
