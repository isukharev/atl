package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestGradingArtifactsAreTransitivelyBoundAndLifecycleAdmitted(t *testing.T) {
	contractData := readGradingGolden(t, "grader-contract-v1.json")
	planData := readGradingGolden(t, "grading-plan-v1.json")
	receiptData := readGradingGolden(t, "grade-receipt-v1.json")
	contract, err := DecodeGraderContract(bytes.NewReader(contractData))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DecodeGradingPlan(bytes.NewReader(planData))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGradingPlan(contract, plan); err != nil {
		t.Fatal(err)
	}
	receipt, err := DecodeGradeReceipt(bytes.NewReader(receiptData), plan)
	if err != nil {
		t.Fatal(err)
	}
	if encoded, err := EncodeGradeReceipt(plan, receipt); err != nil || !bytes.Equal(encoded, receiptData) {
		t.Fatalf("receipt round trip drift: err=%v", err)
	}
	planSHA, err := GradingPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BindGradingPlan(lifecycle.Binding{}, plan)
	if err != nil || binding.Identity.GraderSHA256 != planSHA || binding.Identity.AgentSHA256 != "" || binding.Identity.AdapterSHA256 != "" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	mutated := plan
	mutated.InputProjectionSHA256 = strings.Repeat("f", 64)
	if _, err := DecodeGradeReceipt(bytes.NewReader(receiptData), mutated); err == nil {
		t.Fatal("receipt remained executable after plan identity drift")
	}
}

func TestGraderProcessReferenceConformanceIsProtocolOnly(t *testing.T) {
	executable := buildOutOfPackageExtensionSample(t)
	executableDigest, err := digestSyntheticExecutable(executable, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	support := make(map[grading.CheckKind]grading.Support, len(grading.CheckKinds()))
	for _, kind := range grading.CheckKinds() {
		support[kind] = grading.SupportSupported
	}
	contract, err := grading.NewContract("external-grader", "1", strings.Repeat("a", 64), executableDigest,
		[]grading.ModePolicy{
			{Mode: grading.ModeDeterministic, Support: grading.SupportUnsupported, ExecutionClass: grading.ExecutionInProcess},
			{Mode: grading.ModeJudgeAssessment, Support: grading.SupportUnsupported, ExecutionClass: grading.ExecutionOfflineAssessment},
			{Mode: grading.ModeScriptDSL, Support: grading.SupportSupported, ExecutionClass: grading.ExecutionHermeticVerifier, Process: true},
		}, support)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostTestManifest(executableDigest)
	manifest.Component.ID = contract.GraderID
	manifest.Component.Version = contract.GraderVersion
	manifest.Component.Role = extension.RoleGrader
	manifest.Component.Operations = extension.OperationsForRole(extension.RoleGrader)
	manifest.Component.Capabilities = make([]extension.CapabilityClaim, len(manifest.Component.Operations))
	for index, operation := range manifest.Component.Operations {
		manifest.Component.Capabilities[index] = extension.CapabilityClaim{
			ID: extension.CapabilityFor(extension.RoleGrader, operation), State: extension.CapabilitySupported,
		}
	}
	manifest.Platforms = []extension.Platform{{OS: runtime.GOOS, Architecture: runtime.GOARCH}}
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := extensionHostTestBundle(manifestData, executableDigest, manifest)
	gradeIndex := slices.IndexFunc(bundle.Cases, func(testCase ExtensionConformanceCase) bool {
		return testCase.Operation == extension.OperationGrade && testCase.Expected.Type == extensionExpectedResult
	})
	if gradeIndex < 0 {
		t.Fatal("missing grade conformance case")
	}
	repeat := bundle.Cases[gradeIndex]
	repeat.ID = "grade-repeat"
	bundle.Cases = append(bundle.Cases, repeat)
	slices.SortFunc(bundle.Cases, func(left, right ExtensionConformanceCase) int {
		return strings.Compare(left.ID, right.ID)
	})
	if err := validateGraderDeterminismBundle(bundle); err != nil {
		t.Fatal(err)
	}
	drift := bundle
	drift.Cases = slices.Clone(bundle.Cases)
	repeatIndex := slices.IndexFunc(drift.Cases, func(testCase ExtensionConformanceCase) bool { return testCase.ID == "grade-repeat" })
	drift.Cases[repeatIndex].Inputs = []extension.ArtifactReference{extensionCancelProbeInput()}
	if err := validateGraderDeterminismBundle(drift); err == nil {
		t.Fatal("grader conformance accepted semantically different repeated grade cases")
	}
	bundleData, err := EncodeExtensionConformanceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	contractData, err := grading.EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	unsafeContract := contract
	unsafeContract.Modes = append([]grading.ModePolicy(nil), contract.Modes...)
	unsafeContract.Modes[2].Network = true
	if err := validateGraderProcessBinding(manifest, unsafeContract); err == nil {
		t.Fatal("process verifier accepted grader network authority")
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"manifest.json": manifestData, "bundle.json": bundleData, "contract.json": contractData} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := VerifyGraderProtocolFiles(context.Background(), filepath.Join(directory, "manifest.json"), executable,
		filepath.Join(directory, "bundle.json"), filepath.Join(directory, "contract.json"), filepath.Join(directory, "ledger"))
	if err != nil || !report.ProtocolConformant || report.Role != extension.RoleGrader || len(report.Cases) != 4 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	encoded, err := EncodeExtensionConformanceReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{directory, executable, "credential", "environment", "network", "hermetic"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("report contains %q", forbidden)
		}
	}
	ledger, err := InspectAttemptLedger(filepath.Join(directory, "ledger"))
	if err != nil || !ledger.Complete || len(ledger.Attempts) != len(report.Cases) {
		t.Fatalf("ledger=%+v err=%v", ledger, err)
	}
}

func readGradingGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "standalone-readability", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
