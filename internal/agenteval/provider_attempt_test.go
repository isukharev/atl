package agenteval

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const providerAttemptHelperEnv = "ATL_PROVIDER_ATTEMPT_HELPER"

func TestExecuteProviderAttemptNilCallbacksSuccess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	command := providerAttemptHelperCommand(t, marker, 0)

	stage, err := executeProviderAttempt(command, nil, nil)
	if err != nil {
		t.Fatalf("execute provider attempt: %v", err)
	}
	if stage != providerAttemptStageComplete {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageComplete)
	}
	if command.ProcessState == nil {
		t.Fatal("successful command has nil ProcessState")
	}
	if !command.ProcessState.Success() {
		t.Fatalf("successful command ProcessState = %v", command.ProcessState)
	}
	assertProviderAttemptExecutions(t, marker, 1)
}

func TestExecuteProviderAttemptCommitFailureStopsAttempt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	command := providerAttemptHelperCommand(t, marker, 0)
	wantErr := errors.New("commit failed")
	commitCalls := 0
	revalidateCalls := 0

	stage, err := executeProviderAttempt(command, func() error {
		commitCalls++
		return wantErr
	}, func() error {
		revalidateCalls++
		return nil
	})

	if err != wantErr {
		t.Fatalf("error = %v, want exact error %v", err, wantErr)
	}
	if stage != providerAttemptStageCommit {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageCommit)
	}
	if commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", commitCalls)
	}
	if revalidateCalls != 0 {
		t.Fatalf("revalidate calls = %d, want 0", revalidateCalls)
	}
	if command.Process != nil || command.ProcessState != nil {
		t.Fatalf("command started after commit failure: Process = %v, ProcessState = %v", command.Process, command.ProcessState)
	}
	assertProviderAttemptExecutions(t, marker, 0)
}

func TestExecuteProviderAttemptRevalidationFailureStopsBeforeSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	command := providerAttemptHelperCommand(t, marker, 0)
	wantErr := errors.New("revalidation failed")
	commitCalls := 0
	revalidateCalls := 0

	stage, err := executeProviderAttempt(command, func() error {
		commitCalls++
		return nil
	}, func() error {
		revalidateCalls++
		return wantErr
	})

	if err != wantErr {
		t.Fatalf("error = %v, want exact error %v", err, wantErr)
	}
	if stage != providerAttemptStageRevalidate {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageRevalidate)
	}
	if commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", commitCalls)
	}
	if revalidateCalls != 1 {
		t.Fatalf("revalidate calls = %d, want 1", revalidateCalls)
	}
	if command.Process != nil || command.ProcessState != nil {
		t.Fatalf("command started after revalidation failure: Process = %v, ProcessState = %v", command.Process, command.ProcessState)
	}
	assertProviderAttemptExecutions(t, marker, 0)
}

func TestExecuteProviderAttemptStartFailure(t *testing.T) {
	command := exec.Command(filepath.Join(t.TempDir(), "does-not-exist"))

	stage, err := executeProviderAttempt(command, nil, nil)
	if err == nil {
		t.Fatal("execute provider attempt succeeded with nonexistent binary")
	}
	if stage != providerAttemptStageStart {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageStart)
	}
	if command.Process != nil {
		t.Fatalf("start failure Process = %v, want nil", command.Process)
	}
	if command.ProcessState != nil {
		t.Fatalf("start failure ProcessState = %v, want nil", command.ProcessState)
	}
}

func TestExecuteProviderAttemptWaitFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	command := providerAttemptHelperCommand(t, marker, 23)

	stage, err := executeProviderAttempt(command, nil, nil)
	if err == nil {
		t.Fatal("execute provider attempt succeeded for nonzero child exit")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error type = %T, want *exec.ExitError", err)
	}
	if stage != providerAttemptStageWait {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageWait)
	}
	if command.ProcessState == nil {
		t.Fatal("nonzero child has nil ProcessState")
	}
	if command.ProcessState.ExitCode() != 23 {
		t.Fatalf("exit code = %d, want 23", command.ProcessState.ExitCode())
	}
	if command.ProcessState.Success() {
		t.Fatalf("nonzero child ProcessState reports success: %v", command.ProcessState)
	}
	assertProviderAttemptExecutions(t, marker, 1)
}

func TestExecuteProviderAttemptNilCommand(t *testing.T) {
	commitCalls := 0

	stage, err := executeProviderAttempt(nil, func() error {
		commitCalls++
		return nil
	}, nil)

	if err == nil || err.Error() != "provider attempt requires a command" {
		t.Fatalf("error = %v, want provider attempt requires a command", err)
	}
	if stage != providerAttemptStageStart {
		t.Fatalf("stage = %v, want %v", stage, providerAttemptStageStart)
	}
	if commitCalls != 0 {
		t.Fatalf("commit calls = %d, want 0", commitCalls)
	}
}

