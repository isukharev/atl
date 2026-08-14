package agenteval

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestPinnedInspectQualificationIsClosedAndOneAttempt(t *testing.T) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	if err := qualification.Validate(); err != nil {
		t.Fatal(err)
	}
	if qualification.Identity.Package != "inspect-ai" || qualification.Identity.Version != "0.3.252" ||
		qualification.Identity.SourceCommit != "d105c61478c3fc86ff87d79b355c020869ee6a9b" ||
		qualification.Identity.WheelFilename != "inspect_ai-0.3.252-py3-none-any.whl" ||
		qualification.Identity.WheelSHA256 != "3be38f02d303b433e80e003c5181e609ad56594f45169b3709523781cdcd2ebc" ||
		qualification.Identity.SourceArchiveFilename != "inspect_ai-0.3.252.tar.gz" ||
		qualification.Identity.SourceArchiveSHA256 != "9e5abaaf7930a57c2d0d593a2123c60cd7300eeec7602a3a00bcfaa9e5efd820" ||
		qualification.Identity.RuntimeFloor != "python>=3.10" || qualification.Identity.License != "MIT License" {
		t.Fatalf("pinned identity drifted: %+v", qualification.Identity)
	}
	if qualification.Policy.FrameworkRetries != 0 || qualification.Policy.EvalSetRetries != 0 || qualification.Policy.TaskRetries != 0 || qualification.Policy.ModelRetries != 0 ||
		qualification.Policy.Cache || qualification.Policy.Telemetry || qualification.Policy.Upload ||
		qualification.Policy.Network != "deny" || qualification.Policy.Credentials != "none" ||
		qualification.Policy.PermissionPolicy != "evaluator_owned" || qualification.Policy.Sandbox != "required" ||
		qualification.Policy.RawArtifacts != "owner_private" || qualification.Policy.Projection != "content_minimized" ||
		qualification.Policy.ScoringAuthority != "evaluator_owned" {
		t.Fatalf("unsafe Inspect policy admitted: %+v", qualification.Policy)
	}

	encoded, err := EncodeInspectQualification(qualification)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeInspectQualification(bytes.NewReader(encoded))
	if err != nil || decoded != qualification {
		t.Fatalf("qualification round trip: decoded=%+v err=%v", decoded, err)
	}

	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	attempt, inspection, err := RunInspectSyntheticFailure(qualification, ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Validate(qualification); err != nil {
		t.Fatal(err)
	}
	if attempt.AttemptsStarted != 1 || attempt.RetryAttempts != 0 || !attempt.FailureRetained || attempt.Replayable ||
		attempt.RuntimeSafetyProven || attempt.Adoption != InspectQualificationDeferred {
		t.Fatalf("synthetic probe weakened one-attempt boundary: %+v", attempt)
	}
	attemptData, err := EncodeInspectSyntheticAttempt(qualification, attempt, inspection)
	if err != nil {
		t.Fatal(err)
	}
	decodedAttempt, err := DecodeInspectSyntheticAttempt(bytes.NewReader(attemptData), qualification, inspection)
	if err != nil || decodedAttempt != attempt {
		t.Fatalf("probe round trip: decoded=%+v err=%v", decodedAttempt, err)
	}
}

