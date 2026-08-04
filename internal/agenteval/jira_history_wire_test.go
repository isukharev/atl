package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraHistorySummaryAcceptsReleasedPrimaryAndHoldout(t *testing.T) {
	for _, test := range []struct {
		name string
		view JiraHistorySummaryView
	}{
		{name: "selected primary", view: validJiraHistoryPrimary()},
		{name: "partial holdout", view: validJiraHistoryHoldout()},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, test.view)))
			if err != nil {
				t.Fatalf("decode valid projection: %v", err)
			}
			if got.Key != test.view.Key || got.Complete != test.view.Complete ||
				got.Count != test.view.Count || len(got.Summary.Fields) != len(test.view.Summary.Fields) ||
				len(got.LastChanges) != len(test.view.LastChanges) {
				t.Fatalf("projection drifted: %+v", got)
			}
		})
	}
}

func TestDecodeJiraHistorySummaryRejectsWireDrift(t *testing.T) {
	valid := jiraHistoryEncode(t, validJiraHistoryPrimary())
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraHistorySummaryWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact byte bound was rejected: %v", err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "oversized", data: append(atLimit, ' ')},
		{name: "missing root", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { delete(doc, "summary") })},
		{name: "unknown root", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["history"] = []any{} })},
		{name: "null root", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["filters"] = nil })},
		{name: "unknown filter", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["filters"].(map[string]any)["private"] = true })},
		{name: "unknown selected field", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			doc["filters"].(map[string]any)["fields"].([]any)[0].(map[string]any)["private"] = true
		})},
		{name: "missing selected field id", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			delete(doc["filters"].(map[string]any)["fields"].([]any)[0].(map[string]any), "id")
		})},
		{name: "empty selected field schema", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			doc["filters"].(map[string]any)["fields"].([]any)[0].(map[string]any)["schema"] = ""
		})},
		{name: "null fields", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["filters"].(map[string]any)["fields"] = nil })},
		{name: "missing bucket field", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			delete(doc["summary"].(map[string]any)["fields"].([]any)[0].(map[string]any), "field")
		})},
		{name: "empty present bucket field id", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			doc["summary"].(map[string]any)["fields"].([]any)[0].(map[string]any)["field_id"] = ""
		})},
		{name: "null bucket", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["summary"].(map[string]any)["fields"].([]any)[0] = nil })},
		{name: "null last changes", data: jiraHistoryMutate(t, valid, func(doc map[string]any) { doc["last_changes"] = nil })},
		{name: "missing last change history id", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			delete(doc["last_changes"].([]any)[0].(map[string]any), "history_id")
		})},
		{name: "null last change history id", data: jiraHistoryMutate(t, valid, func(doc map[string]any) {
			doc["last_changes"].([]any)[0].(map[string]any)["history_id"] = nil
		})},
		{name: "duplicate root", data: []byte(strings.Replace(string(valid), `"count":`, `"count":2,"count":`, 1))},
		{name: "recursive duplicate", data: []byte(strings.Replace(string(valid), `"history_count":`, `"history_count":2,"history_count":`, 1))},
		{name: "trailing", data: append(bytes.Clone(valid), []byte(`{}`)...)},
		{name: "invalid UTF-8", data: append(bytes.Clone(valid[:len(valid)-2]), 0xff, '}', '}')},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeJiraHistorySummary(bytes.NewReader(test.data)); err == nil {
				t.Fatal("invalid history wire was accepted")
			} else if test.name == "recursive duplicate" && !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("recursive duplicate bypassed duplicate-key validation: %v", err)
			}
		})
	}
}

