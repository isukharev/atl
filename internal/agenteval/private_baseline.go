package agenteval

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateBaselineSchemaVersion = 1
	PrivateBaselineConfirmation  = "BASELINE"
	PrivatePruneConfirmation     = "PRUNE"

	privateWorkspaceLockPath = ".ephemeral/workspace.lock"
	privateBaselineMaxBytes  = 128 << 20
	privateBaselineMaxFiles  = 256
)

var (
	ErrPrivateBaselineRejected = errors.New("private baseline rejected")
	ErrPrivatePruneRejected    = errors.New("private prune rejected")
	privatePlanIDRE            = regexp.MustCompile(`^pln-[0-9a-f]{32}$`)
	privateRunIDRE             = regexp.MustCompile(`^run-[0-9a-f]{32}$`)
)

// PrivateBaselineSource is the narrow handoff from the private-plan layer.
// The plan loader, not this package, proves plan/state-schema validity and
// completion. This layer independently rechecks containment, hashes, result
// contracts, and retained artifacts before publishing an immutable baseline.
type PrivateBaselineSource struct {
	PlanID         string
	PlanPath       string
	PlanSHA256     string
	ContractSHA256 string
	RunID          string
	RunRoot        string
	Completed      bool
	Immutable      bool
	Surfaces       []PrivateBaselineSurfaceSource
}

type PrivateBaselineSurfaceSource struct {
	Surface                        string
	RunDirectory                   string
	RubricPath                     string
	RubricSHA256                   string
	QualitativeRequired            bool
	QualitativePanelContractPath   string
	QualitativePanelContractSHA256 string
	BlindAssignmentPath            string
	BlindAssignmentSHA256          string
}

type PrivateBaselineSetOptions struct {
	Root           string
	RepositoryRoot string
	Baseline       string
	Confirm        string
	Source         PrivateBaselineSource
}

type PrivateBaselineSummary struct {
	SchemaVersion int      `json:"schema_version"`
	Stored        bool     `json:"stored"`
	Surfaces      []string `json:"surfaces"`
	ArtifactFiles int      `json:"artifact_files"`
	ArtifactBytes int64    `json:"artifact_bytes"`
	TreeSHA256    string   `json:"tree_sha256"`
}

type privateBaselineManifest struct {
	SchemaVersion  int                      `json:"schema_version"`
	Baseline       string                   `json:"baseline"`
	ContractSHA256 string                   `json:"contract_sha256"`
	PlanSHA256     string                   `json:"plan_sha256"`
	TreeSHA256     string                   `json:"tree_sha256"`
	Surfaces       []privateBaselineSurface `json:"surfaces"`
}

type privateBaselineSurface struct {
	Surface             string `json:"surface"`
	ResultPath          string `json:"result_path"`
	ResultSHA256        string `json:"result_sha256"`
	QualitativeRequired bool   `json:"qualitative_required"`
}

type privateBaselinePointer struct {
	SchemaVersion  int    `json:"schema_version"`
	Baseline       string `json:"baseline"`
	ContractSHA256 string `json:"contract_sha256"`
	TreeSHA256     string `json:"tree_sha256"`
}

type PrivateComparison struct {
	SchemaVersion int                   `json:"schema_version"`
	Compatible    bool                  `json:"compatible"`
	TaskClass     string                `json:"task_class"`
	Surfaces      []PrivateSurfaceDelta `json:"surfaces"`
}

type PrivateSurfaceDelta struct {
	Surface                  string               `json:"surface"`
	BaselineStatus           string               `json:"baseline_status"`
	CandidateStatus          string               `json:"candidate_status"`
	BaselineEligibility      string               `json:"baseline_eligibility"`
	CandidateEligibility     string               `json:"candidate_eligibility"`
	QualitativeScoreBPSDelta *int                 `json:"qualitative_score_bps_delta,omitempty"`
	Metrics                  []PrivateMetricDelta `json:"metrics"`
}

type PrivateMetricDelta struct {
	Metric    string `json:"metric"`
	Baseline  int64  `json:"baseline"`
	Candidate int64  `json:"candidate"`
	Delta     int64  `json:"delta"`
}

type PrivateCompareOptions struct {
	Root           string
	RepositoryRoot string
	Baseline       string
	Candidate      PrivateBaselineSource
}

type PrivateRunLifecycle struct {
	RunID          string
	RunSetAlias    string
	PlanID         string
	State          string
	CompletedOrder int64
}

type PrivatePruneInventory struct {
	Runs []PrivateRunLifecycle
}

// PrivatePruneInventoryLoader is implemented by the private-plan layer. It
// returns a schema-validated, privacy-safe lifecycle view; prune never decodes
// plan or state files independently.
type PrivatePruneInventoryLoader func(root string) (PrivatePruneInventory, error)

type PrivatePruneOptions struct {
	Root                    string
	RepositoryRoot          string
	Inventory               PrivatePruneInventoryLoader
	ExpectedInventorySHA256 string
	Confirm                 string
	Now                     time.Time
}

type PrivatePrunePreview struct {
	SchemaVersion   int    `json:"schema_version"`
	EligibleRunSets int    `json:"eligible_run_sets"`
	EligibleFiles   int    `json:"eligible_files"`
	EligibleBytes   int64  `json:"eligible_bytes"`
	InventorySHA256 string `json:"inventory_sha256"`
}

type PrivatePruneSummary struct {
	SchemaVersion int   `json:"schema_version"`
	PrunedRunSets int   `json:"pruned_run_sets"`
	RemovedFiles  int   `json:"removed_files"`
	RemovedBytes  int64 `json:"removed_bytes"`
}

type privatePruneCandidate struct {
	runID  string
	planID string
	path   string
	hash   string
	files  int
	bytes  int64
}

type privatePrunedRun struct {
	SchemaVersion      int    `json:"schema_version"`
	RunID              string `json:"run_id"`
	PlanID             string `json:"plan_id"`
	OriginalTreeSHA256 string `json:"original_tree_sha256"`
}

type privatePruneIntent struct {
	SchemaVersion      int    `json:"schema_version"`
	RunID              string `json:"run_id"`
	PlanID             string `json:"plan_id"`
	OriginalTreeSHA256 string `json:"original_tree_sha256"`
	InventorySHA256    string `json:"inventory_sha256"`
}

const privatePrunedRunName = "pruned.v1.json"

