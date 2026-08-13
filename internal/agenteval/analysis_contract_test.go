package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func TestSequentialReferenceAnalysisUsesOnlyStrictCompletedPublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the durable attempt ledger is not yet available on Windows")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AnalyzeSequentialReferencePublicationContext(canceled, filepath.Join(t.TempDir(), "must-not-be-read")); err == nil {
		t.Fatal("analysis read a publication under a canceled context")
	} else if code, ok := AnalysisErrorCodeOf(err); !ok || code != AnalysisErrorInterrupted {
		t.Fatalf("canceled analysis err=%v code=%s", err, code)
	}
	manifest, bundle := sequentialReferenceFixture(t)
	destination := filepath.Join(t.TempDir(), "analysis-publication")
	if _, err := RunSequentialReferenceToNewDestination(context.Background(), destination, manifest, bundle); err != nil {
		t.Fatal(err)
	}
	inspectionContext := &sequentialInspectionCancelContext{remaining: 16, done: make(chan struct{})}
	if _, err := AnalyzeSequentialReferencePublicationContext(inspectionContext, destination); err == nil {
		t.Fatal("analysis completed after cancellation during publication inspection")
	} else if code, ok := AnalysisErrorCodeOf(err); !ok || code != AnalysisErrorInterrupted || !inspectionContext.canceled || inspectionContext.checks < 10 {
		t.Fatalf("inspection cancellation err=%v code=%s canceled=%t checks=%d", err, code, inspectionContext.canceled, inspectionContext.checks)
	}
	report, err := AnalyzeSequentialReferencePublication(destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.ManifestSHA256 != manifest.ManifestSHA256 || report.AnalysisPlanSHA256 != manifest.AnalysisPlan.AnalysisPlanSHA256 ||
		report.Coverage.ExpectedRecords != 18 || report.Coverage.ReceivedRecords != 18 ||
		report.Coverage.CompletePairs != 6 || report.Coverage.ExcludedPairs != 0 || len(report.Comparisons) != 1 {
		t.Fatalf("report=%+v", report)
	}
	encoded, err := EncodeAnalysisReport(report, manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAnalysisReport(bytes.NewReader(encoded), manifest)
	if err != nil || !reflect.DeepEqual(decoded, report) {
		t.Fatalf("decode err=%v equal=%t", err, reflect.DeepEqual(decoded, report))
	}
	for _, forbidden := range []string{destination, "synthetic public case", "Synthetic public skill", "SKILL.md", "case.txt", "control.txt"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report exposed %q", forbidden)
		}
	}
	trial := filepath.Join(destination, sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(1), sequentialReferenceTrialRecordName)
	if err := os.Remove(trial); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeSequentialReferencePublication(destination); err == nil {
		t.Fatal("analysis accepted a publication missing canonical trial evidence")
	}
}

func TestSequentialReferenceAnalysisAdmissionPrecedesLedgerAndTrialReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the durable attempt ledger is not yet available on Windows")
	}
	manifest, bundle := sequentialReferenceFixture(t)
	destination := filepath.Join(t.TempDir(), "analysis-admission")
	if _, err := RunSequentialReferenceToNewDestination(context.Background(), destination, manifest, bundle); err != nil {
		t.Fatal(err)
	}
	plan := manifest.AnalysisPlan
	plan.AnalysisPlanSHA256 = ""
	plan.MinimumInferenceBlocks = 7
	plan, err := experiment.SealAnalysisPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	design := manifest.Design
	design.DesignSHA256 = ""
	design.AnalysisPlanSHA256 = plan.AnalysisPlanSHA256
	design.Strata = []experiment.StratumRequest{
		{BindingSHA256: rootExperimentDigest("analysis-admission-stratum-a"), Blocks: 6},
		{BindingSHA256: rootExperimentDigest("analysis-admission-stratum-b"), Blocks: 6},
	}
	design.Stopping.MaximumBlocks = 12
	design, err = experiment.SealDesign(design)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = experiment.Compile(design, manifest.CapabilityContract, plan)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := experiment.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, sequentialReferenceManifestName), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(destination, sequentialReferenceLedgerDirectory, attemptLedgerLockName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeSequentialReferencePublication(destination); err == nil {
		t.Fatal("analysis accepted per-stratum-unevaluable thresholds")
	} else if code, ok := AnalysisErrorCodeOf(err); !ok || code != AnalysisErrorInvalidInput {
		t.Fatalf("analysis admission err=%v code=%s", err, code)
	}
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("analysis reached or recreated the missing ledger lock: %v", err)
	}
}

type sequentialInspectionCancelContext struct {
	remaining int
	checks    int
	done      chan struct{}
	canceled  bool
}

func (ctx *sequentialInspectionCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *sequentialInspectionCancelContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *sequentialInspectionCancelContext) Value(any) any               { return nil }
func (ctx *sequentialInspectionCancelContext) Err() error {
	ctx.checks++
	if ctx.canceled {
		return context.Canceled
	}
	ctx.remaining--
	if ctx.remaining <= 0 {
		ctx.canceled = true
		close(ctx.done)
		return context.Canceled
	}
	return nil
}
