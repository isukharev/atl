package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/isukharev/atl/internal/safepath"
)

const privatePanelResultBindingSchemaVersion = 1

type privatePanelResultBinding struct {
	SchemaVersion int    `json:"schema_version"`
	OpaqueToken   string `json:"opaque_token"`
	ResultSHA256  string `json:"result_sha256"`
}

func preparePrivatePanelReview(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource, resultData, finalData, rubricData []byte, result Result, rubric Rubric, options PrivateReviewPrepareOptions) (PrivateReviewSummary, error) {
	contract, assignment, policy, err := loadPrivatePanelReviewContract(root, surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	if options.ReviewerKind != "" || options.ReviewerModel != "" || options.BlindAssignment != "" {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	reviewer, ok := privatePanelReviewer(contract, options.ReviewerID)
	if !ok {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	prepared, _, err := privatePanelReviewProgress(root, source, surface, contract)
	if err != nil || prepared >= policy.ExpectedReviewers {
		return PrivateReviewSummary{}, privatePlanError("review_roster")
	}
	if err := requirePrivatePanelRunUnassessed(root, source); err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_roster")
	}
	review, err := NewReviewTemplate(result, resultData, finalData, rubric, reviewer, optionalPrivateAssignment(assignment)...)
	if err != nil {
		return PrivateReviewSummary{}, privatePlanError("review_template")
	}
	resultBinding := review.ResultSHA256
	if surface.CellID != "" {
		binding, bindingErr := createOrLoadPrivatePanelResultBinding(root, source, surface, reviewer, review.ResultSHA256)
		if bindingErr != nil {
			return PrivateReviewSummary{}, bindingErr
		}
		review.ResultSHA256 = binding.OpaqueToken
		resultBinding = binding.OpaqueToken
	}
	packetRelative := privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), reviewer.ID)
	packet := filepath.Join(root, filepath.FromSlash(packetRelative))
	if _, err := safepath.StatWithin(root, packet); err == nil || !os.IsNotExist(err) {
		return PrivateReviewSummary{}, privatePlanError("review_exists")
	}
	if err := writePrivateReviewPacket(root, packet, resultData, finalData, rubricData, review, surface.CellID == ""); err != nil {
		return PrivateReviewSummary{}, err
	}
	prepared, assessed, err := privatePanelReviewProgress(root, source, surface, contract)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	return privatePanelReviewSummary(source, surface.Surface, reviewer.ID, "prepared", packetRelative, resultBinding, finalData, rubric, policy.ExpectedReviewers, prepared, assessed), nil
}

