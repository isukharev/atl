package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// waveBTracker embeds the interface so only the Wave-B methods need bodies.
type waveBTracker struct {
	domain.Tracker
	historyKey   string
	changelog    []domain.ChangelogEntry
	commentsKey  string
	commentsList []domain.Comment
	delCmtKey    string
	delCmtID     string
	delLinkID    string
	issue        *domain.Issue
	linksKey     string
	err          error
}

func (t *waveBTracker) Changelog(_ context.Context, key string) ([]domain.ChangelogEntry, error) {
	t.historyKey = key
	return t.changelog, t.err
}

func (t *waveBTracker) ListComments(_ context.Context, key string) ([]domain.Comment, error) {
	t.commentsKey = key
	return t.commentsList, t.err
}

func (t *waveBTracker) DeleteComment(_ context.Context, key, id string) error {
	t.delCmtKey, t.delCmtID = key, id
	return t.err
}

func (t *waveBTracker) DeleteLink(_ context.Context, id string) error {
	t.delLinkID = id
	return t.err
}

func (t *waveBTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	t.linksKey = key
	return t.issue, t.err
}

func TestJiraWaveBWrappers(t *testing.T) {
	ctx := context.Background()

	t.Run("History", func(t *testing.T) {
		tr := &waveBTracker{changelog: []domain.ChangelogEntry{{ID: "h1"}}}
		svc := &JiraService{tr: tr}
		got, err := svc.History(ctx, "K-1")
		if err != nil {
			t.Fatal(err)
		}
		if tr.historyKey != "K-1" || len(got) != 1 || got[0].ID != "h1" {
			t.Errorf("History: key=%q ret=%+v", tr.historyKey, got)
		}
	})

	t.Run("Comments", func(t *testing.T) {
		tr := &waveBTracker{commentsList: []domain.Comment{{ID: "c1"}}}
		svc := &JiraService{tr: tr}
		got, err := svc.Comments(ctx, "K-2")
		if err != nil {
			t.Fatal(err)
		}
		if tr.commentsKey != "K-2" || len(got) != 1 {
			t.Errorf("Comments: key=%q ret=%+v", tr.commentsKey, got)
		}
	})

	t.Run("DeleteComment", func(t *testing.T) {
		tr := &waveBTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.DeleteComment(ctx, "K-3", "9"); err != nil {
			t.Fatal(err)
		}
		if tr.delCmtKey != "K-3" || tr.delCmtID != "9" {
			t.Errorf("DeleteComment args: %q %q", tr.delCmtKey, tr.delCmtID)
		}
	})

	t.Run("Links", func(t *testing.T) {
		tr := &waveBTracker{issue: &domain.Issue{Key: "K-4", Links: []domain.IssueLink{{ID: "10", Key: "K-5"}}}}
		svc := &JiraService{tr: tr}
		got, err := svc.Links(ctx, "K-4")
		if err != nil {
			t.Fatal(err)
		}
		if tr.linksKey != "K-4" || len(got) != 1 || got[0].ID != "10" {
			t.Errorf("Links: key=%q ret=%+v", tr.linksKey, got)
		}
	})

	t.Run("DeleteLink", func(t *testing.T) {
		tr := &waveBTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.DeleteLink(ctx, "10"); err != nil {
			t.Fatal(err)
		}
		if tr.delLinkID != "10" {
			t.Errorf("DeleteLink arg: %q", tr.delLinkID)
		}
	})

	t.Run("sentinel propagates", func(t *testing.T) {
		tr := &waveBTracker{err: domain.ErrNotFound}
		svc := &JiraService{tr: tr}
		if _, err := svc.History(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
			t.Error("History did not propagate sentinel")
		}
	})
}
