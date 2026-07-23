package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateSyntheticSamplingSchemaVersion = 2
	privateSyntheticRootDirectory         = "synthetic-roots"
)

type PrivateSyntheticSamplingSpec struct {
	SchemaVersion int                               `json:"schema_version"`
	Tier          string                            `json:"tier"`
	Primary       PrivateSyntheticSamplingRootRef   `json:"primary"`
	Holdout       []PrivateSyntheticSamplingRootRef `json:"holdout,omitempty"`
}

type PrivateSyntheticSamplingRootRef struct {
	Root         string `json:"root"`
	SourceSHA256 string `json:"source_sha256"`
}

type privateSyntheticSamplingCohort struct {
	ScenarioID              string  `json:"scenario_id"`
	TaskClass               string  `json:"task_class"`
	DataClass               string  `json:"data_class"`
	Category                string  `json:"category"`
	Variant                 string  `json:"variant"`
	Surface                 string  `json:"surface"`
	Runtime                 Runtime `json:"runtime"`
	TaskContractSHA256      string  `json:"task_contract_sha256"`
	ExecutionContractSHA256 string  `json:"execution_contract_sha256"`
	AgentExecutableSHA256   string  `json:"agent_executable_sha256"`
	ATLExecutableSHA256     string  `json:"atl_executable_sha256"`
	WrapperExecutableSHA256 string  `json:"wrapper_executable_sha256"`
}

type privateSyntheticSamplingBinding struct {
	Reference    PrivateSyntheticSamplingRootRef `json:"reference"`
	Cohort       privateSyntheticSamplingCohort  `json:"cohort"`
	Observations int                             `json:"observations"`
}

type privateSyntheticSamplingAssessment struct {
	SchemaVersion      int                               `json:"schema_version"`
	Tier               string                            `json:"tier"`
	SourceSHA256       string                            `json:"source_sha256"`
	EvidenceReady      bool                              `json:"evidence_ready"`
	RegressionAccepted *bool                             `json:"regression_accepted,omitempty"`
	Primary            privateSyntheticSamplingBinding   `json:"primary"`
	Holdout            []privateSyntheticSamplingBinding `json:"holdout,omitempty"`
	PrimaryOutcome     PrivateSamplingOutcome            `json:"primary_outcome"`
	HoldoutOutcome     PrivateSamplingOutcome            `json:"holdout_outcome"`
}

func previewPrivateSyntheticSampling(root string, data []byte) (PrivateSamplingPreview, []byte, error) {
	spec, canonical, err := decodePrivateSyntheticSamplingSpec(data)
	if err != nil || !bytes.Equal(data, canonical) {
		return PrivateSamplingPreview{}, nil, privateSamplingError("spec_contract")
	}
	assessment, _, _, err := buildPrivateSyntheticSamplingAssessment(root, spec, sha256HexBytes(canonical))
	if err != nil {
		return PrivateSamplingPreview{}, nil, err
	}
	assessmentData, err := encodePrivateSyntheticSamplingAssessment(assessment)
	if err != nil {
		return PrivateSamplingPreview{}, nil, privateSamplingError("assessment_contract")
	}
	digest := sha256HexBytes(append([]byte("atl-private-sampling-assessment-v2\x00"), assessmentData...))
	preview := PrivateSamplingPreview{
		SchemaVersion: assessment.SchemaVersion, Tier: assessment.Tier,
		SourceSHA256: assessment.SourceSHA256, AssessmentSHA256: digest,
		EvidenceReady: assessment.EvidenceReady, RegressionAccepted: assessment.RegressionAccepted,
		Primary: assessment.PrimaryOutcome, Holdout: assessment.HoldoutOutcome,
	}
	return preview, assessmentData, nil
}

func decodePrivateSyntheticSamplingSpec(data []byte) (PrivateSyntheticSamplingSpec, []byte, error) {
	var spec PrivateSyntheticSamplingSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return spec, nil, fmt.Errorf("trailing data")
	}
	if err := spec.validate(); err != nil {
		return spec, nil, err
	}
	canonical, err := encodePrivateSyntheticSamplingSpec(spec)
	return spec, canonical, err
}

