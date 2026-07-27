package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateFindingScorecardReconcilesFixedRegressionWithoutLeakingReferences(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addPrimary(t, 3, true)
	holdout := fixture.addHoldout(t, 4, true)
	assessmentDigest := fixture.storeAssessment(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
		Primary: primary, Holdout: holdout})
	failure := privateSamplingResult(t, "jira.primary-evidence", false)
	failure.Metrics.InputTokens = 100
	failure.Coverage["input_tokens"] = true
	failureRef := fixture.addResult(t, 9, "failure-capture", failure, strings.Repeat("3", 64))
	writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
		FindingID: "finding-001", Failure: failureRef,
		FailureClass: PrivateFailureModel, ProductIssue: 123, PullRequest: 456,
		ChangedContractSHA256: strings.Repeat("1", 64),
		Regression:            &primary[0],
		Decision:              PrivateFindingDecisionFixed,
	}}})
	writePrivateFindingAcceptance(t, fixture.root, PrivateFindingAcceptanceIndex{SchemaVersion: 1,
		Entries: []PrivateFindingAcceptanceEntry{{FindingID: "finding-001", AssessmentSHA256: assessmentDigest}}})
	before := privateCheckpointTree(t, fixture.root)
	report, err := buildPrivateFindingScorecard(PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository}, fixture.dependencies().load)
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only scorecard changed the workspace")
	}
	if report.SchemaVersion != PrivateFindingScorecardSchemaVersion || !report.Reconciled || report.Findings != 1 || report.Regressions != 1 || report.SamplingAssessments != 1 ||
		report.LinkedIssues != 1 || report.LinkedPullRequests != 1 || report.Decisions.Fixed != 1 || len(report.Groups) != 1 {
		t.Fatalf("report=%+v", report)
	}
	group := report.Groups[0]
	if group.TaskClass != failure.TaskClass || group.FailureClass != PrivateFailureModel || group.Failure.Statuses.Fail != 1 ||
		group.Regression.Statuses.Pass != 1 || group.Sampling.Assessments != 1 || group.Sampling.Primary.Observed != 3 ||
		group.Sampling.Primary.Statuses.Pass != 3 || group.Sampling.Holdout.Observed != 1 || group.Sampling.Holdout.Statuses.Pass != 1 ||
		group.Failure.Metrics.InputTokens.ObservedRuns != 1 || group.Failure.Metrics.InputTokens.P50 != 100 {
		t.Fatalf("group=%+v", group)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{failureRef.PlanID, failureRef.Baseline, "finding-001", "123", "456", failure.ScenarioID,
		failure.Runtime.Model, assessmentDigest}
	for _, ref := range append(append([]PrivateFindingRunRef{}, primary...), holdout...) {
		secrets = append(secrets, ref.PlanID, ref.Baseline)
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("private reference %q leaked in %s", secret, encoded)
		}
	}
	second, err := buildPrivateFindingScorecard(PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository}, fixture.dependencies().load)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, _ := json.Marshal(second)
	if !bytes.Equal(encoded, secondEncoded) {
		t.Fatalf("scorecard is not deterministic\n%s\n%s", encoded, secondEncoded)
	}
}

func TestPrivateFindingScorecardAcceptsCompatibleAttestedSyntheticSampling(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	regressionResult := privateSamplingResult(t, "jira.primary-evidence", true)
	regressionResult.Surface = SurfaceATLMCP
	regressionResult.Runtime.Provider = "codex"
	failureResult := privateSamplingResult(t, "jira.primary-evidence", false)
	failureResult.Surface = SurfaceATLMCP
	failureResult.Runtime.Provider = "codex"
	regression := fixture.addResult(t, 1, "regression-capture", regressionResult, strings.Repeat("1", 64))
	failure := fixture.addResult(t, 9, "failure-capture", failureResult, strings.Repeat("3", 64))
	primary := addSyntheticFindingRoot(t, fixture, "primary-finding-synthetic-runs",
		regressionResult, regressionResult.ScenarioID, 3, strings.Repeat("4", 64),
		strings.Repeat("5", 64), strings.Repeat("6", 64))
	holdout := addSyntheticFindingRoot(t, fixture, "holdout-finding-synthetic-runs",
		regressionResult, "jira.holdout-evidence", 1, strings.Repeat("7", 64),
		strings.Repeat("8", 64), strings.Repeat("9", 64))
	assessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
		Holdout: []PrivateSyntheticSamplingRootRef{holdout},
	})
	writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
		FindingID: "finding-001", Failure: failure, FailureClass: PrivateFailureModel,
		ProductIssue: 123, PullRequest: 456, ChangedContractSHA256: strings.Repeat("1", 64),
		Regression: &regression, Decision: PrivateFindingDecisionFixed,
	}}})
	writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
		SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID: "finding-001", AssessmentSHA256: assessment,
			AssessmentSource:     PrivateFindingAcceptanceSourceSyntheticRoot,
			PromptContractSHA256: strings.Repeat("6", 64),
		}},
	})
	report, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
		fixture.dependencies().load,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != PrivateFindingScorecardSchemaVersion || report.Decisions.Fixed != 1 ||
		report.SamplingAssessments != 1 || len(report.Groups) != 1 ||
		report.Groups[0].Sampling.Primary.Observed != 3 ||
		report.Groups[0].Sampling.Holdout.Observed != 1 {
		t.Fatalf("report=%+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"finding-001", assessment, primary.Root, primary.SourceSHA256,
		holdout.Root, holdout.SourceSHA256, regressionResult.ScenarioID,
		strings.Repeat("6", 64),
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
	writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
		SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID: "finding-001", AssessmentSHA256: assessment,
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
		}},
	})
	if _, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
		fixture.dependencies().load,
	); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("mislabeled synthetic assessment err=%v", err)
	}
}

func TestPrivateFindingScorecardAcceptsTypedPrivateLiveAssessment(t *testing.T) {
	candidate := newPrivateFixedScorecardFixture(t, true)
	candidate.writeLedger(t)
	writePrivateFindingAcceptanceV2(t, candidate.fixture.root, PrivateFindingAcceptanceV2Index{
		SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID:        candidate.ledger.Entries[0].FindingID,
			AssessmentSHA256: candidate.assessment,
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
		}},
	})
	report, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: candidate.fixture.root, RepositoryRoot: candidate.fixture.repository},
		candidate.fixture.dependencies().load,
	)
	if err != nil || report.SamplingAssessments != 1 || report.Groups[0].Sampling.Primary.Observed != 3 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	writePrivateFindingAcceptanceV2(t, candidate.fixture.root, PrivateFindingAcceptanceV2Index{
		SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID:            candidate.ledger.Entries[0].FindingID,
			AssessmentSHA256:     candidate.assessment,
			AssessmentSource:     PrivateFindingAcceptanceSourceSyntheticRoot,
			PromptContractSHA256: strings.Repeat("6", 64),
		}},
	})
	if _, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: candidate.fixture.root, RepositoryRoot: candidate.fixture.repository},
		candidate.fixture.dependencies().load,
	); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("mislabeled private-live assessment err=%v", err)
	}
}

