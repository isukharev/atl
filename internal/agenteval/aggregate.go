package agenteval

import (
	"fmt"
	"sort"
)

const AggregateSchemaVersion = 1

type Aggregate struct {
	SchemaVersion int              `json:"schema_version"`
	Groups        []AggregateGroup `json:"groups"`
}

type AggregateGroup struct {
	ScenarioID  string           `json:"scenario_id"`
	TaskClass   string           `json:"task_class"`
	DataClass   string           `json:"data_class"`
	Variant     string           `json:"variant"`
	Runtime     Runtime          `json:"runtime"`
	Runs        int              `json:"runs"`
	Passes      int              `json:"passes"`
	SuccessRate float64          `json:"success_rate"`
	Metrics     AggregateMetrics `json:"metrics"`
}

type AggregateMetrics struct {
	AgentTurns            Quantiles `json:"agent_turns"`
	ToolCalls             Quantiles `json:"tool_calls"`
	ATLInvocations        Quantiles `json:"atl_invocations"`
	BackendRequests       Quantiles `json:"backend_requests"`
	OutputBytes           Quantiles `json:"output_bytes"`
	InputTokens           Quantiles `json:"input_tokens"`
	OutputTokens          Quantiles `json:"output_tokens"`
	EstimatedCostMicroUSD Quantiles `json:"estimated_cost_microusd"`
	DurationMillis        Quantiles `json:"duration_millis"`
}

type Quantiles struct {
	P50 int64 `json:"p50"`
	P90 int64 `json:"p90"`
}

type aggregateKey struct {
	ScenarioID, TaskClass, DataClass, Variant            string
	Provider, AgentVersion, Model, Reasoning, ATLVersion string
	PluginVersion, SkillDigest                           string
}

