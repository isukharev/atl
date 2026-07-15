// Command agent-eval validates and aggregates privacy-safe atl agent evaluation
// contracts. It is a maintainer tool, not part of the shipped atl binary.
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

func main() {
	base := filepath.Base(os.Args[0])
	if base == "atl-eval-guard" || base == "atl-eval-guard.exe" {
		os.Exit(runClaudeBashGuard(os.Stdin, os.Stdout, os.Stderr))
	}
	if base == "atl" || base == "atl.exe" {
		os.Exit(runATLProxy(os.Args[1:]))
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-eval validate scenarios | validate-run specs | evaluate scenario observation | aggregate results | run options")
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
