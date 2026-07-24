package diagnostic

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

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
