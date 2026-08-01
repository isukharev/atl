package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ConfluenceCommentMutationOperation is the closed semantic operation matrix
// implemented by an explicitly activated compatibility provider.
type ConfluenceCommentMutationOperation string

const (
	ConfluenceCommentMutationReply   ConfluenceCommentMutationOperation = "reply"
	ConfluenceCommentMutationResolve ConfluenceCommentMutationOperation = "resolve"
	ConfluenceCommentMutationReopen  ConfluenceCommentMutationOperation = "reopen"
)

// ValidConfluenceCommentMutationOperation reports whether operation belongs to
// the fixed semantic matrix. Adapters must not accept arbitrary REST actions.
func ValidConfluenceCommentMutationOperation(operation ConfluenceCommentMutationOperation) bool {
	switch operation {
	case ConfluenceCommentMutationReply, ConfluenceCommentMutationResolve, ConfluenceCommentMutationReopen:
		return true
	default:
		return false
	}
}

// ConfluenceCommentMutationRequest contains semantic identifiers and native
// comment storage. Endpoint paths and protocol DTOs remain adapter-private.
type ConfluenceCommentMutationRequest struct {
	Operation   ConfluenceCommentMutationOperation
	PageID      string
	ThreadID    string
	BodyStorage []byte
}

// ValidateConfluenceCommentMutationRequest rejects malformed requests before
// any compatibility qualification or backend write is attempted.
func ValidateConfluenceCommentMutationRequest(request ConfluenceCommentMutationRequest) error {
	if !ValidConfluenceCommentMutationOperation(request.Operation) {
		return fmt.Errorf("%w: unsupported Confluence comment mutation", ErrUsage)
	}
	if !positiveConfluenceContentID(request.PageID) {
		return fmt.Errorf("%w: Confluence page id must be a positive numeric content id", ErrUsage)
	}
	if !positiveConfluenceContentID(request.ThreadID) {
		return fmt.Errorf("%w: Confluence thread id must be a positive numeric content id", ErrUsage)
	}
	switch request.Operation {
	case ConfluenceCommentMutationReply:
		if len(request.BodyStorage) == 0 {
			return fmt.Errorf("%w: Confluence reply body must not be empty", ErrUsage)
		}
	case ConfluenceCommentMutationResolve, ConfluenceCommentMutationReopen:
		if len(request.BodyStorage) != 0 {
			return fmt.Errorf("%w: Confluence resolution mutation must not carry a body", ErrUsage)
		}
	}
	return nil
}

func positiveConfluenceContentID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

// ConfluenceCommentMutationResult is the minimal semantic projection of one
// qualified provider response. For replies CommentID is the created reply id;
// for resolve/reopen it is the affected root thread id.
type ConfluenceCommentMutationResult struct {
	Operation ConfluenceCommentMutationOperation
	ThreadID  string
	CommentID string
	Resolved  bool
}

// ConfluenceCommentMutator is the optional semantic port for compatibility
// providers. Implementations must requalify their exact backend activation
// immediately before every individual write.
type ConfluenceCommentMutator interface {
	MutateConfluenceComment(context.Context, ConfluenceCommentMutationRequest) (ConfluenceCommentMutationResult, error)
}
