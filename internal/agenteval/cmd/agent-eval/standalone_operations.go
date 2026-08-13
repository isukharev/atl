package main

import (
	"context"
	"fmt"
	"os"

	"github.com/isukharev/atl/internal/agenteval"
)

func standaloneExecuteValidate(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	if _, failure := standalonePeekFlag(args, "kind"); failure != nil {
		return standaloneOutcome{}, failure
	}
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(
		map[string]standaloneFlagSpec{"kind": {takesValue: true}, "input": {takesValue: true, repeatable: true}},
	))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || !standaloneOneOf(parsed.one("kind"), "scenario", "run-spec") || len(parsed.many("input")) == 0 {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_validate_options")
	}
	resolved, failure := resolveStandaloneConfig(parsed)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	seen := make(map[string]struct{})
	identities := make([]string, 0, len(parsed.many("input")))
	capabilities := make([]string, 0)
	for _, path := range parsed.many("input") {
		if failure := standaloneContextFailure(ctx); failure != nil {
			return standaloneOutcome{}, failure
		}
		switch parsed.one("kind") {
		case "scenario":
			scenario, err := readScenario(path)
			if err != nil {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_scenario")
			}
			if _, duplicate := seen[scenario.ID]; duplicate {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "duplicate_scenario")
			}
			seen[scenario.ID] = struct{}{}
			identity, identityFailure := standaloneResolutionIdentity("scenario", scenario)
			if identityFailure != nil {
				return standaloneOutcome{}, identityFailure
			}
			identities = append(identities, identity)
			capabilities = append(capabilities, scenario.RequiredCapabilities...)
		case "run-spec":
			spec, scenario, err := agenteval.ValidateRunSpecFile(path)
			if err != nil {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_run_spec")
			}
			identity := scenario.ID + "/" + spec.Provider + "/" + spec.Variant
			if _, duplicate := seen[identity]; duplicate {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "duplicate_run_spec")
			}
			seen[identity] = struct{}{}
			semanticIdentity, identityFailure := standaloneResolutionIdentity("run-spec", struct {
				Spec     agenteval.RunSpec  `json:"run_spec"`
				Scenario agenteval.Scenario `json:"scenario"`
			}{Spec: spec, Scenario: scenario})
			if identityFailure != nil {
				return standaloneOutcome{}, identityFailure
			}
			identities = append(identities, semanticIdentity)
			capabilities = append(capabilities, scenario.RequiredCapabilities...)
		}
	}
	evidence := standaloneNewResolutionEvidence("validate", "default", parsed.one("kind"), len(seen), identities, capabilities)
	if parsed.boolean("dry-run") || parsed.boolean("explain") {
		return standalonePreviewOutcome("validate", "default", parsed, resolved, evidence, true)
	}
	result := struct {
		Kind  string `json:"kind"`
		Valid bool   `json:"valid"`
		Count int    `json:"count"`
	}{Kind: parsed.one("kind"), Valid: true, Count: len(seen)}
	return standaloneOutcome{command: "validate", status: "completed", result: result, outputMode: parsed.outputModeValue(), text: fmt.Sprintf("%d inputs valid\n", len(seen))}, nil
}

func standaloneExecuteGrade(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	mode, peekFailure := standalonePeekFlag(args, "mode")
	if peekFailure != nil {
		return standaloneOutcome{}, peekFailure
	}
	if mode == "judge" {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(map[string]standaloneFlagSpec{
		"mode": {takesValue: true}, "scenario": {takesValue: true}, "observation": {takesValue: true},
	}))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || parsed.one("mode") != "deterministic" || parsed.one("scenario") == "" || parsed.one("observation") == "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_grade_options")
	}
	resolved, failure := resolveStandaloneConfig(parsed)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	scenario, err := readScenario(parsed.one("scenario"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_scenario")
	}
	observation, err := readObservation(parsed.one("observation"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_observation")
	}
	if observation.ScenarioID != scenario.ID {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "grade_rejected")
	}
	scenarioIdentity, identityFailure := standaloneResolutionIdentity("scenario", scenario)
	if identityFailure != nil {
		return standaloneOutcome{}, identityFailure
	}
	observationIdentity, identityFailure := standaloneResolutionIdentity("observation", observation)
	if identityFailure != nil {
		return standaloneOutcome{}, identityFailure
	}
	evidence := standaloneNewResolutionEvidence(
		"grade", "deterministic", "scenario-observation", 2,
		[]string{scenarioIdentity, observationIdentity},
		scenario.RequiredCapabilities,
	)
	if parsed.boolean("dry-run") || parsed.boolean("explain") {
		return standalonePreviewOutcome("grade", "deterministic", parsed, resolved, evidence, true)
	}
	result, err := agenteval.Evaluate(scenario, observation)
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "grade_rejected")
	}
	return standaloneOutcome{command: "grade", status: "completed", result: result, outputMode: parsed.outputModeValue(), text: "deterministic grade completed\n"}, nil
}

