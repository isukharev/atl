package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/agenteval"
)

func TestJiraHistoryBenchmarkFixturesMatchSummaryContract(t *testing.T) {
	ascending := true
	tests := []struct {
		name          string
		directory     string
		key           string
		opts          JiraHistoryOpts
		total         int
		fetched       int
		count         int
		complete      bool
		partialReason string
		expectedGETs  int
		expectedDupes int
		summary       JiraHistorySummary
		lastChanges   []JiraFieldLastChange
	}{
		{
			name: "filtered complete primary", directory: "jira-history-summary", key: "QZ-42",
			opts:  JiraHistoryOpts{Fields: []string{"customfield_20001"}},
			total: 4, fetched: 4, count: 3, complete: true, expectedGETs: 1,
			summary: JiraHistorySummary{
				HistoryCount: 3, HistoryIDNonemptyCount: 2, HistoryIDMissingCount: 1,
				HistoryIDsUnique: false, HistoryNonemptyIDsUnique: false,
				AuthorNonemptyCount: 2, TimestampNonemptyCount: 3,
				ChronologicalComparable: true, ChronologicalAscending: &ascending,
				EntriesWithItems: 3, MultiItemEntryCount: 1, ItemCount: 4,
				ItemFieldNonemptyCount: 4, DistinctItemFieldCount: 1,
				ItemsWithFromCount: 3, ItemsWithToCount: 4, StatusItemCount: 0,
				CountMatchesHistory: true, FetchedMatchesTotal: true,
				Fields: []JiraHistoryFieldSummary{{
					FieldID: "customfield_20001", Field: "Forecast",
					Count: 4, WithFrom: 3, WithTo: 4,
				}},
			},
			lastChanges: []JiraFieldLastChange{{
				FieldID: "customfield_20001", Field: "customfield_20001",
				Created: "2026-06-03T09:00:00.000+0000", HistoryID: "801",
				From: "9", To: "10",
			}},
		},
		{
			name: "partial non-comparable holdout", directory: "jira-history-summary-holdout", key: "RV-9",
			total: 5, fetched: 3, count: 3, complete: false,
			partialReason: "Jira changelog pagination made no forward progress",
			expectedGETs:  2, expectedDupes: 0,
			summary: JiraHistorySummary{
				HistoryCount: 3, HistoryIDNonemptyCount: 3, HistoryIDMissingCount: 0,
				HistoryIDsUnique: true, HistoryNonemptyIDsUnique: true,
				AuthorNonemptyCount: 2, TimestampNonemptyCount: 3,
				ChronologicalComparable: false, ChronologicalAscending: nil,
				EntriesWithItems: 3, MultiItemEntryCount: 2, ItemCount: 5,
				ItemFieldNonemptyCount: 5, DistinctItemFieldCount: 4,
				ItemsWithFromCount: 4, ItemsWithToCount: 4, StatusItemCount: 1,
				CountMatchesHistory: true, FetchedMatchesTotal: false,
				Fields: []JiraHistoryFieldSummary{
					{FieldID: "customfield_30001", Field: "Risk", Count: 1, WithFrom: 0, WithTo: 1},
					{FieldID: "customfield_30002", Field: "Risk", Count: 2, WithFrom: 2, WithTo: 1},
					{FieldID: "status", Field: "Status", Count: 1, WithFrom: 1, WithTo: 1},
					{FieldID: "", Field: "Risk", Count: 1, WithFrom: 1, WithTo: 1},
				},
			},
			lastChanges: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixturePath := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json")
			file, err := os.Open(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := agenteval.DecodeMockFixture(file)
			closeErr := file.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := agenteval.StartMockBackend(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()

			tracker := jira.New(backend.Environment()["ATL_JIRA_URL"], "synthetic-token", "test")
			service := &JiraService{tr: tracker}
			result, err := service.HistoryFiltered(context.Background(), test.key, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			if result.Key != test.key || result.Source != "paginated" ||
				result.Total != test.total || result.Fetched != test.fetched ||
				result.Count != test.count || result.Complete != test.complete ||
				result.PartialReason != test.partialReason {
				t.Fatalf("provenance=%+v", result)
			}
			if !reflect.DeepEqual(result.Summary, test.summary) {
				t.Fatalf("summary=%+v want=%+v", result.Summary, test.summary)
			}
			if !reflect.DeepEqual(result.LastChanges, test.lastChanges) {
				t.Fatalf("last_changes=%+v want=%+v", result.LastChanges, test.lastChanges)
			}
			methods, unexpected, duplicates := backend.Summary()
			if unexpected != 0 || duplicates != test.expectedDupes ||
				len(methods) != 1 || methods["GET"] != test.expectedGETs {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}
