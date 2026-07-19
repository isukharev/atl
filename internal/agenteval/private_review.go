package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

const privateReviewPacketVersion = 1

type PrivateReviewPrepareOptions struct {
	Root, RepositoryRoot, PlanID, Surface string
	ReviewerKind, ReviewerModel           string
	ReviewerID                            string
	BlindAssignment                       string
}

type PrivateReviewAssessOptions struct {
	Root, RepositoryRoot, PlanID, Surface string
	ReviewerID                            string
}

type PrivateReviewSummary struct {
	SchemaVersion int    `json:"schema_version"`
	PlanID        string `json:"plan_id"`
	RunID         string `json:"run_id"`
	Surface       string `json:"surface"`
	Status        string `json:"status"`
	Packet        string `json:"packet"`
	ResultSHA256  string `json:"result_sha256"`
	FinalSHA256   string `json:"final_sha256"`
	RubricSHA256  string `json:"rubric_sha256"`
	ReviewerID    string `json:"reviewer_id,omitempty"`
	Expected      int    `json:"expected_reviews,omitempty"`
	Prepared      int    `json:"prepared_reviews,omitempty"`
	Assessed      int    `json:"assessed_reviews,omitempty"`
}

// PreparePrivateReview creates an owner-only, fixed-layout packet without
// printing private answer or rubric bytes. The packet is deliberately inside
// the candidate run so retention and containment rules cover it.
func PreparePrivateReview(options PrivateReviewPrepareOptions) (PrivateReviewSummary, error) {
	if options.ReviewerKind == "" && options.ReviewerID == "" || !validRunSurface(options.Surface) {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	source, surface, err := loadPrivateReviewSurface(root, options.RepositoryRoot, options.PlanID, options.Surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	resultData, finalData, rubricData, result, rubric, err := loadPrivateReviewInputs(root, surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	if surface.QualitativePanelContractPath != "" {
		return preparePrivatePanelReview(root, source, surface, resultData, finalData, rubricData, result, rubric, options)
	}
	if options.ReviewerID != "" {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	var assignment [][]byte
	if options.BlindAssignment != "" {
		path := filepath.Join(root, filepath.FromSlash(options.BlindAssignment))
		if !privatePathWithin(root, filepath.Join(root, "cases"), path) {
			return PrivateReviewSummary{}, privatePlanError("blind_assignment")
		}
		data, readErr := safepath.ReadFileWithinLimit(root, path, maxReviewBytes)
		if readErr != nil || len(data) == 0 {
			return PrivateReviewSummary{}, privatePlanError("blind_assignment")
		}
		assignment = append(assignment, data)
	}
	review, err := NewReviewTemplate(result, resultData, finalData, rubric, Reviewer{Kind: options.ReviewerKind, Model: options.ReviewerModel}, assignment...)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_template")
	}
	packetRelative := filepath.ToSlash(filepath.Join("runs", source.RunID, "review", options.Surface, "run-01"))
	packet := filepath.Join(root, filepath.FromSlash(packetRelative))
	if _, err := os.Lstat(packet); err == nil || !os.IsNotExist(err) {
		return PrivateReviewSummary{}, privatePlanError("review_exists")
	}
	if err := safepath.MkdirAllWithin(root, filepath.Dir(packet), 0o700); err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_directory")
	}
	stageID, err := privateRandomID("review-stage-")
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_directory")
	}
	stage := filepath.Join(root, ".ephemeral", stageID)
	if err := safepath.MkdirAllWithin(root, stage, 0o700); err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_directory")
	}
	committed := false
	defer func() {
		if !committed {
			_ = removePrivateTree(root, stage)
		}
	}()
	reviewData, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_encode")
	}
	for name, data := range map[string][]byte{
		"final.json": finalData, "result.json": resultData, "rubric.json": rubricData, "review.json": append(reviewData, '\n'),
	} {
		if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(stage, name), data, 0o600); err != nil {
			return PrivateReviewSummary{}, privatePlanError("review_write")
		}
	}
	if err := safepath.RenameWithin(root, stage, packet); err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_commit")
	}
	committed = true
	return privateReviewSummary(source, options.Surface, "prepared", packetRelative, resultData, finalData, rubric), nil
}

