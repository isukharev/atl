package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/mirror"
)

func TestJiraPullCompleteRequiresClosedProjectFlagsBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "pull", "--complete", "--project", "ENG"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--jql", "project=ENG"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--limit", "0"},
		{"jira", "pull", "--jql", "project=ENG", "--restart-complete"},
		{"jira", "pull", "--jql", "project=ENG", "--attachments", "--max-attachments-per-issue", "1"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--attachments"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--attachments", "--max-attachments-per-issue", "1", "--attachment-bodies"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--comments"},
	} {
		if out, code := runCLI(t, map[string]string{}, args...); code != exitUsage || out != "" {
			t.Fatalf("args=%v code=%d stdout=%q", args, code, out)
		}
	}
}

func TestJiraPullCompleteJSONAndText(t *testing.T) {
	for _, output := range []string{"json", "text"} {
		t.Run(output, func(t *testing.T) {
			js := newJiraServer(t)
			searchBody, _ := json.Marshal(map[string]any{
				"issues": []map[string]any{{
					"id": "1042", "key": "ENG-42",
					"fields": map[string]any{"project": map[string]any{"key": "ENG"}},
				}},
				"startAt": 0, "maxResults": 100, "total": 1,
			})
			issueBody, _ := json.Marshal(map[string]any{
				"id": "1042", "key": "ENG-42",
				"fields": map[string]any{
					"summary": "Synthetic issue", "description": "native body",
					"status": map[string]any{"name": "Open"}, "issuetype": map[string]any{"name": "Task"},
					"project": map[string]any{"key": "ENG"},
				},
			})
			js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, string(searchBody))
			js.route(http.MethodGet, "/rest/api/2/issue/1042", http.StatusOK, string(issueBody))
			root := t.TempDir()
			env := jiraEnv(js.srv)
			env["ATL_READ_ONLY"] = "1"
			out, code := runCLI(t, env, "jira", "pull", "--complete", "--project", "ENG", "--max-issues", "1", "--into", root, "-o", output)
			if code != exitOK {
				t.Fatalf("code=%d stdout=%q", code, out)
			}
			if output == "json" {
				var result app.JiraPullResult
				if json.Unmarshal([]byte(out), &result) != nil || result.Complete == nil || !result.Complete.Complete || result.Complete.Total != 1 {
					t.Fatalf("json=%q result=%+v", out, result.Complete)
				}
			} else if !containsAll(out, "complete-pull: complete=true", "ENG-42") {
				t.Fatalf("text=%q", out)
			}
			if got, err := os.ReadFile(filepath.Join(root, "ENG", "ENG-42.wiki")); err != nil || string(got) != "native body" {
				t.Fatalf("wiki=%q err=%v", got, err)
			}
		})
	}
}

