package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestJiraEpicDigestGolden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK, `[
		{"id":"customfield_10001","name":"Epic Link","custom":true,"schema":{"type":"any"}},
		{"id":"customfield_10002","name":"Delivery Notes","custom":true,"schema":{"type":"string"}}
	]`)
	js.route(http.MethodGet, "/rest/api/2/issue/PROJ-1/changelog", http.StatusOK,
		`{"startAt":0,"maxResults":100,"total":1,"values":[{"id":"h1","created":"2026-04-01T00:00:00.000+0000","items":[{"field":"Delivery Notes","fieldId":"customfield_10002","toString":"On track"}]}]}`)
	js.route(http.MethodGet, "/rest/api/2/issue/PROJ-1/comment", http.StatusOK,
		`{"startAt":0,"total":1,"comments":[{"id":"c1","author":{"displayName":"Ada"},"created":"2026-04-05T00:00:00.000+0000","body":"Evidence"}]}`)
	js.route(http.MethodGet, "/rest/api/2/issue/PROJ-1", http.StatusOK, `{
		"id":"10001","key":"PROJ-1","fields":{"summary":"Deliver capability","status":{"name":"In Progress"},"issuetype":{"name":"Epic"},"description":"Definition","updated":"2026-04-06T00:00:00.000+0000","customfield_10002":"On track","issuelinks":[{"id":"1","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"PROJ-9"}}]}}
	`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"startAt":0,"maxResults":100,"total":1,"issues":[{"id":"10002","key":"PROJ-2","fields":{"summary":"Child","status":{"name":"Done"},"issuetype":{"name":"Task"},"updated":"2026-04-03T00:00:00.000+0000"}}]}
	`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--status-field", "Delivery Notes", "--epic-field", "customfield_10001")
	if code != exitOK {
		t.Fatalf("digest exit=%d output=%s", code, out)
	}
	if !strings.Contains(out, `"stale": true`) || !strings.Contains(out, `"newer_child_updates": 1`) || !strings.Contains(out, `"newer_comments": 1`) {
		t.Fatalf("digest output=%s", out)
	}
	assertGolden(t, "jira_epic_digest.json", []byte(out))
}

func TestJiraEpicDigestTextIsEvidenceNotNarrative(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/PROJ-1", http.StatusOK, `{"key":"PROJ-1","fields":{"summary":"Example","status":{"name":"Open"},"description":"Body"}}`)
	out, code := runCLI(t, jiraEnv(js.srv), "-o", "text", "jira", "epic", "digest", "PROJ-1", "--include", "identity")
	if code != exitOK || !strings.Contains(out, "# PROJ-1 — Example") || !strings.Contains(out, "| Source | Complete | Count | Warning |") {
		t.Fatalf("text exit=%d output=%s", code, out)
	}
}
