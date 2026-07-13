package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJiraIssueWorklogListPreviewAndApply(t *testing.T) {
	worklogs := []map[string]any{{
		"id": "10", "issueId": "10001", "comment": "first | line\ncontinued",
		"started": "2026-07-01T09:00:00.000+0000", "timeSpent": "30m", "timeSpentSeconds": 1800,
		"author": map[string]any{
			"name": "alice", "key": "user-1", "displayName": "Alice", "active": true,
			"emailAddress": "private@example.test", "avatarUrls": map[string]any{"48x48": "https://avatar.invalid/private"},
		},
	}}
	requests, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/2/myself":
			_, _ = io.WriteString(w, `{"name":"alice","key":"user-1","displayName":"Alice","emailAddress":"private@example.test","avatarUrls":{"48x48":"https://avatar.invalid/private"},"active":true}`)
		case "/rest/api/2/issue/PROJ-1/worklog":
			switch request.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"startAt": 0, "maxResults": 100, "total": len(worklogs), "worklogs": worklogs,
				})
			case http.MethodPost:
				writes++
				if got := request.URL.Query().Get("adjustEstimate"); got != "leave" {
					t.Fatalf("adjustEstimate=%q", got)
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				created := map[string]any{
					"id": "11", "issueId": "10001", "comment": payload["comment"], "started": payload["started"],
					"timeSpent": "1h 30m", "timeSpentSeconds": payload["timeSpentSeconds"],
					"author": map[string]any{"name": "alice", "key": "user-1", "displayName": "Alice", "active": true},
				}
				worklogs = append(worklogs, created)
				_ = json.NewEncoder(w).Encode(created)
			default:
				t.Fatalf("method=%s", request.Method)
			}
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	for _, format := range []string{"json", "text", "id"} {
		out, code := runCLI(t, jiraEnv(server), "-o", format, "jira", "issue", "worklog", "list", "PROJ-1")
		if code != exitOK {
			t.Fatalf("list %s exit=%d out=%s", format, code, out)
		}
		if strings.Contains(out, "private@example.test") || strings.Contains(out, "avatar.invalid") {
			t.Fatalf("list %s leaked transport PII: %s", format, out)
		}
		switch format {
		case "json":
			if !strings.Contains(out, `"complete": true`) || !strings.Contains(out, `"id": "10"`) {
				t.Fatalf("json=%s", out)
			}
		case "text":
			if !strings.Contains(out, "| ID | Time | Started | Author | Comment |") || !strings.Contains(out, `first \| line continued`) {
				t.Fatalf("text=%s", out)
			}
		case "id":
			if out != "10\n" {
				t.Fatalf("id=%q", out)
			}
		}
	}

	previewOut, code := runCLI(t, jiraEnv(server), "jira", "issue", "worklog", "add", "PROJ-1",
		"--time", "1h30m", "--comment", "implemented", "--started", "2026-07-01T10:00:00Z")
	if code != exitOK || writes != 0 {
		t.Fatalf("preview exit=%d writes=%d out=%s", code, writes, previewOut)
	}
	if strings.Contains(previewOut, "private@example.test") || strings.Contains(previewOut, "avatar.invalid") {
		t.Fatalf("preview leaked transport PII: %s", previewOut)
	}
	var preview struct {
		Status       string `json:"status"`
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil || preview.Status != "would_apply" || preview.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	assertGolden(t, "jira_issue_worklog_add_preview.json", []byte(previewOut))
	applyOut, code := runCLI(t, jiraEnv(server), "jira", "issue", "worklog", "add", "PROJ-1",
		"--time", "1h30m", "--comment", "implemented", "--started", "2026-07-01T10:00:00Z",
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || writes != 1 || !strings.Contains(applyOut, `"status": "applied"`) || !strings.Contains(applyOut, `"id": "11"`) {
		t.Fatalf("apply exit=%d writes=%d out=%s", code, writes, applyOut)
	}
	if requests == 0 {
		t.Fatal("expected backend requests")
	}
}

func TestJiraIssueWorklogPreflightAndReadOnlyPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "worklog", "add", "PROJ-1"},
		{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "1d"},
		{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "1h", "--comment", "x", "--from-file", "missing"},
		{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "1h", "--apply"},
	} {
		if _, code := runCLI(t, nil, args...); code != exitUsage {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	if _, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "issue", "worklog", "add", "PROJ-1",
		"--time", "1h", "--apply", "--expected-proposal-hash", "reviewed", "--from-file", "/definitely/missing"); code != exitCheckFailed || requests != 0 {
		t.Fatalf("read-only exit=%d requests=%d", code, requests)
	}
}