func TestPrivateFindingScorecardRejectsMismatchedSyntheticAcceptance(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Result)
	}{
		{"scenario", func(result *Result) { result.ScenarioID = "jira.other-primary" }},
		{"atl runtime", func(result *Result) { result.Runtime.ATLVersion = "other-atl" }},
		{"model", func(result *Result) { result.Runtime.Model = "other-model" }},
		{"prompt contract", func(result *Result) { result.Runtime.PromptContractSHA256 = strings.Repeat("a", 64) }},
		{"surface", func(result *Result) { result.Surface = SurfaceCLISkill }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateSamplingFixture(t)
			regressionResult := privateSamplingResult(t, "jira.primary-evidence", true)
			regressionResult.Surface = SurfaceATLMCP
			regressionResult.Runtime.Provider = "codex"
			failureResult := privateSamplingResult(t, "jira.primary-evidence", false)
			failureResult.Surface = SurfaceATLMCP
			failureResult.Runtime.Provider = "codex"
			regression := fixture.addResult(t, 1, "regression-capture", regressionResult, strings.Repeat("1", 64))
			failure := fixture.addResult(t, 9, "failure-capture", failureResult, strings.Repeat("3", 64))
			synthetic := regressionResult
			synthetic.Runtime.PromptContractSHA256 = strings.Repeat("6", 64)
			test.mutate(&synthetic)
			primary := addSyntheticFindingRoot(t, fixture, "primary-finding-synthetic-runs",
				synthetic, synthetic.ScenarioID, 3, strings.Repeat("4", 64),
				strings.Repeat("5", 64), synthetic.Runtime.PromptContractSHA256)
			holdout := addSyntheticFindingRoot(t, fixture, "holdout-finding-synthetic-runs",
				synthetic, "jira.holdout-evidence", 1, strings.Repeat("7", 64),
				strings.Repeat("8", 64), strings.Repeat("9", 64))
			assessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
				SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
				Holdout: []PrivateSyntheticSamplingRootRef{holdout},
			})
			writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
				FindingID: "finding-001", Failure: failure, FailureClass: PrivateFailureModel,
				ProductIssue: 1, PullRequest: 2, ChangedContractSHA256: strings.Repeat("1", 64),
				Regression: &regression, Decision: PrivateFindingDecisionFixed,
			}}})
			writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
				SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
					FindingID: "finding-001", AssessmentSHA256: assessment,
					AssessmentSource:     PrivateFindingAcceptanceSourceSyntheticRoot,
					PromptContractSHA256: strings.Repeat("6", 64),
				}},
			})
			if _, err := buildPrivateFindingScorecard(
				PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
				fixture.dependencies().load,
			); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateFindingScorecardRejectsSyntheticAcceptanceWithoutPromptBinding(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	regressionResult := privateSamplingResult(t, "jira.primary-evidence", true)
	regressionResult.Surface = SurfaceATLMCP
	regressionResult.Runtime.Provider = "codex"
	failureResult := privateSamplingResult(t, "jira.primary-evidence", false)
	failureResult.Surface = SurfaceATLMCP
	failureResult.Runtime.Provider = "codex"
	regression := fixture.addResult(t, 1, "regression-capture", regressionResult, strings.Repeat("1", 64))
	failure := fixture.addResult(t, 9, "failure-capture", failureResult, strings.Repeat("3", 64))
	primary := addSyntheticFindingRoot(t, fixture, "primary-finding-synthetic-runs",
		regressionResult, regressionResult.ScenarioID, 3, strings.Repeat("4", 64),
		strings.Repeat("5", 64), strings.Repeat("6", 64))
	holdout := addSyntheticFindingRoot(t, fixture, "holdout-finding-synthetic-runs",
		regressionResult, "jira.holdout-evidence", 1, strings.Repeat("7", 64),
		strings.Repeat("8", 64), strings.Repeat("9", 64))
	assessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
		Holdout: []PrivateSyntheticSamplingRootRef{holdout},
	})
	writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
		FindingID: "finding-001", Failure: failure, FailureClass: PrivateFailureModel,
		ProductIssue: 123, PullRequest: 456, ChangedContractSHA256: strings.Repeat("1", 64),
		Regression: &regression, Decision: PrivateFindingDecisionFixed,
	}}})
	writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
		SchemaVersion: 2, Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID: "finding-001", AssessmentSHA256: assessment,
			AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
		}},
	})
	if _, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
		fixture.dependencies().load,
	); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateFindingScorecardFailsClosed(t *testing.T) {
	newCase := func(t *testing.T) (*privateFindingFixture, PrivateFindingLedger, string, string) {
		fixture := newPrivateFindingFixture(t)
		failureID := "pln-44444444444444444444444444444444"
		regressionID := "pln-55555555555555555555555555555555"
		fixture.addSource(t, failureID, "", privateFindingTestResult(t, false))
		fixture.addSource(t, regressionID, "", privateFindingTestResult(t, true))
		ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
			FindingID: "finding-001", Failure: PrivateFindingRunRef{PlanID: failureID, Surface: SurfaceCLISkill, Baseline: "captured"},
			FailureClass: PrivateFailureModel, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate,
		}}}
		return fixture, ledger, failureID, regressionID
	}
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *privateFindingFixture, ledger *PrivateFindingLedger, failureID, regressionID string)
	}{
		{"duplicate failure", func(_ *testing.T, _ *privateFindingFixture, ledger *PrivateFindingLedger, _, _ string) {
			second := ledger.Entries[0]
			second.FindingID = "finding-002"
			ledger.Entries = append(ledger.Entries, second)
		}},
		{"fixed without change", func(_ *testing.T, _ *privateFindingFixture, ledger *PrivateFindingLedger, _, regressionID string) {
			ledger.Entries[0].Decision = PrivateFindingDecisionFixed
			ledger.Entries[0].Regression = &PrivateFindingRunRef{PlanID: regressionID, Surface: SurfaceCLISkill, Baseline: "captured"}
		}},
		{"fixed regression fails", func(t *testing.T, fixture *privateFindingFixture, ledger *PrivateFindingLedger, failureID, _ string) {
			otherID := "pln-66666666666666666666666666666666"
			fixture.addSource(t, otherID, "", privateFindingTestResult(t, false))
			ledger.Entries[0].Decision = PrivateFindingDecisionFixed
			ledger.Entries[0].PullRequest = 2
			ledger.Entries[0].ChangedContractSHA256 = strings.Repeat("b", 64)
			ledger.Entries[0].Regression = &PrivateFindingRunRef{PlanID: otherID, Surface: SurfaceCLISkill, Baseline: "captured"}
			_ = failureID
		}},
		{"change digest not bound", func(_ *testing.T, _ *privateFindingFixture, ledger *PrivateFindingLedger, _, regressionID string) {
			ledger.Entries[0].Decision = PrivateFindingDecisionFixed
			ledger.Entries[0].PullRequest = 2
			ledger.Entries[0].ChangedContractSHA256 = strings.Repeat("a", 64)
			ledger.Entries[0].Regression = &PrivateFindingRunRef{PlanID: regressionID, Surface: SurfaceCLISkill, Baseline: "captured"}
		}},
		{"incompatible regression", func(t *testing.T, fixture *privateFindingFixture, ledger *PrivateFindingLedger, _, regressionID string) {
			changed := privateFindingTestResult(t, true)
			changed.Runtime.Provider = "other-provider"
			fixture.addSource(t, regressionID, "replacement", changed)
			ledger.Entries[0].Regression = &PrivateFindingRunRef{PlanID: regressionID, Surface: SurfaceCLISkill, Baseline: "captured"}
		}},
		{"source rejected", func(_ *testing.T, fixture *privateFindingFixture, _ *PrivateFindingLedger, failureID, _ string) {
			fixture.errors[failureID] = errors.New("pruned")
		}},
		{"baseline plan mismatch", func(_ *testing.T, fixture *privateFindingFixture, _ *PrivateFindingLedger, failureID, _ string) {
			source := fixture.sources[failureID]
			source.PlanSHA256 = strings.Repeat("e", 64)
			fixture.sources[failureID] = source
		}},
		{"activation source", func(_ *testing.T, fixture *privateFindingFixture, _ *PrivateFindingLedger, failureID, _ string) {
			source := fixture.sources[failureID]
			source.Kind = PrivateRunSetKindActivationStudy
			fixture.sources[failureID] = source
		}},
		{"custom task class", func(t *testing.T, fixture *privateFindingFixture, _ *PrivateFindingLedger, failureID, _ string) {
			result := privateFindingTestResult(t, false)
			result.TaskClass = "private/customer-roadmap"
			fixture.addSource(t, failureID, "custom", result)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, ledger, failureID, regressionID := newCase(t)
			test.mutate(t, fixture, &ledger, failureID, regressionID)
			fixture.writeLedger(t, ledger)
			if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateFindingScorecardRequiresAcceptedSamplingEvidence(t *testing.T) {
	t.Run("missing acceptance index", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		candidate.writeLedger(t)
		candidate.mustReject(t)
	})

	t.Run("failed holdout", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, false)
		candidate.write(t)
		candidate.mustReject(t)
	})

	t.Run("singleton regression outside primary cohort", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		outside := candidate.fixture.addResult(t, 8, "outside-primary",
			privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))
		candidate.ledger.Entries[0].Regression = &outside
		candidate.write(t)
		candidate.mustReject(t)
	})

	t.Run("primary contract differs from changed contract", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		outside := candidate.fixture.addResult(t, 8, "other-contract",
			privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("5", 64))
		candidate.ledger.Entries[0].Regression = &outside
		candidate.ledger.Entries[0].ChangedContractSHA256 = strings.Repeat("5", 64)
		candidate.write(t)
		candidate.mustReject(t)
	})

	t.Run("immutable evidence drift", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		candidate.write(t)
		ref := candidate.primary[1]
		source := candidate.fixture.sources[ref.PlanID]
		path := filepath.Join(candidate.fixture.root, "baselines", source.ContractSHA256, ref.Baseline,
			"surfaces", ref.Surface, "result.json")
		writePrivateBaselineResult(t, path, privateSamplingResult(t, "jira.primary-evidence", false))
		candidate.mustReject(t)
	})

	t.Run("assessment bytes drift", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		candidate.write(t)
		path := filepath.Join(candidate.fixture.root, "reports", "sampling", candidate.assessment+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.TrimSpace(data), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate.mustReject(t)
	})

	t.Run("assessment source digest rebinding", func(t *testing.T) {
		candidate := newPrivateFixedScorecardFixture(t, true)
		path := filepath.Join(candidate.fixture.root, "reports", "sampling", candidate.assessment+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var assessment privateSamplingAssessment
		if err := json.Unmarshal(data, &assessment); err != nil {
			t.Fatal(err)
		}
		assessment.SourceSHA256 = strings.Repeat("f", 64)
		forged, err := encodePrivateSamplingAssessment(assessment)
		if err != nil {
			t.Fatal(err)
		}
		candidate.assessment = sha256HexBytes(append([]byte("atl-private-sampling-assessment-v1\x00"), forged...))
		forgedPath := filepath.Join(candidate.fixture.root, "reports", "sampling", candidate.assessment+".json")
		if err := os.WriteFile(forgedPath, forged, 0o600); err != nil {
			t.Fatal(err)
		}
		candidate.write(t)
		candidate.mustReject(t)
	})

	if runtime.GOOS != "windows" {
		t.Run("assessment loose mode", func(t *testing.T) {
			candidate := newPrivateFixedScorecardFixture(t, true)
			candidate.write(t)
			path := filepath.Join(candidate.fixture.root, "reports", "sampling", candidate.assessment+".json")
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			candidate.mustReject(t)
		})

		t.Run("assessment symlink", func(t *testing.T) {
			candidate := newPrivateFixedScorecardFixture(t, true)
			candidate.write(t)
			path := filepath.Join(candidate.fixture.root, "reports", "sampling", candidate.assessment+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "assessment.json")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			candidate.mustReject(t)
		})
	}
}

