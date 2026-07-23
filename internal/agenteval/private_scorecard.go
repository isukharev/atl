package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateFindingLedgerSchemaVersion    = 1
	PrivateFindingScorecardSchemaVersion = 3
	PrivateFindingLedgerRelativePath     = "reports/finding-ledger.v1.json"
	privateFindingLedgerMaxBytes         = 4 << 20
)

const (
	PrivateFailureModel                 = "model"
	PrivateFailureContractPreflight     = "contract-preflight"
	PrivateFailureTransportVPN          = "transport-vpn"
	PrivateFailureBackendDrift          = "backend-drift"
	PrivateFailureUnsupportedCapability = "unsupported-capability"
	PrivateFailureSafety                = "safety"
	PrivateFailureQualitative           = "qualitative"
	PrivateFailureHarness               = "harness"

	PrivateFindingDecisionFixed       = "fixed"
	PrivateFindingDecisionAccepted    = "accepted"
	PrivateFindingDecisionUnsupported = "unsupported"
	PrivateFindingDecisionDeferred    = "deferred"
	PrivateFindingDecisionInvestigate = "investigate"
)

var ErrPrivateFindingLedgerRejected = errors.New("private finding ledger rejected")

type PrivateFindingLedger struct {
	SchemaVersion int                   `json:"schema_version"`
	Entries       []PrivateFindingEntry `json:"entries"`
}

type PrivateFindingEntry struct {
	FindingID             string                `json:"finding_id"`
	Failure               PrivateFindingRunRef  `json:"failure"`
	FailureClass          string                `json:"failure_class"`
	ProductIssue          int                   `json:"product_issue"`
	PullRequest           int                   `json:"pull_request,omitempty"`
	ChangedContractSHA256 string                `json:"changed_contract_sha256,omitempty"`
	Regression            *PrivateFindingRunRef `json:"regression,omitempty"`
	Decision              string                `json:"decision"`
}

type PrivateFindingRunRef struct {
	PlanID   string `json:"plan_id"`
	Surface  string `json:"surface"`
	Baseline string `json:"baseline"`
}

type PrivateFindingScorecardOptions struct {
	Root           string
	RepositoryRoot string
}

type PrivateFindingScorecard struct {
	SchemaVersion       int                            `json:"schema_version"`
	SourceSHA256        string                         `json:"source_sha256"`
	Reconciled          bool                           `json:"reconciled"`
	Findings            int                            `json:"findings"`
	LinkedIssues        int                            `json:"linked_issues"`
	LinkedPullRequests  int                            `json:"linked_pull_requests"`
	Regressions         int                            `json:"regressions"`
	SamplingAssessments int                            `json:"sampling_assessments"`
	Decisions           PrivateFindingDecisionCounts   `json:"decisions"`
	Groups              []PrivateFindingScorecardGroup `json:"groups"`
}

type PrivateFindingScorecardGroup struct {
	TaskClass    string                        `json:"task_class"`
	FailureClass string                        `json:"failure_class"`
	Findings     int                           `json:"findings"`
	Decisions    PrivateFindingDecisionCounts  `json:"decisions"`
	Failure      PrivateFindingOutcome         `json:"failure"`
	Regression   PrivateFindingOutcome         `json:"regression"`
	Sampling     PrivateFindingSamplingOutcome `json:"sampling"`
}

type PrivateFindingSamplingOutcome struct {
	Assessments int                   `json:"assessments"`
	Primary     PrivateFindingOutcome `json:"primary"`
	Holdout     PrivateFindingOutcome `json:"holdout"`
}

type PrivateFindingDecisionCounts struct {
	Fixed       int `json:"fixed"`
	Accepted    int `json:"accepted"`
	Unsupported int `json:"unsupported"`
	Deferred    int `json:"deferred"`
	Investigate int `json:"investigate"`
}