func standaloneExecuteCompare(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	kind, peekFailure := standalonePeekFlag(args, "kind")
	if peekFailure != nil {
		return standaloneOutcome{}, peekFailure
	}
	if kind == "pair" || kind == "set" {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(map[string]standaloneFlagSpec{
		"kind": {takesValue: true}, "input": {takesValue: true, repeatable: true}, "root": {takesValue: true},
	}))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || !standaloneOneOf(parsed.one("kind"), "results", "root", "experiment") {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_compare_options")
	}
	if parsed.one("kind") == "results" && (len(parsed.many("input")) == 0 || parsed.one("root") != "") {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_compare_options")
	}
	if (parsed.one("kind") == "root" || parsed.one("kind") == "experiment") &&
		(parsed.one("root") == "" || len(parsed.many("input")) != 0) {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_compare_options")
	}
	resolved, failure := resolveStandaloneConfig(parsed)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	var result any
	var evidence standaloneResolutionEvidence
	if parsed.one("kind") == "experiment" {
		report, err := agenteval.AnalyzeSequentialReferencePublicationContext(ctx, parsed.one("root"))
		if err != nil {
			return standaloneOutcome{}, standaloneAnalysisFailure(err)
		}
		identity, identityFailure := standaloneResolutionIdentity("analysis-report", report)
		if identityFailure != nil {
			return standaloneOutcome{}, identityFailure
		}
		result = report
		evidence = standaloneNewResolutionEvidence(
			"compare", "default", "experiment", int(report.Coverage.ReceivedRecords),
			[]string{identity}, nil,
		)
	} else if parsed.one("kind") == "root" {
		aggregate, err := agenteval.AggregateSyntheticOutputRoot(parsed.one("root"))
		if err != nil {
			return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_result_root")
		}
		identity, identityFailure := standaloneResolutionIdentity("result-root", aggregate)
		if identityFailure != nil {
			return standaloneOutcome{}, identityFailure
		}
		result = aggregate
		evidence = standaloneNewResolutionEvidence(
			"compare", "default", "root", aggregate.Results,
			[]string{identity}, nil,
		)
	} else {
		results := make([]agenteval.Result, 0, len(parsed.many("input")))
		identities := make([]string, 0, len(parsed.many("input")))
		capabilities := make([]string, 0)
		for _, path := range parsed.many("input") {
			if failure := standaloneContextFailure(ctx); failure != nil {
				return standaloneOutcome{}, failure
			}
			file, err := os.Open(path)
			if err != nil {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_result")
			}
			decoded, decodeErr := agenteval.DecodeResult(file)
			closeErr := file.Close()
			if decodeErr != nil || closeErr != nil {
				return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_result")
			}
			results = append(results, decoded)
			identity, identityFailure := standaloneResolutionIdentity("result", decoded)
			if identityFailure != nil {
				return standaloneOutcome{}, identityFailure
			}
			identities = append(identities, identity)
			capabilities = append(capabilities, decoded.UnavailableCapabilities...)
			for _, metric := range decoded.CapabilityFamilies {
				capabilities = append(capabilities, metric.Family)
			}
		}
		aggregate, err := agenteval.AggregateResults(results)
		if err != nil {
			return standaloneOutcome{}, standaloneFail(standaloneInputError, "comparison_rejected")
		}
		result = aggregate
		evidence = standaloneNewResolutionEvidence("compare", "default", "results", len(results), identities, capabilities)
	}
	if parsed.boolean("dry-run") || parsed.boolean("explain") {
		return standalonePreviewOutcome("compare", "default", parsed, resolved, evidence, true)
	}
	return standaloneOutcome{command: "compare", status: "completed", result: result, outputMode: parsed.outputModeValue(), text: "comparison completed\n"}, nil
}