func TestPrivateFindingAcceptanceIndexFailsClosed(t *testing.T) {
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{
		{FindingID: "finding-001", Decision: PrivateFindingDecisionFixed},
		{FindingID: "finding-002", Decision: PrivateFindingDecisionFixed},
	}}
	for _, test := range []struct {
		name    string
		entries []PrivateFindingAcceptanceEntry
	}{
		{"dangling finding", []PrivateFindingAcceptanceEntry{
			{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64)},
			{FindingID: "finding-999", AssessmentSHA256: strings.Repeat("2", 64)},
		}},
		{"reused assessment", []PrivateFindingAcceptanceEntry{
			{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64)},
			{FindingID: "finding-002", AssessmentSHA256: strings.Repeat("1", 64)},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private")
			if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
				t.Fatal(err)
			}
			writePrivateFindingAcceptance(t, root, PrivateFindingAcceptanceIndex{SchemaVersion: 1, Entries: test.entries})
			if _, _, err := loadPrivateFindingAcceptance(root, ledger); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateFindingAcceptanceV2IndexFailsClosed(t *testing.T) {
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{
		{FindingID: "finding-001", Decision: PrivateFindingDecisionFixed},
		{FindingID: "finding-002", Decision: PrivateFindingDecisionFixed},
	}}
	for _, test := range []struct {
		name    string
		entries []PrivateFindingAcceptanceV2Entry
	}{
		{"unknown source", []PrivateFindingAcceptanceV2Entry{
			{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64), AssessmentSource: "future"},
			{FindingID: "finding-002", AssessmentSHA256: strings.Repeat("2", 64), AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
				PromptContractSHA256: strings.Repeat("3", 64)},
		}},
		{"reused assessment across sources", []PrivateFindingAcceptanceV2Entry{
			{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64), AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
			{FindingID: "finding-002", AssessmentSHA256: strings.Repeat("1", 64), AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
				PromptContractSHA256: strings.Repeat("3", 64)},
		}},
		{"private live with synthetic prompt binding", []PrivateFindingAcceptanceV2Entry{
			{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64), AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
				PromptContractSHA256: strings.Repeat("3", 64)},
			{FindingID: "finding-002", AssessmentSHA256: strings.Repeat("2", 64), AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private")
			if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
				t.Fatal(err)
			}
			writePrivateFindingAcceptanceV2(t, root, PrivateFindingAcceptanceV2Index{SchemaVersion: 2, Entries: test.entries})
			if _, _, err := loadPrivateFindingAcceptance(root, ledger); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateFindingAcceptanceRejectsAmbiguousVersions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFindingAcceptance(t, root, PrivateFindingAcceptanceIndex{SchemaVersion: 1,
		Entries: []PrivateFindingAcceptanceEntry{{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64)}}})
	writePrivateFindingAcceptanceV2(t, root, PrivateFindingAcceptanceV2Index{SchemaVersion: 2,
		Entries: []PrivateFindingAcceptanceV2Entry{{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64),
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive}}})
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001", Decision: PrivateFindingDecisionFixed}}}
	_, _, err := loadPrivateFindingAcceptance(root, ledger)
	assertPrivateFindingCode(t, err, "acceptance_ambiguous")
	// Both candidates probed cleanly; the pair itself is the rejection.
	if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want an ambiguity-only rejection", causes)
	}
}