type PrivateFindingOutcome struct {
	Observed    int                             `json:"observed"`
	Statuses    PrivateFindingStatusCounts      `json:"statuses"`
	Eligibility PrivateFindingEligibilityCounts `json:"eligibility"`
	Evidence    PrivateFindingEvidenceCounts    `json:"evidence"`
	Metrics     PrivateFindingMetrics           `json:"metrics"`
}

type PrivateFindingStatusCounts struct {
	Pass       int `json:"pass"`
	Fail       int `json:"fail"`
	Ineligible int `json:"ineligible"`
}

type PrivateFindingEligibilityCounts struct {
	Supported               int `json:"supported"`
	UnsupportedCapability   int `json:"unsupported_capability"`
	InvalidatedBackendDrift int `json:"invalidated_backend_drift"`
}

type PrivateFindingEvidenceCounts struct {
	Unobserved  int `json:"unobserved"`
	None        int `json:"none"`
	Unavailable int `json:"unavailable"`
	Blocked     int `json:"blocked"`
	Failed      int `json:"failed"`
	Partial     int `json:"partial"`
	Succeeded   int `json:"succeeded"`
}

type PrivateFindingMetrics struct {
	AgentTurns               Quantiles `json:"agent_turns"`
	ToolCalls                Quantiles `json:"tool_calls"`
	BackendRequests          Quantiles `json:"backend_requests"`
	DuplicateBackendRequests Quantiles `json:"duplicate_backend_requests"`
	InputTokens              Quantiles `json:"input_tokens"`
	OutputTokens             Quantiles `json:"output_tokens"`
	EstimatedCostMicroUSD    Quantiles `json:"estimated_cost_microusd"`
	DurationMillis           Quantiles `json:"duration_millis"`
}

type privateFindingSourceLoader func(root, repository, planID string) (PrivateBaselineSource, error)

type privateFindingResolved struct {
	entry           PrivateFindingEntry
	failure         Result
	regression      *Result
	samplingPrimary []Result
	samplingHoldout []Result
	digests         []string
}

// BuildPrivateFindingScorecard validates an owner-private finding ledger and
// immutable completed run references without changing the workspace.
func BuildPrivateFindingScorecard(options PrivateFindingScorecardOptions) (PrivateFindingScorecard, error) {
	return buildPrivateFindingScorecard(options, LoadCompletedPrivateRun)
}

