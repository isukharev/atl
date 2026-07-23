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
	"reflect"
	"sort"
	"strings"

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
	LedgerSchemaVersion int                            `json:"-"`
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
	entry           privateFindingLedgerEntry
	failure         Result
	regressions     []Result
	samplingPrimary []Result
	samplingHoldout []Result
	digests         []string
	synthetic       []privateSyntheticFindingSnapshot
}

type privateSyntheticFindingSnapshot struct {
	digest     string
	assessment privateSyntheticSamplingAssessment
	primary    []Result
	holdout    []Result
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
	ledger, err := loadPrivateFindingLedger(root)
	if err != nil {
		return PrivateFindingScorecard{}, err
	}
	acceptance, acceptanceCanonical, err := loadPrivateFindingAcceptanceForEntries(root, ledger.Entries)
	if err != nil {
		return PrivateFindingScorecard{}, err
	}
	resolved := make([]privateFindingResolved, 0, len(ledger.Entries))
	seenRefs := map[string]struct{}{}
	enforceAllEvidenceUnique := ledger.Entries[0].SchemaVersion == PrivateFindingLedgerV2SchemaVersion
	for _, entry := range ledger.Entries {
		key := privateFindingEvidenceRefKey(entry.Failure)
		if _, exists := seenRefs[key]; exists {
			return PrivateFindingScorecard{}, privateFindingError("duplicate_failure")
		}
		seenRefs[key] = struct{}{}
		var item privateFindingResolved
		switch entry.Failure.Source {
		case PrivateFindingAcceptanceSourcePrivateLive:
			item, err = resolvePrivateLiveFinding(root, repository, entry, acceptance, load)
		case PrivateFindingAcceptanceSourceSyntheticRoot:
			item, err = resolvePrivateSyntheticFinding(root, entry, acceptance)
		default:
			err = privateFindingError("failure_source")
		}
		if err != nil {
			return PrivateFindingScorecard{}, err
		}
		if enforceAllEvidenceUnique && entry.Regression != nil {
			regressionKey := privateFindingEvidenceRefKey(*entry.Regression)
			if _, exists := seenRefs[regressionKey]; exists {
				return PrivateFindingScorecard{}, privateFindingError("duplicate_regression")
			}
			seenRefs[regressionKey] = struct{}{}
		}
		resolved = append(resolved, item)
	}
	finalLedger, err := loadPrivateFindingLedger(root)
	if err != nil || !bytes.Equal(ledger.Canonical, finalLedger.Canonical) {
		return PrivateFindingScorecard{}, privateFindingError("ledger_drift")
	}
	_, finalAcceptance, err := loadPrivateFindingAcceptanceForEntries(root, finalLedger.Entries)
	if err != nil || !bytes.Equal(acceptanceCanonical, finalAcceptance) {
		return PrivateFindingScorecard{}, privateFindingError("acceptance_drift")
	}
	for _, item := range resolved {
		for _, snapshot := range item.synthetic {
			if !revalidatePrivateSyntheticFindingSnapshot(root, snapshot) {
				return PrivateFindingScorecard{}, privateFindingError("synthetic_evidence_drift")
			}
		}
	}
	return aggregatePrivateFindingScorecard(
		ledger.Canonical, acceptanceCanonical, resolved, ledger.Entries[0].SchemaVersion,
	), nil
}