func SetPrivateBaseline(options PrivateBaselineSetOptions) (PrivateBaselineSummary, error) {
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("workspace")
	}
	if options.Confirm != PrivateBaselineConfirmation {
		return PrivateBaselineSummary{}, privateBaselineError("confirmation")
	}
	if options.Baseline == "current" || !privateWorkspaceAliasRE.MatchString(options.Baseline) || !validPrivateBaselineSource(root, options.Source) {
		return PrivateBaselineSummary{}, privateBaselineError("source")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateBaselineSummary{}, err
	}
	defer func() { _ = lock.Unlock() }()
	if !validPrivateBaselineSource(root, options.Source) {
		return PrivateBaselineSummary{}, privateBaselineError("source_drift")
	}
	workspaceManifestData, err := readPrivatePlanLifecycleFile(root, filepath.Join(root, PrivateWorkspaceManifestName), maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("manifest")
	}
	workspaceManifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(workspaceManifestData))
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("manifest")
	}

	planData, err := readPrivatePlanLifecycleFile(root, options.Source.PlanPath, 1<<20)
	if err != nil || sha256HexBytes(planData) != options.Source.PlanSHA256 {
		return PrivateBaselineSummary{}, privateBaselineError("plan_drift")
	}
	stagingName, err := privateRandomID("baseline-stage-")
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("staging")
	}
	staging := filepath.Join(root, ".ephemeral", stagingName)
	if err := safepath.MkdirAllWithin(root, staging, 0o700); err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("staging")
	}
	committed := false
	defer func() {
		if !committed {
			_ = removePrivateTree(root, staging)
		}
	}()
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(staging, "plan.json"), planData, 0o600); err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("plan_copy")
	}

	surfaces := append([]PrivateBaselineSurfaceSource(nil), options.Source.Surfaces...)
	sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].Surface < surfaces[j].Surface })
	manifest := privateBaselineManifest{
		SchemaVersion: PrivateBaselineSchemaVersion, Baseline: options.Baseline,
		ContractSHA256: options.Source.ContractSHA256, PlanSHA256: options.Source.PlanSHA256,
		Surfaces: make([]privateBaselineSurface, 0, len(surfaces)),
	}
	for _, surface := range surfaces {
		stored, err := compactPrivateSurface(root, staging, surface, workspaceManifest.Retention.RetainBaselineTranscripts)
		if err != nil {
			return PrivateBaselineSummary{}, err
		}
		manifest.Surfaces = append(manifest.Surfaces, stored)
	}
	treeHash, files, size, err := hashPrivateTree(staging, "")
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("tree_hash")
	}
	manifest.TreeSHA256 = treeHash
	manifestData, err := encodePrivateBaselineManifest(manifest)
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("manifest")
	}
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(staging, "baseline.v1.json"), manifestData, 0o600); err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("manifest_write")
	}

	contractDirectory := filepath.Join(root, "baselines", options.Source.ContractSHA256)
	if err := safepath.MkdirAllWithin(root, contractDirectory, 0o700); err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("baseline_parent")
	}
	destination := filepath.Join(contractDirectory, options.Baseline)
	pointer := privateBaselinePointer{
		SchemaVersion: PrivateBaselineSchemaVersion, Baseline: options.Baseline,
		ContractSHA256: options.Source.ContractSHA256, TreeSHA256: treeHash,
	}
	pointerData, err := encodePrivateBaselinePointer(pointer)
	if err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("current_pointer")
	}
	surfaceNames := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		surfaceNames = append(surfaceNames, surface.Surface)
	}
	if _, err := os.Lstat(destination); err == nil {
		if _, pointerErr := os.Lstat(filepath.Join(contractDirectory, "current.json")); pointerErr == nil || !os.IsNotExist(pointerErr) {
			return PrivateBaselineSummary{}, privateBaselineError("baseline_exists")
		}
		if !recoverPrivateBaselinePointer(root, destination, manifest, pointerData, filepath.Join(contractDirectory, "current.json")) {
			return PrivateBaselineSummary{}, privateBaselineError("baseline_exists")
		}
		return PrivateBaselineSummary{SchemaVersion: 1, Stored: true, Surfaces: surfaceNames,
			ArtifactFiles: files, ArtifactBytes: size, TreeSHA256: treeHash}, nil
	} else if !os.IsNotExist(err) {
		return PrivateBaselineSummary{}, privateBaselineError("baseline_exists")
	}
	if err := safepath.RenameWithin(root, staging, destination); err != nil {
		return PrivateBaselineSummary{}, privateBaselineError("baseline_commit")
	}
	committed = true
	if safepath.WriteFileWithin(root, filepath.Join(contractDirectory, "current.json"), pointerData, 0o600) != nil {
		return PrivateBaselineSummary{}, privateBaselineError("current_pointer")
	}
	return PrivateBaselineSummary{
		SchemaVersion: 1, Stored: true, Surfaces: surfaceNames,
		ArtifactFiles: files, ArtifactBytes: size, TreeSHA256: treeHash,
	}, nil
}

func recoverPrivateBaselinePointer(root, destination string, expected privateBaselineManifest, pointerData []byte, pointerPath string) bool {
	data, err := safepath.ReadFileWithinLimit(root, filepath.Join(destination, "baseline.v1.json"), maxContractBytes)
	if err != nil {
		return false
	}
	stored, err := decodePrivateBaselineManifest(data)
	if err != nil || stored.Baseline != expected.Baseline || stored.ContractSHA256 != expected.ContractSHA256 ||
		stored.PlanSHA256 != expected.PlanSHA256 || stored.TreeSHA256 != expected.TreeSHA256 {
		return false
	}
	hash, _, _, err := hashPrivateTree(destination, "baseline.v1.json")
	if err != nil || hash != expected.TreeSHA256 {
		return false
	}
	return safepath.WriteFileWithin(root, pointerPath, pointerData, 0o600) == nil
}

func validPrivateBaselineSource(root string, source PrivateBaselineSource) bool {
	if !source.Completed || !source.Immutable || !privatePlanIDRE.MatchString(source.PlanID) ||
		!privateRunIDRE.MatchString(source.RunID) || !validSHA256(source.PlanSHA256) ||
		!validSHA256(source.ContractSHA256) || len(source.Surfaces) < 1 || len(source.Surfaces) > 3 {
		return false
	}
	if !privatePathWithin(root, filepath.Join(root, "plans"), source.PlanPath) ||
		!privatePathWithin(root, filepath.Join(root, "runs", source.RunID), source.RunRoot) {
		return false
	}
	seen := map[string]struct{}{}
	for _, surface := range source.Surfaces {
		if !validRunSurface(surface.Surface) || !privatePathWithin(root, source.RunRoot, surface.RunDirectory) ||
			!privatePathWithin(root, filepath.Join(source.RunRoot, "contracts", surface.Surface), surface.RubricPath) || !validSHA256(surface.RubricSHA256) {
			return false
		}
		contractRoot := filepath.Join(source.RunRoot, "contracts", surface.Surface)
		if surface.QualitativePanelContractPath != "" {
			if !privatePathWithin(root, contractRoot, surface.QualitativePanelContractPath) || !validSHA256(surface.QualitativePanelContractSHA256) {
				return false
			}
			if (surface.BlindAssignmentPath == "") != (surface.BlindAssignmentSHA256 == "") {
				return false
			}
			if surface.BlindAssignmentPath != "" && (!privatePathWithin(root, contractRoot, surface.BlindAssignmentPath) || !validSHA256(surface.BlindAssignmentSHA256)) {
				return false
			}
		} else if surface.QualitativePanelContractSHA256 != "" || surface.BlindAssignmentPath != "" || surface.BlindAssignmentSHA256 != "" {
			return false
		}
		if _, exists := seen[surface.Surface]; exists {
			return false
		}
		seen[surface.Surface] = struct{}{}
	}
	return true
}

func privatePathWithin(workspaceRoot, parent, target string) bool {
	rootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	parentInside, err := pathWithin(rootAbs, parentAbs)
	if err != nil || !parentInside {
		return false
	}
	targetInside, err := pathWithin(parentAbs, targetAbs)
	return err == nil && targetInside
}

