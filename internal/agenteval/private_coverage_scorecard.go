package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateCoverageIndexSchemaVersion     = 1
	PrivateCoverageScorecardSchemaVersion = 1
	PrivateCoverageIndexRelativePath      = "reports/sampling-coverage.v1.json"
)

var ErrPrivateCoverageIndexRejected = errors.New("private coverage index rejected")

type PrivateCoverageIndex struct {
	SchemaVersion int                         `json:"schema_version"`
	Entries       []PrivateCoverageIndexEntry `json:"entries"`
}

type PrivateCoverageIndexEntry struct {
	AssessmentSHA256 string `json:"assessment_sha256"`
}

type PrivateCoverageScorecardOptions struct {
	Root           string
	RepositoryRoot string
}

type PrivateCoverageScorecard struct {
	SchemaVersion       int                             `json:"schema_version"`
	SourceSHA256        string                          `json:"source_sha256"`
	Reconciled          bool                            `json:"reconciled"`
	Assessments         int                             `json:"assessments"`
	PrimaryObservations int                             `json:"primary_observations"`
	HoldoutObservations int                             `json:"holdout_observations"`
	Groups              []PrivateCoverageScorecardGroup `json:"groups"`
}

type PrivateCoverageScorecardGroup struct {
	TaskClass          string                 `json:"task_class"`
	Category           string                 `json:"category"`
	Surface            string                 `json:"surface"`
	Provider           string                 `json:"provider"`
	Model              string                 `json:"model"`
	Reasoning          string                 `json:"reasoning"`
	CapabilityFamilies []string               `json:"capability_families"`
	Assessments        int                    `json:"assessments"`
	Primary            PrivateCoverageOutcome `json:"primary"`
	Holdout            PrivateCoverageOutcome `json:"holdout"`
}

type PrivateCoverageOutcome struct {
	Observed           int                             `json:"observed"`
	Statuses           PrivateFindingStatusCounts      `json:"statuses"`
	Eligibility        PrivateFindingEligibilityCounts `json:"eligibility"`
	BackendObservation PrivateCoverageBackendCounts    `json:"backend_observation"`
	SafetyAssurance    PrivateCoverageSafetyCounts     `json:"safety_assurance"`
	Metrics            PrivateCoverageMetrics          `json:"metrics"`
}

type PrivateCoverageBackendCounts struct {
	Unobserved   int `json:"unobserved"`
	ObservedHTTP int `json:"observed_http"`
	OpaqueMCP    int `json:"opaque_mcp"`
}

type PrivateCoverageSafetyCounts struct {
	Unobserved         int `json:"unobserved"`
	ObservedHTTPPolicy int `json:"observed_http_policy"`
	ReviewedROMCP      int `json:"reviewed_ro_mcp_interface"`
}

type PrivateCoverageMetrics struct {
	AgentTurns               Quantiles `json:"agent_turns"`
	ToolCalls                Quantiles `json:"tool_calls"`
	ATLInvocations           Quantiles `json:"atl_invocations"`
	InterfaceInvocations     Quantiles `json:"interface_invocations"`
	CapabilityInvocations    Quantiles `json:"capability_invocations"`
	BackendRequests          Quantiles `json:"backend_requests"`
	DuplicateBackendRequests Quantiles `json:"duplicate_backend_requests"`
	RemoteWrites             Quantiles `json:"remote_writes"`
	InputTokens              Quantiles `json:"input_tokens"`
	OutputTokens             Quantiles `json:"output_tokens"`
	EstimatedCostMicroUSD    Quantiles `json:"estimated_cost_microusd"`
	DurationMillis           Quantiles `json:"duration_millis"`
}

type privateCoverageResolved struct {
	digest     string
	assessment privateSyntheticSamplingAssessment
	primary    []Result
	holdout    []Result
	key        privateCoverageGroupKey
}

type privateCoverageGroupKey struct {
	taskClass, category, surface, provider, model, reasoning, capabilityFamilies string
}

