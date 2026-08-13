package agenteval

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	attemptLedgerHeaderName = "ledger.json"
	attemptLedgerLockName   = ".lock"
	attemptLedgerAttempts   = "attempts"
	attemptLedgerPlanName   = "plan.json"
	attemptLedgerEvents     = "events"
)

var (
	ErrAttemptLedger            = errors.New("attempt_ledger_invalid")
	ErrAttemptLedgerBusy        = errors.New("attempt_ledger_busy")
	ErrAttemptLedgerIncomplete  = errors.New("attempt_ledger_incomplete")
	ErrAttemptLedgerUnsupported = errors.New("attempt_ledger_unsupported")
	ErrAttemptLedgerConflict    = errors.New("attempt_ledger_conflict")
)

type AttemptLedgerStore struct {
	root     string
	header   lifecycle.LedgerHeader
	testHook func(string) error
	local    attemptLedgerLocalLock
}

type AttemptLedgerInspection struct {
	Plan       lifecycle.Plan
	Events     []lifecycle.Event
	Projection lifecycle.Projection
	Complete   bool
	TailCode   string
}

func CreateAttemptLedgerStore(root string, random io.Reader) (*AttemptLedgerStore, error) {
	if random == nil {
		random = rand.Reader
	}
	absRoot, err := filepath.Abs(root)
	if err != nil || absRoot != filepath.Clean(root) || runtime.GOOS == "windows" {
		return nil, attemptLedgerError("create", ErrAttemptLedgerUnsupported, err)
	}
	parent := filepath.Dir(absRoot)
	if err := requirePrivateDirectory("attempt ledger parent", parent); err != nil {
		return nil, attemptLedgerError("parent", err)
	}
	rootInfo, rootErr := hardenedStatWithin(parent, absRoot)
	rootCreated := false
	if os.IsNotExist(rootErr) {
		if err := mkdirPrivateWithin(parent, absRoot); err != nil {
			return nil, attemptLedgerError("root", err)
		}
		rootCreated = true
	} else if rootErr != nil || !rootInfo.IsDir() || !privateWorkspaceDirectoryMode(rootInfo.Mode()) {
		return nil, attemptLedgerError("root", rootErr)
	}
	store := &AttemptLedgerStore{root: absRoot}
	lock, err := store.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if !rootCreated {
		if existing, openErr := openAttemptLedgerStoreUnlocked(absRoot); openErr == nil {
			return existing, nil
		} else if !attemptLedgerOnlyUncommittedCreateState(absRoot) {
			return nil, attemptLedgerError("existing_root", openErr)
		}
	}
	if err := ensureAttemptLedgerAttemptsDirectory(absRoot); err != nil {
		return nil, attemptLedgerError("attempts", err)
	}
	header, err := lifecycle.NewHeader(random)
	if err != nil {
		return nil, attemptLedgerError("header", err)
	}
	headerBytes, err := lifecycle.EncodeHeader(header)
	if err != nil {
		return nil, attemptLedgerError("header", err)
	}
	if err := hardenedWriteFileExclusiveWithin(absRoot, filepath.Join(absRoot, attemptLedgerHeaderName), headerBytes, 0o600); err != nil {
		return nil, attemptLedgerError("header_write", err)
	}
	if err := syncAttemptLedgerDirectory(absRoot, filepath.Join(absRoot, attemptLedgerAttempts)); err != nil {
		return nil, err
	}
	if err := syncAttemptLedgerDirectory(absRoot, absRoot); err != nil {
		return nil, err
	}
	if rootCreated {
		if err := syncAttemptLedgerDirectory(parent, parent); err != nil {
			return nil, err
		}
	}
	store.header = header
	if _, err := openAttemptLedgerStoreUnlocked(absRoot); err != nil {
		return nil, attemptLedgerError("readback", err)
	}
	return store, nil
}

func OpenAttemptLedgerStore(root string) (*AttemptLedgerStore, error) {
	return OpenAttemptLedgerStoreContext(context.Background(), root)
}

func OpenAttemptLedgerStoreContext(ctx context.Context, root string) (*AttemptLedgerStore, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil || absRoot != filepath.Clean(root) || runtime.GOOS == "windows" {
		return nil, attemptLedgerError("open", ErrAttemptLedgerUnsupported, err)
	}
	return openAttemptLedgerStoreUnlockedContext(ctx, absRoot)
}

