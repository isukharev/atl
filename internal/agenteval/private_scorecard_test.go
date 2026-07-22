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
	fixture := newPrivateFindingFixture(t)
	failureID := "pln-11111111111111111111111111111111"
	regressionID := "pln-22222222222222222222222222222222"
	failure := privateFindingTestResult(t, false)
	regression := privateFindingTestResult(t, true)
	failure.Metrics.InputTokens, regression.Metrics.InputTokens = 100, 80
	failure.Coverage["input_tokens"], regression.Coverage["input_tokens"] = true, true
	fixture.addSource(t, failureID, "", failure)
	fixture.addSource(t, regressionID, "", regression)
	fixture.writeLedger(t, PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{
		FindingID: "finding-001", Failure: PrivateFindingRunRef{PlanID: failureID, Surface: SurfaceCLISkill, Baseline: "captured"},
		FailureClass: PrivateFailureModel, ProductIssue: 123, PullRequest: 456,
		ChangedContractSHA256: strings.Repeat("2", 64),
		Regression:            &PrivateFindingRunRef{PlanID: regressionID, Surface: SurfaceCLISkill, Baseline: "captured"},
		Decision:              PrivateFindingDecisionFixed,
	}}})
	before, err := os.Stat(filepath.Join(fixture.root, PrivateFindingLedgerRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildPrivateFindingScorecard(fixture.options(), fixture.load)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(fixture.root, PrivateFindingLedgerRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatal("read-only scorecard changed the ledger")
	}
	if report.SchemaVersion != 1 || !report.Reconciled || report.Findings != 1 || report.Regressions != 1 ||
		report.LinkedIssues != 1 || report.LinkedPullRequests != 1 || report.Decisions.Fixed != 1 || len(report.Groups) != 1 {
		t.Fatalf("report=%+v", report)
	}
	group := report.Groups[0]
	if group.TaskClass != failure.TaskClass || group.FailureClass != PrivateFailureModel || group.Failure.Statuses.Fail != 1 ||
		group.Regression.Statuses.Pass != 1 || group.Failure.Metrics.InputTokens.ObservedRuns != 1 ||
		group.Failure.Metrics.InputTokens.P50 != 100 || group.Regression.Metrics.InputTokens.P50 != 80 {
		t.Fatalf("group=%+v", group)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{failureID, regressionID, "finding-001", "123", "456", failure.ScenarioID, failure.Runtime.Model} {
		if secret != "" && bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("private reference %q leaked in %s", secret, encoded)
		}
	}
	second, err := buildPrivateFindingScorecard(fixture.options(), fixture.load)
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
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, PrivateFindingLedgerRelativePath)
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
