package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/extension"
)

func TestExecutionBackendProcessReferenceConformanceIsProtocolOnly(t *testing.T) {
	executable := buildOutOfPackageExtensionSample(t)
	executableDigest, err := digestSyntheticExecutable(executable, 512<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostTestManifest(executableDigest)
	manifest.Component.ID = "local-process"
	manifest.Component.Version = "1"
	manifest.Component.Role = extension.RoleExecutionBackend
	manifest.Component.Operations = extension.OperationsForRole(extension.RoleExecutionBackend)
	manifest.Component.Capabilities = make([]extension.CapabilityClaim, len(manifest.Component.Operations))
	for index, operation := range manifest.Component.Operations {
		manifest.Component.Capabilities[index] = extension.CapabilityClaim{ID: extension.CapabilityFor(extension.RoleExecutionBackend, operation), State: extension.CapabilitySupported}
	}
	manifest.Platforms = []extension.Platform{{OS: runtime.GOOS, Architecture: runtime.GOARCH}}
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := EncodeExtensionConformanceBundle(extensionHostTestBundle(manifestData, executableDigest, manifest))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := executionbackend.LocalProcessContract(strings.Repeat("a", 64), executableDigest)
	if err != nil {
		t.Fatal(err)
	}
	contractData, err := executionbackend.EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := executionbackend.NewLocalProcessPlan(contract, executionbackend.LocalProcessPlanOptions{
		DefinitionsSHA256: strings.Repeat("b", 64), FixtureSHA256: strings.Repeat("c", 64),
		SkillSHA256: strings.Repeat("d", 64), DeadlineMillis: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	planData, err := executionbackend.EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	isolationClaim := contract
	isolationClaim.Assurance = executionbackend.AssuranceIsolatedDeclaredGaps
	isolationPlan := plan
	isolationPlan.ContractSHA256, err = executionbackend.ContractSHA256(isolationClaim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionbackend.Admit(isolationClaim, isolationPlan); err != nil {
		t.Fatalf("isolated-declared-gaps fixture should be structurally admissible: %v", err)
	}
	if err := validateExecutionBackendProcessBinding(manifest, isolationClaim, isolationPlan); err == nil {
		t.Fatal("protocol-only verifier accepted an isolation assurance claim")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := map[string][]byte{"manifest.json": manifestData, "bundle.json": bundleData, "contract.json": contractData, "plan.json": planData}
	for name, data := range paths {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := VerifyExecutionBackendProtocolFiles(context.Background(), filepath.Join(directory, "manifest.json"), executable,
		filepath.Join(directory, "bundle.json"), filepath.Join(directory, "contract.json"), filepath.Join(directory, "plan.json"), filepath.Join(directory, "ledger"))
	if err != nil || !report.ProtocolConformant || report.Role != extension.RoleExecutionBackend || len(report.Cases) != 3 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err := EncodeExtensionConformanceReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{directory, executable, "environment", "credential", "hermetic"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("report contains %q", forbidden)
		}
	}
	ledger, err := InspectAttemptLedger(filepath.Join(directory, "ledger"))
	if err != nil || !ledger.Complete || len(ledger.Attempts) != len(report.Cases) {
		t.Fatalf("ledger=%+v err=%v", ledger, err)
	}
}
