package agenteval

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestPinnedHarborQualificationIsClosedAndProviderFree(t *testing.T) {
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	if err := qualification.Validate(); err != nil {
		t.Fatal(err)
	}
	if qualification.Identity.Package != harborPackage || qualification.Identity.Version != harborVersion ||
		qualification.Identity.SourceCommit != harborSourceCommit || qualification.Identity.SourceArchiveSHA256 != harborSourceArchiveSHA256 ||
		qualification.Identity.ProjectManifestSHA256 != harborProjectManifestSHA256 || qualification.Identity.LockfileSHA256 != harborLockfileSHA256 ||
		qualification.Identity.ExecutableInput != "none_selected" || qualification.Identity.ContainerImage != "none_selected" {
		t.Fatalf("unexpected Harbor identity: %+v", qualification.Identity)
	}
	if qualification.Policy != harborPinnedPolicy() || qualification.Policy.FrameworkRetries != 0 || qualification.Policy.TrialRetries != 0 ||
		qualification.Policy.AgentRetries != 0 || qualification.Policy.VerifierRetries != 0 || qualification.Policy.Cache || qualification.Policy.Telemetry ||
		qualification.Policy.Upload || qualification.Policy.Network != "deny" || qualification.Policy.Credentials != "none" ||
		qualification.Policy.PermissionPolicy != "evaluator_owned" || qualification.Policy.ScoringAuthority != "evaluator_owned" {
		t.Fatalf("unexpected Harbor policy: %+v", qualification.Policy)
	}
	encoded, err := EncodeHarborQualification(qualification)
	if err != nil || len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		t.Fatalf("qualification did not encode canonically: len=%d err=%v", len(encoded), err)
	}
	decoded, err := DecodeHarborQualification(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != qualification {
		t.Fatalf("qualification changed across round trip: got=%+v want=%+v", decoded, qualification)
	}
	if digest, err := HarborQualificationSHA256(decoded); err != nil || digest != qualification.ContractSHA256 {
		t.Fatalf("qualification digest changed: digest=%q err=%v", digest, err)
	}
}

func TestHarborQualificationProductionImportsAreEffectFree(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate Harbor qualification source")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(source), "harbor_qualification.go"), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, specification := range file.Imports {
		path := strings.Trim(specification.Path.Value, `"`)
		for _, forbidden := range []string{"net", "net/", "os", "os/", "os/exec", "os/user", "plugin", "syscall"} {
			if path == forbidden || strings.HasPrefix(path, forbidden) {
				t.Fatalf("provider-free Harbor qualification imports effectful package %q", path)
			}
		}
	}
}

func TestHarborQualificationRejectsIdentityPolicyAndDigestDrift(t *testing.T) {
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*HarborQualification){
		"source commit":     func(value *HarborQualification) { value.Identity.SourceCommit = strings.Repeat("a", 40) },
		"archive digest":    func(value *HarborQualification) { value.Identity.SourceArchiveSHA256 = strings.Repeat("a", 64) },
		"manifest digest":   func(value *HarborQualification) { value.Identity.ProjectManifestSHA256 = strings.Repeat("b", 64) },
		"lock digest":       func(value *HarborQualification) { value.Identity.LockfileSHA256 = strings.Repeat("c", 64) },
		"executable input":  func(value *HarborQualification) { value.Identity.ExecutableInput = "harbor" },
		"container input":   func(value *HarborQualification) { value.Identity.ContainerImage = "harbor:0.20.0" },
		"framework retry":   func(value *HarborQualification) { value.Policy.FrameworkRetries = 1 },
		"trial retry":       func(value *HarborQualification) { value.Policy.TrialRetries = 1 },
		"agent retry":       func(value *HarborQualification) { value.Policy.AgentRetries = 1 },
		"verifier retry":    func(value *HarborQualification) { value.Policy.VerifierRetries = 1 },
		"cache":             func(value *HarborQualification) { value.Policy.Cache = true },
		"telemetry":         func(value *HarborQualification) { value.Policy.Telemetry = true },
		"upload":            func(value *HarborQualification) { value.Policy.Upload = true },
		"network":           func(value *HarborQualification) { value.Policy.Network = "public" },
		"credentials":       func(value *HarborQualification) { value.Policy.Credentials = "ambient" },
		"permission bypass": func(value *HarborQualification) { value.Policy.PermissionPolicy = "bypass" },
		"sandbox":           func(value *HarborQualification) { value.Policy.Sandbox = "optional" },
		"scoring":           func(value *HarborQualification) { value.Policy.ScoringAuthority = "harbor" },
		"digest":            func(value *HarborQualification) { value.ContractSHA256 = strings.Repeat("d", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := qualification
			mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrHarborQualification) {
				t.Fatalf("unsafe qualification was accepted: %v", err)
			}
		})
	}
}

func TestHarborQualificationCodecRejectsNonCanonicalAndUnknownInput(t *testing.T) {
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeHarborQualification(qualification)
	if err != nil {
		t.Fatal(err)
	}
	marker := "harbor-private-marker"
	cases := map[string][]byte{
		"unknown":      bytes.Replace(encoded, []byte(`{"schema":`), []byte(`{"`+marker+`":true,"schema":`), 1),
		"duplicate":    bytes.Replace(encoded, []byte(`{"schema":`), []byte(`{"schema":"duplicate","schema":`), 1),
		"trailing":     append(append([]byte(nil), encoded...), 'x'),
		"invalid utf8": append(append([]byte(nil), encoded[:len(encoded)-1]...), 0xff, '\n'),
		"oversize":     append(bytes.Repeat([]byte{'x'}, HarborQualificationMaxBytes), '\n'),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHarborQualification(bytes.NewReader(data)); !errors.Is(err, ErrHarborQualification) || strings.Contains(err.Error(), marker) {
				t.Fatalf("malformed qualification was accepted or leaked input: %v", err)
			}
		})
	}
}

