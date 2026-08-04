package agenteval

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
)

type headlessOutcomeInput struct {
	contract                resolvedRunContract
	trajectory              headlessTrajectory
	workspace               string
	runDir                  string
	durationMillis          int64
	runtime                 Runtime
	repetition              int
	taskContractSHA256      string
	executionContractSHA256 string
	attestation             *syntheticRunAttestation
	receipt                 *SyntheticRunReceipt
}

func finalizeHeadlessOutcome(input headlessOutcomeInput) (Result, error) {
	trajectory := input.trajectory
	providerMetrics := trajectory.providerMetrics
	evidenceAttempt, err := deriveRunnerEvidenceAttempt(trajectory.proxyRecords, providerMetrics.MCPToolCalls, providerMetrics.FailedMCPToolCalls, trajectory.guardDenials)
	if err != nil {
		return Result{}, fmt.Errorf("derive evidence attempt telemetry: %w", err)
	}
	evidenceReport, err := ParseEvidenceOutcomeReport(trajectory.final)
	if err != nil {
		return Result{}, err
	}
	if evidenceReport.Coverage && !evidenceReport.ConsistentWithAudit(evidenceAttempt) {
		return Result{}, fmt.Errorf("model evidence outcome contradicts audited attempts")
	}
	var outputBytes int64
	familyValues := map[string]CapabilityFamilyMetric{}
	capabilitySequence := make([]string, 0, len(trajectory.proxyRecords)+len(providerMetrics.CapabilityFamilySequence))
	familyCoverage := true
	for _, record := range trajectory.proxyRecords {
		outputBytes += record.StdoutBytes
		if record.Denied || record.CommandFamily == "" {
			familyCoverage = false
			continue
		}
		mergeCapabilityFamily(familyValues, record.CommandFamily, record.ExitCode != 0, record.StdoutBytes)
		capabilitySequence = append(capabilitySequence, record.CommandFamily)
	}
	outputBytes += providerMetrics.MCPToolOutputBytes
	providerFamilies := providerMetrics.CapabilityFamilies
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		outputBytes = trajectory.externalOutputBytes
		providerFamilies = trajectory.externalFamilies
		providerMetrics.CapabilityFamilyCoverage = true
	}
	for _, value := range providerFamilies {
		existing := familyValues[value.Family]
		existing.Family = value.Family
		existing.Invocations += value.Invocations
		existing.Successes += value.Successes
		existing.Failures += value.Failures
		existing.OutputBytes += value.OutputBytes
		familyValues[value.Family] = existing
	}
	capabilitySequence = append(capabilitySequence, providerMetrics.CapabilityFamilySequence...)
	if !providerMetrics.CapabilityFamilyCoverage {
		familyCoverage = false
	}
	providerMetrics.DurationMillis = input.durationMillis
	providerMetrics.Coverage["duration_millis"] = true
	if !providerMetrics.Coverage["estimated_cost_microusd"] && providerMetrics.Coverage["input_tokens"] && providerMetrics.Coverage["output_tokens"] {
		cost, err := estimateCost(providerMetrics.InputTokens, providerMetrics.OutputTokens, input.contract.spec.Pricing)
		if err != nil {
			return Result{}, err
		}
		providerMetrics.EstimatedCostMicroUSD = cost
		providerMetrics.Coverage["estimated_cost_microusd"] = true
	}
	providerMetrics.Coverage["interface_invocations"] = true
	legacyATLInvocations := 0
	if input.contract.scenario.Budgets.MaxInterfaceInvocations == 0 {
		// Legacy scenarios budget and require the historical atl-specific
		// metric. New multi-surface scenarios use only the generic metric so a
		// zero legacy budget cannot become a false violation.
		providerMetrics.Coverage["atl_invocations"] = true
		legacyATLInvocations = trajectory.atlInvocations
	}
	providerMetrics.Coverage["backend_requests"] = trajectory.httpMethodsObserved
	providerMetrics.Coverage["duplicate_backend_requests"] = trajectory.httpMethodsObserved
	providerMetrics.Coverage["remote_writes"] = trajectory.httpMethodsObserved
	providerMetrics.Coverage["output_bytes"] = true
	providerMetrics.Coverage["capability_families"] = familyCoverage
	capabilityFamilies := capabilityFamilySlice(familyValues)
	if !familyCoverage {
		capabilityFamilies = nil
	}
	checks, err := evaluateRunChecksWithCLIErrorContracts(
		input.contract.spec.Checks, trajectory.final, input.workspace, trajectory.atlInvocations, trajectory.failedATL, trajectory.unexpected,
		providerMetrics.SkillToolCalls+trajectory.guardSummary.SkillReadAdmissions,
		providerMetrics.SkillToolCallsByName, providerMetrics.Delegations, trajectory.guardDenials,
		trajectory.methods, trajectory.httpMethodsObserved, trajectory.cliExitCodes, capabilityFamilies, familyCoverage,
		capabilitySequence, providerMetrics.MCPInvocations,
		familyCoverage && providerMetrics.MCPInvocationCoverage, trajectory.cliErrorContracts,
	)
	if err != nil {
		return Result{}, err
	}
	backendObservation, safetyAssurance := BackendObservationHTTP, SafetyAssuranceObservedHTTP
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		backendObservation, safetyAssurance = BackendObservationOpaqueMCP, SafetyAssuranceReviewedROMCP
	}
	observation := Observation{
		SchemaVersion: ObservationSchemaVersion, ScenarioID: input.contract.scenario.ID,
		Variant: input.contract.spec.Variant, Surface: input.contract.spec.EffectiveSurface(), Runtime: input.runtime,
		BackendObservation: backendObservation, SafetyAssurance: safetyAssurance,
		Metrics: InputMetrics{
			AgentTurns: providerMetrics.AgentTurns, ToolCalls: providerMetrics.ToolCalls,
			ATLInvocations: legacyATLInvocations, InterfaceInvocations: trajectory.atlInvocations, Delegations: providerMetrics.Delegations,
			DuplicateBackendRequests: trajectory.duplicateRequests, OutputBytes: outputBytes,
			InputTokens: providerMetrics.InputTokens, OutputTokens: providerMetrics.OutputTokens,
			MainThreadInputTokens: providerMetrics.MainThreadInputTokens, MainThreadOutputTokens: providerMetrics.MainThreadOutputTokens,
			EstimatedCostMicroUSD: providerMetrics.EstimatedCostMicroUSD,
			DurationMillis:        providerMetrics.DurationMillis,
		},
		Coverage: providerMetrics.Coverage, HTTPMethods: trajectory.methods, Checks: checks,
		EvidenceAttempt:    evidenceAttempt,
		EvidenceReport:     evidenceReport,
		CapabilityFamilies: capabilityFamilies,
	}
	result, err := Evaluate(input.contract.scenario, observation)
	if err != nil {
		return Result{}, err
	}
	addRunCheckViolations(&result, input.contract.spec.Checks, input.contract.scenario.RequiredChecks)
	if result.Coverage["estimated_cost_microusd"] && result.Metrics.EstimatedCostMicroUSD > input.contract.spec.MaxEstimatedCostMicroUSD {
		result.Status = "fail"
		result.Violations = append(result.Violations, Violation{
			Code: "run_cost_cap_exceeded", Subject: "estimated_cost_microusd",
			Observed: result.Metrics.EstimatedCostMicroUSD, Limit: input.contract.spec.MaxEstimatedCostMicroUSD,
		})
		sort.Slice(result.Violations, func(i, j int) bool {
			if result.Violations[i].Code != result.Violations[j].Code {
				return result.Violations[i].Code < result.Violations[j].Code
			}
			return result.Violations[i].Subject < result.Violations[j].Subject
		})
	}
	sort.Slice(result.Violations, func(i, j int) bool {
		if result.Violations[i].Code != result.Violations[j].Code {
			return result.Violations[i].Code < result.Violations[j].Code
		}
		return result.Violations[i].Subject < result.Violations[j].Subject
	})
	resultPath := filepath.Join(input.runDir, "result.json")
	encoded, _ := json.MarshalIndent(result, "", "  ")
	encoded = append(encoded, '\n')
	if err := writePrivateFile(resultPath, encoded); err != nil {
		return Result{}, err
	}
	if input.attestation != nil {
		if input.receipt == nil {
			return Result{}, fmt.Errorf("synthetic run receipt destination is missing")
		}
		*input.receipt, err = newSyntheticRunReceipt(input.attestation, input.contract, input.runtime, input.repetition, input.taskContractSHA256, input.executionContractSHA256, encoded)
		if err != nil {
			return Result{}, err
		}
	}
	return result, nil
}
