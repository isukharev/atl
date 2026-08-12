// Command agent-eval validates and aggregates privacy-safe atl agent evaluation
// contracts. It is a maintainer tool, not part of the shipped atl binary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/agenteval"
)

func main() {
	base := filepath.Base(os.Args[0])
	if base == "atl-eval-guard" || base == "atl-eval-guard.exe" {
		os.Exit(runClaudeBashGuard(os.Stdin, os.Stdout, os.Stderr))
	}
	if base == "atl-eval-confinement-probe" || base == "atl-eval-confinement-probe.exe" {
		os.Exit(runCommandBrokerProbe(os.Stderr))
	}
	if base == "atl" || base == "atl.exe" {
		os.Exit(runATLProxy(os.Args[1:]))
	}
	if base == "cat" || base == "sed" || base == "wc" {
		os.Exit(runSkillReader(base, os.Args[1:], os.Stdout, os.Stderr))
	}
	if base == "env" {
		os.Exit(runReviewedWriteEnv(os.Args[1:]))
	}
	if len(os.Args) == 1 {
		writeStandaloneHelp(os.Stdout, nil)
		return
	}
	if err := run(os.Args[1:]); err != nil {
		var exit standaloneExitStatus
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, "agent-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if handled, err := runStandaloneCommand(args, os.Stdin, os.Stdout, os.Stderr); handled {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-eval validate scenarios | validate-run specs | verify-agent-adapter --manifest FILE --adapter FILE --bundle FILE --contract FILE --ledger ROOT | verify-execution-backend --manifest FILE --backend FILE --bundle FILE --contract FILE --plan FILE --ledger ROOT | verify-atl-capabilities --ledger ROOT ATL_BINARY | verify-codex-skill-package PACKAGE_ROOT | verify-extension-protocol --manifest FILE --adapter FILE --bundle FILE --ledger ROOT | attempt-ledger COMMAND options | inventory CORPUS_ROOT | validate-pair CLI_SPEC MCP_SPEC | validate-comparison-set SPEC SPEC [SPEC] | evaluate scenario observation | review-template options | assess options | aggregate results | aggregate-root ROOT | run options | private COMMAND options")
	}
	switch args[0] {
	case "private":
		return runPrivateCommand(args[1:], os.Stdout)
	case "attempt-ledger":
		return runAttemptLedgerCommand(args[1:])
	case "validate":
		if len(args) < 2 {
			return fmt.Errorf("validate requires at least one scenario")
		}
		ids := make([]string, 0, len(args)-1)
		seen := map[string]struct{}{}
		for _, path := range args[1:] {
			scenario, err := readScenario(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if _, exists := seen[scenario.ID]; exists {
				return fmt.Errorf("duplicate scenario id %q", scenario.ID)
			}
			seen[scenario.ID] = struct{}{}
			ids = append(ids, scenario.ID)
		}
		return writeJSON(map[string]any{"schema_version": 1, "valid_scenarios": ids})
	case "evaluate":
		if len(args) != 3 {
			return fmt.Errorf("evaluate requires SCENARIO and OBSERVATION")
		}
		scenario, err := readScenario(args[1])
		if err != nil {
			return err
		}
		observation, err := readObservation(args[2])
		if err != nil {
			return err
		}
		result, err := agenteval.Evaluate(scenario, observation)
		if err != nil {
			return err
		}
		return writeJSON(result)
	case "validate-run":
		if len(args) < 2 {
			return fmt.Errorf("validate-run requires at least one run spec")
		}
		ids := make([]string, 0, len(args)-1)
		for _, path := range args[1:] {
			spec, scenario, err := agenteval.ValidateRunSpecFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			ids = append(ids, scenario.ID+"/"+spec.Provider+"/"+spec.Variant)
		}
		return writeJSON(map[string]any{"schema_version": 1, "valid_runs": ids})
	case "verify-agent-adapter":
		return runVerifyAgentAdapter(args[1:])
	case "verify-execution-backend":
		return runVerifyExecutionBackend(args[1:])
	case "verify-atl-capabilities":
		flags := flag.NewFlagSet("verify-atl-capabilities", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var ledgerPath singleStringFlag
		flags.Var(&ledgerPath, "ledger", "durable attempt ledger")
		if err := flags.Parse(args[1:]); err != nil || ledgerPath.duplicate || flags.NArg() != 1 || ledgerPath.value == "" {
			return fmt.Errorf("verify-atl-capabilities requires --ledger and exactly one ATL executable")
		}
		if err := agenteval.VerifyATLCapabilityCatalog(context.Background(), flags.Arg(0), ledgerPath.value); err != nil {
			return err
		}
		return writeJSON(map[string]any{"schema_version": 1, "compatible": true})
	case "verify-codex-skill-package":
		if len(args) != 2 {
			return fmt.Errorf("verify-codex-skill-package requires exactly one package root")
		}
		catalog, err := agenteval.VerifyCodexSkillPackage(args[1])
		if err != nil {
			return err
		}
		if err := agenteval.VerifyReleasedCodexSkillSemantics(catalog); err != nil {
			return err
		}
		return writeJSON(map[string]any{"schema_version": 1, "compatible": true})
	case "verify-extension-protocol":
		flags := flag.NewFlagSet("verify-extension-protocol", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var manifestPath, adapterPath, bundlePath, ledgerPath singleStringFlag
		flags.Var(&manifestPath, "manifest", "extension manifest")
		flags.Var(&adapterPath, "adapter", "extension adapter executable")
		flags.Var(&bundlePath, "bundle", "extension conformance bundle")
		flags.Var(&ledgerPath, "ledger", "durable attempt ledger")
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("verify-extension-protocol has invalid flags")
		}
		if manifestPath.duplicate || adapterPath.duplicate || bundlePath.duplicate || ledgerPath.duplicate {
			return fmt.Errorf("verify-extension-protocol flags may be specified only once")
		}
		if flags.NArg() != 0 || manifestPath.value == "" || adapterPath.value == "" || bundlePath.value == "" || ledgerPath.value == "" {
			return fmt.Errorf("verify-extension-protocol requires --manifest, --adapter, --bundle, and --ledger")
		}
		report, err := agenteval.VerifyExtensionProtocolFiles(context.Background(), manifestPath.value, adapterPath.value, bundlePath.value, ledgerPath.value)
		if err != nil {
			return err
		}
		encoded, err := agenteval.EncodeExtensionConformanceReport(report)
		if err != nil {
			return err
		}
		_, err = io.Copy(os.Stdout, bytes.NewReader(encoded))
		return err
	case "inventory":
		if len(args) != 2 {
			return fmt.Errorf("inventory requires exactly one corpus root")
		}
		inventory, err := agenteval.ValidateBenchmarkCorpus(args[1])
		if err != nil {
			return err
		}
		return writeJSON(inventory)
	case "validate-pair":
		if len(args) != 3 {
			return fmt.Errorf("validate-pair requires exactly one private CLI spec and one private MCP spec")
		}
		pair, err := agenteval.ValidatePrivateRunPair(args[1], args[2])
		if err != nil {
			return err
		}
		return writeJSON(pair)
	case "validate-comparison-set":
		if len(args) < 3 || len(args) > 4 {
			return fmt.Errorf("validate-comparison-set requires two or three private run specs")
		}
		set, err := agenteval.ValidatePrivateRunComparisonSet(args[1:]...)
		if err != nil {
			return err
		}
		return writeJSON(set)
	case "aggregate":
		if len(args) < 2 {
			return fmt.Errorf("aggregate requires at least one result")
		}
		results := make([]agenteval.Result, 0, len(args)-1)
		for _, path := range args[1:] {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			result, decodeErr := agenteval.DecodeResult(file)
			closeErr := file.Close()
			if decodeErr != nil {
				return fmt.Errorf("%s: %w", path, decodeErr)
			}
			if closeErr != nil {
				return closeErr
			}
			results = append(results, result)
		}
		aggregate, err := agenteval.AggregateResults(results)
		if err != nil {
			return err
		}
		return writeJSON(aggregate)
	case "aggregate-root":
		if len(args) != 2 {
			return fmt.Errorf("aggregate-root requires exactly one marked synthetic output root")
		}
		aggregate, err := agenteval.AggregateSyntheticOutputRoot(args[1])
		if err != nil {
			return err
		}
		return writeJSON(aggregate)
	case "review-template":
		flags := flag.NewFlagSet("review-template", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var rubricPath, resultPath, finalPath, reviewerKind, reviewerModel, blindAssignmentPath string
		flags.StringVar(&rubricPath, "rubric", "", "qualitative rubric")
		flags.StringVar(&resultPath, "result", "", "deterministic result")
		flags.StringVar(&finalPath, "final", "", "private final response")
		flags.StringVar(&reviewerKind, "reviewer", "", "human, codex, or claude-code")
		flags.StringVar(&reviewerModel, "model", "", "exact reviewer model")
		flags.StringVar(&blindAssignmentPath, "blind-assignment", "", "private blind assignment file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || rubricPath == "" || resultPath == "" || finalPath == "" || reviewerKind == "" {
			return fmt.Errorf("review-template requires --rubric, --result, --final, and --reviewer")
		}
		rubric, err := readRubric(rubricPath)
		if err != nil {
			return err
		}
		result, resultBytes, err := readResultBytes(resultPath)
		if err != nil {
			return err
		}
		finalBytes, err := readPrivateFinal(finalPath)
		if err != nil {
			return err
		}
		var blindAssignment [][]byte
		if blindAssignmentPath != "" {
			assignment, err := readPrivateFinal(blindAssignmentPath)
			if err != nil {
				return err
			}
			blindAssignment = append(blindAssignment, assignment)
		}
		review, err := agenteval.NewReviewTemplate(result, resultBytes, finalBytes, rubric, agenteval.Reviewer{Kind: reviewerKind, Model: reviewerModel}, blindAssignment...)
		if err != nil {
			return err
		}
		return writeJSON(review)
	case "assess":
		flags := flag.NewFlagSet("assess", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var rubricPath, resultPath, finalPath, reviewPath string
		flags.StringVar(&rubricPath, "rubric", "", "qualitative rubric")
		flags.StringVar(&resultPath, "result", "", "deterministic result")
		flags.StringVar(&finalPath, "final", "", "private final response")
		flags.StringVar(&reviewPath, "review", "", "private completed review")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || rubricPath == "" || resultPath == "" || finalPath == "" || reviewPath == "" {
			return fmt.Errorf("assess requires --rubric, --result, --final, and --review")
		}
		rubric, err := readRubric(rubricPath)
		if err != nil {
			return err
		}
		result, resultBytes, err := readResultBytes(resultPath)
		if err != nil {
			return err
		}
		finalBytes, err := readPrivateFinal(finalPath)
		if err != nil {
			return err
		}
		reviewFile, err := os.Open(reviewPath)
		if err != nil {
			return err
		}
		review, reviewErr := agenteval.DecodeReview(reviewFile)
		closeErr := reviewFile.Close()
		if reviewErr != nil {
			return reviewErr
		}
		if closeErr != nil {
			return closeErr
		}
		assessed, err := agenteval.AssessQualitative(result, resultBytes, finalBytes, rubric, review)
		if err != nil {
			return err
		}
		return writeJSON(assessed)
	case "run":
		flags := flag.NewFlagSet("run", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var options agenteval.RunOptions
		flags.StringVar(&options.SpecPath, "spec", "", "run specification")
		flags.StringVar(&options.OutputRoot, "output-root", "", "private output root")
		flags.StringVar(&options.RepositoryRoot, "repository-root", ".", "repository root")
		flags.StringVar(&options.AgentBinary, "agent-binary", "", "Claude Code or Codex executable")
		flags.StringVar(&options.ATLBinary, "atl-binary", "", "atl executable")
		flags.StringVar(&options.PluginRoot, "plugin-root", ".", "atl plugin root")
		flags.StringVar(&options.LiveConfigDir, "live-config-dir", "", "private atl config directory for a private-live run")
		flags.StringVar(&options.ExternalMCPProfile, "external-mcp-profile", "", "owner-only external MCP policy profile")
		flags.StringVar(&options.ModelOverride, "model", "", "exact model override")
		flags.IntVar(&options.RepetitionsOverride, "repetitions", 0, "reduce the run repetition count")
		flags.BoolVar(&options.DryRun, "dry-run", false, "validate and preview without invoking a model")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("run does not accept positional arguments")
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		options.WrapperExecutable = executable
		output, err := agenteval.RunHeadless(context.Background(), options)
		if err != nil {
			return err
		}
		return writeJSON(output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAttemptLedgerCommand(args []string) error {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "reconcile") {
		return fmt.Errorf("attempt-ledger requires inspect or reconcile")
	}
	flags := flag.NewFlagSet("attempt-ledger "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var rootPath, attemptID, evidenceSHA256 singleStringFlag
	flags.Var(&rootPath, "root", "owner-private attempt ledger root")
	if args[0] == "reconcile" {
		flags.Var(&attemptID, "attempt", "unknown predecessor attempt id")
		flags.Var(&evidenceSHA256, "evidence", "content-minimized reconciliation evidence digest")
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || rootPath.duplicate || rootPath.value == "" ||
		attemptID.duplicate || evidenceSHA256.duplicate {
		return fmt.Errorf("attempt-ledger %s has invalid flags", args[0])
	}
	if args[0] == "inspect" {
		report, err := agenteval.InspectAttemptLedger(rootPath.value)
		if err != nil {
			return err
		}
		return writeJSON(report)
	}
	if attemptID.value == "" || evidenceSHA256.value == "" {
		return fmt.Errorf("attempt-ledger reconcile requires --root, --attempt, and --evidence")
	}
	report, err := agenteval.ReconcileAttemptLedger(rootPath.value, attemptID.value, evidenceSHA256.value)
	if err != nil {
		return err
	}
	return writeJSON(report)
}

func readRubric(path string) (agenteval.Rubric, error) {
	file, err := os.Open(path)
	if err != nil {
		return agenteval.Rubric{}, err
	}
	defer file.Close()
	return agenteval.DecodeRubric(file)
}

func readResultBytes(path string) (agenteval.Result, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agenteval.Result{}, nil, err
	}
	result, err := agenteval.DecodeResult(bytes.NewReader(data))
	if err != nil {
		return agenteval.Result{}, nil, err
	}
	return result, data, nil
}

func readPrivateFinal(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 16<<20 {
		return nil, fmt.Errorf("final response exceeds 16777216 bytes")
	}
	return os.ReadFile(path)
}

func readScenario(path string) (agenteval.Scenario, error) {
	file, err := os.Open(path)
	if err != nil {
		return agenteval.Scenario{}, err
	}
	defer file.Close()
	return agenteval.DecodeScenario(file)
}

func readObservation(path string) (agenteval.Observation, error) {
	file, err := os.Open(path)
	if err != nil {
		return agenteval.Observation{}, err
	}
	defer file.Close()
	return agenteval.DecodeObservation(file)
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type singleStringFlag struct {
	value     string
	seen      bool
	duplicate bool
}

func (value *singleStringFlag) String() string { return value.value }

func (value *singleStringFlag) Set(next string) error {
	if value.seen {
		value.duplicate = true
		return nil
	}
	value.seen = true
	value.value = next
	return nil
}
