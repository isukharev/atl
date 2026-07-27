package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateSamplingSchemaVersion = 1
	PrivateSamplingConfirmation  = "ASSESS"

	PrivateSamplingTierCalibration = "calibration"
	PrivateSamplingTierRegression  = "regression"
	PrivateSamplingTierDecision    = "decision"

	privateSamplingMaxBytes = 4 << 20
	privateSamplingMaxRefs  = 64
)

var ErrPrivateSamplingRejected = errors.New("private sampling rejected")

type PrivateSamplingSpec struct {
	SchemaVersion int                    `json:"schema_version"`
	Tier          string                 `json:"tier"`
	Primary       []PrivateFindingRunRef `json:"primary"`
	Holdout       []PrivateFindingRunRef `json:"holdout,omitempty"`
}

type PrivateSamplingOptions struct {
	Root                     string
	RepositoryRoot           string
	Spec                     string
	ExpectedAssessmentSHA256 string
	Confirm                  string
}

type PrivateSamplingOutcome struct {
	Observed    int                             `json:"observed"`
	Statuses    PrivateFindingStatusCounts      `json:"statuses"`
	Eligibility PrivateFindingEligibilityCounts `json:"eligibility"`
}

type PrivateSamplingPreview struct {
	SchemaVersion      int                    `json:"schema_version"`
	Tier               string                 `json:"tier"`
	SourceSHA256       string                 `json:"source_sha256"`
	AssessmentSHA256   string                 `json:"assessment_sha256"`
	EvidenceReady      bool                   `json:"evidence_ready"`
	RegressionAccepted *bool                  `json:"regression_accepted,omitempty"`
	Primary            PrivateSamplingOutcome `json:"primary"`
	Holdout            PrivateSamplingOutcome `json:"holdout"`
}

type PrivateSamplingSummary struct {
	PrivateSamplingPreview
	Stored bool `json:"stored"`
}

type privateSamplingBinding struct {
	Reference      PrivateFindingRunRef `json:"reference"`
	PlanSHA256     string               `json:"plan_sha256"`
	ContractSHA256 string               `json:"contract_sha256"`
	RunID          string               `json:"run_id"`
	ResultSHA256   string               `json:"result_sha256"`
}

type privateSamplingAssessment struct {
	SchemaVersion      int                      `json:"schema_version"`
	Tier               string                   `json:"tier"`
	SourceSHA256       string                   `json:"source_sha256"`
	EvidenceReady      bool                     `json:"evidence_ready"`
	RegressionAccepted *bool                    `json:"regression_accepted,omitempty"`
	Primary            []privateSamplingBinding `json:"primary"`
	Holdout            []privateSamplingBinding `json:"holdout,omitempty"`
	PrimaryOutcome     PrivateSamplingOutcome   `json:"primary_outcome"`
	HoldoutOutcome     PrivateSamplingOutcome   `json:"holdout_outcome"`
}

type privateSamplingDependencies struct {
	doctor func(root, repository string) (PrivateWorkspaceReport, error)
	load   privateFindingSourceLoader
}

func defaultPrivateSamplingDependencies() privateSamplingDependencies {
	return privateSamplingDependencies{doctor: DoctorPrivateWorkspace, load: LoadCompletedPrivateRun}
}

func PreviewPrivateSampling(options PrivateSamplingOptions) (PrivateSamplingPreview, error) {
	preview, _, err := previewPrivateSampling(options, defaultPrivateSamplingDependencies())
	return preview, err
}

