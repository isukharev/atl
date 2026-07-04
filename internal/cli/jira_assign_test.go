package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

// assign requires exactly one selector and must not touch the server otherwise.
func TestJiraIssueAssign_SelectorGuard(t *testing.T) {
	js := newJiraServer(t)

	for _, args := range [][]string{
		{"jira", "issue", "assign", "ENG-1"},
		{"jira", "issue", "assign", "ENG-1", "--to", "jdoe", "--me"},
		{"jira", "issue", "assign", "ENG-1", "--me", "--none"},
	} {
		_, code := runCLI(t, jiraEnv(js.srv), args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if n := len(js.requests()); n != 0 {
		t.Fatalf("selector guard must not contact the server, got %d requests", n)
	}
}

func TestJiraIssueAssign_ToPutsAssignee(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNoContent, ``)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "assign", "ENG-1", "--to", "jdoe")
	if code != exitOK {
		t.Fatalf("assign: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-1/assignee")
	if len(writes) != 1 || writes[0].method != http.MethodPut {
		t.Fatalf("expected 1 PUT to /assignee, got %+v", writes)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(writes[0].body), &p); err != nil {
		t.Fatalf("decode body %q: %v", writes[0].body, err)
	}
	if p["name"] != "jdoe" {
		t.Errorf("payload = %v, want name=jdoe", p)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, out)
	}
	if res["status"] != "assigned" || res["assignee"] != "jdoe" || res["key"] != "ENG-1" {
		t.Errorf("result = %v", res)
	}
}

// --me resolves the username via /myself before writing.
func TestJiraIssueAssign_MeResolvesCurrentUser(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK,
		`{"name":"ivan","displayName":"Ivan","active":true}`)
	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNoContent, ``)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "assign", "ENG-1", "--me")
	if code != exitOK {
		t.Fatalf("assign --me: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-1/assignee")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT to /assignee, got %+v", writes)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(writes[0].body), &p)
	if p["name"] != "ivan" {
		t.Errorf("payload = %v, want name=ivan (resolved via /myself)", p)
	}
}

// --none sends an explicit null name and reports "unassigned".
func TestJiraIssueAssign_NoneSendsNull(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNoContent, ``)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "assign", "ENG-1", "--none")
	if code != exitOK {
		t.Fatalf("assign --none: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-1/assignee")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT to /assignee, got %+v", writes)
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal([]byte(writes[0].body), &p); err != nil {
		t.Fatalf("decode body %q: %v", writes[0].body, err)
	}
	if v, ok := p["name"]; !ok || string(v) != "null" {
		t.Errorf("payload = %q, want {\"name\":null}", writes[0].body)
	}
	var res map[string]string
	_ = json.Unmarshal([]byte(out), &res)
	if res["status"] != "unassigned" {
		t.Errorf("result = %v, want status=unassigned", res)
	}
}