func harborSyntheticFixture(t interface {
	Helper()
	TempDir() string
}, qualification HarborQualification) (HarborSyntheticAttempt, AttemptLedgerInspection, error) {
	t.Helper()
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	return RunHarborSyntheticFailure(qualification, ledgerRoot)
}

func TestRunHarborSyntheticFailureRetainsOneTerminalAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	attempt, inspection, err := harborSyntheticFixture(t, qualification)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Validate(qualification); err != nil {
		t.Fatal(err)
	}
	if inspection.Plan.Ordinal != 1 || !inspection.Complete || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateFailed ||
		len(inspection.Events) != 2 || inspection.Events[1].To != lifecycle.StateFailed || attempt.Coverage != harborSyntheticUnknownCoverage() || attempt.RuntimeSafetyProven {
		t.Fatalf("unexpected synthetic Harbor failure: attempt=%+v inspection=%+v", attempt, inspection)
	}
	encoded, err := EncodeHarborSyntheticAttempt(qualification, attempt, inspection)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHarborSyntheticAttempt(bytes.NewReader(encoded), qualification, inspection)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != attempt {
		t.Fatalf("synthetic result changed across round trip: got=%+v want=%+v", decoded, attempt)
	}
}

func TestRunHarborSyntheticFailureRejectsReplayAndBindsLedger(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunHarborSyntheticFailure(qualification, ledgerRoot); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunHarborSyntheticFailure(qualification, ledgerRoot); !errors.Is(err, ErrHarborQualification) || !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("second synthetic invocation was not refused: %v", err)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	inspections, err := store.InspectAll()
	if err != nil || len(inspections) != 1 || !inspections[0].Projection.Terminal {
		t.Fatalf("replay changed durable ledger shape: inspections=%+v err=%v", inspections, err)
	}
	attempt, inspection, err := harborSyntheticFixture(t, qualification)
	if err != nil {
		t.Fatal(err)
	}
	mutated := attempt
	mutated.PlanSHA256 = strings.Repeat("a", 64)
	if _, err := EncodeHarborSyntheticAttempt(qualification, mutated, inspection); !errors.Is(err, ErrHarborQualification) {
		t.Fatalf("ledger-substituted projection was accepted: %v", err)
	}
	if _, err := EncodeHarborSyntheticAttempt(qualification, attempt, AttemptLedgerInspection{}); !errors.Is(err, ErrHarborQualification) {
		t.Fatal("projection without a completed ledger was accepted")
	}
}

func TestRunHarborSyntheticFailureConcurrentLedgerIsSingleUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			defer group.Done()
			_, _, runErr := RunHarborSyntheticFailure(qualification, ledgerRoot)
			results <- runErr
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	for runErr := range results {
		if runErr == nil {
			successes++
		} else if !errors.Is(runErr, ErrHarborQualification) || !errors.Is(runErr, ErrAttemptLedgerConflict) {
			t.Fatalf("concurrent loser was not classified as conflict: %v", runErr)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent synthetic Harbor runs succeeded %d times", successes)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	inspections, err := store.InspectAll()
	if err != nil || len(inspections) != 1 || !inspections[0].Projection.Terminal {
		t.Fatalf("concurrent runs changed ledger shape: inspections=%+v err=%v", inspections, err)
	}
}

func TestRunHarborSyntheticFailureMapsBusyToConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedHarborQualification()
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunHarborSyntheticFailure(qualification, ledgerRoot); err != nil {
		t.Fatal(err)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Unlock() }()
	_, _, err = RunHarborSyntheticFailure(qualification, ledgerRoot)
	if !errors.Is(err, ErrHarborQualification) || !errors.Is(err, ErrAttemptLedgerConflict) || !errors.Is(err, ErrAttemptLedgerBusy) {
		t.Fatalf("held ledger lock was not classified as a conflict: %v", err)
	}
	if strings.Contains(err.Error(), ledgerRoot) || strings.Contains(err.Error(), ErrAttemptLedgerBusy.Error()) {
		t.Fatalf("busy cause escaped coded error: %v", err)
	}
}

func FuzzDecodeHarborQualification(f *testing.F) {
	qualification, err := PinnedHarborQualification()
	if err != nil {
		f.Fatal(err)
	}
	seed, err := EncodeHarborQualification(qualification)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/harbor-qualification","schema_version":1}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeHarborQualification(bytes.NewReader(data))
	})
}

func FuzzDecodeHarborSyntheticAttempt(f *testing.F) {
	qualification, err := PinnedHarborQualification()
	if err != nil {
		f.Fatal(err)
	}
	attempt, inspection, err := harborSyntheticFixture(f, qualification)
	if err != nil {
		f.Fatal(err)
	}
	seed, err := EncodeHarborSyntheticAttempt(qualification, attempt, inspection)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/harbor-qualification-probe","schema_version":1}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeHarborSyntheticAttempt(bytes.NewReader(data), qualification, inspection)
	})
}