func TestPrivateFindingAcceptanceRejectsConcurrentVersionCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFindingAcceptance(t, root, PrivateFindingAcceptanceIndex{SchemaVersion: 1,
		Entries: []PrivateFindingAcceptanceEntry{{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64)}}})
	_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
		writePrivateFindingAcceptanceV2(t, root, PrivateFindingAcceptanceV2Index{SchemaVersion: 2,
			Entries: []PrivateFindingAcceptanceV2Entry{{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("2", 64),
				AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot}}})
	})
	assertPrivateFindingCode(t, err, "acceptance_file")
	// The window closes on an inventory comparison, not on a failed probe.
	if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want an inventory-comparison rejection", causes)
	}
}

func TestPrivateFindingAcceptanceRejectsUnexpectedNonCanonicalLooseOrSymlinkFile(t *testing.T) {
	newCase := func(t *testing.T) (*privateFindingFixture, string) {
		fixture := newPrivateFindingFixture(t)
		planID := "pln-10101010101010101010101010101010"
		fixture.addSource(t, planID, "", privateFindingTestResult(t, false))
		fixture.writeLedger(t, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
			Failure:      PrivateFindingRunRef{PlanID: planID, Surface: SurfaceCLISkill, Baseline: "captured"},
			FailureClass: PrivateFailureModel, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate}}})
		writePrivateFindingAcceptance(t, fixture.root, PrivateFindingAcceptanceIndex{SchemaVersion: 1,
			Entries: []PrivateFindingAcceptanceEntry{{FindingID: "finding-001", AssessmentSHA256: strings.Repeat("1", 64)}}})
		return fixture, filepath.Join(fixture.root, PrivateFindingAcceptanceRelativePath)
	}

	t.Run("unexpected for non-fixed ledger", func(t *testing.T) {
		fixture, _ := newCase(t)
		if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
			t.Fatalf("err=%v", err)
		}
	})

	if runtime.GOOS == "windows" {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{"loose mode", func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "acceptance.json")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := newPrivateFixedScorecardFixture(t, true)
			candidate.write(t)
			path := filepath.Join(candidate.fixture.root, PrivateFindingAcceptanceRelativePath)
			test.mutate(t, path)
			candidate.mustReject(t)
		})
	}
}

// TestPrivateFindingAcceptanceCodesStayStableAndCauseFreeInTheMessage pins the
// nine acceptance classifications the loader and reader can emit. The rendered
// message must stay sentinel plus code for every one of them, so a configured
// private path or acceptance content cannot reach a log line, while the causes
// stay reachable through errors.Is/errors.As and in attach order.
func TestPrivateFindingAcceptanceCodesStayStableAndCauseFreeInTheMessage(t *testing.T) {
	privatePath := filepath.Join("private", "reports", "finding-acceptance.v2.json")
	statCause := &fs.PathError{Op: "statat", Path: privatePath, Err: fs.ErrPermission}
	secondCause := errors.New("synthetic close failure")

	for _, code := range []string{
		"unexpected_acceptance", "acceptance_file", "acceptance_contract", "acceptance_finding",
		"acceptance_reuse", "acceptance_missing", "acceptance_directory", "acceptance_ambiguous",
		"acceptance_read",
	} {
		err := privateFindingError(code, statCause, nil, secondCause)
		assertPrivateFindingCode(t, err, code)
		if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), secondCause.Error()) {
			t.Fatalf("message leaked a cause: %q", err.Error())
		}
		if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, secondCause) {
			t.Fatalf("error %v lost a cause", err)
		}
		var typed *fs.PathError
		if !errors.As(err, &typed) || typed.Path != statCause.Path {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
		// The read window supplies final-stat then close, the directory recheck
		// supplies final-handle then ambient, and the no-index window supplies
		// legacy then current. That fixed order is pinned here because none of
		// those pairs can be driven into a both-failed state with distinguishable
		// causes without racing the reader.
		causes := privateFindingErrorCauses(t, err)
		if len(causes) != 2 || causes[0] != error(statCause) || causes[1] != secondCause {
			t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
		}
		if bare := privateFindingErrorCauses(t, privateFindingError(code, nil, nil)); len(bare) != 0 {
			t.Fatalf("causes=%v, want nil causes dropped", bare)
		}
	}

	// The candidate probe classifies under this same shared constructor and its
	// classification is retained by the no-index window, so a nested code has to
	// stay reachable below the outer one.
	nested := privateFindingError("acceptance_file")
	outer := privateFindingError("acceptance_directory", nested)
	assertPrivateFindingCode(t, outer, "acceptance_directory")
	var classified interface{ Code() string }
	if !errors.As(outer, &classified) || classified.Code() != "acceptance_directory" {
		t.Fatalf("error %v does not report the outer acceptance code", outer)
	}
	if inner := privateFindingErrorCauses(t, outer); len(inner) != 1 || inner[0] != nested {
		t.Fatalf("causes=%v, want the nested classification retained", inner)
	}
}

func TestPrivateFindingAcceptanceAttachesDirectoryProbeCauses(t *testing.T) {
	t.Run("absent reports directory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		if err := os.RemoveAll(filepath.Join(fixture.root, "reports")); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingAcceptance(fixture.root)
		assertPrivateFindingCode(t, err, "acceptance_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the directory probe failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete probe failure", err)
		}
		var typed *fs.PathError
		if !errors.As(err, &typed) {
			t.Fatalf("error %v does not expose a path failure", err)
		}
		if strings.Contains(err.Error(), fixture.root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("loose reports permission", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		if err := os.Chmod(filepath.Join(fixture.root, "reports"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingAcceptance(fixture.root)
		assertPrivateFindingCode(t, err, "acceptance_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a permission-observation rejection", causes)
		}
	})

	t.Run("reports path is not a directory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		reports := filepath.Join(fixture.root, "reports")
		if err := os.RemoveAll(reports); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reports, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingAcceptance(fixture.root)
		assertPrivateFindingCode(t, err, "acceptance_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a file-type-observation rejection", causes)
		}
	})
}

