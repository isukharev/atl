package diagnostic

import (
	"errors"
)

const RecoverySchemaVersion = 1

// OperationContext is the closed semantic context needed to decide whether an
// exact replay is safe and which bounded read can refresh a positional choice.
// It deliberately carries no command path, arguments, identifiers, or backend
// data.
type OperationContext string

const (
	OperationUnknown                  OperationContext = "unknown"
	OperationRead                     OperationContext = "read"
	OperationWrite                    OperationContext = "write"
	OperationConfluenceSectionRead    OperationContext = "confluence_section_read"
	OperationConfluenceTableRead      OperationContext = "confluence_table_read"
	OperationConfluenceAttachmentRead OperationContext = "confluence_attachment_read"
	OperationJiraStructureRead        OperationContext = "jira_structure_read"
)

type RecoveryAction string

const (
	RecoveryAdjustRequest         RecoveryAction = "adjust_request"
	RecoveryInspectFailure        RecoveryAction = "inspect_failure"
	RecoveryReauthenticate        RecoveryAction = "reauthenticate"
	RecoveryRequestAccess         RecoveryAction = "request_access"
	RecoveryCompleteConfiguration RecoveryAction = "complete_configuration"
	RecoveryRereadThenReapply     RecoveryAction = "reread_then_reapply"
	RecoveryRereadThenReselect    RecoveryAction = "reread_then_reselect"
	RecoveryWaitThenRetry         RecoveryAction = "wait_then_retry"
	RecoveryRestoreTransport      RecoveryAction = "restore_transport_then_retry"
	RecoveryRequestHumanApproval  RecoveryAction = "request_human_approval"
	RecoveryReconcileWriteOutcome RecoveryAction = "reconcile_write_outcome"
)

type NextCapability string

const (
	CapabilityConfluencePageOutline  NextCapability = "confluence.page.outline"
	CapabilityConfluenceTableSummary NextCapability = "confluence.table.summary"
	CapabilityConfluencePageMeta     NextCapability = "confluence.page.meta"
	CapabilityJiraStructureView      NextCapability = "jira.structure.view"
)

type RecoveryForestVersion struct {
	Signature int64 `json:"signature"`
	Version   int64 `json:"version"`
}

// Recovery is the privacy-safe, transport-neutral recovery envelope. Optional
// facts are pointers so zero never doubles as an absent or unvalidated value.
type Recovery struct {
	SchemaVersion   int                    `json:"schema_version"`
	Action          RecoveryAction         `json:"action"`
	RetrySafe       bool                   `json:"retry_safe"`
	NextCapability  NextCapability         `json:"next_capability,omitempty"`
	Requested       *int                   `json:"requested,omitempty"`
	Available       *int                   `json:"available,omitempty"`
	Matches         *int                   `json:"matches,omitempty"`
	ExpectedVersion *int                   `json:"expected_version,omitempty"`
	ObservedVersion *int                   `json:"observed_version,omitempty"`
	ExpectedForest  *RecoveryForestVersion `json:"expected_forest,omitempty"`
	ObservedForest  *RecoveryForestVersion `json:"observed_forest,omitempty"`
}

type versionMismatchMetadata interface {
	DiagnosticVersionMismatch() (expected, observed int)
}

type selectionMetadata interface {
	DiagnosticSelection() (requested, available, matches int)
}

type structureSelectionMetadata interface {
	DiagnosticStructureSelection() (reason string, matches, available int)
}

type forestMismatchMetadata interface {
	DiagnosticForestVersionMismatch() (expectedSignature, expectedVersion, observedSignature, observedVersion int64)
}

type ambiguousWriteMetadata interface {
	DiagnosticAmbiguousWrite() bool
}

type readOnlyPolicyMetadata interface {
	DiagnosticReadOnlyPolicy() bool
}

