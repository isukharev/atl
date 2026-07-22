package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-1/changelog", http.StatusOK,
		`{"startAt":0,"maxResults":100,"total":1,"values":[{"id":"100","author":{"displayName":"Jane"},"created":"2026-06-01","items":[{"field":"Status","fieldId":"status","fromString":"Open","toString":"Done"}]}]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "history", "ENG-1")
	if code != exitOK {
		t.Fatalf("history: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Complete bool                    `json:"complete"`
		Source   string                  `json:"source"`
		History  []domain.ChangelogEntry `json:"history"`
		Summary  struct {
			HistoryCount    int  `json:"history_count"`
			ItemCount       int  `json:"item_count"`
			StatusItemCount int  `json:"status_item_count"`
			IDsUnique       bool `json:"history_ids_unique"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode history: %v\n%s", err, out)
	}
	if !res.Complete || res.Source != "paginated" || len(res.History) != 1 || res.History[0].Author != "Jane" || len(res.History[0].Items) != 1 || res.History[0].Items[0].FieldID != "status" || res.History[0].Items[0].To != "Done" {
		t.Fatalf("history = %+v, want one Jane status→Done entry", res.History)
	}
	if res.Summary.HistoryCount != 1 || res.Summary.ItemCount != 1 || res.Summary.StatusItemCount != 1 || !res.Summary.IDsUnique {
		t.Fatalf("summary = %+v, want one unique status item", res.Summary)
	}
	// A capable DC backend uses the complete paginated sub-resource.
	var saw bool
	for _, r := range js.requests() {
		if r.path == "/rest/api/2/issue/ENG-1/changelog" && strings.Contains(r.query, "startAt=0") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected paginated changelog on the wire, got %+v", js.requests())
	}
}

