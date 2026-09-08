package brokercontract

import (
	"errors"

	"github.com/isukharev/atl/internal/domain"
)

type Error struct {
	reason domain.BrokerReason
}

func (e *Error) Error() string { return "broker contract rejected" }
func (e *Error) Reason() domain.BrokerReason {
	if e == nil {
		return ""
	}
	return e.reason
}
func (e *Error) DiagnosticBrokerReason() domain.BrokerReason { return e.Reason() }
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.reason {
	case domain.BrokerReasonMalformed, domain.BrokerReasonUnsupported:
		return domain.ErrUsage
	case domain.BrokerReasonDenied, domain.BrokerReasonRevoked, domain.BrokerReasonGrantExpired:
		return domain.ErrForbidden
	case domain.BrokerReasonCredentialExpired:
		return domain.ErrAuth
	default:
		return domain.ErrCheckFailed
	}
}
func (e *Error) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.reason == domain.BrokerReasonOutcomeUnknown
}
func (e *Error) DiagnosticTerminalCheckFailure() bool {
	return e != nil && errors.Is(e.Unwrap(), domain.ErrCheckFailed)
}
func (e *Error) DiagnosticPolicyDenial() bool {
	return e != nil && e.reason == domain.BrokerReasonProposalClearanceRequired
}

func reject(reason domain.BrokerReason) error { return &Error{reason: reason} }

func Reason(err error) (domain.BrokerReason, bool) {
	var contractErr *Error
	if !errors.As(err, &contractErr) || contractErr == nil {
		return "", false
	}
	return contractErr.reason, true
}

// ContentFreeError extracts only the closed Broker rejection classification.
// Wrapping policy or transport errors may carry private text, so callers must
// not preserve the original chain merely because it contains a Broker Error.
func ContentFreeError(err error) (bool, error) {
	reason, ok := Reason(err)
	if !ok {
		return false, nil
	}
	return true, reject(reason)
}

// ErrorForReason returns a content-free error for one closed Broker reason.
// Transport adapters use it to preserve semantic recovery without retaining
// private error chains.
func ErrorForReason(reason domain.BrokerReason) (bool, error) {
	switch reason {
	case domain.BrokerReasonMalformed, domain.BrokerReasonUnsupported,
		domain.BrokerReasonDenied, domain.BrokerReasonRevoked,
		domain.BrokerReasonCredentialExpired, domain.BrokerReasonGrantExpired,
		domain.BrokerReasonStaleExecution, domain.BrokerReasonStaleAuthority,
		domain.BrokerReasonDecisionExpired, domain.BrokerReasonAuthorizationUnavailable,
		domain.BrokerReasonUnsupportedConsistency, domain.BrokerReasonProposalClearanceRequired,
		domain.BrokerReasonOutcomeUnknown:
		return true, reject(reason)
	default:
		return false, nil
	}
}