func TestInspectQualificationRejectsIdentityPolicyAndWireDrift(t *testing.T) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodeInspectQualification(qualification)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*InspectQualification){
		"package":        func(value *InspectQualification) { value.Identity.Package = "inspect-ai-mutated" },
		"source":         func(value *InspectQualification) { value.Identity.SourceCommit = strings.Repeat("b", 40) },
		"wheel":          func(value *InspectQualification) { value.Identity.WheelSHA256 = strings.Repeat("d", 64) },
		"source archive": func(value *InspectQualification) { value.Identity.SourceArchiveFilename = "mutated.tar.gz" },
		"license":        func(value *InspectQualification) { value.Identity.License = "unknown" },
		"runtime":        func(value *InspectQualification) { value.Identity.RuntimeFloor = "python>=3.11" },
		"retry":          func(value *InspectQualification) { value.Policy.ModelRetries = 1 },
		"task retry":     func(value *InspectQualification) { value.Policy.TaskRetries = 1 },
		"cache":          func(value *InspectQualification) { value.Policy.Cache = true },
		"network":        func(value *InspectQualification) { value.Policy.Network = "ambient" },
		"digest":         func(value *InspectQualification) { value.ContractSHA256 = strings.Repeat("c", 64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := qualification
			mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInspectQualification) {
				t.Fatalf("mutated qualification error=%v", err)
			}
			candidateData, err := encodeInspectQualificationJSON(candidate)
			if err != nil {
				// Invalid candidates are expected to fail validation before encoding.
				return
			}
			if _, err := DecodeInspectQualification(bytes.NewReader(candidateData)); err == nil {
				t.Fatal("mutated wire qualification was accepted")
			}
		})
	}

	mutations := map[string][]byte{
		"unknown":    bytes.Replace(valid, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1),
		"duplicate":  bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"trailing":   append(append([]byte(nil), valid...), []byte("{}\n")...),
		"no newline": valid[:len(valid)-1],
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInspectQualification(bytes.NewReader(mutation)); err == nil {
				t.Fatal("invalid qualification wire was accepted")
			}
		})
	}
}

func TestInspectQualificationErrorsRedactInputNames(t *testing.T) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodeInspectQualification(qualification)
	if err != nil {
		t.Fatal(err)
	}
	marker := "synthetic-marker-qualification"
	mutated := bytes.Replace(valid, []byte(`{"schema":`), []byte(`{"`+marker+`":true,"schema":`), 1)
	decodeErr := func() error {
		_, err := DecodeInspectQualification(bytes.NewReader(mutated))
		return err
	}()
	if decodeErr == nil || !errors.Is(decodeErr, ErrInspectQualification) || strings.Contains(decodeErr.Error(), marker) {
		t.Fatalf("qualification decoder leaked input marker: %v", decodeErr)
	}
}

func TestInspectSyntheticFailureRejectsReplayOrUnknownClaims(t *testing.T) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	attempt, inspection, err := inspectSyntheticFixture(t, qualification)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*InspectSyntheticAttempt){
		"second attempt": func(value *InspectSyntheticAttempt) { value.AttemptsStarted = 2 },
		"retry":          func(value *InspectSyntheticAttempt) { value.RetryAttempts = 1 },
		"not retained":   func(value *InspectSyntheticAttempt) { value.FailureRetained = false },
		"replayable":     func(value *InspectSyntheticAttempt) { value.Replayable = true },
		"safety proven":  func(value *InspectSyntheticAttempt) { value.RuntimeSafetyProven = true },
		"usage observed": func(value *InspectSyntheticAttempt) { value.Coverage.Usage = "observed" },
		"adopted":        func(value *InspectSyntheticAttempt) { value.Adoption = "adopted" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := attempt
			mutate(&candidate)
			if err := candidate.Validate(qualification); !errors.Is(err, ErrInspectQualification) {
				t.Fatalf("unsafe probe error=%v", err)
			}
		})
	}
	if _, err := EncodeInspectSyntheticAttempt(qualification, attempt, inspection); err != nil {
		t.Fatalf("valid synthetic attempt did not encode: %v", err)
	}
}