func previewPrivateSampling(options PrivateSamplingOptions, dependencies privateSamplingDependencies) (PrivateSamplingPreview, []byte, error) {
	// The rejections below are mixed: the branch also fires on an alias, report,
	// or mode the caller merely disagrees with. The in-hand error is passed
	// unguarded because codedError drops a nil cause.
	root, repository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil || !privateWorkspaceAliasRE.MatchString(options.Spec) {
		return PrivateSamplingPreview{}, nil, privateSamplingError("workspace", err)
	}
	report, err := dependencies.doctor(root, repository)
	if err != nil || !report.Healthy || report.SchemaVersion != 1 || report.Counts.ActiveRuns != 0 || report.State == "run_in_progress" {
		return PrivateSamplingPreview{}, nil, privateSamplingError("workspace_state", err)
	}
	specDirectory := filepath.Join(root, "cases", "sampling")
	if info, statErr := safepath.StatWithin(root, specDirectory); statErr != nil || !info.IsDir() ||
		(runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return PrivateSamplingPreview{}, nil, privateSamplingError("spec_directory", statErr)
	}
	specVersion, data, err := readPrivateSamplingSpec(root, specDirectory, options.Spec)
	if err != nil {
		return PrivateSamplingPreview{}, nil, err
	}
	if specVersion == PrivateSyntheticSamplingSchemaVersion {
		return previewPrivateSyntheticSampling(root, data)
	}
	spec, canonical, err := decodePrivateSamplingSpec(data)
	if err != nil || !bytes.Equal(data, canonical) {
		// Mixed again: a decodable but non-canonical spec is rejected by byte
		// comparison alone and leaves nil here.
		return PrivateSamplingPreview{}, nil, privateSamplingError("spec_contract", err)
	}
	assessment, err := buildPrivateSamplingAssessment(root, repository, spec, sha256HexBytes(canonical), dependencies.load)
	if err != nil {
		return PrivateSamplingPreview{}, nil, err
	}
	assessmentData, err := encodePrivateSamplingAssessment(assessment)
	if err != nil {
		return PrivateSamplingPreview{}, nil, privateSamplingError("assessment_contract", err)
	}
	digest := sha256HexBytes(append([]byte("atl-private-sampling-assessment-v1\x00"), assessmentData...))
	preview := PrivateSamplingPreview{SchemaVersion: PrivateSamplingSchemaVersion, Tier: assessment.Tier,
		SourceSHA256: assessment.SourceSHA256, AssessmentSHA256: digest, EvidenceReady: assessment.EvidenceReady,
		RegressionAccepted: assessment.RegressionAccepted, Primary: assessment.PrimaryOutcome, Holdout: assessment.HoldoutOutcome}
	return preview, assessmentData, nil
}

func readPrivateSamplingSpec(root, directory, alias string) (int, []byte, error) {
	return readPrivateSamplingSpecWithHook(root, directory, alias, nil)
}

