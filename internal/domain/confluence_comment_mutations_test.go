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