func TestInspectSyntheticFailureEncodingRequiresExactLedgerBinding(t *testing.T) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	attempt, inspection, err := inspectSyntheticFixture(t, qualification)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*InspectSyntheticAttempt){
		"attempt id":        func(value *InspectSyntheticAttempt) { value.AttemptID = strings.Repeat("a", 64) },
		"plan digest":       func(value *InspectSyntheticAttempt) { value.PlanSHA256 = strings.Repeat("b", 64) },
		"terminal digest":   func(value *InspectSyntheticAttempt) { value.TerminalEventSHA256 = strings.Repeat("c", 64) },
		"projection digest": func(value *InspectSyntheticAttempt) { value.ProjectionSHA256 = strings.Repeat("d", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := attempt
			mutate(&candidate)
			if _, err := EncodeInspectSyntheticAttempt(qualification, candidate, inspection); !errors.Is(err, ErrInspectQualification) {
				t.Fatalf("ledger-substituted probe was accepted: %v", err)
			}
		})
	}
	if _, err := EncodeInspectSyntheticAttempt(qualification, attempt, AttemptLedgerInspection{}); !errors.Is(err, ErrInspectQualification) {
		t.Fatalf("probe without a completed ledger was accepted: %v", err)
	}

	encoded, err := EncodeInspectSyntheticAttempt(qualification, attempt, inspection)
	if err != nil {
		t.Fatal(err)
	}
	marker := "synthetic-marker-probe"
	mutated := bytes.Replace(encoded, []byte(`{"schema":`), []byte(`{"`+marker+`":true,"schema":`), 1)
	if _, err := DecodeInspectSyntheticAttempt(bytes.NewReader(mutated), qualification, inspection); err == nil || !errors.Is(err, ErrInspectQualification) || strings.Contains(err.Error(), marker) {
		t.Fatalf("synthetic decoder accepted or leaked unknown input: %v", err)
	}
}

func inspectSyntheticFixture(t interface {
	Helper()
	TempDir() string
}, qualification InspectQualification) (InspectSyntheticAttempt, AttemptLedgerInspection, error) {
	t.Helper()
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	return RunInspectSyntheticFailure(qualification, ledgerRoot)
}

func TestRunInspectSyntheticFailureRetainsOneTerminalLedgerAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	attempt, inspection, err := RunInspectSyntheticFailure(qualification, ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.Validate(qualification); err != nil {
		t.Fatal(err)
	}
	if !inspection.Complete || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateFailed ||
		len(inspection.Events) != 2 || inspection.Events[1].To != lifecycle.StateFailed {
		t.Fatalf("injected failure was not retained as one terminal attempt: %+v", inspection)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDurableAttemptSession(store, inspection.Plan); err == nil {
		t.Fatal("terminal synthetic failure was reopened for replay")
	}
}

func TestRunInspectSyntheticFailureRejectsSecondInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedInspectQualification()
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := filepath.Join(t.TempDir(), "ledger")
	if err := os.Chmod(filepath.Dir(ledgerRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunInspectSyntheticFailure(qualification, ledgerRoot); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunInspectSyntheticFailure(qualification, ledgerRoot); !errors.Is(err, ErrInspectQualification) || !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("second synthetic invocation was not refused: %v", err)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	inspections, err := store.InspectAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(inspections) != 1 || !inspections[0].Projection.Terminal || inspections[0].Projection.State != lifecycle.StateFailed {
		t.Fatalf("second invocation changed the durable attempt count: %+v", inspections)
	}
}

func TestRunInspectSyntheticFailureConcurrentLedgerIsSingleUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the append-only test ledger is unavailable on Windows")
	}
	qualification, err := PinnedInspectQualification()
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
			_, _, runErr := RunInspectSyntheticFailure(qualification, ledgerRoot)
			results <- runErr
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	for runErr := range results {
		if runErr == nil {
			successes++
		} else if !errors.Is(runErr, ErrInspectQualification) {
			t.Fatalf("concurrent loser returned an unclassified error: %v", runErr)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent synthetic runs succeeded %d times", successes)
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

func FuzzDecodeInspectQualification(f *testing.F) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		f.Fatal(err)
	}
	seed, err := EncodeInspectQualification(qualification)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/inspect-ai-qualification","schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeInspectQualification(bytes.NewReader(data))
	})
}

func FuzzDecodeInspectSyntheticAttempt(f *testing.F) {
	qualification, err := PinnedInspectQualification()
	if err != nil {
		f.Fatal(err)
	}
	attempt, inspection, err := inspectSyntheticFixture(f, qualification)
	if err != nil {
		f.Fatal(err)
	}
	seed, err := EncodeInspectSyntheticAttempt(qualification, attempt, inspection)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/inspect-ai-qualification-probe","schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeInspectSyntheticAttempt(bytes.NewReader(data), qualification, inspection)
	})
}