func readPrivateSamplingSpecWithHook(root, directory, alias string, afterRead func()) (int, []byte, error) {
	directoryInfo, err := safepath.StatWithin(root, directory)
	if err != nil || !directoryInfo.IsDir() ||
		(runtime.GOOS != "windows" && directoryInfo.Mode().Perm() != 0o700) {
		return 0, nil, privateSamplingError("spec_directory", err)
	}
	handle, err := os.OpenRoot(directory)
	if err != nil {
		return 0, nil, privateSamplingError("spec_directory", err)
	}
	defer func() { _ = handle.Close() }()
	openedDirectory, err := handle.Stat(".")
	if err != nil || !openedDirectory.IsDir() || !os.SameFile(directoryInfo, openedDirectory) ||
		!sameSyntheticRootInfo(directoryInfo, openedDirectory) {
		return 0, nil, privateSamplingError("spec_directory", err)
	}
	legacyName, syntheticName := alias+".v1.json", alias+".v2.json"
	legacyBefore, legacyExists, err := privateSamplingSpecEntry(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	syntheticBefore, syntheticExists, err := privateSamplingSpecEntry(handle, syntheticName)
	if err != nil {
		return 0, nil, err
	}
	// Which version to read is decided by the observed pair alone, so an
	// ambiguous or absent pair carries no cause.
	if legacyExists == syntheticExists {
		if legacyExists {
			return 0, nil, privateSamplingError("spec_ambiguous")
		}
		return 0, nil, privateSamplingError("spec_file")
	}
	version, name, before := PrivateSamplingSchemaVersion, legacyName, legacyBefore
	if syntheticExists {
		version, name, before = PrivateSyntheticSamplingSchemaVersion, syntheticName, syntheticBefore
	}
	file, err := handle.Open(name)
	if err != nil {
		return 0, nil, privateSamplingError("spec_read", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		!sameSyntheticRootInfo(before, opened) {
		_ = file.Close()
		return 0, nil, privateSamplingError("spec_file", statErr)
	}
	data, readErr := ioReadAllLimit(file, privateSamplingMaxBytes)
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return 0, nil, privateSamplingError("spec_read", readErr)
	}
	// The recheck can fail on either probe, so both are attached in a fixed
	// order; the read failure above stays a separate classification.
	if finalErr != nil || closeErr != nil || !os.SameFile(opened, final) ||
		!sameSyntheticRootInfo(opened, final) || final.Size() != int64(len(data)) {
		return 0, nil, privateSamplingError("spec_file", finalErr, closeErr)
	}
	if afterRead != nil {
		afterRead()
	}
	legacyAfter, legacyStillExists, err := privateSamplingSpecEntry(handle, legacyName)
	if err != nil {
		return 0, nil, err
	}
	syntheticAfter, syntheticStillExists, err := privateSamplingSpecEntry(handle, syntheticName)
	if err != nil {
		return 0, nil, err
	}
	// This branch compares the re-observed entries against the entries the read
	// was bound to; a probe failure is already reported by the entry helper.
	if legacyExists != legacyStillExists || syntheticExists != syntheticStillExists ||
		legacyExists && (!os.SameFile(legacyBefore, legacyAfter) || !sameSyntheticRootInfo(legacyBefore, legacyAfter)) ||
		syntheticExists && (!os.SameFile(syntheticBefore, syntheticAfter) || !sameSyntheticRootInfo(syntheticBefore, syntheticAfter)) {
		return 0, nil, privateSamplingError("spec_file")
	}
	finalDirectory, err := handle.Stat(".")
	ambientDirectory, ambientErr := safepath.StatWithin(root, directory)
	if err != nil || ambientErr != nil ||
		!os.SameFile(openedDirectory, finalDirectory) || !sameSyntheticRootInfo(openedDirectory, finalDirectory) ||
		!os.SameFile(openedDirectory, ambientDirectory) || !sameSyntheticRootInfo(openedDirectory, ambientDirectory) {
		return 0, nil, privateSamplingError("spec_directory", err, ambientErr)
	}
	return version, data, nil
}

func privateSamplingSpecEntry(root *os.Root, name string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, false, privateSamplingError("spec_file", err)
	}
	return info, true, nil
}

func ApplyPrivateSampling(options PrivateSamplingOptions) (PrivateSamplingSummary, error) {
	return applyPrivateSampling(options, defaultPrivateSamplingDependencies())
}

func applyPrivateSampling(options PrivateSamplingOptions, dependencies privateSamplingDependencies) (PrivateSamplingSummary, error) {
	// The reviewed confirmation and digest are checked against the caller's own
	// input, so this rejection has nothing in hand to attach.
	if options.Confirm != PrivateSamplingConfirmation || !validSHA256(options.ExpectedAssessmentSHA256) {
		return PrivateSamplingSummary{}, privateSamplingError("confirmation")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateSamplingSummary{}, privateSamplingError("workspace", err)
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateSamplingSummary{}, privateSamplingError("workspace_busy", err)
	}
	defer func() { _ = lock.Unlock() }()
	preview, data, err := previewPrivateSampling(options, dependencies)
	if err != nil || preview.AssessmentSHA256 != options.ExpectedAssessmentSHA256 {
		return PrivateSamplingSummary{}, privateSamplingError("assessment_drift", err)
	}
	directory := filepath.Join(root, "reports", "sampling")
	if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
		return PrivateSamplingSummary{}, privateSamplingError("directory", err)
	}
	if info, statErr := safepath.StatWithin(root, directory); statErr != nil || !info.IsDir() ||
		(runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return PrivateSamplingSummary{}, privateSamplingError("directory_mode", statErr)
	}
	path := filepath.Join(directory, preview.AssessmentSHA256+".json")
	if existing, readErr := safepath.ReadFileWithinLimit(root, path, privateSamplingMaxBytes); readErr == nil {
		info, statErr := safepath.StatWithin(root, path)
		if statErr != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) || !bytes.Equal(existing, data) {
			return PrivateSamplingSummary{}, privateSamplingError("assessment_exists", statErr)
		}
		return PrivateSamplingSummary{PrivateSamplingPreview: preview, Stored: false}, nil
	} else if !os.IsNotExist(readErr) {
		return PrivateSamplingSummary{}, privateSamplingError("assessment_read", readErr)
	}
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivateSamplingSummary{}, privateSamplingError("assessment_write", err)
	}
	return PrivateSamplingSummary{PrivateSamplingPreview: preview, Stored: true}, nil
}

