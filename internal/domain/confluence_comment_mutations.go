package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ConfluenceCommentMutationOperation is the closed semantic operation matrix
// implemented by an explicitly activated compatibility provider.
type ConfluenceCommentMutationOperation string

const (
	ConfluenceCommentMutationReply        ConfluenceCommentMutationOperation = "reply"
	ConfluenceCommentMutationResolve      ConfluenceCommentMutationOperation = "resolve"
	ConfluenceCommentMutationReopen       ConfluenceCommentMutationOperation = "reopen"
	ConfluenceCommentMutationInlineCreate ConfluenceCommentMutationOperation = "inline_create"
)

// ValidConfluenceCommentMutationOperation reports whether operation belongs to
// the fixed semantic matrix. Adapters must not accept arbitrary REST actions.
func ValidConfluenceCommentMutationOperation(operation ConfluenceCommentMutationOperation) bool {
	switch operation {
	case ConfluenceCommentMutationReply, ConfluenceCommentMutationResolve, ConfluenceCommentMutationReopen,
		ConfluenceCommentMutationInlineCreate:
		return true
	default:
		return false
	}
}

// ConfluenceCommentMutationRequest contains semantic identifiers, native
// comment storage, and adapter-prepared browser selection evidence.
// SearchSelection retains LF; OriginalSelection is its LF-stripped wire value.
// Endpoint paths and protocol DTOs remain adapter-private.
type ConfluenceCommentMutationRequest struct {
	Operation            ConfluenceCommentMutationOperation
	PageID               string
	ThreadID             string
	PageVersion          int
	BodyStorage          []byte
	SearchSelection      string
	OriginalSelection    string
	NumMatches           int
	MatchIndex           int
	LastFetchTime        int64
	SerializedHighlights []ConfluenceInlineHighlightGeometry
}

// ConfluenceInlineHighlightGeometry is one reviewed DOM-fragment descriptor.
// The adapter owns its wire serialization; callers cannot supply arbitrary
// JSON or a backend path string.
type ConfluenceInlineHighlightGeometry struct {
	Text                      string
	ChildIndexPath            []int
	PreviousTextSiblingOffset int
	Length                    int
}

// ConfluenceInlineCommentPreparationRequest identifies one selection in one
// exact server-rendered page revision. OriginalSelection is user input;
// MatchIndex is zero-based among overlapping matches in the unmasked,
// NBSP-normalized DOM text. A local clock or CSF rendering is never valid
// preparation.
type ConfluenceInlineCommentPreparationRequest struct {
	PageID              string
	ExpectedPageVersion int
	OriginalSelection   string
	MatchIndex          int
}

// ValidateConfluenceInlineCommentPreparationRequest rejects incomplete or
// malformed read-only preparation inputs before any backend request.
func ValidateConfluenceInlineCommentPreparationRequest(request ConfluenceInlineCommentPreparationRequest) error {
	if !positiveConfluenceContentID(request.PageID) || request.ExpectedPageVersion <= 0 ||
		request.OriginalSelection == "" || !utf8.ValidString(request.OriginalSelection) || request.MatchIndex < 0 {
		return fmt.Errorf("%w: invalid Confluence inline preparation request", ErrUsage)
	}
	return nil
}

// ConfluenceInlineCommentPreparation is the content-minimal evidence derived
// from one bounded server-rendered HTML view. ViewSHA256 binds a canonical
// content-subtree projection (not volatile full-page bytes) without allowing
// page content to cross the port. SearchSelection is the pinned browser's
// NBSP-normalized and trimmed RangeHelper value; OriginalSelection is its POST
// value after LF removal; HighlightedSelection is the raw DOM text retained by
// the serialized descriptors. Callers must not assume those values are equal.
type ConfluenceInlineCommentPreparation struct {
	PageID               string
	PageVersion          int
	LastFetchTime        int64
	SearchSelection      string
	OriginalSelection    string
	HighlightedSelection string
	NumMatches           int
	MatchIndex           int
	SerializedHighlights []ConfluenceInlineHighlightGeometry
	ViewSHA256           string
	MarkerRefs           []string
}

// ConfluenceInlineCommentPreparer is the read-only port that derives browser-
// compatible highlight geometry and server request time from the same view.
type ConfluenceInlineCommentPreparer interface {
	PrepareConfluenceInlineComment(context.Context, ConfluenceInlineCommentPreparationRequest) (ConfluenceInlineCommentPreparation, error)
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
	switch request.Operation {
	case ConfluenceCommentMutationReply:
		if !positiveConfluenceContentID(request.ThreadID) {
			return fmt.Errorf("%w: Confluence thread id must be a positive numeric content id", ErrUsage)
		}
		if len(request.BodyStorage) == 0 || hasInlineCreateFields(request) {
			return fmt.Errorf("%w: Confluence reply body must not be empty", ErrUsage)
		}
	case ConfluenceCommentMutationResolve, ConfluenceCommentMutationReopen:
		if !positiveConfluenceContentID(request.ThreadID) {
			return fmt.Errorf("%w: Confluence thread id must be a positive numeric content id", ErrUsage)
		}
		if len(request.BodyStorage) != 0 || hasInlineCreateFields(request) {
			return fmt.Errorf("%w: Confluence resolution mutation must not carry a body", ErrUsage)
		}
	case ConfluenceCommentMutationInlineCreate:
		if request.ThreadID != "" {
			return fmt.Errorf("%w: Confluence inline create must not target an existing thread", ErrUsage)
		}
		if request.PageVersion <= 0 || len(request.BodyStorage) == 0 || !utf8.Valid(request.BodyStorage) || request.SearchSelection == "" ||
			!utf8.ValidString(request.SearchSelection) || request.OriginalSelection == "" || !utf8.ValidString(request.OriginalSelection) ||
			strings.ReplaceAll(request.SearchSelection, "\n", "") != request.OriginalSelection || request.NumMatches <= 0 || request.MatchIndex < 0 ||
			request.MatchIndex >= request.NumMatches || request.LastFetchTime <= 0 || len(request.SerializedHighlights) == 0 {
			return fmt.Errorf("%w: incomplete Confluence inline create evidence", ErrUsage)
		}
		for _, highlight := range request.SerializedHighlights {
			if highlight.Text == "" || !utf8.ValidString(highlight.Text) || len(highlight.ChildIndexPath) == 0 ||
				highlight.PreviousTextSiblingOffset < 0 || highlight.Length <= 0 {
				return fmt.Errorf("%w: invalid Confluence inline highlight geometry", ErrUsage)
			}
			for _, childIndex := range highlight.ChildIndexPath {
				if childIndex < 0 {
					return fmt.Errorf("%w: invalid Confluence inline highlight geometry", ErrUsage)
				}
			}
		}
	}
	return nil
}

func hasInlineCreateFields(request ConfluenceCommentMutationRequest) bool {
	return request.PageVersion != 0 || request.SearchSelection != "" || request.OriginalSelection != "" || request.NumMatches != 0 ||
		request.MatchIndex != 0 || request.LastFetchTime != 0 || request.SerializedHighlights != nil
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
	Operation         ConfluenceCommentMutationOperation
	ThreadID          string
	CommentID         string
	Resolved          bool
	MarkerRef         string
	OriginalSelection string
	PageVersion       int
}

// ConfluenceCommentMutator is the optional semantic port for compatibility
// providers. Implementations must requalify their exact backend activation
// immediately before every individual write.
type ConfluenceCommentMutator interface {
	MutateConfluenceComment(context.Context, ConfluenceCommentMutationRequest) (ConfluenceCommentMutationResult, error)
}