func resolvePrivateLiveFinding(root, repository string, entry privateFindingLedgerEntry,
	acceptance map[string]privateFindingAcceptanceBinding, load privateFindingSourceLoader,
) (privateFindingResolved, error) {
	failureRef := privateFindingRunRef(entry.Failure)
	failureSource, err := load(root, repository, failureRef.PlanID)
	if err != nil {
		return privateFindingResolved{}, privateFindingError("failure_source")
	}
	failure, failureDigest, err := privateFindingBaselineResult(root, failureSource, failureRef)
	if err != nil || failure.EffectiveEligibility() == EligibilitySupported && failure.Status == "pass" {
		return privateFindingResolved{}, privateFindingError("failure_result")
	}
	if _, allowed := publicCorpusTaskClasses[failure.TaskClass]; !allowed {
		return privateFindingResolved{}, privateFindingError("task_class")
	}
	if !privateFailureClassMatchesResult(entry.FailureClass, failure) {
		return privateFindingResolved{}, privateFindingError("failure_class")
	}
	item := privateFindingResolved{
		entry: entry, failure: failure, digests: []string{failureSource.PlanSHA256, failureDigest},
	}
	changedContract := entry.LegacyChangedContractSHA256
	if entry.SchemaVersion == PrivateFindingLedgerV2SchemaVersion && len(entry.ChangedContracts) == 1 {
		transition := entry.ChangedContracts[0]
		if transition.Kind != PrivateFindingContractPlan ||
			transition.BeforeSHA256 != failureSource.ContractSHA256 {
			return privateFindingResolved{}, privateFindingError("change_contract")
		}
		changedContract = transition.AfterSHA256
	}
	regressionContract := ""
	if entry.Regression != nil {
		regressionRef := privateFindingRunRef(*entry.Regression)
		if regressionRef.PlanID == failureRef.PlanID {
			return privateFindingResolved{}, privateFindingError("regression_identity")
		}
		regressionSource, loadErr := load(root, repository, regressionRef.PlanID)
		if loadErr != nil {
			return privateFindingResolved{}, privateFindingError("regression_source")
		}
		regression, regressionDigest, resultErr := privateFindingBaselineResult(root, regressionSource, regressionRef)
		if resultErr != nil || !compatiblePrivateResults(failure, regression) {
			return privateFindingResolved{}, privateFindingError("regression_incompatible")
		}
		if changedContract != "" && changedContract != regressionSource.ContractSHA256 {
			return privateFindingResolved{}, privateFindingError("change_contract")
		}
		regressionContract = regressionSource.ContractSHA256
		item.regressions = []Result{regression}
		item.digests = append(item.digests, regressionSource.PlanSHA256, regressionDigest)
	}
	if entry.Decision != PrivateFindingDecisionFixed {
		return item, nil
	}
	if len(item.regressions) != 1 || item.regressions[0].EffectiveEligibility() != EligibilitySupported ||
		item.regressions[0].Status != "pass" {
		return privateFindingResolved{}, privateFindingError("fixed_regression")
	}
	if !validSHA256(failureSource.ContractSHA256) || !validSHA256(changedContract) ||
		changedContract != regressionContract || failureSource.ContractSHA256 == changedContract {
		return privateFindingResolved{}, privateFindingError("fixed_contract")
	}
	binding, exists := acceptance[entry.FindingID]
	if !exists {
		return privateFindingResolved{}, privateFindingError("fixed_acceptance")
	}
	switch binding.AssessmentSource {
	case PrivateFindingAcceptanceSourcePrivateLive:
		assessment, primary, holdout, assessmentErr := loadPrivateSamplingAssessment(
			root, repository, binding.AssessmentSHA256, load,
		)
		if assessmentErr != nil || assessment.Tier != PrivateSamplingTierRegression ||
			assessment.RegressionAccepted == nil || !*assessment.RegressionAccepted ||
			len(primary) != 3 || len(holdout) == 0 {
			return privateFindingResolved{}, privateFindingError("fixed_assessment")
		}
		regressionPresent := false
		for _, candidate := range assessment.Primary {
			if candidate.ContractSHA256 != changedContract {
				return privateFindingResolved{}, privateFindingError("fixed_assessment_contract")
			}
			if entry.Regression != nil && candidate.Reference == privateFindingRunRef(*entry.Regression) {
				regressionPresent = true
			}
		}
		if !regressionPresent {
			return privateFindingResolved{}, privateFindingError("fixed_assessment_regression")
		}
		item.samplingPrimary, item.samplingHoldout = primary, holdout
	case PrivateFindingAcceptanceSourceSyntheticRoot:
		assessment, primary, holdout, assessmentErr := loadPrivateSyntheticSamplingAssessment(
			root, binding.AssessmentSHA256,
		)
		if assessmentErr != nil || assessment.Tier != PrivateSamplingTierRegression ||
			assessment.RegressionAccepted == nil || !*assessment.RegressionAccepted ||
			len(primary) != 3 || len(holdout) == 0 {
			return privateFindingResolved{}, privateFindingError("fixed_assessment")
		}
		for _, result := range primary {
			if len(item.regressions) != 1 || !compatiblePrivateSyntheticFindingEvidence(
				item.regressions[0], result, binding.PromptContractSHA256,
			) {
				return privateFindingResolved{}, privateFindingError("fixed_assessment_regression")
			}
		}
		item.samplingPrimary, item.samplingHoldout = primary, holdout
		item.synthetic = append(item.synthetic, privateSyntheticFindingSnapshot{
			digest: binding.AssessmentSHA256, assessment: assessment, primary: primary, holdout: holdout,
		})
	default:
		return privateFindingResolved{}, privateFindingError("fixed_acceptance")
	}
	item.digests = append(item.digests, binding.AssessmentSHA256)
	return item, nil
}

