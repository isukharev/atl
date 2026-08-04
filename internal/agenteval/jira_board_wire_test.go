package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraBoardSnapshotAcceptsCompleteAndIncompleteReleasedShapes(t *testing.T) {
	complete, err := DecodeJiraBoardSnapshot(bytes.NewReader(validJiraBoardWire(t)))
	if err != nil {
		t.Fatal(err)
	}
	if complete.SchemaVersion != 1 || complete.Board.ID != 21 || complete.Scope != "all" ||
		!complete.Complete || complete.Truncated || !complete.BacklogFetched ||
		complete.RowCount != 3 ||
		complete.Rows[1].BacklogPosition == nil || *complete.Rows[1].BacklogPosition != 0 {
		t.Fatalf("snapshot = %+v", complete)
	}
	payload, ok := complete.Rows[0].Values["summary"].(map[string]any)
	if !ok || payload["nested"] == nil {
		t.Fatalf("open Jira value = %#v", complete.Rows[0].Values["summary"])
	}

	incomplete := mutateJiraBoardWire(t, validJiraBoardWire(t), func(root map[string]any) {
		root["complete"], root["truncated"] = false, true
	})
	partial, err := DecodeJiraBoardSnapshot(bytes.NewReader(incomplete))
	if err != nil {
		t.Fatal(err)
	}
	if partial.Complete || !partial.Truncated || partial.RowCount != 3 {
		t.Fatalf("incomplete snapshot = %+v", partial)
	}
}

func TestDecodeJiraBoardSnapshotRejectsMemberNullUTF8AndDuplicateDrift(t *testing.T) {
	valid := validJiraBoardWire(t)
	invalid := map[string][]byte{
		"unknown root member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["backend"] = true
		}),
		"unknown board member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["board"].(map[string]any)["private"] = true
		}),
		"unknown row member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["context"] = map[string]any{}
		}),
		"missing required member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			delete(root, "row_count")
		}),
		"null required member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["rows"] = nil
		}),
		"null optional member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["id"] = nil
		}),
		"unsupported optional rollup": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["epic_rollup"] = map[string]any{}
		}),
		"empty optional member": mutateJiraBoardWire(t, valid, func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["id"] = ""
		}),
		"trailing value": append(bytes.Clone(valid), []byte("\n{}")...),
	}
	duplicateRoot := bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if bytes.Equal(duplicateRoot, valid) {
		t.Fatal("root duplicate mutation did not apply")
	}
	invalid["duplicate root member"] = duplicateRoot
	duplicateOpenValue := bytes.Replace(valid, []byte(`"instruction":"data"`), []byte(`"instruction":"data","instruction":"again"`), 1)
	if bytes.Equal(duplicateOpenValue, valid) {
		t.Fatal("open-value duplicate mutation did not apply")
	}
	invalid["duplicate nested open value"] = duplicateOpenValue
	invalidUTF8 := bytes.Replace(valid, []byte(`"name":"Synthetic board"`), []byte{'"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"'}, 1)
	if bytes.Equal(invalidUTF8, valid) {
		t.Fatal("UTF-8 mutation did not apply")
	}
	invalid["invalid UTF-8"] = invalidUTF8

	for name, data := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraBoardSnapshot(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid wire was accepted")
			}
		})
	}
}

