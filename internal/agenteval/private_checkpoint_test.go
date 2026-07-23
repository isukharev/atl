package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPrivateCheckpointPreviewIsDeterministicContentFreeAndReadOnly(t *testing.T) {
	fixture := newPrivateCheckpointFixture(t)
	before := privateCheckpointTree(t, fixture.root)
	preview, err := previewPrivateCheckpoint(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatalf("preview mutated workspace\nbefore=%s\nafter=%s", before, after)
	}
	if preview.SchemaVersion != PrivateCheckpointSchemaVersion || len(preview.CheckpointSHA256) != 64 || preview.Checkpoint.UTCDate != "2026-07-22" ||
		preview.Checkpoint.Repository.Commit != strings.Repeat("a", 40) || !preview.Checkpoint.Repository.Dirty ||
		preview.Checkpoint.Workspace.Counts.CompletedRuns != 7 || preview.Checkpoint.Scorecard.Findings != 3 ||
		preview.Checkpoint.Coverage.Assessments != 2 || preview.Checkpoint.Coverage.PrimaryObservations != 6 ||
		preview.Checkpoint.Coverage.HoldoutObservations != 2 ||
		preview.Checkpoint.Contracts.Ledger != PrivateFindingLedgerSchemaVersion ||
		preview.Checkpoint.Contracts.Scorecard != PrivateFindingScorecardSchemaVersion ||
		preview.Checkpoint.Contracts.CoverageIndex != PrivateCoverageIndexSchemaVersion ||
		preview.Checkpoint.Contracts.CoverageScorecard != PrivateCoverageScorecardSchemaVersion {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := encodePrivateCheckpoint(preview.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"private-run-set", "pln-", "run-", "customer-page", fixture.root} {
		if bytes.Contains(encoded, []byte(marker)) {
			t.Fatalf("private marker %q leaked in %s", marker, encoded)
		}
	}
	second, err := previewPrivateCheckpoint(fixture.options(), fixture.dependencies())
	if err != nil || second.CheckpointSHA256 != preview.CheckpointSHA256 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestPrivateCheckpointBindsSelectedFindingLedgerVersion(t *testing.T) {
	fixture := newPrivateCheckpointFixture(t)
	fixture.scorecard.LedgerSchemaVersion = PrivateFindingLedgerV2SchemaVersion
	preview, err := previewPrivateCheckpoint(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	if preview.Checkpoint.Contracts.Ledger != PrivateFindingLedgerV2SchemaVersion {
		t.Fatalf("contracts=%+v", preview.Checkpoint.Contracts)
	}
	if _, err := encodePrivateCheckpoint(preview.Checkpoint); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCheckpointAcceptsMultipleLinksPerFinding(t *testing.T) {
	fixture := newPrivateCheckpointFixture(t)
	fixture.scorecard.LedgerSchemaVersion = PrivateFindingLedgerV2SchemaVersion
	fixture.scorecard.Findings = 1
	fixture.scorecard.LinkedIssues = 2
	fixture.scorecard.LinkedPullRequests = 3
	fixture.scorecard.Regressions = 1
	fixture.scorecard.Decisions = PrivateFindingDecisionCounts{Fixed: 1}
	preview, err := previewPrivateCheckpoint(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	if preview.Checkpoint.Scorecard.Findings != 1 ||
		preview.Checkpoint.Scorecard.LinkedIssues != 2 ||
		preview.Checkpoint.Scorecard.LinkedPullRequests != 3 {
		t.Fatalf("scorecard=%+v", preview.Checkpoint.Scorecard)
	}
	encoded, err := encodePrivateCheckpoint(preview.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"finding-", "issue-", "pull-"} {
		if bytes.Contains(encoded, []byte(identity)) {
			t.Fatalf("identity %q leaked in %s", identity, encoded)
		}
	}
}

func TestPrivateCheckpointRejectsInvalidAggregateCounts(t *testing.T) {
	fixture := newPrivateCheckpointFixture(t)
	preview, err := previewPrivateCheckpoint(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*PrivateDailyCheckpoint)
	}{
		{"negative findings", func(value *PrivateDailyCheckpoint) { value.Scorecard.Findings = -1 }},
		{"negative issues", func(value *PrivateDailyCheckpoint) { value.Scorecard.LinkedIssues = -1 }},
		{"negative pull requests", func(value *PrivateDailyCheckpoint) { value.Scorecard.LinkedPullRequests = -1 }},
		{"negative regressions", func(value *PrivateDailyCheckpoint) { value.Scorecard.Regressions = -1 }},
		{"too many regressions", func(value *PrivateDailyCheckpoint) {
			value.Scorecard.Regressions = value.Scorecard.Findings + 1
		}},
		{"decision mismatch", func(value *PrivateDailyCheckpoint) { value.Scorecard.Decisions.Fixed++ }},
		{"negative coverage assessments", func(value *PrivateDailyCheckpoint) { value.Coverage.Assessments = -1 }},
		{"coverage group mismatch", func(value *PrivateDailyCheckpoint) { value.Coverage.Groups++ }},
		{"coverage primary mismatch", func(value *PrivateDailyCheckpoint) { value.Coverage.PrimaryObservations++ }},
		{"coverage holdout too small", func(value *PrivateDailyCheckpoint) { value.Coverage.HoldoutObservations = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkpoint := preview.Checkpoint
			test.mutate(&checkpoint)
			if _, err := encodePrivateCheckpoint(checkpoint); !errors.Is(err, ErrPrivateCheckpointRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateCheckpointRepositoryInspectionIsReadOnlyAndTracksDrift(t *testing.T) {
	repository := newPrivateCheckpointRepository(t)
	helperDirectory := t.TempDir()
	helperPath := filepath.Join(helperDirectory, "fsmonitor-helper")
	helperMarker := filepath.Join(helperDirectory, "invoked")
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nprintf invoked > \""+helperMarker+"\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repository, "config", "core.fsmonitor", helperPath).CombinedOutput(); err != nil {
		t.Fatalf("configure fsmonitor: %v\n%s", err, output)
	}
	fixture := newPrivateCheckpointFixture(t)
	fixture.repository = repository
	dependencies := fixture.dependencies()
	dependencies.repository = privateRepositoryIdentity
	gitDirectory := filepath.Join(repository, ".git")
	before := privateCheckpointTree(t, gitDirectory)

	clean, err := previewPrivateCheckpoint(fixture.options(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, gitDirectory)
	if !bytes.Equal(before, after) {
		t.Fatalf("preview mutated repository metadata\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := os.Stat(helperMarker); !os.IsNotExist(err) {
		t.Fatalf("preview invoked configured fsmonitor helper: %v", err)
	}
	if clean.Checkpoint.Repository.Dirty {
		t.Fatalf("clean repository reported dirty: %+v", clean.Checkpoint.Repository)
	}

	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := previewPrivateCheckpoint(fixture.options(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Checkpoint.Repository.Dirty || dirty.CheckpointSHA256 == clean.CheckpointSHA256 {
		t.Fatalf("repository drift not reflected: clean=%+v dirty=%+v", clean, dirty)
	}
	driftedApply := fixture.options()
	driftedApply.ExpectedCheckpointSHA256 = clean.CheckpointSHA256
	driftedApply.Confirm = PrivateCheckpointConfirmation
	if _, err := applyPrivateCheckpoint(driftedApply, dependencies); !errors.Is(err, ErrPrivateCheckpointRejected) {
		t.Fatalf("repository drift apply err=%v", err)
	}

	nextDayOptions := fixture.options()
	nextDayOptions.Now = nextDayOptions.Now.Add(24 * time.Hour)
	nextDay, err := previewPrivateCheckpoint(nextDayOptions, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if nextDay.Checkpoint.UTCDate != "2026-07-23" || nextDay.CheckpointSHA256 == dirty.CheckpointSHA256 {
		t.Fatalf("date drift not reflected: dirty=%+v next=%+v", dirty, nextDay)
	}
	nextDayOptions.ExpectedCheckpointSHA256 = dirty.CheckpointSHA256
	nextDayOptions.Confirm = PrivateCheckpointConfirmation
	if _, err := applyPrivateCheckpoint(nextDayOptions, dependencies); !errors.Is(err, ErrPrivateCheckpointRejected) {
		t.Fatalf("date drift apply err=%v", err)
	}
}

func TestPrivateCheckpointPreviewIntegratesCompletedPlanBaselineAndLedger(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic private-plan executables are Unix-only")
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
	manifest.RunSets[0].QualitativeReviewRequired = false
	manifest.RunSets[0].QualitativeReviewPanel = nil
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}
	plan := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(plan)); err != nil {
		t.Fatal(err)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, plan.PlanID)
	if err != nil || len(source.Surfaces) != 1 {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	resultPath := filepath.Join(source.Surfaces[0].RunDirectory, "result.json")
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	result.Status = "fail"
	result.Violations = []Violation{{Code: "required_check_failed", Subject: "answer_correct", Limit: 1}}
	writePrivateBaselineResult(t, resultPath, result)
	if _, err := SetPrivateBaseline(PrivateBaselineSetOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Baseline: "failure", Confirm: PrivateBaselineConfirmation, Source: source}); err != nil {
		t.Fatal(err)
	}
	ledger := PrivateFindingLedger{SchemaVersion: 1, Entries: []PrivateFindingEntry{{FindingID: "finding-001",
		Failure:      PrivateFindingRunRef{PlanID: plan.PlanID, Surface: source.Surfaces[0].Surface, Baseline: "failure"},
		FailureClass: PrivateFailureModel, ProductIssue: 1, Decision: PrivateFindingDecisionInvestigate}}}
	ledgerData, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, PrivateFindingLedgerRelativePath), append(ledgerData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	samplingFixture := &privateSamplingFixture{
		root: fixture.root, repository: fixture.repository, sources: map[string]PrivateBaselineSource{},
	}
	if err := os.MkdirAll(filepath.Join(fixture.root, "cases", "sampling"), 0o700); err != nil {
		t.Fatal(err)
	}
	coverageAssessment := addPrivateCoverageAssessment(t, samplingFixture, "checkpoint", "jira.coverage-primary",
		"jira.coverage-holdout", "jira.issue.refs", "1")
	writePrivateCoverageIndex(t, fixture.root, []string{coverageAssessment})
	if report := InspectPrivateWorkspace(fixture.root, fixture.repository); !report.Healthy {
		t.Fatalf("workspace report=%+v", report)
	}
	checkpoint, err := PreviewPrivateCheckpoint(PrivateCheckpointOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Checkpoint.Scorecard.Findings != 1 || checkpoint.Checkpoint.Workspace.Counts.CompletedRuns != 1 ||
		checkpoint.Checkpoint.Repository.Commit == "" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	summary, err := ApplyPrivateCheckpoint(PrivateCheckpointOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		Now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC), ExpectedCheckpointSHA256: checkpoint.CheckpointSHA256,
		Confirm: PrivateCheckpointConfirmation})
	if err != nil || !summary.Stored {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
}

func TestPrivateCheckpointApplyIsDigestBoundOwnerOnlyAndIdempotent(t *testing.T) {
	fixture := newPrivateCheckpointFixture(t)
	options := fixture.options()
	preview, err := previewPrivateCheckpoint(options, fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedCheckpointSHA256 = preview.CheckpointSHA256
	options.Confirm = PrivateCheckpointConfirmation
	first, err := applyPrivateCheckpoint(options, fixture.dependencies())
	if err != nil || !first.Stored {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	path := filepath.Join(fixture.root, "reports", "checkpoints", "2026-07-22.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := encodePrivateCheckpoint(preview.Checkpoint)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("stored checkpoint drift: %v\n%s", err, data)
	}
	second, err := applyPrivateCheckpoint(options, fixture.dependencies())
	if err != nil || second.Stored || second.CheckpointSHA256 != first.CheckpointSHA256 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPrivateCheckpoint(options, fixture.dependencies()); !errors.Is(err, ErrPrivateCheckpointRejected) {
		t.Fatalf("conflicting checkpoint err=%v", err)
	}
}

func TestPrivateCheckpointFailsClosedOnStateDigestAndPathControls(t *testing.T) {
	t.Run("active run", func(t *testing.T) {
		fixture := newPrivateCheckpointFixture(t)
		dependencies := fixture.dependencies()
		dependencies.doctor = func(_, _ string) (PrivateWorkspaceReport, error) {
			report := fixture.report
			report.Counts.ActiveRuns = 1
			return report, nil
		}
		if _, err := previewPrivateCheckpoint(fixture.options(), dependencies); !errors.Is(err, ErrPrivateCheckpointRejected) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("coverage scorecard", func(t *testing.T) {
		fixture := newPrivateCheckpointFixture(t)
		dependencies := fixture.dependencies()
		dependencies.coverage = func(PrivateCoverageScorecardOptions) (PrivateCoverageScorecard, error) {
			return PrivateCoverageScorecard{}, ErrPrivateCoverageIndexRejected
		}
		if _, err := previewPrivateCheckpoint(fixture.options(), dependencies); !errors.Is(err, ErrPrivateCheckpointRejected) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("digest drift", func(t *testing.T) {
		fixture := newPrivateCheckpointFixture(t)
		options := fixture.options()
		options.ExpectedCheckpointSHA256 = strings.Repeat("f", 64)
		options.Confirm = PrivateCheckpointConfirmation
		if _, err := applyPrivateCheckpoint(options, fixture.dependencies()); !errors.Is(err, ErrPrivateCheckpointRejected) {
			t.Fatalf("err=%v", err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("checkpoint directory symlink", func(t *testing.T) {
			fixture := newPrivateCheckpointFixture(t)
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(fixture.root, "reports", "checkpoints")); err != nil {
				t.Fatal(err)
			}
			options := fixture.options()
			preview, err := previewPrivateCheckpoint(options, fixture.dependencies())
			if err != nil {
				t.Fatal(err)
			}
			options.ExpectedCheckpointSHA256, options.Confirm = preview.CheckpointSHA256, PrivateCheckpointConfirmation
			if _, err := applyPrivateCheckpoint(options, fixture.dependencies()); !errors.Is(err, ErrPrivateCheckpointRejected) {
				t.Fatalf("err=%v", err)
			}
		})
		t.Run("checkpoint file symlink", func(t *testing.T) {
			fixture := newPrivateCheckpointFixture(t)
			directory := filepath.Join(fixture.root, "reports", "checkpoints")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "checkpoint.json")
			if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, "2026-07-22.json")); err != nil {
				t.Fatal(err)
			}
			applyPrivateCheckpointMustReject(t, fixture)
		})
		t.Run("loose checkpoint directory", func(t *testing.T) {
			fixture := newPrivateCheckpointFixture(t)
			directory := filepath.Join(fixture.root, "reports", "checkpoints")
			if err := os.Mkdir(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			applyPrivateCheckpointMustReject(t, fixture)
		})
		t.Run("loose checkpoint file", func(t *testing.T) {
			fixture := newPrivateCheckpointFixture(t)
			options := fixture.options()
			preview, err := previewPrivateCheckpoint(options, fixture.dependencies())
			if err != nil {
				t.Fatal(err)
			}
			data, err := encodePrivateCheckpoint(preview.Checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(fixture.root, "reports", "checkpoints")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "2026-07-22.json")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			applyPrivateCheckpointMustReject(t, fixture)
		})
	}
}

type privateCheckpointFixture struct {
	root, repository string
	report           PrivateWorkspaceReport
	scorecard        PrivateFindingScorecard
	coverage         PrivateCoverageScorecard
}

func newPrivateCheckpointFixture(t *testing.T) privateCheckpointFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	for _, directory := range []string{root, filepath.Join(root, "reports"), filepath.Join(root, ".ephemeral")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return privateCheckpointFixture{root: root, repository: t.TempDir(),
		report: PrivateWorkspaceReport{SchemaVersion: 1, Healthy: true, State: "needs_review",
			Counts: PrivateWorkspaceCounts{RunSets: 4, SpecReferences: 8, ValidSpecs: 8, IncompleteRuns: 2, CompletedRuns: 7}},
		scorecard: PrivateFindingScorecard{SchemaVersion: PrivateFindingScorecardSchemaVersion,
			LedgerSchemaVersion: PrivateFindingLedgerSchemaVersion, SourceSHA256: strings.Repeat("b", 64),
			Reconciled: true, Findings: 3, LinkedIssues: 2, LinkedPullRequests: 1, Regressions: 1,
			Decisions: PrivateFindingDecisionCounts{Fixed: 1, Investigate: 2}},
		coverage: PrivateCoverageScorecard{SchemaVersion: PrivateCoverageScorecardSchemaVersion,
			SourceSHA256: strings.Repeat("c", 64), Reconciled: true, Assessments: 2,
			PrimaryObservations: 6, HoldoutObservations: 2,
			Groups: []PrivateCoverageScorecardGroup{{}, {}}}}
}

func (f privateCheckpointFixture) options() PrivateCheckpointOptions {
	return PrivateCheckpointOptions{Root: f.root, RepositoryRoot: f.repository, Now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
}

func (f privateCheckpointFixture) dependencies() privateCheckpointDependencies {
	return privateCheckpointDependencies{
		doctor:     func(_, _ string) (PrivateWorkspaceReport, error) { return f.report, nil },
		scorecard:  func(PrivateFindingScorecardOptions) (PrivateFindingScorecard, error) { return f.scorecard, nil },
		coverage:   func(PrivateCoverageScorecardOptions) (PrivateCoverageScorecard, error) { return f.coverage, nil },
		repository: func(string) (string, bool, error) { return strings.Repeat("a", 40), true, nil },
	}
}

func privateCheckpointTree(t *testing.T, root string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		buffer.WriteString(relative)
		buffer.WriteByte(0)
		buffer.WriteString(info.Mode().String())
		buffer.WriteByte(0)
		buffer.WriteString(info.ModTime().UTC().Format(time.RFC3339Nano))
		if info.Mode().IsRegular() {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			buffer.WriteByte(0)
			buffer.Write(data)
		}
		buffer.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func applyPrivateCheckpointMustReject(t *testing.T, fixture privateCheckpointFixture) {
	t.Helper()
	options := fixture.options()
	preview, err := previewPrivateCheckpoint(options, fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedCheckpointSHA256, options.Confirm = preview.CheckpointSHA256, PrivateCheckpointConfirmation
	if _, err := applyPrivateCheckpoint(options, fixture.dependencies()); !errors.Is(err, ErrPrivateCheckpointRejected) {
		t.Fatalf("err=%v", err)
	}
}

func newPrivateCheckpointRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	commands := [][]string{{"init", "-q"}, {"add", "README.md"},
		{"-c", "user.name=ATL Tests", "-c", "user.email=atl-tests@example.invalid", "commit", "-qm", "fixture"}}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range commands {
		if output, err := exec.Command("git", append([]string{"-C", repository}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return repository
}
