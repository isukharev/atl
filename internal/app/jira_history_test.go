package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type historyTracker struct {
	domain.Tracker
	snapshot *domain.ChangelogSnapshot
	defs     []domain.FieldDef
	err      error
}

func (t *historyTracker) CompleteChangelog(context.Context, string) (*domain.ChangelogSnapshot, error) {
	return t.snapshot, t.err
}

func (t *historyTracker) Fields(context.Context) ([]domain.FieldDef, error) { return t.defs, t.err }

func TestHistoryFilteredByResolvedFieldAndInclusiveDates(t *testing.T) {
	tracker := &historyTracker{
		defs: []domain.FieldDef{{ID: "customfield_10001", Name: "Delivery Notes", Custom: true}},
		snapshot: &domain.ChangelogSnapshot{Complete: true, Source: "paginated", Total: 3, Entries: []domain.ChangelogEntry{
			{ID: "1", Created: "2026-04-01T00:00:00.000+0000", Items: []domain.ChangelogItem{{Field: "Delivery Notes", FieldID: "customfield_10001", To: "first"}, {Field: "Status", FieldID: "status", To: "Open"}}},
			{ID: "2", Created: "2026-04-30T23:59:59.000+0000", Items: []domain.ChangelogItem{{Field: "Delivery Notes", FieldID: "customfield_10001", From: "first", To: "second"}}},
			{ID: "3", Created: "2026-05-01T00:00:00.000+0000", Items: []domain.ChangelogItem{{Field: "Delivery Notes", FieldID: "customfield_10001", To: "third"}}},
		}},
	}
	result, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{
		Fields: []string{"Delivery Notes"}, Since: "2026-04-01", Until: "2026-04-30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Fetched != 3 || result.Count != 2 || len(result.History[0].Items) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.LastChanges) != 1 || result.LastChanges[0].HistoryID != "2" || result.LastChanges[0].To != "second" {
		t.Fatalf("last_changes=%+v", result.LastChanges)
	}
}

func TestHistoryFilteredRejectsBadBoundariesBeforeRead(t *testing.T) {
	tracker := &historyTracker{err: errors.New("must not read")}
	_, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "yesterday"})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v", err)
	}
	_, err = (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-05-01", Until: "2026-04-01"})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v", err)
	}
}

func TestHistoryFilteredFailsClosedOnUnparseableServerTimeWhenFiltering(t *testing.T) {
	tracker := &historyTracker{snapshot: &domain.ChangelogSnapshot{Entries: []domain.ChangelogEntry{{ID: "1", Created: "not-a-time"}}}}
	_, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-01-01"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
}

func TestJiraHistoryMarkdownEscapesTableValues(t *testing.T) {
	text := JiraHistoryMarkdown(&JiraHistoryResult{Complete: false, Source: "embedded", Total: 2, Fetched: 1, Count: 1, PartialReason: "clipped", History: []domain.ChangelogEntry{{Created: "2026-01-01", Author: "A|B", Items: []domain.ChangelogItem{{Field: "Notes", From: "one\ntwo", To: "done"}}}}})
	if text == "" || !containsAll(text, "Complete: false", "A\\|B", "one two", "partial: clipped") {
		t.Fatalf("text=%q", text)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
