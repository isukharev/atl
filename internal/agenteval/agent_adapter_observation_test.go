package agenteval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
)

func TestBuiltInAgentObservationPreservesActivationAndMetricPresence(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "codex"
	spec.BackendMode = BackendModePrivateLive
	spec.Surface = SurfaceCLISkill
	spec.ToolTransport = "cli"
	spec.SkillActivation = SkillActivationCombined
	contract, _, err := builtInAgentAdapterContract(spec, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	metrics := ProviderMetrics{InputTokens: 0, OutputTokens: 77, ToolCalls: 0, SkillToolCalls: 0,
		Coverage: map[string]bool{"delegations": true, "input_tokens": true, "output_tokens": false, "tool_calls": true}}
	observation, data, err := normalizeBuiltInAgentObservation(contract, strings.Repeat("d", 64), spec, metrics, true)
	if err != nil {
		t.Fatalf("start=%+v terminal=%+v err=%v", observation.Events[0].Start, observation.Events[1].Terminal, err)
	}
	if !observation.Coverage || len(observation.Events) != 2 || observation.Events[0].Start == nil ||
		observation.Events[0].Start.Activation.Mode != agentadapter.ActivationDeveloperAndForced ||
		observation.Events[0].Start.Activation.UseEvidence != agentadapter.UseEvidenceObserved {
		t.Fatalf("observation=%+v", observation)
	}
	if got := observation.ParentUsage.InputTokens; got.State != agentadapter.MetricObserved || got.Value == nil || *got.Value != 0 {
		t.Fatalf("observed zero changed: %+v", got)
	}
	if got := observation.TreeUsage.OutputTokens; got.State != agentadapter.MetricUnknown || got.Value != nil {
		t.Fatalf("missing output coverage was imputed: %+v", got)
	}
	if got := observation.TreeUsage.EstimatedCostMicroUSD; got.State != agentadapter.MetricUnknown || got.Value != nil {
		t.Fatalf("missing cost coverage was imputed: %+v", got)
	}
	decoded, err := DecodeAgentAdapterObservation(bytes.NewReader(data), contract)
	if err != nil || decoded.AdapterContractSHA256 != observation.AdapterContractSHA256 {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
}

func TestBuiltInAgentObservationKeepsUnattributedParentAndTreeUsageDistinct(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "claude-code"
	spec.Model = "claude-test-1"
	spec.Pricing = Pricing{}
	contract, _, err := builtInAgentAdapterContract(spec, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	metrics := ProviderMetrics{Delegations: 1, InputTokens: 90, MainThreadInputTokens: 30, OutputTokens: 12,
		MainThreadOutputTokens: 4, EstimatedCostMicroUSD: 7, ToolCalls: 2,
		Coverage: map[string]bool{"delegations": true, "input_tokens": true, "main_thread_input_tokens": true,
			"output_tokens": true, "main_thread_output_tokens": true, "estimated_cost_microusd": true, "tool_calls": true}}
	observation, _, err := normalizeBuiltInAgentObservation(contract, strings.Repeat("d", 64), spec, metrics, false)
	if err != nil || observation.Coverage || !containsAgentAdapterIssue(observation.Issues, agentadapter.IssueTreeUnattributed) {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
	if observation.ParentUsage.InputTokens.Value == nil || *observation.ParentUsage.InputTokens.Value != 30 ||
		observation.TreeUsage.InputTokens.Value == nil || *observation.TreeUsage.InputTokens.Value != 90 ||
		observation.ParentUsage.EstimatedCostMicroUSD.State != agentadapter.MetricUnknown ||
		observation.TreeUsage.EstimatedCostMicroUSD.State != agentadapter.MetricObserved {
		t.Fatalf("parent=%+v tree=%+v", observation.ParentUsage, observation.TreeUsage)
	}
}

func containsAgentAdapterIssue(values []agentadapter.IssueCode, want agentadapter.IssueCode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