func TestDecodeJiraHistorySummaryRejectsReconciliationDrift(t *testing.T) {
	valid := jiraHistoryEncode(t, validJiraHistoryPrimary())
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "negative root count", mutate: func(doc map[string]any) { doc["total"] = -1 }},
		{name: "count exceeds fetched", mutate: func(doc map[string]any) { doc["count"] = 4 }},
		{name: "complete with partial reason", mutate: func(doc map[string]any) { doc["partial_reason"] = "partial" }},
		{name: "incomplete without partial reason", mutate: func(doc map[string]any) { doc["complete"] = false }},
		{name: "empty optional filter", mutate: func(doc map[string]any) { doc["filters"].(map[string]any)["until"] = "" }},
		{name: "since without instant", mutate: func(doc map[string]any) { delete(doc["filters"].(map[string]any), "since_instant") }},
		{name: "timezone without source", mutate: func(doc map[string]any) { delete(doc["filters"].(map[string]any), "boundary_time_zone_source") }},
		{name: "wrong timezone source", mutate: func(doc map[string]any) { doc["filters"].(map[string]any)["boundary_time_zone_source"] = "host" }},
		{name: "duplicate selected fields", mutate: func(doc map[string]any) {
			fields := doc["filters"].(map[string]any)["fields"].([]any)
			doc["filters"].(map[string]any)["fields"] = append(fields, fields[0])
		}},
		{name: "count differs from history count", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["history_count"] = 1 }},
		{name: "false count reconciliation", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["count_matches_history"] = false }},
		{name: "wrong fetched total fact", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["fetched_matches_total"] = false }},
		{name: "complete despite fetched total mismatch", mutate: func(doc map[string]any) {
			doc["total"] = 4
			doc["summary"].(map[string]any)["fetched_matches_total"] = false
		}},
		{name: "negative summary count", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["item_count"] = -1 }},
		{name: "id inventory mismatch", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["history_id_missing_count"] = 1 }},
		{name: "comparable with null ascending", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["chronological_ascending"] = nil }},
		{name: "incomparable with ascending", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["chronological_comparable"] = false }},
		{name: "distinct bucket mismatch", mutate: func(doc map[string]any) { doc["summary"].(map[string]any)["distinct_item_field_count"] = 2 }},
		{name: "bucket count exceeds items", mutate: func(doc map[string]any) {
			doc["summary"].(map[string]any)["fields"].([]any)[0].(map[string]any)["count"] = 4
		}},
		{name: "bucket outside selection", mutate: func(doc map[string]any) {
			doc["summary"].(map[string]any)["fields"].([]any)[0].(map[string]any)["field_id"] = "status"
		}},
		{name: "last change outside selection", mutate: func(doc map[string]any) { doc["last_changes"].([]any)[0].(map[string]any)["field_id"] = "status" }},
		{name: "last changes without selected fields", mutate: func(doc map[string]any) { delete(doc["filters"].(map[string]any), "fields") }},
		{name: "empty present last changes", mutate: func(doc map[string]any) { doc["last_changes"] = []any{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := jiraHistoryMutate(t, valid, test.mutate)
			if _, err := DecodeJiraHistorySummary(bytes.NewReader(data)); err == nil {
				t.Fatal("reconciliation drift was accepted")
			}
		})
	}
}

func TestDecodeJiraHistorySummaryAcceptsChronologicalTriState(t *testing.T) {
	for _, ascending := range []*bool{jiraHistoryBool(true), jiraHistoryBool(false), nil} {
		view := validJiraHistoryHoldout()
		view.Summary.ChronologicalAscending = ascending
		view.Summary.ChronologicalComparable = ascending != nil
		if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err != nil {
			t.Fatalf("valid chronological state %v was rejected: %v", ascending, err)
		}
	}
}

func TestDecodeJiraHistorySummaryReconcilesSelectedFieldByID(t *testing.T) {
	view := validJiraHistoryPrimary()
	if view.Filters.Fields[0].Name == view.Summary.Fields[0].Field {
		t.Fatal("fixture must distinguish the selected technical name from the summary display field")
	}
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err != nil {
		t.Fatalf("same field_id with differing display field was rejected: %v", err)
	}
}

func TestDecodeJiraHistorySummaryAcceptsFetchedAboveAdvertisedTotal(t *testing.T) {
	view := validJiraHistoryHoldout()
	view.Total = 2
	view.Fetched = 3
	view.PartialReason = "Jira changelog returned more entries than advertised"
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err != nil {
		t.Fatalf("legitimate partial fetched-above-total result was rejected: %v", err)
	}
}

func TestDecodeJiraHistorySummaryAcceptsMissingLastChangeHistoryID(t *testing.T) {
	view := validJiraHistoryPrimary()
	view.LastChanges[0].HistoryID = ""
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err != nil {
		t.Fatalf("required but empty latest-change history_id was rejected: %v", err)
	}
}

func TestDecodeJiraHistorySummaryReconcilesIDLessBucketTechnicalField(t *testing.T) {
	view := validJiraHistoryPrimary()
	view.Summary.Fields[0].FieldID = ""
	view.Summary.Fields[0].Field = "CUSTOMFIELD_20001"
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err != nil {
		t.Fatalf("id-less bucket technical field was not matched to selected id: %v", err)
	}
}

