package agenteval

import (
	"errors"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

type AttemptLedgerAttemptReport struct {
	AttemptID            string `json:"attempt_id"`
	Ordinal              uint32 `json:"ordinal"`
	State                string `json:"state"`
	Terminal             bool   `json:"terminal"`
	Sequence             uint32 `json:"sequence"`
	Complete             bool   `json:"complete"`
	TailCode             string `json:"tail_code,omitempty"`
	PlanSHA256           string `json:"plan_sha256"`
	LastEventSHA256      string `json:"last_event_sha256,omitempty"`
	PredecessorAttemptID string `json:"predecessor_attempt_id,omitempty"`
}

type AttemptLedgerReport struct {
	SchemaVersion int                          `json:"schema_version"`
	LedgerID      string                       `json:"ledger_id"`
	Complete      bool                         `json:"complete"`
	Attempts      []AttemptLedgerAttemptReport `json:"attempts"`
}

type AttemptLedgerReconciliationReport struct {
	SchemaVersion        int    `json:"schema_version"`
	LedgerID             string `json:"ledger_id"`
	PredecessorAttemptID string `json:"predecessor_attempt_id"`
	AttemptID            string `json:"attempt_id"`
	PlanSHA256           string `json:"plan_sha256"`
	State                string `json:"state"`
}

func InspectAttemptLedger(root string) (AttemptLedgerReport, error) {
	store, err := OpenAttemptLedgerStore(root)
	if err != nil {
		return AttemptLedgerReport{}, err
	}
	inspections, inspectErr := store.InspectAll()
	if inspectErr != nil && !errors.Is(inspectErr, ErrAttemptLedgerIncomplete) {
		return AttemptLedgerReport{}, inspectErr
	}
	report := AttemptLedgerReport{SchemaVersion: 1, LedgerID: store.header.LedgerID, Complete: inspectErr == nil,
		Attempts: make([]AttemptLedgerAttemptReport, 0, len(inspections))}
	for _, inspection := range inspections {
		state := inspection.Projection.State
		terminal := inspection.Projection.Terminal
		if !inspection.Complete {
			// A corrupt or torn durable tail is itself evidence that a safe
			// terminal/non-execution classification is unavailable. The stored
			// verified prefix remains untouched, while the inspection projection
			// is absorbing unknown so no caller can mistake the prefix state for
			// resumable work.
			state = lifecycle.StateUnknown
			terminal = true
		}
		report.Attempts = append(report.Attempts, AttemptLedgerAttemptReport{
			AttemptID: inspection.Plan.AttemptID, Ordinal: inspection.Plan.Ordinal, State: string(state),
			Terminal: terminal, Sequence: inspection.Projection.Sequence,
			Complete: inspection.Complete, TailCode: inspection.TailCode, PlanSHA256: inspection.Plan.PlanSHA256,
			LastEventSHA256: inspection.Projection.LastSHA256, PredecessorAttemptID: inspection.Plan.PredecessorAttemptID,
		})
	}
	return report, nil
}

func ReconcileAttemptLedger(root, predecessorAttemptID, evidenceSHA256 string) (AttemptLedgerReconciliationReport, error) {
	if !validSHA256(predecessorAttemptID) || !validSHA256(evidenceSHA256) {
		return AttemptLedgerReconciliationReport{}, attemptLedgerError("reconciliation_identity")
	}
	store, err := OpenAttemptLedgerStore(root)
	if err != nil {
		return AttemptLedgerReconciliationReport{}, err
	}
	predecessor, err := store.Inspect(predecessorAttemptID)
	if err != nil || predecessor.Projection.State != lifecycle.StateUnknown {
		return AttemptLedgerReconciliationReport{}, attemptLedgerError("reconciliation_predecessor", err)
	}
	plan, err := store.AllocateReconciled(predecessor.Plan.Binding, predecessorAttemptID, evidenceSHA256)
	if err != nil {
		return AttemptLedgerReconciliationReport{}, err
	}
	return AttemptLedgerReconciliationReport{SchemaVersion: 1, LedgerID: plan.LedgerID,
		PredecessorAttemptID: predecessorAttemptID, AttemptID: plan.AttemptID, PlanSHA256: plan.PlanSHA256,
		State: string(lifecycle.StatePlanned)}, nil
}
