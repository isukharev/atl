package diagnostic

import (
	"errors"
	"net/http"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type recoveryFactsError struct {
	cause                 error
	expected, observed    int
	requested, available  int
	matches               int
	expectedSignature     int64
	expectedForestVersion int64
	observedSignature     int64
	observedForestVersion int64
	ambiguous             bool
	structureReason       string
}

func (e *recoveryFactsError) Error() string { return "private prose" }
func (e *recoveryFactsError) Unwrap() error { return e.cause }
func (e *recoveryFactsError) DiagnosticVersionMismatch() (int, int) {
	return e.expected, e.observed
}
func (e *recoveryFactsError) DiagnosticSelection() (int, int, int) {
	return e.requested, e.available, e.matches
}
func (e *recoveryFactsError) DiagnosticStructureSelection() (string, int, int) {
	return e.structureReason, e.matches, e.available
}
func (e *recoveryFactsError) DiagnosticForestVersionMismatch() (int64, int64, int64, int64) {
	return e.expectedSignature, e.expectedForestVersion, e.observedSignature, e.observedForestVersion
}
func (e *recoveryFactsError) DiagnosticAmbiguousWrite() bool { return e.ambiguous }

type selectionFactsError struct {
	cause                error
	requested, available int
	matches              int
}

func (e *selectionFactsError) Error() string { return "private selection prose" }
func (e *selectionFactsError) Unwrap() error { return e.cause }
func (e *selectionFactsError) DiagnosticSelection() (int, int, int) {
	return e.requested, e.available, e.matches
}

func TestRecoverExactReplaySafetyIsOperationBound(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		operation OperationContext
		action    RecoveryAction
		retrySafe bool
	}{
		{"read rate limit", &httpx.APIError{Status: http.StatusTooManyRequests}, OperationRead, RecoveryWaitThenRetry, true},
		{"unknown rate limit", &httpx.APIError{Status: http.StatusTooManyRequests}, OperationUnknown, RecoveryWaitThenRetry, false},
		{"write rate limit", &httpx.APIError{Status: http.StatusTooManyRequests}, OperationWrite, RecoveryReconcileWriteOutcome, false},
		{"read transport", &httpx.TransportError{Method: http.MethodGet, Category: "timeout"}, OperationRead, RecoveryRestoreTransport, true},
		{"write transport", &httpx.TransportError{Method: http.MethodPost, Category: "timeout"}, OperationWrite, RecoveryReconcileWriteOutcome, false},
		{"ambiguous typed write", &recoveryFactsError{cause: errors.New("secret"), ambiguous: true}, OperationRead, RecoveryReconcileWriteOutcome, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Recover(test.err, test.operation)
			if got.Action != test.action || got.RetrySafe != test.retrySafe || !ValidateRecovery(got) {
				t.Fatalf("recovery=%+v, want action=%q retry_safe=%v", got, test.action, test.retrySafe)
			}
		})
	}
}

func TestRecoverTypedFactsAndPrecedence(t *testing.T) {
	pageAndSelection := &recoveryFactsError{
		cause: domain.ErrCheckFailed, expected: 7, observed: 8,
		requested: 0, available: 3, matches: 3,
	}
	section := Recover(pageAndSelection, OperationConfluenceSectionRead)
	if section.NextCapability != CapabilityConfluencePageOutline || section.ExpectedVersion == nil || section.ObservedVersion == nil || section.Available != nil || !ValidateRecovery(section) {
		t.Fatalf("section recovery=%+v", section)
	}

	selectorAndForest := &recoveryFactsError{
		cause: domain.ErrCheckFailed, available: 4, matches: 2,
		structureReason:   "ambiguous",
		expectedSignature: 11, expectedForestVersion: 7,
		observedSignature: 12, observedForestVersion: 8,
	}
	structure := Recover(selectorAndForest, OperationJiraStructureRead)
	if structure.NextCapability != CapabilityJiraStructureView || structure.Matches == nil || structure.ExpectedForest != nil || !ValidateRecovery(structure) {
		t.Fatalf("structure recovery=%+v", structure)
	}

	table := Recover(&selectionFactsError{cause: domain.ErrNotFound, requested: 4, available: 2}, OperationConfluenceTableRead)
	if table.Requested == nil || table.Available == nil || table.NextCapability != CapabilityConfluenceTableSummary || !ValidateRecovery(table) {
		t.Fatalf("table recovery=%+v", table)
	}
}

func TestRecoverMalformedTypedMetadataFallsBackWithoutFacts(t *testing.T) {
	malformed := &recoveryFactsError{cause: domain.ErrCheckFailed, expected: -7, observed: 0, requested: -1, available: -2}
	got := Recover(malformed, OperationConfluenceSectionRead)
	if got.Action != RecoveryInspectFailure || got.NextCapability != "" || hasRecoveryFacts(got) || !ValidateRecovery(got) {
		t.Fatalf("recovery=%+v", got)
	}
}

func TestRecoverRejectsMalformedSelectionMetadata(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		operation OperationContext
	}{
		{"single section match is not ambiguous", &selectionFactsError{cause: domain.ErrCheckFailed, available: 1, matches: 1}, OperationConfluenceSectionRead},
		{"unknown Structure reason", &recoveryFactsError{cause: domain.ErrCheckFailed, structureReason: "private_reason", available: 4}, OperationJiraStructureRead},
		{"single Structure match is not ambiguous", &recoveryFactsError{cause: domain.ErrCheckFailed, structureReason: "ambiguous", matches: 1, available: 4}, OperationJiraStructureRead},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Recover(test.err, test.operation)
			if got.Action != RecoveryInspectFailure || got.NextCapability != "" || hasRecoveryFacts(got) || !ValidateRecovery(got) {
				t.Fatalf("recovery=%+v", got)
			}
		})
	}
}

func TestValidateRecoveryRejectsOpenOrIncoherentShapes(t *testing.T) {
	bad := []Recovery{
		{SchemaVersion: 2, Action: RecoveryInspectFailure},
		{SchemaVersion: 1, Action: "invented"},
		{SchemaVersion: 1, Action: RecoveryAdjustRequest, RetrySafe: true},
		{SchemaVersion: 1, Action: RecoveryRereadThenReselect, NextCapability: "private.capability"},
		{SchemaVersion: 1, Action: RecoveryRereadThenReselect, NextCapability: CapabilityConfluencePageMeta, Available: intPointer(3)},
		{SchemaVersion: 1, Action: RecoveryRereadThenReselect, NextCapability: CapabilityJiraStructureView, ExpectedForest: &RecoveryForestVersion{Signature: 1, Version: 1}},
	}
	for index, recovery := range bad {
		if ValidateRecovery(recovery) {
			t.Fatalf("bad[%d] accepted: %+v", index, recovery)
		}
	}
}