func resolvePrivateSyntheticFinding(root string, entry privateFindingLedgerEntry,
	acceptance map[string]privateFindingAcceptanceBinding,
) (privateFindingResolved, error) {
	failureAssessment, failurePrimary, failureHoldout, err := loadPrivateSyntheticSamplingAssessment(
		root, entry.Failure.AssessmentSHA256,
	)
	if err != nil || failureAssessment.Tier != PrivateSamplingTierCalibration ||
		len(failurePrimary) != 1 || len(failureHoldout) != 0 {
		return privateFindingResolved{}, privateFindingError("failure_assessment")
	}
	failure := failurePrimary[0]
	if failure.DataClass != "synthetic" || failure.EffectiveEligibility() != EligibilitySupported ||
		failure.Status != "fail" {
		return privateFindingResolved{}, privateFindingError("failure_result")
	}
	if _, allowed := publicCorpusTaskClasses[failure.TaskClass]; !allowed {
		return privateFindingResolved{}, privateFindingError("task_class")
	}
	if !privateFailureClassMatchesResult(entry.FailureClass, failure) {
		return privateFindingResolved{}, privateFindingError("failure_class")
	}
	item := privateFindingResolved{
		entry: entry, failure: failure, digests: []string{entry.Failure.AssessmentSHA256},
		synthetic: []privateSyntheticFindingSnapshot{{
			digest: entry.Failure.AssessmentSHA256, assessment: failureAssessment,
			primary: failurePrimary, holdout: failureHoldout,
		}},
	}
	if entry.Regression == nil {
		if entry.Decision == PrivateFindingDecisionFixed {
			return privateFindingResolved{}, privateFindingError("fixed_regression")
		}
		return item, nil
	}
	regressionAssessment, primary, holdout, err := loadPrivateSyntheticSamplingAssessment(
		root, entry.Regression.AssessmentSHA256,
	)
	if err != nil || regressionAssessment.Tier != PrivateSamplingTierRegression ||
		regressionAssessment.RegressionAccepted == nil || !*regressionAssessment.RegressionAccepted ||
		len(primary) != 3 || len(holdout) == 0 {
		return privateFindingResolved{}, privateFindingError("regression_assessment")
	}
	if !disjointPrivateSyntheticAssessments(failureAssessment, regressionAssessment) ||
		len(primary) != regressionAssessment.Primary.Observations {
		return privateFindingResolved{}, privateFindingError("regression_incompatible")
	}
	matchedRegression, matched := matchPrivateSyntheticFindingRegression(
		failureAssessment.Primary.Cohort, regressionAssessment, primary, holdout, entry.ChangedContracts,
	)
	if !matched {
		return privateFindingResolved{}, privateFindingError("regression_incompatible")
	}
	item.regressions = matchedRegression
	item.samplingPrimary, item.samplingHoldout = primary, holdout
	item.digests = append(item.digests, entry.Regression.AssessmentSHA256)
	item.synthetic = append(item.synthetic, privateSyntheticFindingSnapshot{
		digest: entry.Regression.AssessmentSHA256, assessment: regressionAssessment,
		primary: primary, holdout: holdout,
	})
	if entry.Decision != PrivateFindingDecisionFixed {
		return item, nil
	}
	binding, exists := acceptance[entry.FindingID]
	if !exists || binding.AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot ||
		binding.AssessmentSHA256 != entry.Regression.AssessmentSHA256 ||
		binding.PromptContractSHA256 != regressionAssessment.Primary.Cohort.Runtime.PromptContractSHA256 {
		return privateFindingResolved{}, privateFindingError("fixed_acceptance")
	}
	item.digests = append(item.digests, binding.AssessmentSHA256)
	return item, nil
}

