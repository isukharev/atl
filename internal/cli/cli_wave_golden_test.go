package cli

import (
	"net/http"
	"strings"
	"testing"
)

// TestJiraIssueCheck_Golden pins the `jira issue check` report shape. The issue
// has a summary (required, satisfied) but no priority (warn-only), so the report
// is OK with one warning — fully deterministic (no host data).
func TestJiraIssueCheck_Golden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"summary":"Has a summary"}}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "check", "ENG-1", "--require", "summary", "--warn", "priority")
	if code != exitOK {
		t.Fatalf("issue check: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_issue_check.json", []byte(out))
}

// A missing required field reports on stdout but exits non-zero (CI gate).
func TestJiraIssueCheck_MissingRequiredExitsNonZero(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK, `{"key":"ENG-2","fields":{}}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "check", "ENG-2", "--require", "summary")
	if code == exitOK {
		t.Fatalf("check with a missing required field must exit non-zero; stdout=%q", out)
	}
	if !strings.Contains(out, `"ok": false`) {
		t.Errorf("expected the report (ok:false) on stdout, got %q", out)
	}
}

// TestJiraMe_Golden pins `jira me` output (the DC username/userkey identity).
func TestJiraMe_Golden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK,
		`{"name":"jdoe","key":"jdoe","displayName":"Jane Doe","emailAddress":"j@x.io","active":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "me")
	if code != exitOK {
		t.Fatalf("jira me: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_me.json", []byte(out))
}
