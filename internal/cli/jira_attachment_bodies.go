package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraAttachmentBodiesCmd() *cobra.Command {
	var into string
	var mediaTypes []string
	var maxAttachmentBytes int64
	var maxTransactions int
	cmd := &cobra.Command{
		Use:   "attachment-bodies",
		Short: "Resume bounded private Jira attachment-body materialization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("max-attachment-bytes") || !cmd.Flags().Changed("max-transactions") {
				return usageErr("--max-attachment-bytes and --max-transactions must be explicit")
			}
			opts := app.JiraAttachmentBodyMaterializeOpts{
				Into: into, AttachmentMediaTypes: mediaTypes,
				MaxAttachmentBytes: maxAttachmentBytes, MaxTransactions: maxTransactions,
			}
			if err := app.ValidateJiraAttachmentBodyMaterializeOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.MaterializeAttachmentBodies(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("attachment-bodies: inventories=%d pending=%d captured=%d remaining=%d complete=%t", result.Inventories, result.Pending, result.Captured, result.Remaining, result.Complete)
			})
		},
	}
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "existing Jira mirror root (default: $ATL_MIRROR_ROOT or \"mirror-jira\")")
	cmd.Flags().StringArrayVar(&mediaTypes, "attachment-media-type", nil, "exact required attachment MIME type (repeatable)")
	cmd.Flags().Int64Var(&maxAttachmentBytes, "max-attachment-bytes", 0, "required maximum size of one captured body (maximum 134217728)")
	cmd.Flags().IntVar(&maxTransactions, "max-transactions", 0, "required maximum one-body local transactions (1..4096)")
	return cmd
}
