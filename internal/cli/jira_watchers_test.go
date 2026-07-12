package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueWatchersListPreviewAndApply(t *testing.T) {
	watchers := []domain.IssueWatcher{{Name: "alice", Key: "user-1", DisplayName: "Alice", Active: true}}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/rest/api/2/issue/PROJ-1/watchers" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"watchCount": len(watchers), "isWatching": false, "watchers": watchers})
		case http.MethodPost:
			writes++
			var username string
			if err := json.NewDecoder(request.Body).Decode(&username); err != nil {
				t.Fatal(err)
			}
			watchers = append(watchers, domain.IssueWatcher{Name: username, DisplayName: username, Active: true})
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	t.Cleanup(server.Close)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "watchers", "list", "PROJ-1")
	if code != exitOK || !strings.Contains(out, `"complete": true`) || !strings.Contains(out, `"name": "alice"`) {
		t.Fatalf("list exit=%d out=%s", code, out)
	}
	out, code = runCLI(t, jiraEnv(server), "jira", "issue", "watchers", "add", "PROJ-1", "--username", " bob ")
	if code != exitOK || writes != 0 {
		t.Fatalf("preview exit=%d writes=%d out=%s", code, writes, out)
	}
	assertGolden(t, "jira_issue_watchers_add_preview.json", []byte(out))
	var preview struct {
		Status       string `json:"status"`
		Username     string `json:"username"`
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil || preview.Status != "would_apply" || preview.Username != "bob" || preview.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	out, code = runCLI(t, jiraEnv(server), "jira", "issue", "watchers", "add", "PROJ-1", "--username", "bob", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || writes != 1 || !strings.Contains(out, `"status": "applied"`) {
		t.Fatalf("apply exit=%d writes=%d out=%s", code, writes, out)
	}
}

func TestJiraIssueWatchersMeAndReadOnlyPolicy(t *testing.T) {
	requests, deletes := 0, 0
	watchers := []domain.IssueWatcher{{Name: "current", DisplayName: "Current", Active: true}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/2/myself":
			_, _ = io.WriteString(w, `{"name":"current","displayName":"Current","active":true}`)
		case "/rest/api/2/issue/PROJ-1/watchers":
			switch request.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{"watchCount": len(watchers), "watchers": watchers})
			case http.MethodDelete:
				deletes++
				if request.URL.Query().Get("username") != "current" {
					t.Fatalf("username query=%q", request.URL.Query().Get("username"))
				}
				watchers = nil
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Fatalf("path=%s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	previewOut, code := runCLI(t, jiraEnv(server), "jira", "issue", "watchers", "remove", "PROJ-1", "--me")
	if code != exitOK || !strings.Contains(previewOut, `"identity_source": "me"`) {
		t.Fatalf("me preview exit=%d out=%s", code, previewOut)
	}
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "watchers", "remove", "PROJ-1", "--me", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || deletes != 1 || !strings.Contains(out, `"status": "applied"`) {
		t.Fatalf("remove exit=%d deletes=%d out=%s", code, deletes, out)
	}
	before := requests
	if _, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "issue", "watchers", "add", "PROJ-1", "--username", "blocked", "--apply", "--expected-proposal-hash", "x"); code != exitCheckFailed || requests != before {
		t.Fatalf("read-only exit=%d requests=%d->%d", code, before, requests)
	}
}

func TestJiraIssueWatchersRequiresOneIdentityBeforeCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "watchers", "add", "PROJ-1"},
		{"jira", "issue", "watchers", "add", "PROJ-1", "--username", "alice", "--me"},
		{"jira", "issue", "watchers", "add", "PROJ-1", "--username", "alice", "--apply"},
	} {
		if _, code := runCLI(t, nil, args...); code != exitUsage {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}
}