// AssessPrivateReview binds an edited packet back to the exact immutable run
// inputs and writes only the versioned assessed result into the source run.
func AssessPrivateReview(options PrivateReviewAssessOptions) (PrivateReviewSummary, error) {
	if !validRunSurface(options.Surface) {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	source, surface, err := loadPrivateReviewSurface(root, options.RepositoryRoot, options.PlanID, options.Surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	resultData, finalData, rubricData, result, rubric, err := loadPrivateReviewInputs(root, surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	if surface.QualitativePanelContractPath != "" {
		return assessPrivatePanelReview(root, source, surface, resultData, finalData, rubricData, result, rubric, options)
	}
	if options.ReviewerID != "" {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	packetRelative := filepath.ToSlash(filepath.Join("runs", source.RunID, "review", options.Surface, "run-01"))
	packet := filepath.Join(root, filepath.FromSlash(packetRelative))
	for name, expected := range map[string][]byte{"result.json": resultData, "final.json": finalData, "rubric.json": rubricData} {
		actual, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(packet, name), maxReviewBytes)
		if readErr != nil || !bytes.Equal(actual, expected) {
			return PrivateReviewSummary{}, privatePlanError("review_packet_drift")
		}
	}
	reviewData, err := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "review.json"), maxReviewBytes)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_read")
	}
	review, err := DecodeReview(bytes.NewReader(reviewData))
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_decode")
	}
	assessed, err := AssessQualitative(result, resultData, finalData, rubric, review)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("assessment")
	}
	assessedData, err := json.MarshalIndent(assessed, "", "  ")
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("assessment_encode")
	}
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(surface.RunDirectory, "reviewed-result.json"), append(assessedData, '\n'), 0o600); err != nil {
		return PrivateReviewSummary{}, privatePlanError("assessment_write")
	}
	return privateReviewSummary(source, options.Surface, "assessed", packetRelative, resultData, finalData, rubric), nil
}

func loadPrivateReviewSurface(root, repository, planID, surfaceName string) (PrivateBaselineSource, PrivateBaselineSurfaceSource, error) {
	source, err := LoadCompletedPrivateRun(root, repository, planID)
	if err != nil {
		return PrivateBaselineSource{}, PrivateBaselineSurfaceSource{}, err
	}
	for _, surface := range source.Surfaces {
		if surface.Surface == surfaceName {
			return source, surface, nil
		}
	}
	return PrivateBaselineSource{}, PrivateBaselineSurfaceSource{}, privatePlanError("review_surface")
}

func loadPrivateReviewInputs(root string, surface PrivateBaselineSurfaceSource) ([]byte, []byte, []byte, Result, Rubric, error) {
	resultData, err := safepath.ReadFileWithinLimit(root, filepath.Join(surface.RunDirectory, "result.json"), maxContractBytes)
	if err != nil {
		return nil, nil, nil, Result{}, Rubric{}, privatePlanError("review_result")
	}
	result, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil || result.DataClass != "private-local" || result.EffectiveSurface() != surface.Surface {
		return nil, nil, nil, Result{}, Rubric{}, privatePlanError("review_result")
	}
	finalData, err := safepath.ReadFileWithinLimit(root, filepath.Join(surface.RunDirectory, "final.json"), 16<<20)
	if err != nil || len(finalData) == 0 {
		return nil, nil, nil, Result{}, Rubric{}, privatePlanError("review_final")
	}
	rubricData, err := safepath.ReadFileWithinLimit(root, surface.RubricPath, maxReviewBytes)
	if err != nil {
		return nil, nil, nil, Result{}, Rubric{}, privatePlanError("review_rubric")
	}
	rubric, err := DecodeRubric(bytes.NewReader(rubricData))
	if err != nil || rubricSHA256(rubric) != surface.RubricSHA256 {
		return nil, nil, nil, Result{}, Rubric{}, privatePlanError("review_rubric")
	}
	return resultData, finalData, rubricData, result, rubric, nil
}

func privateReviewSummary(source PrivateBaselineSource, surface, status, packet string, resultData, finalData []byte, rubric Rubric) PrivateReviewSummary {
	return PrivateReviewSummary{SchemaVersion: privateReviewPacketVersion, PlanID: source.PlanID, RunID: source.RunID,
		Surface: surface, Status: status, Packet: packet, ResultSHA256: sha256HexBytes(resultData),
		FinalSHA256: sha256HexBytes(finalData), RubricSHA256: rubricSHA256(rubric)}
}

func (s PrivateReviewSummary) Validate() error {
	if s.SchemaVersion != privateReviewPacketVersion || !privatePlanIDRE.MatchString(s.PlanID) || !privateRunIDRE.MatchString(s.RunID) ||
		!validRunSurface(s.Surface) || (s.Status != "prepared" && s.Status != "recorded" && s.Status != "assessed" && s.Status != "disagreement") || s.Packet == "" ||
		!validSHA256(s.ResultSHA256) || !validSHA256(s.FinalSHA256) || !validSHA256(s.RubricSHA256) {
		return fmt.Errorf("invalid private review summary")
	}
	if s.Expected != 0 && (s.Expected != 3 && s.Expected != 5 || s.Prepared < 0 || s.Prepared > s.Expected || s.Assessed < 0 || s.Assessed > s.Prepared || !identifierRE.MatchString(s.ReviewerID)) {
		return fmt.Errorf("invalid private review summary")
	}
	return nil
}
