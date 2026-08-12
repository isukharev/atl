package agenteval

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/grading"
)

func TestLegacyPrivatePanelUsageOutsideGenericBoundsReturnsSkipSignal(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	source := PrivateBaselineSource{RunID: "run-legacy"}
	surface := PrivateBaselineSurfaceSource{Surface: SurfaceATLMCP}
	reviewer := grading.Reviewer{ID: "reviewer-01", Kind: grading.ReviewerModel, Model: "codex/model",
		EnvironmentSHA256: digest, MaxInputTokens: grading.MaxTokens, MaxOutputTokens: grading.MaxTokens,
		MaxEstimatedCostMicroUSD: 100}
	execution := PrivateReviewerExecution{ReviewerID: reviewer.ID, Reasoning: "high", TimeoutSeconds: 30,
		Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}, MaxEstimatedCostMicroUSD: 100}
	started := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	attempt := privateReviewAttempt{SchemaVersion: privateReviewLegacySchemaVersion, PlanSHA256: digest,
		PanelContractSHA256: digest, ReviewerID: reviewer.ID, ReviewerKind: "codex", ReviewerModel: "model",
		ReviewerExecutionSHA256: digest, StartedAt: started.Format(time.RFC3339Nano)}
	inputTokens := int64(grading.MaxTokens + 1)
	cost, err := estimateCost(inputTokens, 1, execution.Pricing)
	if err != nil {
		t.Fatal(err)
	}
	receipt := privateReviewReceipt{SchemaVersion: privateReviewLegacySchemaVersion, PlanSHA256: digest,
		PanelContractSHA256: digest, ReviewerID: reviewer.ID, ReviewerKind: "codex", ReviewerModel: "model",
		ReviewerExecutionSHA256: digest, AgentIdentity: "binary-sha256:" + digest, Status: "succeeded", ModelRequests: 1,
		InputTokens: inputTokens, OutputTokens: 1, EstimatedCostMicroUSD: cost, CostKnown: true,
		ReviewSHA256: digest, CompletedAt: started.Add(time.Second).Format(time.RFC3339Nano)}
	attemptData, err := encodePrivateReviewAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	receiptData, err := encodePrivateReviewReceipt(receipt, execution)
	if err != nil {
		t.Fatal(err)
	}
	packet := filepath.Join(root, filepath.FromSlash(privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), reviewer.ID)))
	if err := os.MkdirAll(packet, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packet, "execution-attempt.json"), attemptData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packet, "execution-receipt.json"), receiptData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := privatePanelGradingUsage(root, source, surface, Review{Reviewer: Reviewer{ID: reviewer.ID}}, reviewer); !errors.Is(err, errPrivateGradingLegacyBounds) {
		t.Fatalf("legacy usage err=%v", err)
	}
}
