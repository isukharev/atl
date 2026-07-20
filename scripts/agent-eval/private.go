package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/agenteval"
)

var privateQualifyCodexCLI = agenteval.QualifyCodexCLIToolAvailability

func runPrivateCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("private requires init, doctor, status, qualify, plan, run, review, study, baseline, compare, or prune")
	}
	switch args[0] {
	case "init":
		flags := privateFlagSet("private init")
		var root, repositoryRoot string
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" {
			return fmt.Errorf("private init requires --root and no positional arguments")
		}
		report, err := agenteval.InitPrivateWorkspace(root, repositoryRoot, agenteval.DefaultPrivateWorkspaceManifest())
		if err != nil {
			return err
		}
		return writePrivateJSON(out, report)
	case "doctor":
		flags := privateFlagSet("private doctor")
		var root, repositoryRoot string
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" {
			return fmt.Errorf("private doctor requires --root and no positional arguments")
		}
		report, err := agenteval.DoctorPrivateWorkspace(root, repositoryRoot)
		if encodeErr := writePrivateJSON(out, report); encodeErr != nil {
			return encodeErr
		}
		return err
	case "status":
		flags := privateFlagSet("private status")
		var root, repositoryRoot string
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" {
			return fmt.Errorf("private status requires --root and no positional arguments")
		}
		return writePrivateJSON(out, agenteval.InspectPrivateWorkspace(root, repositoryRoot))
	case "qualify":
		flags := privateFlagSet("private qualify")
		var root, repositoryRoot, agentBinary, model, reasoning string
		var timeoutSeconds int
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&agentBinary, "agent-binary", "", "reviewed single-file native Codex executable")
		flags.StringVar(&model, "model", "", "exact model used by the reviewed run")
		flags.StringVar(&reasoning, "reasoning", "", "exact reasoning setting used by the reviewed run")
		flags.IntVar(&timeoutSeconds, "timeout-seconds", 30, "offline qualification timeout")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || agentBinary == "" || model == "" {
			return fmt.Errorf("private qualify requires root, agent binary, model, and no positional arguments")
		}
		doctor, err := agenteval.DoctorPrivateWorkspace(root, repositoryRoot)
		if err != nil || !doctor.Healthy {
			return fmt.Errorf("private qualify requires a healthy owner-private workspace")
		}
		canonicalRoot, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("private qualify requires a healthy owner-private workspace")
		}
		canonicalRoot, err = filepath.EvalSymlinks(canonicalRoot)
		if err != nil {
			return fmt.Errorf("private qualify requires a healthy owner-private workspace")
		}
		report, err := privateQualifyCodexCLI(context.Background(), agenteval.CodexCLIToolAvailabilityOptions{
			AgentBinary: agentBinary, ScratchRoot: filepath.Join(canonicalRoot, ".ephemeral"), Model: model,
			Reasoning: reasoning, TimeoutSeconds: timeoutSeconds,
		})
		if err != nil {
			return err
		}
		if err := writePrivateJSON(out, report); err != nil {
			return err
		}
		if !report.Supported() {
			return fmt.Errorf("codex cli tool availability is %s", report.Status)
		}
		return nil
	case "plan":
		flags := privateFlagSet("private plan")
		var root, repositoryRoot, runSet, atlBinary, pluginRoot, agentBinary, expiresAt, confirm string
		var approveProvider, approveExternal bool
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&runSet, "run-set", "", "generic private run-set alias")
		flags.StringVar(&atlBinary, "atl-binary", "", "atl executable")
		flags.StringVar(&pluginRoot, "plugin-root", ".", "plugin root")
		flags.StringVar(&agentBinary, "agent-binary", "", "reviewed single-file native Claude Code or Codex executable")
		flags.StringVar(&expiresAt, "consent-expires", "", "RFC3339 consent expiry")
		flags.StringVar(&confirm, "confirm", "", "must be CONSENT")
		flags.BoolVar(&approveProvider, "approve-provider-data", false, "approve reviewed evidence delivery to the provider")
		flags.BoolVar(&approveExternal, "approve-external-upstream", false, "approve the reviewed external MCP trust boundary")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || runSet == "" || atlBinary == "" || agentBinary == "" || expiresAt == "" {
			return fmt.Errorf("private plan requires root, run-set, runtime paths, consent expiry, and no positional arguments")
		}
		wrapper, err := os.Executable()
		if err != nil {
			return err
		}
		preview, err := agenteval.CreatePrivatePlan(context.Background(), agenteval.PrivatePlanCreateOptions{Root: root, RepositoryRoot: repositoryRoot, RunSetAlias: runSet, ATLBinary: atlBinary, PluginRoot: pluginRoot, AgentBinary: agentBinary, WrapperExecutable: wrapper, Consent: agenteval.PrivatePlanConsent{ExpiresAt: expiresAt, ProviderDataApproved: approveProvider, ExternalUpstreamApproved: approveExternal}, Confirm: confirm})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, preview)
	case "run":
		flags := privateFlagSet("private run")
		var root, repositoryRoot, planID, expected, atlBinary, pluginRoot, agentBinary, confirm string
		flags.StringVar(&root, "root", "", "owner-private workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "plan", "", "reviewed plan id")
		flags.StringVar(&expected, "expected-plan-sha256", "", "reviewed plan digest")
		flags.StringVar(&atlBinary, "atl-binary", "", "atl executable")
		flags.StringVar(&pluginRoot, "plugin-root", ".", "plugin root")
		flags.StringVar(&agentBinary, "agent-binary", "", "reviewed single-file native agent executable")
		flags.StringVar(&confirm, "confirm", "", "must be RUN")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" || expected == "" || atlBinary == "" || agentBinary == "" {
			return fmt.Errorf("private run requires root, plan, reviewed hash, runtime paths, and no positional arguments")
		}
		wrapper, err := os.Executable()
		if err != nil {
			return err
		}
		summary, err := agenteval.ExecutePrivatePlan(context.Background(), agenteval.PrivatePlanExecuteOptions{Root: root, RepositoryRoot: repositoryRoot, PlanID: planID, ExpectedPlanSHA256: expected, Confirm: confirm, ATLBinary: atlBinary, PluginRoot: pluginRoot, AgentBinary: agentBinary, WrapperExecutable: wrapper})
		if err != nil {
			if summary.SchemaVersion != 0 {
				if encodeErr := writePrivateJSON(out, summary); encodeErr != nil {
					return encodeErr
				}
			}
			return err
		}
		return writePrivateJSON(out, summary)
	case "review":
		if len(args) < 2 {
			return fmt.Errorf("private review requires prepare or assess")
		}
		operation := args[1]
		if operation != "prepare" && operation != "assess" {
			return fmt.Errorf("private review requires prepare or assess")
		}
		flags := privateFlagSet("private review " + operation)
		var root, repositoryRoot, planID, surface, treatment, reviewer, model, reviewerID, blindAssignment string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "plan", "", "completed plan id")
		flags.StringVar(&surface, "surface", "", "reviewed surface")
		flags.StringVar(&treatment, "treatment", "", "activation-study treatment")
		if operation == "prepare" {
			flags.StringVar(&reviewer, "reviewer", "", "human, codex, or claude-code")
			flags.StringVar(&model, "model", "", "exact reviewer model")
			flags.StringVar(&reviewerID, "reviewer-id", "", "predeclared generic panel reviewer id")
			flags.StringVar(&blindAssignment, "blind-assignment", "", "workspace-relative blind assignment under cases")
		} else {
			flags.StringVar(&reviewerID, "reviewer-id", "", "predeclared generic panel reviewer id")
		}
		reviewArgs := []string{}
		if len(args) > 2 {
			reviewArgs = args[2:]
		}
		if err := flags.Parse(reviewArgs); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" || surface == "" {
			return fmt.Errorf("private review %s requires root, plan, surface, and no positional arguments", operation)
		}
		if operation == "prepare" {
			if reviewer == "" && reviewerID == "" {
				return fmt.Errorf("private review prepare requires --reviewer for legacy-single or --reviewer-id for a panel")
			}
			summary, err := agenteval.PreparePrivateReview(agenteval.PrivateReviewPrepareOptions{Root: root, RepositoryRoot: repositoryRoot,
				PlanID: planID, Surface: surface, Treatment: treatment, ReviewerKind: reviewer, ReviewerModel: model, ReviewerID: reviewerID, BlindAssignment: blindAssignment})
			if err != nil {
				return err
			}
			return writePrivateJSON(out, summary)
		}
		summary, err := agenteval.AssessPrivateReview(agenteval.PrivateReviewAssessOptions{Root: root, RepositoryRoot: repositoryRoot, PlanID: planID, Surface: surface, Treatment: treatment, ReviewerID: reviewerID})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	case "study":
		return runPrivateStudyCommand(args[1:], out)
	case "baseline":
		if len(args) < 2 || args[1] != "set" {
			return fmt.Errorf("private baseline requires set")
		}
		flags := privateFlagSet("private baseline set")
		var root, repositoryRoot, planID, baseline, confirm string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "plan", "", "completed plan id")
		flags.StringVar(&baseline, "baseline", "", "generic baseline alias")
		flags.StringVar(&confirm, "confirm", "", "must be BASELINE")
		baselineArgs := []string{}
		if len(args) > 2 {
			baselineArgs = args[2:]
		}
		if err := flags.Parse(baselineArgs); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" || baseline == "" {
			return fmt.Errorf("private baseline set requires root, plan, baseline, and no positional arguments")
		}
		source, err := agenteval.LoadCompletedPrivateRun(root, repositoryRoot, planID)
		if err != nil {
			return err
		}
		summary, err := agenteval.SetPrivateBaseline(agenteval.PrivateBaselineSetOptions{Root: root, RepositoryRoot: repositoryRoot, Baseline: baseline, Confirm: confirm, Source: source})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	case "compare":
		flags := privateFlagSet("private compare")
		var root, repositoryRoot, planID, baseline string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "candidate-plan", "", "completed candidate plan id")
		flags.StringVar(&baseline, "baseline", "current", "baseline alias or current")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" {
			return fmt.Errorf("private compare requires root, candidate-plan, and no positional arguments")
		}
		source, err := agenteval.LoadCompletedPrivateRun(root, repositoryRoot, planID)
		if err != nil {
			return err
		}
		comparison, err := agenteval.ComparePrivateBaseline(agenteval.PrivateCompareOptions{Root: root, RepositoryRoot: repositoryRoot, Baseline: baseline, Candidate: source})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, comparison)
	case "prune":
		flags := privateFlagSet("private prune")
		var root, repositoryRoot, expected, confirm string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&expected, "expected-inventory-sha256", "", "reviewed prune inventory digest")
		flags.StringVar(&confirm, "confirm", "", "must be PRUNE")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" {
			return fmt.Errorf("private prune requires root and no positional arguments")
		}
		options := agenteval.PrivatePruneOptions{Root: root, RepositoryRoot: repositoryRoot, ExpectedInventorySHA256: expected, Confirm: confirm}
		if expected == "" && confirm == "" {
			preview, err := agenteval.PreviewPrivatePrune(options)
			if err != nil {
				return err
			}
			return writePrivateJSON(out, preview)
		}
		if expected == "" || confirm == "" {
			return fmt.Errorf("private prune apply requires both --expected-inventory-sha256 and --confirm PRUNE")
		}
		summary, err := agenteval.ApplyPrivatePrune(options)
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	default:
		return fmt.Errorf("unknown private command %q", args[0])
	}
}

func runPrivateStudyCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("private study requires recover, reference, compare, or promote")
	}
	switch args[0] {
	case "recover":
		flags := privateFlagSet("private study recover")
		var root, repositoryRoot, planID, expected, confirm string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "plan", "", "interrupted activation-study plan id")
		flags.StringVar(&expected, "expected-plan-sha256", "", "reviewed plan digest")
		flags.StringVar(&confirm, "confirm", "", "must attest PROVIDER_STOPPED_RECOVER")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" || expected == "" {
			return fmt.Errorf("private study recover requires root, plan, reviewed hash, and no positional arguments")
		}
		summary, err := agenteval.RecoverPrivateActivationStudy(agenteval.PrivateActivationRecoveryOptions{Root: root,
			RepositoryRoot: repositoryRoot, PlanID: planID, ExpectedPlanSHA256: expected, Confirm: confirm})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	case "reference":
		flags := privateFlagSet("private study reference")
		var root, repositoryRoot, planID, reference, confirm string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&planID, "plan", "", "completed activation-study plan id")
		flags.StringVar(&reference, "reference", "", "immutable measurement reference alias")
		flags.StringVar(&confirm, "confirm", "", "must be REFERENCE")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || planID == "" || reference == "" {
			return fmt.Errorf("private study reference requires root, plan, reference, and no positional arguments")
		}
		summary, err := agenteval.SetPrivateActivationReference(agenteval.PrivateActivationReferenceSetOptions{Root: root,
			RepositoryRoot: repositoryRoot, PlanID: planID, Reference: reference, Confirm: confirm})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	case "compare":
		flags := privateFlagSet("private study compare")
		var root, repositoryRoot, reference string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&reference, "reference", "", "measurement reference alias")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || reference == "" {
			return fmt.Errorf("private study compare requires root, reference, and no positional arguments")
		}
		report, err := agenteval.CompareStoredPrivateActivationReference(agenteval.PrivateActivationReferenceCompareOptions{Root: root,
			RepositoryRoot: repositoryRoot, Reference: reference})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, report)
	case "promote":
		flags := privateFlagSet("private study promote")
		var root, repositoryRoot, reference, confirm string
		flags.StringVar(&root, "root", "", "workspace root")
		flags.StringVar(&repositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&reference, "reference", "", "passing measurement reference alias")
		flags.StringVar(&confirm, "confirm", "", "must be PROMOTE")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || root == "" || reference == "" {
			return fmt.Errorf("private study promote requires root, reference, and no positional arguments")
		}
		summary, err := agenteval.PromotePrivateActivationReference(agenteval.PrivateActivationPromotionOptions{Root: root,
			RepositoryRoot: repositoryRoot, Reference: reference, Confirm: confirm})
		if err != nil {
			return err
		}
		return writePrivateJSON(out, summary)
	default:
		return fmt.Errorf("private study requires recover, reference, compare, or promote")
	}
}

func privateFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func writePrivateJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