// OpenAttemptLedgerStoreStrictContext opens a completed-publication ledger
// without creating or tolerating recovery-only temporary members. The caller
// must subsequently use InspectAllStrictContext while holding no write
// authority over the source tree.
func OpenAttemptLedgerStoreStrictContext(ctx context.Context, root string) (*AttemptLedgerStore, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil || absRoot != filepath.Clean(root) || runtime.GOOS == "windows" {
		return nil, attemptLedgerError("strict_open", ErrAttemptLedgerUnsupported, err)
	}
	if err := requirePrivateDirectory("attempt ledger", absRoot); err != nil {
		return nil, attemptLedgerError("strict_root", err)
	}
	top, err := hardenedReadDirWithinLimitContext(ctx, absRoot, absRoot, 3)
	if err != nil || !attemptLedgerEntryNamesEqual(top, []string{attemptLedgerLockName, attemptLedgerAttempts, attemptLedgerHeaderName}) {
		return nil, attemptLedgerError("strict_top_level", err)
	}
	header, err := readAttemptLedgerHeaderContext(ctx, absRoot)
	if err != nil {
		return nil, err
	}
	return &AttemptLedgerStore{root: absRoot, header: header}, nil
}

func openAttemptLedgerStoreUnlocked(absRoot string) (*AttemptLedgerStore, error) {
	return openAttemptLedgerStoreUnlockedContext(context.Background(), absRoot)
}

func openAttemptLedgerStoreUnlockedContext(ctx context.Context, absRoot string) (*AttemptLedgerStore, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	if err := requirePrivateDirectory("attempt ledger", absRoot); err != nil {
		return nil, attemptLedgerError("root", err)
	}
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	header, err := readAttemptLedgerHeaderContext(ctx, absRoot)
	if err != nil {
		return nil, err
	}
	if err := validateAttemptLedgerTopLevelContext(ctx, absRoot); err != nil {
		return nil, err
	}
	return &AttemptLedgerStore{root: absRoot, header: header}, nil
}

func (store *AttemptLedgerStore) Header() lifecycle.LedgerHeader { return store.header }

func (store *AttemptLedgerStore) Allocate(binding lifecycle.Binding) (lifecycle.Plan, error) {
	return store.allocate(binding, "", "")
}

