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
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateCoverageIndexSchemaVersion = 1
	PrivateCoverageIndexRelativePath  = "reports/sampling-coverage.v1.json"

	PrivateCoverageIndexV2SchemaVersion = 2
	PrivateCoverageIndexV2RelativePath  = "reports/sampling-coverage.v2.json"

	PrivateCoverageScorecardSchemaVersion = 2
)

var ErrPrivateCoverageIndexRejected = errors.New("private coverage index rejected")

type PrivateCoverageIndex struct {
	SchemaVersion int                         `json:"schema_version"`
	Entries       []PrivateCoverageIndexEntry `json:"entries"`
}

type PrivateCoverageIndexEntry struct {
	AssessmentSHA256 string `json:"assessment_sha256"`
}

type PrivateCoverageIndexV2 struct {
	SchemaVersion int                           `json:"schema_version"`
	Entries       []PrivateCoverageIndexV2Entry `json:"entries"`
}

type PrivateCoverageIndexV2Entry struct {
	AssessmentSource string `json:"assessment_source"`
	AssessmentSHA256 string `json:"assessment_sha256"`
}

type PrivateCoverageScorecardOptions struct {
	Root           string
	RepositoryRoot string
}

type PrivateCoverageScorecard struct {
	SchemaVersion       int                             `json:"schema_version"`
	IndexSchemaVersion  int                             `json:"index_schema_version"`
	SourceSHA256        string                          `json:"source_sha256"`
	Reconciled          bool                            `json:"reconciled"`
	Assessments         int                             `json:"assessments"`
	PrimaryObservations int                             `json:"primary_observations"`
	HoldoutObservations int                             `json:"holdout_observations"`
	Groups              []PrivateCoverageScorecardGroup `json:"groups"`
}

type PrivateCoverageScorecardGroup struct {
	AssessmentSource   string                 `json:"assessment_source"`
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
	source     string
	digest     string
	assessment any
	primary    []Result
	holdout    []Result
	key        privateCoverageGroupKey
}

type privateCoverageGroupKey struct {
	source, taskClass, category, surface, provider, model, reasoning, capabilityFamilies string
}

// BuildPrivateCoverageScorecard validates the exact active sampling index and
// its immutable accepted evidence without changing the workspace.
func BuildPrivateCoverageScorecard(options PrivateCoverageScorecardOptions) (PrivateCoverageScorecard, error) {
	return buildPrivateCoverageScorecard(options, LoadCompletedPrivateRun)
}

func buildPrivateCoverageScorecard(options PrivateCoverageScorecardOptions, load privateFindingSourceLoader) (PrivateCoverageScorecard, error) {
	root, repository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateCoverageScorecard{}, privateCoverageError("workspace", err)
	}
	index, canonical, err := loadPrivateCoverageIndex(root)
	if err != nil {
		return PrivateCoverageScorecard{}, err
	}
	resolved := make([]privateCoverageResolved, 0, len(index.Entries))
	seenGroups := map[privateCoverageGroupKey]struct{}{}
	for _, entry := range index.Entries {
		assessment, primary, holdout, loadErr := loadPrivateCoverageAssessment(
			root, repository, entry.AssessmentSource, entry.AssessmentSHA256, load,
		)
		if loadErr != nil {
			return PrivateCoverageScorecard{}, privateCoverageError("assessment", loadErr)
		}
		key, keyErr := validatePrivateCoverageAssessment(entry.AssessmentSource, assessment, primary, holdout)
		if keyErr != nil {
			return PrivateCoverageScorecard{}, keyErr
		}
		if _, exists := seenGroups[key]; exists {
			// Two accepted assessments describing the same cohort is a
			// comparison result, so there is no failure to attach.
			return PrivateCoverageScorecard{}, privateCoverageError("duplicate_cohort")
		}
		seenGroups[key] = struct{}{}
		resolved = append(resolved, privateCoverageResolved{
			source: entry.AssessmentSource, digest: entry.AssessmentSHA256, assessment: assessment,
			primary: primary, holdout: holdout, key: key,
		})
	}
	finalIndex, finalCanonical, err := loadPrivateCoverageIndex(root)
	if err != nil || !reflect.DeepEqual(index, finalIndex) || !bytes.Equal(canonical, finalCanonical) {
		// A re-read that succeeds but no longer matches has nothing to attach;
		// the nil cause is dropped.
		return PrivateCoverageScorecard{}, privateCoverageError("index_drift", err)
	}
	for _, item := range resolved {
		assessment, primary, holdout, loadErr := loadPrivateCoverageAssessment(
			root, repository, item.source, item.digest, load,
		)
		if loadErr != nil || !reflect.DeepEqual(assessment, item.assessment) ||
			!reflect.DeepEqual(primary, item.primary) || !reflect.DeepEqual(holdout, item.holdout) {
			// Evidence that reloads cleanly but differs is a comparison
			// rejection; the nil cause is dropped.
			return PrivateCoverageScorecard{}, privateCoverageError("evidence_drift", loadErr)
		}
	}
	return aggregatePrivateCoverageScorecard(index.SchemaVersion, canonical, resolved), nil
}