func decodePrivateSamplingSpec(data []byte) (PrivateSamplingSpec, []byte, error) {
	var spec PrivateSamplingSpec
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
	canonical, err := encodePrivateSamplingSpec(spec)
	return spec, canonical, err
}

func encodePrivateSamplingSpec(spec PrivateSamplingSpec) ([]byte, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	canonical, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

func (spec PrivateSamplingSpec) validate() error {
	if spec.SchemaVersion != PrivateSamplingSchemaVersion || len(spec.Primary) == 0 || len(spec.Primary) > privateSamplingMaxRefs || len(spec.Holdout) > privateSamplingMaxRefs {
		return fmt.Errorf("invalid sampling envelope")
	}
	switch spec.Tier {
	case PrivateSamplingTierCalibration:
		if len(spec.Primary) != 1 || len(spec.Holdout) != 0 {
			return fmt.Errorf("calibration requires one primary and no holdout")
		}
	case PrivateSamplingTierRegression:
		if len(spec.Primary) != 3 || len(spec.Holdout) < 1 {
			return fmt.Errorf("regression requires three primary and holdout evidence")
		}
	case PrivateSamplingTierDecision:
		if len(spec.Primary) < 10 || len(spec.Holdout) < 1 {
			return fmt.Errorf("decision requires at least ten primary and holdout evidence")
		}
	default:
		return fmt.Errorf("invalid sampling tier")
	}
	seenPlans := map[string]struct{}{}
	for _, group := range [][]PrivateFindingRunRef{spec.Primary, spec.Holdout} {
		previous := ""
		for _, ref := range group {
			key := privateFindingRefKey(ref)
			if !validPrivateFindingRef(ref) || key <= previous {
				return fmt.Errorf("invalid sampling reference order")
			}
			if _, exists := seenPlans[ref.PlanID]; exists {
				return fmt.Errorf("duplicate sampling observation")
			}
			seenPlans[ref.PlanID] = struct{}{}
			previous = key
		}
	}
	return nil
}

func buildPrivateSamplingAssessment(root, repository string, spec PrivateSamplingSpec, sourceSHA256 string,
	load privateFindingSourceLoader) (privateSamplingAssessment, error) {
	assessment, _, _, err := buildPrivateSamplingAssessmentEvidence(root, repository, spec, sourceSHA256, load)
	return assessment, err
}

func buildPrivateSamplingAssessmentEvidence(root, repository string, spec PrivateSamplingSpec, sourceSHA256 string,
	load privateFindingSourceLoader) (privateSamplingAssessment, []Result, []Result, error) {
	primaryBindings, primaryResults, err := resolvePrivateSamplingRefs(root, repository, spec.Primary, load)
	if err != nil {
		return privateSamplingAssessment{}, nil, nil, err
	}
	// Every rejection below is decided by comparing the resolved evidence to
	// itself, so none of them has a cause to retain.
	for index := 1; index < len(primaryResults); index++ {
		if !compatiblePrivateSamplingPrimary(primaryResults[0], primaryResults[index]) ||
			primaryBindings[0].ContractSHA256 != primaryBindings[index].ContractSHA256 {
			return privateSamplingAssessment{}, nil, nil, privateSamplingError("primary_incompatible")
		}
	}
	holdoutBindings, holdoutResults, err := resolvePrivateSamplingRefs(root, repository, spec.Holdout, load)
	if err != nil {
		return privateSamplingAssessment{}, nil, nil, err
	}
	seenPlanDigests := make(map[string]struct{}, len(primaryBindings)+len(holdoutBindings))
	seenRunIDs := make(map[string]struct{}, len(primaryBindings)+len(holdoutBindings))
	for _, binding := range append(append([]privateSamplingBinding{}, primaryBindings...), holdoutBindings...) {
		if _, exists := seenPlanDigests[binding.PlanSHA256]; exists {
			return privateSamplingAssessment{}, nil, nil, privateSamplingError("duplicate_observation")
		}
		if !privateRunIDRE.MatchString(binding.RunID) {
			return privateSamplingAssessment{}, nil, nil, privateSamplingError("run_identity")
		}
		if _, exists := seenRunIDs[binding.RunID]; exists {
			return privateSamplingAssessment{}, nil, nil, privateSamplingError("duplicate_observation")
		}
		seenPlanDigests[binding.PlanSHA256] = struct{}{}
		seenRunIDs[binding.RunID] = struct{}{}
	}
	for index, result := range holdoutResults {
		if holdoutBindings[index].ContractSHA256 == primaryBindings[0].ContractSHA256 ||
			!compatiblePrivateSamplingHoldout(primaryResults[0], result) {
			return privateSamplingAssessment{}, nil, nil, privateSamplingError("holdout_incompatible")
		}
	}
	primaryOutcome := privateSamplingOutcome(primaryResults)
	holdoutOutcome := privateSamplingOutcome(holdoutResults)
	assessment := privateSamplingAssessment{SchemaVersion: PrivateSamplingSchemaVersion, Tier: spec.Tier, SourceSHA256: sourceSHA256,
		EvidenceReady: true, Primary: primaryBindings, Holdout: holdoutBindings,
		PrimaryOutcome: primaryOutcome, HoldoutOutcome: holdoutOutcome}
	if spec.Tier == PrivateSamplingTierRegression {
		accepted := privateSamplingAllPass(primaryOutcome) && privateSamplingAllPass(holdoutOutcome)
		assessment.RegressionAccepted = &accepted
	}
	return assessment, primaryResults, holdoutResults, nil
}

func loadPrivateSamplingAssessment(root, repository, digest string, load privateFindingSourceLoader) (privateSamplingAssessment, []Result, []Result, error) {
	// The requested digest is validated on its own, with no probe in hand.
	if !validSHA256(digest) {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_digest")
	}
	directory := filepath.Join(root, "reports", "sampling")
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_directory", err)
	}
	path := filepath.Join(directory, digest+".json")
	info, err = safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_file", err)
	}
	data, err := safepath.ReadFileWithinLimit(root, path, privateSamplingMaxBytes)
	if err != nil {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_read", err)
	}
	var stored privateSamplingAssessment
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_decode", err)
	}
	// io.EOF is this probe's success signal, so only a real trailing-decode
	// failure is a cause; decodable trailing data rejects with none.
	var extra any
	trailingErr := decoder.Decode(&extra)
	if trailingErr != io.EOF {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_decode", trailingErr)
	}
	canonical, err := encodePrivateSamplingAssessment(stored)
	if err != nil || !bytes.Equal(data, canonical) ||
		sha256HexBytes(append([]byte("atl-private-sampling-assessment-v1\x00"), canonical...)) != digest {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_contract", err)
	}
	spec := PrivateSamplingSpec{SchemaVersion: PrivateSamplingSchemaVersion, Tier: stored.Tier,
		Primary: make([]PrivateFindingRunRef, 0, len(stored.Primary)), Holdout: make([]PrivateFindingRunRef, 0, len(stored.Holdout))}
	for _, binding := range stored.Primary {
		spec.Primary = append(spec.Primary, binding.Reference)
	}
	for _, binding := range stored.Holdout {
		spec.Holdout = append(spec.Holdout, binding.Reference)
	}
	specData, err := encodePrivateSamplingSpec(spec)
	// The stored envelope has already passed the same reference validation, so
	// err is nil for today's representation. Keep it attached defensively if
	// encoding gains another fallible step; codedError drops it on the ordinary
	// digest-comparison rejection.
	if err != nil || sha256HexBytes(specData) != stored.SourceSHA256 {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_source", err)
	}
	rebuilt, primary, holdout, err := buildPrivateSamplingAssessmentEvidence(root, repository, spec, stored.SourceSHA256, load)
	if err != nil {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_evidence", err)
	}
	rebuiltData, err := encodePrivateSamplingAssessment(rebuilt)
	if err != nil || !bytes.Equal(canonical, rebuiltData) {
		return privateSamplingAssessment{}, nil, nil, privateSamplingError("assessment_drift", err)
	}
	return stored, primary, holdout, nil
}

