package app

import (
	"context"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type qualifiedSearchStore struct {
	recordingStore
	page domain.PageSearchPage
}

func (s *qualifiedSearchStore) SearchComplete(context.Context, string, int, string) (domain.PageSearchPage, error) {
	return s.page, nil
}

func TestConfluenceSearchQualifiedPreservesCompleteTerminalEvidence(t *testing.T) {
	store := &qualifiedSearchStore{page: domain.PageSearchPage{
		Results:  []domain.PageRef{{ID: "42", Title: "Result"}},
		Complete: true,
	}}
	result, err := (&ConfluenceService{store: store}).SearchQualified(context.Background(), `text ~ "topic"`, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Truncated || result.Count != 1 || result.NextCursor != nil || result.PartialReason != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestConfluenceSearchQualifiedKeepsContinuationFailClosed(t *testing.T) {
	store := &qualifiedSearchStore{page: domain.PageSearchPage{
		Results: []domain.PageRef{{ID: "42"}}, Next: "1", Complete: false,
	}}
	result, err := (&ConfluenceService{store: store}).SearchQualified(context.Background(), "x", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.NextCursor == nil || *result.NextCursor != "1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestConfluenceSearchQualifiedDoesNotTrustLegacyTerminalCursor(t *testing.T) {
	store := &recordingStore{pageRefs: []domain.PageRef{{ID: "42"}}}
	result, err := (&ConfluenceService{store: store}).SearchQualified(context.Background(), "x", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.PartialReason == "" {
		t.Fatalf("result=%+v", result)
	}
}