func TestPrivateFindingAcceptanceCandidateProbeRejectsWithoutCause(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture privateFindingAcceptanceReadFixture)
	}{
		{"world-readable candidate", func(t *testing.T, fixture privateFindingAcceptanceReadFixture) {
			if err := os.Chmod(fixture.currentPath(), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"candidate is a directory", func(t *testing.T, fixture privateFindingAcceptanceReadFixture) {
			if err := os.Remove(fixture.currentPath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(fixture.currentPath(), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"candidate is a symlink", func(t *testing.T, fixture privateFindingAcceptanceReadFixture) {
			if err := os.Symlink(fixture.currentPath(), fixture.legacyPath()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateFindingAcceptanceReadFixture(t)
			test.mutate(t, fixture)
			_, _, err := readPrivateFindingAcceptance(fixture.root)
			assertPrivateFindingCode(t, err, "acceptance_file")
			// A type or mode observation rejects on its own; the Lstat succeeded.
			if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
				t.Fatalf("causes=%v, want an observation-only rejection", causes)
			}
		})
	}
}

// TestPrivateFindingAcceptanceNoIndexWindow covers the branch that has no
// counterpart in the ledger reader: an acceptance index may legitimately be
// absent, so the reader reprobes both candidates and rechecks the directory
// before reporting "no index". Both probes are still evaluated before the
// compound rejection, and the directory recheck still runs only behind them.
func TestPrivateFindingAcceptanceNoIndexWindow(t *testing.T) {
	t.Run("stable absence reports no index", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		version, data, err := readPrivateFindingAcceptance(root)
		if version != 0 || data != nil || err != nil {
			t.Fatalf("version=%d data=%v err=%v, want a clean no-index result", version, data, err)
		}
	})

	t.Run("candidate appears during the window", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
			writePrivateFindingAcceptanceV2(t, root, privateFindingAcceptanceReadFixtureIndex())
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// The reprobe succeeds; the appearance itself is the rejection.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an existence-comparison rejection", causes)
		}
	})

	t.Run("candidate appearance wins before directory movement", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
			reports := filepath.Join(root, "reports")
			moved := filepath.Join(root, "reports-moved")
			if renameErr := os.Rename(reports, moved); renameErr != nil {
				t.Fatal(renameErr)
			}
			writePrivateFindingAcceptanceBytes(t,
				filepath.Join(moved, filepath.Base(PrivateFindingAcceptanceV2RelativePath)),
				[]byte("{}\n"), 0o600)
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// The appeared candidate is observed through the retained handle before
		// the ambient directory path is rechecked. The comparison therefore wins
		// without attaching the later path failure.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want the candidate-appearance comparison to short-circuit first", causes)
		}
	})

	for _, test := range []struct {
		name  string
		path  func(root string) string
		wants int
	}{
		{"legacy candidate appears with a loose mode", func(root string) string {
			return filepath.Join(root, PrivateFindingAcceptanceRelativePath)
		}, 1},
		{"current candidate appears with a loose mode", func(root string) string {
			return filepath.Join(root, PrivateFindingAcceptanceV2RelativePath)
		}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newPrivateFindingFixture(t).root
			_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
				writePrivateFindingAcceptanceBytes(t, test.path(root), []byte("{}\n"), 0o644)
			})
			assertPrivateFindingCode(t, err, "acceptance_file")
			causes := privateFindingErrorCauses(t, err)
			if len(causes) != test.wants {
				t.Fatalf("causes=%v, want the candidate probe classification retained", causes)
			}
			// The probe classification is itself a coded rejection and has to stay
			// inspectable below the outer code.
			var classified interface{ Code() string }
			if !errors.As(causes[0], &classified) || classified.Code() != "acceptance_file" {
				t.Fatalf("cause %v does not report the nested probe code", causes[0])
			}
		})
	}

	t.Run("both candidates appear with a loose mode", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
			writePrivateFindingAcceptanceBytes(t, filepath.Join(root, PrivateFindingAcceptanceRelativePath), []byte("{}\n"), 0o644)
			writePrivateFindingAcceptanceBytes(t, filepath.Join(root, PrivateFindingAcceptanceV2RelativePath), []byte("{}\n"), 0o644)
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// Both probes are evaluated before the rejection, so both failures are
		// retained: legacy first, then current.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 2 {
			t.Fatalf("causes=%v, want both candidate probe classifications retained", causes)
		}
	})

	t.Run("reports directory moved during the window", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
			if renameErr := os.Rename(
				filepath.Join(root, "reports"), filepath.Join(root, "reports-moved"),
			); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		// The no-index window keeps its own outer code for the directory recheck.
		assertPrivateFindingCode(t, err, "acceptance_file")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the ambient directory probe failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete ambient failure", err)
		}
	})

	t.Run("reports directory replaced during the window", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := readPrivateFindingAcceptanceWithHook(root, func() {
			reports := filepath.Join(root, "reports")
			if renameErr := os.Rename(reports, filepath.Join(root, "reports-moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(reports, 0o700); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// Both directory probes succeed; the rejection is the identity change.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a directory-identity rejection", causes)
		}
	})
}