func buildPrivateFindingScorecard(options PrivateFindingScorecardOptions, load privateFindingSourceLoader) (PrivateFindingScorecard, error) {
	root, repository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateFindingScorecard{}, privateFindingError("workspace")
	}
	ledgerPath := filepath.Join(root, filepath.FromSlash(PrivateFindingLedgerRelativePath))
	info, err := safepath.StatWithin(root, ledgerPath)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return PrivateFindingScorecard{}, privateFindingError("ledger_file")
	}
	data, err := safepath.ReadFileWithinLimit(root, ledgerPath, privateFindingLedgerMaxBytes)
	if err != nil {
		return PrivateFindingScorecard{}, privateFindingError("ledger_read")
	}
	ledger, canonical, err := decodePrivateFindingLedger(data)
	if err != nil || !bytes.Equal(data, canonical) {
		return PrivateFindingScorecard{}, privateFindingError("ledger_contract")
	}
	acceptance, acceptanceCanonical, err := loadPrivateFindingAcceptance(root, ledger)
	if err != nil {
		return PrivateFindingScorecard{}, err
	}
	resolved := make([]privateFindingResolved, 0, len(ledger.Entries))
	seenRefs := map[string]struct{}{}
	for _, entry := range ledger.Entries {
		key := privateFindingRefKey(entry.Failure)
		if _, exists := seenRefs[key]; exists {
			return PrivateFindingScorecard{}, privateFindingError("duplicate_failure")
		}
		seenRefs[key] = struct{}{}
		failureSource, loadErr := load(root, repository, entry.Failure.PlanID)
		if loadErr != nil {
			return PrivateFindingScorecard{}, privateFindingError("failure_source")
		}
		failure, failureDigest, resultErr := privateFindingBaselineResult(root, failureSource, entry.Failure)
		if resultErr != nil || (failure.EffectiveEligibility() == EligibilitySupported && failure.Status == "pass") {
			return PrivateFindingScorecard{}, privateFindingError("failure_result")
		}
		if _, allowed := publicCorpusTaskClasses[failure.TaskClass]; !allowed {
			return PrivateFindingScorecard{}, privateFindingError("task_class")
		}
		if !privateFailureClassMatchesResult(entry.FailureClass, failure) {
			return PrivateFindingScorecard{}, privateFindingError("failure_class")
		}
		item := privateFindingResolved{entry: entry, failure: failure, digests: []string{failureSource.PlanSHA256, failureDigest}}
		regressionContractSHA256 := ""
		if entry.Regression != nil {
			if entry.Regression.PlanID == entry.Failure.PlanID {
				return PrivateFindingScorecard{}, privateFindingError("regression_identity")
			}
			regressionSource, loadErr := load(root, repository, entry.Regression.PlanID)
			if loadErr != nil {
				return PrivateFindingScorecard{}, privateFindingError("regression_source")
			}
			regression, regressionDigest, resultErr := privateFindingBaselineResult(root, regressionSource, *entry.Regression)
			if resultErr != nil || !compatiblePrivateResults(failure, regression) {
				return PrivateFindingScorecard{}, privateFindingError("regression_incompatible")
			}
			if entry.ChangedContractSHA256 != "" && entry.ChangedContractSHA256 != regressionSource.ContractSHA256 {
				return PrivateFindingScorecard{}, privateFindingError("change_contract")
			}
			regressionContractSHA256 = regressionSource.ContractSHA256
			item.regression = &regression
			item.digests = append(item.digests, regressionSource.PlanSHA256, regressionDigest)
		}
		if entry.Decision == PrivateFindingDecisionFixed {
			if item.regression == nil || item.regression.EffectiveEligibility() != EligibilitySupported || item.regression.Status != "pass" {
				return PrivateFindingScorecard{}, privateFindingError("fixed_regression")
			}
			if !validSHA256(failureSource.ContractSHA256) || !validSHA256(entry.ChangedContractSHA256) ||
				entry.ChangedContractSHA256 != regressionContractSHA256 ||
				failureSource.ContractSHA256 == entry.ChangedContractSHA256 {
				return PrivateFindingScorecard{}, privateFindingError("fixed_contract")
			}
			acceptanceBinding, exists := acceptance[entry.FindingID]
			if !exists {
				return PrivateFindingScorecard{}, privateFindingError("fixed_acceptance")
			}
			switch acceptanceBinding.AssessmentSource {
			case PrivateFindingAcceptanceSourcePrivateLive:
				assessment, primary, holdout, assessmentErr := loadPrivateSamplingAssessment(
					root, repository, acceptanceBinding.AssessmentSHA256, load,
				)
				if assessmentErr != nil || assessment.Tier != PrivateSamplingTierRegression ||
					assessment.RegressionAccepted == nil || !*assessment.RegressionAccepted ||
					len(primary) != 3 || len(holdout) == 0 {
					return PrivateFindingScorecard{}, privateFindingError("fixed_assessment")
				}
				regressionPresent := false
				for _, binding := range assessment.Primary {
					if binding.ContractSHA256 != entry.ChangedContractSHA256 {
						return PrivateFindingScorecard{}, privateFindingError("fixed_assessment_contract")
					}
					if entry.Regression != nil && binding.Reference == *entry.Regression {
						regressionPresent = true
					}
				}
				if !regressionPresent {
					return PrivateFindingScorecard{}, privateFindingError("fixed_assessment_regression")
				}
				item.samplingPrimary = primary
				item.samplingHoldout = holdout
			case PrivateFindingAcceptanceSourceSyntheticRoot:
				assessment, primary, holdout, assessmentErr := loadPrivateSyntheticSamplingAssessment(
					root, acceptanceBinding.AssessmentSHA256,
				)
				if assessmentErr != nil || assessment.Tier != PrivateSamplingTierRegression ||
					assessment.RegressionAccepted == nil || !*assessment.RegressionAccepted ||
					len(primary) != 3 || len(holdout) == 0 {
					return PrivateFindingScorecard{}, privateFindingError("fixed_assessment")
				}
				for _, result := range primary {
					if item.regression == nil || !compatiblePrivateSyntheticFindingEvidence(*item.regression, result) {
						return PrivateFindingScorecard{}, privateFindingError("fixed_assessment_regression")
					}
				}
				item.samplingPrimary = primary
				item.samplingHoldout = holdout
			default:
				return PrivateFindingScorecard{}, privateFindingError("fixed_acceptance")
			}
			item.digests = append(item.digests, acceptanceBinding.AssessmentSHA256)
		}
		resolved = append(resolved, item)
	}
	return aggregatePrivateFindingScorecard(canonical, acceptanceCanonical, resolved), nil
}

