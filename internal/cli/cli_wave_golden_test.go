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

// jira issue delete refuses without --force (exit 2, no request) and DELETEs the
// right path with --force.
func TestJiraIssueDelete_RequiresForceAndWiresDelete(t *testing.T) {
	js := newJiraServer(t)

	_, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "delete", "ENG-9")
	if code != exitUsage {
		t.Fatalf("delete without --force: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Fatalf("delete without --force must not contact the server, got %d requests", n)
	}

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "delete", "ENG-9", "--force")
	if code != exitOK {
		t.Fatalf("delete --force: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !sawReq(js.requests(), http.MethodDelete, "/rest/api/2/issue/ENG-9") {
		t.Errorf("expected DELETE /rest/api/2/issue/ENG-9, got %+v", js.requests())
	}
}

// conf attachment delete refuses without --force and DELETEs the content id.
func TestConfAttachmentDelete_RequiresForceAndWiresDelete(t *testing.T) {
	cs := newConfServer(t)

	_, code := runCLI(t, confEnv(cs.srv), "conf", "attachment", "delete", "--id", "att123")
	if code != exitUsage {
		t.Fatalf("attachment delete without --force: exit %d, want %d", code, exitUsage)
	}
	if n := len(cs.requests()); n != 0 {
		t.Fatalf("attachment delete without --force must not contact the server, got %d requests", n)
	}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "attachment", "delete", "--id", "att123", "--force")
	if code != exitOK {
		t.Fatalf("attachment delete --force: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !sawReq(cs.requests(), http.MethodDelete, "/rest/api/content/att123") {
		t.Errorf("expected DELETE /rest/api/content/att123, got %+v", cs.requests())
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
		`{"name":"jdoe","key":"jdoe","displayName":"Jane Doe","emailAddress":"j@x.io","active":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "me")
	if code != exitOK {
		t.Fatalf("jira me: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_me.json", []byte(out))
}
