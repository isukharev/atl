package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraQuarterFieldCatalogFailsClosed(t *testing.T) {
	valid := validJiraQuarterFieldCatalogWire(t)
	decoded, err := DecodeJiraQuarterFieldCatalog(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 3 || decoded.Fields[1].ID != "customfield_2" || !decoded.Fields[1].Custom {
		t.Fatalf("catalog=%+v", decoded)
	}
	invalid := map[string][]byte{
		"unknown": mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["backend"] = true }),
		"null":    mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["fields"] = nil }),
		"count":   mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["custom_count"] = 1 }),
		"duplicate id": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["fields"].([]any)[1].(map[string]any)["id"] = "customfield_1"
		}),
		"incomplete": mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["complete"] = false }),
		"trailing":   append(bytes.Clone(valid), []byte("\n{}")...),
	}
	duplicate := bytes.Replace(valid, []byte("\"schema_version\":1"), []byte("\"schema_version\":1,\"schema_version\":1"), 1)
	if bytes.Equal(valid, duplicate) {
		t.Fatal("duplicate mutation did not apply")
	}
	invalid["duplicate"] = duplicate
	invalidUTF8 := bytes.Replace(valid, []byte("\"Epic Link\""), []byte{'"', 0xff, '"'}, 1)
	if bytes.Equal(valid, invalidUTF8) {
		t.Fatal("UTF-8 mutation did not apply")
	}
	invalid["utf8"] = invalidUTF8
	for name, data := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraQuarterFieldCatalog(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid field catalog wire was accepted")
			}
		})
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraQuarterFieldCatalogWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraQuarterFieldCatalog(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact field catalog bound rejected: %v", err)
	}
	if _, err := DecodeJiraQuarterFieldCatalog(bytes.NewReader(append(atLimit, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize field catalog error=%v", err)
	}
}

func TestDecodeJiraQuarterCompactEpicDigestFailsClosed(t *testing.T) {
	valid := validJiraQuarterCompactEpicDigestWire(t)
	decoded, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Epic.Key != "PROJ-1" || decoded.StatusField.LastChange.HistoryID != "history-1" ||
		decoded.HistorySummary.Count != 1 || decoded.Projection.Name != "compact" {
		t.Fatalf("digest=%+v", decoded)
	}
	nonUTC := mutateJiraQuarterWire(t, valid, func(root map[string]any) {
		period := root["period"].(map[string]any)
		period["boundary_time_zone"] = "Europe/Berlin"
		period["since_instant"] = "2026-03-31T22:00:00Z"
		period["until_exclusive_instant"] = "2026-06-30T22:00:00Z"
	})
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(nonUTC)); err != nil {
		t.Fatalf("valid non-UTC digest rejected: %v", err)
	}
	skippedMidnight := mutateJiraQuarterWire(t, valid, func(root map[string]any) {
		period := root["period"].(map[string]any)
		period["quarter"] = "2001-Q2"
		period["since"] = "2001-04-01"
		period["until"] = "2001-06-30"
		period["boundary_time_zone"] = "America/Havana"
		period["since_instant"] = "2001-04-01T05:00:00Z"
		period["until_exclusive_instant"] = "2001-07-01T04:00:00Z"
	})
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(skippedMidnight)); err != nil {
		t.Fatalf("valid skipped-midnight digest rejected: %v", err)
	}
	historyTail := mutateJiraQuarterWire(t, valid, func(root map[string]any) {
		root["sources"].(map[string]any)["history"].(map[string]any)["count"] = 6
		history := root["history_summary"].(map[string]any)
		history["count"] = 6
		history["recent"].([]any)[0].(map[string]any)["id"] = "history-6"
	})
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(historyTail)); err != nil {
		t.Fatalf("valid compact history tail rejected: %v", err)
	}
	nameMatchedHistory := mutateJiraQuarterWire(t, valid, func(root map[string]any) {
		status := root["status_field"].(map[string]any)
		status["name"] = "Quarter Outcome"
		status["last_change"].(map[string]any)["field"] = "Quarter Outcome"
		delete(root["history_summary"].(map[string]any)["recent"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any), "field_id")
	})
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(nameMatchedHistory)); err != nil {
		t.Fatalf("valid name-matched selected history item rejected: %v", err)
	}
	omittedSelectedItem := mutateJiraQuarterWire(t, valid, func(root map[string]any) {
		root["projection"].(map[string]any)["omitted"] = []any{"history_summary.recent.items[remaining]", "history"}
		root["history_summary"].(map[string]any)["recent"].([]any)[0].(map[string]any)["items"] = []any{
			map[string]any{"field": "Summary", "from": "Old", "to": "New"},
			map[string]any{"field": "Priority", "from": "Low", "to": "High"},
			map[string]any{"field": "Assignee", "from": "First", "to": "Second"},
		}
	})
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(omittedSelectedItem)); err != nil {
		t.Fatalf("valid omitted selected history item rejected: %v", err)
	}
	invalid := map[string][]byte{
		"unknown": mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["children"] = map[string]any{} }),
		"null":    mutateJiraQuarterWire(t, valid, func(root map[string]any) { root["status_field"] = nil }),
		"source": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["sources"].(map[string]any)["children"] = map[string]any{"complete": true, "count": 0}
		}),
		"includes": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["includes"] = []any{"identity", "history", "status-field"}
		}),
		"present last change mismatch": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["history_summary"].(map[string]any)["recent"].([]any)[0].(map[string]any)["created"] = "2026-06-28T10:00:00Z"
		}),
		"present last change value mismatch": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["history_summary"].(map[string]any)["recent"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)["to"] = "Planned"
		}),
		"history source count": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["sources"].(map[string]any)["history"].(map[string]any)["count"] = 2
		}),
		"staleness": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["staleness"].(map[string]any)["stale"] = true
		}),
		"projection": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["projection"].(map[string]any)["omitted"] = []any{}
		}),
		"unsupported projection omission": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["projection"].(map[string]any)["omitted"] = []any{"history_summary.recent.items", "history"}
		}),
		"quarter dates": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["period"].(map[string]any)["until"] = "2026-06-29"
		}),
		"quarter instant": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["period"].(map[string]any)["since_instant"] = "2026-04-01T01:00:00Z"
		}),
		"timezone source": mutateJiraQuarterWire(t, valid, func(root map[string]any) {
			root["period"].(map[string]any)["boundary_time_zone_source"] = "host"
		}),
		"trailing": append(bytes.Clone(valid), []byte("\n{}")...),
	}
	duplicate := bytes.Replace(valid, []byte("\"schema_version\":1"), []byte("\"schema_version\":1,\"schema_version\":1"), 1)
	if bytes.Equal(valid, duplicate) {
		t.Fatal("duplicate mutation did not apply")
	}
	invalid["duplicate"] = duplicate
	for name, data := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid compact digest wire was accepted")
			}
		})
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraQuarterEpicDigestWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact compact digest bound rejected: %v", err)
	}
	if _, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(append(atLimit, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize compact digest error=%v", err)
	}
}

func validJiraQuarterFieldCatalogWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraQuarterWire(t, map[string]any{
		"schema_version": 1, "projection": "full", "source": "jira-field-catalog", "complete": true,
		"total": 3, "count": 3, "custom_count": 3, "system_count": 0,
		"fields": []any{
			map[string]any{"id": "customfield_1", "name": "Epic Link", "custom": true, "schema": "any"},
			map[string]any{"id": "customfield_2", "name": "Quarter Outcome", "custom": true, "schema": "string"},
			map[string]any{"id": "customfield_3", "name": "Evidence Page", "custom": true, "schema": "string"},
		},
	})
}

func validJiraQuarterCompactEpicDigestWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraQuarterWire(t, map[string]any{
		"schema_version": 1,
		"period": map[string]any{
			"quarter": "2026-Q2", "since": "2026-04-01", "until": "2026-06-30",
			"boundary_time_zone": "UTC", "boundary_time_zone_source": "jira_current_user",
			"since_instant": "2026-04-01T00:00:00Z", "until_exclusive_instant": "2026-07-01T00:00:00Z",
		},
		"includes": []any{"history", "identity", "status-field"},
		"sources": map[string]any{
			"history":      map[string]any{"complete": true, "count": 1},
			"identity":     map[string]any{"complete": true, "count": 1},
			"status-field": map[string]any{"complete": true, "count": 1},
		},
		"epic": map[string]any{
			"key": "PROJ-1", "summary": "Synthetic epic", "status": "Done", "type": "Epic",
			"updated": "2026-06-30T10:00:00.000+0000", "description": "Synthetic evidence.",
		},
		"status_field": map[string]any{
			"id": "customfield_2", "name": "customfield_2", "value": "Released",
			"last_change": map[string]any{
				"field_id": "customfield_2", "field": "customfield_2", "created": "2026-06-29T10:00:00.000+0000",
				"history_id": "history-1", "to": "Released",
			},
		},
		"staleness": map[string]any{
			"evaluated": true, "stale": false, "status_field_updated": "2026-06-29T10:00:00.000+0000",
			"newer_child_updates": 0, "newer_comments": 0,
			"reasons": []any{"no selected child/comment evidence is newer than the status field"},
		},
		"projection": map[string]any{"name": "compact", "omitted": []any{"history"}, "clipped": []any{}},
		"history_summary": map[string]any{"count": 1, "recent": []any{map[string]any{
			"id": "history-1", "created": "2026-06-29T10:00:00.000+0000",
			"items": []any{map[string]any{"field": "Quarter Outcome", "field_id": "customfield_2", "to": "Released"}},
		}}},
	})
}

func mutateJiraQuarterWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return mustJiraQuarterWire(t, root)
}

func mustJiraQuarterWire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
