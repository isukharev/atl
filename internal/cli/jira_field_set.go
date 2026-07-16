package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
)

func jiraIssueFieldCmd() *cobra.Command {
	group := &cobra.Command{Use: "field", Short: "Exact field evidence and guarded custom-field operations"}
	group.AddCommand(jiraIssueFieldGetCmd(), jiraIssueFieldMutationCmd(false), jiraIssueFieldMutationCmd(true))
	return group
}

func jiraIssueFieldMutationCmd(applyCapable bool) *cobra.Command {
	var rawSpecs, mdSpecs []string
	var allowFields, expectedUpdated, expectedProposalHash string
	var apply bool
	use, short := "preview <KEY>", "Preview bounded file-backed custom-field values"
	if applyCapable {
		use, short = "set <KEY>", "Preview or apply bounded file-backed custom-field values"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Long: "Read custom-field values from bounded files/stdin, fresh-check Jira updated, and preview by default. " +
			"Raw valid JSON objects/arrays stay structured; other raw input is a string. Markdown is converted to a Jira-wiki string. " +
			"Use field preview under a read-only policy; field set apply requires --expected-updated and --expected-proposal-hash from that reviewed preview.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			proposals, err := jiraFieldProposals(rawSpecs, mdSpecs)
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, setErr := svc.SetFieldsGuarded(cmd.Context(), args[0], app.JiraFieldSetOpts{
				Proposals: proposals, AllowFields: splitFields(allowFields),
				ExpectedUpdated: expectedUpdated, ExpectedProposalHash: expectedProposalHash, Apply: apply,
			})
			if res != nil {
				if emitErr := emit(cmd, res, func() string { return jiraFieldSetText(res) }); emitErr != nil {
					return emitErr
				}
			}
			return setErr
		},
	}
	command.Flags().StringArrayVar(&rawSpecs, "from-file", nil, "FIELD=PATH raw value file (repeatable; - reads stdin; object/array JSON stays structured)")
	command.Flags().StringArrayVar(&mdSpecs, "from-md", nil, "FIELD=PATH Markdown file (repeatable; - reads stdin; converted to a Jira-wiki string)")
	command.Flags().StringVar(&allowFields, "allow-fields", "", "comma-separated exact custom field ids allowed by this operation (required)")
	if applyCapable {
		command.Flags().StringVar(&expectedUpdated, "expected-updated", "", "reviewed Jira updated value (required with --apply; preview captures it)")
		command.Flags().StringVar(&expectedProposalHash, "expected-proposal-hash", "", "reviewed aggregate proposal hash (required with --apply; preview captures it)")
		command.Flags().BoolVar(&apply, "apply", false, "perform the guarded write (default: dry-run)")
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
	inputs := make([]input, 0, len(rawInputs))
	stdinCount := 0
	for _, raw := range rawInputs {
		field, path, ok := strings.Cut(raw.spec, "=")
		field, path = strings.TrimSpace(field), strings.TrimSpace(path)
		if !ok || field == "" || path == "" {
			return nil, usageErr("field input must be FIELD=PATH, got %q", raw.spec)
		}
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
		proposal := app.JiraFieldProposal{Field: field, Source: "raw"}
		if in.markdown {
			wiki, err := mdwiki.ConvertDocument(string(data))
			if err != nil {
				return nil, fmt.Errorf("%w: field %q markdown cannot be converted: %v", domain.ErrCheckFailed, field, err)
			}
			proposal.Source, proposal.Value = "markdown", wiki
		} else {
			proposal.Value = rawJiraFieldValue(data)
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

func rawJiraFieldValue(data []byte) any {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&decoded)
		var extra any
		trailingErr := decoder.Decode(&extra)
		if decodeErr == nil && trailingErr == io.EOF {
			switch decoded.(type) {
			case map[string]any, []any:
				return decoded
			}
		}
	}
	return string(data)
}

func jiraFieldSetText(res *app.JiraFieldSetResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%s\t%s\texpected_updated=%s\tproposal_hash=%s", res.Key, res.Mode, res.Status, res.ExpectedUpdated, res.ProposalHash)
	for _, field := range res.Fields {
		fmt.Fprintf(&b, "\n%s\t%s\t%d bytes\tsha256=%s", field.Field, field.Kind, field.Bytes, field.SHA256)
	}
	return b.String()
}
