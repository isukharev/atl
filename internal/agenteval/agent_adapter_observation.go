package agenteval

import (
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
)

const agentAdapterObservationFileName = "agent-observation.json"

func normalizeBuiltInAgentObservation(
	contract agentadapter.Contract,
	attemptID string,
	spec RunSpec,
	metrics ProviderMetrics,
	observedSkillUse bool,
) (agentadapter.Observation, []byte, error) {
	treeUsage, err := builtInAgentAdapterUsage(metrics)
	if err != nil {
		return agentadapter.Observation{}, nil, err
	}
	parentUsage, attributed, err := builtInAgentAdapterParentUsage(metrics, treeUsage)
	if err != nil {
		return agentadapter.Observation{}, nil, err
	}
	capabilities := requiredAgentAdapterCapabilities(spec)
	activation := agentadapter.Activation{Mode: agentadapter.ActivationUnavailable, UseEvidence: agentadapter.UseEvidenceUnavailable}
	switch spec.SkillActivationIdentity() {
	case SkillActivationImplicit:
		activation.Mode = agentadapter.ActivationNative
	case SkillActivationExplicit:
		activation.Mode = agentadapter.ActivationForcedInjection
	case SkillActivationDeveloper:
		activation.Mode = agentadapter.ActivationDeveloperInstructions
	case SkillActivationCombined:
		activation.Mode = agentadapter.ActivationDeveloperAndForced
	}
	if activation.Mode != agentadapter.ActivationUnavailable && observedSkillUse {
		activation.UseEvidence = agentadapter.UseEvidenceObserved
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	events := []agentadapter.Event{{
		Sequence: 1, NodeID: "primary", Type: agentadapter.EventStart,
		Start: &agentadapter.Start{Role: agentadapter.RolePrimary, Capabilities: capabilities, Activation: activation},
	}, {
		Sequence: 2, NodeID: "primary", Type: agentadapter.EventTerminal,
		Terminal: &agentadapter.Terminal{State: agentadapter.TerminalSucceeded, Usage: parentUsage},
	}}
	var observation agentadapter.Observation
	if attributed {
		observation, err = agentadapter.Normalize(contract, attemptID, agentadapter.ProfileSingle, events)
	} else {
		observation, err = agentadapter.NormalizeWithReportedTreeUsage(contract, attemptID, agentadapter.ProfileSingle, events, treeUsage)
	}
	if err != nil {
		return agentadapter.Observation{}, nil, err
	}
	data, err := agentadapter.EncodeObservation(contract, observation)
	if err != nil {
		return observation, nil, err
	}
	return observation, data, nil
}

func builtInAgentAdapterParentUsage(metrics ProviderMetrics, tree agentadapter.Usage) (agentadapter.Usage, bool, error) {
	if metrics.Coverage["delegations"] && metrics.Delegations == 0 {
		return tree, true, nil
	}
	input, err := agentAdapterMetric(metrics.Coverage["main_thread_input_tokens"], metrics.MainThreadInputTokens)
	if err != nil {
		return agentadapter.Usage{}, false, err
	}
	output, err := agentAdapterMetric(metrics.Coverage["main_thread_output_tokens"], metrics.MainThreadOutputTokens)
	if err != nil {
		return agentadapter.Usage{}, false, err
	}
	return agentadapter.Usage{
		EstimatedCostMicroUSD: agentadapter.UnknownMetric(),
		InputTokens:           input,
		OutputTokens:          output,
		ToolCalls:             agentadapter.UnknownMetric(),
		EvidenceItems:         agentadapter.UnknownMetric(),
	}, false, nil
}

func builtInAgentAdapterUsage(metrics ProviderMetrics) (agentadapter.Usage, error) {
	cost, err := agentAdapterMetric(metrics.Coverage["estimated_cost_microusd"], metrics.EstimatedCostMicroUSD)
	if err != nil {
		return agentadapter.Usage{}, err
	}
	input, err := agentAdapterMetric(metrics.Coverage["input_tokens"], metrics.InputTokens)
	if err != nil {
		return agentadapter.Usage{}, err
	}
	output, err := agentAdapterMetric(metrics.Coverage["output_tokens"], metrics.OutputTokens)
	if err != nil {
		return agentadapter.Usage{}, err
	}
	tools, err := agentAdapterMetric(metrics.Coverage["tool_calls"], int64(metrics.ToolCalls))
	if err != nil {
		return agentadapter.Usage{}, err
	}
	return agentadapter.Usage{EstimatedCostMicroUSD: cost, InputTokens: input, OutputTokens: output,
		ToolCalls: tools, EvidenceItems: agentadapter.UnknownMetric()}, nil
}

func agentAdapterMetric(covered bool, value int64) (agentadapter.Metric, error) {
	if !covered {
		return agentadapter.UnknownMetric(), nil
	}
	if value < 0 {
		return agentadapter.Metric{}, fmt.Errorf("covered agent adapter metric is negative")
	}
	return agentadapter.ObservedMetric(uint64(value)), nil // #nosec G115 -- nonnegative guard above.
}

func bindAgentObservationReceipt(receipt, observationSHA256 string) (string, error) {
	if observationSHA256 == "" {
		return receipt, nil
	}
	return contentMinimizedAttemptDigest("agent-observation-terminal", []string{receipt, observationSHA256})
}