// BuildPrivateCoverageScorecard validates the exact active sampling index and
// its immutable accepted synthetic evidence without changing the workspace.
func BuildPrivateCoverageScorecard(options PrivateCoverageScorecardOptions) (PrivateCoverageScorecard, error) {
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateCoverageScorecard{}, privateCoverageError("workspace")
	}
	index, canonical, err := loadPrivateCoverageIndex(root)
	if err != nil {
		return PrivateCoverageScorecard{}, err
	}
	resolved := make([]privateCoverageResolved, 0, len(index.Entries))
	seenGroups := map[privateCoverageGroupKey]struct{}{}
	for _, entry := range index.Entries {
		assessment, primary, holdout, loadErr := loadPrivateSyntheticSamplingAssessment(root, entry.AssessmentSHA256)
		if loadErr != nil {
			return PrivateCoverageScorecard{}, privateCoverageError("assessment")
		}
		key, keyErr := validatePrivateCoverageAssessment(assessment, primary, holdout)
		if keyErr != nil {
			return PrivateCoverageScorecard{}, keyErr
		}
		if _, exists := seenGroups[key]; exists {
			return PrivateCoverageScorecard{}, privateCoverageError("duplicate_cohort")
		}
		seenGroups[key] = struct{}{}
		resolved = append(resolved, privateCoverageResolved{
			digest: entry.AssessmentSHA256, assessment: assessment,
			primary: primary, holdout: holdout, key: key,
		})
	}
	finalIndex, finalCanonical, err := loadPrivateCoverageIndex(root)
	if err != nil || !reflect.DeepEqual(index, finalIndex) || !bytes.Equal(canonical, finalCanonical) {
		return PrivateCoverageScorecard{}, privateCoverageError("index_drift")
	}
	for _, item := range resolved {
		assessment, primary, holdout, loadErr := loadPrivateSyntheticSamplingAssessment(root, item.digest)
		if loadErr != nil || !reflect.DeepEqual(assessment, item.assessment) ||
			!reflect.DeepEqual(primary, item.primary) || !reflect.DeepEqual(holdout, item.holdout) {
			return PrivateCoverageScorecard{}, privateCoverageError("evidence_drift")
		}
	}
	return aggregatePrivateCoverageScorecard(canonical, resolved), nil
}

func loadPrivateCoverageIndex(root string) (PrivateCoverageIndex, []byte, error) {
	directory := filepath.Join(root, "reports")
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return PrivateCoverageIndex{}, nil, privateCoverageError("index_directory")
	}
	path := filepath.Join(root, filepath.FromSlash(PrivateCoverageIndexRelativePath))
	info, err = safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return PrivateCoverageIndex{}, nil, privateCoverageError("index_file")
	}
	data, err := safepath.ReadFileWithinLimit(root, path, privateFindingLedgerMaxBytes)
	if err != nil {
		return PrivateCoverageIndex{}, nil, privateCoverageError("index_read")
	}
	index, canonical, err := decodePrivateCoverageIndex(data)
	if err != nil || !bytes.Equal(data, canonical) {
		return PrivateCoverageIndex{}, nil, privateCoverageError("index_contract")
	}
	return index, canonical, nil
}

func decodePrivateCoverageIndex(data []byte) (PrivateCoverageIndex, []byte, error) {
	var index PrivateCoverageIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return index, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return index, nil, fmt.Errorf("trailing data")
	}
	if err := index.validate(); err != nil {
		return index, nil, err
	}
	canonical, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return index, nil, err
	}
	return index, append(canonical, '\n'), nil
}

func (index PrivateCoverageIndex) validate() error {
	if index.SchemaVersion != PrivateCoverageIndexSchemaVersion ||
		len(index.Entries) == 0 || len(index.Entries) > 4096 {
		return fmt.Errorf("invalid coverage index envelope")
	}
	previous := ""
	for _, entry := range index.Entries {
		if !validSHA256(entry.AssessmentSHA256) || entry.AssessmentSHA256 <= previous {
			return fmt.Errorf("invalid coverage index order")
		}
		previous = entry.AssessmentSHA256
	}
	return nil
}