func compactPrivateSurface(root, staging string, source PrivateBaselineSurfaceSource, retainTranscript bool) (privateBaselineSurface, error) {
	resultPath := filepath.Join(source.RunDirectory, "result.json")
	resultData, err := safepath.ReadFileWithinLimit(root, resultPath, maxContractBytes)
	if err != nil {
		return privateBaselineSurface{}, privateBaselineError("result_missing")
	}
	result, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil || result.DataClass != "private-local" || result.EffectiveSurface() != source.Surface {
		return privateBaselineSurface{}, privateBaselineError("result_invalid")
	}
	assessedPath, assessedData, assessed, err := findPrivateAssessedResult(root, source.RunDirectory)
	if err != nil {
		return privateBaselineSurface{}, err
	}
	effectiveResult, effectiveData, effectiveName := result, resultData, "result.json"
	if assessedPath != "" {
		if !samePrivateResultIdentity(result, assessed) || !hasPrivateQualitativeAssessment(assessed) ||
			validatePrivateAssessmentBinding(root, source, resultData, assessed) != nil {
			return privateBaselineSurface{}, privateBaselineError("assessment_invalid")
		}
		if assessed.QualitativeReviewSet != nil && assessed.QualitativeReviewSet.Status == "disagreement" {
			return privateBaselineSurface{}, privateBaselineError("assessment_disagreement")
		}
		effectiveResult, effectiveData, effectiveName = assessed, assessedData, "reviewed-result.json"
	}
	if privateSurfaceRequiresQualitative(source) && !hasPrivateQualitativeAssessment(effectiveResult) {
		return privateBaselineSurface{}, privateBaselineError("assessment_missing")
	}
	if source.QualitativePanelContractPath != "" && effectiveResult.QualitativeReviewSet == nil {
		return privateBaselineSurface{}, privateBaselineError("assessment_missing")
	}
	if source.QualitativePanelContractPath == "" && effectiveResult.QualitativeReviewSet != nil {
		return privateBaselineSurface{}, privateBaselineError("assessment_invalid")
	}
	destinationRoot := filepath.Join(staging, "surfaces", source.Surface)
	if err := safepath.MkdirAllWithin(root, destinationRoot, 0o700); err != nil {
		return privateBaselineSurface{}, privateBaselineError("surface_directory")
	}
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(destinationRoot, "result.json"), resultData, 0o600); err != nil {
		return privateBaselineSurface{}, privateBaselineError("result_copy")
	}
	if assessedPath != "" {
		if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(destinationRoot, "reviewed-result.json"), assessedData, 0o600); err != nil {
			return privateBaselineSurface{}, privateBaselineError("assessment_copy")
		}
	}
	for _, artifact := range []struct {
		name     string
		limit    int64
		optional bool
		nonempty bool
		retain   bool
	}{
		{"final.json", 16 << 20, false, true, true},
		{"transcript.jsonl", 64 << 20, false, true, retainTranscript},
		{"agent.stderr", 4 << 20, true, true, true},
	} {
		if !artifact.retain {
			data, err := safepath.ReadFileWithinLimit(root, filepath.Join(source.RunDirectory, artifact.name), artifact.limit)
			if err != nil || artifact.nonempty && len(data) == 0 {
				return privateBaselineSurface{}, privateBaselineError("artifact_read")
			}
			continue
		}
		if err := copyPrivateArtifact(root, filepath.Join(source.RunDirectory, artifact.name), destinationRoot, artifact.name, artifact.limit, artifact.optional); err != nil {
			if artifact.optional && os.IsNotExist(err) {
				continue
			}
			return privateBaselineSurface{}, privateBaselineError("artifact_copy")
		}
		if artifact.nonempty {
			path := filepath.Join(destinationRoot, artifact.name)
			info, err := safepath.StatWithin(root, path)
			if err != nil {
				if artifact.optional && os.IsNotExist(err) {
					continue
				}
				return privateBaselineSurface{}, privateBaselineError("artifact_stat")
			}
			if info.Size() == 0 {
				if artifact.optional {
					_ = safepath.RemoveWithin(root, path)
				} else {
					return privateBaselineSurface{}, privateBaselineError("artifact_empty")
				}
			}
		}
	}
	if err := copyPrivateAudits(root, source.RunDirectory, destinationRoot); err != nil {
		return privateBaselineSurface{}, err
	}
	return privateBaselineSurface{
		Surface: source.Surface, ResultPath: filepath.ToSlash(filepath.Join("surfaces", source.Surface, effectiveName)),
		ResultSHA256: sha256HexBytes(effectiveData), QualitativeRequired: source.QualitativeRequired,
	}, nil
}

func findPrivateAssessedResult(root, runDirectory string) (string, []byte, Result, error) {
	var found string
	for _, name := range []string{"reviewed-result.json", "assessed-result.json"} {
		path := filepath.Join(runDirectory, name)
		info, err := safepath.StatWithin(root, path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return "", nil, Result{}, privateBaselineError("assessment_stat")
		}
		if found != "" {
			return "", nil, Result{}, privateBaselineError("assessment_ambiguous")
		}
		found = path
	}
	if found == "" {
		return "", nil, Result{}, nil
	}
	data, err := safepath.ReadFileWithinLimit(root, found, maxContractBytes)
	if err != nil {
		return "", nil, Result{}, privateBaselineError("assessment_read")
	}
	result, err := DecodeResult(bytes.NewReader(data))
	if err != nil {
		return "", nil, Result{}, privateBaselineError("assessment_invalid")
	}
	return found, data, result, nil
}

func samePrivateResultIdentity(left, right Result) bool {
	return left.ScenarioID == right.ScenarioID && left.TaskClass == right.TaskClass &&
		left.DataClass == right.DataClass && left.EffectiveCategory() == right.EffectiveCategory() &&
		left.Variant == right.Variant && left.EffectiveSurface() == right.EffectiveSurface() &&
		left.Runtime == right.Runtime
}

