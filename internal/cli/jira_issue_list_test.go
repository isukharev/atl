package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestJiraIssueSearchUsesCommonListContractAndMarkdown(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{"issues":[{"id":"10001","key":"ENG-1","fields":{"summary":"First","status":{"name":"Open"},"assignee":{"displayName":"Owner"}}}],"startAt":0,"maxResults":50,"total":1}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "search", "--jql", "project = ENG")
	if code != exitOK {
		t.Fatalf("search exit=%d output=%q", code, out)
	}
	var list struct {
		SchemaVersion int `json:"schema_version"`
		Source        struct {
			Kind string `json:"kind"`
		} `json:"source"`
		Projection struct{ Columns, Fields []string } `json:"projection"`
		Rows       []struct {
			Key    string         `json:"key"`
			Values map[string]any `json:"values"`
		} `json:"rows"`
		Page struct {
			Complete   bool    `json:"complete"`
			NextCursor *string `json:"next_cursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || list.SchemaVersion != 1 || list.Source.Kind != "jql" || len(list.Rows) != 1 || list.Rows[0].Key != "ENG-1" || !list.Page.Complete || list.Page.NextCursor != nil {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	text, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "search", "--jql", "project = ENG", "-o", "text")
	if code != exitOK || !strings.Contains(text, "# Jira issues") || !strings.Contains(text, "| Key | Summary | Status | Assignee |") || strings.Contains(text, "\t") {
		t.Fatalf("text exit=%d:\n%s", code, text)
	}
}

func TestJiraIssueListColumnsDriveBackendProjection(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{"issues":[],"startAt":0,"maxResults":50,"total":0}`)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "search", "--jql", "project = ENG", "--columns", "position,key,customfield_10001")
	if code != exitOK {
		t.Fatalf("search exit=%d", code)
	}
	requests := js.requests()
	if len(requests) != 1 || !strings.Contains(requests[0].query, "fields=customfield_10001") {
		t.Fatalf("requests=%+v", requests)
	}
}

func TestJiraEpicChildrenUsesCommonListAndResolvedField(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK, `[{"id":"customfield_10010","name":"Epic Link","custom":true}]`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{"issues":[{"id":"10002","key":"ENG-2","fields":{"summary":"Child","status":{"name":"Open"},"issuetype":{"name":"Story"},"assignee":{"displayName":"Owner"}}}],"startAt":0,"maxResults":50,"total":1}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "children", "ENG-1", "--columns", "key,summary,epic.parent,epic.relation", "-o", "text")
	if code != exitOK || !strings.Contains(out, "| Key | Summary | Epic | Relation |") || !strings.Contains(out, "| ENG-2 | Child | ENG-1 | epic-child |") {
		t.Fatalf("children exit=%d output=%q", code, out)
	}
	requests := js.requests()
	if len(requests) != 2 || !strings.Contains(requests[1].query, "jql=cf%5B10010%5D") || strings.Contains(requests[1].query, "fields=description") {
		t.Fatalf("requests=%+v", requests)
	}

	ids, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "children", "ENG-1", "-o", "id")
	if code != exitOK || ids != "ENG-2\n" {
		t.Fatalf("children ids exit=%d output=%q", code, ids)
	}
}

func TestJiraEpicChildrenJSONGolden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK, `[{"id":"customfield_10010","name":"Epic Link","custom":true}]`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{"issues":[{"id":"10002","key":"ENG-2","fields":{"summary":"Child","status":{"name":"Open"},"issuetype":{"name":"Story"},"assignee":{"displayName":"Owner"}}}],"startAt":0,"maxResults":50,"total":1}`)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "children", "ENG-1")
	if code != exitOK {
		t.Fatalf("children exit=%d output=%q", code, out)
	}
	assertGolden(t, "jira_issue_children.json", []byte(out))
}

func TestBoardAndSprintPagesUseCommonListMarkdown(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/issue", http.StatusOK, boardIssuesBody)
	js.route(http.MethodGet, "/rest/agile/1.0/sprint/7/issue", http.StatusOK, `{"startAt":0,"maxResults":50,"total":1,"issues":[{"id":"10001","key":"ENG-1","fields":{"summary":"First","status":{"name":"Open"}}}]}`)

	for _, args := range [][]string{
		{"jira", "board", "issues", "5", "-o", "text"},
		{"jira", "sprint", "issues", "7", "-o", "text"},
	} {
		out, code := runCLI(t, jiraEnv(js.srv), args...)
		if code != exitOK || !strings.Contains(out, "# Jira issues") || !strings.Contains(out, "| # | Key | Summary | Status | Assignee |") || strings.Contains(out, "\t") {
			t.Fatalf("%v exit=%d output=%q", args, code, out)
		}
	}
}

func TestBoardIssueListResolvesRequestedColumnContext(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/issue", http.StatusOK, boardIssuesBody)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/configuration", http.StatusOK, kanbanConfigBody)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "issues", "5", "--columns", "position,key,board.column", "-o", "text")
	if code != exitOK || !strings.Contains(out, "| # | Key | Column |") || !strings.Contains(out, "| 0 | ENG-1 | To Do |") || !strings.Contains(out, "| 1 | ENG-2 | Unmapped |") {
		t.Fatalf("board column exit=%d output=%q", code, out)
	}
}

func TestBoardIdentityOnlyProjectionDoesNotFetchStatus(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/issue", http.StatusOK, `{"startAt":0,"maxResults":50,"total":0,"issues":[]}`)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "issues", "5", "--columns", "key")
	if code != exitOK {
		t.Fatalf("board identity list exit=%d", code)
	}
	requests := js.requests()
	if len(requests) != 1 || !strings.Contains(requests[0].query, "fields=key") || strings.Contains(requests[0].query, "status") {
		t.Fatalf("requests=%+v", requests)
	}
}
