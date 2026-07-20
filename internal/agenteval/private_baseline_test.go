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
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

func TestSanitizePrivateAuditDropsCalibrationObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atl-invocations.jsonl")
	observation := strings.Repeat("a", 64)
	data := []byte(`{"command_family":"atl_version","calibration_observation_sha256":"` + observation + `","stdout_bytes":42,"stderr_bytes":0,"exit_code":0}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sanitized, err := sanitizePrivateAudit("atl-invocations.jsonl", path, data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sanitized, []byte("atl_version")) || bytes.Contains(sanitized, []byte(observation)) ||
		string(sanitized) != "{\"stdout_bytes\":42,\"stderr_bytes\":0,\"exit_code\":0}\n" {
		t.Fatalf("calibration observation survived sanitized audit: %s", sanitized)
	}
}

func TestSetPrivateBaselineCreatesCompactImmutableBaselineAndCurrentPointer(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 3)
	privateCanary := "private-canary-must-stay-in-retained-content"
	writePrivateBaselineRunArtifacts(t, fixture, privateCanary)

	summary, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Stored || len(summary.Surfaces) != 1 || summary.Surfaces[0] != SurfaceCLISkill || summary.ArtifactFiles < 4 || !validSHA256(summary.TreeSHA256) {
		t.Fatalf("summary=%+v", summary)
	}
	baselineRoot := filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "baseline-01")
	baselineInfo, err := os.Stat(baselineRoot)
	if err != nil || !privateWorkspaceDirectoryMode(baselineInfo.Mode()) {
		t.Fatalf("baseline mode=%v err=%v", baselineInfo, err)
	}
	for _, relative := range []string{
		"baseline.v1.json", "plan.json", "surfaces/cli-skill/result.json",
		"surfaces/cli-skill/final.json", "surfaces/cli-skill/transcript.jsonl",
		"surfaces/cli-skill/audit/guard-decisions.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(baselineRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"surfaces/cli-skill/agent.stderr", "surfaces/cli-skill/bin",
		"surfaces/cli-skill/workspace", "surfaces/cli-skill/.atl-eval/atl-config",
	} {
		if _, err := os.Stat(filepath.Join(baselineRoot, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("excluded %s exists: %v", relative, err)
		}
	}
	transcript, err := os.ReadFile(filepath.Join(baselineRoot, "surfaces", "cli-skill", "transcript.jsonl"))
	if err != nil || !bytes.Contains(transcript, []byte(privateCanary)) {
		t.Fatalf("retained transcript err=%v data=%q", err, transcript)
	}
	gatewayAudit, err := os.ReadFile(filepath.Join(baselineRoot, "surfaces", "cli-skill", "audit", "gateway-audit.jsonl"))
	if err != nil || bytes.Contains(gatewayAudit, []byte("private-route-canary")) || bytes.Contains(gatewayAudit, []byte("reason")) {
		t.Fatalf("gateway audit was not privacy reduced: err=%v data=%q", err, gatewayAudit)
	}
	pointerData, err := os.ReadFile(filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	pointerInfo, err := os.Stat(filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "current.json"))
	if err != nil || !privateWorkspaceFileMode(pointerInfo.Mode()) {
		t.Fatalf("pointer mode=%v err=%v", pointerInfo, err)
	}
	pointer, err := decodePrivateBaselinePointer(pointerData)
	if err != nil || pointer.Baseline != "baseline-01" || pointer.TreeSHA256 != summary.TreeSHA256 {
		t.Fatalf("pointer=%+v err=%v", pointer, err)
	}

	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	}); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("immutable baseline overwrite err=%v", err)
	}
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-02",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(baselineRoot); err != nil {
		t.Fatalf("previous baseline disappeared: %v", err)
	}
	pointerData, _ = os.ReadFile(filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "current.json"))
	pointer, err = decodePrivateBaselinePointer(pointerData)
	if err != nil || pointer.Baseline != "baseline-02" {
		t.Fatalf("pointer=%+v err=%v", pointer, err)
	}
}

func TestSetPrivateBaselineRepairsPointerForExactCommittedBaseline(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	writePrivateBaselineRunArtifacts(t, fixture, "private-pointer-recovery-canary")
	options := PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source}
	first, err := SetPrivateBaseline(options)
	if err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "current.json")
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	second, err := SetPrivateBaseline(options)
	if err != nil || second.TreeSHA256 != first.TreeSHA256 || !second.Stored {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, _, err := loadPrivateBaseline(fixture.root, fixture.source.ContractSHA256, "current"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrivateBaselineRejectsCurrentPointerTreeMismatch(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	writePrivateBaselineRunArtifacts(t, fixture, "private-pointer-binding-canary")
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "baseline-01", Confirm: PrivateBaselineConfirmation, Source: fixture.source}); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "current.json")
	data, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := decodePrivateBaselinePointer(data)
	if err != nil {
		t.Fatal(err)
	}
	pointer.TreeSHA256 = strings.Repeat("d", 64)
	data, err = encodePrivateBaselinePointer(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(pointerPath, data); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPrivateBaseline(fixture.root, fixture.source.ContractSHA256, "current"); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("stale pointer err=%v", err)
	}
}

func TestSetPrivateBaselineRequiresConfirmationAssessmentStablePlanAndFreeLock(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 3)
	writePrivateBaselineRunArtifacts(t, fixture, "answer")
	base := PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01", Source: fixture.source}

	if _, err := SetPrivateBaseline(base); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("missing confirmation err=%v", err)
	}
	stale := base
	stale.Confirm = PrivateBaselineConfirmation
	stale.Source.PlanSHA256 = strings.Repeat("f", 64)
	if _, err := SetPrivateBaseline(stale); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("stale plan err=%v", err)
	}
	qualitative := base
	qualitative.Confirm = PrivateBaselineConfirmation
	qualitative.Source.Surfaces[0].QualitativeRequired = true
	if _, err := SetPrivateBaseline(qualitative); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("missing assessment err=%v", err)
	}

	lock, acquired, err := safepath.TryLockFileWithin(fixture.root, filepath.Join(fixture.root, privateWorkspaceLockPath), 0o600)
	if err != nil || !acquired {
		t.Fatalf("lock acquired=%v err=%v", acquired, err)
	}
	defer func() { _ = lock.Unlock() }()
	base.Confirm = PrivateBaselineConfirmation
	if _, err := SetPrivateBaseline(base); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("concurrent baseline err=%v", err)
	}
}

func TestPrivateBaselineRechecksQualitativeContentBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, privateBaselineFixture)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "result bytes", mutate: func(t *testing.T, fixture privateBaselineFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.surfaceDirectory, "result.json"), "\n")
		}},
		{name: "final response", mutate: func(t *testing.T, fixture privateBaselineFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.surfaceDirectory, "final.json"), "\n")
		}},
		{name: "rubric", mutate: func(t *testing.T, fixture privateBaselineFixture) {
			data, err := os.ReadFile(fixture.source.Surfaces[0].RubricPath)
			if err != nil {
				t.Fatal(err)
			}
			rubric, err := DecodeRubric(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			rubric.Criteria[0].Description += " Changed."
			data, err = json.MarshalIndent(rubric, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.source.Surfaces[0].RubricPath, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateBaselineFixture(t, 3)
			writePrivateBaselineRunArtifacts(t, fixture, "answer")
			writePrivateBaselineAssessment(t, &fixture)
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			_, err := SetPrivateBaseline(PrivateBaselineSetOptions{
				Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
				Confirm: PrivateBaselineConfirmation, Source: fixture.source,
			})
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && !errors.Is(err, ErrPrivateBaselineRejected) {
				t.Fatalf("tampered assessment binding err=%v", err)
			}
		})
	}
}

func TestPrivateBaselineAcceptsUnanimousPanelFailureButRejectsDisagreement(t *testing.T) {
	for _, test := range []struct {
		name          string
		scores        [][2]int
		wantStatus    string
		wantPromotion bool
	}{
		{name: "unanimous failure", scores: [][2]int{{2, 1}, {2, 1}, {2, 1}}, wantStatus: "fail", wantPromotion: true},
		{name: "disagreement", scores: [][2]int{{2, 4}, {3, 4}, {4, 4}}, wantStatus: "disagreement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateBaselineFixture(t, 3)
			writePrivateBaselineRunArtifacts(t, fixture, "answer")
			resultPath := filepath.Join(fixture.surfaceDirectory, "result.json")
			resultData, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := DecodeResult(bytes.NewReader(resultData))
			if err != nil {
				t.Fatal(err)
			}
			finalData, err := os.ReadFile(filepath.Join(fixture.surfaceDirectory, "final.json"))
			if err != nil {
				t.Fatal(err)
			}
			rubricData, err := os.ReadFile(fixture.source.Surfaces[0].RubricPath)
			if err != nil {
				t.Fatal(err)
			}
			rubric, err := DecodeRubric(bytes.NewReader(rubricData))
			if err != nil {
				t.Fatal(err)
			}
			policy := panelPolicy(9999)
			reviews := panelReviews(t, result, resultData, finalData, rubric, test.scores, nil)
			assessed, err := AssessQualitativeReviewSet(result, resultData, finalData, rubric, policy, reviews)
			if err != nil {
				t.Fatal(err)
			}
			if assessed.QualitativeReviewSet.Status != test.wantStatus || assessed.Status != "fail" {
				t.Fatalf("assessment=%+v", assessed)
			}

			panelContract := privateQualitativeReviewPanelContract{
				Method: policy.Method, MaxCriterionRangeBPS: policy.MaxCriterionRangeBPS,
				Reviewers: []Reviewer{reviews[0].Reviewer, reviews[1].Reviewer, reviews[2].Reviewer},
			}
			contractData, err := encodePrivateQualitativeReviewPanelContract(panelContract)
			if err != nil {
				t.Fatal(err)
			}
			contractPath := filepath.Join(fixture.source.RunRoot, "contracts", SurfaceCLISkill, "qualitative-panel.json")
			if err := os.WriteFile(contractPath, contractData, 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.source.Surfaces[0].QualitativeRequired = true
			fixture.source.Surfaces[0].QualitativePanelContractPath = contractPath
			fixture.source.Surfaces[0].QualitativePanelContractSHA256 = sha256HexBytes(contractData)
			writePrivateBaselineResult(t, filepath.Join(fixture.surfaceDirectory, "reviewed-result.json"), assessed)

			_, err = SetPrivateBaseline(PrivateBaselineSetOptions{
				Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-panel",
				Confirm: PrivateBaselineConfirmation, Source: fixture.source,
			})
			if test.wantPromotion && err != nil {
				t.Fatalf("unanimous panel failure was not promotable: %v", err)
			}
			if !test.wantPromotion && (!errors.Is(err, ErrPrivateBaselineRejected) || !strings.Contains(err.Error(), "assessment_disagreement")) {
				t.Fatalf("panel disagreement promotion err=%v", err)
			}
		})
	}
}

func TestSetPrivateBaselineHonorsTranscriptRetentionPolicy(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 3)
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.Retention.KeepCompletedRunSetsPerAlias = 3
	manifest.Retention.RetainBaselineTranscripts = false
	writePrivateWorkspaceManifestForTest(t, fixture.root, manifest)
	writePrivateBaselineRunArtifacts(t, fixture, "private-transcript")
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(fixture.root, "baselines", fixture.source.ContractSHA256, "baseline-01", "surfaces", "cli-skill", "transcript.jsonl")
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatalf("transcript retained against policy: %v", err)
	}
}

func TestSetPrivateBaselineRefusesSurfaceSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	fixture := newPrivateBaselineFixture(t, 3)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "result.json"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fixture.surfaceDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.surfaceDirectory); err != nil {
		t.Fatal(err)
	}
	_, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	})
	if !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("symlink escape err=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(outside, "result.json"))
	if readErr != nil || string(data) != "outside" {
		t.Fatalf("outside changed data=%q err=%v", data, readErr)
	}
}

func TestComparePrivateBaselineReturnsOnlySanitizedCompatibleDeltas(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 3)
	writePrivateBaselineRunArtifacts(t, fixture, "secret-answer-canary")
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "baseline-01",
		Confirm: PrivateBaselineConfirmation, Source: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	result := privateBaselineResult(t, SurfaceCLISkill)
	result.Metrics.OutputBytes += 2
	result.Runtime.ATLVersion = "candidate-version"
	writePrivateBaselineResult(t, filepath.Join(fixture.surfaceDirectory, "result.json"), result)

	comparison, err := ComparePrivateBaseline(PrivateCompareOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "current", Candidate: fixture.source,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Compatible || comparison.TaskClass != result.TaskClass || len(comparison.Surfaces) != 1 {
		t.Fatalf("comparison=%+v", comparison)
	}
	foundOutputBytes := false
	for _, metric := range comparison.Surfaces[0].Metrics {
		if metric.Metric == "output_bytes" {
			foundOutputBytes = metric.Delta == 2
		}
	}
	if !foundOutputBytes {
		t.Fatalf("metrics=%+v", comparison.Surfaces[0].Metrics)
	}
	encoded, _ := json.Marshal(comparison)
	for _, forbidden := range []string{"secret-answer-canary", result.ScenarioID, fixture.root, fixture.source.PlanID, "rubric"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("comparison leaked %q: %s", forbidden, encoded)
		}
	}

	result.Runtime.Model = "different-model"
	writePrivateBaselineResult(t, filepath.Join(fixture.surfaceDirectory, "result.json"), result)
	if _, err := ComparePrivateBaseline(PrivateCompareOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository, Baseline: "current", Candidate: fixture.source,
	}); !errors.Is(err, ErrPrivateBaselineRejected) {
		t.Fatalf("runtime mismatch err=%v", err)
	}
}

func TestPrivatePruneIsHashBoundRetentionAwareAndPreservesProtectedTrees(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	canary := "private-prune-canary"
	runs := []PrivateRunLifecycle{
		{RunID: "run-11111111111111111111111111111111", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 1},
		{RunID: "run-22222222222222222222222222222222", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 2},
		{RunID: "run-33333333333333333333333333333333", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 3},
		{RunID: "run-44444444444444444444444444444444", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "active"},
	}
	for _, run := range runs {
		directory := filepath.Join(fixture.root, "runs", run.RunID)
		mustPrivateMkdirAll(t, directory)
		if err := os.WriteFile(filepath.Join(directory, canary+".txt"), []byte(canary+run.RunID), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unknown := filepath.Join(fixture.root, "runs", "run-55555555555555555555555555555555")
	mustPrivateMkdirAll(t, unknown)
	if err := os.WriteFile(filepath.Join(unknown, "incomplete"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(fixture.root, "baselines", "current.json")
	if err := os.WriteFile(protected, []byte("keep-baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	planProtected := filepath.Join(fixture.root, "plans", fixture.source.PlanID+".state.json")
	if err := os.WriteFile(planProtected, []byte("keep-plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := func(string) (PrivatePruneInventory, error) {
		return PrivatePruneInventory{Runs: append([]PrivateRunLifecycle(nil), runs...)}, nil
	}
	options := PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Inventory: loader}
	preview, err := PreviewPrivatePrune(options)
	if err != nil {
		t.Fatal(err)
	}
	if preview.EligibleRunSets != 2 || preview.EligibleFiles != 2 || !validSHA256(preview.InventorySHA256) {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), runs[0].RunID) || strings.Contains(string(encoded), fixture.root) {
		t.Fatalf("preview leaked private inventory: %s", encoded)
	}
	if _, err := ApplyPrivatePrune(options); !errors.Is(err, ErrPrivatePruneRejected) {
		t.Fatalf("missing confirmation err=%v", err)
	}

	stalePath := filepath.Join(fixture.root, "runs", runs[0].RunID, canary+".txt")
	if err := os.WriteFile(stalePath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := options
	stale.Confirm = PrivatePruneConfirmation
	stale.ExpectedInventorySHA256 = preview.InventorySHA256
	if _, err := ApplyPrivatePrune(stale); !errors.Is(err, ErrPrivatePruneRejected) {
		t.Fatalf("stale hash err=%v", err)
	}
	preview, err = PreviewPrivatePrune(options)
	if err != nil {
		t.Fatal(err)
	}
	apply := options
	apply.Confirm = PrivatePruneConfirmation
	apply.ExpectedInventorySHA256 = preview.InventorySHA256
	summary, err := ApplyPrivatePrune(apply)
	if err != nil || summary.PrunedRunSets != 2 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	for _, run := range runs[:2] {
		pruned, err := inspectPrivatePrunedRun(fixture.root, run.RunID, run.PlanID)
		if err != nil || !pruned {
			t.Fatalf("old run was not compacted to a tombstone: pruned=%v err=%v", pruned, err)
		}
	}
	for _, path := range []string{
		filepath.Join(fixture.root, "runs", runs[2].RunID), filepath.Join(fixture.root, "runs", runs[3].RunID),
		unknown, protected, planProtected,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected path removed: %v", err)
		}
	}
}

func TestPrivatePruneRejectsConcurrentLockAndSymlinkTree(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	run := PrivateRunLifecycle{RunID: "run-11111111111111111111111111111111", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 1}
	newer := PrivateRunLifecycle{RunID: "run-22222222222222222222222222222222", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 2}
	for _, item := range []PrivateRunLifecycle{run, newer} {
		mustPrivateMkdirAll(t, filepath.Join(fixture.root, "runs", item.RunID))
	}
	loader := func(string) (PrivatePruneInventory, error) {
		return PrivatePruneInventory{Runs: []PrivateRunLifecycle{run, newer}}, nil
	}
	options := PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Inventory: loader}

	lock, acquired, err := safepath.TryLockFileWithin(fixture.root, filepath.Join(fixture.root, privateWorkspaceLockPath), 0o600)
	if err != nil || !acquired {
		t.Fatalf("lock acquired=%v err=%v", acquired, err)
	}
	if _, err := PreviewPrivatePrune(options); !errors.Is(err, ErrPrivatePruneRejected) {
		t.Fatalf("concurrent preview err=%v", err)
	}
	_ = lock.Unlock()

	if runtime.GOOS == "windows" {
		return
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.root, "runs", run.RunID, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewPrivatePrune(options); !errors.Is(err, ErrPrivatePruneRejected) {
		t.Fatalf("symlink preview err=%v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside changed data=%q err=%v", data, err)
	}
}

func TestDefaultPrivatePruneKeepsLifecycleHealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	manifestData, err := os.ReadFile(filepath.Join(fixture.root, PrivateWorkspaceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Retention.KeepCompletedRunSetsPerAlias = 1
	manifest.Retention.MaxCandidateAgeDays = 365
	manifest.Retention.MaxCandidateBytes = 1 << 30
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(fixture.root, PrivateWorkspaceManifestName), manifestData); err != nil {
		t.Fatal(err)
	}

	first := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(first)); err != nil {
		t.Fatal(err)
	}
	second := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(second)); err != nil {
		t.Fatal(err)
	}

	options := PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Now: fixture.now.Add(24 * time.Hour)}
	preview, err := PreviewPrivatePrune(options)
	if err != nil || preview.EligibleRunSets != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	options.Confirm = PrivatePruneConfirmation
	options.ExpectedInventorySHA256 = preview.InventorySHA256
	if _, err := ApplyPrivatePrune(options); err != nil {
		t.Fatal(err)
	}
	report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository)
	if err != nil || !report.Healthy || report.Counts.PrunedRuns != 1 || report.Counts.CompletedRuns != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	references, err := InspectPrivatePlanRunReferences(fixture.root, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	prunedPlan := ""
	for _, reference := range references {
		if reference.State == "pruned" {
			prunedPlan = reference.PlanID
		}
	}
	if prunedPlan == "" {
		t.Fatal("no pruned lifecycle reference found")
	}
	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, prunedPlan); err == nil {
		t.Fatal("pruned run remained promotable")
	}
	next, err := PreviewPrivatePrune(PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Now: fixture.now.Add(24 * time.Hour)})
	if err != nil || next.EligibleRunSets != 0 {
		t.Fatalf("next preview=%+v err=%v", next, err)
	}
}

func TestPrivatePruneRecoversRenamedRunBeforeRecheckingReviewedInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Retention.KeepCompletedRunSetsPerAlias = 1
	manifest.Retention.MaxCandidateAgeDays = 365
	manifest.Retention.MaxCandidateBytes = 1 << 30
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		plan := fixture.createPlan(t)
		if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(plan)); err != nil {
			t.Fatal(err)
		}
	}
	options := PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Now: fixture.now.Add(24 * time.Hour)}
	loader := privatePlanPruneInventoryLoader(fixture.repository)
	preview, candidates, err := privatePruneInventory(fixture.root, loader, privatePruneNow(options.Now))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("preview=%+v candidates=%d err=%v", preview, len(candidates), err)
	}
	candidate := candidates[0]
	intent := privatePruneIntent{SchemaVersion: 1, RunID: candidate.runID, PlanID: candidate.planID,
		OriginalTreeSHA256: candidate.hash, InventorySHA256: preview.InventorySHA256}
	intentData, _ := json.MarshalIndent(intent, "", "  ")
	intentPath, stagePath := privatePruneTransactionPaths(fixture.root, candidate.runID)
	if err := safepath.WriteFileExclusiveWithin(fixture.root, intentPath, append(intentData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := safepath.RenameWithin(fixture.root, candidate.path, stagePath); err != nil {
		t.Fatal(err)
	}
	options.Confirm = PrivatePruneConfirmation
	options.ExpectedInventorySHA256 = preview.InventorySHA256
	if _, err := ApplyPrivatePrune(options); !errors.Is(err, ErrPrivatePruneRejected) || !strings.Contains(err.Error(), "stale_plan") {
		t.Fatalf("recovered apply err=%v", err)
	}
	if pruned, err := inspectPrivatePrunedRun(fixture.root, candidate.runID, candidate.planID); err != nil || !pruned {
		t.Fatalf("pruned=%v err=%v", pruned, err)
	}
	for _, path := range []string{intentPath, stagePath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("transaction artifact remains at %q: %v", filepath.Base(path), err)
		}
	}
	report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository)
	if err != nil || !report.Healthy || report.Counts.PrunedRuns != 1 || report.Counts.CompletedRuns != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestPrivatePruneRecoveryRejectsStagedTreeDrift(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	old := PrivateRunLifecycle{RunID: "run-11111111111111111111111111111111", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 1}
	newest := PrivateRunLifecycle{RunID: "run-22222222222222222222222222222222", RunSetAlias: "case-01", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: 2}
	for _, run := range []PrivateRunLifecycle{old, newest} {
		directory := filepath.Join(fixture.root, "runs", run.RunID)
		mustPrivateMkdirAll(t, directory)
		writeTestFile(t, filepath.Join(directory, "result.json"), run.RunID+"\n", 0o600)
	}
	loader := func(string) (PrivatePruneInventory, error) {
		return PrivatePruneInventory{Runs: []PrivateRunLifecycle{old, newest}}, nil
	}
	options := PrivatePruneOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Inventory: loader}
	preview, candidates, err := privatePruneInventory(fixture.root, loader, privatePruneNow(options.Now))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("preview=%+v candidates=%d err=%v", preview, len(candidates), err)
	}
	candidate := candidates[0]
	intent := privatePruneIntent{SchemaVersion: 1, RunID: candidate.runID, PlanID: candidate.planID,
		OriginalTreeSHA256: candidate.hash, InventorySHA256: preview.InventorySHA256}
	data, _ := json.MarshalIndent(intent, "", "  ")
	intentPath, stagePath := privatePruneTransactionPaths(fixture.root, candidate.runID)
	if err := safepath.WriteFileExclusiveWithin(fixture.root, intentPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := safepath.RenameWithin(fixture.root, candidate.path, stagePath); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(stagePath, "unexpected.json"), "drift\n", 0o600)
	options.Confirm = PrivatePruneConfirmation
	options.ExpectedInventorySHA256 = preview.InventorySHA256
	if _, err := ApplyPrivatePrune(options); !errors.Is(err, ErrPrivatePruneRejected) || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("drift recovery err=%v", err)
	}
	if _, err := os.Stat(stagePath); err != nil {
		t.Fatalf("drifted evidence was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "runs", candidate.runID, privatePrunedRunName)); !os.IsNotExist(err) {
		t.Fatalf("drifted staged tree received a tombstone: %v", err)
	}
}

func TestPrivatePruneAppliesAgeAndByteRetentionButPreservesNewestPerAlias(t *testing.T) {
	t.Run("age", func(t *testing.T) {
		fixture := newPrivateBaselineFixture(t, 10)
		manifest := DefaultPrivateWorkspaceManifest()
		manifest.Retention.KeepCompletedRunSetsPerAlias = 10
		manifest.Retention.MaxCandidateAgeDays = 2
		writePrivateWorkspaceManifestForTest(t, fixture.root, manifest)
		now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
		runs := []PrivateRunLifecycle{
			{RunID: "run-11111111111111111111111111111111", RunSetAlias: "age-set", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-72 * time.Hour).UnixNano()},
			{RunID: "run-22222222222222222222222222222222", RunSetAlias: "age-set", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-time.Hour).UnixNano()},
			{RunID: "run-33333333333333333333333333333333", RunSetAlias: "old-singleton", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-96 * time.Hour).UnixNano()},
		}
		for _, run := range runs {
			mustPrivateMkdirAll(t, filepath.Join(fixture.root, "runs", run.RunID))
		}
		preview, err := PreviewPrivatePrune(PrivatePruneOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository, Now: now,
			Inventory: func(string) (PrivatePruneInventory, error) { return PrivatePruneInventory{Runs: runs}, nil },
		})
		if err != nil || preview.EligibleRunSets != 1 {
			t.Fatalf("preview=%+v err=%v", preview, err)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		fixture := newPrivateBaselineFixture(t, 10)
		manifest := DefaultPrivateWorkspaceManifest()
		manifest.Retention.KeepCompletedRunSetsPerAlias = 10
		manifest.Retention.MaxCandidateAgeDays = 365
		manifest.Retention.MaxCandidateBytes = 1 << 20
		writePrivateWorkspaceManifestForTest(t, fixture.root, manifest)
		now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
		runs := []PrivateRunLifecycle{
			{RunID: "run-11111111111111111111111111111111", RunSetAlias: "byte-set", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-3 * time.Hour).UnixNano()},
			{RunID: "run-22222222222222222222222222222222", RunSetAlias: "byte-set", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-2 * time.Hour).UnixNano()},
			{RunID: "run-33333333333333333333333333333333", RunSetAlias: "byte-set", PlanID: fixture.source.PlanID, State: "completed", CompletedOrder: now.Add(-time.Hour).UnixNano()},
		}
		for _, run := range runs {
			directory := filepath.Join(fixture.root, "runs", run.RunID)
			mustPrivateMkdirAll(t, directory)
			if err := os.WriteFile(filepath.Join(directory, "candidate.bin"), bytes.Repeat([]byte{'x'}, 600<<10), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		preview, err := PreviewPrivatePrune(PrivatePruneOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository, Now: now,
			Inventory: func(string) (PrivatePruneInventory, error) { return PrivatePruneInventory{Runs: runs}, nil },
		})
		if err != nil || preview.EligibleRunSets != 2 || preview.EligibleBytes != 1200<<10 {
			t.Fatalf("preview=%+v err=%v", preview, err)
		}
	})
}

func TestPrivatePruneUsesValidatedPlanInventoryByDefault(t *testing.T) {
	fixture := newPrivateBaselineFixture(t, 1)
	preview, err := PreviewPrivatePrune(PrivatePruneOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.EligibleRunSets != 0 || preview.EligibleFiles != 0 || preview.EligibleBytes != 0 || !validSHA256(preview.InventorySHA256) {
		t.Fatalf("preview=%+v", preview)
	}
}

type privateBaselineFixture struct {
	root             string
	repository       string
	surfaceDirectory string
	source           PrivateBaselineSource
}

func newPrivateBaselineFixture(t *testing.T, retention int) privateBaselineFixture {
	t.Helper()
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.Retention.KeepCompletedRunSetsPerAlias = retention
	if _, err := InitPrivateWorkspace(root, repository, manifest); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonicalRoot
	planID := "pln-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runID := "run-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	planPath := filepath.Join(root, "plans", planID+".json")
	planData := []byte("{\"schema_version\":1}\n")
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(root, "runs", runID)
	surfaceDirectory := filepath.Join(runRoot, "raw", "surface-cli")
	rubricPath := filepath.Join(runRoot, "contracts", SurfaceCLISkill, "rubric.json")
	rubric := testRubric(validScenario().ID)
	if err := os.MkdirAll(filepath.Dir(rubricPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONTestFile(t, rubricPath, rubric)
	return privateBaselineFixture{
		root: root, repository: repository, surfaceDirectory: surfaceDirectory,
		source: PrivateBaselineSource{
			PlanID: planID, PlanPath: planPath, PlanSHA256: sha256HexBytes(planData),
			ContractSHA256: strings.Repeat("c", 64), RunID: runID, RunRoot: runRoot,
			Completed: true, Immutable: true,
			Surfaces: []PrivateBaselineSurfaceSource{{Surface: SurfaceCLISkill, RunDirectory: surfaceDirectory,
				RubricPath: rubricPath, RubricSHA256: rubricSHA256(rubric)}},
		},
	}
}

func writePrivateBaselineRunArtifacts(t *testing.T, fixture privateBaselineFixture, content string) {
	t.Helper()
	mustPrivateMkdirAll(t, fixture.surfaceDirectory)
	writePrivateBaselineResult(t, filepath.Join(fixture.surfaceDirectory, "result.json"), privateBaselineResult(t, SurfaceCLISkill))
	for name, data := range map[string][]byte{
		"final.json":       []byte(`{"answer":` + strconvQuote(content) + `}\n`),
		"transcript.jsonl": []byte(`{"message":` + strconvQuote(content) + `}\n`),
		"agent.stderr":     {},
	} {
		if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{
		filepath.Join(fixture.surfaceDirectory, "bin"), filepath.Join(fixture.surfaceDirectory, "workspace"),
		filepath.Join(fixture.surfaceDirectory, ".atl-eval", "atl-config"),
	} {
		mustPrivateMkdirAll(t, directory)
	}
	if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, "bin", "agent"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, "workspace", "evidence"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, ".atl-eval", "atl-config", "credentials.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, ".atl-eval", "guard-decisions.jsonl"), []byte("{\"decision\":\"allow\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := strings.Join([]string{
		`{"sequence":1,"phase":"preflight","service":"jira","route":"private-route-canary","method":"GET","request_hmac":"` + strings.Repeat("a", 64) + `","decision":"forward"}`,
		`{"sequence":2,"phase":"complete","service":"jira","route":"private-route-canary","method":"GET","request_hmac":"` + strings.Repeat("a", 64) + `","decision":"allow","status_class":"2xx","response_bytes":12,"duration_ms":1}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fixture.surfaceDirectory, ".atl-eval", "gateway-audit.jsonl"), []byte(gateway), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePrivateBaselineAssessment(t *testing.T, fixture *privateBaselineFixture) {
	t.Helper()
	resultData, err := os.ReadFile(filepath.Join(fixture.surfaceDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil {
		t.Fatal(err)
	}
	finalData, err := os.ReadFile(filepath.Join(fixture.surfaceDirectory, "final.json"))
	if err != nil {
		t.Fatal(err)
	}
	rubric := testRubric(result.ScenarioID)
	rubricPath := fixture.source.Surfaces[0].RubricPath
	rubricData, err := json.MarshalIndent(rubric, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(rubricPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rubricPath, append(rubricData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	review, err := NewReviewTemplate(result, resultData, finalData, rubric, Reviewer{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range review.Criteria {
		review.Criteria[index].Score = rubric.Criteria[index].Maximum
	}
	assessed, err := AssessQualitative(result, resultData, finalData, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateBaselineResult(t, filepath.Join(fixture.surfaceDirectory, "reviewed-result.json"), assessed)
	fixture.source.Surfaces[0].QualitativeRequired = true
}

func privateBaselineResult(t *testing.T, surface string) Result {
	t.Helper()
	scenario := validScenario()
	scenario.DataClass = "private-local"
	observation := validObservation()
	observation.Surface = surface
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCompatiblePrivateResultsBindsPromptRouting(t *testing.T) {
	baseline := privateBaselineResult(t, SurfaceCLISkill)
	baseline.Runtime.Provider = "codex"
	baseline.Runtime.SkillActivation = SkillActivationImplicit
	baseline.Runtime.PromptContractSHA256 = strings.Repeat("a", 64)

	identical := baseline
	if !compatiblePrivateResults(baseline, identical) {
		t.Fatal("identical prompt routing was incompatible")
	}

	for _, activation := range []string{SkillActivationExplicit, SkillActivationDeveloper, SkillActivationCombined} {
		other := baseline
		other.Runtime.SkillActivation = activation
		if compatiblePrivateResults(baseline, other) {
			t.Fatalf("%s and implicit activation were compatible", activation)
		}
	}

	changedPrompt := baseline
	changedPrompt.Runtime.PromptContractSHA256 = strings.Repeat("b", 64)
	if compatiblePrivateResults(baseline, changedPrompt) {
		t.Fatal("different prompt contracts were compatible")
	}

	legacy := baseline
	legacy.Runtime.SkillActivation = ""
	legacy.Runtime.PromptContractSHA256 = ""
	if compatiblePrivateResults(baseline, legacy) {
		t.Fatal("legacy and activation-bound results were compatible")
	}
}

func writePrivateBaselineResult(t *testing.T, path string, result Result) {
	t.Helper()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustPrivateMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	current := path
	for {
		if err := os.Chmod(current, 0o700); err != nil {
			t.Fatal(err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
		if filepath.Base(current) == "runs" {
			break
		}
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writePrivateWorkspaceManifestForTest(t *testing.T, root string, manifest PrivateWorkspaceManifest) {
	t.Helper()
	data, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileWithin(root, filepath.Join(root, PrivateWorkspaceManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
