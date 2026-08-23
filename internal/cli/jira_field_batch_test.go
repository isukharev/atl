package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

func TestJiraIssueFieldBatchGoldenAndJSONOnlyContract(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK, `[
  {"id":"customfield_1","name":"Delivery Notes","custom":true,"schema":{"type":"string"}},
  {"id":"customfield_2","name":"Empty","custom":true,"schema":{"type":"array"}}
]`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
  "startAt":0,"maxResults":25,"total":1,
  "issues":[{"id":"10001","key":"PROJ-1","fields":{"updated":"2026-08-20T12:00:00Z","customfield_1":"Current evidence","customfield_2":[]}}]
}`)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "field", "batch",
		"--key", "PROJ-2", "--key", "PROJ-1", "--field", "Delivery Notes", "--field", "customfield_2")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	assertGolden(t, "jira_issue_field_batch.json", []byte(out))

	requests := js.requests()
	if len(requests) != 2 || requests[0].path != "/rest/api/2/field" || requests[1].path != "/rest/api/2/search" {
		t.Fatalf("requests=%+v", requests)
	}
	query, err := url.ParseQuery(requests[1].query)
	if err != nil || query.Get("jql") != `key in ("PROJ-2","PROJ-1") ORDER BY key ASC` ||
		query.Get("fields") != "customfield_1,customfield_2,updated" || query.Get("maxResults") != "25" {
		t.Fatalf("query=%v err=%v", query, err)
	}

	before := len(requests)
	textOut, textCode := runCLI(t, jiraEnv(js.srv), "-o", "text", "jira", "issue", "field", "batch",
		"--key", "PROJ-1", "--field", "Delivery Notes")
	if textCode != exitUsage || textOut != "" || len(js.requests()) != before {
		t.Fatalf("text exit=%d output=%q requests=%d->%d", textCode, textOut, before, len(js.requests()))
	}
}

func TestJiraIssueFieldBatchRejectsBoundsBeforeBackendSetup(t *testing.T) {
	tests := [][]string{
		{"jira", "issue", "field", "batch", "--field", "summary"},
		{"jira", "issue", "field", "batch", "--key", "proj-1", "--field", "summary"},
		{"jira", "issue", "field", "batch", "--key", " PROJ-1", "--field", "summary"},
		{"jira", "issue", "field", "batch", "--key", "PROJ-1", "--field", "summary", "--field", "summary"},
		{"jira", "issue", "field", "batch", "--key", "PROJ-1", "--field", " summary"},
		{"jira", "issue", "field", "batch", "--key", "PROJ-1", "--field", "summary" + strings.Repeat(" ", 1024)},
		{"jira", "issue", "field", "batch", "--key", "PROJ-1", "--field", strings.Repeat("x", 1025)},
	}
	for index, args := range tests {
		out, code := runCLI(t, nil, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("case %d exit=%d output=%q", index, code, out)
		}
	}
}

func TestJiraIssueFieldBatchArgsFailBeforeMalformedConfigAndSelfUpdate(t *testing.T) {
	var updateRequests, jiraRequests atomic.Int64
	updateServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { updateRequests.Add(1) }))
	t.Cleanup(updateServer.Close)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { jiraRequests.Add(1) }))
	t.Cleanup(jiraServer.Close)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	malformed := []byte(`{"read_only":`)
	if err := os.WriteFile(configPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	oldVersion := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })
	for _, key := range []string{
		"ATL_NO_UPDATE", "ATL_READ_ONLY", "ATL_UPDATE_DEBUG", "ATL_VERBOSE", "ATL_ALLOW_INSECURE",
		"ATL_JIRA_URL", "JIRA_URL", "ATL_JIRA_PAT", "JIRA_PAT",
		"ATL_POLICY", "ATL_POLICY_FILE", "ATL_POLICY_SHA256", "ATL_POLICY_REQUIRED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ATL_CONFIG_DIR", configDir)
	t.Setenv("ATL_UPDATE_URL", updateServer.URL)
	t.Setenv("ATL_JIRA_URL", jiraServer.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")

	var stdout, stderr bytes.Buffer
	root := newRoot()
	setRootExecutionArgs(root, []string{"jira", "issue", "field", "batch", "--key", " PROJ-1", "--field", "summary"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.ExecuteContext(context.Background())
	if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("err=%v code=%d stdout=%q stderr=%q", err, codeFor(err), stdout.String(), stderr.String())
	}
	if updateRequests.Load() != 0 || jiraRequests.Load() != 0 {
		t.Fatalf("update/Jira requests=%d/%d", updateRequests.Load(), jiraRequests.Load())
	}
	if _, statErr := os.Stat(filepath.Join(configDir, ".update-check")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("self-update stamp stat error=%v, want absent", statErr)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil || !bytes.Equal(after, malformed) {
		t.Fatalf("malformed config changed: %q err=%v", after, readErr)
	}
}
