package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

type jiraGuardedLabelFlags struct {
	add, remove  string
	apply        bool
	expectedHash string
}

func jiraIssueLabelsCmd() *cobra.Command {
	parent := newJiraGuardedLabelLeaf(&jiraGuardedLabelFlags{}, false)
	parent.AddCommand(newJiraGuardedLabelLeaf(&jiraGuardedLabelFlags{}, true))
	return parent
}

func newJiraGuardedLabelLeaf(flags *jiraGuardedLabelFlags, previewOnly bool) *cobra.Command {
	use, short := "labels <KEY>", "Preview or apply one reviewed Jira label add/remove"
	if previewOnly {
		use, short = "preview <KEY>", "Build a Jira label proposal without writing"
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := flags.opts(cmd, previewOnly)
			var err error
			if opts, err = app.NormalizeJiraGuardedLabelOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.GuardedLabels(cmd.Context(), args[0], opts)
			if result == nil {
				return mutationErr
			}
			emitErr := emit(cmd, result, nil)
			return guardedMutationResultErr(mutationErr, emitErr, result.WriteAttempted, "Jira guarded labels")
		},
	}
	cmd.Flags().StringVar(&flags.add, "add", "", "comma-separated labels to add")
	cmd.Flags().StringVar(&flags.remove, "remove", "", "comma-separated labels to remove")
	if !previewOnly {
		cmd.Flags().BoolVar(&flags.apply, "apply", false, "send the sole reviewed label write attempt")
		cmd.Flags().StringVar(&flags.expectedHash, "expected-proposal-hash", "", "exact lowercase SHA-256 from the reviewed preview")
	}
	return cmd
}

func (flags *jiraGuardedLabelFlags) opts(cmd *cobra.Command, previewOnly bool) app.JiraGuardedLabelOpts {
	var add, remove []string
	if cmd.Flags().Changed("add") {
		add = strings.Split(flags.add, ",")
	}
	if cmd.Flags().Changed("remove") {
		remove = strings.Split(flags.remove, ",")
	}
	return app.JiraGuardedLabelOpts{
		Add: add, Remove: remove, Apply: !previewOnly && flags.apply,
		ExpectedProposalHash: strings.TrimSpace(flags.expectedHash),
	}
}

func validateJiraGuardedLabelInvocation(cmd *cobra.Command, applyRequested bool) error {
	if _, err := app.ValidateJiraGuardedLabelKey(cmd.Flags().Arg(0)); err != nil {
		return err
	}
	addRaw, _ := cmd.Flags().GetString("add")
	removeRaw, _ := cmd.Flags().GetString("remove")
	var add, remove []string
	if cmd.Flags().Changed("add") {
		add = strings.Split(addRaw, ",")
	}
	if cmd.Flags().Changed("remove") {
		remove = strings.Split(removeRaw, ",")
	}
	expected := ""
	if cmd.Flags().Lookup("expected-proposal-hash") != nil {
		expected, _ = cmd.Flags().GetString("expected-proposal-hash")
		if !applyRequested && cmd.Flags().Changed("expected-proposal-hash") {
			return usageErr("--expected-proposal-hash requires --apply")
		}
	}
	_, err := app.NormalizeJiraGuardedLabelOpts(app.JiraGuardedLabelOpts{
		Add: add, Remove: remove, Apply: applyRequested, ExpectedProposalHash: strings.TrimSpace(expected),
	})
	return err
}
