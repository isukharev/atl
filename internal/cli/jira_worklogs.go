package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraIssueWorklogCmd() *cobra.Command {
	group := &cobra.Command{Use: "worklog", Short: "List or safely add issue worklogs"}
	group.AddCommand(jiraIssueWorklogListCmd(), jiraIssueWorklogAddCmd())
	return group
}

func jiraIssueWorklogListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <KEY>",
		Short: "List the complete worklog history of an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.ListWorklogs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return app.JiraWorklogListMarkdown(result) }, func() []string {
				ids := make([]string, 0, len(result.Worklogs))
				for _, worklog := range result.Worklogs {
					ids = append(ids, worklog.ID)
				}
				return ids
			})
		},
	}
}

func jiraIssueWorklogAddCmd() *cobra.Command {
	var timeValue, inlineComment, fromFile, started, expectedProposalHash string
	var applyWrite bool
	cmd := &cobra.Command{
		Use:   "add <KEY>",
		Short: "Add one worklog through a reviewed dry-run",
		Long: "Preview by default after reading a complete worklog baseline. Apply requires the exact proposal hash, " +
			"sends one POST with the remaining estimate unchanged, and reconciles an ambiguous response without replay.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := app.NormalizeJiraWorklogDuration(timeValue); err != nil {
				return err
			}
			if _, err := app.NormalizeJiraWorklogStarted(started); err != nil {
				return err
			}
			if cmd.Flags().Changed("comment") && cmd.Flags().Changed("from-file") {
				return usageErr("--comment and --from-file are mutually exclusive")
			}
			if applyWrite && strings.TrimSpace(expectedProposalHash) == "" {
				return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first")
			}
			comment, err := jiraWorklogComment(cmd, inlineComment, fromFile)
			if err != nil {
				return err
			}
			if _, err := app.ValidateJiraWorklogComment(comment); err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, mutationErr := svc.AddWorklogGuarded(cmd.Context(), args[0], app.JiraWorklogAddOpts{
				Time: timeValue, Comment: comment, Started: started, Apply: applyWrite,
				ExpectedProposalHash: expectedProposalHash,
			})
			if result != nil {
				if emitErr := emit(cmd, result, func() string { return app.JiraWorklogAddMarkdown(result) }); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&timeValue, "time", "", "positive h/m/s duration such as 1h30m (required)")
	cmd.Flags().StringVar(&inlineComment, "comment", "", "short worklog comment (visible in the process list; prefer --from-file)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "bounded worklog comment file, or - for stdin")
	cmd.Flags().StringVar(&started, "started", "", "work start time in RFC3339 with an explicit timezone (default: Jira current time)")
	cmd.Flags().StringVar(&expectedProposalHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")
	cmd.Flags().BoolVar(&applyWrite, "apply", false, "perform the guarded write (default: dry-run)")
	return cmd
}

func jiraWorklogComment(cmd *cobra.Command, inline, path string) (string, error) {
	if !cmd.Flags().Changed("from-file") {
		return inline, nil
	}
	if path == "" {
		return "", usageErr("--from-file requires a file path or - for stdin")
	}
	var (
		data []byte
		err  error
	)
	if path == "-" {
		if stdinIsTerminal() {
			return "", usageErr("stdin is a terminal and no worklog comment was piped; pass --from-file FILE or pipe the comment")
		}
		data, err = readBounded(os.Stdin, app.JiraWorklogCommentMaxBytes)
	} else {
		data, err = readFileBounded(path, app.JiraWorklogCommentMaxBytes)
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
