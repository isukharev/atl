package diagnostic_test

import (
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerDiscoveryRecoveryIsClosedAndNeverAutomaticallyReplays(t *testing.T) {
	for _, test := range []struct {
		reason domain.BrokerReason
		action diagnostic.RecoveryAction
	}{
		{domain.BrokerReasonDenied, diagnostic.RecoveryRequestAccess},
		{domain.BrokerReasonRevoked, diagnostic.RecoveryRequestAccess},
		{domain.BrokerReasonGrantExpired, diagnostic.RecoveryRequestAccess},
		{domain.BrokerReasonCredentialExpired, diagnostic.RecoveryReauthenticate},
		{domain.BrokerReasonStaleExecution, diagnostic.RecoveryReauthenticate},
		{domain.BrokerReasonStaleAuthority, diagnostic.RecoveryReauthenticate},
		{domain.BrokerReasonDecisionExpired, diagnostic.RecoveryRereadThenReselect},
		{domain.BrokerReasonUnsupported, diagnostic.RecoveryAdjustRequest},
		{domain.BrokerReasonUnsupportedConsistency, diagnostic.RecoveryAdjustRequest},
		{domain.BrokerReasonAuthorizationUnavailable, diagnostic.RecoveryInspectFailure},
		{domain.BrokerReasonProposalClearanceRequired, diagnostic.RecoveryRequestHumanApproval},
		{domain.BrokerReasonOutcomeUnknown, diagnostic.RecoveryReconcileWriteOutcome},
	} {
		_, err := brokercontract.ErrorForReason(test.reason)
		wire, wireErr := brokertransport.EncodeDiscoveryFailureV2(test.reason)
		failure, decodeErr := brokertransport.DecodeDiscoveryFailureV2(wire)
		for _, operation := range []diagnostic.OperationContext{diagnostic.OperationRead, diagnostic.OperationWrite, diagnostic.OperationUnknown} {
			recovery := diagnostic.Recover(err, operation)
			if recovery.Action != test.action || recovery.RetrySafe || wireErr != nil || decodeErr != nil || failure.Recovery != string(recovery.Action) || failure.RetrySafe {
				t.Fatalf("reason=%s operation=%s recovery=%+v wire=%+v", test.reason, operation, recovery, failure)
			}
		}
	}
}
