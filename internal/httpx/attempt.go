package httpx

import (
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type attemptResult struct {
	kind       error
	retryable  bool
	retryDelay time.Duration
}

// classifyAttempt is the shared response-policy classifier for buffered and
// streaming paths. Callers still own their established body-close and retry
// timing; this value only records the decision derived from method/status.
func (c *Client) classifyAttempt(method string, response *http.Response) attemptResult {
	result := attemptResult{kind: c.classifyResult(response.StatusCode)}
	result.retryable = replaySafe(method) && transientRetryStatus(response.StatusCode)
	if result.retryable {
		result.retryDelay = retryAfter(response)
	}
	return result
}

func (c *Client) classifyResult(status int) error {
	kind := classify(status)
	if c.genericConflict && kind == domain.ErrVersionConflict {
		return nil
	}
	return kind
}
