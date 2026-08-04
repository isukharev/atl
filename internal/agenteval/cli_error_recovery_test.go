package agenteval

import "testing"

func TestCLIErrorRecoveryV1AcceptsOnlyDocumentedShapes(t *testing.T) {
	boolean := func(value bool) *bool { return &value }
	integer := func(value int) *int { return &value }
	forest := func(signature, version int64) *cliErrorForestVersion {
		return &cliErrorForestVersion{Signature: signature, Version: version}
	}
	valid := map[string]cliErrorRecovery{
		"adjust request":         {SchemaVersion: 1, Action: cliErrorRecoveryAdjustRequest},
		"inspect failure":        {SchemaVersion: 1, Action: cliErrorRecoveryInspectFailure},
		"reauthenticate":         {SchemaVersion: 1, Action: cliErrorRecoveryReauthenticate},
		"request access":         {SchemaVersion: 1, Action: cliErrorRecoveryRequestAccess},
		"complete configuration": {SchemaVersion: 1, Action: cliErrorRecoveryCompleteConfiguration},
		"reread then reapply":    {SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReapply},
		"wait without retry":     {SchemaVersion: 1, Action: cliErrorRecoveryWaitThenRetry},
		"wait with retry":        {SchemaVersion: 1, Action: cliErrorRecoveryWaitThenRetry, RetrySafe: boolean(true)},
		"restore with retry":     {SchemaVersion: 1, Action: cliErrorRecoveryRestoreTransport, RetrySafe: boolean(true)},
		"request approval":       {SchemaVersion: 1, Action: cliErrorRecoveryRequestHumanApproval},
		"reconcile write":        {SchemaVersion: 1, Action: cliErrorRecoveryReconcileWriteOutcome},
		"page version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluencePageOutline,
			ExpectedVersion: integer(2), ObservedVersion: integer(3),
		},
		"table version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluenceTableSummary,
			ExpectedVersion: integer(2), ObservedVersion: integer(3),
		},
		"page metadata version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluencePageMeta,
			ExpectedVersion: integer(2), ObservedVersion: integer(3),
		},
		"forest version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView,
			ExpectedForest: forest(11, 2), ObservedForest: forest(12, 3),
		},
		"outline out of range": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageOutline,
			Requested:      integer(4), Available: integer(3),
		},
		"outline ambiguous": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageOutline,
			Available:      integer(3), Matches: integer(3),
		},
		"table out of range": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluenceTableSummary,
			Requested:      integer(2), Available: integer(1),
		},
		"structure absent": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView, Available: integer(0),
		},
		"structure ambiguous": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView,
			Available:      integer(3), Matches: integer(2),
		},
	}
	for name, recovery := range valid {
		t.Run(name, func(t *testing.T) {
			if recovery.RetrySafe == nil {
				recovery.RetrySafe = boolean(false)
			}
			if !validCLIErrorRecovery(recovery) {
				t.Fatalf("valid recovery rejected: %+v", recovery)
			}
		})
	}

	invalid := map[string]cliErrorRecovery{
		"missing retry safety":      {SchemaVersion: 1, Action: cliErrorRecoveryAdjustRequest},
		"wrong schema":              {SchemaVersion: 2, Action: cliErrorRecoveryInspectFailure},
		"unknown action":            {SchemaVersion: 1, Action: "retry"},
		"unsafe retry":              {SchemaVersion: 1, Action: cliErrorRecoveryAdjustRequest, RetrySafe: boolean(true)},
		"facts on simple action":    {SchemaVersion: 1, Action: cliErrorRecoveryInspectFailure, Available: integer(1)},
		"capability on simple":      {SchemaVersion: 1, Action: cliErrorRecoveryInspectFailure, NextCapability: cliErrorCapabilityConfluencePageMeta},
		"reselection missing route": {SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect},
		"partial version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageMeta, ExpectedVersion: integer(1),
		},
		"equal version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluencePageMeta,
			ExpectedVersion: integer(1), ObservedVersion: integer(1),
		},
		"nonpositive version": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluencePageMeta,
			ExpectedVersion: integer(0), ObservedVersion: integer(1),
		},
		"version with selection facts": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityConfluencePageMeta,
			ExpectedVersion: integer(1), ObservedVersion: integer(2), Available: integer(1),
		},
		"version on structure": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability:  cliErrorCapabilityJiraStructureView,
			ExpectedVersion: integer(1), ObservedVersion: integer(2),
		},
		"partial forest": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView, ExpectedForest: forest(1, 1),
		},
		"equal forest": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView,
			ExpectedForest: forest(1, 1), ObservedForest: forest(1, 1),
		},
		"forest on page": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageMeta,
			ExpectedForest: forest(1, 1), ObservedForest: forest(2, 2),
		},
		"outline in range": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageOutline,
			Requested:      integer(2), Available: integer(3),
		},
		"outline one match": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluencePageOutline,
			Available:      integer(1), Matches: integer(1),
		},
		"table with matches": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityConfluenceTableSummary,
			Requested:      integer(2), Available: integer(1), Matches: integer(1),
		},
		"structure requested": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView,
			Requested:      integer(1), Available: integer(1),
		},
		"structure one match": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: cliErrorCapabilityJiraStructureView,
			Available:      integer(1), Matches: integer(1),
		},
		"unknown capability": {
			SchemaVersion: 1, Action: cliErrorRecoveryRereadThenReselect,
			NextCapability: "private.route", Available: integer(1),
		},
	}
	for name, recovery := range invalid {
		t.Run(name, func(t *testing.T) {
			if name != "missing retry safety" && recovery.RetrySafe == nil {
				recovery.RetrySafe = boolean(false)
			}
			if validCLIErrorRecovery(recovery) {
				t.Fatalf("invalid recovery accepted: %+v", recovery)
			}
		})
	}
}