func AggregateResults(results []Result) (Aggregate, error) {
	groups := map[aggregateKey][]Result{}
	for index, result := range results {
		if err := result.Validate(); err != nil {
			return Aggregate{}, fmt.Errorf("result %d: %w", index, err)
		}
		key := aggregateKey{
			ScenarioID: result.ScenarioID, TaskClass: result.TaskClass, DataClass: result.DataClass, Variant: result.Variant,
			Provider: result.Runtime.Provider, AgentVersion: result.Runtime.AgentVersion,
			Model: result.Runtime.Model, Reasoning: result.Runtime.Reasoning,
			ATLVersion: result.Runtime.ATLVersion, PluginVersion: result.Runtime.PluginVersion,
			SkillDigest: result.Runtime.SkillDigest,
		}
		groups[key] = append(groups[key], result)
	}
	keys := make([]aggregateKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return aggregateKeyString(keys[i]) < aggregateKeyString(keys[j]) })
	out := Aggregate{SchemaVersion: AggregateSchemaVersion, Groups: make([]AggregateGroup, 0, len(keys))}
	for _, key := range keys {
		items := groups[key]
		group := AggregateGroup{
			ScenarioID: key.ScenarioID, TaskClass: key.TaskClass, DataClass: key.DataClass, Variant: key.Variant,
			Runtime: Runtime{
				Provider: key.Provider, AgentVersion: key.AgentVersion, Model: key.Model,
				Reasoning: key.Reasoning, ATLVersion: key.ATLVersion,
				PluginVersion: key.PluginVersion, SkillDigest: key.SkillDigest,
			},
			Runs: len(items),
		}
		turns, tools, invocations, requests := make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items))
		bytesOut, input, output, cost, duration := make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items))
		for _, item := range items {
			if item.Status == "pass" {
				group.Passes++
			}
			turns = append(turns, int64(item.Metrics.AgentTurns))
			tools = append(tools, int64(item.Metrics.ToolCalls))
			invocations = append(invocations, int64(item.Metrics.ATLInvocations))
			requests = append(requests, int64(item.Metrics.BackendRequests))
			bytesOut = append(bytesOut, item.Metrics.OutputBytes)
			input = append(input, item.Metrics.InputTokens)
			output = append(output, item.Metrics.OutputTokens)
			cost = append(cost, item.Metrics.EstimatedCostMicroUSD)
			duration = append(duration, item.Metrics.DurationMillis)
		}
		group.SuccessRate = float64(group.Passes) / float64(group.Runs)
		group.Metrics = AggregateMetrics{
			AgentTurns: quantiles(turns), ToolCalls: quantiles(tools),
			ATLInvocations: quantiles(invocations), BackendRequests: quantiles(requests),
			OutputBytes: quantiles(bytesOut), InputTokens: quantiles(input),
			OutputTokens: quantiles(output), EstimatedCostMicroUSD: quantiles(cost),
			DurationMillis: quantiles(duration),
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

func (r Result) Validate() error {
	if r.SchemaVersion != ResultSchemaVersion {
		return fmt.Errorf("unsupported result schema_version %d", r.SchemaVersion)
	}
	if !identifierRE.MatchString(r.ScenarioID) || !identifierRE.MatchString(r.TaskClass) || !identifierRE.MatchString(r.Variant) {
		return fmt.Errorf("result identity is invalid")
	}
	if r.DataClass != "synthetic" && r.DataClass != "private-local" {
		return fmt.Errorf("result data_class is invalid")
	}
	if err := r.Runtime.validate(); err != nil {
		return err
	}
	if r.Status != "pass" && r.Status != "fail" {
		return fmt.Errorf("result status must be pass or fail")
	}
	if (r.Status == "pass") != (len(r.Violations) == 0) {
		return fmt.Errorf("result status and violations disagree")
	}
	if len(r.HTTPMethods) > maxContractListEntries || len(r.Checks) > maxContractListEntries || len(r.Violations) > maxContractListEntries || len(r.Warnings) > maxContractListEntries {
		return fmt.Errorf("result exceeds %d entries in a bounded collection", maxContractListEntries)
	}
	for method, count := range r.HTTPMethods {
		if !methodRE.MatchString(method) || count < 0 || count > maxObservedMethodCount {
			return fmt.Errorf("invalid result HTTP method %q=%d", method, count)
		}
	}
	for check := range r.Checks {
		if !identifierRE.MatchString(check) {
			return fmt.Errorf("invalid result check %q", check)
		}
	}
	if err := validateIdentifierList("warnings", r.Warnings, false); err != nil {
		return err
	}
	for _, violation := range r.Violations {
		if !identifierRE.MatchString(violation.Code) || !identifierRE.MatchString(violation.Subject) || violation.Observed < 0 || violation.Limit < 0 {
			return fmt.Errorf("invalid result violation")
		}
	}
	metrics := InputMetrics{
		AgentTurns: r.Metrics.AgentTurns, ToolCalls: r.Metrics.ToolCalls,
		ATLInvocations: r.Metrics.ATLInvocations, OutputBytes: r.Metrics.OutputBytes,
		InputTokens: r.Metrics.InputTokens, OutputTokens: r.Metrics.OutputTokens,
		EstimatedCostMicroUSD: r.Metrics.EstimatedCostMicroUSD,
		DurationMillis:        r.Metrics.DurationMillis,
	}
	if err := metrics.validate(); err != nil || r.Metrics.BackendRequests < 0 || r.Metrics.RemoteWrites < 0 {
		return fmt.Errorf("invalid result metrics")
	}
	var requests, writes int
	for method, count := range r.HTTPMethods {
		requests += count
		if method != "GET" && method != "HEAD" && method != "OPTIONS" {
			writes += count
		}
	}
	if requests != r.Metrics.BackendRequests || writes != r.Metrics.RemoteWrites {
		return fmt.Errorf("result HTTP method counts disagree with metrics")
	}
	return nil
}

func quantiles(values []int64) Quantiles {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return Quantiles{P50: nearestRank(sorted, 50), P90: nearestRank(sorted, 90)}
}

func nearestRank(sorted []int64, percentile int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (percentile*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func aggregateKeyString(key aggregateKey) string {
	return key.ScenarioID + "\x00" + key.TaskClass + "\x00" + key.DataClass + "\x00" + key.Variant + "\x00" + key.Provider + "\x00" + key.AgentVersion + "\x00" + key.Model + "\x00" + key.Reasoning + "\x00" + key.ATLVersion + "\x00" + key.PluginVersion + "\x00" + key.SkillDigest
}
