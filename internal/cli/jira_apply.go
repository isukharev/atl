package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

// jiraApplyCmd implements `jira apply`: merge edits from an issue's markdown view
// back into the `.wiki` substrate, block by block. Only the `# Description`
// section is writable — an edit to generated metadata/title or to any other section
// (Comments, Links, Image Attachments) is detected and refused with a pointer to
// the dedicated command. Untouched Description blocks keep their exact base bytes;
// changed/new blocks convert from a strict markdown subset; a block it cannot
// convert (or a dropped wiki construct without --allow-loss) fails closed with a
// pointer to edit the .wiki directly. Local only — `jira push` remains the write
// path to the server. Offline: no network or PAT.
func jiraApplyCmd() *cobra.Command {
	var o app.JiraApplyOpts
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "apply <FILE.md>",
		Short: "Merge .md view edits into the .wiki (Description only; block-level, non-lossy)",
		Long: "Merge edits made in an issue's <KEY>.md view back into its <KEY>.wiki substrate.\n\n" +
			"Only the generated `# Description` section is editable through the view; a change to the " +
			"generated metadata/title, Comments, Links, or Image Attachments is refused (exit 8) with a " +
			"pointer to the dedicated command. Untouched Description blocks keep their exact base " +
			"bytes; a wiki-only construct ({panel}, {color}, mentions, !embeds!, macros) dropped by " +
			"the edit is refused (exit 8) unless --allow-loss. Local only — run `jira push` to send " +
			"the merged .wiki to the server under its drift gate.\n\n" +
			"The pristine view compared against the edit is reproduced from the render settings the " +
			".md was last written with (recorded on pull/render), so no flags are needed; pass " +
			"--render-* only to override that recorded view.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := rf.override()
			if err != nil {
				return err
			}
			o.Render = override
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			svc := app.NewJiraRenderer(cfg)
			res, aerr := svc.Apply(args[0], o)
			// Emit the result (with its report) even on a refusal so a loss/divergence
			// exit-8 still shows what would change; the apply error is the actionable
			// one and wins over any emit error.
			if res != nil && res.Report != nil {
				_ = emit(cmd, res, func() string { return jiraApplyText(res) })
			}
			return aerr
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "report the merge without writing files")
	cmd.Flags().BoolVar(&o.AllowLoss, "allow-loss", false,
		"proceed even when the edit drops wiki-only constructs (panels, colors, mentions, embeds, macros)")
	cmd.Flags().StringVar(&o.Into, "into", "", "mirror root (defaults to nearest .atl)")
	rf.register(cmd)
	return cmd
}

// jiraApplyText renders `jira apply`'s result as a compact human loss-review,
// the Jira analog of applyText: one fact per line, `key: value`, indented `- `
// lists, zero-count and empty sections omitted. Stable contract
// (see docs/OUTPUT_CONTRACT.md).
func jiraApplyText(res *app.JiraApplyResult) string {
	var b strings.Builder
	switch {
	case res.DryRun:
		fmt.Fprintln(&b, "dry-run: no files written")
	case res.Wrote:
		fmt.Fprintf(&b, "applied: %s\n", res.WikiPath)
	default:
		fmt.Fprintf(&b, "not applied: %s\n", res.WikiPath)
	}

	r := res.Report
	if r == nil {
		fmt.Fprintln(&b, "blocks: none")
	} else {
		fmt.Fprintln(&b, blockCountsLine(r.Unchanged, r.Moved, r.Converted, r.Removed, 0))
		if len(r.RemovedConstructs) > 0 {
			fmt.Fprintln(&b, "removed constructs:")
			for _, c := range r.RemovedConstructs {
				fmt.Fprintf(&b, "  - %s %q\n", c.Kind, c.Text)
			}
		}
	}

	if res.Warning != "" {
		fmt.Fprintf(&b, "warning: %s\n", res.Warning)
	}

	hasLoss := r != nil && len(r.RemovedConstructs) > 0
	var next string
	switch {
	case res.Wrote:
		next = fmt.Sprintf("run `jira push %s` to publish", filepath.Base(res.WikiPath))
	case hasLoss:
		next = "restore the construct(s) in the .md, or re-run with --allow-loss to accept the loss"
	case res.DryRun:
		next = "apply without --dry-run to write"
	default:
		next = "resolve the errors above and re-run"
	}
	fmt.Fprintf(&b, "next: %s", next)
	return b.String()
}