func TestDecodeJiraHistorySummaryRejectsBucketAndLastChangeOrderingDrift(t *testing.T) {
	holdout := jiraHistoryEncode(t, validJiraHistoryHoldout())
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "reversed buckets", mutate: func(doc map[string]any) {
			fields := doc["summary"].(map[string]any)["fields"].([]any)
			fields[0], fields[1] = fields[1], fields[0]
		}},
		{name: "duplicate bucket identity", mutate: func(doc map[string]any) {
			fields := doc["summary"].(map[string]any)["fields"].([]any)
			fields[1].(map[string]any)["field_id"] = fields[0].(map[string]any)["field_id"]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryMutate(t, holdout, test.mutate))); err == nil {
				t.Fatal("field bucket ordering drift was accepted")
			}
		})
	}

	view := validJiraHistoryPrimary()
	view.Filters.Fields = append(view.Filters.Fields,
		JiraHistorySelectedField{ID: "status", Name: "status"})
	view.LastChanges = append(view.LastChanges,
		JiraHistoryLastChange{FieldID: "status", Field: "status", Created: "2026-01-21T10:00:00Z"})
	view.LastChanges[0], view.LastChanges[1] = view.LastChanges[1], view.LastChanges[0]
	if _, err := DecodeJiraHistorySummary(bytes.NewReader(jiraHistoryEncode(t, view))); err == nil {
		t.Fatal("last_changes selected-field ordering drift was accepted")
	}
}

func validJiraHistoryPrimary() JiraHistorySummaryView {
	return JiraHistorySummaryView{
		Key: "QZ-42", Complete: true, Source: "jira-rest-v3", Total: 3, Fetched: 3, Count: 2,
		Filters: JiraHistoryFiltersView{
			Fields: []JiraHistorySelectedField{{ID: "customfield_20001", Name: "customfield_20001", Custom: true, Schema: "number"}},
			Since:  "2026-01-01", Until: "2026-01-31", BoundaryTimeZone: "Europe/Berlin",
			BoundaryTimeZoneSource: "jira_current_user", SinceInstant: "2025-12-31T23:00:00Z",
			UntilExclusiveInstant: "2026-01-31T23:00:00Z",
		},
		Summary: JiraHistorySummaryFacts{
			HistoryCount: 2, HistoryIDNonemptyCount: 2, HistoryIDsUnique: true,
			HistoryNonemptyIDsUnique: true, AuthorNonemptyCount: 2, TimestampNonemptyCount: 2,
			ChronologicalComparable: true, ChronologicalAscending: jiraHistoryBool(true),
			EntriesWithItems: 2, ItemCount: 2, ItemFieldNonemptyCount: 2, DistinctItemFieldCount: 1,
			ItemsWithFromCount: 1, ItemsWithToCount: 2, CountMatchesHistory: true, FetchedMatchesTotal: true,
			Fields: []JiraHistoryFieldBucket{{FieldID: "customfield_20001", Field: "Forecast", Count: 2, WithFrom: 1, WithTo: 2}},
		},
		LastChanges: []JiraHistoryLastChange{{
			FieldID: "customfield_20001", Field: "customfield_20001", Created: "2026-01-20T10:00:00Z",
			HistoryID: "1002", From: "3", To: "5",
		}},
	}
}

func validJiraHistoryHoldout() JiraHistorySummaryView {
	return JiraHistorySummaryView{
		Key: "RV-9", Complete: false, Source: "jira-rest-v2", Total: 5, Fetched: 3, Count: 3,
		PartialReason: "pagination made no progress", Filters: JiraHistoryFiltersView{},
		Summary: JiraHistorySummaryFacts{
			HistoryCount: 3, HistoryIDNonemptyCount: 2, HistoryIDMissingCount: 1,
			HistoryIDsUnique: true, HistoryNonemptyIDsUnique: true, AuthorNonemptyCount: 2,
			TimestampNonemptyCount: 2, ChronologicalComparable: false,
			EntriesWithItems: 2, MultiItemEntryCount: 1, ItemCount: 3, ItemFieldNonemptyCount: 3,
			DistinctItemFieldCount: 2, ItemsWithFromCount: 2, ItemsWithToCount: 3, StatusItemCount: 2,
			CountMatchesHistory: true, FetchedMatchesTotal: false,
			Fields: []JiraHistoryFieldBucket{
				{FieldID: "priority", Field: "Priority", Count: 1, WithFrom: 1, WithTo: 1},
				{FieldID: "status", Field: "status", Count: 2, WithFrom: 1, WithTo: 2},
			},
		},
	}
}

func jiraHistoryEncode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jiraHistoryMutate(t *testing.T, input []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(input, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	return jiraHistoryEncode(t, doc)
}

func jiraHistoryBool(value bool) *bool { return &value }