// TestPrivateFindingAcceptanceAttachesReadPathCauses drives the populated read
// window through the reader's existing post-inventory seam. Five failure-path
// groups stay uncovered here because they are reachable only by racing the
// reader and the production code deliberately grows no hook for them: a
// directory open or opened-handle stat that fails after the ambient probe
// already succeeded, a stat or close failure on the already-open acceptance
// descriptor, a final size that no longer matches the bytes just read, a
// candidate probe that fails for a reason other than ordinary absence, and a
// final-handle directory recheck failure. They attach on the same terms as the
// branches covered below; the fixed multi-cause order they rely on is pinned
// directly on the constructor instead.
func TestPrivateFindingAcceptanceAttachesReadPathCauses(t *testing.T) {
	t.Run("candidate removed after inventory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		_, _, err := readPrivateFindingAcceptanceWithHook(fixture.root, func() {
			if removeErr := os.Remove(fixture.currentPath()); removeErr != nil {
				t.Fatal(removeErr)
			}
		})
		assertPrivateFindingCode(t, err, "acceptance_read")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the open failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete open failure", err)
		}
	})

	t.Run("oversized candidate", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		oversized := bytes.Repeat([]byte("x"), privateFindingLedgerMaxBytes+1)
		writePrivateFindingAcceptanceBytes(t, fixture.currentPath(), oversized, 0o600)
		_, _, err := readPrivateFindingAcceptance(fixture.root)
		assertPrivateFindingCode(t, err, "acceptance_read")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the bounded-read failure retained", causes)
		}
		if strings.Contains(err.Error(), fixture.root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("candidate replaced after inventory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		_, _, err := readPrivateFindingAcceptanceWithHook(fixture.root, func() {
			replacement := filepath.Join(fixture.root, "reports", "replacement.tmp")
			writePrivateFindingAcceptanceBytes(t, replacement, []byte("{}\n"), 0o600)
			if renameErr := os.Rename(replacement, fixture.currentPath()); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// The opened handle stats cleanly; only its identity differs.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an identity-comparison rejection", causes)
		}
	})

	t.Run("inventory change wins before directory movement", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		_, _, err := readPrivateFindingAcceptanceWithHook(fixture.root, func() {
			reports := filepath.Join(fixture.root, "reports")
			writePrivateFindingAcceptanceBytes(t,
				filepath.Join(reports, filepath.Base(PrivateFindingAcceptanceRelativePath)),
				[]byte("{}\n"), 0o600)
			if renameErr := os.Rename(reports, filepath.Join(fixture.root, "reports-moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		assertPrivateFindingCode(t, err, "acceptance_file")
		// The candidate inventory is compared before the ambient directory path.
		// Its change therefore wins and the later missing-path failure is not
		// observed or attached.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want the inventory comparison to short-circuit first", causes)
		}
	})

	t.Run("reports directory moved after inventory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		_, _, err := readPrivateFindingAcceptanceWithHook(fixture.root, func() {
			if renameErr := os.Rename(
				filepath.Join(fixture.root, "reports"), filepath.Join(fixture.root, "reports-moved"),
			); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		// The inventory comparisons run first and pass, so the directory recheck
		// keeps its own code here.
		assertPrivateFindingCode(t, err, "acceptance_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the ambient directory probe failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete ambient failure", err)
		}
	})

	t.Run("reports directory replaced after inventory", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		_, _, err := readPrivateFindingAcceptanceWithHook(fixture.root, func() {
			reports := filepath.Join(fixture.root, "reports")
			if renameErr := os.Rename(reports, filepath.Join(fixture.root, "reports-moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(reports, 0o700); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		})
		assertPrivateFindingCode(t, err, "acceptance_directory")
		// Both directory probes succeed; the rejection is the identity change.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a directory-identity rejection", causes)
		}
	})
}

func TestPrivateFindingAcceptanceAttachesContractCauses(t *testing.T) {
	ledger := privateFindingAcceptanceReadFixtureLedger()

	t.Run("undecodable schema-v2 bytes", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		writePrivateFindingAcceptanceBytes(t, fixture.currentPath(), []byte("{not json"), 0o600)
		_, _, err := loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the decode failure retained", causes)
		}
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("error %v does not expose the concrete decode failure", err)
		}
	})

	t.Run("rejected schema-v2 envelope", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
			SchemaVersion: PrivateFindingAcceptanceV2SchemaVersion,
		})
		_, _, err := loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the validation failure retained", causes)
		}
	})

	t.Run("decodable but non-canonical schema-v2 bytes", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		compact, err := json.Marshal(privateFindingAcceptanceReadFixtureIndex())
		if err != nil {
			t.Fatal(err)
		}
		writePrivateFindingAcceptanceBytes(t, fixture.currentPath(), append(compact, '\n'), 0o600)
		_, _, err = loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_contract")
		// The bytes decode and validate; only the canonical comparison rejects.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a canonical-comparison rejection", causes)
		}
	})

	t.Run("undecodable legacy bytes", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		writePrivateFindingAcceptanceBytes(t, fixture.legacyPath(), []byte("{not json"), 0o600)
		_, _, err := loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the legacy decode failure retained", causes)
		}
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("error %v does not expose the concrete legacy decode failure", err)
		}
	})

	t.Run("decodable but non-canonical legacy bytes", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		compact, err := json.Marshal(PrivateFindingAcceptanceIndex{
			SchemaVersion: PrivateFindingAcceptanceSchemaVersion,
			Entries: []PrivateFindingAcceptanceEntry{{
				FindingID: "finding-read-001", AssessmentSHA256: strings.Repeat("1", 64),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		writePrivateFindingAcceptanceBytes(t, fixture.legacyPath(), append(compact, '\n'), 0o600)
		_, _, err = loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a canonical-comparison rejection", causes)
		}
	})
}

// TestPrivateFindingAcceptanceReconciliationRejectsWithoutCauses covers the
// classifications the loader reaches after the bytes are already decoded and
// canonical. Every one of them is a comparison against the ledger, so none has
// a failure in hand to retain.
func TestPrivateFindingAcceptanceReconciliationRejectsWithoutCauses(t *testing.T) {
	fixed := privateFindingAcceptanceReadFixtureLedger()

	for _, test := range []struct {
		name    string
		ledger  PrivateFindingLedger
		entries []PrivateFindingAcceptanceV2Entry
		code    string
	}{
		{"acceptance without a fixed finding", PrivateFindingLedger{SchemaVersion: 1,
			Entries: []PrivateFindingEntry{{FindingID: "finding-read-001", Decision: PrivateFindingDecisionInvestigate}}},
			privateFindingAcceptanceReadFixtureIndex().Entries, "unexpected_acceptance"},
		{"more acceptance entries than fixed findings", fixed, []PrivateFindingAcceptanceV2Entry{
			{FindingID: "finding-read-001", AssessmentSHA256: strings.Repeat("1", 64),
				AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
			{FindingID: "finding-read-002", AssessmentSHA256: strings.Repeat("2", 64),
				AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
		}, "acceptance_contract"},
		{"dangling finding", fixed, []PrivateFindingAcceptanceV2Entry{
			{FindingID: "finding-read-999", AssessmentSHA256: strings.Repeat("1", 64),
				AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
		}, "acceptance_finding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateFindingAcceptanceReadFixture(t)
			writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
				SchemaVersion: PrivateFindingAcceptanceV2SchemaVersion, Entries: test.entries})
			_, _, err := loadPrivateFindingAcceptance(fixture.root, test.ledger)
			assertPrivateFindingCode(t, err, test.code)
			if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
				t.Fatalf("causes=%v, want a comparison-only rejection", causes)
			}
		})
	}

	t.Run("reused assessment", func(t *testing.T) {
		fixture := newPrivateFindingAcceptanceReadFixture(t)
		writePrivateFindingAcceptanceV2(t, fixture.root, PrivateFindingAcceptanceV2Index{
			SchemaVersion: PrivateFindingAcceptanceV2SchemaVersion,
			Entries: []PrivateFindingAcceptanceV2Entry{
				{FindingID: "finding-read-001", AssessmentSHA256: strings.Repeat("1", 64),
					AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
				{FindingID: "finding-read-002", AssessmentSHA256: strings.Repeat("1", 64),
					AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive},
			}})
		ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{
			{FindingID: "finding-read-001", Decision: PrivateFindingDecisionFixed},
			{FindingID: "finding-read-002", Decision: PrivateFindingDecisionFixed},
		}}
		_, _, err := loadPrivateFindingAcceptance(fixture.root, ledger)
		assertPrivateFindingCode(t, err, "acceptance_reuse")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a comparison-only rejection", causes)
		}
	})

	t.Run("missing acceptance for a fixed finding", func(t *testing.T) {
		root := newPrivateFindingFixture(t).root
		_, _, err := loadPrivateFindingAcceptance(root, fixed)
		assertPrivateFindingCode(t, err, "acceptance_file")
		// The reader reported a clean absence, not a probe failure.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an absence-only rejection", causes)
		}
	})
}

func TestPrivateFindingScorecardRejectsNonCanonicalLooseOrSymlinkLedger(t *testing.T) {
	fixture := newPrivateFindingFixture(t)
	planID := "pln-77777777777777777777777777777777"
	fixture.addSource(t, planID, "", privateFindingTestResult(t, false))
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
		Failure: PrivateFindingRunRef{PlanID: planID, Surface: SurfaceCLISkill, Baseline: "captured"}, FailureClass: PrivateFailureModel,
		ProductIssue: 1, Decision: PrivateFindingDecisionDeferred}}}
	fixture.writeLedger(t, ledger)
	path := filepath.Join(fixture.root, PrivateFindingLedgerRelativePath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.TrimSpace(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("noncanonical err=%v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fixture.writeLedger(t, ledger)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("mode err=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.root, "target.json")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("symlink err=%v", err)
	}
}

