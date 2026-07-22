package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestPrivateInitStatusAndDoctor(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"init", "--root", root, "--repository-root", repository}, &output); err != nil {
		t.Fatalf("private init: %v", err)
	}
	assertPrivateReport(t, output.Bytes(), true)

	output.Reset()
	if err := runPrivateCommand([]string{"status", "--root", root, "--repository-root", repository}, &output); err != nil {
		t.Fatalf("private status: %v", err)
	}
	assertPrivateReport(t, output.Bytes(), true)

	output.Reset()
	if err := runPrivateCommand([]string{"doctor", "--root", root, "--repository-root", repository}, &output); err != nil {
		t.Fatalf("private doctor: %v", err)
	}
	assertPrivateReport(t, output.Bytes(), true)
}

func TestPrivateMigrateIsPreviewByDefaultAndDigestBoundOnApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	manifest := agenteval.DefaultPrivateWorkspaceManifest()
	manifest.SchemaVersion = agenteval.LegacyCalibratedWorkspaceSchemaVersion
	if report, err := agenteval.InitPrivateWorkspace(root, repository, manifest); err != nil || !report.Healthy {
		t.Fatalf("init report=%+v err=%v", report, err)
	}
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"migrate", "--root", root, "--repository-root", repository}, &output); err != nil {
		t.Fatal(err)
	}
	var preview agenteval.PrivateWorkspaceMigrationPreview
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "ready" || len(preview.MigrationSHA256) != 64 {
		t.Fatalf("preview=%+v", preview)
	}
	if _, err := os.Lstat(filepath.Join(root, agenteval.PrivateWorkspaceManifestName)); !os.IsNotExist(err) {
		t.Fatalf("preview mutated workspace: %v", err)
	}
	output.Reset()
	if err := runPrivateCommand([]string{"migrate", "--root", root, "--repository-root", repository,
		"--expected-migration-sha256", preview.MigrationSHA256, "--confirm", "MIGRATE"}, &output); err != nil {
		t.Fatal(err)
	}
	var summary agenteval.PrivateWorkspaceMigrationSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "migrated" || summary.MigrationSHA256 != preview.MigrationSHA256 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestPrivateDoctorEmitsSanitizedFailure(t *testing.T) {
	repository := t.TempDir()
	privateMarker := "private-host.example.invalid/PROJ-123"
	root := filepath.Join(t.TempDir(), privateMarker)
	var output bytes.Buffer
	err := runPrivateCommand([]string{"doctor", "--root", root, "--repository-root", repository}, &output)
	if err == nil {
		t.Fatal("doctor accepted a missing workspace")
	}
	if strings.Contains(output.String(), privateMarker) || strings.Contains(err.Error(), privateMarker) {
		t.Fatalf("private marker leaked: output=%q err=%v", output.String(), err)
	}
	assertPrivateReport(t, output.Bytes(), false)
}

func TestPrivateQualifyEmitsContentFreeReport(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	if err := runPrivateCommand([]string{"init", "--root", root, "--repository-root", repository}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	original := privateQualifyCodexCLI
	var captured agenteval.CodexCLIToolAvailabilityOptions
	privateQualifyCodexCLI = func(_ context.Context, options agenteval.CodexCLIToolAvailabilityOptions) (agenteval.CodexCLIToolAvailabilityReport, error) {
		captured = options
		return agenteval.CodexCLIToolAvailabilityReport{
			SchemaVersion:   agenteval.CodexCLIToolAvailabilitySchemaVersion,
			Provider:        "codex",
			AgentIdentity:   "binary-sha256:" + strings.Repeat("a", 64),
			ContractSHA256:  strings.Repeat("b", 64),
			Status:          agenteval.CodexCLIToolAvailabilitySupported,
			ShellTool:       "exec_command",
			RequestObserved: true, SyntheticRequests: 1,
		}, nil
	}
	t.Cleanup(func() { privateQualifyCodexCLI = original })
	var output bytes.Buffer
	err := runPrivateCommand([]string{
		"qualify", "--root", root, "--repository-root", repository,
		"--agent-binary", "/reviewed/codex", "--model", "synthetic-model",
		"--reasoning", "high", "--timeout-seconds", "17",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var report agenteval.CodexCLIToolAvailabilityReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || !report.Supported() {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if captured.AgentBinary != "/reviewed/codex" || captured.Model != "synthetic-model" || captured.Reasoning != "high" ||
		captured.TimeoutSeconds != 17 || captured.ScratchRoot != filepath.Join(canonicalRoot, ".ephemeral") {
		t.Fatalf("options=%+v", captured)
	}
}

func TestPrivateCommandRejectsMissingAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"init"}, {"status", "extra"}, {"doctor", "--root", "x", "extra"}, {"migrate"},
		{"migrate", "--root", "x", "--confirm", "MIGRATE"}, {"qualify"},
		{"review"}, {"review", "prepare"}, {"review", "run"}, {"review", "assess"}, {"baseline"}, {"baseline", "set"},
		{"study"}, {"study", "recover"}, {"study", "reference"}, {"study", "compare"}, {"study", "promote"}, {"study", "unknown"},
		{"compare"}, {"scorecard"}, {"scorecard", "--root", "x", "extra"}, {"sample"},
		{"sample", "--root", "x", "--spec", "sample", "--confirm", "ASSESS"}, {"checkpoint"},
		{"checkpoint", "--root", "x", "--confirm", "CHECKPOINT"},
		{"prune", "--root", "x", "--confirm", "PRUNE"}, {"unknown"},
	} {
		if err := runPrivateCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("runPrivateCommand(%q) succeeded", args)
		}
	}
}

