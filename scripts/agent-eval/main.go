// Command agent-eval validates and aggregates privacy-safe atl agent evaluation
// contracts. It is a maintainer tool, not part of the shipped atl binary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-eval validate scenarios | validate-run specs | validate-pair CLI_SPEC MCP_SPEC | evaluate scenario observation | review-template options | assess options | aggregate results | run options")
	}
	switch args[0] {
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
	case "validate-pair":
		if len(args) != 3 {
			return fmt.Errorf("validate-pair requires exactly one private CLI spec and one private MCP spec")
		}
		pair, err := agenteval.ValidatePrivateRunPair(args[1], args[2])
		if err != nil {
			return err
		}
		return writeJSON(pair)
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
	case "review-template":
		flags := flag.NewFlagSet("review-template", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var rubricPath, resultPath, finalPath, reviewerKind, reviewerModel string
		flags.StringVar(&rubricPath, "rubric", "", "qualitative rubric")
		flags.StringVar(&resultPath, "result", "", "deterministic result")
		flags.StringVar(&finalPath, "final", "", "private final response")
		flags.StringVar(&reviewerKind, "reviewer", "", "human, codex, or claude-code")
		flags.StringVar(&reviewerModel, "model", "", "exact reviewer model")
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
		review, err := agenteval.NewReviewTemplate(result, resultBytes, finalBytes, rubric, agenteval.Reviewer{Kind: reviewerKind, Model: reviewerModel})
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
