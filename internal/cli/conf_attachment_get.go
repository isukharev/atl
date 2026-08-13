package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func confAttachmentGetCmd() *cobra.Command {
	var pageID, name, into string
	var version int
	var maxBytes int64
	command := &cobra.Command{
		Use:   "get",
		Short: "Download an attachment to a directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(pageID) == "" || name == "" {
				return usageErr("--id and --name are required")
			}
			if !app.ValidConfluencePageReferenceInput(pageID) {
				return usageErr("--id must be an opaque id, absolute URL, or root-relative path")
			}
			if !app.ValidConfluenceAttachmentDownloadFilename(name) {
				return usageErr(fmt.Sprintf("--name must be nonblank valid UTF-8 and at most %d bytes", app.ConfluenceAttachmentDownloadMaxFilenameBytes))
			}
			if version < 0 {
				return usageErr("--version must be non-negative")
			}
			if cmd.Flags().Changed("max-bytes") && maxBytes <= 0 {
				return usageErr("--max-bytes must be greater than zero")
			}
			options, err := app.NormalizeConfluenceAttachmentDownloadOptions(app.ConfluenceAttachmentDownloadOptions{MaxBytes: maxBytes})
			if err != nil {
				return err
			}
			service, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := service.DownloadAttachmentKnownPageWithOptions(cmd.Context(), pageID, name, version, into, options)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return result.Path })
		},
	}
	command.Flags().StringVar(&pageID, "id", "", "page id or supported same-origin URL")
	command.Flags().StringVar(&name, "name", "", "attachment filename")
	command.Flags().IntVar(&version, "version", 0, "attachment version (0 = latest)")
	command.Flags().Int64Var(&maxBytes, "max-bytes", app.ConfluenceAttachmentDownloadDefaultMaxBytes,
		fmt.Sprintf("maximum attachment bytes (default %d, max %d)", app.ConfluenceAttachmentDownloadDefaultMaxBytes, app.ConfluenceAttachmentDownloadMaxBytes))
	command.Flags().StringVar(&into, "into", ".", "output directory")
	return command
}
