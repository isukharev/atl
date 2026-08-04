package agenteval

import (
	"bytes"
	"encoding/json"
	"io"
)

// cliErrorRecovery is the evaluator-owned decoder for the documented recovery
// schema v1 emitted in the atl CLI's JSON error envelope. It deliberately
// models wire data only: product error construction and classification remain
// owned by atl.
type cliErrorRecovery struct {
	SchemaVersion   int                    `json:"schema_version"`
	Action          cliErrorRecoveryAction `json:"action"`
	RetrySafe       *bool                  `json:"retry_safe"`
	NextCapability  cliErrorNextCapability `json:"next_capability,omitempty"`
	Requested       *int                   `json:"requested,omitempty"`
	Available       *int                   `json:"available,omitempty"`
	Matches         *int                   `json:"matches,omitempty"`
	ExpectedVersion *int                   `json:"expected_version,omitempty"`
	ObservedVersion *int                   `json:"observed_version,omitempty"`
	ExpectedForest  *cliErrorForestVersion `json:"expected_forest,omitempty"`
	ObservedForest  *cliErrorForestVersion `json:"observed_forest,omitempty"`
}

type cliErrorRecoveryAction string

const (
	cliErrorRecoveryAdjustRequest         cliErrorRecoveryAction = "adjust_request"
	cliErrorRecoveryInspectFailure        cliErrorRecoveryAction = "inspect_failure"
	cliErrorRecoveryReauthenticate        cliErrorRecoveryAction = "reauthenticate"
	cliErrorRecoveryRequestAccess         cliErrorRecoveryAction = "request_access"
	cliErrorRecoveryCompleteConfiguration cliErrorRecoveryAction = "complete_configuration"
	cliErrorRecoveryRereadThenReapply     cliErrorRecoveryAction = "reread_then_reapply"
	cliErrorRecoveryRereadThenReselect    cliErrorRecoveryAction = "reread_then_reselect"
	cliErrorRecoveryWaitThenRetry         cliErrorRecoveryAction = "wait_then_retry"
	cliErrorRecoveryRestoreTransport      cliErrorRecoveryAction = "restore_transport_then_retry"
	cliErrorRecoveryRequestHumanApproval  cliErrorRecoveryAction = "request_human_approval"
	cliErrorRecoveryReconcileWriteOutcome cliErrorRecoveryAction = "reconcile_write_outcome"
)

type cliErrorNextCapability string

const (
	cliErrorCapabilityConfluencePageOutline  cliErrorNextCapability = "confluence.page.outline"
	cliErrorCapabilityConfluenceTableSummary cliErrorNextCapability = "confluence.table.summary"
	cliErrorCapabilityConfluencePageMeta     cliErrorNextCapability = "confluence.page.meta"
	cliErrorCapabilityJiraStructureView      cliErrorNextCapability = "jira.structure.view"
)

type cliErrorForestVersion struct {
	Signature int64 `json:"signature"`
	Version   int64 `json:"version"`
}

func validCLIErrorRecoveryJSON(raw []byte) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var recovery cliErrorRecovery
	return decoder.Decode(&recovery) == nil && decoder.Decode(new(any)) == io.EOF && validCLIErrorRecovery(recovery)
}

func validCLIErrorRecovery(recovery cliErrorRecovery) bool {
	if recovery.SchemaVersion != 1 || recovery.RetrySafe == nil || !validCLIErrorRecoveryAction(recovery.Action) {
		return false
	}
	if *recovery.RetrySafe && recovery.Action != cliErrorRecoveryWaitThenRetry && recovery.Action != cliErrorRecoveryRestoreTransport {
		return false
	}
	if recovery.Action != cliErrorRecoveryRereadThenReselect {
		return recovery.NextCapability == "" && !hasCLIErrorRecoveryFacts(recovery)
	}

	if recovery.NextCapability == "" {
		return false
	}
	if recovery.ExpectedVersion != nil || recovery.ObservedVersion != nil {
		if recovery.Requested != nil || recovery.Available != nil || recovery.Matches != nil || recovery.ExpectedForest != nil || recovery.ObservedForest != nil {
			return false
		}
		if recovery.ExpectedVersion == nil || recovery.ObservedVersion == nil || *recovery.ExpectedVersion <= 0 || *recovery.ObservedVersion <= 0 || *recovery.ExpectedVersion == *recovery.ObservedVersion {
			return false
		}
		return recovery.NextCapability == cliErrorCapabilityConfluencePageOutline ||
			recovery.NextCapability == cliErrorCapabilityConfluenceTableSummary ||
			recovery.NextCapability == cliErrorCapabilityConfluencePageMeta
	}
	if recovery.ExpectedForest != nil || recovery.ObservedForest != nil {
		if recovery.Requested != nil || recovery.Available != nil || recovery.Matches != nil || recovery.ExpectedForest == nil || recovery.ObservedForest == nil {
			return false
		}
		expected, observed := recovery.ExpectedForest, recovery.ObservedForest
		return recovery.NextCapability == cliErrorCapabilityJiraStructureView &&
			expected.Signature != 0 && observed.Signature != 0 && expected.Version > 0 && observed.Version > 0 && *expected != *observed
	}

	switch recovery.NextCapability {
	case cliErrorCapabilityConfluencePageOutline:
		if recovery.Available == nil || *recovery.Available <= 0 {
			return false
		}
		outOfRange := recovery.Requested != nil && *recovery.Requested > *recovery.Available && recovery.Matches == nil
		ambiguous := recovery.Requested == nil && recovery.Matches != nil && *recovery.Matches >= 2 && *recovery.Matches == *recovery.Available
		return outOfRange || ambiguous
	case cliErrorCapabilityConfluenceTableSummary:
		return recovery.Requested != nil && recovery.Available != nil && recovery.Matches == nil &&
			*recovery.Requested > 0 && *recovery.Available >= 0 && *recovery.Requested > *recovery.Available
	case cliErrorCapabilityJiraStructureView:
		return recovery.Requested == nil && recovery.Available != nil && *recovery.Available >= 0 &&
			(recovery.Matches == nil || *recovery.Matches >= 2 && *recovery.Matches <= *recovery.Available)
	default:
		return false
	}
}

func validCLIErrorRecoveryAction(action cliErrorRecoveryAction) bool {
	switch action {
	case cliErrorRecoveryAdjustRequest, cliErrorRecoveryInspectFailure, cliErrorRecoveryReauthenticate,
		cliErrorRecoveryRequestAccess, cliErrorRecoveryCompleteConfiguration, cliErrorRecoveryRereadThenReapply,
		cliErrorRecoveryRereadThenReselect, cliErrorRecoveryWaitThenRetry, cliErrorRecoveryRestoreTransport,
		cliErrorRecoveryRequestHumanApproval, cliErrorRecoveryReconcileWriteOutcome:
		return true
	default:
		return false
	}
}

func hasCLIErrorRecoveryFacts(recovery cliErrorRecovery) bool {
	return recovery.Requested != nil || recovery.Available != nil || recovery.Matches != nil ||
		recovery.ExpectedVersion != nil || recovery.ObservedVersion != nil ||
		recovery.ExpectedForest != nil || recovery.ObservedForest != nil
}
