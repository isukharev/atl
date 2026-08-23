package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
)

// jiraCommentCmd builds `jira issue comment {preview,add,list,delete}`.
func jiraCommentCmd() *cobra.Command {
	c := &cobra.Command{Use: "comment", Short: "Preview/list/add/delete issue comments"}

	preview := jiraCommentMutationCmd(false)
	add := jiraCommentMutationCmd(true)

	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List an issue's comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			cs, err := svc.Comments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"key": args[0], "comments": cs}, func() string {
				var b strings.Builder
				for _, cm := range cs {
					fmt.Fprintf(&b, "%s\t%s (%s):\n%s\n\n", cm.ID, cm.Author, cm.Created, cm.Body)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(cs))
				for i, cm := range cs {
					ids[i] = cm.ID
				}
				return ids
			})
		},
	}

	del := &cobra.Command{
		Use:   "delete <KEY> <COMMENT-ID>",
		Short: "Delete a comment by id",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			if err := svc.DeleteComment(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "comment": args[1], "status": "deleted"}, nil)
		},
	}

	c.AddCommand(preview, add, list, del)
	return c
}

func jiraCommentMutationCmd(applyCapable bool) *cobra.Command {
	var fromFile, fromMD string
	guardedWrite := guardedWriteFlags{profile: guardedWriteProposal}
	use, short := "preview <KEY>", "Preview a bounded Jira-wiki comment"
	if applyCapable {
		use, short = "add <KEY>", "Preview or apply a bounded Jira-wiki comment"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: "Preview by default against a complete comment-id baseline. Apply requires the exact proposal hash, " +
			"sends at most one POST, and reconciles the outcome without replay.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := jiraCommentBody(cmd, orDash(fromFile), fromMD)
			if err != nil {
				return err
			}
			body, err = app.ValidateJiraCommentBody(body)
			if err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.AddCommentGuarded(cmd.Context(), args[0], app.JiraCommentAddOpts{
				Body: body, Apply: applyCapable && guardedWrite.apply,
				ExpectedProposalHash: guardedWrite.expectedProposalHash, SatisfactionPolicy: "append_always",
			})
			if result == nil {
				return mutationErr
			}
			emitErr := emit(cmd, result, func() string { return app.JiraCommentAddText(result) })
			return guardedMutationResultErr(mutationErr, emitErr, result.WriteAttempted, "Jira guarded comment append")
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "-", "bounded Jira-wiki comment body file or - for stdin")
	cmd.Flags().StringVar(&fromMD, "from-md", "", "bounded markdown comment file or - for stdin (converted to wiki; unsupported constructs are refused)")
	if applyCapable {
		guardedWrite.register(cmd)
	}
	return cmd
}

func validateJiraGuardedCommentInvocation(cmd *cobra.Command, applyRequested bool) error {
	if _, err := app.ValidateJiraGuardedCommentKey(cmd.Flags().Arg(0)); err != nil {
		return err
	}
	if cmd.Flags().Changed("from-file") && cmd.Flags().Changed("from-md") {
		return usageErr("--from-file and --from-md are mutually exclusive")
	}
	if cmd.Flags().Changed("from-file") {
		value, _ := cmd.Flags().GetString("from-file")
		if strings.TrimSpace(value) == "" {
			return usageErr("--from-file requires a file path or - for stdin")
		}
	}
	if cmd.Flags().Changed("from-md") {
		value, _ := cmd.Flags().GetString("from-md")
		if strings.TrimSpace(value) == "" {
			return usageErr("--from-md requires a file path or - for stdin")
		}
	}
	expected := ""
	if cmd.Flags().Lookup("expected-proposal-hash") != nil {
		expected, _ = cmd.Flags().GetString("expected-proposal-hash")
		if !applyRequested && cmd.Flags().Changed("expected-proposal-hash") {
			return usageErr("--expected-proposal-hash requires --apply")
		}
	}
	if !applyRequested {
		return nil
	}
	if strings.TrimSpace(expected) == "" {
		return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first")
	}
	return app.ValidateJiraDescriptionEditReviewHash(strings.TrimSpace(expected))
}

func jiraCommentBody(cmd *cobra.Command, fromFile, fromMD string) ([]byte, error) {
	if !cmd.Flags().Changed("from-md") {
		return readJiraCommentBody(fromFile)
	}
	if fromMD == "" {
		return nil, usageErr("--from-md requires a file path or - for stdin")
	}
	if cmd.Flags().Changed("from-file") {
		return nil, usageErr("--from-file and --from-md are mutually exclusive")
	}
	markdown, err := readJiraCommentBody(fromMD)
	if err != nil {
		return nil, err
	}
	wiki, err := mdwiki.ConvertDocument(string(markdown))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot convert markdown body: %v (constructs outside the md subset need a wiki body via --from-file)", domain.ErrCheckFailed, err)
	}
	return []byte(wiki), nil
}

func readJiraCommentBody(path string) ([]byte, error) {
	switch path {
	case "":
		return nil, nil
	case "-":
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no body was piped; pass --from-file FILE (or --from-md FILE where supported), or pipe the body")
		}
		return readBounded(os.Stdin, app.JiraCommentBodyMaxBytes)
	default:
		body, err := readFileBounded(path, app.JiraCommentBodyMaxBytes)
		if err == nil {
			return body, nil
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: body file %q does not exist", domain.ErrNotFound, path)
		}
		if errors.Is(err, domain.ErrUsage) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: read body file %q: %v", domain.ErrCheckFailed, path, err)
	}
}
