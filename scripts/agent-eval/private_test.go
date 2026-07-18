package main

import (
	"bytes"
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

func TestPrivateCommandRejectsMissingAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"init"}, {"status", "extra"}, {"doctor", "--root", "x", "extra"},
		{"review"}, {"review", "prepare"}, {"review", "assess"}, {"baseline"}, {"baseline", "set"},
		{"compare"}, {"prune", "--root", "x", "--confirm", "PRUNE"}, {"unknown"},
	} {
		if err := runPrivateCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("runPrivateCommand(%q) succeeded", args)
		}
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
