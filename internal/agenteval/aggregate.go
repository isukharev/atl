package agenteval

import (
	"fmt"
	"sort"
)

const AggregateSchemaVersion = 4

type Aggregate struct {
	SchemaVersion int              `json:"schema_version"`
	Groups        []AggregateGroup `json:"groups"`
}

type AggregateGroup struct {
	ScenarioID           string                         `json:"scenario_id"`
	TaskClass            string                         `json:"task_class"`
	DataClass            string                         `json:"data_class"`
	Category             string                         `json:"category"`
	Variant              string                         `json:"variant"`
	Surface              string                         `json:"surface"`
	Runtime              Runtime                        `json:"runtime"`
	Runs                 int                            `json:"runs"`
	EligibleRuns         int                            `json:"eligible_runs"`
	UnsupportedRuns      int                            `json:"unsupported_runs"`
	DriftedRuns          int                            `json:"drifted_runs"`
	CoverageRate         float64                        `json:"coverage_rate"`
	Passes               int                            `json:"passes"`
	SuccessRate          float64                        `json:"success_rate"`
	Metrics              AggregateMetrics               `json:"metrics"`
	Qualitative          *AggregateQualitative          `json:"qualitative,omitempty"`
	QualitativeReviewSet *AggregateQualitativeReviewSet `json:"qualitative_review_set,omitempty"`
	CapabilityFamilies   []AggregateCapabilityFamily    `json:"capability_families,omitempty"`
}

type AggregateCapabilityFamily struct {
	Family      string    `json:"family"`
	Invocations Quantiles `json:"invocations"`
	Successes   Quantiles `json:"successes"`
	Failures    Quantiles `json:"failures"`
	OutputBytes Quantiles `json:"output_bytes"`
}

type AggregateQualitative struct {
	RubricID     string    `json:"rubric_id"`
	RubricSHA256 string    `json:"rubric_sha256"`
	Reviewer     Reviewer  `json:"reviewer"`
	Passes       int       `json:"passes"`
	ScoreBPS     Quantiles `json:"score_bps"`
}

type AggregateQualitativeReviewSet struct {
	ContractSHA256 string                 `json:"contract_sha256"`
	RubricID       string                 `json:"rubric_id"`
	RubricSHA256   string                 `json:"rubric_sha256"`
	Policy         QualitativePanelPolicy `json:"policy"`
	Passes         int                    `json:"passes"`
	Failures       int                    `json:"failures"`
	Disagreements  int                    `json:"disagreements"`
	ScoreBPS       Quantiles              `json:"score_bps"`
}

type AggregateMetrics struct {
	AgentTurns               Quantiles `json:"agent_turns"`
	ToolCalls                Quantiles `json:"tool_calls"`
	ATLInvocations           Quantiles `json:"atl_invocations"`
	InterfaceInvocations     Quantiles `json:"interface_invocations"`
	Delegations              Quantiles `json:"delegations"`
	BackendRequests          Quantiles `json:"backend_requests"`
	DuplicateBackendRequests Quantiles `json:"duplicate_backend_requests"`
	OutputBytes              Quantiles `json:"output_bytes"`
	InputTokens              Quantiles `json:"input_tokens"`
	OutputTokens             Quantiles `json:"output_tokens"`
	MainThreadInputTokens    Quantiles `json:"main_thread_input_tokens"`
	MainThreadOutputTokens   Quantiles `json:"main_thread_output_tokens"`
	EstimatedCostMicroUSD    Quantiles `json:"estimated_cost_microusd"`
	DurationMillis           Quantiles `json:"duration_millis"`
}

type Quantiles struct {
	ObservedRuns int   `json:"observed_runs"`
	P50          int64 `json:"p50"`
	P90          int64 `json:"p90"`
}

type aggregateKey struct {
	ScenarioID, TaskClass, DataClass, Category, Variant, Surface string
	Provider, AgentVersion, Model, Reasoning, ATLVersion         string
	PluginVersion, SkillDigest                                   string
	ReviewerID, ReviewerKind, ReviewerModel                      string
	RubricID, RubricSHA256, AssignmentDigest                     string
	ReviewSetContractSHA256                                      string
}

