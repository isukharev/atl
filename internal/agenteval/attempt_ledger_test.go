package agenteval

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestAttemptLedgerPersistsAllocationTransitionsAndLowerBounds(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateCommitted, []lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	appendAttemptEventForTest(t, store, plan, lifecycle.StateSpawning, []lifecycle.Proof{lifecycle.ProofDurableSpawnIntent}, attemptEvidence(lifecycle.ErrorNone))
	running := attemptEvidence(lifecycle.ErrorNone)
	running.ProcessIdentitySHA256 = strings.Repeat("b", 64)
	running.Usage.InputTokens = lifecycle.ObservedMetric(8)
	appendAttemptEventForTest(t, store, plan, lifecycle.StateRunning, []lifecycle.Proof{lifecycle.ProofDurableProcessIdentity}, running)
	unknown := attemptEvidence(lifecycle.ErrorTerminationAmbiguous)
	unknown.Usage.InputTokens = lifecycle.ObservedMetric(11)
	unknown.Usage.EstimatedCostMicroUSD = lifecycle.ObservedMetric(25)
	appendAttemptEventForTest(t, store, plan, lifecycle.StateUnknown, []lifecycle.Proof{lifecycle.ProofIncompleteTerminal}, unknown)

	reopened, err := OpenAttemptLedgerStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := reopened.Inspect(plan.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Complete || inspection.Projection.State != lifecycle.StateUnknown || !inspection.Projection.Terminal ||
		inspection.Projection.Usage.EstimatedCostMicroUSD.Value == nil || *inspection.Projection.Usage.EstimatedCostMicroUSD.Value != 25 ||
		inspection.Projection.Usage.InputTokens.Value == nil || *inspection.Projection.Usage.InputTokens.Value != 11 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if _, err := reopened.Append(plan.AttemptID, lifecycle.StateSucceeded,
		[]lifecycle.Proof{lifecycle.ProofTerminalReceipt, lifecycle.ProofTermination}, attemptEvidence(lifecycle.ErrorNone)); !errors.Is(err, lifecycle.ErrContract) && !errors.Is(err, ErrAttemptLedger) {
		t.Fatalf("unknown attempt was replayed: %v", err)
	}
}

func TestAttemptLedgerReconciliationAllocatesANewIdentity(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	first, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, first, lifecycle.StateUnknown, []lifecycle.Proof{lifecycle.ProofIncompleteTerminal}, attemptEvidence(lifecycle.ErrorInternal))
	second, err := store.AllocateReconciled(testAttemptBinding(), first.AttemptID, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptID == first.AttemptID || second.Ordinal != first.Ordinal+1 || second.PredecessorAttemptID != first.AttemptID {
		t.Fatalf("reconciliation did not allocate a distinct linked attempt: first=%+v second=%+v", first, second)
	}
	appendAttemptEventForTest(t, store, second, lifecycle.StateCanceled,
		[]lifecycle.Proof{lifecycle.ProofCompleteLedger, lifecycle.ProofDurableCancel, lifecycle.ProofNoCommit},
		attemptEvidence(lifecycle.ErrorCanceled))
	if _, err := store.AllocateReconciled(testAttemptBinding(), first.AttemptID, strings.Repeat("e", 64)); !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("predecessor allocated more than one reconciled child: %v", err)
	}
	if _, err := store.AllocateReconciled(testAttemptBinding(), second.AttemptID, strings.Repeat("e", 64)); !errors.Is(err, ErrAttemptLedger) {
		t.Fatalf("non-unknown predecessor accepted: %v", err)
	}
}

func TestAttemptLedgerCannotFishForASecondIdentityFromTheSameBinding(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateCanceled,
		[]lifecycle.Proof{lifecycle.ProofCompleteLedger, lifecycle.ProofDurableCancel, lifecycle.ProofNoCommit},
		attemptEvidence(lifecycle.ErrorCanceled))
	if _, err := store.Allocate(testAttemptBinding()); !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("duplicate binding allocated another identity: %v", err)
	}
}

