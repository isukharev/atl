// Package diagnostic exposes stable, transport-neutral error classification.
// Human-readable error strings remain useful diagnostics; Kind and Remediation
// are the durable contract consumed by CLI JSON and MCP tool clients.
package diagnostic

import (
	"errors"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// Classify maps an application error to a stable machine-readable kind and a
// coarse recovery action. Transport layers may add their own policy-specific
// cases before calling Classify.
func Classify(err error) (kind, remediation string) {
	switch {
	case errors.Is(err, domain.ErrAuth):
		return "authentication_failed", "reauthenticate"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found", "verify_identifier_or_access"
	case errors.Is(err, domain.ErrVersionConflict):
		return "version_conflict", "refresh_and_reapply"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden", "request_access"
	case errors.Is(err, domain.ErrConfig):
		return "configuration_error", "complete_configuration"
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
		return "api_error", "inspect_backend_error"
	}
	return "unexpected_error", "inspect_error"
}