func matchPrivateSyntheticFindingRegression(
	failure privateSyntheticSamplingCohort,
	assessment privateSyntheticSamplingAssessment,
	primary, holdout []Result,
	transitions []PrivateFindingContractTransition,
) ([]Result, bool) {
	type candidate struct {
		cohort  privateSyntheticSamplingCohort
		results []Result
	}
	candidates := []candidate{{cohort: assessment.Primary.Cohort, results: primary}}
	offset := 0
	for _, binding := range assessment.Holdout {
		if binding.Observations <= 0 || offset+binding.Observations > len(holdout) {
			return nil, false
		}
		candidates = append(candidates, candidate{
			cohort:  binding.Cohort,
			results: holdout[offset : offset+binding.Observations],
		})
		offset += binding.Observations
	}
	if offset != len(holdout) {
		return nil, false
	}
	var matched []Result
	for _, candidate := range candidates {
		if !compatiblePrivateSyntheticFindingTransition(failure, candidate.cohort, transitions) {
			continue
		}
		if matched != nil || len(candidate.results) == 0 {
			return nil, false
		}
		matched = candidate.results
	}
	return matched, matched != nil
}

func compatiblePrivateSyntheticFindingTransition(
	failure, regression privateSyntheticSamplingCohort,
	transitions []PrivateFindingContractTransition,
) bool {
	failureSkillDigest, failureSkillOK := privateFindingSkillDigest(failure.Runtime.SkillDigest)
	regressionSkillDigest, regressionSkillOK := privateFindingSkillDigest(regression.Runtime.SkillDigest)
	if failure.ScenarioID != regression.ScenarioID ||
		failure.TaskClass != regression.TaskClass ||
		failure.DataClass != regression.DataClass ||
		failure.Category != regression.Category ||
		failure.Surface != regression.Surface ||
		failure.Runtime.Provider != regression.Runtime.Provider ||
		failure.Runtime.AgentVersion != regression.Runtime.AgentVersion ||
		failure.Runtime.Model != regression.Runtime.Model ||
		failure.Runtime.Reasoning != regression.Runtime.Reasoning ||
		failure.Runtime.PluginVersion != regression.Runtime.PluginVersion ||
		failure.Runtime.SkillActivation != regression.Runtime.SkillActivation ||
		failure.AgentExecutableSHA256 != regression.AgentExecutableSHA256 ||
		!failureSkillOK || !regressionSkillOK {
		return false
	}
	observed := map[string]PrivateFindingContractTransition{}
	add := func(kind, before, after string) {
		if before != after {
			observed[kind] = PrivateFindingContractTransition{
				Kind: kind, BeforeSHA256: before, AfterSHA256: after,
			}
		}
	}
	add(PrivateFindingContractPrompt, failure.Runtime.PromptContractSHA256, regression.Runtime.PromptContractSHA256)
	add(PrivateFindingContractTask, failure.TaskContractSHA256, regression.TaskContractSHA256)
	add(PrivateFindingContractExecution, failure.ExecutionContractSHA256, regression.ExecutionContractSHA256)
	add(PrivateFindingContractATLBinary, failure.ATLExecutableSHA256, regression.ATLExecutableSHA256)
	add(PrivateFindingContractRunner, failure.WrapperExecutableSHA256, regression.WrapperExecutableSHA256)
	add(PrivateFindingContractSkill, failureSkillDigest, regressionSkillDigest)
	if len(observed) > 0 {
		if _, exists := observed[PrivateFindingContractExecution]; !exists {
			return false
		}
	}
	if failure.Variant != regression.Variant {
		if _, taskChanged := observed[PrivateFindingContractTask]; !taskChanged {
			return false
		}
		if _, executionChanged := observed[PrivateFindingContractExecution]; !executionChanged {
			return false
		}
	}
	if failure.Runtime.ATLVersion != regression.Runtime.ATLVersion {
		if _, exists := observed[PrivateFindingContractATLBinary]; !exists {
			return false
		}
	}
	if len(observed) != len(transitions) {
		return false
	}
	for _, transition := range transitions {
		if observed[transition.Kind] != transition {
			return false
		}
	}
	return true
}