func TestJiraPullCompleteCapturesQualifiedCommentsAndAttachmentBodies(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search":
			requests["search"]++
			writeJSON(w, http.StatusOK, `{"issues":[{"id":"1042","key":"ENG-42","fields":{"project":{"key":"ENG"}}}],"startAt":0,"maxResults":100,"total":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042/comment":
			requests["comments"]++
			writeJSON(w, http.StatusOK, `{"startAt":0,"total":1,"comments":[{"id":"5001","author":{"name":"fixture","key":"stable","displayName":"Fixture"},"created":"2026-01-01","updated":"2026-01-01","parentId":"0","body":"comment body"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042" && r.URL.Query().Get("fields") == "attachment":
			requests["attachment"]++
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"attachment":[{"id":"7001","filename":"fixture.bin","mimeType":"application/octet-stream","size":3,"created":"2026-01-01","content":"/secure/attachment/7001/fixture.bin","author":{"name":"fixture","key":"stable","displayName":"Fixture"}}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042" && r.URL.Query().Get("fields") == "updated":
			requests["parent"]++
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"updated":"2026-01-01"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042":
			requests["issue"]++
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"summary":"Synthetic issue","description":"native body","status":{"name":"Open"},"issuetype":{"name":"Task"},"project":{"key":"ENG"},"updated":"2026-01-01","issuelinks":[]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/secure/attachment/7001/fixture.bin":
			requests["body"]++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("abc"))
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			writeJSON(w, http.StatusNotFound, `{}`)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	env := jiraEnv(server)
	env["ATL_READ_ONLY"] = "1"
	out, stderr, execErr := executeCLIRaw(t, env,
		"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "1", "--into", root,
		"--comments", "--max-comment-pages-per-issue", "1", "--max-comments-per-issue", "1",
		"--attachments", "--max-attachments-per-issue", "1",
		"--attachment-bodies", "--attachment-media-type", "application/octet-stream",
		"--max-attachment-bytes", "3", "--max-total-attachment-bytes", "3",
	)
	code := exitOK
	if execErr != nil {
		code = codeFor(execErr)
	}
	if code != exitOK {
		t.Fatalf("code=%d stdout=%q stderr=%q error=%v", code, out, stderr, execErr)
	}
	var result app.JiraPullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Complete == nil || !result.Complete.Complete || result.Complete.Total != 1 {
		t.Fatalf("decode=%v result=%+v", err, result.Complete)
	}
	mu.Lock()
	gotRequests := make(map[string]int, len(requests))
	for key, count := range requests {
		gotRequests[key] = count
	}
	mu.Unlock()
	if gotRequests["search"] != 2 || gotRequests["issue"] != 1 || gotRequests["parent"] != 1 ||
		gotRequests["comments"] != 1 || gotRequests["attachment"] != 2 || gotRequests["body"] != 1 {
		t.Fatalf("requests=%v", gotRequests)
	}

	stem := filepath.Join(root, "ENG", "ENG-42")
	commentsData, err := os.ReadFile(stem + ".comments.json")
	if err != nil {
		t.Fatal(err)
	}
	comments, err := mirror.DecodeJiraCommentsSidecarV1(commentsData)
	if err != nil || !comments.Complete || comments.Count != 1 || comments.ParentID != "1042" {
		t.Fatalf("comments=%+v error=%v", comments, err)
	}
	attachmentsData, err := os.ReadFile(stem + ".attachments.json")
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := mirror.DecodeAttachmentSidecarV1(attachmentsData)
	if err != nil || !attachments.Complete || attachments.Count != 1 ||
		attachments.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("attachments=%+v error=%v", attachments, err)
	}
	bodyPath := filepath.Join(root, filepath.FromSlash(attachments.Attachments[0].Body.Path))
	body, err := os.ReadFile(bodyPath)
	info, statErr := os.Stat(bodyPath)
	if err != nil || statErr != nil || string(body) != "abc" || info.Mode().Perm() != 0o600 {
		t.Fatalf("body=%q read=%v stat=%v mode=%v", body, err, statErr, info.Mode())
	}
}

func TestJiraPullCompleteReportsClosedPartialReason(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{"issues":[],"startAt":0,"maxResults":100,"total":1}`)
	root := t.TempDir()
	env := jiraEnv(js.srv)
	env["ATL_READ_ONLY"] = "1"
	out, code := runCLI(t, env, "jira", "pull", "--complete", "--project", "ENG", "--max-issues", "1", "--into", root)
	if code != exitCheckFailed {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	var result app.JiraPullResult
	if json.Unmarshal([]byte(out), &result) != nil || result.Complete == nil || result.Complete.Complete || result.Complete.PartialReason != "pagination_stalled" || result.Complete.SelectionSHA256 != "" {
		t.Fatalf("json=%q result=%+v", out, result.Complete)
	}
	if _, err := os.Stat(filepath.Join(root, "ENG")); !os.IsNotExist(err) {
		t.Fatalf("partial selection published issue tree: %v", err)
	}
}

func containsAll(value string, values ...string) bool {
	for _, candidate := range values {
		if !strings.Contains(value, candidate) {
			return false
		}
	}
	return true
}
