package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type jiraGuardedUpdateFlags struct {
	summary              string
	fromFile             string
	fromMD               string
	fieldKV              []string
	fieldJSON            []string
	apply                bool
	expectedProposalHash string
}

func jiraIssueUpdateCmd() *cobra.Command {
	parent := newJiraGuardedUpdateLeaf(&jiraGuardedUpdateFlags{}, false)
	parent.AddCommand(newJiraGuardedUpdateLeaf(&jiraGuardedUpdateFlags{}, true))
	return parent
}

func newJiraGuardedUpdateLeaf(flags *jiraGuardedUpdateFlags, previewOnly bool) *cobra.Command {
	use, short := "update <KEY>", "Preview or apply one reviewed whole-issue update"
	if previewOnly {
		use, short = "preview <KEY>", "Build a whole-issue update proposal without writing"
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		Long: "Preview an exact atomic update of summary, whole description, and catalog-qualified custom fields. " +
			"The parent previews by default and writes only with --apply plus the reviewed proposal hash. " +
			"The preview child remains available under a read-only policy.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateJiraGuardedUpdateInvocation(cmd, flags.apply && !previewOnly); err != nil {
				return err
			}
			fields, err := parseJiraGuardedUpdateFieldInputs(flags.fieldKV, flags.fieldJSON)
			if err != nil {
				return err
			}
			description, present, source, err := readJiraGuardedUpdateDescription(cmd, flags)
			if err != nil {
				return err
			}
			opts := app.JiraGuardedUpdateOpts{
				Summary: flags.summary, SummaryPresent: cmd.Flags().Changed("summary"),
				Description: description, DescriptionPresent: present, DescriptionSource: source,
				Fields: fields, Apply: flags.apply && !previewOnly, ExpectedProposalHash: strings.TrimSpace(flags.expectedProposalHash),
			}
			if err := app.ValidateJiraGuardedUpdateOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, updateErr := svc.UpdateIssueGuarded(cmd.Context(), args[0], opts)
			if result == nil {
				return updateErr
			}
			emitErr := emit(cmd, result, nil)
			return guardedUpdateResultErr(updateErr, emitErr, result.WriteAttempted)
		},
	}
	cmd.Flags().StringVar(&flags.summary, "summary", "", "new non-empty summary")
	cmd.Flags().StringVar(&flags.fromFile, "from-file", "", "new Jira-wiki description file or - for stdin")
	cmd.Flags().StringVar(&flags.fromMD, "from-md", "", "new Markdown description file or - for stdin (converted before backend access)")
	cmd.Flags().StringArrayVar(&flags.fieldKV, "field", nil, "qualified custom field key=value (repeatable; strict object/array JSON stays structured)")
	cmd.Flags().StringArrayVar(&flags.fieldJSON, "field-json", nil, "qualified custom field key=JSON (repeatable; explicit JSON scalars are supported)")
	if !previewOnly {
		cmd.Flags().BoolVar(&flags.apply, "apply", false, "perform the reviewed update (default: dry-run)")
		cmd.Flags().StringVar(&flags.expectedProposalHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")
	}
	return cmd
}

func guardedUpdateResultErr(updateErr, emitErr error, attempted bool) error {
	if emitErr == nil {
		return updateErr
	}
	emitCause := fmt.Errorf("write Jira guarded issue update result: %w", emitErr)
	if updateErr != nil {
		return errors.Join(updateErr, emitCause)
	}
	if !attempted {
		return emitCause
	}
	return errors.Join(
		fmt.Errorf("%w: the remote Jira issue update was attempted, but the result could not be written; do not replay the operation", domain.ErrCheckFailed),
		emitCause,
	)
}

