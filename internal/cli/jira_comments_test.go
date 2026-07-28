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
)

type jiraCommentCLIServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	items  []map[string]any
	post   int
	list   int
	myself int
	status int
}

func newJiraCommentCLIServer(t *testing.T) *jiraCommentCLIServer {
	t.Helper()
	s := &jiraCommentCLIServer{t: t, status: http.StatusCreated}
	s.items = []map[string]any{{
		"id": "10", "author": map[string]any{"name": "alice", "key": "user-1", "displayName": "Alice"},
		"created": "2026-07-01T10:00:00.000+0000", "body": "reviewed body",
	}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jiraCommentCLIServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/myself":
		s.myself++
		_, _ = w.Write([]byte(`{"name":"alice","key":"user-1","displayName":"Alice","emailAddress":"private@example.test"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1/comment":
		s.list++
		_ = json.NewEncoder(w).Encode(map[string]any{"startAt": 0, "total": len(s.items), "comments": s.items})
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/PROJ-1/comment":
		s.post++
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Errorf("decode POST: %v", err)
		}
		if s.status >= 200 && s.status < 300 {
			created := map[string]any{
				"id": "20", "author": map[string]any{"name": "alice", "key": "user-1", "displayName": "Alice"},
				"created": "2026-07-02T10:00:00.000+0000", "body": payload["body"],
			}
			s.items = append(s.items, created)
			w.WriteHeader(s.status)
			_ = json.NewEncoder(w).Encode(created)
			return
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(`{"message":"synthetic failure"}`))
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeCommentBody(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comment.wiki")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJiraCommentPreviewDryRunAndApplyUseExactRequestCounts(t *testing.T) {
	server := newJiraCommentCLIServer(t)
	path := writeCommentBody(t, "reviewed body")

	previewOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "preview", "PROJ-1", "--from-file", path)
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, previewOut)
	}
	var preview app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil || preview.Status != "would_apply" || preview.Body != "reviewed body" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}

	dryOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "add", "PROJ-1", "--from-file", path)
	var dry app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(dryOut), &dry); code != exitOK || err != nil || dry.Status != "would_apply" || dry.ProposalHash != preview.ProposalHash {
		t.Fatalf("dry=%+v exit=%d err=%v out=%s", dry, code, err, dryOut)
	}
	if server.post != 0 {
		t.Fatalf("dry-run posted %d comments", server.post)
	}

	applyOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "add", "PROJ-1", "--from-file", path,
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	var applied app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(applyOut), &applied); code != exitOK || err != nil || applied.Status != "applied" || applied.Created == nil || applied.Created.ID != "20" {
		t.Fatalf("applied=%+v exit=%d err=%v out=%s", applied, code, err, applyOut)
	}
	if server.myself != 3 || server.list != 5 || server.post != 1 {
		t.Fatalf("requests myself=%d list=%d post=%d, want 3/5/1", server.myself, server.list, server.post)
	}
}

func TestJiraCommentMissingHashPrecedesInputAndService(t *testing.T) {
	out, _, code := runCLIFull(t, nil, "jira", "issue", "comment", "add", "PROJ-1",
		"--from-file", "/definitely/missing/comment.wiki", "--apply")
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
}

func TestJiraCommentBoundedInputFailsBeforeService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.wiki")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", app.JiraCommentBodyMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIFull(t, nil, "jira", "issue", "comment", "preview", "PROJ-1", "--from-file", path)
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
}

func TestJiraCommentTextOutputDoesNotExposeBody(t *testing.T) {
	server := newJiraCommentCLIServer(t)
	const privateBody = "reviewed body with private detail"
	out, code := runCLI(t, jiraEnv(server.srv), "-o", "text", "jira", "issue", "comment", "preview", "PROJ-1",
		"--from-file", writeCommentBody(t, privateBody))
	if code != exitOK || strings.Contains(out, privateBody) {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	for _, field := range []string{"status: would_apply", "key: PROJ-1", "proposal_hash:", "body_sha256:", "body_bytes:"} {
		if !strings.Contains(out, field) {
			t.Fatalf("output=%q missing %q", out, field)
		}
	}
}

func TestJiraCommentEmitsUnverifiableResultBeforeAmbiguousError(t *testing.T) {
	server := newJiraCommentCLIServer(t)
	path := writeCommentBody(t, "new body")
	previewOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "preview", "PROJ-1", "--from-file", path)
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, previewOut)
	}
	var preview app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	server.status = http.StatusInternalServerError
	out, _, code := runCLIFull(t, jiraEnv(server.srv), "jira", "issue", "comment", "add", "PROJ-1", "--from-file", path,
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	var result app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(out), &result); code != exitCheckFailed || err != nil || result.Status != "unverifiable" {
		t.Fatalf("result=%+v exit=%d err=%v stdout=%q", result, code, err, out)
	}
	if server.post != 1 || server.list != 4 {
		t.Fatalf("requests list=%d post=%d, want 4/1", server.list, server.post)
	}
}