func privateFindingSkillDigest(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	value = strings.TrimPrefix(value, "sha256:")
	return value, validSHA256(value)
}

func disjointPrivateSyntheticAssessments(
	failure, regression privateSyntheticSamplingAssessment,
) bool {
	seenRoots := map[string]struct{}{}
	seenSources := map[string]struct{}{}
	add := func(binding privateSyntheticSamplingBinding) {
		seenRoots[binding.Reference.Root] = struct{}{}
		seenSources[binding.Reference.SourceSHA256] = struct{}{}
	}
	add(failure.Primary)
	for _, binding := range failure.Holdout {
		add(binding)
	}
	for _, binding := range append(
		[]privateSyntheticSamplingBinding{regression.Primary}, regression.Holdout...,
	) {
		if _, exists := seenRoots[binding.Reference.Root]; exists {
			return false
		}
		if _, exists := seenSources[binding.Reference.SourceSHA256]; exists {
			return false
		}
	}
	return true
}

func revalidatePrivateSyntheticFindingSnapshot(root string, expected privateSyntheticFindingSnapshot) bool {
	assessment, primary, holdout, err := loadPrivateSyntheticSamplingAssessment(root, expected.digest)
	return err == nil && reflect.DeepEqual(assessment, expected.assessment) &&
		reflect.DeepEqual(primary, expected.primary) && reflect.DeepEqual(holdout, expected.holdout)
}

func compatiblePrivateSyntheticFindingEvidence(regression, synthetic Result, promptContractSHA256 string) bool {
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
		regression.Runtime.SkillActivation != synthetic.Runtime.SkillActivation ||
		!validSHA256(promptContractSHA256) ||
		promptContractSHA256 != synthetic.Runtime.PromptContractSHA256 {
		return false
	}
	return true
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

func aggregatePrivateFindingScorecard(
	ledger, acceptance []byte,
	resolved []privateFindingResolved,
	ledgerSchemaVersion int,
) PrivateFindingScorecard {
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
	report := PrivateFindingScorecard{
		SchemaVersion: PrivateFindingScorecardSchemaVersion, Reconciled: true,
		Findings: len(resolved), LedgerSchemaVersion: ledgerSchemaVersion,
	}
	issues := map[int]struct{}{}
	pullRequests := map[int]struct{}{}
	for _, item := range resolved {
		for _, digest := range item.digests {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(digest))
		}
		for _, issue := range item.entry.ProductIssues {
			issues[issue] = struct{}{}
		}
		for _, pullRequest := range item.entry.PullRequests {
			pullRequests[pullRequest] = struct{}{}
		}
		if len(item.regressions) > 0 {
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
		group.regressions = append(group.regressions, item.regressions...)
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
