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
	if result.Summary.HistoryCount != 2 || result.Summary.ItemCount != 2 || !result.Summary.CountMatchesHistory || !result.Summary.FetchedMatchesTotal || !result.Summary.ChronologicalComparable || result.Summary.ChronologicalAscending == nil || !*result.Summary.ChronologicalAscending {
		t.Fatalf("summary=%+v", result.Summary)
	}
	if result.Summary.DistinctItemFieldCount != 1 || len(result.Summary.Fields) != 1 || result.Summary.Fields[0].Field != "Delivery Notes" || result.Summary.Fields[0].Count != 2 {
		t.Fatalf("summary fields=%+v", result.Summary.Fields)
	}
	if tracker.timeZoneCalls != 1 || result.Filters.BoundaryTimeZone != "UTC" || result.Filters.SinceInstant != "2026-04-01T00:00:00Z" || result.Filters.UntilExclusiveInstant != "2026-05-01T00:00:00Z" {
		t.Fatalf("timezone calls=%d filters=%+v", tracker.timeZoneCalls, result.Filters)
	}
}

func TestSummarizeJiraHistoryReportsCardinalityConsistencyAndUnknownOrdering(t *testing.T) {
	result := &JiraHistoryResult{
		Total: 5, Fetched: 2, Count: 3,
		History: []domain.ChangelogEntry{
			{ID: "duplicate", Author: "Jane", Created: "2026-04-02T10:00:00Z", Items: []domain.ChangelogItem{
				{Field: "Status", FieldID: "status", From: "Open", To: "Done"},
				{Field: "priority", FieldID: "priority", To: "High"},
			}},
			{ID: "duplicate", Created: "not-a-time", Items: []domain.ChangelogItem{{FieldID: "assignee", From: "Jane"}}},
		},
	}

	summary := summarizeJiraHistory(result)
	if summary.HistoryCount != 2 || summary.HistoryIDNonemptyCount != 2 || summary.HistoryIDMissingCount != 0 ||
		summary.HistoryIDsUnique || summary.HistoryNonemptyIDsUnique {
		t.Fatalf("identity summary=%+v", summary)
	}
	if summary.AuthorNonemptyCount != 1 || summary.TimestampNonemptyCount != 2 || summary.ChronologicalComparable || summary.ChronologicalAscending != nil {
		t.Fatalf("ordering summary=%+v", summary)
	}
	if summary.EntriesWithItems != 2 || summary.MultiItemEntryCount != 1 || summary.ItemCount != 3 || summary.ItemFieldNonemptyCount != 2 {
		t.Fatalf("entry summary=%+v", summary)
	}
	if summary.ItemsWithFromCount != 2 || summary.ItemsWithToCount != 2 || summary.StatusItemCount != 1 || summary.DistinctItemFieldCount != 3 {
		t.Fatalf("item summary=%+v", summary)
	}
	if summary.CountMatchesHistory || summary.FetchedMatchesTotal {
		t.Fatalf("consistency summary=%+v", summary)
	}
	if len(summary.Fields) != 3 || summary.Fields[0].FieldID != "assignee" || summary.Fields[1].Field != "priority" || summary.Fields[2].Field != "Status" || summary.Fields[2].WithFrom != 1 || summary.Fields[2].WithTo != 1 {
		t.Fatalf("field summary=%+v", summary.Fields)
	}
}

func TestSummarizeJiraHistoryEmptyHistoryIsComparableAndAscending(t *testing.T) {
	summary := summarizeJiraHistory(&JiraHistoryResult{})
	if !summary.HistoryIDsUnique || !summary.HistoryNonemptyIDsUnique || !summary.ChronologicalComparable || summary.ChronologicalAscending == nil || !*summary.ChronologicalAscending || summary.Fields == nil {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSummarizeJiraHistorySeparatesMissingAndDuplicateNonemptyIDs(t *testing.T) {
	summary := summarizeJiraHistory(&JiraHistoryResult{History: []domain.ChangelogEntry{
		{}, {}, {ID: "duplicate"}, {ID: "duplicate"},
	}})
	if summary.HistoryIDNonemptyCount != 2 || summary.HistoryIDMissingCount != 2 ||
		summary.HistoryIDsUnique || summary.HistoryNonemptyIDsUnique {
		t.Fatalf("identity summary=%+v", summary)
	}

	missingOnly := summarizeJiraHistory(&JiraHistoryResult{History: []domain.ChangelogEntry{{}, {}}})
	if missingOnly.HistoryIDMissingCount != 2 || missingOnly.HistoryIDsUnique || !missingOnly.HistoryNonemptyIDsUnique {
		t.Fatalf("missing-only identity summary=%+v", missingOnly)
	}
}

func TestSummarizeJiraHistoryReportsDescendingAndCoalescesNameOnlyFields(t *testing.T) {
	summary := summarizeJiraHistory(&JiraHistoryResult{Count: 2, History: []domain.ChangelogEntry{
		{ID: "2", Created: "2026-04-02T10:00:00Z", Items: []domain.ChangelogItem{{Field: "Priority", To: "High"}}},
		{ID: "1", Created: "2026-04-01T10:00:00Z", Items: []domain.ChangelogItem{{Field: "priority", From: "Low"}}},
	}})
	if !summary.ChronologicalComparable || summary.ChronologicalAscending == nil || *summary.ChronologicalAscending {
		t.Fatalf("ordering summary=%+v", summary)
	}
	if summary.DistinctItemFieldCount != 1 || len(summary.Fields) != 1 || summary.Fields[0].Count != 2 || summary.Fields[0].WithFrom != 1 || summary.Fields[0].WithTo != 1 {
		t.Fatalf("field summary=%+v", summary.Fields)
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

func TestHistoryCalendarBoundariesCoverMidnightGapsFoldsAndSkippedNextDate(t *testing.T) {
	for _, tc := range []struct {
		name, zone, date, since, until string
	}{
		{"midnight gap Havana", "America/Havana", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"midnight fold Havana", "America/Havana", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
		{"midnight gap Santiago", "America/Santiago", "2026-09-06", "2026-09-06T04:00:00Z", "2026-09-07T03:00:00Z"},
		{"next date skipped Apia", "Pacific/Apia", "2011-12-29", "2011-12-29T10:00:00Z", "2011-12-30T10:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := &historyTracker{timeZone: tc.zone, snapshot: &domain.ChangelogSnapshot{Complete: true}}
			result, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: tc.date, Until: tc.date})
			if err != nil {
				t.Fatal(err)
			}
			if tracker.timeZoneCalls != 1 || result.Filters.SinceInstant != tc.since || result.Filters.UntilExclusiveInstant != tc.until {
				t.Fatalf("calls=%d filters=%+v", tracker.timeZoneCalls, result.Filters)
			}
		})
	}
}

func TestHistoryCalendarBoundaryRejectsFullySkippedCivilDate(t *testing.T) {
	tracker := &historyTracker{timeZone: "Pacific/Apia", snapshot: &domain.ChangelogSnapshot{Complete: true}}
	_, err := (&JiraService{tr: tracker}).HistoryFiltered(context.Background(), "PROJ-1", JiraHistoryOpts{Since: "2011-12-30"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "has no real instant") || tracker.timeZoneCalls != 1 {
		t.Fatalf("calls=%d err=%v", tracker.timeZoneCalls, err)
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
