package httpx

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

const maxReviewedResponseBody = int64(129 << 20)

// BoundedResponse is the content-minimized result of one reviewed transport
// attempt. Only the Broker correlation header crosses this boundary.
type BoundedResponse struct {
	Status        int
	Body          []byte
	CorrelationID string
}

// DoBoundedResponse performs exactly one caller-classified HTTP attempt and
// returns both success and failure bodies under separate finite bounds.
func (c *Client) DoBoundedResponse(ctx context.Context, method, path string, body []byte, headers map[string]string, successLimit, failureLimit int64) (BoundedResponse, error) {
	if !domain.SingleAttempt(ctx) || successLimit <= 0 || failureLimit <= 0 || successLimit > maxReviewedResponseBody || failureLimit > maxReviewedResponseBody {
		return BoundedResponse{}, fmt.Errorf("%w: invalid single-attempt response bounds", domain.ErrCheckFailed)
	}
	if err := validateNoReplayReadBudget(ctx); err != nil {
		return BoundedResponse{}, err
	}
	resolved, err := c.resolveURL(path)
	if err != nil {
		return BoundedResponse{}, err
	}
	req, err := c.newRequest(ctx, method, resolved, body, headers)
	if err != nil {
		return BoundedResponse{}, err
	}
	c.tracef("→ %s %s\n", method, traceRequestURL(ctx, req.URL))
	resp, err := c.hc.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
			return BoundedResponse{}, budgetErr
		}
		return BoundedResponse{}, transportError(method, req.URL, err)
	}
	defer resp.Body.Close()
	c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, req.URL.Path))
	limit := failureLimit
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		limit = successLimit
	}
	data, err := readResponseBody(ctx, resp.Body, limit)
	if err != nil {
		return BoundedResponse{}, err
	}
	return BoundedResponse{Status: resp.StatusCode, Body: data, CorrelationID: resp.Header.Get("X-ATL-Correlation-ID")}, nil
}