func compatiblePrivateSyntheticFindingEvidence(regression, synthetic Result) bool {
	if regression.DataClass != "private-local" || synthetic.DataClass != "synthetic" ||
		regression.ScenarioID != synthetic.ScenarioID ||
		regression.TaskClass != synthetic.TaskClass ||
		regression.EffectiveCategory() != synthetic.EffectiveCategory() ||
		regression.Variant != synthetic.Variant ||
		regression.EffectiveSurface() != synthetic.EffectiveSurface() ||
		regression.Runtime.Provider != synthetic.Runtime.Provider ||
		regression.Runtime.AgentVersion != synthetic.Runtime.AgentVersion ||
		regression.Runtime.Model != synthetic.Runtime.Model ||
		regression.Runtime.Reasoning != synthetic.Runtime.Reasoning ||
		regression.Runtime.ATLVersion != synthetic.Runtime.ATLVersion ||
		regression.Runtime.PluginVersion != synthetic.Runtime.PluginVersion ||
		regression.Runtime.SkillDigest != synthetic.Runtime.SkillDigest ||
		regression.Runtime.SkillActivation != synthetic.Runtime.SkillActivation {
		return false
	}
	return regression.Runtime.PromptContractSHA256 == "" ||
		regression.Runtime.PromptContractSHA256 == synthetic.Runtime.PromptContractSHA256
}

func decodePrivateFindingLedger(data []byte) (PrivateFindingLedger, []byte, error) {
	var ledger PrivateFindingLedger
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ledger); err != nil {
		return ledger, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ledger, nil, fmt.Errorf("trailing data")
	}
	if err := ledger.validate(); err != nil {
		return ledger, nil, err
	}
	canonical, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return ledger, nil, err
	}
	canonical = append(canonical, '\n')
	return ledger, canonical, nil
}

func (l PrivateFindingLedger) validate() error {
	if l.SchemaVersion != PrivateFindingLedgerSchemaVersion || len(l.Entries) == 0 || len(l.Entries) > 4096 {
		return fmt.Errorf("invalid ledger envelope")
	}
	previous := ""
	seen := map[string]struct{}{}
	for _, entry := range l.Entries {
		if !pathComponentIDRE.MatchString(entry.FindingID) || entry.FindingID == "." || entry.FindingID == ".." ||
			previous >= entry.FindingID {
			return fmt.Errorf("invalid finding order")
		}
		if _, exists := seen[entry.FindingID]; exists {
			return fmt.Errorf("duplicate finding")
		}
		seen[entry.FindingID] = struct{}{}
		previous = entry.FindingID
		if !validPrivateFindingRef(entry.Failure) || !validPrivateFailureClass(entry.FailureClass) || entry.ProductIssue <= 0 ||
			!validPrivateFindingDecision(entry.Decision) {
			return fmt.Errorf("invalid finding")
		}
		if (entry.PullRequest == 0) != (entry.ChangedContractSHA256 == "") || entry.PullRequest < 0 ||
			(entry.ChangedContractSHA256 != "" && !validSHA256(entry.ChangedContractSHA256)) {
			return fmt.Errorf("invalid change binding")
		}
		if entry.Regression != nil && !validPrivateFindingRef(*entry.Regression) {
			return fmt.Errorf("invalid regression")
		}
		if entry.Decision == PrivateFindingDecisionFixed && (entry.PullRequest == 0 || entry.Regression == nil) {
			return fmt.Errorf("incomplete fixed finding")
		}
	}
	return nil
}

