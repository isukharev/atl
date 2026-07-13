package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jiraIssueFieldsServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	issueReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/2/field":
			_, _ = io.WriteString(w, `[
                {"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
                {"id":"assignee","name":"Assignee","custom":false,"schema":{"type":"user"}},
                {"id":"customfield_1","name":"Delivery Notes","custom":true,"schema":{"type":"string"}},
                {"id":"customfield_2","name":"Empty","custom":true,"schema":{"type":"string"}}
            ]`)
		case "/rest/api/2/issue/PROJ-1":
			issueReads++
			if got := request.URL.Query().Get("fields"); got != "*all" && got != "customfield_1" && got != "assignee" {
				t.Fatalf("fields query=%q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "10001", "key": "PROJ-1", "fields": map[string]any{
				"summary": "Plan", "assignee": map[string]any{"name": "alice", "displayName": "Alice", "emailAddress": "private@example.test", "avatarUrls": map[string]string{"48x48": "https://example.test/avatar"}, "active": true},
				"customfield_1": "Current delivery evidence", "customfield_2": nil,
			}})
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server, &issueReads
}

func TestJiraIssueFieldsDefaultsToCompactNonEmptyGolden(t *testing.T) {
	server, _ := jiraIssueFieldsServer(t)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1")
	if code != exitOK || strings.Contains(out, "private@example.test") || strings.Contains(out, "avatar") || strings.Contains(out, `"name": "Empty"`) {
		t.Fatalf("compact fields exit=%d out=%s", code, out)
	}
	assertGolden(t, "jira_issue_fields_compact.json", []byte(out))
	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "fields", "PROJ-1", "--field", "Delivery Notes")
	if code != exitOK || !strings.Contains(text, "| Field | ID | Type | Value |") || !strings.Contains(text, "Current delivery evidence") {
		t.Fatalf("text fields exit=%d out=%s", code, text)
	}
}

func TestJiraIssueFieldsIncludeEmptyAndRawAreExplicit(t *testing.T) {
	server, _ := jiraIssueFieldsServer(t)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--include-empty")
	if code != exitOK || !strings.Contains(out, `"name": "Empty"`) || !strings.Contains(out, `"empty": true`) {
		t.Fatalf("include empty exit=%d out=%s", code, out)
	}
	out, code = runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--field", "assignee", "--raw")
	if code != exitOK || !strings.Contains(out, "private@example.test") {
		t.Fatalf("raw fields exit=%d out=%s", code, out)
	}
}

func TestJiraIssueFieldsMetadataOnlyGoldenAndText(t *testing.T) {
	server, _ := jiraIssueFieldsServer(t)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--metadata-only")
	if code != exitOK || strings.Contains(out, `"value":`) || strings.Contains(out, "Current delivery evidence") || strings.Contains(out, "private@example.test") {
		t.Fatalf("metadata fields exit=%d out=%s", code, out)
	}
	assertGolden(t, "jira_issue_fields_metadata.json", []byte(out))

	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "fields", "PROJ-1", "--field", "Delivery Notes", "--metadata-only")
	if code != exitOK || !strings.Contains(text, "| Field | ID | Schema | Value type | Empty |") || !strings.Contains(text, "| Delivery Notes | customfield_1 | string | string | false |") || strings.Contains(text, "Current delivery evidence") {
		t.Fatalf("metadata text exit=%d out=%s", code, text)
	}
}

func TestJiraIssueFieldsMetadataOnlyIncludeEmptyAndRawConflict(t *testing.T) {
	server, issueReads := jiraIssueFieldsServer(t)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--metadata-only", "--include-empty")
	if code != exitOK || !strings.Contains(out, `"name": "Empty"`) || !strings.Contains(out, `"value_type": "null"`) || strings.Contains(out, `"value":`) {
		t.Fatalf("metadata include-empty exit=%d out=%s", code, out)
	}
	readsBefore := *issueReads
	out, code = runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--metadata-only", "--raw")
	if code != exitUsage || out != "" || *issueReads != readsBefore {
		t.Fatalf("metadata/raw conflict exit=%d reads=%d->%d out=%q", code, readsBefore, *issueReads, out)
	}
}

func TestJiraIssueFieldsAmbiguousNameFailsBeforeIssueRead(t *testing.T) {
	issueReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/rest/api/2/field" {
			_, _ = io.WriteString(w, `[{"id":"customfield_1","name":"Risk"},{"id":"customfield_2","name":"Risk"}]`)
			return
		}
		issueReads++
	}))
	t.Cleanup(server.Close)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "fields", "PROJ-1", "--field", "Risk")
	if code != exitCheckFailed || out != "" || issueReads != 0 {
		t.Fatalf("ambiguous exit=%d reads=%d out=%q", code, issueReads, out)
	}
}
