package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func confAttachmentDiscoveryCmd() *cobra.Command {
	var opts app.ConfluenceAttachmentDiscoveryOpts
	command := &cobra.Command{
		Use:   "search",
		Short: "Search bounded attachment metadata across Confluence",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, name := range []string{"max-items", "max-requests", "max-response-bytes", "deadline"} {
				if !cmd.Flags().Changed(name) {
					return usageErr("--" + name + " is required")
				}
			}
			normalized, err := app.NormalizeConfluenceAttachmentDiscoveryOpts(opts)
			if err != nil {
				return err
			}
			opts = normalized
			service, err := confService(cmd)
			if err != nil {
				return err
			}
			result, discoveryErr := service.DiscoverAttachments(cmd.Context(), opts)
			if result == nil {
				return discoveryErr
			}
			emitErr := emitID(cmd, result, func() string { return confluenceAttachmentDiscoveryText(result) }, func() []string {
				ids := make([]string, len(result.Attachments))
				for i, attachment := range result.Attachments {
					ids[i] = attachment.ID
				}
				return ids
			})
			return errors.Join(discoveryErr, emitErr)
		},
	}
	command.Flags().StringVar(&opts.Space, "space", "", "optional exact space key scope")
	command.Flags().StringVar(&opts.CQL, "cql", "", "optional additional CQL filter (ORDER BY is not allowed)")
	command.Flags().StringVar(&opts.Cursor, "cursor", "", "query-bound live offset cursor from a previous result")
	command.Flags().IntVar(&opts.MaxItems, "max-items", 0, fmt.Sprintf("required attachment item bound (max %d)", app.ConfluenceAttachmentDiscoveryMaxItems))
	command.Flags().IntVar(&opts.MaxRequests, "max-requests", 0, fmt.Sprintf("required physical request bound (max %d)", app.ConfluenceAttachmentDiscoveryMaxRequests))
	command.Flags().Int64Var(&opts.MaxResponseBytes, "max-response-bytes", 0, fmt.Sprintf("required aggregate response-byte bound (max %d)", app.ConfluenceAttachmentDiscoveryMaxResponseBytes))
	command.Flags().DurationVar(&opts.Deadline, "deadline", 0*time.Second, fmt.Sprintf("required wall-clock deadline (max %s)", app.ConfluenceAttachmentDiscoveryMaxDeadline))
	return command
}

func confluenceAttachmentDiscoveryText(result *app.ConfluenceAttachmentDiscoveryResult) string {
	if result == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "qualification: %s complete=%t consistency=%s count=%d", result.Qualification, result.Complete, result.Consistency, result.Count)
	if result.Reason != "" {
		fmt.Fprintf(&out, " reason=%s", result.Reason)
	}
	out.WriteByte('\n')
	if result.NextCursor != "" {
		fmt.Fprintf(&out, "next_cursor: %s\n", result.NextCursor)
	}
	for _, attachment := range result.Attachments {
		fmt.Fprintf(&out, "%s\t%q\tattachment_version=%d\tcontainer=%s:%s@%d\tspace=%q\tmedia_type=%q\tsize=%d\n",
			attachment.ID, attachment.Title, attachment.Version, attachment.ContainerType, attachment.ContainerID,
			attachment.ContainerVersion, attachment.Space, attachment.MediaType, attachment.FileSize)
	}
	return strings.TrimRight(out.String(), "\n")
}
