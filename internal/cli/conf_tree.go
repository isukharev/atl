package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func normalizeConfluenceTreeCommand(cmd *cobra.Command, opts *app.ConfluenceTreeOpts) error {
	for _, flag := range []struct {
		name    string
		invalid bool
	}{
		{name: "max-items", invalid: opts.MaxItems <= 0},
		{name: "max-scanned-items", invalid: opts.MaxScannedItems <= 0},
		{name: "max-requests", invalid: opts.MaxRequests <= 0},
		{name: "max-response-bytes", invalid: opts.MaxResponseBytes <= 0},
		{name: "deadline", invalid: opts.Deadline <= 0},
	} {
		if cmd.Flags().Changed(flag.name) && flag.invalid {
			return usageErr("--%s must be greater than zero", flag.name)
		}
	}
	normalized, err := app.NormalizeConfluenceTreeOpts(*opts)
	if err == nil {
		*opts = normalized
	}
	return err
}

func readConfluenceTreeCommand(cmd *cobra.Command, opts app.ConfluenceTreeOpts) (*app.ConfluenceTreeResult, error) {
	svc, err := confService(cmd)
	if err != nil {
		return nil, err
	}
	return svc.TreeQualified(cmd.Context(), opts)
}

func warnConfluenceTreePartial(cmd *cobra.Command, result *app.ConfluenceTreeResult) {
	if !result.Complete {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: space listing is partial after %d pages (%s) — omitted pages are NOT proven absent\n", result.Count, result.PartialReason)
	}
}

func confluenceTreeText(pages []domain.PageRef) string {
	var b strings.Builder
	for _, ref := range pages {
		fmt.Fprintf(&b, "%s\t%s\t(parent %s)\n", ref.ID, ref.Title, ref.Parent)
	}
	return strings.TrimRight(b.String(), "\n")
}

func registerConfluenceTreeFlags(tree *cobra.Command, opts *app.ConfluenceTreeOpts) {
	tree.Flags().StringVar(&opts.Space, "space", "", "space key")
	tree.Flags().IntVar(&opts.Depth, "depth", 0, "max depth (0 = unlimited)")
	tree.Flags().IntVar(&opts.MaxItems, "max-items", 0, fmt.Sprintf("returned page limit (default %d, max %d)", app.ConfluenceTreeDefaultMaxItems, app.ConfluenceTreeMaxItems))
	tree.Flags().IntVar(&opts.MaxScannedItems, "max-scanned-items", 0, fmt.Sprintf("raw backend row limit (default %d, max %d)", app.ConfluenceTreeDefaultMaxScannedItems, app.ConfluenceTreeMaxScannedItems))
	tree.Flags().IntVar(&opts.MaxRequests, "max-requests", 0, fmt.Sprintf("physical HTTP attempt limit (default %d, max %d)", app.ConfluenceTreeDefaultMaxRequests, app.ConfluenceTreeMaxRequests))
	tree.Flags().Int64Var(&opts.MaxResponseBytes, "max-response-bytes", 0, fmt.Sprintf("aggregate buffered response byte limit (default %d, max %d)", app.ConfluenceTreeDefaultResponseBytes, app.ConfluenceTreeMaxResponseBytes))
	tree.Flags().DurationVar(&opts.Deadline, "deadline", 0, fmt.Sprintf("wall-clock traversal deadline (default %s, max %s)", app.ConfluenceTreeDefaultDeadline, app.ConfluenceTreeMaxDeadline))
}
