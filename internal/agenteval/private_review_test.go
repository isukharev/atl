package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateReviewPacketBindsAssessmentWithoutPrintingPrivateContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)
	execution, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerKind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Validate() != nil || prepared.RunID != execution.RunID || prepared.Status != "prepared" ||
		!strings.HasPrefix(prepared.Packet, "runs/"+execution.RunID+"/review/") {
		t.Fatalf("prepared=%+v", prepared)
	}
	encoded, _ := json.Marshal(prepared)
	if bytes.Contains(encoded, []byte(privatePlanTestSecret)) || bytes.Contains(encoded, []byte(fixture.root)) {
		t.Fatalf("safe summary leaked private content: %s", encoded)
	}

	packet := filepath.Join(fixture.root, filepath.FromSlash(prepared.Packet))
	reviewData, err := os.ReadFile(filepath.Join(packet, "review.json"))
	if err != nil {
		t.Fatal(err)
	}
	review, err := DecodeReview(bytes.NewReader(reviewData))
	if err != nil {
		t.Fatal(err)
	}
	rubricData, err := os.ReadFile(filepath.Join(packet, "rubric.json"))
	if err != nil {
		t.Fatal(err)
	}
	rubric, err := DecodeRubric(bytes.NewReader(rubricData))
	if err != nil {
		t.Fatal(err)
	}
	for index := range review.Criteria {
		review.Criteria[index].Score = rubric.Criteria[index].Maximum
	}
	completedReview, _ := json.MarshalIndent(review, "", "  ")
	if err := writePrivateFile(filepath.Join(packet, "review.json"), append(completedReview, '\n')); err != nil {
		t.Fatal(err)
	}
	assessed, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP})
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Validate() != nil || assessed.Status != "assessed" {
		t.Fatalf("assessed=%+v", assessed)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source.Surfaces[0].RunDirectory, "reviewed-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(bytes.NewReader(data))
	if err != nil || result.Qualitative == nil || result.Qualitative.ResultSHA256 != assessed.ResultSHA256 ||
		result.Qualitative.FinalResponseSHA256 != assessed.FinalSHA256 {
		t.Fatalf("result=%+v err=%v", result.Qualitative, err)
	}
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP}); err == nil {
		t.Fatal("assessment overwrote an existing reviewed result")
	}
}

func TestPrivateReviewRejectsPacketDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerKind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(fixture.root, filepath.FromSlash(prepared.Packet), "final.json"), []byte("tampered\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP}); err == nil || !strings.Contains(err.Error(), "review_packet_drift") {
		t.Fatalf("drift err=%v", err)
	}
}

func TestPrivateReviewUsesExecutionTimeRubricAfterCasesChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	executionRubric, err := os.ReadFile(source.Surfaces[0].RubricPath)
	if err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(fixture.root, "cases", "portfolio", "rubric.json")
	currentData, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	var changed Rubric
	if err := json.Unmarshal(currentData, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Criteria[0].Description = "Changed only after the candidate completed."
	changedData, _ := json.MarshalIndent(changed, "", "  ")
	if err := writePrivateFile(currentPath, append(changedData, '\n')); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerKind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	packetRubric, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(prepared.Packet), "rubric.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packetRubric, executionRubric) || bytes.Equal(packetRubric, append(changedData, '\n')) {
		t.Fatal("review packet was not bound to the execution-time rubric")
	}
}