func assessPrivatePanelReview(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource, resultData, finalData []byte, result Result, rubric Rubric, options PrivateReviewAssessOptions) (PrivateReviewSummary, error) {
	contract, _, policy, err := loadPrivatePanelReviewContract(root, surface)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	reviewer, ok := privatePanelReviewer(contract, options.ReviewerID)
	if !ok {
		return PrivateReviewSummary{}, privatePlanError("review_input")
	}
	prepared, _, err := privatePanelReviewProgress(root, source, surface, contract)
	if err != nil || prepared != policy.ExpectedReviewers {
		return PrivateReviewSummary{}, privatePlanError("review_roster_incomplete")
	}
	if err := validatePrivatePanelRunPrepared(root, source); err != nil {
		return PrivateReviewSummary{}, err
	}
	packetRelative := privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), reviewer.ID)
	packet := filepath.Join(root, filepath.FromSlash(packetRelative))
	assessmentPath := filepath.Join(packet, "assessment.json")
	reviewData, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "review.json"), maxReviewBytes)
	if readErr != nil {
		return PrivateReviewSummary{}, privatePlanError("review_read")
	}
	binding, err := loadPrivatePanelResultBinding(root, source, surface, reviewer, sha256HexBytes(resultData))
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	canonicalData, err := canonicalPrivatePanelAssessment(result, resultData, finalData, rubric, reviewer, reviewData, binding)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	if _, statErr := safepath.StatWithin(root, assessmentPath); os.IsNotExist(statErr) {
		if err := safepath.WriteFileExclusiveWithin(root, assessmentPath, canonicalData, 0o600); err != nil {
			return PrivateReviewSummary{}, privatePlanError("assessment_write")
		}
	} else if statErr != nil {
		return PrivateReviewSummary{}, privatePlanError("assessment_invalid")
	} else {
		existing, readErr := readPrivatePlanLifecycleFile(root, assessmentPath, maxReviewBytes)
		if readErr != nil || !bytes.Equal(existing, canonicalData) {
			return PrivateReviewSummary{}, privatePlanError("assessment_drift")
		}
	}
	prepared, assessed, err := privatePanelReviewProgress(root, source, surface, contract)
	if err != nil {
		return PrivateReviewSummary{}, err
	}
	status := "recorded"
	if assessed == policy.ExpectedReviewers {
		reviews := make([]Review, 0, assessed)
		for _, expectedReviewer := range contract.Reviewers {
			path := filepath.Join(root, filepath.FromSlash(privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), expectedReviewer.ID)), "assessment.json")
			data, readErr := readPrivatePlanLifecycleFile(root, path, maxReviewBytes)
			if readErr != nil {
				return PrivateReviewSummary{}, privatePlanError("assessment_read")
			}
			item, decodeErr := DecodeReview(bytes.NewReader(data))
			if decodeErr != nil || item.Reviewer != expectedReviewer {
				return PrivateReviewSummary{}, privatePlanError("assessment_invalid")
			}
			reviews = append(reviews, item)
		}
		panelResult, assessErr := AssessQualitativeReviewSet(result, resultData, finalData, rubric, policy, reviews)
		if assessErr != nil {
			return PrivateReviewSummary{}, privatePlanError("assessment")
		}
		encoded, encodeErr := json.MarshalIndent(panelResult, "", "  ")
		if encodeErr != nil {
			return PrivateReviewSummary{}, privatePlanError("assessment_encode")
		}
		reviewedPath := filepath.Join(surface.RunDirectory, "reviewed-result.json")
		if existing, readErr := safepath.ReadFileWithinLimit(root, reviewedPath, maxContractBytes); readErr == nil {
			if !bytes.Equal(existing, append(encoded, '\n')) {
				return PrivateReviewSummary{}, privatePlanError("assessment_drift")
			}
		} else if !os.IsNotExist(readErr) {
			return PrivateReviewSummary{}, privatePlanError("assessment_write")
		} else if err := safepath.WriteFileExclusiveWithin(root, reviewedPath, append(encoded, '\n'), 0o600); err != nil {
			return PrivateReviewSummary{}, privatePlanError("assessment_write")
		}
		status = "assessed"
		if panelResult.QualitativeReviewSet.Status == "disagreement" {
			status = "disagreement"
		}
	}
	return privatePanelReviewSummary(source, surface.Surface, reviewer.ID, status, packetRelative, binding.OpaqueToken, finalData, rubric, policy.ExpectedReviewers, prepared, assessed), nil
}

func loadPrivatePanelReviewContract(root string, surface PrivateBaselineSurfaceSource) (privateQualitativeReviewPanelContract, []byte, QualitativePanelPolicy, error) {
	data, err := readPrivatePlanLifecycleFile(root, surface.QualitativePanelContractPath, maxReviewBytes)
	if err != nil || sha256HexBytes(data) != surface.QualitativePanelContractSHA256 {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("panel_contract")
	}
	var contract privateQualitativeReviewPanelContract
	if decodePrivateLifecycleJSON(data, &contract) != nil {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("panel_contract")
	}
	encoded, err := encodePrivateQualitativeReviewPanelContract(contract)
	if err != nil || !bytes.Equal(data, encoded) {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("panel_contract")
	}
	policy := QualitativePanelPolicy{SchemaVersion: QualitativePanelSchemaVersion, Method: contract.Method, ExpectedReviewers: len(contract.Reviewers), MaxCriterionRangeBPS: contract.MaxCriterionRangeBPS}
	if err := policy.Validate(); err != nil {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("panel_contract")
	}
	panel := PrivateQualitativeReviewPanel{Method: contract.Method, Reviewers: contract.Reviewers, MaxCriterionRangeBPS: contract.MaxCriterionRangeBPS}
	if err := panel.validate(); err != nil {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("panel_contract")
	}
	var assignment []byte
	if contract.BlindAssignmentSHA256 != "" {
		assignment, err = readPrivatePlanLifecycleFile(root, surface.BlindAssignmentPath, maxReviewBytes)
		if err != nil || len(assignment) == 0 || sha256HexBytes(assignment) != contract.BlindAssignmentSHA256 || surface.BlindAssignmentSHA256 != contract.BlindAssignmentSHA256 {
			return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("blind_assignment")
		}
	} else if surface.BlindAssignmentPath != "" || surface.BlindAssignmentSHA256 != "" {
		return privateQualitativeReviewPanelContract{}, nil, QualitativePanelPolicy{}, privatePlanError("blind_assignment")
	}
	return contract, assignment, policy, nil
}

