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
	"github.com/isukharev/atl/internal/config"
)

func TestJiraPull_EpicChildrenSidecar(t *testing.T) {
	var mu sync.Mutex
	searches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/field":
			_, _ = w.Write([]byte(`[{"id":"customfield_10010","name":"Epic Link","custom":true,"schema":{"type":"any"}}]`))
		case "/rest/api/2/search":
			mu.Lock()
			searches++
			mu.Unlock()
			if strings.Contains(r.URL.Query().Get("jql"), "cf[10010]") {
				_, _ = w.Write([]byte(`{"issues":[
                  {"id":"2","key":"PROJ-2","fields":{"summary":"First child","status":{"name":"Open"},"issuetype":{"name":"Story"},"assignee":{"displayName":"alice"},"customfield_10010":"PROJ-1"}},
                  {"id":"3","key":"PROJ-3","fields":{"summary":"Second child","status":{"name":"Done"},"issuetype":{"name":"Task"},"customfield_10010":"PROJ-1"}}
                ],"startAt":0,"maxResults":100,"total":2}`))
				return
			}
			_, _ = w.Write([]byte(`{"issues":[{"id":"1","key":"PROJ-1","fields":{"summary":"Epic","description":"body","status":{"name":"Open"},"issuetype":{"name":"Epic"},"project":{"key":"PROJ"}}}],"startAt":0,"maxResults":100,"total":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	into := t.TempDir()
	if err := config.SaveLocal(into, &config.LocalConfig{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile: "full", Include: []string{app.SecEpicChildren},
	}}}); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLIFull(t, jiraEnv(srv), "jira", "pull", "--jql", "key = PROJ-1", "--into", into, "--limit", "1")
	if code != exitOK || stderr != "" {
		t.Fatalf("pull: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	mu.Lock()
	gotSearches := searches
	mu.Unlock()
	if gotSearches != 2 {
		t.Fatalf("search requests = %d, want main + one related query", gotSearches)
	}
	normalized := strings.ReplaceAll(out, into, "<INTO>")
	assertGolden(t, "jira_pull_epic_children.json", []byte(normalized))

	sidecarPath := filepath.Join(into, "PROJ", "PROJ-1.epic-children.json")
	b, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	var sidecar app.JiraEpicChildrenSidecar
	if err := json.Unmarshal(b, &sidecar); err != nil || len(sidecar.Children) != 2 {
		t.Fatalf("sidecar: err=%v value=%+v", err, sidecar)
	}
	md, err := os.ReadFile(filepath.Join(into, "PROJ", "PROJ-1.md"))
	if err != nil || !strings.Contains(string(md), "## Epic Children") {
		t.Fatalf("epic section missing: err=%v md=%s", err, md)
	}
}