func encodePrivateSyntheticSamplingSpec(spec PrivateSyntheticSamplingSpec) ([]byte, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	canonical, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

func (spec PrivateSyntheticSamplingSpec) validate() error {
	if spec.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
		!validPrivateSyntheticSamplingRootRef(spec.Primary) ||
		len(spec.Holdout) > privateSamplingMaxRefs {
		return fmt.Errorf("invalid synthetic sampling envelope")
	}
	switch spec.Tier {
	case PrivateSamplingTierCalibration:
		if len(spec.Holdout) != 0 {
			return fmt.Errorf("calibration forbids holdout evidence")
		}
	case PrivateSamplingTierRegression, PrivateSamplingTierDecision:
		if len(spec.Holdout) == 0 {
			return fmt.Errorf("sampling tier requires holdout evidence")
		}
	default:
		return fmt.Errorf("invalid sampling tier")
	}
	seenRoots := map[string]struct{}{spec.Primary.Root: {}}
	seenSources := map[string]struct{}{spec.Primary.SourceSHA256: {}}
	previous := ""
	for _, ref := range spec.Holdout {
		if !validPrivateSyntheticSamplingRootRef(ref) || ref.Root <= previous {
			return fmt.Errorf("invalid synthetic sampling reference order")
		}
		if _, exists := seenRoots[ref.Root]; exists {
			return fmt.Errorf("duplicate synthetic sampling root")
		}
		if _, exists := seenSources[ref.SourceSHA256]; exists {
			return fmt.Errorf("duplicate synthetic sampling source")
		}
		seenRoots[ref.Root] = struct{}{}
		seenSources[ref.SourceSHA256] = struct{}{}
		previous = ref.Root
	}
	return nil
}

func validPrivateSyntheticSamplingRootRef(ref PrivateSyntheticSamplingRootRef) bool {
	return privateWorkspaceAliasRE.MatchString(ref.Root) && ref.Root != "current" && validSHA256(ref.SourceSHA256)
}

func buildPrivateSyntheticSamplingAssessment(root string, spec PrivateSyntheticSamplingSpec, sourceSHA256 string) (privateSyntheticSamplingAssessment, []Result, []Result, error) {
	primaryBinding, primaryResults, err := resolvePrivateSyntheticSamplingRoot(root, spec.Primary)
	if err != nil {
		return privateSyntheticSamplingAssessment{}, nil, nil, err
	}
	holdoutBindings := make([]privateSyntheticSamplingBinding, 0, len(spec.Holdout))
	holdoutResults := make([]Result, 0, len(spec.Holdout))
	seenScenarios := map[string]struct{}{primaryBinding.Cohort.ScenarioID: {}}
	seenTasks := map[string]struct{}{primaryBinding.Cohort.TaskContractSHA256: {}}
	for _, ref := range spec.Holdout {
		binding, results, resolveErr := resolvePrivateSyntheticSamplingRoot(root, ref)
		if resolveErr != nil {
			return privateSyntheticSamplingAssessment{}, nil, nil, resolveErr
		}
		if !compatiblePrivateSyntheticSamplingHoldout(primaryBinding.Cohort, binding.Cohort) {
			return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("holdout_incompatible")
		}
		if _, exists := seenScenarios[binding.Cohort.ScenarioID]; exists {
			return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("duplicate_observation")
		}
		if _, exists := seenTasks[binding.Cohort.TaskContractSHA256]; exists {
			return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("duplicate_observation")
		}
		seenScenarios[binding.Cohort.ScenarioID] = struct{}{}
		seenTasks[binding.Cohort.TaskContractSHA256] = struct{}{}
		holdoutBindings = append(holdoutBindings, binding)
		holdoutResults = append(holdoutResults, results...)
	}
	if err := validatePrivateSyntheticSamplingCardinality(spec.Tier, len(primaryResults), len(holdoutResults)); err != nil {
		return privateSyntheticSamplingAssessment{}, nil, nil, err
	}
	primaryOutcome := privateSamplingOutcome(primaryResults)
	holdoutOutcome := privateSamplingOutcome(holdoutResults)
	assessment := privateSyntheticSamplingAssessment{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: spec.Tier, SourceSHA256: sourceSHA256,
		EvidenceReady: true, Primary: primaryBinding, Holdout: holdoutBindings,
		PrimaryOutcome: primaryOutcome, HoldoutOutcome: holdoutOutcome,
	}
	if spec.Tier == PrivateSamplingTierRegression {
		accepted := privateSamplingAllPass(primaryOutcome) && privateSamplingAllPass(holdoutOutcome)
		assessment.RegressionAccepted = &accepted
	}
	return assessment, primaryResults, holdoutResults, nil
}

func loadPrivateSyntheticSamplingAssessment(root, digest string) (privateSyntheticSamplingAssessment, []Result, []Result, error) {
	if !validSHA256(digest) {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_digest")
	}
	directory := filepath.Join(root, "reports", "sampling")
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_directory")
	}
	path := filepath.Join(directory, digest+".json")
	info, err = safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_file")
	}
	data, err := safepath.ReadFileWithinLimit(root, path, privateSamplingMaxBytes)
	if err != nil {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_read")
	}
	var stored privateSyntheticSamplingAssessment
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_decode")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_decode")
	}
	canonical, err := encodePrivateSyntheticSamplingAssessment(stored)
	if err != nil || !bytes.Equal(data, canonical) ||
		sha256HexBytes(append([]byte("atl-private-sampling-assessment-v2\x00"), canonical...)) != digest {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_contract")
	}
	spec := PrivateSyntheticSamplingSpec{
		SchemaVersion: stored.SchemaVersion, Tier: stored.Tier, Primary: stored.Primary.Reference,
		Holdout: make([]PrivateSyntheticSamplingRootRef, 0, len(stored.Holdout)),
	}
	for _, binding := range stored.Holdout {
		spec.Holdout = append(spec.Holdout, binding.Reference)
	}
	specData, err := encodePrivateSyntheticSamplingSpec(spec)
	if err != nil || sha256HexBytes(specData) != stored.SourceSHA256 {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_source")
	}
	rebuilt, primary, holdout, err := buildPrivateSyntheticSamplingAssessment(root, spec, stored.SourceSHA256)
	if err != nil {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_evidence")
	}
	rebuiltData, err := encodePrivateSyntheticSamplingAssessment(rebuilt)
	if err != nil || !bytes.Equal(canonical, rebuiltData) {
		return privateSyntheticSamplingAssessment{}, nil, nil, privateSamplingError("assessment_drift")
	}
	return stored, primary, holdout, nil
}