func AggregateResults(results []Result) (Aggregate, error) {
	groups := map[aggregateKey][]Result{}
	for index, result := range results {
		if err := result.Validate(); err != nil {
			return Aggregate{}, fmt.Errorf("result %d: %w", index, err)
		}
		key := aggregateKey{
			ScenarioID: result.ScenarioID, TaskClass: result.TaskClass, DataClass: result.DataClass,
			Category: result.EffectiveCategory(), Variant: result.Variant, Surface: result.EffectiveSurface(),
			Provider: result.Runtime.Provider, AgentVersion: result.Runtime.AgentVersion,
			Model: result.Runtime.Model, Reasoning: result.Runtime.Reasoning,
			ATLVersion: result.Runtime.ATLVersion, PluginVersion: result.Runtime.PluginVersion,
			SkillDigest: result.Runtime.SkillDigest,
		}
		if result.Qualitative != nil {
			key.ReviewerID = result.Qualitative.Reviewer.ID
			key.ReviewerKind = result.Qualitative.Reviewer.Kind
			key.ReviewerModel = result.Qualitative.Reviewer.Model
			key.RubricID = result.Qualitative.RubricID
			key.RubricSHA256 = result.Qualitative.RubricSHA256
			key.AssignmentDigest = result.Qualitative.AssignmentDigest
		}
		if result.QualitativeReviewSet != nil {
			key.RubricID = result.QualitativeReviewSet.RubricID
			key.RubricSHA256 = result.QualitativeReviewSet.RubricSHA256
			key.ReviewSetContractSHA256 = result.QualitativeReviewSet.ContractSHA256
			key.AssignmentDigest = result.QualitativeReviewSet.AssignmentDigest
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
			ScenarioID: key.ScenarioID, TaskClass: key.TaskClass, DataClass: key.DataClass,
			Category: key.Category, Variant: key.Variant, Surface: key.Surface,
			Runtime: Runtime{
				Provider: key.Provider, AgentVersion: key.AgentVersion, Model: key.Model,
				Reasoning: key.Reasoning, ATLVersion: key.ATLVersion,
				PluginVersion: key.PluginVersion, SkillDigest: key.SkillDigest,
			},
			Runs: len(items),
		}
		turns, tools, invocations, interfaceInvocations, delegations, requests, duplicates := make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items))
		bytesOut, input, output, mainInput, mainOutput, cost, duration := make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items))
		for _, item := range items {
			switch item.EffectiveEligibility() {
			case EligibilitySupported:
				group.EligibleRuns++
			case EligibilityUnsupportedCapability:
				group.UnsupportedRuns++
			case EligibilityInvalidatedDrift:
				group.DriftedRuns++
			}
			if item.EffectiveEligibility() == EligibilitySupported && item.Status == "pass" {
				group.Passes++
			}
			if key.Category != BenchmarkCategoryRouteFixed && !deterministicValidForEfficiency(item) {
				continue
			}
			turns = appendCovered(turns, item.Coverage, "agent_turns", int64(item.Metrics.AgentTurns))
			tools = appendCovered(tools, item.Coverage, "tool_calls", int64(item.Metrics.ToolCalls))
			invocations = appendCovered(invocations, item.Coverage, "atl_invocations", int64(item.Metrics.ATLInvocations))
			interfaceInvocations = appendCovered(interfaceInvocations, item.Coverage, "interface_invocations", int64(item.Metrics.InterfaceInvocations))
			delegations = appendCovered(delegations, item.Coverage, "delegations", int64(item.Metrics.Delegations))
			requests = appendCovered(requests, item.Coverage, "backend_requests", int64(item.Metrics.BackendRequests))
			duplicates = appendCovered(duplicates, item.Coverage, "duplicate_backend_requests", int64(item.Metrics.DuplicateBackendRequests))
			bytesOut = appendCovered(bytesOut, item.Coverage, "output_bytes", item.Metrics.OutputBytes)
			input = appendCovered(input, item.Coverage, "input_tokens", item.Metrics.InputTokens)
			output = appendCovered(output, item.Coverage, "output_tokens", item.Metrics.OutputTokens)
			mainInput = appendCovered(mainInput, item.Coverage, "main_thread_input_tokens", item.Metrics.MainThreadInputTokens)
			mainOutput = appendCovered(mainOutput, item.Coverage, "main_thread_output_tokens", item.Metrics.MainThreadOutputTokens)
			cost = appendCovered(cost, item.Coverage, "estimated_cost_microusd", item.Metrics.EstimatedCostMicroUSD)
			duration = appendCovered(duration, item.Coverage, "duration_millis", item.Metrics.DurationMillis)
		}
		coverageDenominator := group.EligibleRuns + group.UnsupportedRuns
		if coverageDenominator > 0 {
			// Backend drift invalidates the block rather than demonstrating that
			// an interface does or does not support the task.
			group.CoverageRate = float64(group.EligibleRuns) / float64(coverageDenominator)
		}
		if group.EligibleRuns > 0 {
			group.SuccessRate = float64(group.Passes) / float64(group.EligibleRuns)
		}
		group.Metrics = AggregateMetrics{
			AgentTurns: quantiles(turns), ToolCalls: quantiles(tools),
			ATLInvocations: quantiles(invocations), InterfaceInvocations: quantiles(interfaceInvocations), Delegations: quantiles(delegations),
			BackendRequests: quantiles(requests), DuplicateBackendRequests: quantiles(duplicates),
			OutputBytes: quantiles(bytesOut), InputTokens: quantiles(input),
			OutputTokens: quantiles(output), MainThreadInputTokens: quantiles(mainInput),
			MainThreadOutputTokens: quantiles(mainOutput), EstimatedCostMicroUSD: quantiles(cost),
			DurationMillis: quantiles(duration),
		}
		familyNames := map[string]struct{}{}
		coveredRuns := 0
		for _, item := range items {
			if key.Category != BenchmarkCategoryRouteFixed && !deterministicValidForEfficiency(item) {
				continue
			}
			if item.Coverage["capability_families"] {
				coveredRuns++
				for _, family := range item.CapabilityFamilies {
					familyNames[family.Family] = struct{}{}
				}
			}
		}
		names := make([]string, 0, len(familyNames))
		for name := range familyNames {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			invocations, successes, failures, bytesOut := make([]int64, 0, coveredRuns), make([]int64, 0, coveredRuns), make([]int64, 0, coveredRuns), make([]int64, 0, coveredRuns)
			for _, item := range items {
				if key.Category != BenchmarkCategoryRouteFixed && !deterministicValidForEfficiency(item) {
					continue
				}
				if !item.Coverage["capability_families"] {
					continue
				}
				value := CapabilityFamilyMetric{}
				for _, family := range item.CapabilityFamilies {
					if family.Family == name {
						value = family
						break
					}
				}
				invocations = append(invocations, int64(value.Invocations))
				successes = append(successes, int64(value.Successes))
				failures = append(failures, int64(value.Failures))
				bytesOut = append(bytesOut, value.OutputBytes)
			}
			group.CapabilityFamilies = append(group.CapabilityFamilies, AggregateCapabilityFamily{Family: name, Invocations: quantiles(invocations), Successes: quantiles(successes), Failures: quantiles(failures), OutputBytes: quantiles(bytesOut)})
		}
		if key.ReviewerKind != "" {
			scores := make([]int64, 0, len(items))
			passes := 0
			for _, item := range items {
				if key.Category != BenchmarkCategoryRouteFixed && !deterministicValidForEfficiency(item) {
					continue
				}
				if item.Qualitative == nil {
					continue
				}
				scores = append(scores, int64(item.Qualitative.ScoreBPS))
				if item.Qualitative.Status == "pass" {
					passes++
				}
			}
			group.Qualitative = &AggregateQualitative{RubricID: key.RubricID, RubricSHA256: key.RubricSHA256, Reviewer: Reviewer{ID: key.ReviewerID, Kind: key.ReviewerKind, Model: key.ReviewerModel}, Passes: passes, ScoreBPS: quantiles(scores)}
		}
		if key.ReviewSetContractSHA256 != "" {
			scores := make([]int64, 0, len(items))
			passes, failures, disagreements := 0, 0, 0
			var policy QualitativePanelPolicy
			for _, item := range items {
				if key.Category != BenchmarkCategoryRouteFixed && !deterministicValidForEfficiency(item) {
					continue
				}
				if item.QualitativeReviewSet == nil {
					continue
				}
				policy = item.QualitativeReviewSet.Policy
				scores = append(scores, int64(item.QualitativeReviewSet.ScoreBPS))
				switch item.QualitativeReviewSet.Status {
				case "pass":
					passes++
				case "fail":
					failures++
				case "disagreement":
					disagreements++
				}
			}
			group.QualitativeReviewSet = &AggregateQualitativeReviewSet{ContractSHA256: key.ReviewSetContractSHA256, RubricID: key.RubricID, RubricSHA256: key.RubricSHA256, Policy: policy, Passes: passes, Failures: failures, Disagreements: disagreements, ScoreBPS: quantiles(scores)}
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

func deterministicValidForEfficiency(result Result) bool {
	if result.EffectiveEligibility() != EligibilitySupported {
		return false
	}
	for _, violation := range result.Violations {
		if violation.Code != "qualitative_review_failed" && violation.Code != "qualitative_review_disagreement" {
			return false
		}
	}
	return true
}

func (r Result) Validate() error {
	if r.SchemaVersion != ResultSchemaVersion && r.SchemaVersion != LegacyResultSchemaVersion {
		return fmt.Errorf("unsupported result schema_version %d", r.SchemaVersion)
	}
	if r.SchemaVersion == LegacyResultSchemaVersion && r.QualitativeReviewSet != nil {
		return fmt.Errorf("legacy result cannot contain a qualitative review set")
	}
	if !identifierRE.MatchString(r.ScenarioID) || !identifierRE.MatchString(r.TaskClass) || !identifierRE.MatchString(r.Variant) {
		return fmt.Errorf("result identity is invalid")
	}
	if !validBenchmarkCategory(r.EffectiveCategory()) || !validResultSurface(r.EffectiveSurface()) {
		return fmt.Errorf("result benchmark identity is invalid")
	}
	if r.DataClass != "synthetic" && r.DataClass != "private-local" {
		return fmt.Errorf("result data_class is invalid")
	}
	if err := r.Runtime.validate(); err != nil {
		return err
	}
	eligibility := r.EffectiveEligibility()
	if err := validateEligibility(eligibility, r.UnavailableCapabilities); err != nil {
		return fmt.Errorf("result eligibility: %w", err)
	}
	if r.Qualitative != nil && r.QualitativeReviewSet != nil {
		return fmt.Errorf("result cannot contain both legacy and panel qualitative assessments")
	}
	if r.Qualitative != nil {
		if eligibility != EligibilitySupported {
			return fmt.Errorf("ineligible result cannot have a qualitative assessment")
		}
		if r.EffectiveCategory() == BenchmarkCategoryNeutralCommon && (!r.Qualitative.Blinded || !validSHA256(r.Qualitative.AssignmentDigest)) {
			return fmt.Errorf("neutral-common qualitative assessment must be blinded")
		}
		if err := r.Qualitative.validate(r.ScenarioID); err != nil {
			return err
		}
		if r.Qualitative.Status == "fail" && r.Status != "fail" {
			return fmt.Errorf("failed qualitative assessment requires failed result")
		}
		found := false
		for _, violation := range r.Violations {
			if violation.Code == "qualitative_review_failed" && violation.Subject == r.Qualitative.RubricID {
				found = true
			}
		}
		if found != (r.Qualitative.Status == "fail") {
			return fmt.Errorf("qualitative status and violation disagree")
		}
	}
	if r.QualitativeReviewSet != nil {
		assessment := r.QualitativeReviewSet
		if eligibility != EligibilitySupported {
			return fmt.Errorf("ineligible result cannot have a qualitative review-set assessment")
		}
		if r.EffectiveCategory() == BenchmarkCategoryNeutralCommon && (!assessment.Blinded || !validSHA256(assessment.AssignmentDigest)) {
			return fmt.Errorf("neutral-common qualitative review-set assessment must be blinded")
		}
		if err := assessment.validate(r.ScenarioID); err != nil {
			return err
		}
		if assessment.Status != "pass" && r.Status != "fail" {
			return fmt.Errorf("non-passing qualitative review-set assessment requires failed result")
		}
		failureCount, disagreementCount := 0, 0
		for _, violation := range r.Violations {
			if violation.Subject != assessment.RubricID {
				continue
			}
			switch violation.Code {
			case "qualitative_review_failed":
				failureCount++
				if violation.Observed != int64(assessment.ScoreBPS) || violation.Limit != int64(assessment.MinimumScoreBPS) {
					return fmt.Errorf("qualitative review-set failure violation is inconsistent")
				}
			case "qualitative_review_disagreement":
				disagreementCount++
				if violation.Observed != 1 || violation.Limit != 0 {
					return fmt.Errorf("qualitative review-set disagreement violation is inconsistent")
				}
			}
		}
		if failureCount != boolCount(assessment.Status == "fail") || disagreementCount != boolCount(assessment.Status == "disagreement") {
			return fmt.Errorf("qualitative review-set status and violation disagree")
		}
	}
	if r.Status != "pass" && r.Status != "fail" && r.Status != "ineligible" {
		return fmt.Errorf("result status must be pass, fail, or ineligible")
	}
	if eligibility == EligibilitySupported && ((r.Status == "pass") != (len(r.Violations) == 0) || r.Status == "ineligible") {
		return fmt.Errorf("result status and violations disagree")
	}
	if eligibility != EligibilitySupported && r.Status != "ineligible" {
		return fmt.Errorf("ineligible result must use ineligible status")
	}
	if len(r.Coverage) > len(metricNames) || len(r.HTTPMethods) > maxContractListEntries || len(r.Checks) > maxContractListEntries || len(r.Violations) > maxContractListEntries || len(r.Warnings) > maxContractListEntries {
		return fmt.Errorf("result exceeds %d entries in a bounded collection", maxContractListEntries)
	}
	for name := range r.Coverage {
		if _, ok := metricNames[name]; !ok {
			return fmt.Errorf("unknown covered metric %q", name)
		}
	}
	if !r.Coverage["backend_requests"] && len(r.HTTPMethods) != 0 {
		return fmt.Errorf("result http_methods require backend_requests coverage")
	}
	if r.Coverage["duplicate_backend_requests"] && !r.Coverage["backend_requests"] {
		return fmt.Errorf("result duplicate_backend_requests coverage requires backend_requests coverage")
	}
	if r.Coverage["remote_writes"] && !r.Coverage["backend_requests"] {
		return fmt.Errorf("result remote_writes coverage requires backend_requests coverage")
	}
	if err := validateBackendAssurance(r.EffectiveSurface(), r.BackendObservation, r.SafetyAssurance, r.Coverage, r.HTTPMethods); err != nil {
		return fmt.Errorf("result backend assurance: %w", err)
	}
	if !r.Coverage["capability_families"] && len(r.CapabilityFamilies) != 0 {
		return fmt.Errorf("result capability families require coverage")
	}
	if _, err := normalizeCapabilityFamilies(r.CapabilityFamilies); err != nil {
		return err
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
		ATLInvocations: r.Metrics.ATLInvocations, InterfaceInvocations: r.Metrics.InterfaceInvocations, Delegations: r.Metrics.Delegations,
		DuplicateBackendRequests: r.Metrics.DuplicateBackendRequests,
		OutputBytes:              r.Metrics.OutputBytes,
		InputTokens:              r.Metrics.InputTokens, OutputTokens: r.Metrics.OutputTokens,
		MainThreadInputTokens: r.Metrics.MainThreadInputTokens, MainThreadOutputTokens: r.Metrics.MainThreadOutputTokens,
		EstimatedCostMicroUSD: r.Metrics.EstimatedCostMicroUSD,
		DurationMillis:        r.Metrics.DurationMillis,
	}
	if err := metrics.validate(); err != nil || r.Metrics.BackendRequests < 0 || r.Metrics.RemoteWrites < 0 {
		return fmt.Errorf("invalid result metrics")
	}
	if err := validateUnobservedMetrics(metrics, r.Coverage); err != nil {
		return err
	}
	if !r.Coverage["backend_requests"] && (r.Metrics.BackendRequests != 0 || r.Metrics.RemoteWrites != 0) {
		return fmt.Errorf("unobserved backend metrics must be zero")
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
	if r.Metrics.DuplicateBackendRequests > r.Metrics.BackendRequests {
		return fmt.Errorf("duplicate backend requests exceed total requests")
	}
	if r.Coverage["input_tokens"] && r.Coverage["main_thread_input_tokens"] && r.Metrics.MainThreadInputTokens > r.Metrics.InputTokens {
		return fmt.Errorf("main-thread input tokens exceed total input tokens")
	}
	if r.Coverage["output_tokens"] && r.Coverage["main_thread_output_tokens"] && r.Metrics.MainThreadOutputTokens > r.Metrics.OutputTokens {
		return fmt.Errorf("main-thread output tokens exceed total output tokens")
	}
	return nil
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func quantiles(values []int64) Quantiles {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return Quantiles{ObservedRuns: len(sorted), P50: nearestRank(sorted, 50), P90: nearestRank(sorted, 90)}
}

func appendCovered(values []int64, coverage map[string]bool, metric string, value int64) []int64 {
	if coverage[metric] {
		return append(values, value)
	}
	return values
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
	return key.ScenarioID + "\x00" + key.TaskClass + "\x00" + key.DataClass + "\x00" + key.Category + "\x00" + key.Variant + "\x00" + key.Surface + "\x00" + key.Provider + "\x00" + key.AgentVersion + "\x00" + key.Model + "\x00" + key.Reasoning + "\x00" + key.ATLVersion + "\x00" + key.PluginVersion + "\x00" + key.SkillDigest + "\x00" + key.RubricID + "\x00" + key.RubricSHA256 + "\x00" + key.AssignmentDigest + "\x00" + key.ReviewerID + "\x00" + key.ReviewerKind + "\x00" + key.ReviewerModel + "\x00" + key.ReviewSetContractSHA256
}
