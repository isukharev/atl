package domain

import (
	"errors"
	"testing"
)

func TestValidateConfluenceCommentMutationRequest(t *testing.T) {
	valid := []ConfluenceCommentMutationRequest{
		{Operation: ConfluenceCommentMutationReply, PageID: "1", ThreadID: "2", BodyStorage: []byte("<p>reply</p>")},
		{Operation: ConfluenceCommentMutationResolve, PageID: "1", ThreadID: "2"},
		{Operation: ConfluenceCommentMutationReopen, PageID: "1", ThreadID: "2"},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 3, BodyStorage: []byte("<p>body</p>"),
			SearchSelection: "chosen", OriginalSelection: "chosen", NumMatches: 2, MatchIndex: 1, LastFetchTime: 123456,
			SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "chosen", ChildIndexPath: []int{0, 2}, PreviousTextSiblingOffset: 1, Length: 6}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 3, BodyStorage: []byte("<p>body</p>"),
			SearchSelection: "line\nfeed", OriginalSelection: "linefeed", NumMatches: 1, MatchIndex: 0, LastFetchTime: 123456,
			SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "line\nfeed", ChildIndexPath: []int{0, 2}, PreviousTextSiblingOffset: 1, Length: 9}}},
	}
	for _, request := range valid {
		if err := ValidateConfluenceCommentMutationRequest(request); err != nil {
			t.Errorf("ValidateConfluenceCommentMutationRequest(%+v): %v", request, err)
		}
	}

	invalid := []ConfluenceCommentMutationRequest{
		{Operation: "delete", PageID: "1", ThreadID: "2"},
		{Operation: ConfluenceCommentMutationReply, PageID: "", ThreadID: "2", BodyStorage: []byte("x")},
		{Operation: ConfluenceCommentMutationReply, PageID: "page", ThreadID: "2", BodyStorage: []byte("x")},
		{Operation: ConfluenceCommentMutationReply, PageID: "1", ThreadID: "0", BodyStorage: []byte("x")},
		{Operation: ConfluenceCommentMutationReply, PageID: "1", ThreadID: " 2", BodyStorage: []byte("x")},
		{Operation: ConfluenceCommentMutationReply, PageID: "1", ThreadID: "2"},
		{Operation: ConfluenceCommentMutationResolve, PageID: "1", ThreadID: "2", BodyStorage: []byte("ignored")},
		{Operation: ConfluenceCommentMutationReopen, PageID: "1", ThreadID: "2", BodyStorage: []byte("ignored")},
		{Operation: ConfluenceCommentMutationReply, PageID: "1", ThreadID: "2", BodyStorage: []byte("x"), PageVersion: 1},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", ThreadID: "2", PageVersion: 1, BodyStorage: []byte("x"), SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{0}, Length: 1}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 0, BodyStorage: []byte("x"), SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{0}, Length: 1}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 1, BodyStorage: []byte("x"), SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, MatchIndex: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{0}, Length: 1}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 1, BodyStorage: []byte("x"), SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{-1}, Length: 1}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 1, BodyStorage: []byte("x"), SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{0}, Length: 0}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 1, BodyStorage: []byte{0xff}, SearchSelection: "x", OriginalSelection: "x", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "x", ChildIndexPath: []int{0}, Length: 1}}},
		{Operation: ConfluenceCommentMutationInlineCreate, PageID: "1", PageVersion: 1, BodyStorage: []byte("x"), SearchSelection: "line\nfeed", OriginalSelection: "line\nfeed", NumMatches: 1, LastFetchTime: 1, SerializedHighlights: []ConfluenceInlineHighlightGeometry{{Text: "line\nfeed", ChildIndexPath: []int{0}, Length: 9}}},
	}
	for _, request := range invalid {
		if err := ValidateConfluenceCommentMutationRequest(request); !errors.Is(err, ErrUsage) {
			t.Errorf("ValidateConfluenceCommentMutationRequest(%+v) error = %v, want ErrUsage", request, err)
		}
	}
}

func TestValidConfluenceCommentMutationOperation(t *testing.T) {
	for _, operation := range []ConfluenceCommentMutationOperation{
		ConfluenceCommentMutationReply,
		ConfluenceCommentMutationResolve,
		ConfluenceCommentMutationReopen,
		ConfluenceCommentMutationInlineCreate,
	} {
		if !ValidConfluenceCommentMutationOperation(operation) {
			t.Errorf("operation %q is not valid", operation)
		}
	}
	for _, operation := range []ConfluenceCommentMutationOperation{"", "create", "delete", "reply/../../delete"} {
		if ValidConfluenceCommentMutationOperation(operation) {
			t.Errorf("operation %q unexpectedly valid", operation)
		}
	}
}

func TestValidateConfluenceInlineCommentPreparationRequest(t *testing.T) {
	valid := ConfluenceInlineCommentPreparationRequest{PageID: "1", ExpectedPageVersion: 2, OriginalSelection: "selected", MatchIndex: 0}
	if err := ValidateConfluenceInlineCommentPreparationRequest(valid); err != nil {
		t.Fatalf("valid preparation: %v", err)
	}
	for _, request := range []ConfluenceInlineCommentPreparationRequest{
		{},
		{PageID: "page", ExpectedPageVersion: 2, OriginalSelection: "selected"},
		{PageID: "1", ExpectedPageVersion: 0, OriginalSelection: "selected"},
		{PageID: "1", ExpectedPageVersion: 2, OriginalSelection: ""},
		{PageID: "1", ExpectedPageVersion: 2, OriginalSelection: "selected", MatchIndex: -1},
	} {
		if err := ValidateConfluenceInlineCommentPreparationRequest(request); !errors.Is(err, ErrUsage) {
			t.Errorf("request %+v error = %v, want ErrUsage", request, err)
		}
	}
}
