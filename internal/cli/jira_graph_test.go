package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jiraGraphServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/2/issue/PROJ-1":
			if request.URL.Query().Get("fields") != "*all" ||
				request.URL.Query().Get("properties") != "*all" ||
				request.URL.Query().Get("expand") != "names,schema" {
				t.Fatalf("snapshot query = %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{
				"id":"10001","key":"PROJ-1",
				"fields":{
					"summary":"Graph seed",
					"description":"See PROJ-2 and pageId=7",
					"issuelinks":[],
					"parent":null,
					"subtasks":[],
					"attachment":[{"id":"4","filename":"design.txt","content":"https://private.invalid/download"}]
				},
				"names":{"summary":"Summary","description":"Description"},
				"schema":{"summary":{"type":"string","system":"summary"},"description":{"type":"string","system":"description"}},
				"properties":{}
			}`)
		case "/rest/api/2/issue/PROJ-1/comment":
			_, _ = io.WriteString(w, `{"startAt":0,"total":0,"comments":[]}`)
		case "/rest/api/2/issue/PROJ-1/worklog":
			_, _ = io.WriteString(w, `{"startAt":0,"total":0,"worklogs":[]}`)
		case "/rest/api/2/issue/PROJ-1/remotelink":
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.RequestURI())
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestJiraIssueGraphJSONAndTextGoldens(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("json exit=%d output=%s", code, output)
	}
	if strings.Contains(output, "private.invalid") {
		t.Fatalf("attachment download URL leaked: %s", output)
	}
	assertGolden(t, "jira_issue_graph.json", []byte(output))
	if len(*requests) != 4 {
		t.Fatalf("requests = %#v", *requests)
	}

	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
	assertGolden(t, "jira_issue_graph.md", []byte(text))
}

func TestJiraIssueGraphRejectsIDAndArityBeforeNetwork(t *testing.T) {
	server, requests := jiraGraphServer(t)
	for _, args := range [][]string{
		{"-o", "id", "jira", "issue", "graph", "PROJ-1"},
		{"jira", "issue", "graph"},
		{"jira", "issue", "graph", "PROJ-1", "PROJ-2"},
	} {
		output, code := runCLI(t, jiraEnv(server), args...)
		if code != exitUsage || output != "" {
			t.Fatalf("args=%v exit=%d output=%q", args, code, output)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("requests = %#v", *requests)
	}
}