func TestPrivatePanelReviewRequiresCompleteRosterAndAggregatesOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	panel := privateReviewTestPanel()
	setPrivatePlanTestPanel(t, fixture, &panel)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	first, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: panel.Reviewers[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if first.Expected != 3 || first.Prepared != 1 || first.Assessed != 0 || first.ReviewerID != panel.Reviewers[0].ID {
		t.Fatalf("first=%+v", first)
	}
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: panel.Reviewers[0].ID}); err == nil || !strings.Contains(err.Error(), "review_roster_incomplete") {
		t.Fatalf("early assessment err=%v", err)
	}
	packets := []PrivateReviewSummary{first}
	for _, reviewer := range panel.Reviewers[1:] {
		prepared, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: reviewer.ID})
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, prepared)
	}
	for index, packet := range packets {
		completePrivateReviewPacket(t, fixture.root, packet.Packet, true)
		assessed, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packet.ReviewerID})
		if err != nil {
			t.Fatal(err)
		}
		expectedStatus := "recorded"
		if index == len(packets)-1 {
			expectedStatus = "assessed"
		}
		if assessed.Status != expectedStatus || assessed.Assessed != index+1 || assessed.Prepared != 3 {
			t.Fatalf("assessment %d=%+v", index, assessed)
		}
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source.Surfaces[0].RunDirectory, "reviewed-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(bytes.NewReader(data))
	if err != nil || result.Qualitative != nil || result.QualitativeReviewSet == nil || result.QualitativeReviewSet.Status != "pass" || len(result.QualitativeReviewSet.Members) != 3 {
		t.Fatalf("result=%+v err=%v", result.QualitativeReviewSet, err)
	}
	reconciled, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packets[len(packets)-1].ReviewerID})
	if err != nil || reconciled.Status != "assessed" || reconciled.Assessed != 3 {
		t.Fatalf("reconciled=%+v err=%v", reconciled, err)
	}
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "panel-pass", Confirm: PrivateBaselineConfirmation, Source: source}); err != nil {
		t.Fatalf("promote complete panel: %v", err)
	}
	rawResultData, err := os.ReadFile(filepath.Join(source.Surfaces[0].RunDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	rawResult, err := DecodeResult(bytes.NewReader(rawResultData))
	if err != nil {
		t.Fatal(err)
	}
	finalData, err := os.ReadFile(filepath.Join(source.Surfaces[0].RunDirectory, "final.json"))
	if err != nil {
		t.Fatal(err)
	}
	rubricData, err := os.ReadFile(source.Surfaces[0].RubricPath)
	if err != nil {
		t.Fatal(err)
	}
	rubric, err := DecodeRubric(bytes.NewReader(rubricData))
	if err != nil {
		t.Fatal(err)
	}
	_, _, policy, err := loadPrivatePanelReviewContract(fixture.root, source.Surfaces[0])
	if err != nil {
		t.Fatal(err)
	}
	changedReviews := privateReviewTestReviewsFromSet(rawResult.ScenarioID, result.QualitativeReviewSet)
	changedReviews[0].Reviewer.ID = "reviewer-substitute"
	changedPanel, err := AssessQualitativeReviewSet(rawResult, rawResultData, finalData, rubric, policy, changedReviews)
	if err != nil {
		t.Fatalf("changed panel=%v", err)
	}
	changedData, _ := json.MarshalIndent(changedPanel, "", "  ")
	if err := writePrivateFile(filepath.Join(source.Surfaces[0].RunDirectory, "reviewed-result.json"), append(changedData, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "panel-substituted", Confirm: PrivateBaselineConfirmation, Source: source}); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("substituted roster promoted: %v", err)
	}
	if err := writePrivateFile(filepath.Join(source.Surfaces[0].RunDirectory, "reviewed-result.json"), data); err != nil {
		t.Fatal(err)
	}
	result.QualitativeReviewSet.Members[0].Criteria[0].Score--
	tampered, _ := json.MarshalIndent(result, "", "  ")
	if err := writePrivateFile(filepath.Join(source.Surfaces[0].RunDirectory, "reviewed-result.json"), append(tampered, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "panel-tampered", Confirm: PrivateBaselineConfirmation, Source: source}); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("tampered panel promoted: %v", err)
	}
}

func TestPrivatePanelReviewDisagreementCannotPromote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	panel := privateReviewTestPanel()
	setPrivatePlanTestPanel(t, fixture, &panel)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	packets := make([]PrivateReviewSummary, 0, len(panel.Reviewers))
	for _, reviewer := range panel.Reviewers {
		prepared, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: reviewer.ID})
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, prepared)
	}
	for index, packet := range packets {
		completePrivateReviewPacket(t, fixture.root, packet.Packet, index != 0)
		assessed, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packet.ReviewerID})
		if err != nil {
			t.Fatal(err)
		}
		if index == len(packets)-1 && assessed.Status != "disagreement" {
			t.Fatalf("final=%+v", assessed)
		}
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "panel-disagreement", Confirm: PrivateBaselineConfirmation, Source: source}); !errors.Is(err, ErrPrivateBaselineRejected) || !strings.Contains(err.Error(), "assessment_disagreement") {
		t.Fatalf("promotion err=%v", err)
	}
}