func privatePanelReviewer(contract privateQualitativeReviewPanelContract, id string) (Reviewer, bool) {
	for _, reviewer := range contract.Reviewers {
		if reviewer.ID == id {
			return reviewer, true
		}
	}
	return Reviewer{}, false
}

func optionalPrivateAssignment(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	return [][]byte{data}
}

func privatePanelPacketRelative(runID, surface, reviewerID string) string {
	return filepath.ToSlash(filepath.Join("runs", runID, "review", surface, reviewerID))
}

func privatePanelReviewProgress(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource, contract privateQualitativeReviewPanelContract) (int, int, error) {
	reviewRoot := filepath.Join(root, "runs", source.RunID, "review", privateReviewCellKey(surface))
	entries, err := safepath.ReadDirWithin(root, reviewRoot)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, privatePlanError("review_directory")
	}
	allowed := make(map[string]struct{}, len(contract.Reviewers))
	for _, reviewer := range contract.Reviewers {
		allowed[reviewer.ID] = struct{}{}
	}
	prepared, assessed := 0, 0
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return 0, 0, privatePlanError("review_roster")
		}
		packet := filepath.Join(reviewRoot, entry.Name())
		info, statErr := safepath.StatWithin(root, packet)
		if statErr != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
			return 0, 0, privatePlanError("review_roster")
		}
		prepared++
		assessment, statErr := safepath.StatWithin(root, filepath.Join(packet, "assessment.json"))
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil || !assessment.Mode().IsRegular() || !privateWorkspaceFileMode(assessment.Mode()) {
			return 0, 0, privatePlanError("assessment_invalid")
		}
		assessed++
	}
	return prepared, assessed, nil
}

func requirePrivatePanelRunUnassessed(root string, source PrivateBaselineSource) error {
	for _, surface := range source.Surfaces {
		if surface.QualitativePanelContractPath == "" {
			return privatePlanError("review_roster")
		}
		contract, _, _, err := loadPrivatePanelReviewContract(root, surface)
		if err != nil {
			return err
		}
		_, assessed, err := privatePanelReviewProgress(root, source, surface, contract)
		if err != nil {
			return err
		}
		if assessed != 0 {
			return privatePlanError("review_roster")
		}
	}
	return nil
}

