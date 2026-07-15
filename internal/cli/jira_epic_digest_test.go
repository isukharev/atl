package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func epicDigestServer(t *testing.T) *jiraServer {
	t.Helper()
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK, `{"timeZone":"UTC"}`)
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
	return js
}

func TestJiraEpicDigestGolden(t *testing.T) {
	js := epicDigestServer(t)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--status-field", "Delivery Notes", "--epic-field", "customfield_10001")
	if code != exitOK {
		t.Fatalf("digest exit=%d output=%s", code, out)
	}
	if !strings.Contains(out, `"stale": true`) || !strings.Contains(out, `"newer_child_updates": 1`) || !strings.Contains(out, `"newer_comments": 1`) {
		t.Fatalf("digest output=%s", out)
	}
	assertGolden(t, "jira_epic_digest.json", []byte(out))
}

// TestEvidenceFirstEpicWorkflowBudget is a deterministic agent-contract
// benchmark, not a wall-clock microbenchmark. It pins the first-use workflow
// (discover non-empty fields, then request the aggregate digest) to a bounded
// number of read-only backend calls and a bounded context payload.
func TestEvidenceFirstEpicWorkflowBudget(t *testing.T) {
	js := epicDigestServer(t)
	env := jiraEnv(js.srv)
	fields, code := runCLI(t, env, "--read-only", "jira", "issue", "fields", "PROJ-1")
	if code != exitOK {
		t.Fatalf("fields exit=%d output=%s", code, fields)
	}
	digest, code := runCLI(t, env, "--read-only", "jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--status-field", "Delivery Notes", "--epic-field", "customfield_10001")
	if code != exitOK {
		t.Fatalf("digest exit=%d output=%s", code, digest)
	}
	var decoded struct {
		Sources map[string]struct {
			Complete bool `json:"complete"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(digest), &decoded); err != nil {
		t.Fatal(err)
	}
	sourcesComplete := true
	for _, source := range []string{"identity", "status-field", "children", "comments", "links", "history", "refs"} {
		value, ok := decoded.Sources[source]
		if !ok || !value.Complete {
			sourcesComplete = false
		}
	}
	evaluateAgentWorkflow(t, "jira-epic-evidence.v1.json", deterministicObservation(
		"jira.epic-evidence", 2, int64(len(fields)+len(digest)), js.requests(),
		map[string]bool{
			"answer_correct":   strings.Contains(digest, `"key": "PROJ-1"`),
			"sources_complete": sourcesComplete,
		},
	))
}

func TestJiraEpicDigestTextIsEvidenceNotNarrative(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/PROJ-1", http.StatusOK, `{"key":"PROJ-1","fields":{"summary":"Example","status":{"name":"Open"},"description":"Body"}}`)
	out, code := runCLI(t, jiraEnv(js.srv), "-o", "text", "jira", "epic", "digest", "PROJ-1", "--include", "identity")
	if code != exitOK || !strings.Contains(out, "# PROJ-1 — Example") || !strings.Contains(out, "| Source | Complete | Count | Warning |") {
		t.Fatalf("text exit=%d output=%s", code, out)
	}
}
