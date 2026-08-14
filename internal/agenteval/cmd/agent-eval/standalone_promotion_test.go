package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func standalonePromotionTestDigest(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func standalonePromotionTestIdentity(seed string) agenteval.PromotionIdentity {
	return agenteval.PromotionIdentity{LineageSHA256: standalonePromotionTestDigest(seed + ":lineage"), SkillSHA256: standalonePromotionTestDigest(seed + ":skill"), EvaluationSHA256: standalonePromotionTestDigest(seed + ":evaluation"), GraderSHA256: standalonePromotionTestDigest(seed + ":grader"), HoldoutSHA256: standalonePromotionTestDigest(seed + ":holdout"), RuntimeSHA256: standalonePromotionTestDigest(seed + ":runtime")}
}

func standalonePromotionTestComparison(blocking bool) agenteval.PromotionComparisonInput {
	reference, candidate := standalonePromotionTestIdentity("reference"), standalonePromotionTestIdentity("candidate")
	reviews := []agenteval.PromotionComponentReview{
		{Component: agenteval.PromotionComponentSkill, ReferenceSHA256: reference.SkillSHA256, CandidateSHA256: candidate.SkillSHA256, ReviewSHA256: standalonePromotionTestDigest("review-skill"), Reviewed: true},
		{Component: agenteval.PromotionComponentEvaluation, ReferenceSHA256: reference.EvaluationSHA256, CandidateSHA256: candidate.EvaluationSHA256, ReviewSHA256: standalonePromotionTestDigest("review-evaluation"), Reviewed: true},
		{Component: agenteval.PromotionComponentGrader, ReferenceSHA256: reference.GraderSHA256, CandidateSHA256: candidate.GraderSHA256, ReviewSHA256: standalonePromotionTestDigest("review-grader"), Reviewed: true},
		{Component: agenteval.PromotionComponentHoldout, ReferenceSHA256: reference.HoldoutSHA256, CandidateSHA256: candidate.HoldoutSHA256, ReviewSHA256: standalonePromotionTestDigest("review-holdout"), Reviewed: true},
	}
	axes := make([]agenteval.PromotionAxisResult, 0, 6)
	for _, axis := range []agenteval.PromotionAxis{agenteval.PromotionAxisSafety, agenteval.PromotionAxisCoverage, agenteval.PromotionAxisRuntime, agenteval.PromotionAxisQuality, agenteval.PromotionAxisNegativeLift, agenteval.PromotionAxisResource} {
		state := agenteval.PromotionAxisState(agenteval.PromotionAxisPass)
		blockingValue := false
		reason := agenteval.PromotionReason("")
		if blocking && axis == agenteval.PromotionAxisSafety {
			state, blockingValue, reason = agenteval.PromotionAxisState(agenteval.PromotionAxisFail), true, agenteval.PromotionReasonSafetyRegression
		}
		axes = append(axes, agenteval.PromotionAxisResult{Axis: axis, State: state, Blocking: blockingValue, Reason: reason})
	}
	return agenteval.PromotionComparisonInput{Reference: reference, Candidate: candidate, Reviews: reviews, Axes: axes}
}

func TestStandalonePromotionAndExactRollbackWholeProcess(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "store")
	if err := os.Mkdir(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	comparisonData, err := agenteval.EncodePromotionComparison(standalonePromotionTestComparison(false))
	if err != nil {
		t.Fatalf("EncodePromotionComparison(): %v", err)
	}
	comparisonPath := filepath.Join(root, "comparison.json")
	if err := os.WriteFile(comparisonPath, comparisonData, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{"promote", "--comparison", comparisonPath, "--store", storeRoot, "--confirm", "PROMOTE"}, "")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"decision":"promote"`) {
		t.Fatalf("promotion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := agenteval.NewPromotionStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	current, present, err := store.Current()
	if err != nil || !present || current != standalonePromotionTestComparison(false).Candidate {
		t.Fatalf("current after promotion: %+v present=%v err=%v", current, present, err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"promote", "--comparison", comparisonPath, "--store", storeRoot, "--confirm", "PROMOTE"}, "")
	if code != standalonePolicyDeniedError.code || stdout != "" {
		t.Fatalf("replayed promotion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	rollback, err := agenteval.PlanPromotionRollback(agenteval.PromotionRollbackRequest{Current: current, Restore: standalonePromotionTestComparison(false).Reference, AuthorizationSHA256: standalonePromotionTestDigest("rollback")})
	if err != nil {
		t.Fatal(err)
	}
	rollbackData, err := agenteval.EncodePromotionRollback(rollback)
	if err != nil {
		t.Fatal(err)
	}
	rollbackPath := filepath.Join(root, "rollback.json")
	if err := os.WriteFile(rollbackPath, rollbackData, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"rollback", "--receipt", rollbackPath, "--store", storeRoot, "--confirm", "ROLLBACK"}, "")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"decision":"rollback"`) {
		t.Fatalf("rollback: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	current, present, err = store.Current()
	if err != nil || !present || current != standalonePromotionTestComparison(false).Reference {
		t.Fatalf("current after rollback: %+v present=%v err=%v", current, present, err)
	}
}

func TestStandalonePromotionRefusesAndPersistsCandidateDecision(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "store")
	if err := os.Mkdir(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := agenteval.EncodePromotionComparison(standalonePromotionTestComparison(true))
	if err != nil {
		t.Fatal(err)
	}
	comparisonPath := filepath.Join(root, "comparison.json")
	if err := os.WriteFile(comparisonPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{"promote", "--comparison", comparisonPath, "--store", storeRoot, "--confirm", "PROMOTE"}, "")
	if code != 9 || stdout != "" {
		t.Fatalf("refusal: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "promotion_refused") {
		t.Fatalf("refusal did not expose stable failure kind: %q", stderr)
	}
	entries, err := os.ReadDir(filepath.Join(storeRoot, "decisions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("refusal receipt was not retained: entries=%v err=%v", entries, err)
	}
}