func validPrivateFindingRef(ref PrivateFindingRunRef) bool {
	if !privatePlanIDRE.MatchString(ref.PlanID) || !validPrivateFindingSurface(ref.Surface) ||
		ref.Baseline == "current" || !privateWorkspaceAliasRE.MatchString(ref.Baseline) {
		return false
	}
	return true
}

func validPrivateFindingSurface(surface string) bool {
	return surface == SurfaceCLISkill || surface == SurfaceATLMCP || surface == SurfaceExternalMCP
}

func validPrivateFailureClass(value string) bool {
	switch value {
	case PrivateFailureModel, PrivateFailureContractPreflight, PrivateFailureTransportVPN, PrivateFailureBackendDrift,
		PrivateFailureUnsupportedCapability, PrivateFailureSafety, PrivateFailureQualitative, PrivateFailureHarness:
		return true
	default:
		return false
	}
}

func validPrivateFindingDecision(value string) bool {
	switch value {
	case PrivateFindingDecisionFixed, PrivateFindingDecisionAccepted, PrivateFindingDecisionUnsupported,
		PrivateFindingDecisionDeferred, PrivateFindingDecisionInvestigate:
		return true
	default:
		return false
	}
}

func privateFindingBaselineResult(root string, source PrivateBaselineSource, ref PrivateFindingRunRef) (Result, string, error) {
	if !source.Completed || !source.Immutable || source.Kind == PrivateRunSetKindActivationStudy || source.PlanID != ref.PlanID ||
		!validSHA256(source.PlanSHA256) || !validSHA256(source.ContractSHA256) {
		return Result{}, "", privateFindingError("mutable_source")
	}
	manifest, baselineRoot, err := loadPrivateBaseline(root, source.ContractSHA256, ref.Baseline)
	if err != nil || manifest.PlanSHA256 != source.PlanSHA256 {
		return Result{}, "", privateFindingError("baseline")
	}
	var selected *privateBaselineSurface
	for index := range manifest.Surfaces {
		candidate := &manifest.Surfaces[index]
		if candidate.Surface != ref.Surface {
			continue
		}
		if selected != nil {
			return Result{}, "", privateFindingError("ambiguous_surface")
		}
		selected = candidate
	}
	if selected == nil {
		return Result{}, "", privateFindingError("surface_missing")
	}
	expectedResultPath := filepath.ToSlash(filepath.Join("surfaces", ref.Surface, "result.json"))
	expectedReviewedPath := filepath.ToSlash(filepath.Join("surfaces", ref.Surface, "reviewed-result.json"))
	reviewedInfo, reviewedErr := safepath.StatWithin(root, filepath.Join(baselineRoot, filepath.FromSlash(expectedReviewedPath)))
	reviewedExists := reviewedErr == nil && reviewedInfo.Mode().IsRegular()
	if reviewedErr != nil && !os.IsNotExist(reviewedErr) || reviewedErr == nil && !reviewedInfo.Mode().IsRegular() {
		return Result{}, "", privateFindingError("baseline_reviewed_result")
	}
	expectedSelectedPath := expectedResultPath
	if reviewedExists {
		expectedSelectedPath = expectedReviewedPath
	}
	if selected.ResultPath != expectedSelectedPath {
		return Result{}, "", privateFindingError("baseline_result_path")
	}
	resultData, err := safepath.ReadFileWithinLimit(root, filepath.Join(baselineRoot, filepath.FromSlash(selected.ResultPath)), maxContractBytes)
	if err != nil || sha256HexBytes(resultData) != selected.ResultSHA256 {
		return Result{}, "", privateFindingError("baseline_result")
	}
	result, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil || result.DataClass != "private-local" || result.EffectiveSurface() != ref.Surface {
		return Result{}, "", privateFindingError("result_contract")
	}
	return result, sha256HexBytes([]byte(manifest.TreeSHA256 + "\x00" + selected.ResultSHA256)), nil
}

