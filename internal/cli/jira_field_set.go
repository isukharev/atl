package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
	"github.com/isukharev/atl/internal/strictjson"
)

func jiraIssueFieldCmd() *cobra.Command {
	group := &cobra.Command{Use: "field", Short: "Exact field evidence and guarded custom-field operations"}
	group.AddCommand(jiraIssueFieldGetCmd(), jiraIssueFieldBatchCmd(), jiraIssueFieldMutationCmd(false), jiraIssueFieldMutationCmd(true))
	return group
}

func jiraIssueFieldMutationCmd(applyCapable bool) *cobra.Command {
	var rawSpecs, mdSpecs []string
	var allowFields, expectedUpdated string
	guardedWrite := guardedWriteFlags{profile: guardedWriteCapturedAggregateProposal}
	use, short := "preview <KEY>", "Preview bounded file-backed custom-field values"
	if applyCapable {
		use, short = "set <KEY>", "Preview or apply bounded file-backed custom-field values"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Long: "Read custom-field values from bounded files/stdin, fresh-check Jira updated, and preview by default. " +
			"All invalid UTF-8 is rejected before raw classification. Complete strict JSON objects/arrays within the 9,997-level value bound stay structured; scalars and candidates with no decodable complete value remain strings, while strict violations fail. Markdown is converted to a Jira-wiki string. " +
			"Use field preview under a read-only policy; field set apply requires --expected-updated and --expected-proposal-hash from that reviewed preview.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			proposals, err := jiraFieldProposals(rawSpecs, mdSpecs)
			if err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			res, setErr := svc.SetFieldsGuarded(cmd.Context(), args[0], app.JiraFieldSetOpts{
				Proposals: proposals, AllowFields: jiraGuardedFieldAllowlist(allowFields),
				ExpectedUpdated: expectedUpdated, ExpectedProposalHash: guardedWrite.expectedProposalHash, Apply: guardedWrite.apply,
			})
			if res == nil {
				return setErr
			}
			emitErr := emit(cmd, res, func() string { return jiraFieldSetText(res) })
			return guardedMutationResultErr(setErr, emitErr, res.WriteAttempted, "Jira guarded fields")
		},
	}
	command.Flags().StringArrayVar(&rawSpecs, "from-file", nil, "FIELD=PATH raw value file (repeatable; - reads stdin; complete strict object/array JSON stays structured)")
	command.Flags().StringArrayVar(&mdSpecs, "from-md", nil, "FIELD=PATH Markdown file (repeatable; - reads stdin; converted to a Jira-wiki string)")
	command.Flags().StringVar(&allowFields, "allow-fields", "", "comma-separated exact custom field ids allowed by this operation (required)")
	if applyCapable {
		command.Flags().StringVar(&expectedUpdated, "expected-updated", "", "reviewed Jira updated value (required with --apply; preview captures it)")
		guardedWrite.register(command)
	}
	return command
}

func jiraFieldProposals(rawSpecs, mdSpecs []string) ([]app.JiraFieldProposal, error) {
	return jiraFieldProposalsWithLimit(rawSpecs, mdSpecs, int64(app.JiraFieldSetValueCap))
}

