package diagnostic

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type classifiedAmbiguousWrite struct{ cause error }

func (*classifiedAmbiguousWrite) Error() string                  { return "content-free ambiguous write" }
func (e *classifiedAmbiguousWrite) Unwrap() []error              { return []error{domain.ErrCheckFailed, e.cause} }
func (*classifiedAmbiguousWrite) DiagnosticAmbiguousWrite() bool { return true }

type classifiedTerminalCheckFailure struct{ cause error }

func (*classifiedTerminalCheckFailure) Error() string { return "content-free terminal check failure" }
func (e *classifiedTerminalCheckFailure) Unwrap() []error {
	return []error{domain.ErrCheckFailed, e.cause}
}
func (*classifiedTerminalCheckFailure) DiagnosticTerminalCheckFailure() bool { return true }

func TestClassifyTerminalAmbiguityPrecedesSafeNestedCauses(t *testing.T) {
	assertTerminalCheckFailureClassification(t, func(cause error) error { return &classifiedAmbiguousWrite{cause: cause} })
}

func TestClassifyClosedTerminalFailurePrecedesSafeNestedCauses(t *testing.T) {
	assertTerminalCheckFailureClassification(t, func(cause error) error { return &classifiedTerminalCheckFailure{cause: cause} })
}

func assertTerminalCheckFailureClassification(t *testing.T, wrap func(error) error) {
	t.Helper()
	for _, cause := range []error{domain.ErrAuth, domain.ErrNotFound, domain.ErrForbidden, domain.ErrConfig} {
		err := wrap(cause)
		if !errors.Is(err, cause) || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("safe identities lost for %v", cause)
		}
		if kind, remediation := Classify(err); kind != "check_failed" || remediation != "review_failed_check" {
			t.Fatalf("cause=%v classification=%q/%q", cause, kind, remediation)
		}
	}
}

func TestClassifyOutputLimitBeforeGenericCheckFailure(t *testing.T) {
	err := fmt.Errorf("%w: %w: encoded result exceeds max_bytes", domain.ErrCheckFailed, domain.ErrOutputLimit)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrOutputLimit) {
		t.Fatalf("sentinel identity lost: %v", err)
	}
	if kind, remediation := Classify(err); kind != "output_limit_exceeded" || remediation != "narrow_or_raise_bound" {
		t.Fatalf("classification=%q/%q", kind, remediation)
	}
	if kind, remediation := Classify(fmt.Errorf("%w: unrelated reconciliation", domain.ErrCheckFailed)); kind != "check_failed" || remediation != "review_failed_check" {
		t.Fatalf("unrelated classification=%q/%q", kind, remediation)
	}
}

func TestClassifyRateLimitedAPIError(t *testing.T) {
	err := fmt.Errorf("read failed: %w", &httpx.APIError{
		Status: http.StatusTooManyRequests,
		Method: http.MethodGet,
		Path:   "/private/path?query=secret",
		Body:   "private backend detail",
	})
	if kind, remediation := Classify(err); kind != "rate_limited" || remediation != "wait_before_retry" {
		t.Fatalf("classification=%q/%q", kind, remediation)
	}
}

func TestClassifyOtherAPIErrorRemainsGeneric(t *testing.T) {
	err := &httpx.APIError{Status: http.StatusServiceUnavailable, Method: http.MethodGet, Path: "/safe"}
	if kind, remediation := Classify(err); kind != "api_error" || remediation != "inspect_backend_error" {
		t.Fatalf("classification=%q/%q", kind, remediation)
	}
}
