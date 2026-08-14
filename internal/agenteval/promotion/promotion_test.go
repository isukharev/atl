package promotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func testDigest(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func testIdentity(seed string) Identity {
	return Identity{LineageSHA256: testDigest(seed + ":lineage"), SkillSHA256: testDigest(seed + ":skill"),
		EvaluationSHA256: testDigest(seed + ":evaluation"), GraderSHA256: testDigest(seed + ":grader"),
		HoldoutSHA256: testDigest(seed + ":holdout"), RuntimeSHA256: testDigest(seed + ":runtime")}
}

func testInput(blocking bool) ComparisonInput {
	reference, candidate := testIdentity("reference"), testIdentity("candidate")
	reviews := []ComponentReview{
		{Component: ComponentSkill, ReferenceSHA256: reference.SkillSHA256, CandidateSHA256: candidate.SkillSHA256, ReviewSHA256: testDigest("review-skill"), Reviewed: true},
		{Component: ComponentEvaluation, ReferenceSHA256: reference.EvaluationSHA256, CandidateSHA256: candidate.EvaluationSHA256, ReviewSHA256: testDigest("review-evaluation"), Reviewed: true},
		{Component: ComponentGrader, ReferenceSHA256: reference.GraderSHA256, CandidateSHA256: candidate.GraderSHA256, ReviewSHA256: testDigest("review-grader"), Reviewed: true},
		{Component: ComponentHoldout, ReferenceSHA256: reference.HoldoutSHA256, CandidateSHA256: candidate.HoldoutSHA256, ReviewSHA256: testDigest("review-holdout"), Reviewed: true},
	}
	results := make([]AxisResult, 0, len(axes))
	for _, axis := range axes {
		value := AxisResult{Axis: axis, State: AxisPass}
		if blocking && axis == AxisSafety {
			value.State, value.Blocking, value.Reason = AxisFail, true, ReasonSafetyRegression
		}
		results = append(results, value)
	}
	return ComparisonInput{Reference: reference, Candidate: candidate, Reviews: reviews, Axes: results}
}

func TestEvaluatePromotionIsDeterministicAndContentMinimized(t *testing.T) {
	receipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if receipt.Decision != DecisionPromote || len(receipt.Reasons) != 0 {
		t.Fatalf("unexpected promotion receipt: %+v", receipt)
	}
	encoded, err := EncodeDecision(receipt)
	if err != nil {
		t.Fatalf("EncodeDecision(): %v", err)
	}
	decoded, err := DecodeDecision(bytes.NewReader(encoded))
	if err != nil || decoded.ReceiptSHA256 != receipt.ReceiptSHA256 {
		t.Fatalf("DecodeDecision(): receipt=%+v err=%v", decoded, err)
	}
	repeated, err := Evaluate(testInput(false))
	if err != nil || repeated.ReceiptSHA256 != receipt.ReceiptSHA256 {
		t.Fatalf("decision was not idempotent: first=%+v second=%+v err=%v", receipt, repeated, err)
	}
	if strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "path") || strings.Contains(string(encoded), "evidence") {
		t.Fatalf("receipt retained sealed content: %s", encoded)
	}
}

func TestEvaluateRefusesEveryBlockingAxisWithoutWeightedAggregate(t *testing.T) {
	input := testInput(true)
	receipt, err := Evaluate(input)
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if receipt.Decision != DecisionRefuse || len(receipt.Reasons) != 1 || receipt.Reasons[0] != ReasonSafetyRegression {
		t.Fatalf("unexpected refusal: %+v", receipt)
	}
	input.Axes[1].State, input.Axes[1].Blocking, input.Axes[1].Reason = AxisUnknown, true, ReasonCoverageMissing
	receipt, err = Evaluate(input)
	if err != nil || receipt.Decision != DecisionRefuse || len(receipt.Reasons) != 2 {
		t.Fatalf("unknown coverage did not remain blocking: receipt=%+v err=%v", receipt, err)
	}
}