type privateCoverageSelectedIndex struct {
	SchemaVersion int
	Entries       []PrivateCoverageIndexV2Entry
}

func loadPrivateCoverageIndex(root string) (privateCoverageSelectedIndex, []byte, error) {
	directory := filepath.Join(root, "reports")
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		// A directory that stats cleanly but has the wrong type or permission
		// mode is rejected by observation alone; the nil cause is dropped.
		return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_directory", err)
	}
	legacyPath := filepath.Join(root, filepath.FromSlash(PrivateCoverageIndexRelativePath))
	currentPath := filepath.Join(root, filepath.FromSlash(PrivateCoverageIndexV2RelativePath))
	legacyInfo, legacyExists, err := privateCoverageIndexEntry(root, legacyPath)
	if err != nil {
		return privateCoverageSelectedIndex{}, nil, err
	}
	currentInfo, currentExists, err := privateCoverageIndexEntry(root, currentPath)
	if err != nil {
		return privateCoverageSelectedIndex{}, nil, err
	}
	if legacyExists == currentExists {
		// Both probes succeeded: either two indexes are present or neither is.
		// Ordinary absence is not a failure, so nothing is attached.
		code := "index_file"
		if legacyExists {
			code = "index_ambiguous"
		}
		return privateCoverageSelectedIndex{}, nil, privateCoverageError(code)
	}
	path, selectedInfo, version := legacyPath, legacyInfo, PrivateCoverageIndexSchemaVersion
	if currentExists {
		path, selectedInfo, version = currentPath, currentInfo, PrivateCoverageIndexV2SchemaVersion
	}
	data, err := safepath.ReadFileWithinLimit(root, path, privateFindingLedgerMaxBytes)
	if err != nil {
		return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_read", err)
	}
	legacyFinal, legacyStillExists, legacyErr := privateCoverageIndexEntry(root, legacyPath)
	currentFinal, currentStillExists, currentErr := privateCoverageIndexEntry(root, currentPath)
	if legacyErr != nil || currentErr != nil ||
		legacyExists != legacyStillExists || currentExists != currentStillExists ||
		legacyExists && (!os.SameFile(legacyInfo, legacyFinal) || !sameSyntheticRootInfo(legacyInfo, legacyFinal)) ||
		currentExists && (!os.SameFile(currentInfo, currentFinal) || !sameSyntheticRootInfo(currentInfo, currentFinal)) {
		// Retain whichever recheck actually failed, in the fixed order the two
		// candidates are probed; an identity or existence change observed by a
		// successful recheck contributes no cause and its nil is dropped.
		return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_file", legacyErr, currentErr)
	}
	finalInfo := legacyFinal
	if currentExists {
		finalInfo = currentFinal
	}
	if !os.SameFile(selectedInfo, finalInfo) || !sameSyntheticRootInfo(selectedInfo, finalInfo) {
		// Both stats succeeded; only the identity comparison rejects the file.
		return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_file")
	}
	if version == PrivateCoverageIndexSchemaVersion {
		index, canonical, decodeErr := decodePrivateCoverageIndex(data)
		if decodeErr != nil || !bytes.Equal(data, canonical) {
			// An index that decodes but is not byte-canonical is rejected by
			// comparison; the nil cause is dropped.
			return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_contract", decodeErr)
		}
		entries := make([]PrivateCoverageIndexV2Entry, 0, len(index.Entries))
		for _, entry := range index.Entries {
			entries = append(entries, PrivateCoverageIndexV2Entry{
				AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
				AssessmentSHA256: entry.AssessmentSHA256,
			})
		}
		return privateCoverageSelectedIndex{SchemaVersion: version, Entries: entries}, canonical, nil
	}
	index, canonical, decodeErr := decodePrivateCoverageIndexV2(data)
	if decodeErr != nil || !bytes.Equal(data, canonical) {
		// Same contract check for the current schema: a non-canonical but
		// decodable index attaches nothing.
		return privateCoverageSelectedIndex{}, nil, privateCoverageError("index_contract", decodeErr)
	}
	return privateCoverageSelectedIndex{SchemaVersion: version, Entries: index.Entries}, canonical, nil
}