func standaloneAnalysisFailure(err error) *standaloneFailure {
	if code, ok := agenteval.AnalysisErrorCodeOf(err); ok {
		switch code {
		case agenteval.AnalysisErrorInterrupted:
			failure := standaloneFail(standaloneInterruptedError, "analysis_interrupted")
			failure.retrySafe = true
			return failure
		case agenteval.AnalysisErrorLimitExceeded:
			return standaloneFail(standaloneInputError, "analysis_limit_exceeded")
		case agenteval.AnalysisErrorInvalidInput:
			return standaloneFail(standaloneInputError, "invalid_experiment_publication")
		case agenteval.AnalysisErrorInvalidReport:
			return standaloneFail(standaloneInternalError, "analysis_internal")
		}
	}
	return standaloneFail(standaloneInputError, "invalid_experiment_publication")
}

func standaloneExecuteInspect(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	kind, peekFailure := standalonePeekFlag(args, "kind")
	if peekFailure != nil {
		return standaloneOutcome{}, peekFailure
	}
	if kind == "artifact" {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(map[string]standaloneFlagSpec{
		"kind": {takesValue: true}, "root": {takesValue: true},
	}))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || !standaloneOneOf(parsed.one("kind"), "configuration", "corpus") {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_inspect_options")
	}
	if parsed.one("kind") == "corpus" && parsed.one("root") == "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_inspect_options")
	}
	if parsed.one("kind") == "configuration" && parsed.one("root") != "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_inspect_options")
	}
	resolved, failure := resolveStandaloneConfig(parsed)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	if parsed.one("kind") == "configuration" {
		evidence := standaloneNewResolutionEvidence("inspect", "default", "configuration", 0, nil, nil)
		return standalonePreviewOutcome("inspect", "default", parsed, resolved, evidence, resolved.localRead)
	}
	inventory, err := agenteval.ValidateBenchmarkCorpus(parsed.one("root"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_corpus")
	}
	identity, identityFailure := standaloneResolutionIdentity("corpus", inventory)
	if identityFailure != nil {
		return standaloneOutcome{}, identityFailure
	}
	capabilities := make([]string, 0, len(inventory.MCPTools))
	for _, tool := range inventory.MCPTools {
		capabilities = append(capabilities, tool.Tool)
	}
	evidence := standaloneNewResolutionEvidence("inspect", "default", "corpus", inventory.Runs, []string{identity}, capabilities)
	if parsed.boolean("dry-run") || parsed.boolean("explain") {
		return standalonePreviewOutcome("inspect", "default", parsed, resolved, evidence, true)
	}
	return standaloneOutcome{command: "inspect", status: "completed", result: inventory, outputMode: parsed.outputModeValue(), text: "corpus valid\n"}, nil
}

func standaloneResolutionIdentity(kind string, value any) (string, *standaloneFailure) {
	identity, err := standaloneSemanticIdentity(kind, value)
	if err != nil {
		return "", standaloneFail(standaloneInternalError, "identity_projection_failed")
	}
	return identity, nil
}

type standalonePreviewResult struct {
	Operation        string                         `json:"operation"`
	Mode             string                         `json:"mode"`
	AuthorityCeiling standaloneAuthorityProfile     `json:"authority_ceiling"`
	DryRunEffects    standaloneAuthorityDimensions  `json:"dry_run_effects"`
	Resolution       standaloneResolutionEvidence   `json:"resolution"`
	Configuration    standaloneConfigurationSummary `json:"configuration"`
}

func standalonePreviewOutcome(
	command, mode string,
	parsed standaloneParsedFlags,
	resolved standaloneResolvedConfig,
	evidence standaloneResolutionEvidence,
	localRead bool,
) (standaloneOutcome, *standaloneFailure) {
	authority, ok := standaloneAuthorityProfileFor(command, mode)
	if !ok {
		return standaloneOutcome{}, standaloneFail(standaloneInternalError, "authority_profile_missing")
	}
	status := "explained"
	if parsed.boolean("dry-run") {
		status = "planned"
	}
	result := standalonePreviewResult{
		Operation:        command,
		Mode:             mode,
		AuthorityCeiling: authority,
		DryRunEffects:    standaloneAuthorityDimensions{LocalRead: localRead || resolved.localRead},
		Resolution:       evidence,
		Configuration:    resolved.summary(),
	}
	return standaloneOutcome{command: command, status: status, result: result, outputMode: parsed.outputModeValue(), text: command + ": " + status + "\n"}, nil
}
