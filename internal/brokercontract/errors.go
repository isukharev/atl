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