func validatePrivatePanelRunPrepared(root string, source PrivateBaselineSource) error {
	for _, surface := range source.Surfaces {
		if surface.QualitativePanelContractPath == "" {
			return privatePlanError("review_roster_incomplete")
		}
		contract, _, policy, err := loadPrivatePanelReviewContract(root, surface)
		if err != nil {
			return err
		}
		prepared, _, err := privatePanelReviewProgress(root, source, surface, contract)
		if err != nil || prepared != policy.ExpectedReviewers {
			return privatePlanError("review_roster_incomplete")
		}
		resultData, finalData, rubricData, result, rubric, err := loadPrivateReviewInputs(root, surface)
		if err != nil {
			return err
		}
		for _, reviewer := range contract.Reviewers {
			packet := filepath.Join(root, filepath.FromSlash(privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), reviewer.ID)))
			if err := validatePrivatePanelPacket(root, packet, resultData, finalData, rubricData, surface.CellID == ""); err != nil {
				return err
			}
			reviewData, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "review.json"), maxReviewBytes)
			if readErr != nil {
				return privatePlanError("review_packet_drift")
			}
			binding, bindingErr := loadPrivatePanelResultBinding(root, source, surface, reviewer, sha256HexBytes(resultData))
			if bindingErr != nil {
				return bindingErr
			}
			canonicalData, canonicalErr := canonicalPrivatePanelAssessment(result, resultData, finalData, rubric, reviewer, reviewData, binding)
			if canonicalErr != nil {
				return canonicalErr
			}
			assessmentPath := filepath.Join(packet, "assessment.json")
			if _, statErr := safepath.StatWithin(root, assessmentPath); os.IsNotExist(statErr) {
				continue
			} else if statErr != nil {
				return privatePlanError("assessment_invalid")
			}
			existing, readErr := readPrivatePlanLifecycleFile(root, assessmentPath, maxReviewBytes)
			if readErr != nil || !bytes.Equal(existing, canonicalData) {
				return privatePlanError("assessment_drift")
			}
		}
	}
	return nil
}

func canonicalPrivatePanelAssessment(result Result, resultData, finalData []byte, rubric Rubric, reviewer Reviewer, reviewData []byte,
	binding privatePanelResultBinding,
) ([]byte, error) {
	review, err := DecodeReview(bytes.NewReader(reviewData))
	if err != nil || review.Reviewer != reviewer || review.ResultSHA256 != binding.OpaqueToken ||
		binding.ResultSHA256 != sha256HexBytes(resultData) {
		return nil, privatePlanError("review_decode")
	}
	review.ResultSHA256 = binding.ResultSHA256
	if _, err := AssessQualitative(result, resultData, finalData, rubric, review); err != nil {
		return nil, privatePlanError("assessment")
	}
	canonical, err := canonicalPrivateReview(review, rubric)
	if err != nil {
		return nil, privatePlanError("assessment")
	}
	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, privatePlanError("assessment_encode")
	}
	return append(data, '\n'), nil
}

func validatePrivatePanelPacket(root, packet string, resultData, finalData, rubricData []byte, includeResult bool) error {
	entries, err := safepath.ReadDirWithin(root, packet)
	if err != nil {
		return privatePlanError("review_packet")
	}
	allowed := map[string]bool{"final.json": true, "rubric.json": true, "review.json": true, "assessment.json": true}
	if includeResult {
		allowed["result.json"] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return privatePlanError("review_packet_drift")
		}
	}
	expectedFiles := map[string][]byte{"final.json": finalData, "rubric.json": rubricData}
	if includeResult {
		expectedFiles["result.json"] = resultData
	}
	for name, expected := range expectedFiles {
		actual, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(packet, name), maxReviewBytes)
		if readErr != nil || !bytes.Equal(actual, expected) {
			return privatePlanError("review_packet_drift")
		}
	}
	return nil
}

func writePrivateReviewPacket(root, packet string, resultData, finalData, rubricData []byte, review Review, includeResult bool) error {
	if err := safepath.MkdirAllWithin(root, filepath.Dir(packet), 0o700); err != nil {
		return privatePlanError("review_directory")
	}
	stageID, err := privateRandomID("review-stage-")
	if err != nil {
		return privatePlanError("review_directory")
	}
	stage := filepath.Join(root, ".ephemeral", stageID)
	if err := safepath.MkdirAllWithin(root, stage, 0o700); err != nil {
		return privatePlanError("review_directory")
	}
	committed := false
	defer func() {
		if !committed {
			_ = removePrivateTree(root, stage)
		}
	}()
	reviewData, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return privatePlanError("review_encode")
	}
	files := map[string][]byte{"final.json": finalData, "rubric.json": rubricData, "review.json": append(reviewData, '\n')}
	if includeResult {
		files["result.json"] = resultData
	}
	for name, data := range files {
		if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(stage, name), data, 0o600); err != nil {
			return privatePlanError("review_write")
		}
	}
	if err := safepath.RenameWithin(root, stage, packet); err != nil {
		return privatePlanError("review_commit")
	}
	committed = true
	return nil
}

