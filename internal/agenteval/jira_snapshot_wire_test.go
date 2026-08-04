package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraSnapshotWiresAcceptReleasedShapesAndArbitraryValues(t *testing.T) {
	list, err := DecodeJiraSnapshotIssueList(bytes.NewReader(validJiraSnapshotIssueListWire(t)))
	if err != nil {
		t.Fatal(err)
	}
	if list.SchemaVersion != 1 || list.Source.Kind != "jql" || list.Page.Count != 3 ||
		list.Rows[0].Values["nullable"] != nil || list.Projection.View != "explicit" {
		t.Fatalf("issue list = %+v", list)
	}
	object, ok := list.Rows[0].Values["payload"].(map[string]any)
	if !ok || object["nested"] == nil {
		t.Fatalf("arbitrary row value = %#v", list.Rows[0].Values["payload"])
	}

	field, err := DecodeJiraSnapshotFieldEvidence(bytes.NewReader(validJiraSnapshotFieldWire(t)))
	if err != nil {
		t.Fatal(err)
	}
	value, ok := field.Value.(map[string]any)
	if !ok || value["kind"] != "named" || field.EmittedValueBytes != len(mustJiraSnapshotJSON(t, field.Value)) {
		t.Fatalf("field evidence = %+v", field)
	}

	nullField := mutateJiraSnapshotWire(t, validJiraSnapshotFieldWire(t), func(root map[string]any) {
		root["value"] = nil
		root["original_value_bytes"] = 4
		root["emitted_value_bytes"] = 4
		root["field"].(map[string]any)["present"] = false
		root["field"].(map[string]any)["empty"] = true
		root["field"].(map[string]any)["value_type"] = "null"
	})
	if decoded, err := DecodeJiraSnapshotFieldEvidence(bytes.NewReader(nullField)); err != nil || decoded.Value != nil {
		t.Fatalf("nullable field value = %+v err=%v", decoded, err)
	}
}

func TestDecodeJiraSnapshotWiresRejectSyntaxMemberAndNullDrift(t *testing.T) {
	tests := []struct {
		name   string
		valid  []byte
		decode func([]byte) error
	}{
		{
			name: "issue list", valid: validJiraSnapshotIssueListWire(t),
			decode: func(data []byte) error {
				_, err := DecodeJiraSnapshotIssueList(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "field evidence", valid: validJiraSnapshotFieldWire(t),
			decode: func(data []byte) error {
				_, err := DecodeJiraSnapshotFieldEvidence(bytes.NewReader(data))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := map[string][]byte{
				"unknown root": mutateJiraSnapshotWire(t, test.valid, func(root map[string]any) {
					root["backend_payload"] = true
				}),
				"missing root": mutateJiraSnapshotWire(t, test.valid, func(root map[string]any) {
					delete(root, "schema_version")
				}),
				"null structural": mutateJiraSnapshotWire(t, test.valid, func(root map[string]any) {
					root["schema_version"] = nil
				}),
				"trailing value": append(bytes.Clone(test.valid), []byte("\n{}")...),
			}
			duplicate := bytes.Replace(test.valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			if bytes.Equal(duplicate, test.valid) {
				t.Fatal("duplicate mutation did not apply")
			}
			invalid["duplicate member"] = duplicate
			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("invalid wire was accepted")
					}
				})
			}
		})
	}
}

