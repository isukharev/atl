package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraDescriptionEditCmd() *cobra.Command {
	parent := newJiraDescriptionEditLeaf(false)
	parent.AddCommand(newJiraDescriptionEditLeaf(true))
	return parent
}

func newJiraDescriptionEditLeaf(previewOnly bool) *cobra.Command {
	var oldText, newText, oldFile, newFile string
	var all, dryRun bool
	guard := guardedWriteFlags{profile: guardedWriteProposal}
	use := "edit <KEY>"
	short := "Preview or apply a reviewed targeted description edit"
	if previewOnly {
		use = "preview <KEY>"
		short = "Preview a targeted description edit through a read-only command"
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !previewOnly && cmd.Flags().Changed("dry-run") {
				if !dryRun {
					return usageErr("--dry-run=false is not supported; omit --dry-run to preview")
				}
				if guard.apply {
					return usageErr("--dry-run cannot be combined with --apply")
				}
			}
			if !previewOnly {
				if err := guard.validate(); err != nil {
					return err
				}
				if guard.apply {
					if err := app.ValidateJiraDescriptionEditReviewHash(guard.expectedProposalHash); err != nil {
						return err
					}
				}
			}
			old, err := textFromFlagPair(oldText, oldFile, "--old")
			if err != nil {
				return err
			}
			replacement, err := textFromFlagPair(newText, newFile, "--new")
			if err != nil {
				return err
			}
			if old == "" {
				return usageErr("--old (or --old-file) is required and must be non-empty")
			}
			if !cmd.Flags().Changed("new") && newFile == "" {
				return usageErr("--new (or --new-file) is required (pass --new '' to delete the matched text)")
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, editErr := svc.EditDescriptionGuarded(cmd.Context(), args[0], app.JiraDescriptionEditOpts{
				Old: []byte(old), New: []byte(replacement), All: all,
				Apply:                !previewOnly && guard.apply,
				ExpectedProposalHash: guard.expectedProposalHash,
			})
			if result == nil {
				return editErr
			}
			emitErr := emit(cmd, result, func() string { return app.JiraDescriptionEditText(result) })
			return guardedMutationResultErr(editErr, emitErr, result.WriteAttempted, "Jira description edit")
		},
	}
	cmd.Flags().StringVar(&oldText, "old", "", "text to find in the description (tolerant of NBSP/zero-width/entity differences)")
	cmd.Flags().StringVar(&newText, "new", "", "replacement text (native wiki, inserted verbatim)")
	cmd.Flags().StringVar(&oldFile, "old-file", "", "read the text to find from a file (- for stdin; one trailing newline is stripped)")
	cmd.Flags().StringVar(&newFile, "new-file", "", "read the replacement from a file (one trailing newline is stripped)")
	cmd.Flags().BoolVar(&all, "all", false, "replace every match instead of requiring a unique one")
	if !previewOnly {
		guard.register(cmd)
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compatibility alias for the default preview")
	}
	return cmd
}