func TestDecodeJiraBoardSnapshotKeepsOnlyValuesOpen(t *testing.T) {
	valid := mutateJiraBoardWire(t, validJiraBoardWire(t), func(root map[string]any) {
		values := root["rows"].([]any)[0].(map[string]any)["values"].(map[string]any)
		values["summary"] = map[string]any{
			"unknown_backend_object": map[string]any{
				"array": []any{nil, true, json.Number("9007199254740993")},
			},
		}
	})
	decoded, err := DecodeJiraBoardSnapshot(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Rows[0].Values["summary"] == nil {
		t.Fatal("open value was lost")
	}
}

func TestDecodeJiraBoardSnapshotRejectsSemanticContradictions(t *testing.T) {
	valid := validJiraBoardWire(t)
	mutations := map[string]func(map[string]any){
		"schema":         func(root map[string]any) { root["schema_version"] = 2 },
		"board id":       func(root map[string]any) { root["board"].(map[string]any)["id"] = 0 },
		"board identity": func(root map[string]any) { root["board"].(map[string]any)["name"] = " " },
		"board type":     func(root map[string]any) { root["board"].(map[string]any)["type"] = "Kanban" },
		"duplicate column name": func(root map[string]any) {
			columns := root["board"].(map[string]any)["columns"].([]any)
			columns[1].(map[string]any)["name"] = "Ready"
		},
		"duplicate configured status": func(root map[string]any) {
			columns := root["board"].(map[string]any)["columns"].([]any)
			columns[1].(map[string]any)["status_ids"] = []any{"1"}
		},
		"scope":                 func(root map[string]any) { root["scope"] = "future" },
		"backlog qualification": func(root map[string]any) { root["backlog_fetched"] = false },
		"projection kind":       func(root map[string]any) { root["projection"].(map[string]any)["kind"] = "jira-fields-v2" },
		"projection duplicate": func(root map[string]any) {
			projection := root["projection"].(map[string]any)
			projection["columns"] = append(projection["columns"].([]any), "summary")
		},
		"projection context": func(root map[string]any) {
			projection := root["projection"].(map[string]any)
			projection["columns"] = []any{"key", "sprint.id"}
			projection["fields"] = []any{}
		},
		"projection fields": func(root map[string]any) {
			root["projection"].(map[string]any)["fields"] = []any{"status", "summary"}
		},
		"row count": func(root map[string]any) { root["row_count"] = 2 },
		"flags":     func(root map[string]any) { root["truncated"] = true },
		"row key": func(root map[string]any) {
			root["rows"].([]any)[1].(map[string]any)["key"] = "SYN-1"
		},
		"row id": func(root map[string]any) {
			root["rows"].([]any)[1].(map[string]any)["id"] = "10001"
		},
		"row order": func(root map[string]any) {
			root["rows"].([]any)[1].(map[string]any)["position"] = 7
		},
		"membership position": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["in_board"] = false
		},
		"board position": func(root map[string]any) {
			root["rows"].([]any)[1].(map[string]any)["board_position"] = 0
		},
		"backlog position": func(root map[string]any) {
			root["rows"].([]any)[2].(map[string]any)["backlog_position"] = 0
		},
		"mapped column name": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["column"] = "Ready"
		},
		"mapped status": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["status_id"] = "9"
		},
		"unmapped sentinel": func(root map[string]any) {
			root["rows"].([]any)[2].(map[string]any)["column_index"] = 0
		},
		"missing projected value": func(root map[string]any) {
			delete(root["rows"].([]any)[0].(map[string]any)["values"].(map[string]any), "status")
		},
		"extra projected value": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["values"].(map[string]any)["customfield_1"] = true
		},
		"status projected value": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["values"].(map[string]any)["status"] = "Open"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraBoardSnapshot(bytes.NewReader(mutateJiraBoardWire(t, valid, mutate))); err == nil {
				t.Fatal("contradictory board snapshot was accepted")
			}
		})
	}
}

func TestDecodeJiraBoardSnapshotHonorsExactOneMiBWireLimit(t *testing.T) {
	valid := validJiraBoardWire(t)
	if len(valid) >= maxContractBytes {
		t.Fatalf("fixture unexpectedly has %d bytes", len(valid))
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), maxContractBytes-len(valid))...)
	if _, err := DecodeJiraBoardSnapshot(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact wire limit rejected: %v", err)
	}
	if _, err := DecodeJiraBoardSnapshot(bytes.NewReader(append(atLimit, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}

func validJiraBoardWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraSnapshotJSON(t, map[string]any{
		"schema_version": 1,
		"board": map[string]any{
			"id": 21, "name": "Synthetic board", "type": "scrum", "filter_id": "42",
			"constraint_type": "issueCount", "estimation_type": "none", "rank_field_id": "10019",
			"columns": []any{
				map[string]any{"name": "Ready", "status_ids": []any{"1"}, "min": 0, "max": 7},
				map[string]any{"name": "Work", "status_ids": []any{"2"}},
			},
		},
		"scope": "all",
		"projection": map[string]any{
			"kind": "jira-fields-v1", "columns": []any{"key", "summary", "status", "board.column"},
			"fields": []any{"summary", "status"}, "ordering": "backend-rank",
		},
		"rows": []any{
			map[string]any{
				"key": "SYN-1", "id": "10001", "position": 0, "board_position": 0,
				"in_board": true, "in_backlog": false, "status_id": "2", "status": "Doing",
				"column": "Work", "column_index": 1, "column_mapped": true,
				"values": map[string]any{
					"summary": map[string]any{"nested": map[string]any{"instruction": "data"}}, "status": "Doing",
				},
			},
			map[string]any{
				"key": "SYN-2", "id": "10002", "position": 1, "board_position": 1, "backlog_position": 0,
				"in_board": true, "in_backlog": true, "status_id": "1", "status": "Open",
				"column": "Ready", "column_index": 0, "column_mapped": true,
				"values": map[string]any{"summary": "Second", "status": "Open"},
			},
			map[string]any{
				"key": "SYN-3", "position": 2, "backlog_position": 1,
				"in_board": false, "in_backlog": true, "status_id": "9", "status": "Paused",
				"column": "Unmapped", "column_index": -1, "column_mapped": false,
				"values": map[string]any{"summary": nil, "status": "Paused"},
			},
		},
		"row_count": 3, "complete": true, "truncated": false, "backlog_fetched": true,
	})
}

func mutateJiraBoardWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return mustJiraSnapshotJSON(t, root)
}