func resolvePrivateSyntheticSamplingRoot(root string, ref PrivateSyntheticSamplingRootRef) (privateSyntheticSamplingBinding, []Result, error) {
	parent := filepath.Join(root, "reports", privateSyntheticRootDirectory)
	info, err := safepath.StatWithin(root, parent)
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return privateSyntheticSamplingBinding{}, nil, privateSamplingError("synthetic_root_directory")
	}
	rootPath := filepath.Join(parent, ref.Root)
	containedBefore, err := safepath.StatWithin(root, rootPath)
	if err != nil || !containedBefore.IsDir() {
		return privateSyntheticSamplingBinding{}, nil, privateSamplingError("synthetic_root")
	}
	aggregate, loaded, err := loadSyntheticOutputRootEvidence(rootPath)
	containedAfter, containedErr := safepath.StatWithin(root, rootPath)
	if err != nil || aggregate.SchemaVersion != SyntheticRootAggregateSchemaVersion ||
		aggregate.SourceSHA256 != ref.SourceSHA256 || aggregate.Cohorts != 1 ||
		len(aggregate.Aggregate.Groups) != 1 || len(loaded.observations) != aggregate.Results ||
		len(loaded.observations) == 0 || containedErr != nil || !containedAfter.IsDir() ||
		!os.SameFile(containedBefore, loaded.root) || !os.SameFile(containedAfter, loaded.root) ||
		!sameSyntheticRootInfo(containedBefore, loaded.root) || !sameSyntheticRootInfo(containedAfter, loaded.root) {
		return privateSyntheticSamplingBinding{}, nil, privateSamplingError("synthetic_root")
	}
	first := loaded.observations[0]
	cohort := privateSyntheticSamplingCohort{
		ScenarioID: first.Result.ScenarioID, TaskClass: first.Result.TaskClass, DataClass: first.Result.DataClass,
		Category: first.Result.EffectiveCategory(), Variant: first.Result.Variant, Surface: first.Result.EffectiveSurface(),
		Runtime: first.Result.Runtime, TaskContractSHA256: first.Receipt.TaskContractSHA256,
		ExecutionContractSHA256: first.Receipt.ExecutionContractSHA256,
		AgentExecutableSHA256:   first.Receipt.AgentExecutableSHA256,
		ATLExecutableSHA256:     first.Receipt.ATLExecutableSHA256,
		WrapperExecutableSHA256: first.Receipt.WrapperExecutableSHA256,
	}
	if err := cohort.validate(); err != nil {
		return privateSyntheticSamplingBinding{}, nil, privateSamplingError("synthetic_cohort")
	}
	results := make([]Result, 0, len(loaded.observations))
	for _, observation := range loaded.observations {
		if !samePrivateSyntheticSamplingCohort(cohort, observation) {
			return privateSyntheticSamplingBinding{}, nil, privateSamplingError("synthetic_cohort")
		}
		results = append(results, observation.Result)
	}
	return privateSyntheticSamplingBinding{Reference: ref, Cohort: cohort, Observations: len(results)}, results, nil
}

