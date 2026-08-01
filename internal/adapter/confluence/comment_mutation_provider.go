package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

// CommentMutationProvider implements the fixed internal-comment protocol for
// one explicitly activated and exactly requalified Confluence backend.
type CommentMutationProvider struct {
	confluence *Confluence
	activation compatibility.Activation
}

const commentMutationResponseMaxBytes = 64 << 10

var _ domain.ConfluenceCommentMutator = (*CommentMutationProvider)(nil)

// NewCommentMutationProvider binds the generic compiled profile to an owner
// supplied exact backend identity. Construction performs no network access.
func NewCommentMutationProvider(confluence *Confluence, activation compatibility.Activation) (*CommentMutationProvider, error) {
	if confluence == nil || confluence.c == nil {
		return nil, fmt.Errorf("%w: Confluence compatibility provider is unavailable", domain.ErrConfig)
	}
	if err := activation.Validate(compatibility.ProductConfluence); err != nil {
		return nil, err
	}
	return &CommentMutationProvider{confluence: confluence, activation: activation}, nil
}

// MutateConfluenceComment requalifies exact product identity, then performs
// exactly one fixed write. No fallback is attempted after the write begins.
func (provider *CommentMutationProvider) MutateConfluenceComment(ctx context.Context, request domain.ConfluenceCommentMutationRequest) (domain.ConfluenceCommentMutationResult, error) {
	if provider == nil || provider.confluence == nil || provider.confluence.c == nil {
		return domain.ConfluenceCommentMutationResult{}, fmt.Errorf("%w: Confluence compatibility provider is unavailable", domain.ErrConfig)
	}
	if err := domain.ValidateConfluenceCommentMutationRequest(request); err != nil {
		return domain.ConfluenceCommentMutationResult{}, err
	}
	if err := provider.activation.Validate(compatibility.ProductConfluence); err != nil {
		return domain.ConfluenceCommentMutationResult{}, err
	}

	writeContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	metadata, err := provider.confluence.ExactServerMetadata(writeContext)
	if err != nil {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError("qualification", err, false)
	}
	if metadata.Product != domain.ServerProductConfluence ||
		metadata.Version != string(provider.activation.Version) ||
		metadata.BuildNumber != string(provider.activation.BuildNumber) {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError(
			"qualification", fmt.Errorf("%w: activation does not match", domain.ErrCheckFailed), false,
		)
	}

	switch request.Operation {
	case domain.ConfluenceCommentMutationReply:
		return provider.reply(writeContext, request)
	case domain.ConfluenceCommentMutationResolve:
		return provider.setResolved(writeContext, request, true)
	case domain.ConfluenceCommentMutationReopen:
		return provider.setResolved(writeContext, request, false)
	default:
		// Domain validation above makes this unreachable and preserves a fixed
		// operation-to-endpoint mapping even if this method changes later.
		return domain.ConfluenceCommentMutationResult{}, fmt.Errorf("%w: unsupported Confluence comment mutation", domain.ErrUsage)
	}
}

func (provider *CommentMutationProvider) reply(ctx context.Context, request domain.ConfluenceCommentMutationRequest) (domain.ConfluenceCommentMutationResult, error) {
	payload := struct {
		CommentID   string `json:"commentId"`
		ContainerID string `json:"containerId"`
		Body        string `json:"body"`
	}{CommentID: request.ThreadID, ContainerID: request.PageID, Body: string(request.BodyStorage)}
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.ConfluenceCommentMutationResult{}, fmt.Errorf("%w: Confluence reply request could not be encoded", domain.ErrCheckFailed)
	}
	query := url.Values{}
	query.Set("containerId", request.PageID)
	path := "/rest/inlinecomments/1.0/comments/" + url.PathEscape(request.ThreadID) + "/replies?" + query.Encode()
	response, err := provider.confluence.c.DoWithBodyLimit(ctx, http.MethodPost, path, body, commentMutationHeaders(), commentMutationResponseMaxBytes)
	if err != nil {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError("write", err, true)
	}
	commentID, err := decodeReplyID(response, request.ThreadID)
	if err != nil {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError("response", err, true)
	}
	return domain.ConfluenceCommentMutationResult{
		Operation: request.Operation,
		ThreadID:  request.ThreadID,
		CommentID: commentID,
		Resolved:  false,
	}, nil
}

