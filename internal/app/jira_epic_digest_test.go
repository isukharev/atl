package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type digestTracker struct {
	domain.Tracker
	issue      *domain.Issue
	defs       []domain.FieldDef
	children   []domain.Issue
	comments   []domain.Comment
	history    *domain.ChangelogSnapshot
	searchCall int
}

func (t *digestTracker) GetIssue(context.Context, string, []string) (*domain.Issue, error) {
	return t.issue, nil
}
func (t *digestTracker) Fields(context.Context) ([]domain.FieldDef, error) { return t.defs, nil }
func (t *digestTracker) ListComments(context.Context, string) ([]domain.Comment, error) {
	return t.comments, nil
}
func (t *digestTracker) CompleteChangelog(context.Context, string) (*domain.ChangelogSnapshot, error) {
	return t.history, nil
}
func (t *digestTracker) Changelog(context.Context, string) ([]domain.ChangelogEntry, error) {
	return t.history.Entries, nil
}
func (t *digestTracker) Search(_ context.Context, _ string, _ []string, limit int, cursor string) ([]domain.Issue, string, error) {
	t.searchCall++
	start := 0
	if cursor != "" {
		start = 1
	}
	end := min(start+limit, len(t.children))
	next := ""
	if end < len(t.children) {
		next = "next"
	}
	return append([]domain.Issue(nil), t.children[start:end]...), next, nil
}

type digestConfluence struct{ calls int }

func (d *digestConfluence) PageSection(_ context.Context, _ string, _ ConfluencePageSectionOpts) (*ConfluencePageSectionResult, error) {
	d.calls++
	return &ConfluencePageSectionResult{ID: "9", Heading: "Metrics", Markdown: "## Metrics\n\n42\n", Complete: true}, nil
}

func digestFixture() (*JiraService, *digestTracker) {
	tracker := &digestTracker{
		defs: []domain.FieldDef{
			{ID: "customfield_10001", Name: "Epic Link", Custom: true},
			{ID: "customfield_10002", Name: "Delivery Notes", Custom: true},
			{ID: "customfield_10003", Name: "Definition of Done", Custom: true},
		},
		issue: &domain.Issue{Key: "PROJ-1", Summary: "Deliver capability", Status: "In Progress", Type: "Epic", Body: "Do the work. https://confluence.example.test/pages/viewpage.action?pageId=9", Fields: map[string]any{
			"updated": "2026-04-06T00:00:00.000+0000", "resolution": nil,
			"customfield_10002": "On track", "customfield_10003": "All checks pass",
		}, Links: []domain.IssueLink{{ID: "1", Key: "PROJ-9", Type: "is blocked by", TypeName: "Blocks", Direction: "inward"}}},
		children: []domain.Issue{
			{Key: "PROJ-2", Summary: "First", Status: "Done", Type: "Task", Fields: map[string]any{"updated": "2026-04-03T00:00:00.000+0000"}},
			{Key: "PROJ-3", Summary: "Second", Status: "Open", Type: "Task", Fields: map[string]any{"updated": "2026-03-01T00:00:00.000+0000"}},
		},
		comments: []domain.Comment{{ID: "c1", Created: "2026-04-05T00:00:00.000+0000", Body: "Latest evidence"}},
		history: &domain.ChangelogSnapshot{
			Complete: true, Source: "paginated", Total: 1,
			Entries: []domain.ChangelogEntry{{
				ID: "h1", Created: "2026-04-01T00:00:00.000+0000",
				Items: []domain.ChangelogItem{{Field: "Delivery Notes", FieldID: "customfield_10002", To: "On track"}},
			}},
		},
	}
	return &JiraService{tr: tracker}, tracker
}

func TestJiraEpicDigestJoinsDatedEvidence(t *testing.T) {
	service, tracker := digestFixture()
	conf := &digestConfluence{}
	result, err := service.EpicDigest(context.Background(), "PROJ-1", JiraEpicDigestOpts{
		Quarter: "2026-Q2", StatusField: "Delivery Notes", DoDField: "Definition of Done", EpicField: "customfield_10001",
		ExpandConfluence: 1, ConfluenceHeading: "Metrics", Confluence: conf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Period.Since != "2026-04-01" || result.Period.Until != "2026-06-30" || result.StatusField == nil || result.StatusField.LastChange == nil || result.DoDField == nil {
		t.Fatalf("result=%+v", result)
	}
	if result.Children == nil || result.Children.ByStatus["Done"] != 1 || result.Children.ByStatus["Open"] != 1 || result.Children.UpdatedInPeriod != 1 || !result.Sources["children"].Complete {
		t.Fatalf("children=%+v source=%+v", result.Children, result.Sources["children"])
	}
	if !result.Staleness.Evaluated || !result.Staleness.Stale || result.Staleness.NewerChildUpdates != 1 || result.Staleness.NewerComments != 1 {
		t.Fatalf("staleness=%+v", result.Staleness)
	}
	if len(result.Blockers) != 1 || len(result.Refs) != 1 || len(result.Confluence) != 1 || conf.calls != 1 || tracker.searchCall != 1 {
		t.Fatalf("blockers=%v refs=%v confluence=%v calls=%d/%d", result.Blockers, result.Refs, result.Confluence, conf.calls, tracker.searchCall)
	}
}

func TestJiraEpicDigestValidatesPeriodAndIncludes(t *testing.T) {
	service, _ := digestFixture()
	for _, opts := range []JiraEpicDigestOpts{
		{Quarter: "2026-Q5"}, {Quarter: "2026-Q2", Since: "2026-01-01", Until: "2026-02-01"},
		{Since: "2026-01-01"}, {Include: []string{"narrative"}}, {ExpandConfluence: 1}, {ChildLimit: -1},
	} {
		if _, err := service.EpicDigest(context.Background(), "PROJ-1", opts); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("opts=%+v err=%v", opts, err)
		}
	}
}

func TestJiraEpicDigestCapsOptionalEvidenceExplicitly(t *testing.T) {
	service, tracker := digestFixture()
	tracker.comments = append(tracker.comments, domain.Comment{ID: "c2"})
	result, err := service.EpicDigest(context.Background(), "PROJ-1", JiraEpicDigestOpts{EpicField: "customfield_10001", CommentLimit: 1, ChildLimit: 1, Include: []string{"children,comments"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["comments"].Complete || result.Sources["children"].Complete || len(result.Comments) != 1 || len(result.Children.List.Rows) != 1 {
		t.Fatalf("sources=%+v", result.Sources)
	}
	if !strings.Contains(JiraEpicDigestMarkdown(result), "Children by status") {
		t.Fatal("text digest omitted children")
	}
}
