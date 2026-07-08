package cli

import (
	"fmt"
	"path/filepath"
	"strings"

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

// applyText renders `conf apply`'s result as a compact human loss-review: one
// fact per line, `key: value`, indented `- ` lists, zero-count and empty
// sections omitted. It is a stable contract (see docs/OUTPUT_CONTRACT.md).
func applyText(res *app.ApplyResult) string {
	var b strings.Builder
	switch {
	case res.DryRun:
		fmt.Fprintln(&b, "dry-run: no files written")
	case res.Wrote:
		fmt.Fprintf(&b, "applied: %s\n", res.CSFPath)
	default:
		fmt.Fprintf(&b, "not applied: %s\n", res.CSFPath)
	}

	r := res.Report
	if r == nil {
		fmt.Fprintln(&b, "blocks: none")
	} else {
		fmt.Fprintln(&b, blockCountsLine(r.Unchanged, r.Moved, r.Converted, r.Removed, r.MergedTables))
		if len(r.RemovedFragments) > 0 {
			fmt.Fprintln(&b, "removed fragments:")
			for _, f := range r.RemovedFragments {
				id := f.Display
				if id == "" {
					id = f.Key
				}
				fmt.Fprintf(&b, "  - %s %q\n", f.Kind, id)
			}
		}
		if len(r.Problems) > 0 {
			fmt.Fprintln(&b, "problems:")
			for _, p := range r.Problems {
				if p.Line > 0 && p.Col > 0 {
					fmt.Fprintf(&b, "  - %s at %d:%d: %s\n", p.Severity, p.Line, p.Col, p.Message)
				} else if p.Line > 0 {
					fmt.Fprintf(&b, "  - %s at %d: %s\n", p.Severity, p.Line, p.Message)
				} else {
					fmt.Fprintf(&b, "  - %s: %s\n", p.Severity, p.Message)
				}
			}
		}
	}

	if res.CSFOK {
		fmt.Fprintln(&b, "validation: ok")
	} else {
		fmt.Fprintln(&b, "validation: FAILED")
	}
	if res.Warning != "" {
		fmt.Fprintf(&b, "warning: %s\n", res.Warning)
	}

	hasLoss := r != nil && len(r.RemovedFragments) > 0
	var next string
	switch {
	case res.Wrote:
		next = fmt.Sprintf("run `conf push %s` to publish", filepath.Base(res.CSFPath))
	case hasLoss:
		next = "restore the marker(s) in the .md, or re-run with --allow-fragment-loss to accept the loss"
	case res.DryRun:
		next = "apply without --dry-run to write"
	default:
		next = "resolve the errors above and re-run"
	}
	fmt.Fprintf(&b, "next: %s", next)
	return b.String()
}

// blockCountsLine renders the "blocks:" summary shared by conf and jira apply,
// omitting zero counts; mergedTables (conf only) is appended when non-zero. A
// jira caller passes 0 for mergedTables to skip it.
func blockCountsLine(unchanged, moved, converted, removed, mergedTables int) string {
	var parts []string
	if unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", unchanged))
	}
	if moved > 0 {
		parts = append(parts, fmt.Sprintf("%d moved", moved))
	}
	if converted > 0 {
		parts = append(parts, fmt.Sprintf("%d converted", converted))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", removed))
	}
	if mergedTables > 0 {
		unit := "tables merged"
		if mergedTables == 1 {
			unit = "table merged"
		}
		parts = append(parts, fmt.Sprintf("%d %s", mergedTables, unit))
	}
	if len(parts) == 0 {
		return "blocks: none"
	}
	return "blocks: " + strings.Join(parts, ", ")
}