func validatePrivateAssessmentBinding(root string, source PrivateBaselineSurfaceSource, resultData []byte, assessed Result) error {
	if !hasPrivateQualitativeAssessment(assessed) || !privatePathWithin(root, filepath.Join(root, "runs"), source.RubricPath) {
		return privateBaselineError("assessment_binding")
	}
	finalData, err := safepath.ReadFileWithinLimit(root, filepath.Join(source.RunDirectory, "final.json"), 16<<20)
	if err != nil {
		return privateBaselineError("assessment_binding")
	}
	rubricData, err := safepath.ReadFileWithinLimit(root, source.RubricPath, maxReviewBytes)
	if err != nil {
		return privateBaselineError("assessment_binding")
	}
	rubric, err := DecodeRubric(bytes.NewReader(rubricData))
	if err != nil || rubricSHA256(rubric) != source.RubricSHA256 {
		return privateBaselineError("assessment_binding")
	}
	original, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil {
		return privateBaselineError("assessment_binding")
	}
	var recomputed Result
	if assessed.Qualitative != nil {
		assessment := assessed.Qualitative
		if source.QualitativePanelContractPath != "" || assessment.ResultSHA256 != sha256HexBytes(resultData) || assessment.FinalResponseSHA256 != sha256HexBytes(finalData) || assessment.RubricID != rubric.ID || assessment.RubricSHA256 != rubricSHA256(rubric) {
			return privateBaselineError("assessment_binding")
		}
		review := privateReviewFromAssessment(*assessment)
		review.ScenarioID = original.ScenarioID
		recomputed, err = AssessQualitative(original, resultData, finalData, rubric, review)
	} else {
		set := assessed.QualitativeReviewSet
		if source.QualitativePanelContractPath == "" || set.ResultSHA256 != sha256HexBytes(resultData) || set.FinalResponseSHA256 != sha256HexBytes(finalData) || set.RubricID != rubric.ID || set.RubricSHA256 != rubricSHA256(rubric) {
			return privateBaselineError("assessment_binding")
		}
		contract, _, policy, loadErr := loadPrivatePanelReviewContract(root, source)
		if loadErr != nil || policy != set.Policy || len(contract.Reviewers) != len(set.Members) {
			return privateBaselineError("assessment_binding")
		}
		expectedContract, contractErr := QualitativeReviewSetContractSHA256(policy, contract.Reviewers)
		if contractErr != nil || set.ContractSHA256 != expectedContract || set.AssignmentDigest != contract.BlindAssignmentSHA256 || set.Blinded != (contract.BlindAssignmentSHA256 != "") {
			return privateBaselineError("assessment_binding")
		}
		expectedReviewers := append([]Reviewer(nil), contract.Reviewers...)
		sort.Slice(expectedReviewers, func(i, j int) bool { return expectedReviewers[i].ID < expectedReviewers[j].ID })
		for index, reviewer := range expectedReviewers {
			if reviewer != set.Members[index].Reviewer {
				return privateBaselineError("assessment_binding")
			}
		}
		reviews := make([]Review, 0, len(set.Members))
		for _, member := range set.Members {
			reviews = append(reviews, Review{SchemaVersion: ReviewSchemaVersion, RubricID: set.RubricID, RubricSHA256: set.RubricSHA256,
				ScenarioID: original.ScenarioID, ResultSHA256: set.ResultSHA256, FinalResponseSHA256: set.FinalResponseSHA256,
				Blinded: set.Blinded, AssignmentDigest: set.AssignmentDigest, Reviewer: member.Reviewer,
				Criteria: append([]ReviewCriterionScore{}, member.Criteria...), FindingIDs: append([]string{}, member.FindingIDs...)})
		}
		recomputed, err = AssessQualitativeReviewSet(original, resultData, finalData, rubric, policy, reviews)
	}
	if err != nil || !equalPrivateResultJSON(recomputed, assessed) {
		return privateBaselineError("assessment_binding")
	}
	return nil
}

func privateSurfaceRequiresQualitative(source PrivateBaselineSurfaceSource) bool {
	return source.QualitativeRequired || source.QualitativePanelContractPath != ""
}

func hasPrivateQualitativeAssessment(result Result) bool {
	return (result.Qualitative != nil) != (result.QualitativeReviewSet != nil)
}

func privateReviewFromAssessment(assessment QualitativeAssessment) Review {
	criteria := make([]ReviewCriterionScore, 0, len(assessment.Criteria))
	for _, item := range assessment.Criteria {
		criteria = append(criteria, ReviewCriterionScore{ID: item.ID, Score: item.Score})
	}
	return Review{SchemaVersion: ReviewSchemaVersion, RubricID: assessment.RubricID, RubricSHA256: assessment.RubricSHA256,
		ResultSHA256: assessment.ResultSHA256, FinalResponseSHA256: assessment.FinalResponseSHA256,
		Blinded: assessment.Blinded, AssignmentDigest: assessment.AssignmentDigest, Reviewer: assessment.Reviewer,
		Criteria: criteria, FindingIDs: append([]string{}, assessment.FindingIDs...)}
}

func equalPrivateResultJSON(left, right Result) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func copyPrivateArtifact(root, source, destinationRoot, name string, limit int64, optional bool) error {
	data, err := safepath.ReadFileWithinLimit(root, source, limit)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return err
		}
		return err
	}
	return safepath.WriteFileExclusiveWithin(root, filepath.Join(destinationRoot, name), data, 0o600)
}

var privateAuditFiles = []string{
	"atl-invocations.jsonl", "external-mcp-audit.jsonl", "gateway-audit.jsonl",
	"guard-decisions.jsonl", "http-methods.jsonl",
}

func copyPrivateAudits(root, runDirectory, destinationRoot string) error {
	auditDestination := filepath.Join(destinationRoot, "audit")
	written := false
	for _, name := range privateAuditFiles {
		source := filepath.Join(runDirectory, ".atl-eval", name)
		data, err := safepath.ReadFileWithinLimit(root, source, 4<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return privateBaselineError("audit_read")
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		sanitized, err := sanitizePrivateAudit(name, source, data)
		if err != nil {
			return privateBaselineError("audit_invalid")
		}
		if !written {
			if err := safepath.MkdirAllWithin(root, auditDestination, 0o700); err != nil {
				return privateBaselineError("audit_directory")
			}
			written = true
		}
		if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(auditDestination, name), sanitized, 0o600); err != nil {
			return privateBaselineError("audit_copy")
		}
	}
	return nil
}

func sanitizePrivateAudit(name, path string, data []byte) ([]byte, error) {
	switch name {
	case "atl-invocations.jsonl":
		records, err := readProxyRecords(path)
		if err != nil {
			return nil, err
		}
		type safeRecord struct {
			Denied      bool  `json:"denied,omitempty"`
			StdoutBytes int64 `json:"stdout_bytes"`
			StderrBytes int64 `json:"stderr_bytes"`
			ExitCode    int   `json:"exit_code"`
		}
		values := make([]safeRecord, 0, len(records))
		for _, record := range records {
			if record.StdoutBytes < 0 || record.StderrBytes < 0 {
				return nil, privateBaselineError("audit_record")
			}
			values = append(values, safeRecord{Denied: record.Denied, StdoutBytes: record.StdoutBytes, StderrBytes: record.StderrBytes, ExitCode: record.ExitCode})
		}
		return encodePrivateAuditLines(values)
	case "guard-decisions.jsonl":
		if _, err := countGuardDenials(path); err != nil {
			return nil, err
		}
		return normalizePrivateJSONLines[guardDecisionRecord](data, func(record guardDecisionRecord) (guardDecisionRecord, bool) {
			return record, record.Decision == "allow" || record.Decision == "deny"
		})
	case "http-methods.jsonl":
		if _, _, _, err := readLiveHTTPRecords(path); err != nil {
			return nil, err
		}
		return normalizePrivateJSONLines[liveHTTPRecord](data, func(record liveHTTPRecord) (liveHTTPRecord, bool) { return record, true })
	case "gateway-audit.jsonl":
		if _, _, _, err := readLiveGatewayRecords(path); err != nil {
			return nil, err
		}
		type safeRecord struct {
			Sequence      int64  `json:"sequence"`
			Phase         string `json:"phase"`
			Service       string `json:"service"`
			Method        string `json:"method"`
			RequestHMAC   string `json:"request_hmac"`
			Decision      string `json:"decision"`
			StatusClass   string `json:"status_class,omitempty"`
			ResponseBytes int64  `json:"response_bytes,omitempty"`
			DurationMS    int64  `json:"duration_ms,omitempty"`
		}
		return normalizePrivateJSONLines[LiveGatewayAuditRecord](data, func(record LiveGatewayAuditRecord) (safeRecord, bool) {
			return safeRecord{Sequence: record.Sequence, Phase: record.Phase, Service: record.Service, Method: record.Method, RequestHMAC: record.RequestHMAC, Decision: record.Decision, StatusClass: record.StatusClass, ResponseBytes: record.ResponseBytes, DurationMS: record.DurationMS}, true
		})
	case "external-mcp-audit.jsonl":
		if _, _, _, _, _, err := readExternalMCPAudit(path); err != nil {
			return nil, err
		}
		return normalizePrivateJSONLines[ExternalMCPAuditRecord](data, func(record ExternalMCPAuditRecord) (ExternalMCPAuditRecord, bool) {
			_, safe := externalMCPReadCapabilityFamilies[record.Capability]
			return record, record.Capability == "" || safe
		})
	default:
		return nil, privateBaselineError("audit_name")
	}
}

