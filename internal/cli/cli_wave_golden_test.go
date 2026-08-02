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

// A missing required field reports on stdout but exits 8 (ErrCheckFailed), a
// distinct code so a CI gate can tell "fields missing" from a transport error.
func TestJiraIssueCheck_MissingRequiredExits8(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK, `{"key":"ENG-2","fields":{}}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "check", "ENG-2", "--require", "summary")
	if code != exitCheckFailed {
		t.Fatalf("check with a missing required field: exit %d, want %d (stdout=%q)", code, exitCheckFailed, out)
	}
	if !strings.Contains(out, `"ok": false`) {
		t.Errorf("expected the report (ok:false) on stdout, got %q", out)
	}
}

// A check that would audit zero fields (no --require and --warn explicitly
// emptied) is a silent no-op gate — reject it as a usage error before any
// request rather than always passing ok:true.
func TestJiraIssueCheck_NoFieldsIsUsageError(t *testing.T) {
	js := newJiraServer(t)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "check", "ENG-1", "--warn", "")
	if code != exitUsage {
		t.Fatalf("check with no fields to audit: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Errorf("a no-field check must not contact the server, got %d requests", n)
	}
}

// jira issue delete is preview-first. Legacy direct-write flags and incomplete
// apply invocations fail before configuration/network.
func TestJiraIssueDelete_PreviewsAndRejectsLegacyOrIncompleteApply(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-9", http.StatusOK,
		`{"id":"10009","key":"ENG-9","fields":{"updated":"2026-08-02T20:00:00.000+0000","subtasks":[]}}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "delete", "ENG-9")
	if code != exitOK || !strings.Contains(out, `"status": "would_apply"`) || !strings.Contains(out, `"write_attempted": false`) {
		t.Fatalf("delete preview: exit=%d stdout=%q", code, out)
	}
	if reqs := js.requests(); len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != "/rest/api/2/issue/ENG-9" {
		t.Fatalf("preview requests: %+v", reqs)
	}

	before := len(js.requests())
	_, code = runCLI(t, jiraEnv(js.srv), "jira", "issue", "delete", "ENG-9", "--force")
	if code != exitUsage || len(js.requests()) != before {
		t.Fatalf("legacy --force: exit=%d requests=%d want=%d", code, len(js.requests()), before)
	}

	_, code = runCLI(t, jiraEnv(js.srv), "jira", "issue", "delete", "ENG-9", "--apply")
	if code != exitUsage || len(js.requests()) != before {
		t.Fatalf("incomplete apply: exit=%d requests=%d want=%d", code, len(js.requests()), before)
	}
}

func sawReq(reqs []capturedReq, method, path string) bool {
	for _, r := range reqs {
		if r.method == method && r.path == path {
			return true
		}
	}
	return false
}

// TestJiraMe_Golden pins `jira me` output (the DC username/userkey identity).
func TestJiraMe_Golden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK,
		`{"name":"jdoe","key":"jdoe","displayName":"Jane Doe","emailAddress":"redacted","active":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "me")
	if code != exitOK {
		t.Fatalf("jira me: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_me.json", []byte(out))
}