func TestPrivateSamplePreviewsAndAppliesExactDigest(t *testing.T) {
	originalPreview, originalApply := privatePreviewSampling, privateApplySampling
	privatePreviewSampling = func(options agenteval.PrivateSamplingOptions) (agenteval.PrivateSamplingPreview, error) {
		if options.Root != "/private" || options.RepositoryRoot != "/repository" || options.Spec != "sample-set" || options.Confirm != "" {
			t.Fatalf("preview options=%+v", options)
		}
		accepted := true
		return agenteval.PrivateSamplingPreview{SchemaVersion: 1, Tier: agenteval.PrivateSamplingTierRegression,
			SourceSHA256: strings.Repeat("b", 64), AssessmentSHA256: strings.Repeat("a", 64), EvidenceReady: true,
			RegressionAccepted: &accepted, Primary: agenteval.PrivateSamplingOutcome{Observed: 3},
			Holdout: agenteval.PrivateSamplingOutcome{Observed: 1}}, nil
	}
	privateApplySampling = func(options agenteval.PrivateSamplingOptions) (agenteval.PrivateSamplingSummary, error) {
		if options.Spec != "sample-set" || options.ExpectedAssessmentSHA256 != strings.Repeat("a", 64) ||
			options.Confirm != agenteval.PrivateSamplingConfirmation {
			t.Fatalf("apply options=%+v", options)
		}
		return agenteval.PrivateSamplingSummary{PrivateSamplingPreview: agenteval.PrivateSamplingPreview{
			SchemaVersion: 1, Tier: agenteval.PrivateSamplingTierRegression, AssessmentSHA256: options.ExpectedAssessmentSHA256,
			EvidenceReady: true}, Stored: true}, nil
	}
	t.Cleanup(func() { privatePreviewSampling, privateApplySampling = originalPreview, originalApply })
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"sample", "--root", "/private", "--repository-root", "/repository", "--spec", "sample-set"}, &output); err != nil {
		t.Fatal(err)
	}
	var preview agenteval.PrivateSamplingPreview
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil || preview.Primary.Observed != 3 || preview.RegressionAccepted == nil {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	output.Reset()
	if err := runPrivateCommand([]string{"sample", "--root", "/private", "--repository-root", "/repository", "--spec", "sample-set",
		"--expected-assessment-sha256", preview.AssessmentSHA256, "--confirm", "ASSESS"}, &output); err != nil {
		t.Fatal(err)
	}
	var summary agenteval.PrivateSamplingSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil || !summary.Stored {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
}

func TestPrivateCheckpointPreviewsAndAppliesExactDigest(t *testing.T) {
	originalPreview, originalApply := privatePreviewCheckpoint, privateApplyCheckpoint
	privatePreviewCheckpoint = func(options agenteval.PrivateCheckpointOptions) (agenteval.PrivateCheckpointPreview, error) {
		if options.Root != "/private" || options.RepositoryRoot != "/repository" || options.Confirm != "" {
			t.Fatalf("preview options=%+v", options)
		}
		return agenteval.PrivateCheckpointPreview{SchemaVersion: 1, CheckpointSHA256: strings.Repeat("a", 64),
			Checkpoint: agenteval.PrivateDailyCheckpoint{SchemaVersion: 1, UTCDate: "2026-07-22"}}, nil
	}
	privateApplyCheckpoint = func(options agenteval.PrivateCheckpointOptions) (agenteval.PrivateCheckpointSummary, error) {
		if options.ExpectedCheckpointSHA256 != strings.Repeat("a", 64) || options.Confirm != agenteval.PrivateCheckpointConfirmation {
			t.Fatalf("apply options=%+v", options)
		}
		return agenteval.PrivateCheckpointSummary{SchemaVersion: 1, UTCDate: "2026-07-22",
			CheckpointSHA256: options.ExpectedCheckpointSHA256, Stored: true}, nil
	}
	t.Cleanup(func() { privatePreviewCheckpoint, privateApplyCheckpoint = originalPreview, originalApply })
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"checkpoint", "--root", "/private", "--repository-root", "/repository"}, &output); err != nil {
		t.Fatal(err)
	}
	var preview agenteval.PrivateCheckpointPreview
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil || len(preview.CheckpointSHA256) != 64 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	output.Reset()
	if err := runPrivateCommand([]string{"checkpoint", "--root", "/private", "--repository-root", "/repository",
		"--expected-checkpoint-sha256", preview.CheckpointSHA256, "--confirm", "CHECKPOINT"}, &output); err != nil {
		t.Fatal(err)
	}
	var summary agenteval.PrivateCheckpointSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil || !summary.Stored {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
}

