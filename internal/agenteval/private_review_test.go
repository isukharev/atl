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