func TestDecodeJiraSnapshotIssueListRejectsProjectionAndPageContradictions(t *testing.T) {
	valid := validJiraSnapshotIssueListWire(t)
	mutations := map[string]func(map[string]any){
		"schema": func(root map[string]any) { root["schema_version"] = 2 },
		"source": func(root map[string]any) { root["source"].(map[string]any)["kind"] = "board" },
		"source member": func(root map[string]any) {
			root["source"].(map[string]any)["id"] = "42"
		},
		"selection member": func(root map[string]any) {
			root["selection"].(map[string]any)["scope"] = "search"
		},
		"empty jql": func(root map[string]any) { root["selection"].(map[string]any)["jql"] = " " },
		"ordering": func(root map[string]any) {
			root["projection"].(map[string]any)["ordering"] = "backend-rank"
		},
		"view": func(root map[string]any) { root["projection"].(map[string]any)["view"] = "Bad View" },
		"duplicate column": func(root map[string]any) {
			projection := root["projection"].(map[string]any)
			projection["columns"] = append(projection["columns"].([]any), "summary")
		},
		"foreign context column": func(root map[string]any) {
			projection := root["projection"].(map[string]any)
			projection["columns"] = []any{"key", "board.column"}
			projection["fields"] = []any{"board.column"}
		},
		"field order": func(root map[string]any) {
			root["projection"].(map[string]any)["fields"] = []any{"updated", "status", "summary", "nullable", "payload"}
		},
		"row member": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["context"] = map[string]any{}
		},
		"empty optional row id": func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["id"] = ""
		},
		"row position": func(root map[string]any) { root["rows"].([]any)[1].(map[string]any)["position"] = 7 },
		"duplicate row": func(root map[string]any) {
			root["rows"].([]any)[1].(map[string]any)["key"] = "SYN-1"
		},
		"missing value": func(root map[string]any) {
			delete(root["rows"].([]any)[0].(map[string]any)["values"].(map[string]any), "updated")
		},
		"page count":         func(root map[string]any) { root["page"].(map[string]any)["count"] = 2 },
		"complete truncated": func(root map[string]any) { root["page"].(map[string]any)["truncated"] = true },
		"complete cursor":    func(root map[string]any) { root["page"].(map[string]any)["next_cursor"] = "3" },
		"empty optional reason": func(root map[string]any) {
			root["page"].(map[string]any)["partial_reason"] = ""
		},
		"terminal reason": func(root map[string]any) {
			page := root["page"].(map[string]any)
			page["complete"], page["truncated"], page["partial_reason"] = false, true, "backend text"
		},
		"resumable reason": func(root map[string]any) {
			page := root["page"].(map[string]any)
			page["complete"], page["truncated"], page["next_cursor"], page["partial_reason"] = false, true, "3", "pagination_stalled"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraSnapshotIssueList(bytes.NewReader(mutateJiraSnapshotWire(t, valid, mutate))); err == nil {
				t.Fatal("contradictory issue list was accepted")
			}
		})
	}

	for name, mutate := range map[string]func(map[string]any){
		"resumable": func(root map[string]any) {
			page := root["page"].(map[string]any)
			page["complete"], page["truncated"], page["next_cursor"] = false, true, "3"
		},
		"terminal partial": func(root map[string]any) {
			page := root["page"].(map[string]any)
			page["complete"], page["truncated"], page["partial_reason"] = false, true, "pagination_stalled"
		},
	} {
		t.Run("accept "+name, func(t *testing.T) {
			if _, err := DecodeJiraSnapshotIssueList(bytes.NewReader(mutateJiraSnapshotWire(t, valid, mutate))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeJiraSnapshotFieldEvidenceRejectsBoundAndAccountingContradictions(t *testing.T) {
	valid := validJiraSnapshotFieldWire(t)
	mutations := map[string]func(map[string]any){
		"schema":      func(root map[string]any) { root["schema_version"] = 2 },
		"issue key":   func(root map[string]any) { root["issue"].(map[string]any)["key"] = " " },
		"issue field": func(root map[string]any) { root["issue"].(map[string]any)["private"] = true },
		"empty optional issue id": func(root map[string]any) {
			root["issue"].(map[string]any)["id"] = ""
		},
		"field id": func(root map[string]any) { root["field"].(map[string]any)["id"] = "" },
		"field member": func(root map[string]any) {
			root["field"].(map[string]any)["backend"] = true
		},
		"value type": func(root map[string]any) { root["field"].(map[string]any)["value_type"] = "backend" },
		"empty optional schema": func(root map[string]any) {
			root["field"].(map[string]any)["schema"] = ""
		},
		"projection": func(root map[string]any) { root["projection"] = "raw" },
		"minimum":    func(root map[string]any) { root["max_value_bytes"] = 255 },
		"maximum":    func(root map[string]any) { root["max_value_bytes"] = (128 << 10) + 1 },
		"emitted bound": func(root map[string]any) {
			root["emitted_value_bytes"] = 4097
		},
		"emitted mismatch": func(root map[string]any) {
			root["emitted_value_bytes"] = 1
		},
		"flags": func(root map[string]any) { root["truncated"] = true },
		"absent field": func(root map[string]any) {
			root["field"].(map[string]any)["present"] = false
		},
		"null metadata": func(root map[string]any) {
			root["field"].(map[string]any)["value_type"] = "null"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraSnapshotFieldEvidence(bytes.NewReader(mutateJiraSnapshotWire(t, valid, mutate))); err == nil {
				t.Fatal("contradictory field evidence was accepted")
			}
		})
	}
}

func TestDecodeJiraSnapshotWiresHonorExactOneMiBLimit(t *testing.T) {
	valid := validJiraSnapshotIssueListWire(t)
	if len(valid) >= maxContractBytes {
		t.Fatalf("fixture unexpectedly has %d bytes", len(valid))
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), maxContractBytes-len(valid))...)
	if _, err := DecodeJiraSnapshotIssueList(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact wire limit rejected: %v", err)
	}
	if _, err := DecodeJiraSnapshotIssueList(bytes.NewReader(append(atLimit, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}

func validJiraSnapshotIssueListWire(t *testing.T) []byte {
	t.Helper()
	columns := []any{"key", "summary", "status", "updated", "nullable", "payload"}
	fields := []any{"summary", "status", "updated", "nullable", "payload"}
	rows := make([]any, 0, 3)
	for index, status := range []string{"In Review", "Open", "Done"} {
		rows = append(rows, map[string]any{
			"key": "SYN-" + string(rune('1'+index)), "id": "1000" + string(rune('1'+index)), "position": index,
			"values": map[string]any{
				"summary": "Synthetic evidence", "status": status, "updated": "2026-08-01T10:00:00Z",
				"nullable": nil, "payload": map[string]any{"nested": []any{true, 7.0, nil}},
			},
		})
	}
	return mustJiraSnapshotJSON(t, map[string]any{
		"schema_version": 1,
		"source":         map[string]any{"kind": "jql"},
		"selection":      map[string]any{"jql": "project = SYN ORDER BY updated DESC"},
		"projection": map[string]any{
			"columns": columns, "fields": fields, "ordering": "jql-order", "view": "explicit",
		},
		"rows": rows,
		"page": map[string]any{"count": 3, "complete": true, "truncated": false, "next_cursor": nil},
	})
}

func validJiraSnapshotFieldWire(t *testing.T) []byte {
	t.Helper()
	value := map[string]any{"kind": "named", "name": "Synthetic", "details": []any{"one", nil, true}}
	encoded := mustJiraSnapshotJSON(t, value)
	return mustJiraSnapshotJSON(t, map[string]any{
		"schema_version": 1,
		"issue":          map[string]any{"id": "10001", "key": "SYN-1", "updated": "2026-08-01T10:00:00Z"},
		"field": map[string]any{
			"id": "description", "name": "Description", "custom": false, "schema": "string",
			"present": true, "empty": false, "value_type": "object",
		},
		"projection": "compact", "max_value_bytes": 4096,
		"original_value_bytes": len(encoded), "emitted_value_bytes": len(encoded),
		"complete": true, "truncated": false, "value": value,
	})
}

func mutateJiraSnapshotWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
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

func mustJiraSnapshotJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
