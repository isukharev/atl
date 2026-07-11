package jiramap

import "testing"

func TestIssueMapsSnapshotFieldsDefensively(t *testing.T) {
	fields := map[string]any{
		"summary":     "Summary",
		"description": "Body",
		"status":      map[string]any{"id": "11", "name": "Open"},
		"issuetype":   map[string]any{"name": "Task"},
		"project":     map[string]any{"key": "PROJ"},
		"assignee":    map[string]any{"displayName": "Alice"},
		"labels":      []any{"one", "two"},
		"issuelinks": []any{map[string]any{
			"id": "10", "type": map[string]any{"name": "Blocks", "outward": "blocks"},
			"outwardIssue": map[string]any{"key": "PROJ-2"},
		}},
		"comment": map[string]any{"comments": []any{map[string]any{
			"id": "20", "author": map[string]any{"name": "bob"}, "created": "now", "body": "hello",
		}}},
	}
	issue := Issue("1", "PROJ-1", fields)
	if issue.Summary != "Summary" || issue.Body != "Body" || issue.Status != "Open" || issue.StatusID != "11" || issue.Type != "Task" || issue.Project != "PROJ" || issue.Assignee != "Alice" {
		t.Fatalf("mapped issue = %+v", issue)
	}
	if len(issue.Labels) != 2 || len(issue.Links) != 1 || issue.Links[0].TypeName != "Blocks" || issue.Links[0].Type != "blocks" || len(issue.Comments) != 1 {
		t.Fatalf("mapped collections = labels=%v links=%v comments=%v", issue.Labels, issue.Links, issue.Comments)
	}
	delete(issue.Fields, "summary")
	if _, ok := issue.Raw["summary"]; !ok {
		t.Fatal("Raw and Fields top-level maps alias")
	}
}

func TestIssueToleratesNilAndOddShapes(t *testing.T) {
	if issue := Issue("1", "PROJ-1", nil); issue == nil || issue.Key != "PROJ-1" {
		t.Fatalf("nil fields issue = %+v", issue)
	}
	if issue := Issue("1", "PROJ-1", map[string]any{"issuelinks": []any{"bad"}, "comment": map[string]any{"comments": []any{"bad"}}}); issue == nil {
		t.Fatal("odd shapes returned nil")
	}
}
