package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraWorkflowWiresAcceptReleasedShapes(t *testing.T) {
	capabilities, err := DecodeJiraPortfolioCapabilityCatalog(bytes.NewReader(validJiraPortfolioCapabilityWire(t)))
	if err != nil || capabilities.Selection.Count != jiraPortfolioCapabilityCount || capabilities.Capabilities[0].ID != "jira.board.list" {
		t.Fatalf("portfolio capabilities=%+v err=%v", capabilities, err)
	}

	boards, err := DecodeJiraPortfolioBoardList(bytes.NewReader(validJiraPortfolioBoardListWire(t)))
	if err != nil || len(boards.Boards) != 1 || boards.Boards[0].ProjectKey != "SYN" {
		t.Fatalf("board list=%+v err=%v", boards, err)
	}

	folders, err := DecodeJiraPortfolioStructureFolders(bytes.NewReader(validJiraPortfolioFoldersWire(t)))
	if err != nil || !folders.Complete || len(folders.Folders) != 3 ||
		folders.Folders[1].ParentFolderID != "root" || folders.Folders[1].FolderID != folders.Folders[2].FolderID {
		t.Fatalf("folders=%+v err=%v", folders, err)
	}

	sprint, err := DecodeJiraSprintCurrent(bytes.NewReader(validJiraSprintCurrentWire(t)))
	if err != nil || sprint.ID != 71 || sprint.OriginBoardID != 31 || sprint.State != "active" {
		t.Fatalf("sprint=%+v err=%v", sprint, err)
	}

	membership, err := DecodeJiraSprintMembershipIssueList(bytes.NewReader(validJiraSprintMembershipWire(t)))
	if err != nil || membership.Source.ID != "71" || membership.Page.NextCursor == nil || *membership.Page.NextCursor != "2" {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
	if _, ok := membership.Rows[0].Values["summary"].(map[string]any); !ok {
		t.Fatalf("backend-defined values were not preserved open: %#v", membership.Rows[0].Values)
	}
}

func TestDecodeJiraWorkflowWiresFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		valid      []byte
		rootMember string
		maximum    int
		decode     func([]byte) error
		semantic   func(map[string]any)
	}{
		{
			name: "portfolio capabilities", valid: validJiraPortfolioCapabilityWire(t), rootMember: "schema_version", maximum: maxCapabilityCatalogBytes,
			decode: func(data []byte) error {
				_, err := DecodeJiraPortfolioCapabilityCatalog(bytes.NewReader(data))
				return err
			},
			semantic: func(root map[string]any) {
				root["selection"].(map[string]any)["task"] = "jira/other"
			},
		},
		{
			name: "board list", valid: validJiraPortfolioBoardListWire(t), rootMember: "boards", maximum: jiraPortfolioBoardListWireMaxBytes,
			decode:   func(data []byte) error { _, err := DecodeJiraPortfolioBoardList(bytes.NewReader(data)); return err },
			semantic: func(root map[string]any) { root["boards"].([]any)[0].(map[string]any)["type"] = "backend" },
		},
		{
			name: "structure folders", valid: validJiraPortfolioFoldersWire(t), rootMember: "schema_version", maximum: jiraPortfolioFoldersWireMaxBytes,
			decode: func(data []byte) error {
				_, err := DecodeJiraPortfolioStructureFolders(bytes.NewReader(data))
				return err
			},
			semantic: func(root map[string]any) {
				root["folders"].([]any)[0].(map[string]any)["stats"].(map[string]any)["issue_rows"] = float64(99)
			},
		},
		{
			name: "sprint current", valid: validJiraSprintCurrentWire(t), rootMember: "id", maximum: jiraSprintCurrentWireMaxBytes,
			decode:   func(data []byte) error { _, err := DecodeJiraSprintCurrent(bytes.NewReader(data)); return err },
			semantic: func(root map[string]any) { root["state"] = "backend" },
		},
		{
			name: "sprint membership", valid: validJiraSprintMembershipWire(t), rootMember: "schema_version", maximum: jiraSprintMembershipWireMaxBytes,
			decode: func(data []byte) error {
				_, err := DecodeJiraSprintMembershipIssueList(bytes.NewReader(data))
				return err
			},
			semantic: func(root map[string]any) {
				root["rows"].([]any)[0].(map[string]any)["context"].(map[string]any)["sprint"].(map[string]any)["id"] = float64(72)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := map[string][]byte{
				"unknown":  mutateJiraWorkflowWire(t, test.valid, func(root map[string]any) { root["backend_payload"] = true }),
				"missing":  mutateJiraWorkflowWire(t, test.valid, func(root map[string]any) { delete(root, test.rootMember) }),
				"null":     mutateJiraWorkflowWire(t, test.valid, func(root map[string]any) { root[test.rootMember] = nil }),
				"semantic": mutateJiraWorkflowWire(t, test.valid, test.semantic),
				"trailing": append(bytes.Clone(test.valid), []byte("\n{}")...),
				"oversize": bytes.Repeat([]byte(" "), test.maximum+1),
			}
			duplicate := bytes.Replace(test.valid, []byte(`"`+test.rootMember+`":`), []byte(`"`+test.rootMember+`":null,"`+test.rootMember+`":`), 1)
			if bytes.Equal(duplicate, test.valid) {
				t.Fatal("duplicate-key mutation did not apply")
			}
			invalid["duplicate"] = duplicate
			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("invalid workflow wire was accepted")
					}
				})
			}
		})
	}
}