func TestAttemptLedgerInspectionAndEvidenceOnlyReconciliation(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateUnknown,
		[]lifecycle.Proof{lifecycle.ProofIncompleteTerminal}, attemptEvidence(lifecycle.ErrorTerminationAmbiguous))
	report, err := InspectAttemptLedger(store.root)
	if err != nil || !report.Complete || report.LedgerID != store.header.LedgerID || len(report.Attempts) != 1 ||
		report.Attempts[0].AttemptID != plan.AttemptID || report.Attempts[0].State != string(lifecycle.StateUnknown) {
		t.Fatalf("inspection report=%+v err=%v", report, err)
	}
	evidence := strings.Repeat("e", 64)
	reconciled, err := ReconcileAttemptLedger(store.root, plan.AttemptID, evidence)
	if err != nil || reconciled.AttemptID == plan.AttemptID || reconciled.PredecessorAttemptID != plan.AttemptID ||
		reconciled.State != string(lifecycle.StatePlanned) {
		t.Fatalf("reconciliation report=%+v err=%v", reconciled, err)
	}
	original, err := store.Inspect(plan.AttemptID)
	if err != nil || original.Projection.State != lifecycle.StateUnknown || len(original.Events) != 1 {
		t.Fatalf("original attempt mutated: %+v err=%v", original, err)
	}
	if _, err := ReconcileAttemptLedger(store.root, plan.AttemptID, evidence); !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("second reconciliation bypassed planned-attempt gate: %v", err)
	}
}