func TestPrivatePanelReviewRequiresAllRunSurfacesBeforeAssessment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	panel := privateReviewTestPanel()
	setPrivatePlanTestPanel(t, fixture, &panel)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	first := source.Surfaces[0]
	second := first
	second.Surface = SurfaceCLISkill
	second.RunDirectory = filepath.Join(source.RunRoot, "surfaces", SurfaceCLISkill)
	if err := os.MkdirAll(second.RunDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	firstResultData, firstFinalData, firstRubricData, firstResult, firstRubric, err := loadPrivateReviewInputs(fixture.root, first)
	if err != nil {
		t.Fatal(err)
	}
	secondResult := firstResult
	secondResult.Surface = SurfaceCLISkill
	secondResult.Variant = SurfaceCLISkill
	secondResultData, _ := json.MarshalIndent(secondResult, "", "  ")
	if err := writePrivateFile(filepath.Join(second.RunDirectory, "result.json"), append(secondResultData, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(second.RunDirectory, "final.json"), firstFinalData); err != nil {
		t.Fatal(err)
	}
	source.Surfaces = append(source.Surfaces, second)
	firstPackets := make([]PrivateReviewSummary, 0, len(panel.Reviewers))
	for _, reviewer := range panel.Reviewers {
		packet, err := preparePrivatePanelReview(fixture.root, source, first, firstResultData, firstFinalData, firstRubricData, firstResult, firstRubric,
			PrivateReviewPrepareOptions{ReviewerID: reviewer.ID})
		if err != nil {
			t.Fatal(err)
		}
		firstPackets = append(firstPackets, packet)
	}
	completePrivateReviewPacket(t, fixture.root, firstPackets[0].Packet, true)
	if _, err := assessPrivatePanelReview(fixture.root, source, first, firstResultData, firstFinalData, firstResult, firstRubric,
		PrivateReviewAssessOptions{ReviewerID: panel.Reviewers[0].ID}); err == nil || !strings.Contains(err.Error(), "review_roster_incomplete") {
		t.Fatalf("assessment before all surfaces were prepared: %v", err)
	}
	secondResultData = append(secondResultData, '\n')
	for _, reviewer := range panel.Reviewers {
		if _, err := preparePrivatePanelReview(fixture.root, source, second, secondResultData, firstFinalData, firstRubricData, secondResult, firstRubric,
			PrivateReviewPrepareOptions{ReviewerID: reviewer.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := assessPrivatePanelReview(fixture.root, source, first, firstResultData, firstFinalData, firstResult, firstRubric,
		PrivateReviewAssessOptions{ReviewerID: panel.Reviewers[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePrivatePanelReview(fixture.root, source, second, secondResultData, firstFinalData, firstRubricData, secondResult, firstRubric,
		PrivateReviewPrepareOptions{ReviewerID: panel.Reviewers[0].ID}); err == nil || !strings.Contains(err.Error(), "review_roster") {
		t.Fatalf("preparation resumed after assessment began: %v", err)
	}
}

func TestPrivatePanelReviewRetryRejectsChangedReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	panel := privateReviewTestPanel()
	setPrivatePlanTestPanel(t, fixture, &panel)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	packets := make([]PrivateReviewSummary, 0, len(panel.Reviewers))
	for _, reviewer := range panel.Reviewers {
		packet, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: reviewer.ID})
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, packet)
	}
	completePrivateReviewPacket(t, fixture.root, packets[0].Packet, true)
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packets[0].ReviewerID}); err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(fixture.root, filepath.FromSlash(packets[0].Packet), "review.json")
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	review, err := DecodeReview(bytes.NewReader(reviewData))
	if err != nil {
		t.Fatal(err)
	}
	review.Criteria[0].Score--
	changed, _ := json.MarshalIndent(review, "", "  ")
	if err := writePrivateFile(reviewPath, append(changed, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packets[0].ReviewerID}); err == nil || !strings.Contains(err.Error(), "assessment_drift") {
		t.Fatalf("changed retry was accepted: %v", err)
	}
}

func privateReviewTestPanel() PrivateQualitativeReviewPanel {
	return PrivateQualitativeReviewPanel{Method: QualitativePanelMethod, MaxCriterionRangeBPS: 2500, Reviewers: []Reviewer{
		{ID: "reviewer-01", Kind: "codex", Model: "test-reviewer"},
		{ID: "reviewer-02", Kind: "codex", Model: "test-reviewer"},
		{ID: "reviewer-03", Kind: "codex", Model: "test-reviewer"},
	}}
}

func completePrivateReviewPacket(t *testing.T, root, packetRelative string, maximum bool) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(packetRelative), "review.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	review, err := DecodeReview(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	rubricData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(packetRelative), "rubric.json"))
	if err != nil {
		t.Fatal(err)
	}
	rubric, err := DecodeRubric(bytes.NewReader(rubricData))
	if err != nil {
		t.Fatal(err)
	}
	for index := range review.Criteria {
		if maximum {
			review.Criteria[index].Score = rubric.Criteria[index].Maximum
		}
	}
	encoded, _ := json.MarshalIndent(review, "", "  ")
	if err := writePrivateFile(path, append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
}

func privateReviewTestReviewsFromSet(scenarioID string, set *QualitativeReviewSetAssessment) []Review {
	reviews := make([]Review, 0, len(set.Members))
	for _, member := range set.Members {
		reviews = append(reviews, Review{SchemaVersion: ReviewSchemaVersion, RubricID: set.RubricID, RubricSHA256: set.RubricSHA256,
			ScenarioID: scenarioID, ResultSHA256: set.ResultSHA256, FinalResponseSHA256: set.FinalResponseSHA256,
			Blinded: set.Blinded, AssignmentDigest: set.AssignmentDigest, Reviewer: member.Reviewer,
			Criteria: append([]ReviewCriterionScore{}, member.Criteria...), FindingIDs: append([]string{}, member.FindingIDs...)})
	}
	return reviews
}