func (provider *CommentMutationProvider) setResolved(ctx context.Context, request domain.ConfluenceCommentMutationRequest, resolved bool) (domain.ConfluenceCommentMutationResult, error) {
	path := "/rest/inlinecomments/1.0/comments/" + url.PathEscape(request.ThreadID) +
		"/resolve/" + strconv.FormatBool(resolved) + "/dangling/false"
	response, err := provider.confluence.c.DoWithBodyLimit(ctx, http.MethodPut, path, []byte("{}"), commentMutationHeaders(), commentMutationResponseMaxBytes)
	if err != nil {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError("write", err, true)
	}
	var decoded struct {
		ResolveProperties *struct {
			Resolved *bool `json:"resolved"`
		} `json:"resolveProperties"`
	}
	if err := decodeOneJSON(response, &decoded); err != nil || decoded.ResolveProperties == nil ||
		decoded.ResolveProperties.Resolved == nil || *decoded.ResolveProperties.Resolved != resolved {
		return domain.ConfluenceCommentMutationResult{}, sanitizedCommentMutationError(
			"response", fmt.Errorf("%w: response was not qualified", domain.ErrCheckFailed), true,
		)
	}
	return domain.ConfluenceCommentMutationResult{
		Operation: request.Operation,
		ThreadID:  request.ThreadID,
		CommentID: request.ThreadID,
		Resolved:  resolved,
	}, nil
}

func commentMutationHeaders() map[string]string {
	return map[string]string{
		"Content-Type":      "application/json",
		"Accept":            "application/json",
		"X-Atlassian-Token": "no-check",
	}
}

func decodeReplyID(data []byte, expectedThreadID string) (string, error) {
	var response struct {
		ID        json.RawMessage `json:"id"`
		CommentID json.RawMessage `json:"commentId"`
	}
	if err := decodeOneJSON(data, &response); err != nil {
		return "", fmt.Errorf("%w: Confluence reply response was not qualified", domain.ErrCheckFailed)
	}
	id, ok := positiveJSONContentID(response.ID)
	if !ok {
		return "", fmt.Errorf("%w: Confluence reply response was not qualified", domain.ErrCheckFailed)
	}
	threadID, ok := positiveJSONContentID(response.CommentID)
	if !ok || threadID != expectedThreadID {
		return "", fmt.Errorf("%w: Confluence reply response was not qualified", domain.ErrCheckFailed)
	}
	return id, nil
}

func decodeOneJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func positiveJSONContentID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	value := string(raw)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", false
		}
	}
	if value == "" || value[0] == '0' {
		return "", false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return value, err == nil
}

type commentMutationProviderError struct {
	stage     string
	cause     error
	attempted bool
	status    int
}

func (e *commentMutationProviderError) Error() string {
	return "Confluence compatibility " + e.stage + " failed"
}

func (e *commentMutationProviderError) Unwrap() error   { return e.cause }
func (e *commentMutationProviderError) HTTPStatus() int { return e.status }
func (e *commentMutationProviderError) DiagnosticWriteAttempted() bool {
	return e != nil && e.attempted
}

func sanitizedCommentMutationError(stage string, err error, attempted bool) error {
	status := 0
	var statusError interface{ HTTPStatus() int }
	if errors.As(err, &statusError) {
		status = statusError.HTTPStatus()
	}
	for _, sentinel := range []error{
		domain.ErrUsage,
		domain.ErrAuth,
		domain.ErrNotFound,
		domain.ErrVersionConflict,
		domain.ErrForbidden,
		domain.ErrConfig,
		domain.ErrCheckFailed,
		domain.ErrReadAttemptBudgetExhausted,
		domain.ErrReadResponseBudgetExhausted,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		if errors.Is(err, sentinel) {
			return &commentMutationProviderError{stage: stage, cause: sentinel, attempted: attempted, status: status}
		}
	}
	return &commentMutationProviderError{stage: stage, attempted: attempted, status: status}
}