func privateFailureClassMatchesResult(class string, result Result) bool {
	switch class {
	case PrivateFailureUnsupportedCapability:
		return result.EffectiveEligibility() == EligibilityUnsupportedCapability
	case PrivateFailureBackendDrift:
		return result.EffectiveEligibility() == EligibilityInvalidatedDrift
	default:
		return result.EffectiveEligibility() == EligibilitySupported && result.Status == "fail"
	}
}

func aggregatePrivateFindingScorecard(ledger, acceptance []byte, resolved []privateFindingResolved) PrivateFindingScorecard {
	type groupKey struct{ taskClass, failureClass string }
	type groupValues struct {
		entries         []privateFindingResolved
		failures        []Result
		regressions     []Result
		samplingPrimary []Result
		samplingHoldout []Result
		samplingCount   int
	}
	groups := map[groupKey]*groupValues{}
	hash := sha256.New()
	_, _ = hash.Write([]byte("atl-private-finding-scorecard-v3\x00"))
	_, _ = hash.Write(ledger)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(acceptance)
	report := PrivateFindingScorecard{SchemaVersion: PrivateFindingScorecardSchemaVersion, Reconciled: true, Findings: len(resolved)}
	issues := map[int]struct{}{}
	pullRequests := map[int]struct{}{}
	for _, item := range resolved {
		for _, digest := range item.digests {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(digest))
		}
		issues[item.entry.ProductIssue] = struct{}{}
		if item.entry.PullRequest > 0 {
			pullRequests[item.entry.PullRequest] = struct{}{}
		}
		if item.regression != nil {
			report.Regressions++
		}
		if len(item.samplingPrimary) > 0 {
			report.SamplingAssessments++
		}
		addPrivateFindingDecision(&report.Decisions, item.entry.Decision)
		key := groupKey{item.failure.TaskClass, item.entry.FailureClass}
		group := groups[key]
		if group == nil {
			group = &groupValues{}
			groups[key] = group
		}
		group.entries = append(group.entries, item)
		group.failures = append(group.failures, item.failure)
		if item.regression != nil {
			group.regressions = append(group.regressions, *item.regression)
		}
		if len(item.samplingPrimary) > 0 {
			group.samplingCount++
			group.samplingPrimary = append(group.samplingPrimary, item.samplingPrimary...)
			group.samplingHoldout = append(group.samplingHoldout, item.samplingHoldout...)
		}
	}
	report.LinkedIssues = len(issues)
	report.LinkedPullRequests = len(pullRequests)
	report.SourceSHA256 = hex.EncodeToString(hash.Sum(nil))
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].taskClass != keys[j].taskClass {
			return keys[i].taskClass < keys[j].taskClass
		}
		return keys[i].failureClass < keys[j].failureClass
	})
	for _, key := range keys {
		values := groups[key]
		group := PrivateFindingScorecardGroup{TaskClass: key.taskClass, FailureClass: key.failureClass, Findings: len(values.entries),
			Failure: privateFindingOutcome(values.failures), Regression: privateFindingOutcome(values.regressions),
			Sampling: PrivateFindingSamplingOutcome{Assessments: values.samplingCount,
				Primary: privateFindingOutcome(values.samplingPrimary), Holdout: privateFindingOutcome(values.samplingHoldout)}}
		for _, item := range values.entries {
			addPrivateFindingDecision(&group.Decisions, item.entry.Decision)
		}
		report.Groups = append(report.Groups, group)
	}
	return report
}

