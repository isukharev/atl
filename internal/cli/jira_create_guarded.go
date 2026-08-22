package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type jiraGuardedCreateFlags struct {
	project, issueType, summary string
	fromFile, fromMD            string
	fieldKV, fieldJSON          []string
	register                    bool
	into                        string
	apply                       bool
	expectedHash                string
}

func jiraIssueCreateCmd() *cobra.Command {
	flags := &jiraGuardedCreateFlags{}
	parent := newJiraGuardedCreateLeaf(flags, false)
	parent.AddCommand(newJiraGuardedCreateLeaf(&jiraGuardedCreateFlags{}, true))
	return parent
}

func newJiraGuardedCreateLeaf(flags *jiraGuardedCreateFlags, previewOnly bool) *cobra.Command {
	use, short := "create", "Preview or apply one reviewed Jira issue create"
	if previewOnly {
		use, short = "preview", "Build a Jira issue create proposal without writing"
	}
	cmd := &cobra.Command{
		Use: use, Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inputs, err := parseJiraFieldInputs(flags.fieldKV, flags.fieldJSON, true)
			if err != nil {
				return err
			}
			// Keep the leaf safe when invoked outside the root command as well:
			// pure candidate validation must precede file/stdin reads and service
			// assembly. The root preflight performs the same check before config
			// and self-update for normal CLI execution.
			if err := app.ValidateJiraGuardedCreateOpts(flags.opts(cmd, inputs, nil, previewOnly)); err != nil {
				return err
			}
			body, err := wikiBody(cmd, flags.fromFile, flags.fromMD)
			if err != nil {
				return err
			}
			opts := flags.opts(cmd, inputs, body, previewOnly)
			if err := app.ValidateJiraGuardedCreateOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, createErr := svc.GuardedCreate(cmd.Context(), opts)
			if result == nil {
				return createErr
			}
			var emitErr error
			if invocationRuntimeFor(cmd).outputFormat == "id" {
				emitErr = emitID(cmd, result, nil, func() []string {
					if result.Status == "applied" && result.Issue != nil {
						return []string{result.Issue.Key}
					}
					return nil
				})
			} else {
				emitErr = emit(cmd, result, nil)
			}
			return guardedCreateResultErr(createErr, emitErr, result.WriteAttempted)
		},
	}
	bindJiraGuardedCreateFlags(cmd, flags, previewOnly)
	return cmd
}

func guardedCreateResultErr(createErr, emitErr error, attempted bool) error {
	if emitErr == nil {
		return createErr
	}
	emitCause := fmt.Errorf("write Jira issue create result: %w", emitErr)
	if createErr != nil {
		// Guarded create has an exact closed outcome model. Preserve its typed
		// sentinel and ambiguity marker even when the result stream fails.
		return errors.Join(createErr, emitCause)
	}
	if !attempted {
		return emitCause
	}
	return errors.Join(
		fmt.Errorf("%w: the remote Jira issue create was attempted, but the result could not be written; do not replay the operation", domain.ErrCheckFailed),
		emitCause,
	)
}

func bindJiraGuardedCreateFlags(cmd *cobra.Command, flags *jiraGuardedCreateFlags, previewOnly bool) {
	cmd.Flags().StringVar(&flags.project, "project", "", "canonical project key")
	cmd.Flags().StringVar(&flags.issueType, "type", "", "exact issue type id or name from qualified create metadata")
	cmd.Flags().StringVar(&flags.summary, "summary", "", "summary")
	cmd.Flags().StringVar(&flags.fromFile, "from-file", "", "description (native wiki) file or - for stdin")
	cmd.Flags().StringVar(&flags.fromMD, "from-md", "", "markdown description file or - for stdin (converted to native wiki)")
	cmd.Flags().StringArrayVar(&flags.fieldKV, "field", nil, "extra field key=value (repeatable); objects/arrays retain legacy coercion")
	cmd.Flags().StringArrayVar(&flags.fieldJSON, "field-json", nil, "extra field key=JSON (repeatable); preserves explicit JSON scalars")
	cmd.Flags().BoolVar(&flags.register, "register", false, "register the proved issue in the mirror named by --into")
	cmd.Flags().StringVar(&flags.into, "into", "", "mirror root for explicit post-create registration (requires --register)")
	if !previewOnly {
		cmd.Flags().BoolVar(&flags.apply, "apply", false, "send the sole reviewed create attempt")
		cmd.Flags().StringVar(&flags.expectedHash, "expected-proposal-hash", "", "exact lowercase SHA-256 from the reviewed preview")
	}
}