func TestPrivateScorecardEmitsSanitizedReport(t *testing.T) {
	original := privateBuildFindingScorecard
	privateBuildFindingScorecard = func(options agenteval.PrivateFindingScorecardOptions) (agenteval.PrivateFindingScorecard, error) {
		if options.Root != "/reviewed/private" || options.RepositoryRoot != "/reviewed/repository" {
			t.Fatalf("options=%+v", options)
		}
		return agenteval.PrivateFindingScorecard{SchemaVersion: 1, SourceSHA256: strings.Repeat("a", 64), Reconciled: true,
			Findings: 2, LinkedIssues: 2, LinkedPullRequests: 1, Regressions: 1}, nil
	}
	t.Cleanup(func() { privateBuildFindingScorecard = original })
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"scorecard", "--root", "/reviewed/private", "--repository-root", "/reviewed/repository"}, &output); err != nil {
		t.Fatal(err)
	}
	var report agenteval.PrivateFindingScorecard
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Reconciled || report.Findings != 2 || report.LinkedPullRequests != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrivatePruneIsPreviewByDefaultAndHashBoundOnApply(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	if err := runPrivateCommand([]string{"init", "--root", root, "--repository-root", repository}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runPrivateCommand([]string{"prune", "--root", root, "--repository-root", repository}, &output); err != nil {
		t.Fatal(err)
	}
	var preview agenteval.PrivatePrunePreview
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.EligibleRunSets != 0 || len(preview.InventorySHA256) != 64 {
		t.Fatalf("preview=%+v", preview)
	}
	output.Reset()
	if err := runPrivateCommand([]string{
		"prune", "--root", root, "--repository-root", repository,
		"--expected-inventory-sha256", preview.InventorySHA256, "--confirm", "PRUNE",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var summary agenteval.PrivatePruneSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.PrunedRunSets != 0 || summary.RemovedFiles != 0 || summary.RemovedBytes != 0 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestPrivateInitCreatesOwnerOnlyFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode assertion")
	}
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	if err := runPrivateCommand([]string{"init", "--root", root, "--repository-root", repository}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.Stat(filepath.Join(root, agenteval.PrivateWorkspaceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode=%#o", manifest.Mode().Perm())
	}
}

func assertPrivateReport(t *testing.T, data []byte, healthy bool) {
	t.Helper()
	var report agenteval.PrivateWorkspaceReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, data)
	}
	if report.Healthy != healthy {
		t.Fatalf("healthy=%v want %v: %+v", report.Healthy, healthy, report)
	}
}
