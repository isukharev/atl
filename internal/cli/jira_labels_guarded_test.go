package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type guardedLabelCLIServer struct {
	server *httptest.Server
	mu     sync.Mutex
	put    bool
	reads  int
	writes int
}

func newGuardedLabelCLIServer(t *testing.T) *guardedLabelCLIServer {
	fixture := &guardedLabelCLIServer{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			fixture.reads++
			labels, updated := `["old","private-current"]`, "2026-08-22T10:00:00Z"
			if fixture.put {
				labels, updated = `["new","private-current"]`, "2026-08-22T10:00:01Z"
			}
			_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":`+labels+`,"updated":"`+updated+`"}}`)
		case http.MethodPut:
			fixture.writes++
			fixture.put = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *guardedLabelCLIServer) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads, s.writes
}

func TestJiraGuardedLabelsParentChildApplyAndSchema(t *testing.T) {
	fixture := newGuardedLabelCLIServer(t)
	env := jiraEnv(fixture.server)
	parentOut, code := runCLI(t, env, "jira", "issue", "labels", "OPS-1", "--add", "new", "--remove", "old")
	if code != exitOK {
		t.Fatalf("parent preview exit=%d output=%s", code, parentOut)
	}
	childOut, code := runCLI(t, env, "jira", "issue", "labels", "preview", "OPS-1", "--add", "new", "--remove", "old")
	if code != exitOK || childOut != parentOut {
		t.Fatalf("child preview exit=%d\nparent=%s\nchild=%s", code, parentOut, childOut)
	}
	if strings.Contains(parentOut, "private-current") {
		t.Fatalf("unrelated current label leaked: %s", parentOut)
	}
	var preview app.JiraGuardedLabelResult
	if err := json.Unmarshal([]byte(parentOut), &preview); err != nil || preview.Status != "would_apply" || preview.Mode != "preview" || !preview.Complete || preview.ProposalHash == "" || preview.Usage.Requests != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	assertGuardedLabelJSONKeys(t, parentOut, []string{
		"add", "backend_sha256", "bounds", "complete", "current", "desired", "effective_add", "effective_remove", "issue_id", "key", "mode", "operation", "project", "proposal_hash", "reconciled", "remove", "requested_key", "schema_version", "status", "updated", "usage", "write_attempted",
	})
	applyOut, code := runCLI(t, env, "jira", "issue", "labels", "OPS-1", "--add", "new", "--remove", "old", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK {
		t.Fatalf("apply exit=%d output=%s", code, applyOut)
	}
	if strings.Contains(applyOut, "private-current") {
		t.Fatalf("unrelated current label leaked after apply: %s", applyOut)
	}
	var applied app.JiraGuardedLabelResult
	if err := json.Unmarshal([]byte(applyOut), &applied); err != nil || applied.Status != "applied" || !applied.Complete || !applied.WriteAttempted || !applied.Reconciled || applied.Usage.Requests != 4 || applied.Usage.ResponseBytes > applied.Bounds.MaxResponseBytes {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	reads, writes := fixture.counts()
	if reads != 5 || writes != 1 {
		t.Fatalf("reads=%d writes=%d", reads, writes)
	}
}

func TestJiraGuardedLabelsStdoutFailureAfterDispatchIsNoReplay(t *testing.T) {
	fixture := newGuardedLabelCLIServer(t)
	env := jiraEnv(fixture.server)
	previewOut, code := runCLI(t, env, "jira", "issue", "labels", "preview", "OPS-1", "--add", "new", "--remove", "old")
	var preview app.JiraGuardedLabelResult
	if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil {
		t.Fatalf("preview exit=%d output=%s", code, previewOut)
	}
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, env, cause, "jira", "issue", "labels", "OPS-1", "--add", "new", "--remove", "old", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	reads, writes := fixture.counts()
	if codeFor(err) != exitCheckFailed || !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || reads != 4 || writes != 1 {
		t.Fatalf("err=%v code=%d reads=%d writes=%d", err, codeFor(err), reads, writes)
	}
}

func TestJiraGuardedLabelsAccessOutputAndPurePreConfig(t *testing.T) {
	root := newRoot()
	for _, item := range []struct{ path, access string }{
		{"jira issue labels", "mutating"}, {"jira issue labels preview", "read-only"},
	} {
		command, _, err := root.Find(strings.Fields(item.path))
		if err != nil || command.Annotations[accessAnnotation] != item.access {
			t.Fatalf("%s command=%v err=%v access=%q", item.path, command, err, command.Annotations[accessAnnotation])
		}
		if command.Annotations[textOutputAnnotation] != "unsupported" || command.Annotations[idOutputAnnotation] != "unsupported" {
			t.Fatalf("%s admitted non-JSON output", item.path)
		}
	}
	for _, args := range [][]string{
		{"jira", "issue", "labels", "OPS-1"},
		{"jira", "issue", "labels", "OPS-" + strings.Repeat("1", 64), "--add", "one"},
		{"jira", "issue", "labels", "OPS-1", "--add", ""},
		{"jira", "issue", "labels", "OPS-1", "--add", "one,,two"},
		{"jira", "issue", "labels", "OPS-1", "--add", "one", "--remove", "one"},
		{"jira", "issue", "labels", "OPS-1", "--add", "one", "--expected-proposal-hash", strings.Repeat("a", 64)},
		{"jira", "issue", "labels", "OPS-1", "--add", "one", "--apply"},
		{"jira", "issue", "labels", "OPS-1", "--add", "one", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)},
	} {
		out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"}, args...)
		if codeFor(err) != exitUsage || out != "" {
			t.Fatalf("args=%v output=%q err=%v code=%d", args, out, err, codeFor(err))
		}
	}
	fixture := newGuardedLabelCLIServer(t)
	out, code := runCLI(t, jiraEnv(fixture.server), "--read-only", "jira", "issue", "labels", "preview", "OPS-1", "--add", "new")
	if code != exitOK || !strings.Contains(out, `"status": "would_apply"`) {
		t.Fatalf("read-only child exit=%d output=%s", code, out)
	}
	reads, writes := fixture.counts()
	_, code = runCLI(t, jiraEnv(fixture.server), "--read-only", "jira", "issue", "labels", "OPS-1", "--add", "new", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64))
	newReads, newWrites := fixture.counts()
	if code != exitCheckFailed || reads != newReads || writes != newWrites {
		t.Fatalf("read-only apply exit=%d before=%d/%d after=%d/%d", code, reads, writes, newReads, newWrites)
	}
}

func assertGuardedLabelJSONKeys(t *testing.T, encoded string, want []string) {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys=%v want=%v", got, want)
	}
}