func TestPromotionRejectsAliasesAndUnreviewedComponents(t *testing.T) {
	input := testInput(false)
	input.Reference.LineageSHA256 = "latest"
	if _, err := Evaluate(input); err == nil {
		t.Fatal("latest alias was accepted")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidIdentity {
		t.Fatalf("alias error code=%q ok=%v", code, ok)
	}
	input = testInput(false)
	input.Reviews[0].Reviewed = false
	if _, err := Evaluate(input); err == nil {
		t.Fatal("unreviewed component was accepted")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidReview {
		t.Fatalf("review error code=%q ok=%v", code, ok)
	}
}

func TestRollbackIsExactAndCanonical(t *testing.T) {
	request := RollbackRequest{Current: testIdentity("candidate"), Restore: testIdentity("reference"), AuthorizationSHA256: testDigest("rollback-review")}
	receipt, err := PlanRollback(request)
	if err != nil {
		t.Fatalf("PlanRollback(): %v", err)
	}
	if !receipt.Restored || receipt.Decision != DecisionRollback {
		t.Fatalf("unexpected rollback: %+v", receipt)
	}
	data, err := EncodeRollback(receipt)
	if err != nil {
		t.Fatalf("encodeRollback(): %v", err)
	}
	if _, err := DecodeRollback(bytes.NewReader(data)); err != nil {
		t.Fatalf("DecodeRollback(): %v", err)
	}
	if _, err := PlanRollback(RollbackRequest{Current: request.Current, Restore: request.Restore, AuthorizationSHA256: "latest"}); err == nil {
		t.Fatal("rollback accepted an alias authorization")
	}
}

func TestDecodeRejectsUnknownAndNonCanonicalMembers(t *testing.T) {
	receipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	data, err := EncodeDecision(receipt)
	if err != nil {
		t.Fatalf("EncodeDecision(): %v", err)
	}
	mutated := bytes.TrimSuffix(data, []byte{'\n'})
	mutated = append(mutated[:len(mutated)-1], []byte(`,"future":1}`)...)
	if _, err := DecodeDecision(bytes.NewReader(mutated)); err == nil {
		t.Fatal("future member was accepted")
	}
	if _, err := DecodeDecision(bytes.NewReader(append(data, data...))); err == nil {
		t.Fatal("trailing second value was accepted")
	}
}

func TestStorePromotionIsExplicitIdempotentAndRollbackExact(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod(): %v", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	promotionReceipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if err := store.ApplyPromotion(promotionReceipt, nil); err != nil {
		t.Fatalf("ApplyPromotion(): %v", err)
	}
	if err := store.ApplyPromotion(promotionReceipt, nil); err == nil {
		t.Fatal("promotion without an exact expected current identity was replayed")
	}
	current, present, err := store.Current()
	if err != nil || !present || current != promotionReceipt.Candidate {
		t.Fatalf("Current(): current=%+v present=%v err=%v", current, present, err)
	}
	rollback, err := PlanRollback(RollbackRequest{Current: promotionReceipt.Candidate, Restore: promotionReceipt.Reference, AuthorizationSHA256: testDigest("rollback")})
	if err != nil {
		t.Fatalf("PlanRollback(): %v", err)
	}
	if err := store.ApplyRollback(rollback); err != nil {
		t.Fatalf("ApplyRollback(): %v", err)
	}
	current, present, err = store.Current()
	if err != nil || !present || current != promotionReceipt.Reference {
		t.Fatalf("Current after rollback: current=%+v present=%v err=%v", current, present, err)
	}
	if err := store.ApplyRollback(rollback); err == nil {
		t.Fatal("rollback replay was accepted")
	}
}

func TestStoreRejectsUnsafeRoot(t *testing.T) {
	unsafe := t.TempDir()
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatalf("Chmod(): %v", err)
	}
	if _, err := NewStore(unsafe); err == nil {
		t.Fatal("world-readable store root was accepted")
	}
}
