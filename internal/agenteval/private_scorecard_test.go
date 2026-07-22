package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if report.SchemaVersion != 2 || !report.Reconciled || report.Findings != 1 || report.Regressions != 1 || report.SamplingAssessments != 1 ||
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