func TestPrivateFindingScorecardRejectsBaselineResultDrift(t *testing.T) {
	fixture := newPrivateFindingFixture(t)
	planID := "pln-88888888888888888888888888888888"
	fixture.addSource(t, planID, "", privateFindingTestResult(t, false))
	fixture.writeLedger(t, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
		Failure:      PrivateFindingRunRef{PlanID: planID, Surface: SurfaceCLISkill, Baseline: "captured"},
		FailureClass: PrivateFailureModel, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate}}})
	source := fixture.sources[planID]
	path := filepath.Join(fixture.root, "baselines", source.ContractSHA256, "captured", "surfaces", SurfaceCLISkill, "result.json")
	writePrivateBaselineResult(t, path, privateFindingTestResult(t, true))
	if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("drift err=%v", err)
	}
}

func TestPrivateFindingScorecardRejectsBaselineResultPathRebinding(t *testing.T) {
	fixture := newPrivateFindingFixture(t)
	planID := "pln-99999999999999999999999999999999"
	fixture.addSource(t, planID, "", privateFindingTestResult(t, false))
	fixture.writeLedger(t, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
		Failure:      PrivateFindingRunRef{PlanID: planID, Surface: SurfaceCLISkill, Baseline: "captured"},
		FailureClass: PrivateFailureModel, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate}}})
	source := fixture.sources[planID]
	baselineRoot := filepath.Join(fixture.root, "baselines", source.ContractSHA256, "captured")
	manifestPath := filepath.Join(baselineRoot, "baseline.v1.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodePrivateBaselineManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	reboundPath := filepath.Join(fixture.root, "rebound-result.json")
	writePrivateBaselineResult(t, reboundPath, privateFindingTestResult(t, false))
	relative, err := filepath.Rel(baselineRoot, reboundPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Surfaces[0].ResultPath = filepath.ToSlash(relative)
	manifest.Surfaces[0].ResultSHA256 = privateFindingResultFileSHA256(t, reboundPath)
	manifestData, err = encodePrivateBaselineManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrivateFindingScorecard(fixture.options(), fixture.load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("rebound err=%v", err)
	}
}

func TestPrivateFindingScorecardAcceptsHashBoundReviewedBaseline(t *testing.T) {
	baseline := newPrivateBaselineFixture(t, 3)
	writePrivateBaselineRunArtifacts(t, baseline, "reviewed failure")
	writePrivateBaselineResult(t, filepath.Join(baseline.surfaceDirectory, "result.json"), privateFindingTestResult(t, false))
	writePrivateBaselineAssessment(t, &baseline)
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: baseline.root, RepositoryRoot: baseline.repository,
		Baseline: "reviewed", Confirm: PrivateBaselineConfirmation, Source: baseline.source}); err != nil {
		t.Fatal(err)
	}
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
		Failure:      PrivateFindingRunRef{PlanID: baseline.source.PlanID, Surface: SurfaceCLISkill, Baseline: "reviewed"},
		FailureClass: PrivateFailureQualitative, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate}}}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline.root, PrivateFindingLedgerRelativePath), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := buildPrivateFindingScorecard(PrivateFindingScorecardOptions{Root: baseline.root, RepositoryRoot: baseline.repository},
		func(_, _, planID string) (PrivateBaselineSource, error) {
			if planID != baseline.source.PlanID {
				return PrivateBaselineSource{}, errors.New("unknown")
			}
			return baseline.source, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if report.Findings != 1 || report.Groups[0].Failure.Statuses.Fail != 1 {
		t.Fatalf("report=%+v", report)
	}
	baselineRoot := filepath.Join(baseline.root, "baselines", baseline.source.ContractSHA256, "reviewed")
	manifestPath := filepath.Join(baselineRoot, "baseline.v1.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodePrivateBaselineManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(baselineRoot, "surfaces", SurfaceCLISkill, "result.json")
	manifest.Surfaces[0].ResultPath = filepath.ToSlash(filepath.Join("surfaces", SurfaceCLISkill, "result.json"))
	manifest.Surfaces[0].ResultSHA256 = privateFindingResultFileSHA256(t, rawPath)
	manifestData, err = encodePrivateBaselineManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrivateFindingScorecard(PrivateFindingScorecardOptions{Root: baseline.root, RepositoryRoot: baseline.repository},
		func(_, _, _ string) (PrivateBaselineSource, error) { return baseline.source, nil }); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("reviewed downgrade err=%v", err)
	}
}

func TestPrivateFindingLedgerPublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-finding-ledger.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, canonical, err := decodePrivateFindingLedger(data)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.SchemaVersion != PrivateFindingLedgerSchemaVersion || len(ledger.Entries) != 1 || !bytes.Equal(data, canonical) {
		t.Fatal("public finding-ledger example is not canonical schema v1")
	}
}

func TestPrivateFindingAcceptancePublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-finding-acceptance.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, canonical, err := decodePrivateFindingAcceptance(data)
	if err != nil || index.SchemaVersion != PrivateFindingAcceptanceSchemaVersion || len(index.Entries) != 1 || !bytes.Equal(data, canonical) {
		t.Fatalf("index=%+v canonical=%t err=%v", index, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-finding-acceptance.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func TestPrivateFindingAcceptanceV2PublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-finding-acceptance-v2.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, canonical, err := decodePrivateFindingAcceptanceV2(data)
	if err != nil || index.SchemaVersion != PrivateFindingAcceptanceV2SchemaVersion ||
		len(index.Entries) != 1 || index.Entries[0].AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot ||
		!validSHA256(index.Entries[0].PromptContractSHA256) ||
		!bytes.Equal(data, canonical) {
		t.Fatalf("index=%+v canonical=%t err=%v", index, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-finding-acceptance-v2.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

type privateFixedScorecardFixture struct {
	fixture    *privateSamplingFixture
	ledger     PrivateFindingLedger
	assessment string
	primary    []PrivateFindingRunRef
}

func newPrivateFixedScorecardFixture(t *testing.T, holdoutPass bool) *privateFixedScorecardFixture {
	t.Helper()
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addPrimary(t, 3, true)
	holdout := fixture.addHoldout(t, 4, holdoutPass)
	assessment := fixture.storeAssessment(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
		Primary: primary, Holdout: holdout})
	failure := fixture.addResult(t, 9, "failure-capture", privateSamplingResult(t, "jira.primary-evidence", false), strings.Repeat("3", 64))
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001", Failure: failure,
		FailureClass: PrivateFailureModel, ProductIssue: 1, PullRequest: 2, ChangedContractSHA256: strings.Repeat("1", 64),
		Regression: &primary[0], Decision: PrivateFindingDecisionFixed}}}
	return &privateFixedScorecardFixture{fixture: fixture, ledger: ledger, assessment: assessment, primary: primary}
}

func (fixture *privateFixedScorecardFixture) writeLedger(t *testing.T) {
	t.Helper()
	writePrivateFindingLedger(t, fixture.fixture.root, fixture.ledger)
}