func (store *AttemptLedgerStore) AllocateReconciled(binding lifecycle.Binding, predecessorAttemptID, reconciliationSHA256 string) (lifecycle.Plan, error) {
	inspection, err := store.Inspect(predecessorAttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateUnknown {
		return lifecycle.Plan{}, attemptLedgerError("reconciliation_predecessor", err)
	}
	return store.allocate(binding, predecessorAttemptID, reconciliationSHA256)
}

func (store *AttemptLedgerStore) allocate(binding lifecycle.Binding, predecessorAttemptID, reconciliationSHA256 string) (lifecycle.Plan, error) {
	lock, err := store.lock()
	if err != nil {
		return lifecycle.Plan{}, err
	}
	defer func() { _ = lock.Unlock() }()
	plans, err := store.readAllLocked()
	if err != nil {
		return lifecycle.Plan{}, err
	}
	for _, inspection := range plans {
		if inspection.Projection.State == lifecycle.StatePlanned {
			return lifecycle.Plan{}, attemptLedgerError("planned_attempt_exists", ErrAttemptLedgerConflict)
		}
	}
	if len(plans) >= lifecycle.MaxAttempts {
		return lifecycle.Plan{}, attemptLedgerError("attempt_capacity")
	}
	ordinal := uint32(len(plans) + 1) // #nosec G115 -- len is bounded by MaxAttempts above.
	return store.writePlanLocked(ordinal, binding, predecessorAttemptID, reconciliationSHA256)
}

func (store *AttemptLedgerStore) AllocateRoster(bindings []lifecycle.Binding) ([]lifecycle.Plan, error) {
	if len(bindings) == 0 || len(bindings) > lifecycle.MaxAttempts {
		return nil, attemptLedgerError("roster_limit")
	}
	lock, err := store.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	existing, err := store.readAllLocked()
	if err != nil {
		return nil, err
	}
	planned := make([]lifecycle.Plan, 0)
	for _, inspection := range existing {
		if inspection.Projection.State == lifecycle.StatePlanned {
			planned = append(planned, inspection.Plan)
		}
	}
	if len(planned) != 0 {
		return nil, attemptLedgerError("planned_roster", ErrAttemptLedgerConflict)
	}
	if len(existing)+len(bindings) > lifecycle.MaxAttempts {
		return nil, attemptLedgerError("roster_capacity")
	}
	result := make([]lifecycle.Plan, 0, len(bindings))
	for index, binding := range bindings {
		ordinal := uint32(len(existing) + index + 1) // #nosec G115 -- the combined length is bounded by MaxAttempts above.
		plan, writeErr := store.writePlanLocked(ordinal, binding, "", "")
		if writeErr != nil {
			return nil, writeErr
		}
		result = append(result, plan)
	}
	return result, nil
}

// EnsureRoster atomically validates the immutable binding prefix already
// present in the ledger and completes only a crash-interrupted planned roster.
// Once any existing member has left planned, an incomplete or changed roster
// is rejected: committed execution can never authorize new repetitions or a
// different order.
func (store *AttemptLedgerStore) EnsureRoster(bindings []lifecycle.Binding) ([]lifecycle.Plan, error) {
	if len(bindings) == 0 || len(bindings) > lifecycle.MaxAttempts {
		return nil, attemptLedgerError("roster_limit")
	}
	lock, err := store.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	existing, err := store.readAllLocked()
	if err != nil {
		return nil, err
	}
	if len(existing) > len(bindings) {
		return nil, attemptLedgerError("roster_length", ErrAttemptLedgerConflict)
	}
	result := make([]lifecycle.Plan, 0, len(bindings))
	for index, inspection := range existing {
		if index >= len(bindings) {
			return nil, attemptLedgerError("roster_length", ErrAttemptLedgerConflict)
		}
		want, planErr := lifecycle.NewPlan(store.header, uint32(index+1), bindings[index]) // #nosec G115,G602 -- the local guard proves the binding index and MaxAttempts bounds the ordinal.
		if planErr != nil || inspection.Plan.PlanSHA256 != want.PlanSHA256 {
			return nil, attemptLedgerError("roster_binding", ErrAttemptLedgerConflict, planErr)
		}
		result = append(result, inspection.Plan)
	}
	if len(existing) == len(bindings) {
		return result, nil
	}
	for _, inspection := range existing {
		if inspection.Projection.State != lifecycle.StatePlanned {
			return nil, attemptLedgerError("roster_committed", ErrAttemptLedgerConflict)
		}
	}
	for index := len(existing); index < len(bindings); index++ {
		ordinal := uint32(index + 1) // #nosec G115 -- bindings are bounded by lifecycle.MaxAttempts.
		plan, writeErr := store.writePlanLocked(ordinal, bindings[index], "", "")
		if writeErr != nil {
			return nil, writeErr
		}
		result = append(result, plan)
	}
	return result, nil
}

func (store *AttemptLedgerStore) writePlanLocked(ordinal uint32, binding lifecycle.Binding, predecessorAttemptID, reconciliationSHA256 string) (lifecycle.Plan, error) {
	var plan lifecycle.Plan
	var err error
	if predecessorAttemptID == "" {
		plan, err = lifecycle.NewPlan(store.header, ordinal, binding)
	} else {
		plan, err = lifecycle.NewReconciledPlan(store.header, ordinal, predecessorAttemptID, reconciliationSHA256, binding)
	}
	if err != nil {
		return lifecycle.Plan{}, attemptLedgerError("plan", err)
	}
	existing, readErr := store.readAllLocked()
	if readErr != nil {
		return lifecycle.Plan{}, readErr
	}
	for _, inspection := range existing {
		if predecessorAttemptID == "" {
			if inspection.Plan.BindingSHA256 == plan.BindingSHA256 {
				return lifecycle.Plan{}, attemptLedgerError("duplicate_binding", ErrAttemptLedgerConflict)
			}
		} else if inspection.Plan.PredecessorAttemptID == predecessorAttemptID {
			return lifecycle.Plan{}, attemptLedgerError("duplicate_reconciliation", ErrAttemptLedgerConflict)
		}
	}
	attemptRoot := filepath.Join(store.root, attemptLedgerAttempts, attemptLedgerOrdinalName(ordinal))
	if err := store.prepareAttemptDirectoryLocked(ordinal); err != nil {
		return lifecycle.Plan{}, attemptLedgerError("attempt_directory", err)
	}
	data, err := lifecycle.EncodePlan(plan)
	if err != nil {
		return lifecycle.Plan{}, attemptLedgerError("plan_encode", err)
	}
	if err := hardenedWriteFileExclusiveWithin(store.root, filepath.Join(attemptRoot, attemptLedgerPlanName), data, 0o600); err != nil {
		return lifecycle.Plan{}, attemptLedgerError("plan_write", err)
	}
	if store.testHook != nil {
		if err := store.testHook("after_plan_write"); err != nil {
			return lifecycle.Plan{}, attemptLedgerError("plan_interrupted", err)
		}
	}
	if err := syncAttemptLedgerDirectory(store.root, filepath.Join(attemptRoot, attemptLedgerEvents)); err != nil {
		return lifecycle.Plan{}, err
	}
	if err := syncAttemptLedgerDirectory(store.root, attemptRoot); err != nil {
		return lifecycle.Plan{}, err
	}
	if err := syncAttemptLedgerDirectory(store.root, filepath.Join(store.root, attemptLedgerAttempts)); err != nil {
		return lifecycle.Plan{}, err
	}
	inspection, err := store.inspectOrdinalLocked(ordinal)
	if err != nil || inspection.Plan.PlanSHA256 != plan.PlanSHA256 {
		return lifecycle.Plan{}, attemptLedgerError("plan_readback", err)
	}
	return plan, nil
}

func (store *AttemptLedgerStore) Append(attemptID string, to lifecycle.State, proofs []lifecycle.Proof, evidence lifecycle.Evidence) (lifecycle.Event, error) {
	lock, err := store.lock()
	if err != nil {
		return lifecycle.Event{}, err
	}
	defer func() { _ = lock.Unlock() }()
	return store.appendLocked(attemptID, to, proofs, evidence)
}

func (store *AttemptLedgerStore) appendLocked(attemptID string, to lifecycle.State, proofs []lifecycle.Proof, evidence lifecycle.Evidence) (lifecycle.Event, error) {
	inspection, err := store.inspectIDLocked(attemptID)
	if err != nil {
		return lifecycle.Event{}, err
	}
	event, err := lifecycle.NewEvent(inspection.Plan, inspection.Projection, to, proofs, evidence)
	if err != nil {
		return lifecycle.Event{}, attemptLedgerError("transition", err)
	}
	data, err := lifecycle.EncodeEvent(event)
	if err != nil {
		return lifecycle.Event{}, attemptLedgerError("event_encode", err)
	}
	eventPath := filepath.Join(store.attemptRoot(inspection.Plan.Ordinal), attemptLedgerEvents, attemptLedgerEventName(event.Sequence))
	if store.testHook != nil {
		if err := store.testHook("before_event_write"); err != nil {
			return lifecycle.Event{}, attemptLedgerError("event_interrupted", err)
		}
	}
	if err := hardenedWriteFileExclusiveWithin(store.root, eventPath, data, 0o600); err != nil {
		return lifecycle.Event{}, attemptLedgerError("event_write", err)
	}
	if store.testHook != nil {
		if err := store.testHook("after_event_write"); err != nil {
			return lifecycle.Event{}, attemptLedgerError("event_interrupted", err)
		}
	}
	if err := syncAttemptLedgerDirectory(store.root, filepath.Dir(eventPath)); err != nil {
		return lifecycle.Event{}, err
	}
	verified, err := store.inspectOrdinalLocked(inspection.Plan.Ordinal)
	if err != nil || verified.Projection.LastSHA256 != event.EventSHA256 {
		return lifecycle.Event{}, attemptLedgerError("event_readback", err)
	}
	return event, nil
}

func (store *AttemptLedgerStore) RecoverIncomplete() error {
	lock, err := store.lock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	all, err := store.readAllLocked()
	if err != nil {
		return err
	}
	for _, inspection := range all {
		if inspection.Projection.Terminal {
			continue
		}
		to := lifecycle.StateUnknown
		proofs := []lifecycle.Proof{lifecycle.ProofIncompleteTerminal}
		errorClass := lifecycle.ErrorTerminationAmbiguous
		if inspection.Projection.State == lifecycle.StatePlanned {
			to = lifecycle.StateCanceled
			proofs = []lifecycle.Proof{lifecycle.ProofCompleteLedger, lifecycle.ProofDurableCancel, lifecycle.ProofNoCommit}
			errorClass = lifecycle.ErrorCanceled
		}
		if _, err := store.appendLocked(inspection.Plan.AttemptID, to, proofs,
			attemptEvidenceWithUsage(errorClass, inspection.Projection.Usage)); err != nil {
			return err
		}
	}
	return nil
}

func (store *AttemptLedgerStore) Inspect(attemptID string) (AttemptLedgerInspection, error) {
	lock, err := store.lock()
	if err != nil {
		return AttemptLedgerInspection{}, err
	}
	defer func() { _ = lock.Unlock() }()
	return store.inspectIDLocked(attemptID)
}

func (store *AttemptLedgerStore) InspectAll() ([]AttemptLedgerInspection, error) {
	return store.InspectAllContext(context.Background())
}

func (store *AttemptLedgerStore) InspectAllContext(ctx context.Context) ([]AttemptLedgerInspection, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	lock, err := store.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	return store.readAllLockedContext(ctx)
}

// InspectAllStrictContext is the read-only completed-publication contour. In
// contrast with recovery inspection it requires the physical ledger tree to
// contain exactly expectedAttempts committed entries: no ignored temporary,
// crash-tail, or uncommitted ordinal is accepted. The exact expected roster is
// also the directory-read bound, so an oversized physical tree is rejected
// before any unexpected attempt is decoded.
func (store *AttemptLedgerStore) InspectAllStrictContext(ctx context.Context, expectedAttempts int) ([]AttemptLedgerInspection, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	if expectedAttempts <= 0 || expectedAttempts > lifecycle.MaxAttempts {
		return nil, attemptLedgerError("strict_expected_attempts")
	}
	lock, err := store.readOnlyLock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	return store.readAllStrictLockedContext(ctx, expectedAttempts)
}

func (store *AttemptLedgerStore) readAllStrictLockedContext(ctx context.Context, expectedAttempts int) ([]AttemptLedgerInspection, error) {
	top, err := hardenedReadDirWithinLimitContext(ctx, store.root, store.root, 3)
	if err != nil || !attemptLedgerEntryNamesEqual(top, []string{attemptLedgerLockName, attemptLedgerAttempts, attemptLedgerHeaderName}) {
		return nil, attemptLedgerError("strict_top_level", err)
	}
	attemptsRoot := filepath.Join(store.root, attemptLedgerAttempts)
	entries, err := hardenedReadDirWithinLimitContext(ctx, store.root, attemptsRoot, expectedAttempts)
	if err != nil || len(entries) != expectedAttempts {
		return nil, attemptLedgerError("strict_attempts", err)
	}
	result := make([]AttemptLedgerInspection, 0, len(entries))
	for index, entry := range entries {
		if err := attemptLedgerContextError(ctx); err != nil {
			return result, err
		}
		ordinal := uint32(index + 1)
		info, infoErr := entry.Info()
		if infoErr != nil || entry.Name() != attemptLedgerOrdinalName(ordinal) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!privateWorkspaceDirectoryMode(info.Mode()) {
			return result, attemptLedgerError("strict_attempt_order", ErrAttemptLedgerIncomplete)
		}
		attemptRoot := store.attemptRoot(ordinal)
		attemptEntries, readErr := hardenedReadDirWithinLimitContext(ctx, store.root, attemptRoot, 2)
		if readErr != nil || !attemptLedgerEntryNamesEqual(attemptEntries, []string{attemptLedgerEvents, attemptLedgerPlanName}) {
			return result, attemptLedgerError("strict_attempt_shape", readErr)
		}
		inspection, inspectErr := store.inspectOrdinalLockedContext(ctx, ordinal)
		if inspectErr != nil || !inspection.Complete {
			return append(result, inspection), attemptLedgerError("strict_attempt_incomplete", inspectErr)
		}
		eventsRoot := filepath.Join(attemptRoot, attemptLedgerEvents)
		events, readErr := hardenedReadDirWithinLimitContext(ctx, store.root, eventsRoot, lifecycle.MaxEvents)
		if readErr != nil || len(events) != len(inspection.Events) {
			return append(result, inspection), attemptLedgerError("strict_events", readErr)
		}
		for eventIndex, event := range events {
			if event.Name() != attemptLedgerEventName(uint32(eventIndex+1)) || event.IsDir() || event.Type()&os.ModeSymlink != 0 {
				return append(result, inspection), attemptLedgerError("strict_event_order", ErrAttemptLedgerIncomplete)
			}
		}
		result = append(result, inspection)
	}
	return result, nil
}

func attemptLedgerEntryNamesEqual(entries []os.DirEntry, want []string) bool {
	if len(entries) != len(want) {
		return false
	}
	for index := range want {
		if entries[index].Name() != want[index] || entries[index].Type()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func (store *AttemptLedgerStore) readAllLocked() ([]AttemptLedgerInspection, error) {
	return store.readAllLockedContext(context.Background())
}

func (store *AttemptLedgerStore) readAllLockedContext(ctx context.Context) ([]AttemptLedgerInspection, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := hardenedReadDirWithinLimitContext(ctx, store.root, filepath.Join(store.root, attemptLedgerAttempts), lifecycle.MaxAttempts+1)
	if err != nil {
		return nil, attemptLedgerError("attempts_read", err)
	}
	entries, err = committedAttemptLedgerEntries(entries, lifecycle.MaxAttempts)
	if err != nil {
		return nil, attemptLedgerError("attempts_shape", err)
	}
	if len(entries) > lifecycle.MaxAttempts {
		return nil, attemptLedgerError("attempts_limit")
	}
	result := make([]AttemptLedgerInspection, 0, len(entries))
	for index, entry := range entries {
		if err := attemptLedgerContextError(ctx); err != nil {
			return result, err
		}
		ordinal := uint32(index + 1)
		info, infoErr := entry.Info()
		if infoErr != nil || entry.Name() != attemptLedgerOrdinalName(ordinal) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!privateWorkspaceDirectoryMode(info.Mode()) {
			return result, attemptLedgerError("attempt_order", ErrAttemptLedgerIncomplete)
		}
		inspection, inspectErr := store.inspectOrdinalLockedContext(ctx, ordinal)
		if inspectErr != nil {
			if contextErr := attemptLedgerContextError(ctx); contextErr != nil {
				return result, contextErr
			}
			if index == len(entries)-1 && inspection.Plan.AttemptID == "" {
				uncommitted := store.uncommittedAttemptDirectoryLockedContext(ctx, ordinal)
				if contextErr := attemptLedgerContextError(ctx); contextErr != nil {
					return result, contextErr
				}
				if uncommitted {
					return result, nil
				}
			}
			return append(result, inspection), inspectErr
		}
		result = append(result, inspection)
	}
	return result, nil
}

func (store *AttemptLedgerStore) inspectIDLocked(attemptID string) (AttemptLedgerInspection, error) {
	if len(attemptID) != 64 {
		return AttemptLedgerInspection{}, attemptLedgerError("attempt_id")
	}
	all, err := store.readAllLocked()
	for _, inspection := range all {
		if inspection.Plan.AttemptID == attemptID {
			return inspection, err
		}
	}
	if err != nil {
		return AttemptLedgerInspection{}, err
	}
	return AttemptLedgerInspection{}, attemptLedgerError("attempt_missing")
}

func (store *AttemptLedgerStore) inspectOrdinalLocked(ordinal uint32) (AttemptLedgerInspection, error) {
	return store.inspectOrdinalLockedContext(context.Background(), ordinal)
}

func (store *AttemptLedgerStore) inspectOrdinalLockedContext(ctx context.Context, ordinal uint32) (AttemptLedgerInspection, error) {
	result := AttemptLedgerInspection{Complete: false}
	incomplete := func(code string, causes ...error) (AttemptLedgerInspection, error) {
		result.TailCode = code
		return result, incompleteAttemptLedger(code, causes...)
	}
	attemptRoot := store.attemptRoot(ordinal)
	if err := attemptLedgerContextError(ctx); err != nil {
		return result, err
	}
	entries, err := hardenedReadDirWithinLimitContext(ctx, store.root, attemptRoot, 3)
	if err == nil {
		entries, err = committedAttemptLedgerEntries(entries, 2)
	}
	if err != nil || len(entries) != 2 || entries[0].Name() != attemptLedgerEvents || !entries[0].IsDir() ||
		entries[1].Name() != attemptLedgerPlanName || entries[1].Type()&os.ModeSymlink != 0 {
		return incomplete("attempt_shape", err)
	}
	eventsInfo, err := entries[0].Info()
	if err != nil || !privateWorkspaceDirectoryMode(eventsInfo.Mode()) {
		return incomplete("events_mode", err)
	}
	planData, err := readAttemptLedgerFileContext(ctx, store.root, filepath.Join(attemptRoot, attemptLedgerPlanName), lifecycle.MaxPlanBytes)
	if err != nil {
		return incomplete("plan_read", err)
	}
	plan, err := lifecycle.DecodePlan(planData)
	if err != nil || plan.LedgerID != store.header.LedgerID || plan.Ordinal != ordinal {
		return incomplete("plan_decode", err)
	}
	result.Plan = plan
	if err := attemptLedgerContextError(ctx); err != nil {
		return result, err
	}
	projection, err := lifecycle.InitialProjection(plan)
	if err != nil {
		return result, incompleteAttemptLedger("plan_projection", err)
	}
	eventEntries, err := hardenedReadDirWithinLimitContext(ctx, store.root, filepath.Join(attemptRoot, attemptLedgerEvents), lifecycle.MaxEvents+1)
	if err == nil {
		eventEntries, err = committedAttemptLedgerEntries(eventEntries, lifecycle.MaxEvents)
	}
	if err != nil || len(eventEntries) > lifecycle.MaxEvents {
		result.Projection = projection
		return incomplete("events_read", err)
	}
	for index, entry := range eventEntries {
		if err := attemptLedgerContextError(ctx); err != nil {
			result.Projection = projection
			return result, err
		}
		sequence := uint32(index + 1)
		if entry.Name() != attemptLedgerEventName(sequence) || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			result.Projection, result.TailCode = projection, "event_order"
			return result, incompleteAttemptLedger("event_order")
		}
		data, readErr := readAttemptLedgerFileContext(ctx, store.root, filepath.Join(attemptRoot, attemptLedgerEvents, entry.Name()), lifecycle.MaxEventBytes)
		if readErr != nil {
			result.Projection, result.TailCode = projection, "event_read"
			return result, incompleteAttemptLedger("event_read", readErr)
		}
		event, decodeErr := lifecycle.DecodeEvent(data)
		if decodeErr != nil {
			result.Projection, result.TailCode = projection, "event_decode"
			return result, incompleteAttemptLedger("event_decode", decodeErr)
		}
		projection, decodeErr = lifecycle.Apply(plan, projection, event)
		if decodeErr != nil {
			result.Projection, result.TailCode = projection, "event_chain"
			return result, incompleteAttemptLedger("event_chain", decodeErr)
		}
		result.Events = append(result.Events, event)
	}
	if err := attemptLedgerContextError(ctx); err != nil {
		result.Projection = projection
		return result, err
	}
	result.Projection = projection
	result.Complete = true
	return result, nil
}

func (store *AttemptLedgerStore) attemptRoot(ordinal uint32) string {
	return filepath.Join(store.root, attemptLedgerAttempts, attemptLedgerOrdinalName(ordinal))
}

func readAttemptLedgerHeaderContext(ctx context.Context, root string) (lifecycle.LedgerHeader, error) {
	data, err := readAttemptLedgerFileContext(ctx, root, filepath.Join(root, attemptLedgerHeaderName), lifecycle.MaxHeaderBytes)
	if err != nil {
		return lifecycle.LedgerHeader{}, attemptLedgerError("header_read", err)
	}
	header, err := lifecycle.DecodeHeader(data)
	if err != nil {
		return lifecycle.LedgerHeader{}, attemptLedgerError("header_decode", err)
	}
	return header, nil
}

func readAttemptLedgerFileContext(ctx context.Context, root, path string, maximum int) ([]byte, error) {
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	info, err := hardenedStatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, attemptLedgerError("file_mode", err)
	}
	data, err := hardenedReadFileWithinLimitContext(ctx, root, path, int64(maximum))
	if err != nil {
		return nil, err
	}
	if err := attemptLedgerContextError(ctx); err != nil {
		return nil, err
	}
	return data, nil
}

func attemptLedgerContextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func validateAttemptLedgerTopLevelContext(ctx context.Context, root string) error {
	entries, err := hardenedReadDirWithinLimitContext(ctx, root, root, 4)
	if err != nil {
		return attemptLedgerError("top_level", err)
	}
	entries, err = committedAttemptLedgerEntries(entries, 3)
	if err != nil {
		return attemptLedgerError("top_level_shape", err)
	}
	want := []string{attemptLedgerAttempts, attemptLedgerHeaderName}
	for _, entry := range entries {
		if entry.Name() == attemptLedgerLockName {
			info, infoErr := entry.Info()
			if infoErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !privateWorkspaceFileMode(info.Mode()) {
				return attemptLedgerError("lock_mode", infoErr)
			}
			continue
		}
		if len(want) == 0 || entry.Name() != want[0] || entry.Type()&os.ModeSymlink != 0 {
			return attemptLedgerError("top_level_shape")
		}
		if entry.Name() == attemptLedgerAttempts {
			info, infoErr := entry.Info()
			if infoErr != nil || !entry.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
				return attemptLedgerError("attempts_mode", infoErr)
			}
		}
		want = want[1:]
	}
	if len(want) != 0 {
		return attemptLedgerError("top_level_missing")
	}
	return nil
}