// Recover classifies one failure without parsing its prose. Invalid typed
// metadata is ignored in full and falls back to a fact-free generic recovery.
func Recover(err error, operation OperationContext) Recovery {
	base := Recovery{SchemaVersion: RecoverySchemaVersion, Action: RecoveryInspectFailure}

	var policy readOnlyPolicyMetadata
	if errors.As(err, &policy) && policy.DiagnosticReadOnlyPolicy() {
		base.Action = RecoveryRequestHumanApproval
		return base
	}
	var ambiguous ambiguousWriteMetadata
	if errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite() {
		base.Action = RecoveryReconcileWriteOutcome
		return base
	}

	if recovery, ok := typedRecovery(err, operation); ok {
		return recovery
	}

	kind, _ := Classify(err)
	switch kind {
	case "authentication_failed":
		base.Action = RecoveryReauthenticate
	case "forbidden":
		base.Action = RecoveryRequestAccess
	case "configuration_error":
		base.Action = RecoveryCompleteConfiguration
	case "version_conflict":
		base.Action = RecoveryRereadThenReapply
	case "usage_error", "not_found", "output_limit_exceeded":
		base.Action = RecoveryAdjustRequest
	case "rate_limited":
		if operation == OperationWrite {
			base.Action = RecoveryReconcileWriteOutcome
		} else {
			base.Action = RecoveryWaitThenRetry
			base.RetrySafe = isReadOperation(operation)
		}
	case "transport_error":
		if operation == OperationWrite {
			base.Action = RecoveryReconcileWriteOutcome
		} else {
			base.Action = RecoveryRestoreTransport
			base.RetrySafe = isReadOperation(operation)
		}
	}
	return base
}

func typedRecovery(err error, operation OperationContext) (Recovery, bool) {
	base := Recovery{SchemaVersion: RecoverySchemaVersion, Action: RecoveryRereadThenReselect}

	// Existing transport policy gives page-version mismatch precedence over a
	// section/table selection error, while a Structure selector must be repaired
	// before a forest binding can mean anything. Keep that precedence explicit.
	if operation != OperationJiraStructureRead {
		var version versionMismatchMetadata
		if errors.As(err, &version) {
			expected, observed := version.DiagnosticVersionMismatch()
			capability, ok := pageRefreshCapability(operation)
			if !ok || expected <= 0 || observed <= 0 || expected == observed {
				return Recovery{}, false
			}
			base.NextCapability = capability
			base.ExpectedVersion, base.ObservedVersion = intPointer(expected), intPointer(observed)
			return base, true
		}
	}

	if operation == OperationJiraStructureRead {
		var selection structureSelectionMetadata
		if errors.As(err, &selection) {
			reason, matches, available := selection.DiagnosticStructureSelection()
			valid := available >= 0
			switch reason {
			case "not_found", "labels_incomplete":
				valid = valid && matches == 0
			case "ambiguous":
				valid = valid && matches >= 2 && matches <= available
			default:
				valid = false
			}
			if !valid {
				return Recovery{}, false
			}
			base.NextCapability = CapabilityJiraStructureView
			base.Available = intPointer(available)
			if matches > 0 {
				base.Matches = intPointer(matches)
			}
			return base, true
		}
	}

	var selection selectionMetadata
	if errors.As(err, &selection) {
		requested, available, matches := selection.DiagnosticSelection()
		switch operation {
		case OperationConfluenceSectionRead:
			if available <= 0 || requested < 0 || matches < 0 || (requested == 0 && (available < 2 || matches != available)) || (requested > 0 && (requested <= available || matches != 0)) {
				return Recovery{}, false
			}
			base.NextCapability = CapabilityConfluencePageOutline
			base.Available = intPointer(available)
			if requested == 0 {
				base.Matches = intPointer(matches)
			} else {
				base.Requested = intPointer(requested)
			}
			return base, true
		case OperationConfluenceTableRead:
			if requested <= 0 || available < 0 || requested <= available || matches != 0 {
				return Recovery{}, false
			}
			base.NextCapability = CapabilityConfluenceTableSummary
			base.Requested, base.Available = intPointer(requested), intPointer(available)
			return base, true
		}
	}

	if operation == OperationJiraStructureRead {
		var forest forestMismatchMetadata
		if errors.As(err, &forest) {
			es, ev, os, ov := forest.DiagnosticForestVersionMismatch()
			if es == 0 || os == 0 || ev <= 0 || ov <= 0 || (es == os && ev == ov) {
				return Recovery{}, false
			}
			base.NextCapability = CapabilityJiraStructureView
			base.ExpectedForest = &RecoveryForestVersion{Signature: es, Version: ev}
			base.ObservedForest = &RecoveryForestVersion{Signature: os, Version: ov}
			return base, true
		}
	}
	return Recovery{}, false
}

