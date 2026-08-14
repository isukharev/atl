package promotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestInterruptedDecisionIsPersistableAndSelfVerifying(t *testing.T) {
	input := testInput(false)
	input.Interrupted = true
	receipt, err := Evaluate(input)
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if receipt.Decision != DecisionRefuse || !receipt.Interrupted || len(receipt.Reasons) != 1 || receipt.Reasons[0] != ReasonInterrupted {
		t.Fatalf("unexpected interrupted receipt: %+v", receipt)
	}
	data, err := EncodeDecision(receipt)
	if err != nil {
		t.Fatalf("EncodeDecision(): %v", err)
	}
	decoded, err := DecodeDecision(bytes.NewReader(data))
	if err != nil || !decoded.Interrupted || decoded.ReceiptSHA256 != receipt.ReceiptSHA256 {
		t.Fatalf("DecodeDecision(): receipt=%+v err=%v", decoded, err)
	}
}

func TestPromotionRejectsNonCanonicalIdentityAndDecisionOrdering(t *testing.T) {
	input := testInput(false)
	input.Reference.LineageSHA256 = strings.ToUpper(input.Reference.LineageSHA256)
	if _, err := Evaluate(input); err == nil {
		t.Fatal("uppercase identity digest was accepted")
	}
	input = testInput(false)
	input.Axes[0], input.Axes[1] = input.Axes[1], input.Axes[0]
	if _, err := Evaluate(input); err == nil {
		t.Fatal("non-canonical axis order was accepted")
	}
	input = testInput(false)
	input.Axes[0].Reason = ReasonSafetyRegression
	if _, err := Evaluate(input); err == nil {
		t.Fatal("reason on a passing axis was accepted")
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
	receipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Reasons = nil
	receipt.ReceiptSHA256, err = digestJSON(receiptWithoutDigest(receipt))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeDecision(receipt); err == nil {
		t.Fatal("nil reasons alias was accepted")
	}
}

func TestRollbackIsExactAndCanonical(t *testing.T) {
	request := RollbackRequest{Current: testIdentity("candidate"), Restore: testIdentity("reference"), AuthorizationSHA256: testDigest("rollback-review")}
	receipt, err := PlanRollback(request)
	if err != nil {
		t.Fatalf("PlanRollback(): %v", err)
	}
	if receipt.Restored || receipt.RequestSHA256 != "" || receipt.Decision != DecisionRollback {
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
	mutated := append([]byte(nil), data...)
	mutated = append(mutated[:len(mutated)-2], []byte(`,"future":1}`)...)
	mutated = append(mutated, '\n')
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
	if _, err := store.ApplyRollback(rollback); err != nil {
		t.Fatalf("ApplyRollback(): %v", err)
	}
	current, present, err = store.Current()
	if err != nil || !present || current != promotionReceipt.Reference {
		t.Fatalf("Current after rollback: current=%+v present=%v err=%v", current, present, err)
	}
	if _, err := store.ApplyRollback(rollback); err == nil {
		t.Fatal("rollback replay was accepted")
	}
}

func TestStorePointerBindsContentTransitionDigests(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	promotionReceipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPromotion(promotionReceipt, nil); err != nil {
		t.Fatal(err)
	}
	pointerBytes, err := os.ReadFile(filepath.Join(root, promotionCurrentName))
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := decodePointer(pointerBytes)
	if err != nil {
		t.Fatalf("decode promotion pointer: %v", err)
	}
	transitionBytes, err := os.ReadFile(filepath.Join(root, promotionTransitionDirectory, "promotion-"+promotionReceipt.ReceiptSHA256+".json"))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := decodeTransition(transitionBytes)
	if err != nil {
		t.Fatalf("decode promotion transition: %v", err)
	}
	if pointer.TransitionSHA256 != transition.TransitionSHA256 {
		t.Fatalf("pointer did not bind transition content: pointer=%s transition=%s", pointer.TransitionSHA256, transition.TransitionSHA256)
	}
	if pointer.TransitionSHA256 == transition.RequestSHA256 || transition.PreviousTransitionSHA256 != "" {
		t.Fatalf("pointer/history used request identity or unexpected previous digest: pointer=%+v transition=%+v", pointer, transition)
	}

	rollback, err := PlanRollback(RollbackRequest{Current: promotionReceipt.Candidate, Restore: promotionReceipt.Reference, AuthorizationSHA256: testDigest("metadata-rollback")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRollback(rollback); err != nil {
		t.Fatal(err)
	}
	rollbackPointerBytes, err := os.ReadFile(filepath.Join(root, promotionCurrentName))
	if err != nil {
		t.Fatal(err)
	}
	rollbackPointer, err := decodePointer(rollbackPointerBytes)
	if err != nil {
		t.Fatalf("decode rollback pointer: %v", err)
	}
	rollbackTransitionBytes, err := os.ReadFile(filepath.Join(root, promotionTransitionDirectory, "rollback-"+rollback.ReceiptSHA256+".json"))
	if err != nil {
		t.Fatal(err)
	}
	rollbackTransition, err := decodeTransition(rollbackTransitionBytes)
	if err != nil {
		t.Fatalf("decode rollback transition: %v", err)
	}
	if rollbackPointer.TransitionSHA256 != rollbackTransition.TransitionSHA256 {
		t.Fatalf("rollback pointer did not bind transition content: pointer=%s transition=%s", rollbackPointer.TransitionSHA256, rollbackTransition.TransitionSHA256)
	}
	if rollbackTransition.PreviousTransitionSHA256 != transition.TransitionSHA256 {
		t.Fatalf("rollback history did not bind prior transition: previous=%s promotion=%s", rollbackTransition.PreviousTransitionSHA256, transition.TransitionSHA256)
	}
}

func TestStoreRollbackPreservesReadErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	promotionReceipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPromotion(promotionReceipt, nil); err != nil {
		t.Fatal(err)
	}
	rollback, err := PlanRollback(RollbackRequest{Current: promotionReceipt.Candidate, Restore: promotionReceipt.Reference, AuthorizationSHA256: testDigest("read-error")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, promotionCurrentName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRollback(rollback); err == nil {
		t.Fatal("malformed pointer was accepted")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidReceipt {
		t.Fatalf("malformed pointer code=%q ok=%v err=%v", code, ok, err)
	}
}

func TestStoreTransitionCapacityIsAdmittedBeforeWriting(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	transitionRoot := filepath.Join(root, promotionTransitionDirectory)
	if err := os.Mkdir(transitionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxTransitionEntries; index++ {
		name := filepath.Join(transitionRoot, "reserved-"+testDigest(string(rune(index+1)))+".json")
		if err := os.WriteFile(name, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.recordTransition(store.root, transitionRecord{}); err == nil {
		t.Fatal("transition beyond bounded history was accepted")
	} else if code, ok := CodeOf(err); !ok || code != ErrorLimitExceeded {
		t.Fatalf("transition capacity code=%q ok=%v err=%v", code, ok, err)
	}
	if err := os.WriteFile(filepath.Join(transitionRoot, "overflow.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.readTransitionByDigest(store.root, "promotion", testDigest("missing-transition")); err == nil {
		t.Fatal("over-capacity transition history was scanned")
	} else if code, ok := CodeOf(err); !ok || code != ErrorLimitExceeded {
		t.Fatalf("over-capacity scan code=%q ok=%v err=%v", code, ok, err)
	}
}

func TestStoreRollbackRequiresRecordedPromotionAndRejectsCycleReplay(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPromotion(first, nil); err != nil {
		t.Fatal(err)
	}
	invalid, err := PlanRollback(RollbackRequest{Current: first.Candidate, Restore: testIdentity("unrelated"), AuthorizationSHA256: testDigest("invalid-restore")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRollback(invalid); err == nil {
		t.Fatal("rollback to an unrecorded identity was accepted")
	}
	rollback, err := PlanRollback(RollbackRequest{Current: first.Candidate, Restore: first.Reference, AuthorizationSHA256: testDigest("rollback-cycle")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRollback(rollback); err != nil {
		t.Fatal(err)
	}
	secondInput := testInput(false)
	secondInput.Reviews[0].ReviewSHA256 = testDigest("second-promotion-review")
	second, err := Evaluate(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	current, present, err := store.Current()
	if err != nil || !present {
		t.Fatalf("Current(): current=%+v present=%v err=%v", current, present, err)
	}
	if err := store.ApplyPromotion(second, &current); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRollback(rollback); err == nil {
		t.Fatal("rollback receipt replay after a later promotion was accepted")
	}
}

func TestStoreHeldRootSurvivesRenameWithoutFollowingReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "store")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	moved := filepath.Join(parent, "moved-store")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPromotion(receipt, nil); err != nil {
		t.Fatalf("ApplyPromotion after rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, promotionCurrentName)); err != nil {
		t.Fatalf("held root did not receive pointer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, promotionCurrentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root was mutated: err=%v", err)
	}
}

func TestStoreRejectsUnsafeRoot(t *testing.T) {
	if _, err := NewStore("relative-store"); err == nil {
		t.Fatal("relative store root was accepted")
	}
	unsafe := t.TempDir()
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatalf("Chmod(): %v", err)
	}
	if _, err := NewStore(unsafe); err == nil {
		t.Fatal("world-readable store root was accepted")
	}
	symlinkParent := t.TempDir()
	symlinkTarget := t.TempDir()
	link := filepath.Join(symlinkParent, "store")
	if err := os.Symlink(symlinkTarget, link); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}
	if _, err := NewStore(link); err == nil {
		t.Fatal("symlink store root was accepted")
	}
}

func TestStoreSerializesConcurrentPromotionAndRejectsReplay(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Evaluate(testInput(false))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- store.ApplyPromotion(receipt, nil)
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if code, ok := CodeOf(err); ok && code == ErrorConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent promotion error=%v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent promotion outcomes successes=%d conflicts=%d", successes, conflicts)
	}
}
