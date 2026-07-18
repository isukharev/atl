package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
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
