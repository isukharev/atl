package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueDeleteOutputFailureAfterAttemptIsNoReplay(t *testing.T) {
	var mu sync.Mutex
	deleted := false
	deleteCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/rest/api/2/issue/PROJ-1" || r.URL.Path == "/rest/api/2/issue/10001"):
			if deleted {
				http.Error(w, `{"errorMessages":["private"]}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"10001","key":"PROJ-1","fields":{"updated":"2026-08-02T20:00:00.000+0000","subtasks":[]}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/rest/api/2/issue/10001":
			deleteCalls++
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "delete", "PROJ-1")
	if code != exitOK {
		t.Fatalf("preview exit=%d stdout=%q", code, out)
	}
	var preview app.JiraIssueDeleteResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil || preview.ProposalHash == "" || preview.CurrentUpdated == "" {
		t.Fatalf("decode preview: result=%+v err=%v", preview, err)
	}

	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, jiraEnv(srv), cause,
		"jira", "issue", "delete", "PROJ-1", "--apply", "--confirm", "DELETE",
		"--expected-updated", preview.CurrentUpdated, "--expected-proposal-hash", preview.ProposalHash)
	mu.Lock()
	calls := deleteCalls
	mu.Unlock()
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") || calls != 1 {
		t.Fatalf("error=%v delete_calls=%d", err, calls)
	}
}

func TestJiraIssueDeleteInvocationFailsBeforeConfiguration(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"jira", "issue", "delete", "proj-1"}, "issue key must be canonical"},
		{[]string{"jira", "issue", "delete", "PROJ-1", "--confirm", "DELETE"}, "require --apply"},
		{[]string{"jira", "issue", "delete", "PROJ-1", "--apply"}, "--confirm is required"},
		{[]string{"jira", "issue", "delete", "PROJ-1", "--force"}, "unknown flag"},
		{[]string{"jira", "issue", "delete", "PROJ-1", "--apply", "--confirm", "DELETE", "--expected-updated", "not-a-time", "--expected-proposal-hash", strings.Repeat("a", 64)}, "exact supported Jira timestamp"},
		{[]string{"jira", "issue", "delete", "PROJ-1", "--apply", "--confirm", "DELETE", "--expected-updated", "2026-08-02T20:00:00.000+0000", "--expected-proposal-hash", strings.Repeat("A", 64)}, "lowercase 64-character SHA-256"},
	}
	for _, test := range tests {
		out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"}, test.args...)
		if codeFor(err) != exitUsage || out != "" || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args=%v error=%v stdout=%q", test.args, err, out)
		}
	}
}