func compatiblePrivateSamplingPrimary(first, candidate Result) bool {
	return compatiblePrivateResults(first, candidate) && first.Runtime.ATLVersion == candidate.Runtime.ATLVersion &&
		first.Runtime.PluginVersion == candidate.Runtime.PluginVersion && first.Runtime.SkillDigest == candidate.Runtime.SkillDigest
}

func resolvePrivateSamplingRefs(root, repository string, refs []PrivateFindingRunRef,
	load privateFindingSourceLoader) ([]privateSamplingBinding, []Result, error) {
	bindings := make([]privateSamplingBinding, 0, len(refs))
	results := make([]Result, 0, len(refs))
	for _, ref := range refs {
		source, err := load(root, repository, ref.PlanID)
		if err != nil {
			return nil, nil, privateSamplingError("source", err)
		}
		result, resultSHA256, err := privateFindingBaselineResult(root, source, ref)
		if err != nil {
			return nil, nil, privateSamplingError("baseline", err)
		}
		// The task class is decided by set membership alone.
		if _, allowed := publicCorpusTaskClasses[result.TaskClass]; !allowed {
			return nil, nil, privateSamplingError("task_class")
		}
		bindings = append(bindings, privateSamplingBinding{Reference: ref, PlanSHA256: source.PlanSHA256,
			ContractSHA256: source.ContractSHA256, RunID: source.RunID, ResultSHA256: resultSHA256})
		results = append(results, result)
	}
	return bindings, results, nil
}