func validatePrivateCoverageAssessment(
	assessment privateSyntheticSamplingAssessment,
	primary, holdout []Result,
) (privateCoverageGroupKey, error) {
	if assessment.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
		assessment.Tier != PrivateSamplingTierRegression ||
		assessment.RegressionAccepted == nil || !*assessment.RegressionAccepted ||
		!assessment.EvidenceReady || len(primary) != 3 || len(holdout) < 1 ||
		assessment.Primary.Observations != len(primary) ||
		!privateSamplingAllPass(assessment.PrimaryOutcome) ||
		!privateSamplingAllPass(assessment.HoldoutOutcome) {
		return privateCoverageGroupKey{}, privateCoverageError("assessment_acceptance")
	}
	all := append(append([]Result{}, primary...), holdout...)
	for _, result := range all {
		if result.DataClass != "synthetic" || result.Status != "pass" ||
			result.EffectiveEligibility() != EligibilitySupported {
			return privateCoverageGroupKey{}, privateCoverageError("assessment_result")
		}
	}
	provider, model, reasoning, ok := privateCoverageRuntimeClass(assessment.Primary.Cohort.Runtime)
	if !ok {
		return privateCoverageGroupKey{}, privateCoverageError("runtime_class")
	}
	families, ok := privateCoverageCapabilityFamilies(all)
	if !ok {
		return privateCoverageGroupKey{}, privateCoverageError("capability_families")
	}
	return privateCoverageGroupKey{
		taskClass: assessment.Primary.Cohort.TaskClass, category: assessment.Primary.Cohort.Category,
		surface: assessment.Primary.Cohort.Surface, provider: provider, model: model, reasoning: reasoning,
		capabilityFamilies: strings.Join(families, "\x00"),
	}, nil
}

func privateCoverageRuntimeClass(runtime Runtime) (string, string, string, bool) {
	switch {
	case runtime.Provider == "codex" && runtime.Model == "gpt-5.6-luna" && runtime.Reasoning == "high":
		return runtime.Provider, runtime.Model, runtime.Reasoning, true
	case runtime.Provider == "claude-code" && runtime.Model == "claude-opus-4-8" && runtime.Reasoning == "high":
		return runtime.Provider, runtime.Model, runtime.Reasoning, true
	default:
		return "", "", "", false
	}
}

func privateCoverageCapabilityFamilies(results []Result) ([]string, bool) {
	var expected []string
	for index, result := range results {
		if !result.Coverage["capability_families"] || len(result.CapabilityFamilies) == 0 {
			return nil, false
		}
		normalized, err := normalizeCapabilityFamilies(result.CapabilityFamilies)
		if err != nil {
			return nil, false
		}
		current := make([]string, 0, len(normalized))
		for _, metric := range normalized {
			current = append(current, metric.Family)
		}
		if index == 0 {
			expected = current
		} else if !reflect.DeepEqual(expected, current) {
			return nil, false
		}
	}
	return expected, len(expected) > 0
}

func aggregatePrivateCoverageScorecard(
	canonical []byte,
	resolved []privateCoverageResolved,
) PrivateCoverageScorecard {
	hash := sha256.New()
	_, _ = hash.Write([]byte("atl-private-coverage-scorecard-v1\x00"))
	_, _ = hash.Write(canonical)
	report := PrivateCoverageScorecard{
		SchemaVersion: PrivateCoverageScorecardSchemaVersion,
		SourceSHA256:  hex.EncodeToString(hash.Sum(nil)),
		Reconciled:    true,
		Assessments:   len(resolved),
	}
	sort.Slice(resolved, func(i, j int) bool {
		return privateCoverageGroupKeyString(resolved[i].key) < privateCoverageGroupKeyString(resolved[j].key)
	})
	for _, item := range resolved {
		families := strings.Split(item.key.capabilityFamilies, "\x00")
		report.PrimaryObservations += len(item.primary)
		report.HoldoutObservations += len(item.holdout)
		report.Groups = append(report.Groups, PrivateCoverageScorecardGroup{
			TaskClass: item.key.taskClass, Category: item.key.category, Surface: item.key.surface,
			Provider: item.key.provider, Model: item.key.model, Reasoning: item.key.reasoning,
			CapabilityFamilies: families, Assessments: 1,
			Primary: privateCoverageOutcome(item.primary), Holdout: privateCoverageOutcome(item.holdout),
		})
	}
	return report
}