func TestJiraIssueHistory_FiltersByFieldNameAndTime(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK, `{"timeZone":"UTC"}`)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK,
		`[{"id":"customfield_10001","name":"Delivery Notes","custom":true,"schema":{"type":"string"}}]`)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-1/changelog", http.StatusOK,
		`{"startAt":0,"maxResults":100,"total":2,"values":[`+
			`{"id":"100","created":"2026-03-31T23:59:59.000+0000","items":[{"field":"Delivery Notes","fieldId":"customfield_10001","toString":"old"}]},`+
			`{"id":"101","created":"2026-04-01T12:00:00.000+0000","items":[{"field":"Delivery Notes","fieldId":"customfield_10001","toString":"current"},{"field":"Status","fieldId":"status","toString":"Done"}]}`+
			`]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "history", "ENG-1", "--field", "Delivery Notes", "--since", "2026-04-01")
	if code != exitOK {
		t.Fatalf("history: exit %d output=%s", code, out)
	}
	if strings.Contains(out, `"old"`) || strings.Contains(out, `"field": "Status"`) || !strings.Contains(out, `"history_id": "101"`) || !strings.Contains(out, `"to": "current"`) {
		t.Fatalf("filtered output=%s", out)
	}
	assertGolden(t, "jira_issue_history_filtered.json", []byte(out))

	text, code := runCLI(t, jiraEnv(js.srv), "-o", "text", "jira", "issue", "history", "ENG-1", "--field", "Delivery Notes", "--since", "2026-04-01")
	if code != exitOK || !strings.Contains(text, "Complete: true") || !strings.Contains(text, "| Created | Author | Field | From | To |") {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
}

func TestJiraIssueCommentList_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-1/comment", http.StatusOK,
		`{"startAt":0,"total":1,"comments":[{"id":"42","author":{"displayName":"Jane"},"created":"2026-06-01","body":"hi"}]}`)

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

func TestJiraIssueLinkSuggestCLIIsReadOnly(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"issuelinks":[{"id":"9","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"ENG-2"}}]}}`)
	csvPath := filepath.Join(t.TempDir(), "links.csv")
	if err := os.WriteFile(csvPath, []byte("source,target,type,rationale\nENG-1,ENG-2,Blocks,exists\nENG-1,ENG-3,Blocks,missing\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "link", "suggest", "--csv", csvPath)
	if code != exitOK {
		t.Fatalf("link suggest: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		PlannedCount int `json:"planned_count"`
		Count        int `json:"count"`
		Candidates   []struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Type   string `json:"type"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode suggest: %v\n%s", err, out)
	}
	if res.PlannedCount != 2 || res.Count != 1 || res.Candidates[0].Target != "ENG-3" {
		t.Fatalf("suggest result = %+v, want only missing ENG-3", res)
	}
	for _, req := range js.requests() {
		if req.method != http.MethodGet {
			t.Fatalf("link suggest sent write request: %+v", js.requests())
		}
	}
}

func TestJiraIssueAttachmentList_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"attachment":[
			{"id":"42","filename":"spec.xlsx","mimeType":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","size":12,"content":"/secure/attachment/42/spec.xlsx"},
			{"id":"43","filename":"diagram.png","mimeType":"image/png","size":99,"content":"/secure/attachment/43/diagram.png"}
		]}}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "list", "ENG-1")
	if code != exitOK {
		t.Fatalf("attachment list: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Key         string              `json:"key"`
		Attachments []domain.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode attachment list: %v\n%s", err, out)
	}
	if res.Key != "ENG-1" || len(res.Attachments) != 2 || res.Attachments[0].ID != "42" || res.Attachments[0].Title != "spec.xlsx" {
		t.Fatalf("attachments = %+v, want spec.xlsx and diagram.png", res)
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "list", "ENG-1", "-o", "id")
	if code != exitOK {
		t.Fatalf("attachment list -o id: exit %d, want 0", code)
	}
	if strings.TrimSpace(idOut) != "42\n43" {
		t.Errorf("attachment list -o id = %q, want ids 42 and 43", idOut)
	}
}

func TestJiraIssueAttachmentGet_DownloadsNonImage(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"attachment":[
			{"id":"42","filename":"spec.xlsx","mimeType":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","size":9,"content":"/secure/attachment/42/spec.xlsx"}
		]}}`)
	js.route(http.MethodGet, "/secure/attachment/42/spec.xlsx", http.StatusOK, "xlsx-data")

	dir := t.TempDir()
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "get", "ENG-1", "--id", "42", "--into", dir)
	if code != exitOK {
		t.Fatalf("attachment get: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode attachment get: %v\n%s", err, out)
	}
	if res["name"] != "spec.xlsx" || res["id"] != "42" || res["key"] != "ENG-1" {
		t.Fatalf("result = %v, want key/id/name", res)
	}
	data, err := os.ReadFile(filepath.Join(dir, "spec.xlsx"))
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(data) != "xlsx-data" {
		t.Fatalf("downloaded data = %q, want xlsx-data", data)
	}
	if !sawReq(js.requests(), http.MethodGet, "/secure/attachment/42/spec.xlsx") {
		t.Fatalf("expected attachment download request, got %+v", js.requests())
	}
}

func TestJiraIssueAttachmentGet_RequiresIDBeforeNetwork(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "get", "ENG-1")
	if code != exitUsage {
		t.Fatalf("attachment get without --id: exit %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("attachment get guard must not contact the server, got %+v", js.requests())
	}
}

func TestJiraIssueAttachmentUpload_WiresMultipart(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/api/2/issue/ENG-1/attachments", http.StatusOK,
		`[{"id":"44","filename":"report.xlsx","mimeType":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","size":10,"content":"/secure/attachment/44/report.xlsx"}]`)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "report.xlsx")
	if err := os.WriteFile(filePath, []byte("xlsx bytes"), 0o644); err != nil {
		t.Fatalf("write upload file: %v", err)
	}

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "upload", "ENG-1", "--file", filePath)
	if code != exitOK {
		t.Fatalf("attachment upload: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Key        string             `json:"key"`
		Attachment *domain.Attachment `json:"attachment"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode attachment upload: %v\n%s", err, out)
	}
	if res.Key != "ENG-1" || res.Attachment == nil || res.Attachment.ID != "44" || res.Attachment.Title != "report.xlsx" {
		t.Fatalf("result = %+v, want uploaded attachment", res)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-1/attachments")
	if len(writes) != 1 || writes[0].method != http.MethodPost {
		t.Fatalf("expected one POST attachment upload, got %+v", writes)
	}
	if !strings.Contains(writes[0].body, `name="file"; filename="report.xlsx"`) || !strings.Contains(writes[0].body, "xlsx bytes") {
		t.Fatalf("upload body does not contain multipart file field and data: %q", writes[0].body)
	}
}

func TestJiraIssueAttachmentUpload_RequiresFileBeforeNetwork(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "attachment", "upload", "ENG-1")
	if code != exitUsage {
		t.Fatalf("attachment upload without --file: exit %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("attachment upload guard must not contact the server, got %+v", js.requests())
	}
}

func TestJiraIssuePlanApplyDryRunAndConfirmGuard(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"issuelinks":[],"labels":["backend"],"priority":"Low","updated":"2026-01-01"}}`)
	js.route(http.MethodGet, "/rest/api/2/issueLinkType", http.StatusOK,
		`{"issueLinkTypes":[{"name":"Blocks"}]}`)
	csvPath := filepath.Join(t.TempDir(), "plan.csv")
	if err := os.WriteFile(csvPath, []byte("version,op,source,target,type,field,value,expected_updated\n1,link,ENG-1,ENG-2,Blocks,,,2026-01-01\n1,field,ENG-3,,,priority,High,2026-01-01\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "plan", "apply", "--csv", csvPath, "--allow-ops", "link,field", "--allow-fields", "priority")
	if code != exitOK {
		t.Fatalf("plan apply dry-run: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Mode    string `json:"mode"`
		Results []struct {
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode plan result: %v\n%s", err, out)
	}
	if res.Mode != "dry-run" || len(res.Results) != 2 || res.Results[0].Status != "would_apply" || res.Results[1].Status != "would_apply" {
		t.Fatalf("plan result = %+v, want two would_apply dry-run rows", res)
	}
	for _, req := range js.requests() {
		if req.method != http.MethodGet {
			t.Fatalf("plan dry-run sent write request: %+v", js.requests())
		}
	}

	js = newJiraServer(t)
	_, code = runCLI(t, jiraEnv(js.srv), "jira", "issue", "plan", "apply", "--csv", csvPath, "--apply")
	if code != exitUsage {
		t.Fatalf("plan apply without confirm exit = %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("confirm guard should run before network, got %+v", js.requests())
	}
}

func TestJiraIssuePlanApplyEmitsAuditResultAndExitsEightOnBlockedRow(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"key":"ENG-1","fields":{"priority":"Low","updated":"new"}}`)
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "version,op,source,field,value,expected_updated\n1,field,ENG-1,priority,High,old\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLIFull(t, jiraEnv(js.srv), "jira", "issue", "plan", "apply",
		"--csv", path, "--allow-ops", "field", "--allow-fields", "priority")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 8", code, out, stderr)
	}
	var res struct {
		Version int `json:"version"`
		Results []struct {
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("blocked result is not JSON: %v\n%s", err, out)
	}
	if res.Version != 1 || len(res.Results) != 1 || res.Results[0].Status != "blocked" {
		t.Fatalf("result=%+v, want version 1 blocked audit row", res)
	}
	if len(js.writeReqsTo("/rest/api/2/issue/ENG-1")) != 0 {
		t.Fatalf("blocked preview wrote to Jira: %+v", js.requests())
	}
}

func TestJiraUserSearch_EmitsAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/user/search", http.StatusOK,
		`[{"name":"alice","key":"alice","displayName":"Alice A","emailAddress":"redacted","active":true}]`)

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
		`{"name":"alice","key":"alice","displayName":"Alice A","emailAddress":"redacted","active":true}`)

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