func addPrivateFindingDecision(counts *PrivateFindingDecisionCounts, decision string) {
	switch decision {
	case PrivateFindingDecisionFixed:
		counts.Fixed++
	case PrivateFindingDecisionAccepted:
		counts.Accepted++
	case PrivateFindingDecisionUnsupported:
		counts.Unsupported++
	case PrivateFindingDecisionDeferred:
		counts.Deferred++
	case PrivateFindingDecisionInvestigate:
		counts.Investigate++
	}
}

func privateFindingOutcome(results []Result) PrivateFindingOutcome {
	outcome := PrivateFindingOutcome{Observed: len(results)}
	var agentTurns, toolCalls, backendRequests, duplicates, inputTokens, outputTokens, costs, durations []int64
	for _, result := range results {
		switch result.Status {
		case "pass":
			outcome.Statuses.Pass++
		case "fail":
			outcome.Statuses.Fail++
		case "ineligible":
			outcome.Statuses.Ineligible++
		}
		switch result.EffectiveEligibility() {
		case EligibilitySupported:
			outcome.Eligibility.Supported++
		case EligibilityUnsupportedCapability:
			outcome.Eligibility.UnsupportedCapability++
		case EligibilityInvalidatedDrift:
			outcome.Eligibility.InvalidatedBackendDrift++
		}
		addPrivateFindingEvidence(&outcome.Evidence, result.EvidenceAttempt)
		agentTurns = appendCovered(agentTurns, result.Coverage, "agent_turns", int64(result.Metrics.AgentTurns))
		toolCalls = appendCovered(toolCalls, result.Coverage, "tool_calls", int64(result.Metrics.ToolCalls))
		backendRequests = appendCovered(backendRequests, result.Coverage, "backend_requests", int64(result.Metrics.BackendRequests))
		duplicates = appendCovered(duplicates, result.Coverage, "duplicate_backend_requests", int64(result.Metrics.DuplicateBackendRequests))
		inputTokens = appendCovered(inputTokens, result.Coverage, "input_tokens", result.Metrics.InputTokens)
		outputTokens = appendCovered(outputTokens, result.Coverage, "output_tokens", result.Metrics.OutputTokens)
		costs = appendCovered(costs, result.Coverage, "estimated_cost_microusd", result.Metrics.EstimatedCostMicroUSD)
		durations = appendCovered(durations, result.Coverage, "duration_millis", result.Metrics.DurationMillis)
	}
	outcome.Metrics = PrivateFindingMetrics{AgentTurns: quantiles(agentTurns), ToolCalls: quantiles(toolCalls),
		BackendRequests: quantiles(backendRequests), DuplicateBackendRequests: quantiles(duplicates), InputTokens: quantiles(inputTokens),
		OutputTokens: quantiles(outputTokens), EstimatedCostMicroUSD: quantiles(costs), DurationMillis: quantiles(durations)}
	return outcome
}

func addPrivateFindingEvidence(counts *PrivateFindingEvidenceCounts, evidence EvidenceAttemptTelemetry) {
	if !evidence.Coverage {
		counts.Unobserved++
		return
	}
	switch evidence.State {
	case EvidenceAttemptStateNone:
		counts.None++
	case EvidenceAttemptStateUnavailable:
		counts.Unavailable++
	case EvidenceAttemptStateBlocked:
		counts.Blocked++
	case EvidenceAttemptStateFailed:
		counts.Failed++
	case EvidenceAttemptStatePartial:
		counts.Partial++
	case EvidenceAttemptStateSucceeded:
		counts.Succeeded++
	}
}

func privateFindingRefKey(ref PrivateFindingRunRef) string {
	return ref.PlanID + "\x00" + ref.Surface
}

func privateFindingError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateFindingLedgerRejected, code)
}