func jiraFieldProposalsWithLimit(rawSpecs, mdSpecs []string, limit int64) ([]app.JiraFieldProposal, error) {
	type input struct {
		field    string
		path     string
		markdown bool
	}
	type rawInput struct {
		spec     string
		markdown bool
	}
	rawInputs := make([]rawInput, 0, len(rawSpecs)+len(mdSpecs))
	for _, spec := range rawSpecs {
		rawInputs = append(rawInputs, rawInput{spec: spec})
	}
	for _, spec := range mdSpecs {
		rawInputs = append(rawInputs, rawInput{spec: spec, markdown: true})
	}
	if len(rawInputs) == 0 {
		return nil, usageErr("at least one --from-file FIELD=PATH or --from-md FIELD=PATH is required")
	}
	if len(rawInputs) > domain.JiraGuardedFieldMaxSelected {
		return nil, usageErr("at most %d field inputs are allowed", domain.JiraGuardedFieldMaxSelected)
	}
	inputs := make([]input, 0, len(rawInputs))
	stdinCount := 0
	seen := make(map[string]bool, len(rawInputs))
	for _, raw := range rawInputs {
		field, path, ok := strings.Cut(raw.spec, "=")
		field, path = strings.TrimSpace(field), strings.TrimSpace(path)
		if !ok || !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) || path == "" {
			return nil, usageErr("field input must be FIELD=PATH, got %q", raw.spec)
		}
		if seen[field] {
			return nil, usageErr("duplicate input for field %q", field)
		}
		seen[field] = true
		if path == "-" {
			stdinCount++
		}
		inputs = append(inputs, input{field: field, path: path, markdown: raw.markdown})
	}
	if stdinCount > 1 {
		return nil, usageErr("stdin (-) may be used by only one field input")
	}
	remaining := limit
	proposals := make([]app.JiraFieldProposal, 0, len(inputs))
	for _, in := range inputs {
		field, path := in.field, in.path
		data, err := readJiraFieldInput(path, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		if !utf8.Valid(data) {
			return nil, usageErr("field %q input is not valid UTF-8", field)
		}
		proposal := app.JiraFieldProposal{Field: field, Source: "raw", InputBytes: len(data)}
		if in.markdown {
			wiki, err := mdwiki.ConvertDocument(string(data))
			if err != nil {
				return nil, fmt.Errorf("%w: field %q markdown cannot be converted: %v", domain.ErrCheckFailed, field, err)
			}
			proposal.Source, proposal.Value = "markdown", wiki
		} else {
			proposal.Value, err = rawJiraFieldValue(data)
			if err != nil {
				return nil, fmt.Errorf("%w: field %q: %v", domain.ErrUsage, field, err)
			}
		}
		proposals = append(proposals, proposal)
	}
	return proposals, nil
}

func readJiraFieldInput(path string, max int64) ([]byte, error) {
	if max < 0 {
		max = 0
	}
	if path != "-" {
		return readFileBounded(path, max)
	}
	if stdinIsTerminal() {
		return nil, usageErr("stdin is a terminal; pass a FIELD=PATH input or pipe the value")
	}
	return readBounded(os.Stdin, max)
}

func rawJiraFieldValue(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("input is not valid UTF-8")
	}
	first := 0
	for first < len(data) && isJSONWhitespace(data[first]) {
		first++
	}
	if first == len(data) || data[first] != '{' && data[first] != '[' {
		return string(data), nil
	}
	if err := strictjson.ValidateNestingDepth(data, domain.JiraGuardedFieldMaxValueNestingDepth); err != nil {
		if errors.Is(err, strictjson.ErrNestingDepth) {
			return nil, fmt.Errorf("structured JSON input exceeds the supported value nesting bound")
		}
		return nil, fmt.Errorf("structured JSON input is invalid")
	}
	decoded, _, err := strictjson.DecodeFirst(data)
	if err != nil {
		if errors.Is(err, strictjson.ErrNestingDepth) {
			return nil, fmt.Errorf("structured JSON input exceeds the supported nesting bound")
		}
		return string(data), nil
	}
	if err := strictjson.Validate(data); err != nil {
		return nil, fmt.Errorf("structured JSON input is invalid")
	}
	switch decoded.(type) {
	case map[string]any, []any:
		return decoded, nil
	default:
		return nil, fmt.Errorf("structured field candidate did not decode as an object or array")
	}
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func jiraGuardedFieldAllowlist(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, len(parts))
	for index, part := range parts {
		out[index] = strings.TrimSpace(part)
	}
	return out
}