func (fixture *privateFixedScorecardFixture) write(t *testing.T) {
	t.Helper()
	fixture.writeLedger(t)
	writePrivateFindingAcceptance(t, fixture.fixture.root, PrivateFindingAcceptanceIndex{SchemaVersion: 1,
		Entries: []PrivateFindingAcceptanceEntry{{FindingID: fixture.ledger.Entries[0].FindingID, AssessmentSHA256: fixture.assessment}}})
}

func (fixture *privateFixedScorecardFixture) mustReject(t *testing.T) {
	t.Helper()
	if _, err := buildPrivateFindingScorecard(PrivateFindingScorecardOptions{Root: fixture.fixture.root,
		RepositoryRoot: fixture.fixture.repository}, fixture.fixture.dependencies().load); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("err=%v", err)
	}
}

type privateFindingFixture struct {
	root, repository string
	sources          map[string]PrivateBaselineSource
	errors           map[string]error
}

func newPrivateFindingFixture(t *testing.T) *privateFindingFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	for _, directory := range []string{root, filepath.Join(root, "reports"), filepath.Join(root, "runs"), filepath.Join(root, "baselines")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &privateFindingFixture{root: root, repository: t.TempDir(), sources: map[string]PrivateBaselineSource{}, errors: map[string]error{}}
}

func (f *privateFindingFixture) options() PrivateFindingScorecardOptions {
	return PrivateFindingScorecardOptions{Root: f.root, RepositoryRoot: f.repository}
}

func (f *privateFindingFixture) load(_, _, planID string) (PrivateBaselineSource, error) {
	if err := f.errors[planID]; err != nil {
		return PrivateBaselineSource{}, err
	}
	source, ok := f.sources[planID]
	if !ok {
		return PrivateBaselineSource{}, errors.New("missing")
	}
	return source, nil
}

func (f *privateFindingFixture) addSource(t *testing.T, planID, _ string, result Result) {
	t.Helper()
	contractByte := strings.TrimPrefix(planID, "pln-")[:1]
	contractSHA256 := strings.Repeat(contractByte, 64)
	baselineRoot := filepath.Join(f.root, "baselines", contractSHA256, "captured")
	resultPath := filepath.Join(baselineRoot, "surfaces", result.EffectiveSurface(), "result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateBaselineResult(t, resultPath, result)
	treeSHA256, _, _, err := hashPrivateTree(baselineRoot, "baseline.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := privateBaselineManifest{SchemaVersion: PrivateBaselineSchemaVersion, Baseline: "captured", ContractSHA256: contractSHA256,
		PlanSHA256: strings.Repeat("c", 64), TreeSHA256: treeSHA256,
		Surfaces: []privateBaselineSurface{{Surface: result.EffectiveSurface(), ResultPath: filepath.ToSlash(filepath.Join("surfaces", result.EffectiveSurface(), "result.json")), ResultSHA256: privateFindingResultFileSHA256(t, resultPath)}}}
	manifestData, err := encodePrivateBaselineManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baselineRoot, "baseline.v1.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sources[planID] = PrivateBaselineSource{PlanID: planID, PlanSHA256: strings.Repeat("c", 64),
		ContractSHA256: contractSHA256, Completed: true, Immutable: true}
}

func privateFindingResultFileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256HexBytes(data)
}

func (f *privateFindingFixture) writeLedger(t *testing.T, ledger PrivateFindingLedger) {
	t.Helper()
	writePrivateFindingLedger(t, f.root, ledger)
}

func writePrivateFindingLedger(t *testing.T, root string, ledger PrivateFindingLedger) {
	t.Helper()
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, PrivateFindingLedgerRelativePath)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// privateFindingAcceptanceReadFixture is the smallest workspace the acceptance
// reader accepts: an owner-only reports directory holding one canonical
// schema-v2 acceptance index. It exercises the read path without building
// sampling evidence.
type privateFindingAcceptanceReadFixture struct{ root string }

func (f privateFindingAcceptanceReadFixture) currentPath() string {
	return filepath.Join(f.root, PrivateFindingAcceptanceV2RelativePath)
}

func (f privateFindingAcceptanceReadFixture) legacyPath() string {
	return filepath.Join(f.root, PrivateFindingAcceptanceRelativePath)
}

func newPrivateFindingAcceptanceReadFixture(t *testing.T) privateFindingAcceptanceReadFixture {
	t.Helper()
	fixture := privateFindingAcceptanceReadFixture{root: newPrivateFindingFixture(t).root}
	writePrivateFindingAcceptanceV2(t, fixture.root, privateFindingAcceptanceReadFixtureIndex())
	return fixture
}

func privateFindingAcceptanceReadFixtureIndex() PrivateFindingAcceptanceV2Index {
	return PrivateFindingAcceptanceV2Index{
		SchemaVersion: PrivateFindingAcceptanceV2SchemaVersion,
		Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID: "finding-read-001", AssessmentSHA256: strings.Repeat("1", 64),
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
		}},
	}
}

// privateFindingAcceptanceReadFixtureLedger is the ledger that reconciles with
// the fixture index: one fixed finding, which is all the loader reads from it.
func privateFindingAcceptanceReadFixtureLedger() PrivateFindingLedger {
	return PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{
		{FindingID: "finding-read-001", Decision: PrivateFindingDecisionFixed},
	}}
}

func writePrivateFindingAcceptanceBytes(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writePrivateFindingAcceptance(t *testing.T, root string, index PrivateFindingAcceptanceIndex) {
	t.Helper()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, PrivateFindingAcceptanceRelativePath)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePrivateFindingAcceptanceV2(t *testing.T, root string, index PrivateFindingAcceptanceV2Index) {
	t.Helper()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, PrivateFindingAcceptanceV2RelativePath)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func addSyntheticFindingRoot(t *testing.T, fixture *privateSamplingFixture, alias string,
	template Result, scenarioID string, repetitions int, taskSHA256, executionSHA256, promptSHA256 string,
) PrivateSyntheticSamplingRootRef {
	t.Helper()
	parent := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory)
	root := filepath.Join(parent, alias)
	for _, directory := range []string{parent, root, filepath.Join(root, ".ephemeral")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, privateOutputRootMarker), []byte(privateOutputRootMarkerContents), 0o600); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= repetitions; run++ {
		result := template
		result.DataClass = "synthetic"
		result.ScenarioID = scenarioID
		result.Runtime.PromptContractSHA256 = promptSHA256
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		writeSyntheticRootResultForCohort(t, root, run, repetitions, result)
		receipt := readSyntheticRootReceipt(t, root, result, run)
		receipt.TaskContractSHA256 = taskSHA256
		receipt.ExecutionContractSHA256 = executionSHA256
		writeSyntheticRootReceiptTest(t, root, receipt)
	}
	aggregate, err := AggregateSyntheticOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return PrivateSyntheticSamplingRootRef{Root: alias, SourceSHA256: aggregate.SourceSHA256}
}

func privateFindingTestResult(t *testing.T, pass bool) Result {
	t.Helper()
	scenario := validScenario()
	scenario.DataClass = "private-local"
	observation := validObservation()
	observation.Surface = SurfaceCLISkill
	if !pass {
		observation.Checks["answer_correct"] = false
	}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
