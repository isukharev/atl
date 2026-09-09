package httpx

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const maxProjectedResponseMetadataBytes = 4 << 10

// BoundedResponseStream is the content-minimized result of one streamed POST
// response. Body owns the response, cancellation, idle watchdog, and budget
// reservation until EOF or Close.
type BoundedResponseStream struct {
	Status          int
	ContentType     string
	ContentEncoding string
	CorrelationID   string
	Body            io.ReadCloser
}

// PostBoundedResponseStream performs one caller-classified JSON POST and
// returns its response body under a status-selected finite streaming bound.
func (c *Client) PostBoundedResponseStream(ctx context.Context, path string, requestBody []byte, successLimit, failureLimit int64) (BoundedResponseStream, error) {
	if c == nil || ctx == nil || !domain.ReadIntent(ctx) || !domain.SingleAttempt(ctx) || len(requestBody) == 0 ||
		successLimit <= 0 || failureLimit <= 0 || successLimit > maxReviewedResponseBody || failureLimit > maxReviewedResponseBody {
		return BoundedResponseStream{}, fmt.Errorf("%w: invalid bounded POST response stream", domain.ErrCheckFailed)
	}
	if err := ctx.Err(); err != nil {
		return BoundedResponseStream{}, err
	}
	deadline, bounded := ctx.Deadline()
	if !bounded || !deadline.After(time.Now()) {
		if err := ctx.Err(); err != nil {
			return BoundedResponseStream{}, err
		}
		return BoundedResponseStream{}, fmt.Errorf("%w: bounded POST response stream requires a live deadline", domain.ErrCheckFailed)
	}
	parent := domain.ReadBudgetFromContext(ctx)
	if parent == nil {
		return BoundedResponseStream{}, fmt.Errorf("%w: bounded POST response stream requires a read budget", domain.ErrCheckFailed)
	}
	attemptBudget, err := domain.NewChildReadBudget(parent, 1, math.MaxInt64)
	if err != nil {
		return BoundedResponseStream{}, fmt.Errorf("%w: bounded POST response stream budget is invalid", domain.ErrCheckFailed)
	}
	resolved, err := c.resolveURL(path)
	if err != nil {
		return BoundedResponseStream{}, err
	}
	rctx, cancel := context.WithCancel(domain.WithReadBudget(ctx, attemptBudget))
	rctx = context.WithValue(rctx, downloadRedirectCancelKey{}, cancel)
	req, err := c.newRequest(rctx, http.MethodPost, resolved, requestBody, map[string]string{
		"Accept":          "application/x-ndjson, application/json",
		"Accept-Encoding": "identity",
		"Content-Type":    "application/json",
	})
	if err != nil {
		cancel()
		return BoundedResponseStream{}, err
	}
	c.tracef("→ POST %s\n", traceRequestURL(ctx, req.URL))
	resp, err := c.dl.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		c.tracef("× POST %s (transport error: %s)\n", traceRequestURL(ctx, req.URL), transportErrorCategory(err))
		if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
			return BoundedResponseStream{}, budgetErr
		}
		return BoundedResponseStream{}, transportError(http.MethodPost, req.URL, err)
	}
	c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, req.URL.Path))
	contentType, contentEncoding, correlationID, ok := boundedResponseMetadata(resp.Header)
	if !ok {
		cancel()
		_ = resp.Body.Close()
		return BoundedResponseStream{}, fmt.Errorf("%w: response metadata exceeds the reviewed boundary", domain.ErrCheckFailed)
	}
	limit := failureLimit
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		limit = successLimit
	}
	bodyBudget, err := domain.NewChildReadBudget(attemptBudget, 0, limit)
	if err != nil {
		cancel()
		_ = resp.Body.Close()
		return BoundedResponseStream{}, fmt.Errorf("%w: response stream budget is invalid", domain.ErrCheckFailed)
	}
	bodyCtx := domain.WithReadBudget(rctx, bodyBudget)
	return BoundedResponseStream{
		Status: resp.StatusCode, ContentType: contentType, ContentEncoding: contentEncoding,
		CorrelationID: correlationID, Body: newDownloadStream(bodyCtx, resp.Body, cancel),
	}, nil
}

func boundedResponseMetadata(header http.Header) (string, string, string, bool) {
	values := make([]string, 0, 3)
	for _, name := range []string{"Content-Type", "Content-Encoding", "X-ATL-Correlation-ID"} {
		current := header.Values(name)
		if len(current) > 1 {
			return "", "", "", false
		}
		value := ""
		if len(current) == 1 {
			value = current[0]
		}
		values = append(values, value)
	}
	if len(values[0])+len(values[1])+len(values[2]) > maxProjectedResponseMetadataBytes ||
		strings.ContainsAny(values[0]+values[1]+values[2], "\r\n\x00") {
		return "", "", "", false
	}
	return values[0], values[1], values[2], true
}