func (cohort privateSyntheticSamplingCohort) validate() error {
	if validatePathComponentID("scenario id", cohort.ScenarioID) != nil ||
		cohort.DataClass != "synthetic" || cohort.Category == "" ||
		validatePathComponentID("run variant", cohort.Variant) != nil ||
		cohort.Surface == "" || cohort.Runtime.Provider != "codex" && cohort.Runtime.Provider != "claude-code" ||
		cohort.Runtime.PromptContractSHA256 == "" {
		return fmt.Errorf("invalid synthetic cohort")
	}
	if _, allowed := publicCorpusTaskClasses[cohort.TaskClass]; !allowed {
		return fmt.Errorf("invalid synthetic cohort")
	}
	for _, digest := range []string{
		cohort.Runtime.PromptContractSHA256,
		cohort.TaskContractSHA256,
		cohort.ExecutionContractSHA256,
		cohort.AgentExecutableSHA256,
		cohort.ATLExecutableSHA256,
		cohort.WrapperExecutableSHA256,
	} {
		if !validSHA256(digest) {
			return fmt.Errorf("invalid synthetic cohort")
		}
	}
	return nil
}

func samePrivateSyntheticSamplingCohort(cohort privateSyntheticSamplingCohort, observation syntheticRunEvidence) bool {
	result, receipt := observation.Result, observation.Receipt
	return result.ScenarioID == cohort.ScenarioID &&
		result.TaskClass == cohort.TaskClass && result.DataClass == cohort.DataClass &&
		result.EffectiveCategory() == cohort.Category && result.Variant == cohort.Variant &&
		result.EffectiveSurface() == cohort.Surface && result.Runtime == cohort.Runtime &&
		receipt.TaskContractSHA256 == cohort.TaskContractSHA256 &&
		receipt.ExecutionContractSHA256 == cohort.ExecutionContractSHA256 &&
		receipt.AgentExecutableSHA256 == cohort.AgentExecutableSHA256 &&
		receipt.ATLExecutableSHA256 == cohort.ATLExecutableSHA256 &&
		receipt.WrapperExecutableSHA256 == cohort.WrapperExecutableSHA256
}