func privateCoverageIndexEntry(root, path string) (os.FileInfo, bool, error) {
	info, err := safepath.StatWithin(root, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		// An ordinary absence already returned above, so a remaining stat
		// failure is real and is retained; a file rejected only by its observed
		// type or permission mode drops the nil cause.
		return nil, false, privateCoverageError("index_file", err)
	}
	return info, true, nil
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

func decodePrivateCoverageIndexV2(data []byte) (PrivateCoverageIndexV2, []byte, error) {
	var index PrivateCoverageIndexV2
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

func (index PrivateCoverageIndexV2) validate() error {
	if index.SchemaVersion != PrivateCoverageIndexV2SchemaVersion ||
		len(index.Entries) == 0 || len(index.Entries) > 4096 {
		return fmt.Errorf("invalid coverage index envelope")
	}
	previous := ""
	for _, entry := range index.Entries {
		if entry.AssessmentSource != PrivateFindingAcceptanceSourcePrivateLive &&
			entry.AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot {
			return fmt.Errorf("invalid coverage index source")
		}
		current := entry.AssessmentSource + "\x00" + entry.AssessmentSHA256
		if !validSHA256(entry.AssessmentSHA256) || current <= previous {
			return fmt.Errorf("invalid coverage index order")
		}
		previous = current
	}
	return nil
}

func loadPrivateCoverageAssessment(root, repository, source, digest string, load privateFindingSourceLoader) (any, []Result, []Result, error) {
	switch source {
	case PrivateFindingAcceptanceSourcePrivateLive:
		assessment, primary, holdout, err := loadPrivateSamplingAssessment(root, repository, digest, load)
		return assessment, primary, holdout, err
	case PrivateFindingAcceptanceSourceSyntheticRoot:
		assessment, primary, holdout, err := loadPrivateSyntheticSamplingAssessment(root, digest)
		return assessment, primary, holdout, err
	default:
		// An unrecognised source is decided by the label alone, with no load
		// attempted and therefore no failure to attach.
		return nil, nil, nil, privateCoverageError("assessment_source")
	}
}

// validatePrivateCoverageAssessment inspects evidence that already loaded, so
// every rejection below follows from validation or comparison alone and none of
// them carries a cause.
func validatePrivateCoverageAssessment(
	source string,
	assessment any,
	primary, holdout []Result,
) (privateCoverageGroupKey, error) {
	var primaryOutcome, holdoutOutcome PrivateSamplingOutcome
	var declared *privateSyntheticSamplingCohort
	switch value := assessment.(type) {
	case privateSamplingAssessment:
		if source != PrivateFindingAcceptanceSourcePrivateLive ||
			value.SchemaVersion != PrivateSamplingSchemaVersion ||
			value.Tier != PrivateSamplingTierRegression ||
			value.RegressionAccepted == nil || !*value.RegressionAccepted ||
			!value.EvidenceReady || len(value.Primary) != len(primary) || len(value.Holdout) != len(holdout) {
			return privateCoverageGroupKey{}, privateCoverageError("assessment_acceptance")
		}
		primaryOutcome, holdoutOutcome = value.PrimaryOutcome, value.HoldoutOutcome
	case privateSyntheticSamplingAssessment:
		if source != PrivateFindingAcceptanceSourceSyntheticRoot ||
			value.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
			value.Tier != PrivateSamplingTierRegression ||
			value.RegressionAccepted == nil || !*value.RegressionAccepted ||
			!value.EvidenceReady || value.Primary.Observations != len(primary) {
			return privateCoverageGroupKey{}, privateCoverageError("assessment_acceptance")
		}
		primaryOutcome, holdoutOutcome = value.PrimaryOutcome, value.HoldoutOutcome
		declared = &value.Primary.Cohort
	default:
		return privateCoverageGroupKey{}, privateCoverageError("assessment_source")
	}
	if len(primary) != 3 || len(holdout) < 1 ||
		!privateSamplingAllPass(primaryOutcome) || !privateSamplingAllPass(holdoutOutcome) {
		return privateCoverageGroupKey{}, privateCoverageError("assessment_acceptance")
	}
	all := append(append([]Result{}, primary...), holdout...)
	expectedDataClass := "synthetic"
	if source == PrivateFindingAcceptanceSourcePrivateLive {
		expectedDataClass = "private-local"
	}
	for _, result := range all {
		if result.DataClass != expectedDataClass || result.Status != "pass" ||
			result.EffectiveEligibility() != EligibilitySupported {
			return privateCoverageGroupKey{}, privateCoverageError("assessment_result")
		}
	}
	first := primary[0]
	if declared != nil &&
		(declared.TaskClass != first.TaskClass ||
			declared.Category != first.EffectiveCategory() ||
			declared.Surface != first.EffectiveSurface() ||
			declared.Runtime.Provider != first.Runtime.Provider ||
			declared.Runtime.Model != first.Runtime.Model ||
			declared.Runtime.Reasoning != first.Runtime.Reasoning) {
		return privateCoverageGroupKey{}, privateCoverageError("assessment_cohort")
	}
	for _, result := range all[1:] {
		if result.TaskClass != first.TaskClass || result.EffectiveCategory() != first.EffectiveCategory() ||
			result.EffectiveSurface() != first.EffectiveSurface() ||
			result.Runtime.Provider != first.Runtime.Provider || result.Runtime.Model != first.Runtime.Model ||
			result.Runtime.Reasoning != first.Runtime.Reasoning {
			return privateCoverageGroupKey{}, privateCoverageError("assessment_cohort")
		}
	}
	provider, model, reasoning, ok := privateCoverageRuntimeClass(first.Runtime)
	if !ok {
		return privateCoverageGroupKey{}, privateCoverageError("runtime_class")
	}
	families, ok := privateCoverageCapabilityFamilies(all)
	if !ok {
		// The helper reports a boolean, so nothing concrete reaches this branch.
		return privateCoverageGroupKey{}, privateCoverageError("capability_families")
	}
	return privateCoverageGroupKey{
		source: source, taskClass: first.TaskClass, category: first.EffectiveCategory(),
		surface: first.EffectiveSurface(), provider: provider, model: model, reasoning: reasoning,
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
	indexSchemaVersion int,
	canonical []byte,
	resolved []privateCoverageResolved,
) PrivateCoverageScorecard {
	hash := sha256.New()
	_, _ = hash.Write([]byte("atl-private-coverage-scorecard-v2\x00"))
	_, _ = hash.Write(canonical)
	report := PrivateCoverageScorecard{
		SchemaVersion:      PrivateCoverageScorecardSchemaVersion,
		IndexSchemaVersion: indexSchemaVersion,
		SourceSHA256:       hex.EncodeToString(hash.Sum(nil)),
		Reconciled:         true,
		Assessments:        len(resolved),
	}
	sort.Slice(resolved, func(i, j int) bool {
		return privateCoverageGroupKeyString(resolved[i].key) < privateCoverageGroupKeyString(resolved[j].key)
	})
	for _, item := range resolved {
		families := strings.Split(item.key.capabilityFamilies, "\x00")
		report.PrimaryObservations += len(item.primary)
		report.HoldoutObservations += len(item.holdout)
		report.Groups = append(report.Groups, PrivateCoverageScorecardGroup{
			AssessmentSource: item.key.source,
			TaskClass:        item.key.taskClass, Category: item.key.category, Surface: item.key.surface,
			Provider: item.key.provider, Model: item.key.model, Reasoning: item.key.reasoning,
			CapabilityFamilies: families, Assessments: 1,
			Primary: privateCoverageOutcome(item.primary), Holdout: privateCoverageOutcome(item.holdout),
		})
	}
	return report
}

func privateCoverageGroupKeyString(key privateCoverageGroupKey) string {
	return key.source + "\x00" + key.taskClass + "\x00" + key.category + "\x00" + key.surface + "\x00" +
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

// privateCoverageError classifies a coverage-index rejection under the stable
// sentinel and code while retaining the concrete causes already in hand. The
// rendered message stays exactly the sentinel plus the code, so a configured
// workspace path, an index or assessment digest, or a dependency failure never
// reaches a log line. Callers can traverse every attached cause through the
// standard unwrap tree and use errors.Is or errors.As for typed or sentinel-
// bearing causes, including a classification raised deeper in the load. Nil
// causes are dropped, so a branch that rejects on either a failure or a
// comparison can pass its error unguarded, and a rejection that follows from
// validation or comparison alone carries nothing.
func privateCoverageError(code string, causes ...error) error {
	return codedError(ErrPrivateCoverageIndexRejected, code, causes...)
}
