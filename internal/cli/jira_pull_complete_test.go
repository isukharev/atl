package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestJiraPullCompleteRequiresClosedProjectFlagsBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "pull", "--complete", "--project", "ENG"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--jql", "project=ENG"},
		{"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "10", "--limit", "0"},
		{"jira", "pull", "--jql", "project=ENG", "--restart-complete"},
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