func (flags *jiraGuardedCreateFlags) opts(cmd *cobra.Command, fields map[string]domain.JiraFieldInput, body []byte, previewOnly bool) app.JiraGuardedCreateOpts {
	source := "none"
	if cmd.Flags().Changed("from-file") {
		source = "wiki"
	}
	if cmd.Flags().Changed("from-md") {
		source = "markdown"
	}
	return app.JiraGuardedCreateOpts{
		Project: strings.ToUpper(strings.TrimSpace(flags.project)), IssueType: strings.TrimSpace(flags.issueType),
		Summary: flags.summary, Description: body, DescriptionSource: source, Fields: fields,
		Register: flags.register, Into: strings.TrimSpace(flags.into), Apply: !previewOnly && flags.apply,
		ExpectedProposalHash: strings.TrimSpace(flags.expectedHash),
	}
}

func validateJiraGuardedCreateInvocation(cmd *cobra.Command, applyRequested bool) error {
	fieldKV, kvErr := cmd.Flags().GetStringArray("field")
	fieldJSON, jsonErr := cmd.Flags().GetStringArray("field-json")
	if kvErr != nil || jsonErr != nil {
		return usageErr("invalid Jira create field flags")
	}
	if len(fieldKV)+len(fieldJSON) > 1000 {
		return usageErr("Jira create accepts at most 1000 supplied fields")
	}
	fields, err := parseJiraFieldInputs(fieldKV, fieldJSON, true)
	if err != nil {
		return err
	}
	fromFile, _ := cmd.Flags().GetString("from-file")
	fromMD, _ := cmd.Flags().GetString("from-md")
	if cmd.Flags().Changed("from-file") && cmd.Flags().Changed("from-md") {
		return usageErr("--from-file and --from-md are mutually exclusive")
	}
	if cmd.Flags().Changed("from-md") && strings.TrimSpace(fromMD) == "" {
		return usageErr("--from-md requires a file path or - for stdin")
	}
	if cmd.Flags().Changed("from-file") && strings.TrimSpace(fromFile) == "" {
		return usageErr("--from-file requires a file path or - for stdin")
	}
	register, _ := cmd.Flags().GetBool("register")
	into, _ := cmd.Flags().GetString("into")
	expected, _ := cmd.Flags().GetString("expected-proposal-hash")
	if !applyRequested && cmd.Flags().Lookup("expected-proposal-hash") != nil && cmd.Flags().Changed("expected-proposal-hash") {
		return usageErr("--expected-proposal-hash requires --apply")
	}
	if invocationRuntimeFor(cmd).outputFormat == "id" && !applyRequested {
		return usageErr("-o id is apply-only for Jira issue create")
	}
	source := "none"
	if cmd.Flags().Changed("from-file") {
		source = "wiki"
	} else if cmd.Flags().Changed("from-md") {
		source = "markdown"
	}
	project, _ := cmd.Flags().GetString("project")
	issueType, _ := cmd.Flags().GetString("type")
	summary, _ := cmd.Flags().GetString("summary")
	return app.ValidateJiraGuardedCreateOpts(app.JiraGuardedCreateOpts{
		Project: strings.ToUpper(strings.TrimSpace(project)), IssueType: strings.TrimSpace(issueType), Summary: summary,
		DescriptionSource: source, Fields: fields, Register: register, Into: strings.TrimSpace(into),
		Apply: applyRequested, ExpectedProposalHash: strings.TrimSpace(expected),
	})
}