func compatiblePrivateSyntheticSamplingHoldout(primary, holdout privateSyntheticSamplingCohort) bool {
	return primary.ScenarioID != holdout.ScenarioID &&
		primary.TaskContractSHA256 != holdout.TaskContractSHA256 &&
		primary.ExecutionContractSHA256 != holdout.ExecutionContractSHA256 &&
		primary.Runtime.PromptContractSHA256 != holdout.Runtime.PromptContractSHA256 &&
		primary.TaskClass == holdout.TaskClass && primary.DataClass == holdout.DataClass &&
		primary.Category == holdout.Category && primary.Variant == holdout.Variant &&
		primary.Surface == holdout.Surface &&
		primary.Runtime.Provider == holdout.Runtime.Provider &&
		primary.Runtime.AgentVersion == holdout.Runtime.AgentVersion &&
		primary.Runtime.Model == holdout.Runtime.Model &&
		primary.Runtime.Reasoning == holdout.Runtime.Reasoning &&
		primary.Runtime.ATLVersion == holdout.Runtime.ATLVersion &&
		primary.Runtime.PluginVersion == holdout.Runtime.PluginVersion &&
		primary.Runtime.SkillDigest == holdout.Runtime.SkillDigest &&
		primary.Runtime.SkillActivation == holdout.Runtime.SkillActivation &&
		primary.AgentExecutableSHA256 == holdout.AgentExecutableSHA256 &&
		primary.ATLExecutableSHA256 == holdout.ATLExecutableSHA256 &&
		primary.WrapperExecutableSHA256 == holdout.WrapperExecutableSHA256
}

func validatePrivateSyntheticSamplingCardinality(tier string, primary, holdout int) error {
	switch tier {
	case PrivateSamplingTierCalibration:
		if primary != 1 || holdout != 0 {
			return privateSamplingError("calibration_cardinality")
		}
	case PrivateSamplingTierRegression:
		if primary != 3 || holdout < 1 {
			return privateSamplingError("regression_cardinality")
		}
	case PrivateSamplingTierDecision:
		if primary < 10 || holdout < 1 {
			return privateSamplingError("decision_cardinality")
		}
	default:
		return privateSamplingError("sampling_tier")
	}
	return nil
}

func encodePrivateSyntheticSamplingAssessment(assessment privateSyntheticSamplingAssessment) ([]byte, error) {
	if assessment.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
		!validSHA256(assessment.SourceSHA256) || !assessment.EvidenceReady ||
		assessment.Primary.Observations < 1 ||
		assessment.PrimaryOutcome.Observed != assessment.Primary.Observations ||
		!validPrivateSamplingOutcome(assessment.PrimaryOutcome) ||
		!validPrivateSamplingOutcome(assessment.HoldoutOutcome) {
		return nil, privateSamplingError("assessment_contract")
	}
	spec := PrivateSyntheticSamplingSpec{
		SchemaVersion: assessment.SchemaVersion, Tier: assessment.Tier,
		Primary: assessment.Primary.Reference,
		Holdout: make([]PrivateSyntheticSamplingRootRef, 0, len(assessment.Holdout)),
	}
	holdoutObserved := 0
	if assessment.Primary.Cohort.validate() != nil {
		return nil, privateSamplingError("assessment_contract")
	}
	for _, binding := range assessment.Holdout {
		if binding.Observations < 1 || binding.Cohort.validate() != nil ||
			!compatiblePrivateSyntheticSamplingHoldout(assessment.Primary.Cohort, binding.Cohort) {
			return nil, privateSamplingError("assessment_contract")
		}
		spec.Holdout = append(spec.Holdout, binding.Reference)
		holdoutObserved += binding.Observations
	}
	if spec.validate() != nil || holdoutObserved != assessment.HoldoutOutcome.Observed ||
		validatePrivateSyntheticSamplingCardinality(assessment.Tier, assessment.PrimaryOutcome.Observed, assessment.HoldoutOutcome.Observed) != nil {
		return nil, privateSamplingError("assessment_contract")
	}
	if assessment.Tier == PrivateSamplingTierRegression {
		if assessment.RegressionAccepted == nil ||
			*assessment.RegressionAccepted != (privateSamplingAllPass(assessment.PrimaryOutcome) && privateSamplingAllPass(assessment.HoldoutOutcome)) {
			return nil, privateSamplingError("assessment_contract")
		}
	} else if assessment.RegressionAccepted != nil {
		return nil, privateSamplingError("assessment_contract")
	}
	data, err := json.MarshalIndent(assessment, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