func compatiblePrivateSamplingHoldout(primary, holdout Result) bool {
	if primary.ScenarioID == holdout.ScenarioID || primary.TaskClass != holdout.TaskClass || primary.DataClass != holdout.DataClass ||
		primary.EffectiveCategory() != holdout.EffectiveCategory() || primary.Variant != holdout.Variant ||
		primary.EffectiveSurface() != holdout.EffectiveSurface() || primary.Runtime.Provider != holdout.Runtime.Provider ||
		primary.Runtime.AgentVersion != holdout.Runtime.AgentVersion || primary.Runtime.Model != holdout.Runtime.Model ||
		primary.Runtime.Reasoning != holdout.Runtime.Reasoning || primary.Runtime.ATLVersion != holdout.Runtime.ATLVersion ||
		primary.Runtime.PluginVersion != holdout.Runtime.PluginVersion || primary.Runtime.SkillDigest != holdout.Runtime.SkillDigest ||
		primary.Runtime.SkillActivation != holdout.Runtime.SkillActivation {
		return false
	}
	if primary.Runtime.PromptContractSHA256 != "" && primary.Runtime.PromptContractSHA256 == holdout.Runtime.PromptContractSHA256 {
		return false
	}
	return true
}

func privateSamplingOutcome(results []Result) PrivateSamplingOutcome {
	outcome := PrivateSamplingOutcome{Observed: len(results)}
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
	}
	return outcome
}

func privateSamplingAllPass(outcome PrivateSamplingOutcome) bool {
	return outcome.Observed > 0 && outcome.Statuses.Pass == outcome.Observed && outcome.Eligibility.Supported == outcome.Observed
}

