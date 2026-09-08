package brokercontract

import "github.com/isukharev/atl/internal/domain"

// ValidatePhaseTransitionV1 defines the only admitted authorization order.
// It performs no I/O and grants no phase by itself.
func ValidatePhaseTransitionV1(from, to domain.BrokerAuthorizationPhase, proposalRequired bool) error {
	allowed := false
	switch from {
	case domain.BrokerPhaseStrictDecode:
		allowed = to == domain.BrokerPhaseAdmission
	case domain.BrokerPhaseAdmission:
		allowed = to == domain.BrokerPhaseQualificationAuthorization
	case domain.BrokerPhaseQualificationAuthorization:
		allowed = to == domain.BrokerPhaseQualification
	case domain.BrokerPhaseQualification:
		allowed = to == domain.BrokerPhaseFinalAuthorization
	case domain.BrokerPhaseFinalAuthorization:
		if proposalRequired {
			allowed = to == domain.BrokerPhaseProposalClearance
		} else {
			allowed = to == domain.BrokerPhaseBusinessOperation
		}
	case domain.BrokerPhaseProposalClearance:
		allowed = proposalRequired && to == domain.BrokerPhaseBusinessOperation
	case domain.BrokerPhaseBusinessOperation:
		allowed = to == domain.BrokerPhaseOutcomeObservation
	}
	if !allowed {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}
