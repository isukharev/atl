// Package diagnostic exposes stable, transport-neutral error classification.
// Human-readable error strings remain useful diagnostics; Kind and Remediation
// are the durable contract consumed by CLI JSON and MCP tool clients.
package diagnostic

import (
	"errors"
	"net/http"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// Classify maps an application error to a stable machine-readable kind and a
// coarse recovery action. Transport layers may add their own policy-specific
// cases before calling Classify.
func Classify(err error) (kind, remediation string) {
	terminal := terminalCheckFailure(err)
	switch {
	case !terminal && errors.Is(err, domain.ErrAuth):
		return "authentication_failed", "reauthenticate"
	case !terminal && errors.Is(err, domain.ErrNotFound):
		return "not_found", "verify_identifier_or_access"
	case !terminal && errors.Is(err, domain.ErrVersionConflict):
		return "version_conflict", "refresh_and_reapply"
	case !terminal && errors.Is(err, domain.ErrForbidden):
		return "forbidden", "request_access"
	case !terminal && errors.Is(err, domain.ErrConfig):
		return "configuration_error", "complete_configuration"
	case !terminal && errors.Is(err, domain.ErrOutputLimit):
		return "output_limit_exceeded", "narrow_or_raise_bound"
	case errors.Is(err, domain.ErrCheckFailed):
		return "check_failed", "review_failed_check"
	case errors.Is(err, domain.ErrUsage):
		return "usage_error", "fix_request"
	}
	var transportErr *httpx.TransportError
	if errors.As(err, &transportErr) {
		return "transport_error", "inspect_network_before_retry"
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusTooManyRequests {
			return "rate_limited", "wait_before_retry"
		}
		return "api_error", "inspect_backend_error"
	}
	return "unexpected_error", "inspect_error"
}

func terminalCheckFailure(err error) bool {
	var terminal terminalCheckFailureMetadata
	if errors.Is(err, domain.ErrCheckFailed) && errors.As(err, &terminal) && terminal.DiagnosticTerminalCheckFailure() {
		return true
	}
	var ambiguous ambiguousWriteMetadata
	return errors.Is(err, domain.ErrCheckFailed) && errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
}