// encodePrivateSamplingAssessment rejects on in-frame validation only, so every
// classification it returns is cause-free; callers attach it as their cause.
func encodePrivateSamplingAssessment(assessment privateSamplingAssessment) ([]byte, error) {
	if assessment.SchemaVersion != PrivateSamplingSchemaVersion || !validSHA256(assessment.SourceSHA256) || !assessment.EvidenceReady ||
		len(assessment.Primary) == 0 || assessment.PrimaryOutcome.Observed != len(assessment.Primary) ||
		assessment.HoldoutOutcome.Observed != len(assessment.Holdout) || !validPrivateSamplingOutcome(assessment.PrimaryOutcome) ||
		!validPrivateSamplingOutcome(assessment.HoldoutOutcome) {
		return nil, privateSamplingError("assessment_contract")
	}
	refs := PrivateSamplingSpec{SchemaVersion: PrivateSamplingSchemaVersion, Tier: assessment.Tier,
		Primary: make([]PrivateFindingRunRef, 0, len(assessment.Primary)), Holdout: make([]PrivateFindingRunRef, 0, len(assessment.Holdout))}
	for _, binding := range assessment.Primary {
		refs.Primary = append(refs.Primary, binding.Reference)
	}
	for _, binding := range assessment.Holdout {
		refs.Holdout = append(refs.Holdout, binding.Reference)
	}
	if refs.validate() != nil {
		return nil, privateSamplingError("assessment_contract")
	}
	seenPlanDigests := make(map[string]struct{}, len(assessment.Primary)+len(assessment.Holdout))
	seenRunIDs := make(map[string]struct{}, len(assessment.Primary)+len(assessment.Holdout))
	for index, binding := range append(append([]privateSamplingBinding{}, assessment.Primary...), assessment.Holdout...) {
		if !validPrivateFindingRef(binding.Reference) || !validSHA256(binding.PlanSHA256) || !privateRunIDRE.MatchString(binding.RunID) ||
			!validSHA256(binding.ContractSHA256) || !validSHA256(binding.ResultSHA256) {
			return nil, privateSamplingError("assessment_contract")
		}
		if _, exists := seenPlanDigests[binding.PlanSHA256]; exists {
			return nil, privateSamplingError("assessment_contract")
		}
		if _, exists := seenRunIDs[binding.RunID]; exists {
			return nil, privateSamplingError("assessment_contract")
		}
		seenPlanDigests[binding.PlanSHA256] = struct{}{}
		seenRunIDs[binding.RunID] = struct{}{}
		if (index < len(assessment.Primary) && binding.ContractSHA256 != assessment.Primary[0].ContractSHA256) ||
			(index >= len(assessment.Primary) && binding.ContractSHA256 == assessment.Primary[0].ContractSHA256) {
			return nil, privateSamplingError("assessment_contract")
		}
	}
	if assessment.Tier == PrivateSamplingTierRegression {
		if assessment.RegressionAccepted == nil || *assessment.RegressionAccepted !=
			(privateSamplingAllPass(assessment.PrimaryOutcome) && privateSamplingAllPass(assessment.HoldoutOutcome)) {
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

func validPrivateSamplingOutcome(outcome PrivateSamplingOutcome) bool {
	statuses := outcome.Statuses.Pass + outcome.Statuses.Fail + outcome.Statuses.Ineligible
	eligibility := outcome.Eligibility.Supported + outcome.Eligibility.UnsupportedCapability + outcome.Eligibility.InvalidatedBackendDrift
	return outcome.Observed >= 0 && outcome.Statuses.Pass >= 0 && outcome.Statuses.Fail >= 0 && outcome.Statuses.Ineligible >= 0 &&
		outcome.Eligibility.Supported >= 0 && outcome.Eligibility.UnsupportedCapability >= 0 &&
		outcome.Eligibility.InvalidatedBackendDrift >= 0 && statuses == outcome.Observed && eligibility == outcome.Observed
}

// privateSamplingError classifies a sampling rejection under the shared
// sentinel and its stable short code while retaining the causes already in
// hand. The rendered message stays sentinel plus code, so a configured private
// path, a selected digest, or spec content cannot reach a log line through the
// error string.
func privateSamplingError(code string, causes ...error) error {
	return codedError(ErrPrivateSamplingRejected, code, causes...)
}
