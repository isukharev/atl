package diagnostic

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/isukharev/atl/internal/httpx"
)

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
