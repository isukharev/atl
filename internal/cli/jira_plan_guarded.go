package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type jiraPlanFlags struct {
	csvPath         string
	confirm         string
	expectedHash    string
	allowOps        string
	allowFields     string
	allowLinkTypes  string
	continueOnError bool
}

func jiraPlanGuardedCommand() *cobra.Command {
	parent := &cobra.Command{Use: "plan", Short: "Preview or apply guarded Jira CSV plans"}
	previewFlags := &jiraPlanFlags{}
	applyFlags := &jiraPlanFlags{}
	preview := jiraPlanLeaf("preview", previewFlags)
	apply := jiraPlanLeaf("apply", applyFlags)
	apply.Flags().StringVar(&applyFlags.confirm, "confirm", "", "required exact value APPLY")
	apply.Flags().StringVar(&applyFlags.expectedHash, "expected-proposal-hash", "", "reviewed aggregate proposal hash")
	apply.Flags().BoolVar(&applyFlags.continueOnError, "continue-on-error", false, "continue after conclusive row execution failures")
	parent.AddCommand(preview, apply)
	return parent
}

func jiraPlanLeaf(mode string, flags *jiraPlanFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   mode,
		Short: map[string]string{"preview": "Qualify a guarded CSV proposal without writing", "apply": "Apply an explicitly confirmed guarded CSV proposal"}[mode],
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := invocationRuntimeFor(cmd)
			if runtime.jiraPlanDocument == nil || runtime.jiraPlanCommand != mode {
				return fmt.Errorf("%w: Jira plan document lifecycle is invalid", domain.ErrCheckFailed)
			}
			document := runtime.jiraPlanDocument
			runtime.jiraPlanDocument, runtime.jiraPlanCommand = nil, ""
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, runErr := svc.RunJiraPlan(cmd.Context(), document, app.JiraPlanRunOpts{
				Mode: mode, ExpectedProposalHash: flags.expectedHash,
				AllowOps: splitFields(flags.allowOps), AllowFields: splitFields(flags.allowFields),
				AllowLinkTypes: splitFields(flags.allowLinkTypes), ContinueOnError: flags.continueOnError,
			})
			if result == nil {
				return runErr
			}
			if emitErr := emit(cmd, result, func() string { return app.JiraPlanResultText(result) }); emitErr != nil {
				return emitErr
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&flags.csvPath, "csv", "", "schema-v2 CSV plan")
	cmd.Flags().StringVar(&flags.allowOps, "allow-ops", "link", "comma-separated exact admitted operations")
	cmd.Flags().StringVar(&flags.allowFields, "allow-fields", "", "comma-separated exact admitted guarded field ids")
	cmd.Flags().StringVar(&flags.allowLinkTypes, "allow-link-types", "", "comma-separated exact link type selectors")
	return cmd
}

func validateJiraPlanPreviewInvocation(cmd *cobra.Command) error {
	return validateJiraPlanPureFlags(cmd, "preview")
}

func validateJiraPlanApplyInvocation(cmd *cobra.Command) error {
	if err := validateJiraPlanPureFlags(cmd, "apply"); err != nil {
		return err
	}
	confirm, confirmErr := cmd.Flags().GetString("confirm")
	hash, hashErr := cmd.Flags().GetString("expected-proposal-hash")
	if confirmErr != nil || confirm != "APPLY" {
		return usageErr("--confirm must be exactly APPLY")
	}
	if hashErr != nil || len(strings.TrimSpace(hash)) != 64 {
		return usageErr("--expected-proposal-hash must be a lowercase SHA-256 value")
	}
	for _, current := range strings.TrimSpace(hash) {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return usageErr("--expected-proposal-hash must be a lowercase SHA-256 value")
			}
		}
	}
	return nil
}

func validateJiraPlanPureFlags(cmd *cobra.Command, mode string) error {
	path, err := cmd.Flags().GetString("csv")
	if err != nil || strings.TrimSpace(path) == "" {
		return usageErr("--csv is required")
	}
	if invocationRuntimeFor(cmd).outputFormat == "id" {
		return usageErr("-o id is not supported for Jira plan %s", mode)
	}
	for _, name := range []string{"allow-ops", "allow-fields", "allow-link-types"} {
		value, getErr := cmd.Flags().GetString(name)
		if getErr != nil {
			return usageErr("invalid --%s value", name)
		}
		seen := map[string]bool{}
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" && value != "" || part != "" && seen[part] {
				return usageErr("--%s contains an empty or duplicate value", name)
			}
			if part != "" {
				seen[part] = true
			}
		}
	}
	return nil
}

// prepareJiraPlanInvocation is called after pure mutation/read-only guards and
// before process content policy, config, self-update, credentials, or Jira.
func prepareJiraPlanInvocation(cmd *cobra.Command) error {
	path := commandRegistryPath(cmd.Root(), cmd)
	mode := ""
	switch path {
	case "jira issue plan preview":
		mode = "preview"
	case "jira issue plan apply":
		mode = "apply"
	default:
		return nil
	}
	runtime := invocationRuntimeFor(cmd)
	if runtime.jiraPlanDocument != nil || runtime.jiraPlanCommand != "" {
		return fmt.Errorf("%w: Jira plan document lifecycle is invalid", domain.ErrCheckFailed)
	}
	document, err := app.ReadJiraPlanDocument(policyFlagValue(cmd, "csv"))
	if err != nil {
		return err
	}
	if err := app.BindJiraPlanDocument(document, mode); err != nil {
		return err
	}
	runtime.jiraPlanDocument, runtime.jiraPlanCommand = document, mode
	return nil
}