func TestAttemptSessionCancellationAndDeadlineRequirePhaseProofs(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	bindings := []lifecycle.Binding{testAttemptBinding(), testAttemptBinding(), testAttemptBinding(), testAttemptBinding()}
	for index := range bindings {
		bindings[index].Identity.TaskSHA256 = contentMinimizedDigestForTest(t, index+200)
	}
	plans, err := store.AllocateRoster(bindings)
	if err != nil {
		t.Fatal(err)
	}
	sessions := make([]*DurableAttemptSession, len(plans))
	for index, plan := range plans {
		sessions[index], err = NewDurableAttemptSession(store, plan)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions[0].Cancel(false, lifecycle.UnknownUsage()); err != nil {
		t.Fatal(err)
	}
	if err := sessions[1].Commit(); err != nil {
		t.Fatal(err)
	}
	if err := sessions[1].Timeout(true, lifecycle.UnknownUsage()); err != nil {
		t.Fatal(err)
	}
	if err := sessions[2].Commit(); err != nil {
		t.Fatal(err)
	}
	if err := sessions[2].SpawnIntent(); err != nil {
		t.Fatal(err)
	}
	if err := sessions[2].Cancel(false, lifecycle.UnknownUsage()); err != nil {
		t.Fatal(err)
	}
	if err := sessions[3].Commit(); err != nil {
		t.Fatal(err)
	}
	if err := sessions[3].SpawnIntent(); err != nil {
		t.Fatal(err)
	}
	if err := sessions[3].Running(strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := sessions[3].Timeout(false, lifecycle.UnknownUsage()); err != nil {
		t.Fatal(err)
	}
	want := []lifecycle.State{lifecycle.StateCanceled, lifecycle.StateTimedOut, lifecycle.StateUnknown, lifecycle.StateUnknown}
	for index, plan := range plans {
		inspection, err := store.Inspect(plan.AttemptID)
		if err != nil || inspection.Projection.State != want[index] {
			t.Fatalf("attempt %d state=%s want=%s err=%v", index, inspection.Projection.State, want[index], err)
		}
	}
}

func TestAttemptLedgerCorruptTailReturnsVerifiedPrefix(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateCommitted, []lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	eventPath := filepath.Join(store.attemptRoot(plan.Ordinal), attemptLedgerEvents, attemptLedgerEventName(2))
	if err := os.WriteFile(eventPath, []byte("{\"torn\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if !errors.Is(err, ErrAttemptLedgerIncomplete) || inspection.Complete || inspection.TailCode != "event_decode" ||
		inspection.Projection.State != lifecycle.StateCommitted || len(inspection.Events) != 1 {
		t.Fatalf("corrupt tail was not retained: inspection=%+v err=%v", inspection, err)
	}
	if _, err := store.Append(plan.AttemptID, lifecycle.StateUnknown, []lifecycle.Proof{lifecycle.ProofIncompleteTerminal}, attemptEvidence(lifecycle.ErrorInternal)); !errors.Is(err, ErrAttemptLedgerIncomplete) {
		t.Fatalf("append crossed corrupt tail: %v", err)
	}
	report, err := InspectAttemptLedger(store.root)
	if err != nil || report.Complete || len(report.Attempts) != 1 ||
		report.Attempts[0].State != string(lifecycle.StateUnknown) || !report.Attempts[0].Terminal ||
		report.Attempts[0].Complete || report.Attempts[0].TailCode != "event_decode" {
		t.Fatalf("corrupt tail was exposed as resumable: report=%+v err=%v", report, err)
	}
}

func TestAttemptLedgerInterruptedWritesRemainRecoverable(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	stop := errors.New("synthetic interruption")
	store.testHook = func(point string) error {
		if point == "after_plan_write" {
			return stop
		}
		return nil
	}
	plan, err := store.Allocate(testAttemptBinding())
	if err == nil || plan.AttemptID != "" {
		t.Fatalf("interrupted plan allocation unexpectedly succeeded: %+v %v", plan, err)
	}
	store.testHook = nil
	all, err := store.InspectAll()
	if err != nil || len(all) != 1 || !all[0].Complete || all[0].Projection.State != lifecycle.StatePlanned {
		t.Fatalf("written plan was not recoverable: all=%+v err=%v", all, err)
	}
	store.testHook = func(point string) error {
		if point == "after_event_write" {
			return stop
		}
		return nil
	}
	_, err = store.Append(all[0].Plan.AttemptID, lifecycle.StateCommitted, []lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	if err == nil {
		t.Fatal("interrupted event append unexpectedly succeeded")
	}
	store.testHook = nil
	inspection, err := store.Inspect(all[0].Plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateCommitted {
		t.Fatalf("written event was not recoverable: %+v %v", inspection, err)
	}
}

func TestAttemptLedgerRejectsAndCancelsBoundedCrashTailFallback(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	attemptRoot := store.attemptRoot(1)
	if err := os.Mkdir(attemptRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(attemptRoot, attemptLedgerEvents), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unexpected-a", "unexpected-b", "unexpected-c"} {
		if err := os.WriteFile(filepath.Join(attemptRoot, name), []byte("unexpected"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if inspections, err := store.InspectAll(); err == nil || len(inspections) != 1 || !errors.Is(err, ErrAttemptLedgerIncomplete) {
		t.Fatalf("overflowed crash tail inspections=%+v err=%v", inspections, err)
	}
	ctx := &attemptLedgerCancelContext{remaining: 10, done: make(chan struct{})}
	if inspections, err := store.InspectAllContext(ctx); !errors.Is(err, context.Canceled) || len(inspections) != 0 || !ctx.canceled {
		t.Fatalf("crash-tail cancellation inspections=%+v err=%v canceled=%t remaining=%d", inspections, err, ctx.canceled, ctx.remaining)
	}
}

type attemptLedgerCancelContext struct {
	remaining int
	done      chan struct{}
	canceled  bool
}

func (ctx *attemptLedgerCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *attemptLedgerCancelContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *attemptLedgerCancelContext) Value(any) any               { return nil }
func (ctx *attemptLedgerCancelContext) Err() error {
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

func TestAttemptLedgerEnsureRosterCompletesOnlyExactPlannedPrefix(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	bindings := []lifecycle.Binding{testAttemptBinding(), testAttemptBinding(), testAttemptBinding()}
	for index := range bindings {
		bindings[index].Identity.TaskSHA256 = contentMinimizedDigestForTest(t, index+900)
	}
	stop := errors.New("synthetic roster interruption")
	writes := 0
	store.testHook = func(point string) error {
		if point == "after_plan_write" {
			writes++
			if writes == 2 {
				return stop
			}
		}
		return nil
	}
	if plans, err := store.EnsureRoster(bindings); err == nil || plans != nil {
		t.Fatalf("interrupted roster unexpectedly succeeded: plans=%+v err=%v", plans, err)
	}
	store.testHook = nil
	plans, err := store.EnsureRoster(bindings)
	if err != nil || len(plans) != len(bindings) {
		t.Fatalf("exact planned prefix was not completed: plans=%+v err=%v", plans, err)
	}
	again, err := store.EnsureRoster(bindings)
	if err != nil || !reflect.DeepEqual(plans, again) {
		t.Fatalf("completed roster was not idempotent: again=%+v err=%v", again, err)
	}

	mutated := append([]lifecycle.Binding{}, bindings...)
	mutated[1].Identity.TaskSHA256 = contentMinimizedDigestForTest(t, 999)
	if _, err := store.EnsureRoster(mutated); !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("mutated roster was accepted: %v", err)
	}

	partialStore := newAttemptLedgerForTest(t)
	first, err := partialStore.EnsureRoster(bindings[:1])
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, partialStore, first[0], lifecycle.StateCommitted,
		[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	if _, err := partialStore.EnsureRoster(bindings); !errors.Is(err, ErrAttemptLedgerConflict) {
		t.Fatalf("committed partial roster was extended: %v", err)
	}
}

func TestAttemptLedgerRecoversUnpublishedCreatePlanAndEventTemps(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "ledger")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tmp-0000000000000001"), []byte("unpublished header"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := CreateAttemptLedgerStore(root, bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := store.attemptRoot(1)
	if err := os.Mkdir(attemptRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(attemptRoot, attemptLedgerEvents), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptRoot, ".tmp-0000000000000002"), []byte("unpublished plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if all, err := store.InspectAll(); err != nil || len(all) != 0 {
		t.Fatalf("unpublished plan became an attempt: all=%+v err=%v", all, err)
	}
	plan, err := store.Allocate(testAttemptBinding())
	if err != nil || plan.Ordinal != 1 {
		t.Fatalf("unpublished ordinal was not safely reused: plan=%+v err=%v", plan, err)
	}
	if err := os.WriteFile(filepath.Join(attemptRoot, attemptLedgerEvents, ".tmp-0000000000000003"), []byte("unpublished event"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateCommitted,
		[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || !inspection.Complete || inspection.Projection.State != lifecycle.StateCommitted || len(inspection.Events) != 1 {
		t.Fatalf("unpublished temp changed the committed prefix: inspection=%+v err=%v", inspection, err)
	}
}

func TestAttemptLedgerCrashAtEveryTransitionConvergesWithoutReplay(t *testing.T) {
	if os.Getenv("ATL_ATTEMPT_LEDGER_CRASH_HELPER") == "1" {
		runAttemptLedgerCrashHelper()
		return
	}
	if testing.Short() {
		t.Skip("subprocess crash matrix is an integration-strength oracle")
	}
	for _, from := range lifecycle.States() {
		for _, to := range lifecycle.States() {
			proofSets := lifecycle.AllowedProofSets(from, to)
			if len(proofSets) == 0 {
				continue
			}
			for _, point := range []string{"before_event_write", "after_event_write"} {
				name := string(from) + "_to_" + string(to) + "_" + point
				t.Run(name, func(t *testing.T) {
					store := newAttemptLedgerForTest(t)
					plan, err := store.Allocate(testAttemptBinding())
					if err != nil {
						t.Fatal(err)
					}
					advanceAttemptForCrashTest(t, store, plan, from)
					command := exec.Command(os.Args[0], "-test.run=^TestAttemptLedgerCrashAtEveryTransitionConvergesWithoutReplay$")
					command.Env = append(os.Environ(),
						"ATL_ATTEMPT_LEDGER_CRASH_HELPER=1",
						"ATL_ATTEMPT_LEDGER_ROOT="+store.root,
						"ATL_ATTEMPT_LEDGER_ID="+plan.AttemptID,
						"ATL_ATTEMPT_LEDGER_FROM="+string(from),
						"ATL_ATTEMPT_LEDGER_TO="+string(to),
						"ATL_ATTEMPT_LEDGER_POINT="+point,
					)
					if err := command.Run(); err == nil {
						t.Fatal("crash helper exited successfully")
					}
					reopened, err := OpenAttemptLedgerStore(store.root)
					if err != nil {
						t.Fatal(err)
					}
					if err := reopened.RecoverIncomplete(); err != nil {
						t.Fatal(err)
					}
					inspection, err := reopened.Inspect(plan.AttemptID)
					if err != nil || !inspection.Complete || !inspection.Projection.Terminal {
						t.Fatalf("crash did not converge: inspection=%+v err=%v", inspection, err)
					}
					if inspection.Projection.State == lifecycle.StateSucceeded && to != lifecycle.StateSucceeded {
						t.Fatalf("crash invented success: %+v", inspection)
					}
					before := len(inspection.Events)
					if err := reopened.RecoverIncomplete(); err != nil {
						t.Fatal(err)
					}
					again, err := reopened.Inspect(plan.AttemptID)
					if err != nil || len(again.Events) != before || again.Projection != inspection.Projection {
						t.Fatalf("recovery replayed terminal attempt: before=%+v after=%+v err=%v", inspection, again, err)
					}
				})
			}
		}
	}
}

func runAttemptLedgerCrashHelper() {
	store, err := OpenAttemptLedgerStore(os.Getenv("ATL_ATTEMPT_LEDGER_ROOT"))
	if err != nil {
		os.Exit(91)
	}
	from := lifecycle.State(os.Getenv("ATL_ATTEMPT_LEDGER_FROM"))
	to := lifecycle.State(os.Getenv("ATL_ATTEMPT_LEDGER_TO"))
	sets := lifecycle.AllowedProofSets(from, to)
	if len(sets) == 0 {
		os.Exit(92)
	}
	store.testHook = func(point string) error {
		if point == os.Getenv("ATL_ATTEMPT_LEDGER_POINT") {
			os.Exit(23)
		}
		return nil
	}
	_, _ = store.Append(os.Getenv("ATL_ATTEMPT_LEDGER_ID"), to, sets[0], crashEvidenceForState(to, sets[0]))
	os.Exit(93)
}

func advanceAttemptForCrashTest(t *testing.T, store *AttemptLedgerStore, plan lifecycle.Plan, target lifecycle.State) {
	t.Helper()
	if target == lifecycle.StatePlanned {
		return
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateCommitted,
		[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
	if target == lifecycle.StateCommitted {
		return
	}
	appendAttemptEventForTest(t, store, plan, lifecycle.StateSpawning,
		[]lifecycle.Proof{lifecycle.ProofDurableSpawnIntent}, attemptEvidence(lifecycle.ErrorNone))
	if target == lifecycle.StateSpawning {
		return
	}
	running := attemptEvidence(lifecycle.ErrorNone)
	running.ProcessIdentitySHA256 = strings.Repeat("b", 64)
	appendAttemptEventForTest(t, store, plan, lifecycle.StateRunning,
		[]lifecycle.Proof{lifecycle.ProofDurableProcessIdentity}, running)
}

func crashEvidenceForState(to lifecycle.State, proofs []lifecycle.Proof) lifecycle.Evidence {
	evidence := attemptEvidence(lifecycle.ErrorNone)
	switch to {
	case lifecycle.StatePolicyDenied:
		evidence.ErrorClass = lifecycle.ErrorPolicyDenied
	case lifecycle.StateUnsupported:
		evidence.ErrorClass = lifecycle.ErrorUnsupported
	case lifecycle.StateCanceled:
		evidence.ErrorClass = lifecycle.ErrorCanceled
	case lifecycle.StateTimedOut:
		evidence.ErrorClass = lifecycle.ErrorDeadline
	case lifecycle.StateFailed:
		evidence.ErrorClass = lifecycle.ErrorComponentFailure
		for _, proof := range proofs {
			if proof == lifecycle.ProofDefinitiveSpawnFailure {
				evidence.ErrorClass = lifecycle.ErrorSpawnFailure
			}
		}
	case lifecycle.StateUnknown:
		evidence.ErrorClass = lifecycle.ErrorTerminationAmbiguous
	case lifecycle.StateRunning:
		evidence.ProcessIdentitySHA256 = strings.Repeat("b", 64)
	}
	for _, proof := range proofs {
		if proof == lifecycle.ProofTerminalReceipt {
			evidence.ReceiptSHA256 = strings.Repeat("c", 64)
		}
	}
	return evidence
}

func TestAttemptLedgerConcurrentWritersPreserveOrderAndIntegrity(t *testing.T) {
	store := newAttemptLedgerForTest(t)
	const attempts = 32
	bindings := make([]lifecycle.Binding, attempts)
	for index := range bindings {
		bindings[index] = testAttemptBinding()
		bindings[index].Identity.TaskSHA256 = contentMinimizedDigestForTest(t, index)
	}
	roster, err := store.AllocateRoster(bindings)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, attempts)
	for _, plan := range roster {
		wait.Add(1)
		go func(plan lifecycle.Plan) {
			defer wait.Done()
			_, err := store.Append(plan.AttemptID, lifecycle.StateCommitted,
				[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
			if err != nil {
				errorsSeen <- err
			}
		}(plan)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	ordinals := make([]int, 0, attempts)
	identities := map[string]bool{}
	for _, plan := range roster {
		ordinals = append(ordinals, int(plan.Ordinal))
		if identities[plan.AttemptID] {
			t.Fatalf("duplicate attempt id %s", plan.AttemptID)
		}
		identities[plan.AttemptID] = true
	}
	sort.Ints(ordinals)
	for index, ordinal := range ordinals {
		if ordinal != index+1 {
			t.Fatalf("ordinal roster drift: %v", ordinals)
		}
	}
	all, err := store.InspectAll()
	if err != nil || len(all) != attempts {
		t.Fatalf("concurrent roster invalid: len=%d err=%v", len(all), err)
	}
	for _, inspection := range all {
		if inspection.Projection.State != lifecycle.StateCommitted {
			t.Fatalf("concurrent append lost: %+v", inspection)
		}
	}
}

func TestAttemptLedgerRejectsSymlinkAndUnsafeModes(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(parent, "ledger")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateAttemptLedgerStore(link, bytes.NewReader(bytes.Repeat([]byte{1}, 32))); !errors.Is(err, ErrAttemptLedger) {
		t.Fatalf("symlink destination accepted: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	store, err := CreateAttemptLedgerStore(link, bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(store.root, attemptLedgerHeaderName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAttemptLedgerStore(store.root); !errors.Is(err, ErrAttemptLedger) {
		t.Fatalf("public ledger file accepted: %v", err)
	}
}

func newAttemptLedgerForTest(t *testing.T) *AttemptLedgerStore {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := CreateAttemptLedgerStore(filepath.Join(parent, "ledger"), bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func attemptLedgerRootForTest(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "attempt-ledger")
}

func testAttemptBinding() lifecycle.Binding {
	digest := strings.Repeat("a", 64)
	return lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: lifecycle.Identity{
		ExperimentSHA256: digest, TaskSHA256: digest, SkillSHA256: digest, AgentSHA256: digest,
		ModelSHA256: digest, EnvironmentSHA256: digest, GraderSHA256: digest, BudgetsSHA256: digest, AdapterSHA256: digest,
		AuthoritySHA256: digest,
	}}
}

func attemptEvidence(code string) lifecycle.Evidence {
	return lifecycle.Evidence{ErrorClass: code, Usage: lifecycle.UnknownUsage()}
}

func appendAttemptEventForTest(t *testing.T, store *AttemptLedgerStore, plan lifecycle.Plan, to lifecycle.State, proofs []lifecycle.Proof, evidence lifecycle.Evidence) {
	t.Helper()
	if _, err := store.Append(plan.AttemptID, to, proofs, evidence); err != nil {
		t.Fatal(err)
	}
}

func contentMinimizedDigestForTest(t *testing.T, value int) string {
	t.Helper()
	digest, err := contentMinimizedAttemptDigest("test-task", value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
