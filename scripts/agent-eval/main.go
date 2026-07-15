// Command agent-eval validates and aggregates privacy-safe atl agent evaluation
// contracts. It is a maintainer tool, not part of the shipped atl binary.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/isukharev/atl/internal/agenteval"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-eval:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-eval validate scenarios | evaluate scenario observation | aggregate results")
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