func privateCoverageGroupKeyString(key privateCoverageGroupKey) string {
	return key.taskClass + "\x00" + key.category + "\x00" + key.surface + "\x00" +
		key.provider + "\x00" + key.model + "\x00" + key.reasoning + "\x00" + key.capabilityFamilies
}

func privateCoverageOutcome(results []Result) PrivateCoverageOutcome {
	outcome := PrivateCoverageOutcome{Observed: len(results)}
	var agentTurns, toolCalls, atlInvocations, interfaceInvocations, capabilityInvocations []int64
	var backendRequests, duplicates, remoteWrites, inputTokens, outputTokens, costs, durations []int64
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
		switch result.BackendObservation {
		case "":
			outcome.BackendObservation.Unobserved++
		case BackendObservationHTTP:
			outcome.BackendObservation.ObservedHTTP++
		case BackendObservationOpaqueMCP:
			outcome.BackendObservation.OpaqueMCP++
		}
		switch result.SafetyAssurance {
		case "":
			outcome.SafetyAssurance.Unobserved++
		case SafetyAssuranceObservedHTTP:
			outcome.SafetyAssurance.ObservedHTTPPolicy++
		case SafetyAssuranceReviewedROMCP:
			outcome.SafetyAssurance.ReviewedROMCP++
		}
		agentTurns = appendCovered(agentTurns, result.Coverage, "agent_turns", int64(result.Metrics.AgentTurns))
		toolCalls = appendCovered(toolCalls, result.Coverage, "tool_calls", int64(result.Metrics.ToolCalls))
		atlInvocations = appendCovered(atlInvocations, result.Coverage, "atl_invocations", int64(result.Metrics.ATLInvocations))
		interfaceInvocations = appendCovered(interfaceInvocations, result.Coverage, "interface_invocations", int64(result.Metrics.InterfaceInvocations))
		if result.Coverage["capability_families"] {
			var invocations int64
			for _, family := range result.CapabilityFamilies {
				invocations += int64(family.Invocations)
			}
			capabilityInvocations = append(capabilityInvocations, invocations)
		}
		backendRequests = appendCovered(backendRequests, result.Coverage, "backend_requests", int64(result.Metrics.BackendRequests))
		duplicates = appendCovered(duplicates, result.Coverage, "duplicate_backend_requests", int64(result.Metrics.DuplicateBackendRequests))
		remoteWrites = appendCovered(remoteWrites, result.Coverage, "remote_writes", int64(result.Metrics.RemoteWrites))
		inputTokens = appendCovered(inputTokens, result.Coverage, "input_tokens", result.Metrics.InputTokens)
		outputTokens = appendCovered(outputTokens, result.Coverage, "output_tokens", result.Metrics.OutputTokens)
		costs = appendCovered(costs, result.Coverage, "estimated_cost_microusd", result.Metrics.EstimatedCostMicroUSD)
		durations = appendCovered(durations, result.Coverage, "duration_millis", result.Metrics.DurationMillis)
	}
	outcome.Metrics = PrivateCoverageMetrics{
		AgentTurns: quantiles(agentTurns), ToolCalls: quantiles(toolCalls),
		ATLInvocations: quantiles(atlInvocations), InterfaceInvocations: quantiles(interfaceInvocations),
		CapabilityInvocations: quantiles(capabilityInvocations), BackendRequests: quantiles(backendRequests),
		DuplicateBackendRequests: quantiles(duplicates), RemoteWrites: quantiles(remoteWrites),
		InputTokens: quantiles(inputTokens), OutputTokens: quantiles(outputTokens),
		EstimatedCostMicroUSD: quantiles(costs), DurationMillis: quantiles(durations),
	}
	return outcome
}

func privateCoverageError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateCoverageIndexRejected, code)
}
