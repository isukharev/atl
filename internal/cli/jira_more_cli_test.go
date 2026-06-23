package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// These exercise the jira commands that previously had only adapter/app-level
// coverage, pinning the CLI wiring: flags → endpoint → JSON shape / exit code.

func TestJiraIssueLabels_WiresUpdateAndGuards(t *testing.T) {
	js := newJiraServer(t)

	// No --add/--remove is a usage error before any request.
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "labels", "ENG-1")
	if code != exitUsage {
		t.Fatalf("labels with no add/remove: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Fatalf("labels guard must not contact the server, got %d requests", n)
	}

	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNoContent, ``)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "labels", "ENG-1", "--add", "bug,backend", "--remove", "wontfix")
	if code != exitOK {
		t.Fatalf("labels: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-1")
	if len(writes) != 1 || writes[0].method != http.MethodPut {
		t.Fatalf("expected 1 PUT, got %+v", writes)
	}
	// The labels go out via the field-update verb (add/remove ops), not a full PUT.
	var p struct {
		Update struct {
			Labels []map[string]string `json:"labels"`
		} `json:"update"`
	}
	if err := json.Unmarshal([]byte(writes[0].body), &p); err != nil {
		t.Fatalf("decode labels body %q: %v", writes[0].body, err)
	}
	if len(p.Update.Labels) != 3 || p.Update.Labels[0]["add"] != "bug" || p.Update.Labels[2]["remove"] != "wontfix" {
		t.Errorf("label ops = %v, want add bug/backend, remove wontfix", p.Update.Labels)
	}
}

func TestJiraIssueHistory_EmitsChangelog(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","changelog":{"histories":[{"id":"100","author":{"displayName":"Jane"},"created":"2026-06-01","items":[{"field":"status","fromString":"Open","toString":"Done"}]}]}}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "history", "ENG-1")
	if code != exitOK {
		t.Fatalf("history: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		History []domain.ChangelogEntry `json:"history"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode history: %v\n%s", err, out)
	}
	if len(res.History) != 1 || res.History[0].Author != "Jane" || len(res.History[0].Items) != 1 || res.History[0].Items[0].To != "Done" {
		t.Fatalf("history = %+v, want one Jane status→Done entry", res.History)
	}
	// The DC-universal expand=changelog form is used (not the Cloud /changelog).
	var saw bool
	for _, r := range js.requests() {
		if strings.Contains(r.query, "expand=changelog") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected ?expand=changelog on the wire, got %+v", js.requests())
	}
}

func TestJiraIssueCommentList_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-1/comment", http.StatusOK,
		`{"comments":[{"id":"42","author":{"displayName":"Jane"},"created":"2026-06-01","body":"hi"}]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "comment", "list", "ENG-1")
	if code != exitOK {
		t.Fatalf("comment list: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"id": "42"`) || !strings.Contains(out, `"body": "hi"`) {
		t.Errorf("comment list output = %q, want comment 42 'hi'", out)
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "comment", "list", "ENG-1", "-o", "id")
	if code != exitOK {
		t.Fatalf("comment list -o id: exit %d, want 0", code)
	}
	if strings.TrimSpace(idOut) != "42" {
		t.Errorf("comment list -o id = %q, want \"42\"", idOut)
	}
}

func TestJiraIssueCommentDelete_WiresDelete(t *testing.T) {
	js := newJiraServer(t)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "comment", "delete", "ENG-1", "42")
	if code != exitOK {
		t.Fatalf("comment delete: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !sawReq(js.requests(), http.MethodDelete, "/rest/api/2/issue/ENG-1/comment/42") {
		t.Errorf("expected DELETE /rest/api/2/issue/ENG-1/comment/42, got %+v", js.requests())
	}
}

func TestJiraIssueLinkList_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"issuelinks":[{"id":"9","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"ENG-2"}}]}}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "link", "list", "ENG-1")
	if code != exitOK {
		t.Fatalf("link list: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Links []domain.IssueLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode links: %v\n%s", err, out)
	}
	if len(res.Links) != 1 || res.Links[0].ID != "9" || res.Links[0].Key != "ENG-2" {
		t.Fatalf("links = %+v, want one id=9 →ENG-2", res.Links)
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "link", "list", "ENG-1", "-o", "id")
	if code != exitOK {
		t.Fatalf("link list -o id: exit %d, want 0", code)
	}
	if strings.TrimSpace(idOut) != "9" {
		t.Errorf("link list -o id = %q, want \"9\"", idOut)
	}
}

func TestJiraIssueLinkDelete_WiresDelete(t *testing.T) {
	js := newJiraServer(t)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "link", "delete", "9")
	if code != exitOK {
		t.Fatalf("link delete: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !sawReq(js.requests(), http.MethodDelete, "/rest/api/2/issueLink/9") {
		t.Errorf("expected DELETE /rest/api/2/issueLink/9, got %+v", js.requests())
	}
}

func TestJiraUserSearch_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/user/search", http.StatusOK,
		`[{"name":"alice","key":"alice","displayName":"Alice A","emailAddress":"a@x.io","active":true}]`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "user", "search", "alice")
	if code != exitOK {
		t.Fatalf("user search: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Users []domain.User `json:"users"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode users: %v\n%s", err, out)
	}
	if len(res.Users) != 1 || res.Users[0].Name != "alice" {
		t.Fatalf("users = %+v, want one alice", res.Users)
	}
	// DC matches on the username param (not the Cloud query param).
	var saw bool
	for _, r := range js.requests() {
		if strings.Contains(r.query, "username=alice") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected ?username=alice (DC param), got %+v", js.requests())
	}

	idOut, _ := runCLI(t, jiraEnv(js.srv), "jira", "user", "search", "alice", "-o", "id")
	if strings.TrimSpace(idOut) != "alice" {
		t.Errorf("user search -o id = %q, want \"alice\"", idOut)
	}
}

func TestJiraUserGet_Emits(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/user", http.StatusOK,
		`{"name":"alice","key":"alice","displayName":"Alice A","emailAddress":"a@x.io","active":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "user", "get", "alice")
	if code != exitOK {
		t.Fatalf("user get: exit %d, want 0 (stdout=%q)", code, out)
	}
	var u domain.User
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		t.Fatalf("decode user: %v\n%s", err, out)
	}
	if u.Name != "alice" || u.DisplayName != "Alice A" {
		t.Errorf("user = %+v, want alice/Alice A", u)
	}
}
