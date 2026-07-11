package app

import (
	"context"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestIssueListColumnsDriveFieldsAndRejectForeignContext(t *testing.T) {
	columns, fields, err := NormalizeIssueListColumns([]string{"position", "key", "summary", "board.column", "customfield_10001", "summary"}, nil, "board")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(columns, ",") != "position,key,summary,board.column,customfield_10001" || strings.Join(fields, ",") != "summary,customfield_10001" {
		t.Fatalf("columns=%v fields=%v", columns, fields)
	}
	if _, _, err := NormalizeIssueListColumns([]string{"structure.depth"}, nil, "board"); err == nil {
		t.Fatal("foreign source context was accepted")
	}
}

func TestEpicChildrenIssueListResolvesFieldAndPreservesContext(t *testing.T) {
	tracker := &recordingTracker{
		fieldDefs: []domain.FieldDef{{ID: "customfield_10010", Name: "Epic Link"}},
		issues:    []domain.Issue{{ID: "10002", Key: "PROJ-2", Summary: "Child", Status: "Open", Type: "Story", Fields: map[string]any{}}},
	}
	list, err := (&JiraService{tr: tracker}).EpicChildrenIssueList(context.Background(), "PROJ-1", JiraEpicChildrenOpts{
		Columns: []string{"key", "summary", "epic.parent", "epic.relation"}, Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.issueKey != "" {
		t.Fatalf("unexpected per-parent issue read: %q", tracker.issueKey)
	}
	if !strings.Contains(tracker.searchJQL, `cf[10010] in ("PROJ-1") ORDER BY key`) || tracker.searchLimit != 25 {
		t.Fatalf("search jql=%q limit=%d", tracker.searchJQL, tracker.searchLimit)
	}
	if got := list.Rows[0].Context["epic"]; got["parent"] != "PROJ-1" || got["relation"] != "epic-child" {
		t.Fatalf("context=%v", got)
	}
	if md := IssueListMarkdown(list, false); !strings.Contains(md, "| PROJ-2 | Child | PROJ-1 | epic-child |") {
		t.Fatalf("markdown:\n%s", md)
	}
}

func TestIssueListJSONShapeAndMarkdownShareOrderedProjection(t *testing.T) {
	issues := []domain.Issue{{Key: "PROJ-1", ID: "10001", Summary: "First | line\nnext", Status: "Open", Assignee: "Owner", Fields: map[string]any{"customfield_10001": map[string]any{"opaque": true}}}}
	contexts := []map[string]map[string]any{{"board": {"column": "Review"}}}
	list := NewIssueList(IssueListSource{Kind: "board", ID: "5"}, map[string]any{"scope": "board"}, []string{"position", "key", "summary", "board.column", "customfield_10001"}, []string{"summary", "customfield_10001"}, "backend-rank", issues, contexts, "50")
	if list.Rows == nil || list.Rows[0].Values["customfield_10001"] != "[object]" || list.Page.Complete || !list.Page.Truncated || list.Page.NextCursor == nil {
		t.Fatalf("list=%+v", list)
	}
	md := IssueListMarkdown(list, false)
	for _, want := range []string{"# Jira issues", "complete: false", "**Truncated:**", "| # | Key | Summary | Column | customfield_10001 |", `First \| line next`, "[object]"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown missing %q:\n%s", want, md)
		}
	}
}

func TestMarkdownTableEscapesStructuralTextAndEmptyList(t *testing.T) {
	got := MarkdownTable([]string{"A|B"}, [][]string{{`C:\temp | <x> don't`}})
	for _, want := range []string{`A\|B`, `C:\\temp \| &lt;x&gt; don't`} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q: %s", want, got)
		}
	}
	if empty := MarkdownTable([]string{"A"}, nil); empty != "_None._\n" {
		t.Fatalf("empty table=%q", empty)
	}
}