func TestDecodeJiraWorkflowWiresRejectNestedMemberAndSelectionDrift(t *testing.T) {
	capabilities := mutateJiraWorkflowWire(t, validJiraPortfolioCapabilityWire(t), func(root map[string]any) {
		root["capabilities"].([]any)[0].(map[string]any)["backend"] = true
	})
	if _, err := DecodeJiraPortfolioCapabilityCatalog(bytes.NewReader(capabilities)); err == nil {
		t.Fatal("unknown nested capability member was accepted")
	}

	folders := mutateJiraWorkflowWire(t, validJiraPortfolioFoldersWire(t), func(root map[string]any) {
		root["folders"].([]any)[0].(map[string]any)["stats"].(map[string]any)["backend"] = true
	})
	if _, err := DecodeJiraPortfolioStructureFolders(bytes.NewReader(folders)); err == nil {
		t.Fatal("unknown nested Structure member was accepted")
	}

	membership := mutateJiraWorkflowWire(t, validJiraSprintMembershipWire(t), func(root map[string]any) {
		root["selection"].(map[string]any)["jql"] = "project = SYN"
	})
	if _, err := DecodeJiraSprintMembershipIssueList(bytes.NewReader(membership)); err == nil {
		t.Fatal("JQL selection was accepted by sprint membership wire")
	}
}

func validJiraPortfolioCapabilityWire(t *testing.T) []byte {
	t.Helper()
	items := make([]CapabilityCatalogItem, 0, jiraPortfolioCapabilityCount)
	for _, item := range PinnedCapabilityCatalog().Capabilities {
		if item.TaskClass == "jira/portfolio" {
			items = append(items, item)
		}
	}
	catalog := JiraPortfolioCapabilityCatalog{
		SchemaVersion: CapabilityCatalogSchemaVersion,
		Routing: CapabilityCatalogRouting{
			Match: capabilityRoutingMatch, ReferenceLoad: capabilityRoutingReferenceLoad, Stop: capabilityRoutingStop,
		},
		Selection:    JiraPortfolioCapabilitySelection{Task: "jira/portfolio", Count: jiraPortfolioCapabilityCount},
		Capabilities: items,
	}
	if len(items) != jiraPortfolioCapabilityCount {
		t.Fatalf("pinned Jira portfolio capabilities=%d want=%d", len(items), jiraPortfolioCapabilityCount)
	}
	return mustJiraWorkflowJSON(t, catalog)
}

func validJiraPortfolioBoardListWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraWorkflowJSON(t, map[string]any{
		"boards":      []any{map[string]any{"id": 17, "name": "Synthetic board", "type": "scrum", "project_key": "SYN"}},
		"next_cursor": "",
	})
}

func validJiraPortfolioFoldersWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraWorkflowJSON(t, map[string]any{
		"schema_version": 1,
		"structure":      map[string]any{"id": 123, "name": "Synthetic plan", "read_only": true},
		"forest_version": map[string]any{"signature": 55, "version": 7},
		"folders": []any{
			map[string]any{
				"folder_id": "root", "row_id": 100, "name": "Root", "path": []any{"Root"}, "depth": 0, "parent_folder_id": "",
				"stats": map[string]any{"descendant_rows": 3, "issue_rows": 1, "unique_issues": 1, "subfolders": 2, "max_relative_depth": 1},
			},
			map[string]any{
				"folder_id": "child", "row_id": 101, "name": "Child", "path": []any{"Root", "Child"}, "depth": 1, "parent_folder_id": "root",
				"stats": map[string]any{"descendant_rows": 1, "issue_rows": 1, "unique_issues": 1, "subfolders": 0, "max_relative_depth": 0},
			},
			map[string]any{
				"folder_id": "child", "row_id": 102, "name": "Child", "path": []any{"Root", "Child"}, "depth": 1, "parent_folder_id": "root",
				"stats": map[string]any{"descendant_rows": 1, "issue_rows": 1, "unique_issues": 1, "subfolders": 0, "max_relative_depth": 0},
			},
		},
		"complete": true,
		"warnings": []any{},
	})
}

func validJiraSprintCurrentWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraWorkflowJSON(t, map[string]any{
		"id": 71, "name": "Flow 12", "state": "active", "start_date": "2026-07-20", "end_date": "2026-07-31",
		"goal": "Synthetic flow", "origin_board_id": 31,
	})
}

func validJiraSprintMembershipWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraWorkflowJSON(t, map[string]any{
		"schema_version": 1,
		"source":         map[string]any{"kind": "sprint", "id": "71"},
		"selection":      map[string]any{},
		"projection": map[string]any{
			"columns":  []any{"position", "key", "summary", "status", "assignee", "priority", "issuetype", "updated", "sprint.id"},
			"fields":   []any{"summary", "status", "assignee", "priority", "issuetype", "updated"},
			"ordering": "backend-order", "view": "explicit",
		},
		"rows": []any{map[string]any{
			"key": "SYN-1", "id": "9001", "position": 0,
			"values": map[string]any{
				"summary": map[string]any{"nested": []any{true, float64(7), nil}}, "status": "In Progress", "assignee": nil,
				"priority": "High", "issuetype": "Task", "updated": "2026-07-26T09:00:00.000+0000",
			},
			"context": map[string]any{"sprint": map[string]any{"id": 71}},
		}},
		"page": map[string]any{"count": 1, "complete": false, "truncated": true, "next_cursor": "2"},
	})
}

func mutateJiraWorkflowWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return mustJiraWorkflowJSON(t, root)
}

func mustJiraWorkflowJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeJiraWorkflowWireExactLimit(t *testing.T) {
	valid := validJiraPortfolioBoardListWire(t)
	if len(valid) >= jiraPortfolioBoardListWireMaxBytes {
		t.Fatalf("valid wire unexpectedly has %d bytes", len(valid))
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraPortfolioBoardListWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraPortfolioBoardList(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact limit was rejected: %v", err)
	}
	if _, err := DecodeJiraPortfolioBoardList(bytes.NewReader(append(atLimit, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error=%v", err)
	}
}