// validateJiraGuardedFieldInvocation is the pure preconfiguration boundary for
// both field leaves. It does not read files/stdin, configuration, credentials,
// updater state, or the backend.
func validateJiraGuardedFieldInvocation(cmd *cobra.Command, applyRequested bool) error {
	if _, err := app.ValidateJiraGuardedFieldKey(cmd.Flags().Arg(0)); err != nil {
		return err
	}
	rawSpecs, rawErr := cmd.Flags().GetStringArray("from-file")
	mdSpecs, mdErr := cmd.Flags().GetStringArray("from-md")
	if rawErr != nil || mdErr != nil {
		return usageErr("invalid guarded field input flags")
	}
	all := append(append([]string(nil), rawSpecs...), mdSpecs...)
	if len(all) == 0 || len(all) > domain.JiraGuardedFieldMaxSelected {
		return usageErr("between 1 and %d field inputs are required", domain.JiraGuardedFieldMaxSelected)
	}
	selected := make(map[string]bool, len(all))
	stdin := 0
	for _, spec := range all {
		field, path, ok := strings.Cut(spec, "=")
		field, path = strings.TrimSpace(field), strings.TrimSpace(path)
		if !ok || !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) || path == "" {
			return usageErr("field input must use a non-reserved bounded FIELD=PATH")
		}
		if selected[field] {
			return usageErr("duplicate input for field %q", field)
		}
		selected[field] = true
		if path == "-" {
			stdin++
		}
	}
	if stdin > 1 {
		return usageErr("stdin (-) may be used by only one field input")
	}
	allowRaw, allowErr := cmd.Flags().GetString("allow-fields")
	if allowErr != nil || !cmd.Flags().Changed("allow-fields") {
		return usageErr("--allow-fields is required")
	}
	allowlist := jiraGuardedFieldAllowlist(allowRaw)
	if len(allowlist) == 0 || len(allowlist) > domain.JiraGuardedFieldMaxAllowlist {
		return usageErr("--allow-fields requires between 1 and %d entries", domain.JiraGuardedFieldMaxAllowlist)
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, field := range allowlist {
		if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) {
			return usageErr("--allow-fields contains an invalid or reserved field id")
		}
		if allowed[field] {
			return usageErr("--allow-fields contains duplicate field %q", field)
		}
		allowed[field] = true
	}
	for field := range selected {
		if !allowed[field] {
			return usageErr("field %q is not in --allow-fields", field)
		}
	}
	expectedHashFlag := cmd.Flags().Lookup("expected-proposal-hash")
	expectedUpdatedFlag := cmd.Flags().Lookup("expected-updated")
	if !applyRequested {
		if expectedHashFlag != nil && expectedHashFlag.Changed || expectedUpdatedFlag != nil && expectedUpdatedFlag.Changed {
			return usageErr("--expected-updated and --expected-proposal-hash require --apply")
		}
		return nil
	}
	if expectedHashFlag == nil || expectedUpdatedFlag == nil {
		return &accessPolicyInvariantError{Command: fmt.Sprintf("%s missing guarded field review flags", cmd.CommandPath())}
	}
	if !expectedUpdatedFlag.Changed {
		return usageErr("--expected-updated is required with --apply; run the dry-run first to capture it")
	}
	if !expectedHashFlag.Changed {
		return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first to capture it")
	}
	expectedUpdated, updatedErr := cmd.Flags().GetString("expected-updated")
	expectedHash, hashErr := cmd.Flags().GetString("expected-proposal-hash")
	if updatedErr != nil || strings.TrimSpace(expectedUpdated) == "" || strings.TrimSpace(expectedUpdated) != expectedUpdated || !domain.ValidJiraGuardedCommentInstant(expectedUpdated) {
		return usageErr("--expected-updated must be the exact reviewed Jira timestamp")
	}
	if hashErr != nil {
		return usageErr("invalid --expected-proposal-hash")
	}
	return app.ValidateJiraDescriptionEditReviewHash(expectedHash)
}

func jiraFieldSetText(res *app.JiraFieldSetResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%s\t%s\texpected_updated=%s\tproposal_hash=%s\twrite_attempted=%t\treconciled=%t\tcomplete=%t", res.Key, res.Mode, res.Status, res.ExpectedUpdated, res.ProposalHash, res.WriteAttempted, res.Reconciled, res.Complete)
	for _, field := range res.Fields {
		fmt.Fprintf(&b, "\n%s\t%s\t%d bytes\tsha256=%s", field.Field, field.Kind, field.Bytes, field.SHA256)
	}
	return b.String()
}
