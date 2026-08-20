package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

// jiraTransitionCmd builds the executable guarded transition command and its
// separately classified GET-only preview child.
func jiraTransitionCmd() *cobra.Command {
	transition := jiraTransitionMutationCmd(true)
	transition.AddCommand(jiraTransitionMutationCmd(false))
	return transition
}

func jiraTransitionMutationCmd(applyCapable bool) *cobra.Command {
	var to, comment string
	var fieldPairs, fieldJSON []string
	guardedWrite := guardedWriteFlags{profile: guardedWriteProposal}
	use, short := "preview <KEY>", "Preview a reviewed Jira transition"
	if applyCapable {
		use, short = "transition <KEY>", "Preview or apply a reviewed Jira transition"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: "Preview by default against current issue, transition, field, and optional comment evidence. " +
			"Apply requires the exact proposal hash, sends at most one POST, and reconciles fresh state without replay.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if applyCapable {
				if err := guardedWrite.validate(); err != nil {
					return err
				}
			}
			if strings.TrimSpace(to) == "" {
				return usageErr("--to is required")
			}
			fields, err := parseUniqueJiraTransitionFields(fieldPairs, fieldJSON)
			if err != nil {
				return err
			}
			var commentBody []byte
			if cmd.Flags().Changed("comment") {
				commentBody, err = app.ValidateJiraCommentBody([]byte(comment))
				if err != nil {
					return err
				}
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.TransitionGuarded(cmd.Context(), args[0], app.JiraTransitionGuardedOpts{
				To: to, Comment: commentBody, Fields: fields,
				Apply:                applyCapable && guardedWrite.apply,
				ExpectedProposalHash: guardedWrite.expectedProposalHash,
			})
			if result != nil {
				if emitErr := emit(cmd, result, func() string { return jiraTransitionText(result) }); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target status or transition name")
	cmd.Flags().StringVar(&comment, "comment", "", "optional bounded native Jira-wiki comment")
	cmd.Flags().StringArrayVar(&fieldPairs, "field", nil, "field key=value to set on the transition (repeatable); JSON objects/arrays are sent as JSON")
	cmd.Flags().StringArrayVar(&fieldJSON, "field-json", nil, "field key=JSON to set on the transition (repeatable); sends an explicit JSON value including scalars")
	if applyCapable {
		guardedWrite.register(cmd)
	}
	return cmd
}

func parseUniqueJiraTransitionFields(pairs, jsonPairs []string) ([]app.JiraTransitionFieldInput, error) {
	inputs, err := parseJiraFieldInputs(pairs, jsonPairs, true)
	if err != nil {
		return nil, err
	}
	fields := make([]app.JiraTransitionFieldInput, 0, len(inputs))
	for _, pair := range append(append([]string(nil), pairs...), jsonPairs...) {
		key, _, _ := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		value := inputs[key]
		fields = append(fields, app.JiraTransitionFieldInput{Field: key, Value: value.Value, ExplicitJSON: value.ExplicitJSON})
	}
	return fields, nil
}

func jiraTransitionText(result *app.JiraTransitionGuardedResult) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n",
		textCell(result.Status), textCell(result.Key), textCell(result.Transition.ID),
		textCell(result.Transition.Name), textCell(result.Transition.To))
	fmt.Fprintf(&b, "current_status\t%s\t%s\n", textCell(result.CurrentStatus.ID), textCell(result.CurrentStatus.Name))
	fmt.Fprintf(&b, "fields\t%d\n", len(result.Fields))
	if result.Comment != nil {
		fmt.Fprintf(&b, "comment\t%d\t%s\t%s\n", result.Comment.BodyBytes,
			textCell(result.Comment.BodySHA256), textCell(result.Comment.BaselineSHA256))
	}
	fmt.Fprintf(&b, "proposal_hash\t%s", textCell(result.ProposalHash))
	return b.String()
}
