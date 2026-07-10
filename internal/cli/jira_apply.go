package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

// jiraApplyCmd implements `jira apply`: merge edits from an issue's markdown view
// back into the `.wiki` substrate/pending write set, block by block. Description
// and explicitly configured editable jira_wiki field sections are writable; an
// edit to generated metadata/title or any other section is detected and refused.
// Untouched native wiki blocks keep their exact base bytes;
// changed/new blocks convert from a strict markdown subset; a block it cannot
// convert (or a dropped wiki construct without --allow-loss) fails closed with a
// pointer to edit the .wiki directly. Local only — `jira push` remains the write
// path to the server. Offline: no network or PAT.
func jiraApplyCmd() *cobra.Command {
	var o app.JiraApplyOpts
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "apply <FILE.md>",
		Short: "Merge supported .md view edits into the guarded local Jira write set",
		Long: "Merge edits made in an issue's <KEY>.md view back into its <KEY>.wiki substrate.\n\n" +
			"The generated `# Description` section and field sections explicitly configured with " +
			"`editable:true` + `format:jira_wiki` are editable through the view; a change to the " +
			"generated metadata/title, Comments, Links, or Image Attachments is refused (exit 8) with a " +
			"pointer to the dedicated command. Untouched native wiki blocks keep their exact base " +
			"bytes; a wiki-only construct ({panel}, {color}, mentions, !embeds!, macros) dropped by " +
			"the edit is refused (exit 8) unless --allow-loss. Local only — run `jira push` to send " +
			"the merged .wiki and pending fields to the server under their drift gates.\n\n" +
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
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "report without committing wiki, pending, or view changes")
	cmd.Flags().BoolVar(&o.AllowLoss, "allow-loss", false,
		"proceed even when the edit drops wiki-only constructs (panels, colors, mentions, embeds, macros)")
	cmd.Flags().BoolVar(&o.RebasePending, "rebase-pending", false,
		"after a fresh pull and review, adopt raw snapshot field values as new pending drift bases")
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
	if res.Rebased {
		fmt.Fprintln(&b, "rebased: pending field bases updated from the raw snapshot")
	}
	for _, field := range res.Fields {
		state := "unchanged"
		if field.Pending {
			state = "pending"
		}
		fmt.Fprintf(&b, "field %s: %s\n", field.ID, state)
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
