package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type qualifiedSearchTracker struct {
	*recordingTracker
	page domain.IssueSearchPage
}

type resumableLegacySearchTracker struct{ *recordingTracker }

func (t *resumableLegacySearchTracker) Search(_ context.Context, jql string, fields []string, limit int, _ string) ([]domain.Issue, string, error) {
	t.searchJQL, t.searchFields, t.searchLimit = jql, fields, limit
	return t.issues, "50", t.err
}

func (t *qualifiedSearchTracker) SearchQualified(_ context.Context, jql string, fields []string, limit int, _ string) (domain.IssueSearchPage, error) {
	t.searchJQL, t.searchFields, t.searchLimit = jql, fields, limit
	return t.page, t.err
}

func TestSearchIssueListUsesQualifiedCompletion(t *testing.T) {
	tracker := &qualifiedSearchTracker{
		recordingTracker: &recordingTracker{},
		page: domain.IssueSearchPage{
			Issues:        []domain.Issue{},
			PartialReason: domain.IssueSearchPartialPaginationStalled,
		},
	}
	list, err := (&JiraService{tr: tracker}).SearchIssueList(
		context.Background(), "project = PROJ", []string{"key", "status"}, 50, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if list.Page.Complete || !list.Page.Truncated || list.Page.NextCursor != nil ||
		list.Page.PartialReason != domain.IssueSearchPartialPaginationStalled || list.Page.Count != 0 {
		t.Fatalf("page=%+v", list.Page)
	}
	if tracker.searchJQL != "project = PROJ" || tracker.searchLimit != 50 ||
		len(tracker.searchFields) != 1 || tracker.searchFields[0] != "status" {
		t.Fatalf("qualified search args: jql=%q fields=%v limit=%d", tracker.searchJQL, tracker.searchFields, tracker.searchLimit)
	}
}

func TestSearchIssueListLegacyTrackerIsExplicitlyUnqualified(t *testing.T) {
	tracker := &recordingTracker{issues: []domain.Issue{{Key: "PROJ-1"}}}
	list, err := (&JiraService{tr: tracker}).SearchIssueList(
		context.Background(), "project = PROJ", []string{"key"}, 50, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if list.Page.Complete || !list.Page.Truncated ||
		list.Page.PartialReason != domain.IssueSearchPartialLegacyUnqualified || list.Page.Count != 1 {
		t.Fatalf("page=%+v", list.Page)
	}
}

func TestSearchIssueListResumableLegacyTrackerUsesCursorWithoutPartialReason(t *testing.T) {
	tracker := &resumableLegacySearchTracker{recordingTracker: &recordingTracker{
		issues: []domain.Issue{{Key: "PROJ-1"}},
	}}
	list, err := (&JiraService{tr: tracker}).SearchIssueList(
		context.Background(), "project = PROJ", []string{"key"}, 50, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if list.Page.Complete || !list.Page.Truncated || list.Page.PartialReason != "" ||
		list.Page.NextCursor == nil || *list.Page.NextCursor != "50" {
		t.Fatalf("page=%+v", list.Page)
	}
}

func TestSearchIssueListRejectsUnclosedQualificationReasons(t *testing.T) {
	tracker := &qualifiedSearchTracker{
		recordingTracker: &recordingTracker{},
		page:             domain.IssueSearchPage{PartialReason: "backend supplied explanation"},
	}
	_, err := (&JiraService{tr: tracker}).SearchIssueList(
		context.Background(), "project = PROJ", []string{"key"}, 50, "",
	)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
	for _, reason := range []string{
		domain.IssueSearchPartialLegacyUnqualified,
		domain.IssueSearchPartialPaginationUnqualified,
		domain.IssueSearchPartialPaginationStalled,
	} {
		if !domain.ValidIssueSearchPartialReason(reason) {
			t.Fatalf("static reason %q rejected", reason)
		}
	}
	if domain.ValidIssueSearchPartialReason("") || domain.ValidIssueSearchPartialReason("backend supplied explanation") {
		t.Fatal("open partial-reason value accepted")
	}
}

func TestSearchIssueListRejectsTerminalReasonWithContinuationCursor(t *testing.T) {
	tracker := &qualifiedSearchTracker{
		recordingTracker: &recordingTracker{},
		page: domain.IssueSearchPage{
			Next:          "50",
			PartialReason: domain.IssueSearchPartialPaginationStalled,
		},
	}
	_, err := (&JiraService{tr: tracker}).SearchIssueList(
		context.Background(), "project = PROJ", []string{"key"}, 50, "",
	)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
}
