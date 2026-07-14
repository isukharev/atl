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
	snapshot      *domain.ChangelogSnapshot
	defs          []domain.FieldDef
	err           error
	timeZone      string
	timeZoneCalls int
}

func (t *historyTracker) CompleteChangelog(context.Context, string) (*domain.ChangelogSnapshot, error) {
	return t.snapshot, t.err
}

func (t *historyTracker) Fields(context.Context) ([]domain.FieldDef, error) { return t.defs, t.err }

func (t *historyTracker) CurrentUserTimeZone(context.Context) (string, error) {
	t.timeZoneCalls++
	if t.timeZone == "" {
		return "UTC", t.err
	}
	return t.timeZone, t.err
}

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
	if tracker.timeZoneCalls != 1 || result.Filters.BoundaryTimeZone != "UTC" || result.Filters.SinceInstant != "2026-04-01T00:00:00Z" || result.Filters.UntilExclusiveInstant != "2026-05-01T00:00:00Z" {
		t.Fatalf("timezone calls=%d filters=%+v", tracker.timeZoneCalls, result.Filters)
	}
}

func TestHistoryCalendarBoundariesFollowCurrentUserTimeZoneAndDST(t *testing.T) {
	tracker := &historyTracker{
		timeZone: "America/New_York",
		snapshot: &domain.ChangelogSnapshot{Complete: true, Source: "paginated", Total: 3, Entries: []domain.ChangelogEntry{
			{ID: "before", Created: "2026-03-08T04:59:59Z", Items: []domain.ChangelogItem{{Field: "Status"}}},
			{ID: "start", Created: "2026-03-08T05:00:00Z", Items: []domain.ChangelogItem{{Field: "Status"}}},
			{ID: "end", Created: "2026-03-09T03:59:59Z", Items: []domain.ChangelogItem{{Field: "Status"}}},
		}},
	}
	result, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-03-08", Until: "2026-03-08"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.History) != 2 || result.History[0].ID != "start" || result.History[1].ID != "end" {
		t.Fatalf("history=%+v", result.History)
	}
	if result.Filters.SinceInstant != "2026-03-08T05:00:00Z" || result.Filters.UntilExclusiveInstant != "2026-03-09T04:00:00Z" {
		t.Fatalf("filters=%+v", result.Filters)
	}
}

func TestHistoryExplicitInstantsNeedNoUserTimeZoneRead(t *testing.T) {
	tracker := &historyTracker{snapshot: &domain.ChangelogSnapshot{Complete: true}}
	result, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{
		Since: "2026-04-01T00:00:00+03:00", Until: "2026-04-02T00:00:00+03:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.timeZoneCalls != 0 || result.Filters.BoundaryTimeZone != "" || result.Filters.SinceInstant != "2026-03-31T21:00:00Z" || result.Filters.UntilExclusiveInstant != "2026-04-01T21:00:00.000000001Z" {
		t.Fatalf("calls=%d filters=%+v", tracker.timeZoneCalls, result.Filters)
	}
}

func TestHistoryCalendarBoundariesRejectInvalidUserTimeZone(t *testing.T) {
	tracker := &historyTracker{timeZone: "Mars/Olympus", snapshot: &domain.ChangelogSnapshot{Complete: true}}
	_, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-04-01"})
	if !errors.Is(err, domain.ErrCheckFailed) || tracker.timeZoneCalls != 1 {
		t.Fatalf("calls=%d err=%v", tracker.timeZoneCalls, err)
	}
}

func TestHistoryObservesChangedUserTimeZoneOnNextCommand(t *testing.T) {
	tracker := &historyTracker{timeZone: "UTC", snapshot: &domain.ChangelogSnapshot{Complete: true}}
	service := &JiraService{tr: tracker}
	first, err := service.HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-04-01"})
	if err != nil {
		t.Fatal(err)
	}
	tracker.timeZone = "Europe/Moscow"
	second, err := service.HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2026-04-01"})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.timeZoneCalls != 2 || first.Filters.SinceInstant != "2026-04-01T00:00:00Z" || second.Filters.SinceInstant != "2026-03-31T21:00:00Z" {
		t.Fatalf("calls=%d first=%+v second=%+v", tracker.timeZoneCalls, first.Filters, second.Filters)
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

func TestHistorySelectedLatestChangeFailsClosedOnUnparseableServerTime(t *testing.T) {
	tracker := &historyTracker{
		defs: []domain.FieldDef{{ID: "customfield_10001", Name: "Delivery Notes"}},
		snapshot: &domain.ChangelogSnapshot{Complete: true, Entries: []domain.ChangelogEntry{
			{ID: "valid", Created: "2026-04-01", Items: []domain.ChangelogItem{{FieldID: "customfield_10001", Field: "Delivery Notes", To: "first"}}},
			{ID: "invalid", Created: "not-a-time", Items: []domain.ChangelogItem{{FieldID: "customfield_10001", Field: "Delivery Notes", To: "unknown"}}},
		}},
	}
	_, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Fields: []string{"Delivery Notes"}})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "cannot determine latest change") {
		t.Fatalf("err=%v", err)
	}
}

func TestHistorySelectedLatestChangeIgnoresBadTimeOnUnrelatedField(t *testing.T) {
	tracker := &historyTracker{
		defs: []domain.FieldDef{{ID: "customfield_10001", Name: "Delivery Notes"}},
		snapshot: &domain.ChangelogSnapshot{Complete: true, Entries: []domain.ChangelogEntry{
			{ID: "valid", Created: "2026-04-01", Items: []domain.ChangelogItem{{FieldID: "customfield_10001", Field: "Delivery Notes", To: "first"}}},
			{ID: "unrelated", Created: "not-a-time", Items: []domain.ChangelogItem{{FieldID: "status", Field: "Status", To: "Done"}}},
		}},
	}
	result, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Fields: []string{"Delivery Notes"}})
	if err != nil || len(result.LastChanges) != 1 || result.LastChanges[0].HistoryID != "valid" {
		t.Fatalf("result=%+v err=%v", result, err)
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