func TestExecuteProviderAttemptConsumesDurableSessionBeforeSpawn(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDurableAttemptSession(store, plan)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executions")
	command := providerAttemptHelperCommand(t, marker, 0)
	stage, terminationProven, processReceipt, err := executeProviderAttemptWithSession(command, nil, nil, session)
	if err != nil || stage != providerAttemptStageComplete || !terminationProven || !validSHA256(processReceipt) {
		t.Fatalf("durable execution: stage=%v termination=%t receipt=%q err=%v", stage, terminationProven, processReceipt, err)
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateRunning || !validSHA256(inspection.Projection.ProcessSHA256) {
		t.Fatalf("running transition missing: inspection=%+v err=%v", inspection, err)
	}
	if err := session.Succeed(processReceipt, lifecycle.UnknownUsage()); err != nil {
		t.Fatal(err)
	}
	inspection, err = store.Inspect(plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateSucceeded || !inspection.Projection.Terminal {
		t.Fatalf("terminal transition missing: inspection=%+v err=%v", inspection, err)
	}
}

func TestExecuteProviderAttemptStartFailureIsDurablyTerminal(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDurableAttemptSession(store, plan)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(t.TempDir(), "missing"))
	stage, _, _, err := executeProviderAttemptWithSession(command, nil, nil, session)
	if err == nil || stage != providerAttemptStageStart {
		t.Fatalf("start failure: stage=%v err=%v", stage, err)
	}
	inspection, inspectErr := store.Inspect(plan.AttemptID)
	if inspectErr != nil || inspection.Projection.State != lifecycle.StateFailed || !inspection.Projection.Terminal {
		t.Fatalf("start failure not terminal: inspection=%+v err=%v", inspection, inspectErr)
	}
}

func TestExecuteProviderAttemptBridgesLegacyCommitAfterGenericCommit(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDurableAttemptSession(store, plan)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executions")
	wantErr := errors.New("legacy commit failed")
	command := providerAttemptHelperCommand(t, marker, 0)
	stage, _, _, err := executeProviderAttemptWithSession(command, func() error {
		inspection, inspectErr := store.Inspect(plan.AttemptID)
		if inspectErr != nil || inspection.Projection.State != lifecycle.StateCommitted {
			t.Fatalf("legacy commit ran before generic commitment: %+v %v", inspection, inspectErr)
		}
		return wantErr
	}, nil, session)
	if !errors.Is(err, wantErr) || stage != providerAttemptStageCommit || command.Process != nil {
		t.Fatalf("legacy commit failure: stage=%v process=%v err=%v", stage, command.Process, err)
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateFailed {
		t.Fatalf("generic attempt was not closed before spawn: %+v %v", inspection, err)
	}
}

func providerAttemptHelperCommand(t *testing.T, marker string, exitCode int) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0],
		"-test.run=^TestProviderAttemptHelperProcess$",
		"--",
		marker,
		strconv.Itoa(exitCode),
	)
	command.Env = append(os.Environ(), providerAttemptHelperEnv+"=1")
	return command
}

func assertProviderAttemptExecutions(t *testing.T, marker string, want int) {
	t.Helper()
	contents, err := os.ReadFile(marker)
	if want == 0 && errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read helper marker: %v", err)
	}
	wantContents := strings.Repeat("executed\n", want)
	if string(contents) != wantContents {
		t.Fatalf("helper marker = %q, want %q", contents, wantContents)
	}
}

func TestProviderAttemptHelperProcess(_ *testing.T) {
	if os.Getenv(providerAttemptHelperEnv) != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) != separator+3 {
		fmt.Fprintln(os.Stderr, "provider attempt helper requires marker and exit code")
		os.Exit(97)
	}
	exitCode, err := strconv.Atoi(os.Args[separator+2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse provider attempt helper exit code: %v\n", err)
		os.Exit(98)
	}
	marker, err := os.OpenFile(os.Args[separator+1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open provider attempt helper marker: %v\n", err)
		os.Exit(99)
	}
	if _, err := marker.WriteString("executed\n"); err != nil {
		fmt.Fprintf(os.Stderr, "write provider attempt helper marker: %v\n", err)
		_ = marker.Close()
		os.Exit(100)
	}
	if err := marker.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close provider attempt helper marker: %v\n", err)
		os.Exit(101)
	}
	os.Exit(exitCode)
}
