package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

// confApplyCmd implements `conf apply`: merge edits from the page's markdown
// view back into the .csf, block by block. Untouched blocks keep their exact
// base bytes; changed/new blocks are converted from a strict markdown subset;
// anything unconvertible fails closed with a pointer to edit the .csf
// directly. Local only — `conf push` remains the write path to the server.
func confApplyCmd() *cobra.Command {
	var o app.ApplyOpts
	cmd := &cobra.Command{
		Use:   "apply <page.md>",
		Short: "Merge edits from the .md view into the .csf (block-level, non-lossy)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := app.Apply(args[0], o)
			if res != nil && res.Report != nil {
				_ = emit(cmd, res, func() string { return applyText(res) })
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "report the merge without writing files")
	cmd.Flags().BoolVar(&o.AllowFragmentLoss, "allow-fragment-loss", false,
		"proceed even when the edit drops opaque fragments (macros, mentions, links)")
	cmd.Flags().StringVar(&o.Into, "into", "", "mirror root (defaults to nearest .atl)")
	return cmd
}

func applyText(res *app.ApplyResult) string {
	verb := "applied"
	if res.DryRun {
		verb = "would apply"
	}
	r := res.Report
	s := fmt.Sprintf("%s\t%s: %d unchanged, %d converted, %d moved, %d removed",
		res.CSFPath, verb, r.Unchanged, r.Converted, r.Moved, r.Removed)
	for _, f := range r.RemovedFragments {
		s += fmt.Sprintf("\n   - removes %s %s", f.Kind, f.Display)
	}
	for _, p := range r.Problems {
		s += fmt.Sprintf("\n   ! %s:%d:%d %s", p.Severity, p.Line, p.Col, p.Message)
	}
	return s
}