func pageRefreshCapability(operation OperationContext) (NextCapability, bool) {
	switch operation {
	case OperationConfluenceSectionRead:
		return CapabilityConfluencePageOutline, true
	case OperationConfluenceTableRead:
		return CapabilityConfluenceTableSummary, true
	case OperationConfluenceAttachmentRead:
		return CapabilityConfluencePageMeta, true
	default:
		return "", false
	}
}

func isReadOperation(operation OperationContext) bool {
	switch operation {
	case OperationRead, OperationConfluenceSectionRead, OperationConfluenceTableRead,
		OperationConfluenceAttachmentRead, OperationJiraStructureRead:
		return true
	default:
		return false
	}
}

func intPointer(value int) *int { return &value }

// ValidateRecovery enforces the complete closed v1 wire shape for product-side
// producers and consumers. External consumers keep an independent validator
// bound to the same versioned public wire contract.
func ValidateRecovery(recovery Recovery) bool {
	if recovery.SchemaVersion != RecoverySchemaVersion || !validRecoveryAction(recovery.Action) {
		return false
	}
	if recovery.RetrySafe && recovery.Action != RecoveryWaitThenRetry && recovery.Action != RecoveryRestoreTransport {
		return false
	}
	if recovery.Action != RecoveryRereadThenReselect {
		return recovery.NextCapability == "" && !hasRecoveryFacts(recovery)
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
		return recovery.NextCapability == CapabilityConfluencePageOutline ||
			recovery.NextCapability == CapabilityConfluenceTableSummary ||
			recovery.NextCapability == CapabilityConfluencePageMeta
	}
	if recovery.ExpectedForest != nil || recovery.ObservedForest != nil {
		if recovery.Requested != nil || recovery.Available != nil || recovery.Matches != nil || recovery.ExpectedForest == nil || recovery.ObservedForest == nil {
			return false
		}
		expected, observed := recovery.ExpectedForest, recovery.ObservedForest
		return recovery.NextCapability == CapabilityJiraStructureView &&
			expected.Signature != 0 && observed.Signature != 0 && expected.Version > 0 && observed.Version > 0 && *expected != *observed
	}

	switch recovery.NextCapability {
	case CapabilityConfluencePageOutline:
		if recovery.Available == nil || *recovery.Available <= 0 {
			return false
		}
		outOfRange := recovery.Requested != nil && *recovery.Requested > *recovery.Available && recovery.Matches == nil
		ambiguous := recovery.Requested == nil && recovery.Matches != nil && *recovery.Matches >= 2 && *recovery.Matches == *recovery.Available
		return outOfRange || ambiguous
	case CapabilityConfluenceTableSummary:
		return recovery.Requested != nil && recovery.Available != nil && recovery.Matches == nil &&
			*recovery.Requested > 0 && *recovery.Available >= 0 && *recovery.Requested > *recovery.Available
	case CapabilityJiraStructureView:
		return recovery.Requested == nil && recovery.Available != nil && *recovery.Available >= 0 &&
			(recovery.Matches == nil || *recovery.Matches >= 2 && *recovery.Matches <= *recovery.Available)
	default:
		return false
	}
}

func validRecoveryAction(action RecoveryAction) bool {
	switch action {
	case RecoveryAdjustRequest, RecoveryInspectFailure, RecoveryReauthenticate,
		RecoveryRequestAccess, RecoveryCompleteConfiguration, RecoveryRereadThenReapply,
		RecoveryRereadThenReselect, RecoveryWaitThenRetry, RecoveryRestoreTransport,
		RecoveryRequestHumanApproval, RecoveryReconcileWriteOutcome:
		return true
	default:
		return false
	}
}

func hasRecoveryFacts(recovery Recovery) bool {
	return recovery.Requested != nil || recovery.Available != nil || recovery.Matches != nil ||
		recovery.ExpectedVersion != nil || recovery.ObservedVersion != nil ||
		recovery.ExpectedForest != nil || recovery.ObservedForest != nil
}
