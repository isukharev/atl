package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

type jiraGuardedLinkFlags struct {
	from, to, selector string
	apply              bool
	expectedHash       string
}

func validateJiraGuardedLinkInvocation(cmd *cobra.Command, applyRequested bool) error {
	path := commandRegistryPath(cmd.Root(), cmd)
	operation := "add"
	from, linkID := firstArg(cmd.Flags().Args()), ""
	if strings.HasSuffix(path, "link delete") {
		operation, from, linkID = "delete", policyFlagValue(cmd, "from"), firstArg(cmd.Flags().Args())
	}
	opts := app.JiraGuardedLinkOpts{
		Operation: operation, From: strings.ToUpper(strings.TrimSpace(from)), To: strings.ToUpper(policyFlagValue(cmd, "to")),
		Type: strings.TrimSpace(policyFlagValue(cmd, "type")), LinkID: strings.TrimSpace(linkID), Apply: applyRequested,
		ExpectedProposalHash: strings.TrimSpace(policyFlagValue(cmd, "expected-proposal-hash")),
	}
	if !applyRequested {
		if expected := cmd.Flags().Lookup("expected-proposal-hash"); expected != nil && expected.Changed {
			return usageErr("--expected-proposal-hash requires --apply")
		}
	}
	return app.ValidateJiraGuardedLinkOpts(opts)
}

func jiraGuardedLinkAddCmd() *cobra.Command {
	flags := &jiraGuardedLinkFlags{}
	cmd := &cobra.Command{
		Use: "add <FROM>", Short: "Preview or apply one reviewed issue link",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := flags.opts("add", args[0])
			if err := app.ValidateJiraGuardedLinkOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.GuardedLink(cmd.Context(), opts)
			if result == nil {
				return mutationErr
			}
			emitErr := emit(cmd, result, nil)
			return guardedMutationResultErr(mutationErr, emitErr, result.WriteAttempted, "Jira guarded link add")
		},
	}
	bindJiraGuardedLinkFlags(cmd, flags, false)
	previewFlags := &jiraGuardedLinkFlags{}
	preview := &cobra.Command{
		Use: "preview <FROM>", Short: "Build the guarded link proposal without writing",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := previewFlags.opts("add", args[0])
			if err := app.ValidateJiraGuardedLinkOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.GuardedLink(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return emit(cmd, result, nil)
		},
	}
	bindJiraGuardedLinkFlags(preview, previewFlags, false)
	cmd.AddCommand(preview)
	return cmd
}

func jiraGuardedLinkDeleteCmd() *cobra.Command {
	flags := &jiraGuardedLinkFlags{}
	cmd := &cobra.Command{
		Use: "delete <LINK-ID>", Short: "Preview or apply deletion of one exact reviewed issue link",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := flags.opts("delete", args[0])
			if err := app.ValidateJiraGuardedLinkOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.GuardedLink(cmd.Context(), opts)
			if result == nil {
				return mutationErr
			}
			emitErr := emit(cmd, result, nil)
			return guardedMutationResultErr(mutationErr, emitErr, result.WriteAttempted, "Jira guarded link delete")
		},
	}
	bindJiraGuardedLinkFlags(cmd, flags, true)
	previewFlags := &jiraGuardedLinkFlags{}
	preview := &cobra.Command{
		Use: "preview <LINK-ID>", Short: "Build the guarded deletion proposal without writing",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := previewFlags.opts("delete", args[0])
			if err := app.ValidateJiraGuardedLinkOpts(opts); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.GuardedLink(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return emit(cmd, result, nil)
		},
	}
	bindJiraGuardedLinkFlags(preview, previewFlags, true)
	cmd.AddCommand(preview)
	return cmd
}

func bindJiraGuardedLinkFlags(cmd *cobra.Command, flags *jiraGuardedLinkFlags, deletion bool) {
	if deletion {
		cmd.Flags().StringVar(&flags.from, "from", "", "reviewed source issue key")
	}
	cmd.Flags().StringVar(&flags.to, "to", "", "reviewed target issue key")
	cmd.Flags().StringVar(&flags.selector, "type", "", "reviewed canonical link name or directional phrase")
	if cmd.Name() != "preview" {
		cmd.Flags().BoolVar(&flags.apply, "apply", false, "send the sole reviewed write attempt")
		cmd.Flags().StringVar(&flags.expectedHash, "expected-proposal-hash", "", "exact lowercase SHA-256 from the reviewed preview")
	}
}

func (flags *jiraGuardedLinkFlags) opts(operation, positional string) app.JiraGuardedLinkOpts {
	from, linkID := flags.from, ""
	if operation == "add" {
		from = positional
	} else {
		linkID = positional
	}
	return app.JiraGuardedLinkOpts{
		Operation: operation, From: strings.ToUpper(strings.TrimSpace(from)), To: strings.ToUpper(strings.TrimSpace(flags.to)),
		Type: strings.TrimSpace(flags.selector), LinkID: strings.TrimSpace(linkID), Apply: flags.apply,
		ExpectedProposalHash: strings.TrimSpace(flags.expectedHash),
	}
}