func normalizePrivateJSONLines[Input any, Output any](data []byte, normalize func(Input) (Output, bool)) ([]byte, error) {
	var output bytes.Buffer
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var input Input
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, privateBaselineError("audit_decode")
		}
		value, ok := normalize(input)
		if !ok {
			return nil, privateBaselineError("audit_record")
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, privateBaselineError("audit_encode")
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func encodePrivateAuditLines[T any](values []T) ([]byte, error) {
	var output bytes.Buffer
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func PreviewPrivatePrune(options PrivatePruneOptions) (PrivatePrunePreview, error) {
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivatePrunePreview{}, privatePruneError("workspace")
	}
	loader := options.Inventory
	if loader == nil {
		loader = privatePlanPruneInventoryLoader(options.RepositoryRoot)
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivatePrunePreview{}, privatePruneError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	return buildPrivatePrunePreview(root, loader, privatePruneNow(options.Now))
}

func ApplyPrivatePrune(options PrivatePruneOptions) (PrivatePruneSummary, error) {
	if options.Confirm != PrivatePruneConfirmation || !validSHA256(options.ExpectedInventorySHA256) {
		return PrivatePruneSummary{}, privatePruneError("confirmation")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivatePruneSummary{}, privatePruneError("workspace")
	}
	loader := options.Inventory
	if loader == nil {
		loader = privatePlanPruneInventoryLoader(options.RepositoryRoot)
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivatePruneSummary{}, privatePruneError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	if err := recoverPrivatePruneTransactions(root); err != nil {
		return PrivatePruneSummary{}, privatePruneError("recovery")
	}
	preview, candidates, err := privatePruneInventory(root, loader, privatePruneNow(options.Now))
	if err != nil {
		return PrivatePruneSummary{}, err
	}
	if preview.InventorySHA256 != options.ExpectedInventorySHA256 {
		return PrivatePruneSummary{}, privatePruneError("stale_plan")
	}
	for _, candidate := range candidates {
		if err := compactPrivatePrunedRun(root, candidate, preview.InventorySHA256); err != nil {
			return PrivatePruneSummary{}, privatePruneError("remove")
		}
	}
	return PrivatePruneSummary{SchemaVersion: 1, PrunedRunSets: len(candidates), RemovedFiles: preview.EligibleFiles, RemovedBytes: preview.EligibleBytes}, nil
}

func privatePlanPruneInventoryLoader(repository string) PrivatePruneInventoryLoader {
	return func(root string) (PrivatePruneInventory, error) {
		references, err := InspectPrivatePlanRunReferences(root, repository)
		if err != nil {
			return PrivatePruneInventory{}, err
		}
		inventory := PrivatePruneInventory{Runs: make([]PrivateRunLifecycle, 0, len(references))}
		for _, reference := range references {
			inventory.Runs = append(inventory.Runs, PrivateRunLifecycle(reference))
		}
		return inventory, nil
	}
}

func buildPrivatePrunePreview(root string, loader PrivatePruneInventoryLoader, now time.Time) (PrivatePrunePreview, error) {
	preview, _, err := privatePruneInventory(root, loader, now)
	return preview, err
}

func privatePruneInventory(root string, loader PrivatePruneInventoryLoader, now time.Time) (PrivatePrunePreview, []privatePruneCandidate, error) {
	manifestData, err := readPrivatePlanLifecycleFile(root, filepath.Join(root, PrivateWorkspaceManifestName), maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return PrivatePrunePreview{}, nil, privatePruneError("manifest")
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		return PrivatePrunePreview{}, nil, privatePruneError("manifest")
	}
	inventory, err := loader(root)
	if err != nil {
		return PrivatePrunePreview{}, nil, privatePruneError("inventory")
	}
	if len(inventory.Runs) > maxPrivateWorkspaceTreeEntries {
		return PrivatePrunePreview{}, nil, privatePruneError("inventory_bound")
	}
	byAlias := map[string][]PrivateRunLifecycle{}
	seenRuns := map[string]struct{}{}
	for _, run := range inventory.Runs {
		if !privateRunIDRE.MatchString(run.RunID) || !privatePlanIDRE.MatchString(run.PlanID) || !privateWorkspaceAliasRE.MatchString(run.RunSetAlias) {
			return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
		}
		if _, exists := seenRuns[run.RunID]; exists {
			return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
		}
		seenRuns[run.RunID] = struct{}{}
		switch run.State {
		case "active", "incomplete":
			if run.CompletedOrder != 0 {
				return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
			}
		case "completed":
			if run.CompletedOrder < 1 {
				return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
			}
			byAlias[run.RunSetAlias] = append(byAlias[run.RunSetAlias], run)
		case "pruned":
			if run.CompletedOrder != 0 {
				return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
			}
		default:
			return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
		}
	}
	allCompleted := make(map[string]privatePruneCandidate)
	for _, runs := range byAlias {
		for _, run := range runs {
			path := filepath.Join(root, "runs", run.RunID)
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateWorkspaceDirectoryMode(info.Mode()) {
				return PrivatePrunePreview{}, nil, privatePruneError("run_tree")
			}
			hash, files, size, err := hashPrivatePruneTree(root, path)
			if err != nil {
				return PrivatePrunePreview{}, nil, err
			}
			allCompleted[run.RunID] = privatePruneCandidate{runID: run.RunID, planID: run.PlanID, path: path, hash: hash, files: files, bytes: size}
		}
	}

	eligibleIDs := map[string]struct{}{}
	protectedNewest := map[string]struct{}{}
	cutoff := now.UTC().AddDate(0, 0, -manifest.Retention.MaxCandidateAgeDays).UnixNano()
	for _, runs := range byAlias {
		sort.Slice(runs, func(i, j int) bool {
			if runs[i].CompletedOrder != runs[j].CompletedOrder {
				return runs[i].CompletedOrder > runs[j].CompletedOrder
			}
			return runs[i].RunID > runs[j].RunID
		})
		if len(runs) > 0 {
			protectedNewest[runs[0].RunID] = struct{}{}
		}
		keep := manifest.Retention.KeepCompletedRunSetsPerAlias
		if len(runs) > keep {
			for _, run := range runs[keep:] {
				eligibleIDs[run.RunID] = struct{}{}
			}
		}
		for _, run := range runs[1:] {
			if run.CompletedOrder < cutoff {
				eligibleIDs[run.RunID] = struct{}{}
			}
		}
	}

	var retainedBytes int64
	for runID, candidate := range allCompleted {
		if _, eligible := eligibleIDs[runID]; !eligible {
			retainedBytes += candidate.bytes
		}
	}
	if retainedBytes > manifest.Retention.MaxCandidateBytes {
		var oldest []PrivateRunLifecycle
		for _, runs := range byAlias {
			oldest = append(oldest, runs...)
		}
		sort.Slice(oldest, func(i, j int) bool {
			if oldest[i].CompletedOrder != oldest[j].CompletedOrder {
				return oldest[i].CompletedOrder < oldest[j].CompletedOrder
			}
			return oldest[i].RunID < oldest[j].RunID
		})
		for _, run := range oldest {
			if retainedBytes <= manifest.Retention.MaxCandidateBytes {
				break
			}
			if _, keep := protectedNewest[run.RunID]; keep {
				continue
			}
			if _, already := eligibleIDs[run.RunID]; already {
				continue
			}
			eligibleIDs[run.RunID] = struct{}{}
			retainedBytes -= allCompleted[run.RunID].bytes
		}
	}

	candidates := make([]privatePruneCandidate, 0, len(eligibleIDs))
	for runID := range eligibleIDs {
		if _, keep := protectedNewest[runID]; keep {
			continue
		}
		candidate, exists := allCompleted[runID]
		if !exists {
			return PrivatePrunePreview{}, nil, privatePruneError("inventory_record")
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].runID < candidates[j].runID })
	hash := sha256.New()
	_, _ = hash.Write([]byte(sha256HexBytes(manifestData)))
	_, _ = hash.Write([]byte{0})
	var files int
	var size int64
	for _, candidate := range candidates {
		_, _ = hash.Write([]byte(candidate.runID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(candidate.hash))
		_, _ = hash.Write([]byte{0})
		files += candidate.files
		size += candidate.bytes
	}
	return PrivatePrunePreview{
		SchemaVersion: 1, EligibleRunSets: len(candidates), EligibleFiles: files,
		EligibleBytes: size, InventorySHA256: hex.EncodeToString(hash.Sum(nil)),
	}, candidates, nil
}

func compactPrivatePrunedRun(root string, candidate privatePruneCandidate, inventorySHA string) error {
	if !privateRunIDRE.MatchString(candidate.runID) || !privatePlanIDRE.MatchString(candidate.planID) ||
		!validSHA256(candidate.hash) || !validSHA256(inventorySHA) || !privatePathWithin(root, filepath.Join(root, "runs"), candidate.path) {
		return privatePruneError("compact_input")
	}
	intent := privatePruneIntent{SchemaVersion: 1, RunID: candidate.runID, PlanID: candidate.planID, OriginalTreeSHA256: candidate.hash, InventorySHA256: inventorySHA}
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	intentPath, _ := privatePruneTransactionPaths(root, candidate.runID)
	if err := safepath.WriteFileExclusiveWithin(root, intentPath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return finishPrivatePruneTransaction(root, intent)
}

func recoverPrivatePruneTransactions(root string) error {
	entries, err := os.ReadDir(filepath.Join(root, ".ephemeral"))
	if err != nil {
		return err
	}
	intents := map[string]struct{}{}
	stages := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if name == filepath.Base(privateWorkspaceLockPath) {
			continue
		}
		switch {
		case strings.HasPrefix(name, "prune-") && strings.HasSuffix(name, ".intent.json") && !entry.IsDir():
			runID := strings.TrimSuffix(strings.TrimPrefix(name, "prune-"), ".intent.json")
			intents[runID] = struct{}{}
		case strings.HasPrefix(name, "prune-") && strings.HasSuffix(name, ".tree") && entry.IsDir():
			runID := strings.TrimSuffix(strings.TrimPrefix(name, "prune-"), ".tree")
			stages[runID] = struct{}{}
		default:
			return privatePruneError("unknown_scratch")
		}
	}
	for runID := range stages {
		if _, exists := intents[runID]; !exists {
			return privatePruneError("orphan_stage")
		}
	}
	for runID := range intents {
		if !privateRunIDRE.MatchString(runID) {
			return privatePruneError("intent_name")
		}
		intentPath, _ := privatePruneTransactionPaths(root, runID)
		data, err := readPrivatePlanLifecycleFile(root, intentPath, 1<<20)
		if err != nil {
			return err
		}
		var intent privatePruneIntent
		if decodePrivateLifecycleJSON(data, &intent) != nil || intent.SchemaVersion != 1 || intent.RunID != runID ||
			!privatePlanIDRE.MatchString(intent.PlanID) || !validSHA256(intent.OriginalTreeSHA256) || !validSHA256(intent.InventorySHA256) {
			return privatePruneError("intent")
		}
		if err := finishPrivatePruneTransaction(root, intent); err != nil {
			return err
		}
	}
	return nil
}

func finishPrivatePruneTransaction(root string, intent privatePruneIntent) error {
	intentPath, stagePath := privatePruneTransactionPaths(root, intent.RunID)
	runPath := filepath.Join(root, "runs", intent.RunID)
	stageInfo, stageErr := os.Lstat(stagePath)
	if os.IsNotExist(stageErr) {
		if pruned, err := inspectPrivatePrunedRun(root, intent.RunID, intent.PlanID); err == nil && pruned {
			return safepath.RemoveWithin(root, intentPath)
		}
		hash, _, _, err := hashPrivatePruneTree(root, runPath)
		if err != nil || hash != intent.OriginalTreeSHA256 {
			return privatePruneError("recovery_drift")
		}
		if err := safepath.RenameWithin(root, runPath, stagePath); err != nil {
			return err
		}
	} else if stageErr != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
		return privatePruneError("stage")
	}
	stageHash, _, _, err := hashPrivatePruneTree(root, stagePath)
	if err != nil || stageHash != intent.OriginalTreeSHA256 {
		return privatePruneError("stage_drift")
	}
	if _, err := os.Lstat(runPath); os.IsNotExist(err) {
		if err := safepath.MkdirAllWithin(root, runPath, 0o700); err != nil {
			return err
		}
		if err := writePrivatePrunedRun(root, runPath, intent); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if pruned, inspectErr := inspectPrivatePrunedRun(root, intent.RunID, intent.PlanID); inspectErr != nil || !pruned {
		return privatePruneError("replacement")
	}
	if err := removePrivateTree(root, stagePath); err != nil {
		return err
	}
	return safepath.RemoveWithin(root, intentPath)
}

func writePrivatePrunedRun(root, runPath string, intent privatePruneIntent) error {
	tombstone := privatePrunedRun{SchemaVersion: 1, RunID: intent.RunID, PlanID: intent.PlanID, OriginalTreeSHA256: intent.OriginalTreeSHA256}
	data, err := json.MarshalIndent(tombstone, "", "  ")
	if err != nil {
		return err
	}
	return safepath.WriteFileExclusiveWithin(root, filepath.Join(runPath, privatePrunedRunName), append(data, '\n'), 0o600)
}

func privatePruneTransactionPaths(root, runID string) (string, string) {
	base := "prune-" + runID
	return filepath.Join(root, ".ephemeral", base+".intent.json"), filepath.Join(root, ".ephemeral", base+".tree")
}

func privatePruneNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func hashPrivatePruneTree(root, target string) (string, int, int64, error) {
	insideRuns := privatePathWithin(root, filepath.Join(root, "runs"), target)
	base := filepath.Base(target)
	insideTransaction := privatePathWithin(root, filepath.Join(root, ".ephemeral"), target) &&
		strings.HasPrefix(base, "prune-run-") && strings.HasSuffix(base, ".tree")
	if !insideRuns && !insideTransaction {
		return "", 0, 0, privatePruneError("containment")
	}
	var paths []string
	var size int64
	err := filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return privatePruneError("symlink")
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil || !privateWorkspaceDirectoryMode(info.Mode()) {
				return privatePruneError("mode")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
			return privatePruneError("mode")
		}
		relative, err := filepath.Rel(target, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		size += info.Size()
		if len(paths) > 10_000 || size > 512<<20 {
			return privatePruneError("tree_bound")
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		data, err := safepath.ReadFileWithinLimit(root, filepath.Join(target, filepath.FromSlash(relative)), 512<<20)
		if err != nil {
			return "", 0, 0, privatePruneError("tree_read")
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), len(paths), size, nil
}

func ComparePrivateBaseline(options PrivateCompareOptions) (PrivateComparison, error) {
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil || !privateWorkspaceAliasRE.MatchString(options.Baseline) || !validPrivateBaselineSource(root, options.Candidate) {
		return PrivateComparison{}, privateBaselineError("compare_input")
	}
	manifest, baselineRoot, err := loadPrivateBaseline(root, options.Candidate.ContractSHA256, options.Baseline)
	if err != nil {
		return PrivateComparison{}, err
	}
	if manifest.ContractSHA256 != options.Candidate.ContractSHA256 {
		return PrivateComparison{}, privateBaselineError("contract_mismatch")
	}
	candidateResults, err := effectivePrivateSourceResults(root, options.Candidate)
	if err != nil {
		return PrivateComparison{}, err
	}
	baselineResults := make(map[string]Result, len(manifest.Surfaces))
	for _, surface := range manifest.Surfaces {
		path := filepath.Join(baselineRoot, filepath.FromSlash(surface.ResultPath))
		data, err := safepath.ReadFileWithinLimit(root, path, maxContractBytes)
		if err != nil || sha256HexBytes(data) != surface.ResultSHA256 {
			return PrivateComparison{}, privateBaselineError("baseline_result")
		}
		result, err := DecodeResult(bytes.NewReader(data))
		if err != nil || result.EffectiveSurface() != surface.Surface {
			return PrivateComparison{}, privateBaselineError("baseline_result")
		}
		baselineResults[surface.Surface] = result
	}
	if len(baselineResults) != len(candidateResults) {
		return PrivateComparison{}, privateBaselineError("surface_mismatch")
	}
	surfaces := make([]string, 0, len(baselineResults))
	for surface := range baselineResults {
		surfaces = append(surfaces, surface)
	}
	sort.Strings(surfaces)
	comparison := PrivateComparison{SchemaVersion: 1, Compatible: true, Surfaces: make([]PrivateSurfaceDelta, 0, len(surfaces))}
	for _, surface := range surfaces {
		baseline := baselineResults[surface]
		candidate, exists := candidateResults[surface]
		if !exists || !compatiblePrivateResults(baseline, candidate) {
			return PrivateComparison{}, privateBaselineError("result_incompatible")
		}
		if comparison.TaskClass == "" {
			comparison.TaskClass = baseline.TaskClass
		} else if comparison.TaskClass != baseline.TaskClass {
			return PrivateComparison{}, privateBaselineError("task_class_mismatch")
		}
		delta := PrivateSurfaceDelta{
			Surface: surface, BaselineStatus: baseline.Status, CandidateStatus: candidate.Status,
			BaselineEligibility: baseline.EffectiveEligibility(), CandidateEligibility: candidate.EffectiveEligibility(),
			Metrics: privateResultMetricDeltas(baseline, candidate),
		}
		if baseline.Qualitative != nil && candidate.Qualitative != nil {
			value := candidate.Qualitative.ScoreBPS - baseline.Qualitative.ScoreBPS
			delta.QualitativeScoreBPSDelta = &value
		} else if baseline.QualitativeReviewSet != nil && candidate.QualitativeReviewSet != nil {
			value := candidate.QualitativeReviewSet.ScoreBPS - baseline.QualitativeReviewSet.ScoreBPS
			delta.QualitativeScoreBPSDelta = &value
		}
		comparison.Surfaces = append(comparison.Surfaces, delta)
	}
	return comparison, nil
}

func compatiblePrivateResults(baseline, candidate Result) bool {
	if baseline.ScenarioID != candidate.ScenarioID || baseline.TaskClass != candidate.TaskClass ||
		baseline.DataClass != candidate.DataClass || baseline.EffectiveCategory() != candidate.EffectiveCategory() ||
		baseline.Variant != candidate.Variant || baseline.EffectiveSurface() != candidate.EffectiveSurface() ||
		baseline.Runtime.Provider != candidate.Runtime.Provider || baseline.Runtime.AgentVersion != candidate.Runtime.AgentVersion ||
		baseline.Runtime.Model != candidate.Runtime.Model || baseline.Runtime.Reasoning != candidate.Runtime.Reasoning {
		return false
	}
	if (baseline.Qualitative == nil) != (candidate.Qualitative == nil) {
		return false
	}
	if (baseline.QualitativeReviewSet == nil) != (candidate.QualitativeReviewSet == nil) {
		return false
	}
	if baseline.Qualitative != nil {
		return baseline.Qualitative.RubricID == candidate.Qualitative.RubricID &&
			baseline.Qualitative.RubricSHA256 == candidate.Qualitative.RubricSHA256 &&
			baseline.Qualitative.Reviewer == candidate.Qualitative.Reviewer &&
			baseline.Qualitative.Blinded == candidate.Qualitative.Blinded
	}
	if baseline.QualitativeReviewSet != nil {
		return baseline.QualitativeReviewSet.RubricID == candidate.QualitativeReviewSet.RubricID &&
			baseline.QualitativeReviewSet.RubricSHA256 == candidate.QualitativeReviewSet.RubricSHA256 &&
			baseline.QualitativeReviewSet.ContractSHA256 == candidate.QualitativeReviewSet.ContractSHA256 &&
			baseline.QualitativeReviewSet.Blinded == candidate.QualitativeReviewSet.Blinded &&
			baseline.QualitativeReviewSet.AssignmentDigest == candidate.QualitativeReviewSet.AssignmentDigest
	}
	return true
}

func privateResultMetricDeltas(baseline, candidate Result) []PrivateMetricDelta {
	type metric struct {
		name      string
		baseline  int64
		candidate int64
	}
	values := []metric{
		{"agent_turns", int64(baseline.Metrics.AgentTurns), int64(candidate.Metrics.AgentTurns)},
		{"tool_calls", int64(baseline.Metrics.ToolCalls), int64(candidate.Metrics.ToolCalls)},
		{"interface_invocations", int64(baseline.Metrics.InterfaceInvocations), int64(candidate.Metrics.InterfaceInvocations)},
		{"backend_requests", int64(baseline.Metrics.BackendRequests), int64(candidate.Metrics.BackendRequests)},
		{"duplicate_backend_requests", int64(baseline.Metrics.DuplicateBackendRequests), int64(candidate.Metrics.DuplicateBackendRequests)},
		{"output_bytes", baseline.Metrics.OutputBytes, candidate.Metrics.OutputBytes},
		{"input_tokens", baseline.Metrics.InputTokens, candidate.Metrics.InputTokens},
		{"output_tokens", baseline.Metrics.OutputTokens, candidate.Metrics.OutputTokens},
		{"main_thread_input_tokens", baseline.Metrics.MainThreadInputTokens, candidate.Metrics.MainThreadInputTokens},
		{"main_thread_output_tokens", baseline.Metrics.MainThreadOutputTokens, candidate.Metrics.MainThreadOutputTokens},
		{"estimated_cost_microusd", baseline.Metrics.EstimatedCostMicroUSD, candidate.Metrics.EstimatedCostMicroUSD},
		{"duration_millis", baseline.Metrics.DurationMillis, candidate.Metrics.DurationMillis},
	}
	result := make([]PrivateMetricDelta, 0, len(values))
	for _, value := range values {
		if !baseline.Coverage[value.name] || !candidate.Coverage[value.name] {
			continue
		}
		result = append(result, PrivateMetricDelta{Metric: value.name, Baseline: value.baseline, Candidate: value.candidate, Delta: value.candidate - value.baseline})
	}
	return result
}

func effectivePrivateSourceResults(root string, source PrivateBaselineSource) (map[string]Result, error) {
	results := make(map[string]Result, len(source.Surfaces))
	for _, surface := range source.Surfaces {
		resultData, err := safepath.ReadFileWithinLimit(root, filepath.Join(surface.RunDirectory, "result.json"), maxContractBytes)
		if err != nil {
			return nil, privateBaselineError("candidate_result")
		}
		result, err := DecodeResult(bytes.NewReader(resultData))
		if err != nil || result.DataClass != "private-local" || result.EffectiveSurface() != surface.Surface {
			return nil, privateBaselineError("candidate_result")
		}
		_, _, assessed, err := findPrivateAssessedResult(root, surface.RunDirectory)
		if err != nil {
			return nil, err
		}
		if assessed.SchemaVersion != 0 {
			if !samePrivateResultIdentity(result, assessed) || !hasPrivateQualitativeAssessment(assessed) ||
				validatePrivateAssessmentBinding(root, surface, resultData, assessed) != nil {
				return nil, privateBaselineError("candidate_assessment")
			}
			result = assessed
		}
		if privateSurfaceRequiresQualitative(surface) && !hasPrivateQualitativeAssessment(result) {
			return nil, privateBaselineError("candidate_assessment")
		}
		if surface.QualitativePanelContractPath != "" && result.QualitativeReviewSet == nil {
			return nil, privateBaselineError("candidate_assessment")
		}
		results[surface.Surface] = result
	}
	return results, nil
}

func loadPrivateBaseline(root, contract, baseline string) (privateBaselineManifest, string, error) {
	contractRoot := filepath.Join(root, "baselines", contract)
	pointerTreeSHA256 := ""
	if baseline == "current" {
		pointerData, err := safepath.ReadFileWithinLimit(root, filepath.Join(contractRoot, "current.json"), maxContractBytes)
		if err != nil {
			return privateBaselineManifest{}, "", privateBaselineError("current_missing")
		}
		pointer, err := decodePrivateBaselinePointer(pointerData)
		if err != nil || pointer.ContractSHA256 != contract {
			return privateBaselineManifest{}, "", privateBaselineError("current_invalid")
		}
		baseline = pointer.Baseline
		pointerTreeSHA256 = pointer.TreeSHA256
	}
	if !privateWorkspaceAliasRE.MatchString(baseline) {
		return privateBaselineManifest{}, "", privateBaselineError("baseline_name")
	}
	baselineRoot := filepath.Join(contractRoot, baseline)
	data, err := safepath.ReadFileWithinLimit(root, filepath.Join(baselineRoot, "baseline.v1.json"), maxContractBytes)
	if err != nil {
		return privateBaselineManifest{}, "", privateBaselineError("baseline_missing")
	}
	manifest, err := decodePrivateBaselineManifest(data)
	if err != nil || manifest.Baseline != baseline || manifest.ContractSHA256 != contract ||
		(pointerTreeSHA256 != "" && manifest.TreeSHA256 != pointerTreeSHA256) {
		return privateBaselineManifest{}, "", privateBaselineError("baseline_invalid")
	}
	treeHash, _, _, err := hashPrivateTree(baselineRoot, "baseline.v1.json")
	if err != nil || treeHash != manifest.TreeSHA256 {
		return privateBaselineManifest{}, "", privateBaselineError("baseline_drift")
	}
	return manifest, baselineRoot, nil
}

func encodePrivateBaselineManifest(manifest privateBaselineManifest) ([]byte, error) {
	if manifest.SchemaVersion != PrivateBaselineSchemaVersion || !privateWorkspaceAliasRE.MatchString(manifest.Baseline) ||
		!validSHA256(manifest.ContractSHA256) || !validSHA256(manifest.PlanSHA256) || !validSHA256(manifest.TreeSHA256) ||
		len(manifest.Surfaces) < 1 || len(manifest.Surfaces) > 3 {
		return nil, privateBaselineError("manifest")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func decodePrivateBaselineManifest(data []byte) (privateBaselineManifest, error) {
	var manifest privateBaselineManifest
	if err := decodePrivateBaselineJSON(data, &manifest); err != nil {
		return privateBaselineManifest{}, err
	}
	if _, err := encodePrivateBaselineManifest(manifest); err != nil {
		return privateBaselineManifest{}, err
	}
	return manifest, nil
}

func encodePrivateBaselinePointer(pointer privateBaselinePointer) ([]byte, error) {
	if pointer.SchemaVersion != PrivateBaselineSchemaVersion || !privateWorkspaceAliasRE.MatchString(pointer.Baseline) ||
		!validSHA256(pointer.ContractSHA256) || !validSHA256(pointer.TreeSHA256) {
		return nil, privateBaselineError("pointer")
	}
	data, err := json.MarshalIndent(pointer, "", "  ")
	return append(data, '\n'), err
}

func decodePrivateBaselinePointer(data []byte) (privateBaselinePointer, error) {
	var pointer privateBaselinePointer
	if err := decodePrivateBaselineJSON(data, &pointer); err != nil {
		return privateBaselinePointer{}, err
	}
	if _, err := encodePrivateBaselinePointer(pointer); err != nil {
		return privateBaselinePointer{}, err
	}
	return pointer, nil
}

func decodePrivateBaselineJSON(data []byte, target any) error {
	if len(data) > maxContractBytes || validateJSONNoDuplicateKeys(data) != nil {
		return privateBaselineError("decode")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(new(any)) != io.EOF {
		return privateBaselineError("decode")
	}
	return nil
}

func acquirePrivateWorkspaceLock(root string) (*safepath.FileLock, error) {
	lock, acquired, err := safepath.TryLockFileWithin(root, filepath.Join(root, privateWorkspaceLockPath), 0o600)
	if err != nil || !acquired {
		return nil, privateBaselineError("workspace_busy")
	}
	return lock, nil
}

func hashPrivateTree(root, excluded string) (string, int, int64, error) {
	var paths []string
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return privateBaselineError("tree_symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if relative == excluded {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
			return privateBaselineError("tree_file")
		}
		total += info.Size()
		if len(paths) >= privateBaselineMaxFiles || total > privateBaselineMaxBytes {
			return privateBaselineError("tree_bound")
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		data, err := safepath.ReadFileWithinLimit(root, filepath.Join(root, filepath.FromSlash(relative)), privateBaselineMaxBytes)
		if err != nil {
			return "", 0, 0, err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), len(paths), total, nil
}

func removePrivateTree(root, target string) error {
	if !privatePathWithin(root, root, target) || target == root {
		return privateBaselineError("remove_containment")
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	info, err := handle.Lstat(relative)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return privateBaselineError("remove_symlink")
	}
	return handle.RemoveAll(relative)
}

func privateRandomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func privateBaselineError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateBaselineRejected, code)
}

func privatePruneError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivatePruneRejected, code)
}