func ensureAttemptLedgerAttemptsDirectory(root string) error {
	path := filepath.Join(root, attemptLedgerAttempts)
	info, err := hardenedStatWithin(root, path)
	if os.IsNotExist(err) {
		return hardenedMkdirAllWithin(root, path, 0o700)
	}
	if err != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
		return attemptLedgerError("attempts_mode", err)
	}
	return nil
}

func attemptLedgerOnlyUncommittedCreateState(root string) bool {
	entries, err := hardenedReadDirWithin(root, root)
	if err != nil {
		return false
	}
	entries, err = committedAttemptLedgerEntries(entries, 2)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		switch entry.Name() {
		case attemptLedgerLockName:
			info, infoErr := entry.Info()
			if infoErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !privateWorkspaceFileMode(info.Mode()) {
				return false
			}
		case attemptLedgerAttempts:
			info, infoErr := entry.Info()
			if infoErr != nil || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !privateWorkspaceDirectoryMode(info.Mode()) {
				return false
			}
			attempts, readErr := hardenedReadDirWithin(root, filepath.Join(root, attemptLedgerAttempts))
			if readErr != nil {
				return false
			}
			attempts, readErr = committedAttemptLedgerEntries(attempts, 0)
			if readErr != nil || len(attempts) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (store *AttemptLedgerStore) prepareAttemptDirectoryLocked(ordinal uint32) error {
	attemptRoot := store.attemptRoot(ordinal)
	info, err := hardenedStatWithin(store.root, attemptRoot)
	if os.IsNotExist(err) {
		if err := mkdirPrivateWithin(filepath.Join(store.root, attemptLedgerAttempts), attemptRoot); err != nil {
			return err
		}
	} else if err != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) || !store.uncommittedAttemptDirectoryLocked(ordinal) {
		return attemptLedgerError("attempt_directory_shape", err)
	}
	eventsPath := filepath.Join(attemptRoot, attemptLedgerEvents)
	eventsInfo, eventsErr := hardenedStatWithin(store.root, eventsPath)
	if os.IsNotExist(eventsErr) {
		return hardenedMkdirAllWithin(store.root, eventsPath, 0o700)
	}
	if eventsErr != nil || !eventsInfo.IsDir() || !privateWorkspaceDirectoryMode(eventsInfo.Mode()) {
		return attemptLedgerError("events_directory", eventsErr)
	}
	return nil
}

func (store *AttemptLedgerStore) uncommittedAttemptDirectoryLocked(ordinal uint32) bool {
	return store.uncommittedAttemptDirectoryLockedContext(context.Background(), ordinal)
}

func (store *AttemptLedgerStore) uncommittedAttemptDirectoryLockedContext(ctx context.Context, ordinal uint32) bool {
	if attemptLedgerContextError(ctx) != nil {
		return false
	}
	attemptRoot := store.attemptRoot(ordinal)
	info, err := hardenedStatWithin(store.root, attemptRoot)
	if err != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
		return false
	}
	entries, err := hardenedReadDirWithinLimitContext(ctx, store.root, attemptRoot, 2)
	if err != nil {
		return false
	}
	entries, err = committedAttemptLedgerEntries(entries, 1)
	if err != nil || len(entries) > 1 {
		return false
	}
	if len(entries) == 0 {
		return true
	}
	if entries[0].Name() != attemptLedgerEvents || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		return false
	}
	eventsInfo, err := entries[0].Info()
	if err != nil || !privateWorkspaceDirectoryMode(eventsInfo.Mode()) {
		return false
	}
	events, err := hardenedReadDirWithinLimitContext(ctx, store.root, filepath.Join(attemptRoot, attemptLedgerEvents), 1)
	if err != nil {
		return false
	}
	events, err = committedAttemptLedgerEntries(events, 0)
	return err == nil && len(events) == 0
}