func readJiraGuardedUpdateDescription(cmd *cobra.Command, flags *jiraGuardedUpdateFlags) ([]byte, bool, string, error) {
	fileChanged, mdChanged := cmd.Flags().Changed("from-file"), cmd.Flags().Changed("from-md")
	if fileChanged && mdChanged {
		return nil, false, "", usageErr("--from-file and --from-md are mutually exclusive")
	}
	if !fileChanged && !mdChanged {
		return nil, false, "none", nil
	}
	path, source := flags.fromFile, "wiki"
	if mdChanged {
		path, source = flags.fromMD, "markdown"
	}
	if path == "" {
		return nil, false, "", usageErr("description path must not be empty")
	}
	if path != "-" {
		data, err := readFileBounded(path, domain.JiraGuardedUpdateMaxInputBytes)
		return data, true, source, err
	}
	if stdinIsTerminal() {
		return nil, false, "", usageErr("stdin is a terminal; pass a description file or pipe the body")
	}
	data, err := readBounded(os.Stdin, domain.JiraGuardedUpdateMaxInputBytes)
	return data, true, source, err
}

// validateJiraGuardedUpdateInvocation is the flag-only preconfiguration gate
// shared by the guarded parent and its read-only preview child.
func validateJiraGuardedUpdateInvocation(cmd *cobra.Command, applyRequested bool) error {
	if _, err := app.ValidateJiraGuardedFieldKey(cmd.Flags().Arg(0)); err != nil {
		return err
	}
	fromFile, fileErr := cmd.Flags().GetString("from-file")
	fromMD, mdErr := cmd.Flags().GetString("from-md")
	fieldKV, fieldErr := cmd.Flags().GetStringArray("field")
	fieldJSON, jsonErr := cmd.Flags().GetStringArray("field-json")
	if fileErr != nil || mdErr != nil || fieldErr != nil || jsonErr != nil {
		return usageErr("invalid guarded update input flags")
	}
	fileChanged, mdChanged := cmd.Flags().Changed("from-file"), cmd.Flags().Changed("from-md")
	if fileChanged && mdChanged {
		return usageErr("--from-file and --from-md are mutually exclusive")
	}
	if fileChanged && fromFile == "" || mdChanged && fromMD == "" {
		return usageErr("description path must not be empty")
	}
	fields, err := parseJiraGuardedUpdateFieldInputs(fieldKV, fieldJSON)
	if err != nil {
		return err
	}
	if err := app.ValidateJiraGuardedUpdateFieldInputs(fields); err != nil {
		return err
	}
	if !cmd.Flags().Changed("summary") && !fileChanged && !mdChanged && len(fields) == 0 {
		return usageErr("at least one of --summary, --from-file, --from-md, --field, or --field-json is required")
	}
	if cmd.Flags().Changed("summary") {
		summary, summaryErr := cmd.Flags().GetString("summary")
		if summaryErr != nil || summary == "" {
			return usageErr("--summary must be non-empty")
		}
	}
	expected := cmd.Flags().Lookup("expected-proposal-hash")
	if !applyRequested {
		if expected != nil && expected.Changed {
			return usageErr("--expected-proposal-hash requires --apply")
		}
		return nil
	}
	if expected == nil || !expected.Changed || strings.TrimSpace(expected.Value.String()) == "" {
		return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first")
	}
	return app.ValidateJiraDescriptionEditReviewHash(strings.TrimSpace(expected.Value.String()))
}

func parseJiraGuardedUpdateFieldInputs(fieldPairs, jsonPairs []string) (map[string]domain.JiraFieldInput, error) {
	fields := make(map[string]domain.JiraFieldInput, len(fieldPairs)+len(jsonPairs))
	for _, input := range []struct {
		pairs []string
		flag  string
		json  bool
	}{{fieldPairs, "--field", false}, {jsonPairs, "--field-json", true}} {
		for _, pair := range input.pairs {
			if _, _, ok := strings.Cut(pair, "="); !ok {
				return nil, usageErr("%s must be key=value", input.flag)
			}
			if err := addJiraFieldInput(fields, pair, input.flag, input.json, true); err != nil {
				return nil, err
			}
		}
	}
	return fields, nil
}