func canonicalPrivateReview(review Review, rubric Rubric) (Review, error) {
	scores := make(map[string]int, len(review.Criteria))
	for _, criterion := range review.Criteria {
		scores[criterion.ID] = criterion.Score
	}
	canonical := review
	canonical.Criteria = make([]ReviewCriterionScore, 0, len(rubric.Criteria))
	for _, criterion := range rubric.Criteria {
		score, ok := scores[criterion.ID]
		if !ok {
			return Review{}, fmt.Errorf("missing criterion")
		}
		canonical.Criteria = append(canonical.Criteria, ReviewCriterionScore{ID: criterion.ID, Score: score})
	}
	canonical.FindingIDs = append([]string{}, review.FindingIDs...)
	sort.Strings(canonical.FindingIDs)
	return canonical, nil
}

func privatePanelReviewSummary(source PrivateBaselineSource, surface, reviewerID, status, packet, resultBinding string, finalData []byte, rubric Rubric, expected, prepared, assessed int) PrivateReviewSummary {
	summary := privateReviewSummary(source, surface, status, packet, []byte(resultBinding), finalData, rubric)
	summary.ResultSHA256 = resultBinding
	summary.ReviewerID = reviewerID
	summary.Expected = expected
	summary.Prepared = prepared
	summary.Assessed = assessed
	return summary
}

func privatePanelResultBindingPath(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource, reviewer Reviewer) string {
	return filepath.Join(root, "runs", source.RunID, "contracts", privateReviewCellKey(surface), "review-bindings", reviewer.ID+".json")
}

func createOrLoadPrivatePanelResultBinding(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource,
	reviewer Reviewer, resultSHA256 string,
) (privatePanelResultBinding, error) {
	path := privatePanelResultBindingPath(root, source, surface, reviewer)
	if _, err := safepath.StatWithin(root, path); err == nil {
		return loadPrivatePanelResultBinding(root, source, surface, reviewer, resultSHA256)
	} else if !os.IsNotExist(err) {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	randomID, err := privateRandomID("")
	if err != nil {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	binding := privatePanelResultBinding{SchemaVersion: privatePanelResultBindingSchemaVersion,
		OpaqueToken: sha256HexBytes([]byte(randomID)), ResultSHA256: resultSHA256}
	data, err := encodePrivatePanelResultBinding(binding)
	if err != nil {
		return privatePanelResultBinding{}, err
	}
	if err := safepath.MkdirAllWithin(root, filepath.Dir(path), 0o700); err != nil {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	return binding, nil
}

func loadPrivatePanelResultBinding(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource,
	reviewer Reviewer, resultSHA256 string,
) (privatePanelResultBinding, error) {
	if surface.CellID == "" {
		return privatePanelResultBinding{SchemaVersion: privatePanelResultBindingSchemaVersion,
			OpaqueToken: resultSHA256, ResultSHA256: resultSHA256}, nil
	}
	path := privatePanelResultBindingPath(root, source, surface, reviewer)
	data, err := readPrivatePlanLifecycleFile(root, path, maxContractBytes)
	if err != nil {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	var binding privatePanelResultBinding
	if decodePrivateLifecycleJSON(data, &binding) != nil || binding.SchemaVersion != privatePanelResultBindingSchemaVersion ||
		binding.ResultSHA256 != resultSHA256 || binding.OpaqueToken == binding.ResultSHA256 {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	canonical, err := encodePrivatePanelResultBinding(binding)
	if err != nil || !bytes.Equal(canonical, data) {
		return privatePanelResultBinding{}, privatePlanError("review_binding")
	}
	return binding, nil
}

func encodePrivatePanelResultBinding(binding privatePanelResultBinding) ([]byte, error) {
	if binding.SchemaVersion != privatePanelResultBindingSchemaVersion || !validSHA256(binding.OpaqueToken) ||
		!validSHA256(binding.ResultSHA256) || binding.OpaqueToken == binding.ResultSHA256 {
		return nil, privatePlanError("review_binding")
	}
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return nil, privatePlanError("review_binding")
	}
	return append(data, '\n'), nil
}