func committedAttemptLedgerEntries(entries []os.DirEntry, maximum int) ([]os.DirEntry, error) {
	if len(entries) > maximum+1 {
		return nil, ErrAttemptLedgerIncomplete
	}
	committed := make([]os.DirEntry, 0, len(entries))
	temporary := 0
	for _, entry := range entries {
		if !attemptLedgerTemporaryName(entry.Name()) {
			committed = append(committed, entry)
			continue
		}
		temporary++
		info, err := entry.Info()
		if temporary > 1 || err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !privateWorkspaceFileMode(info.Mode()) {
			return nil, ErrAttemptLedgerIncomplete
		}
	}
	if len(committed) > maximum {
		return nil, ErrAttemptLedgerIncomplete
	}
	return committed, nil
}

func attemptLedgerTemporaryName(name string) bool {
	if len(name) != len(".tmp-")+16 || !strings.HasPrefix(name, ".tmp-") {
		return false
	}
	for _, character := range name[len(".tmp-"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func syncAttemptLedgerDirectory(root, path string) error {
	if err := hardenedSyncDirectoryWithin(root, path); err != nil {
		return attemptLedgerError("directory_sync", ErrAttemptLedgerUnsupported, err)
	}
	return nil
}

func attemptLedgerOrdinalName(ordinal uint32) string { return fmt.Sprintf("%010d", ordinal) }
func attemptLedgerEventName(sequence uint32) string  { return fmt.Sprintf("%010d.json", sequence) }

func incompleteAttemptLedger(code string, causes ...error) error {
	return attemptLedgerError(code, append([]error{ErrAttemptLedgerIncomplete}, causes...)...)
}

func attemptLedgerError(code string, causes ...error) error {
	return codedError(ErrAttemptLedger, code, causes...)
}
