package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

const jiraCommentBackendCanary = "JIRA-COMMENT-BACKEND-CANARY"

type jiraCommentCLIServer struct {
	t            *testing.T
	srv          *httptest.Server
	mu           sync.Mutex
	items        []map[string]any
	post         int
	list         int
	myself       int
	issue        int
	status       int
	myselfStatus int
	issueStatus  int
	myselfBody   string
}

func newJiraCommentCLIServer(t *testing.T) *jiraCommentCLIServer {
	t.Helper()
	s := &jiraCommentCLIServer{t: t, status: http.StatusCreated}
	s.items = []map[string]any{{
		"id": "10", "author": map[string]any{"name": "alice", "key": "user-1", "displayName": "Alice"},
		"created": "2026-07-01T10:00:00.000+0000", "updated": "2026-07-01T10:00:00.000+0000", "body": "reviewed body",
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
		if s.myselfStatus != 0 {
			w.WriteHeader(s.myselfStatus)
			_, _ = w.Write([]byte(`{"message":"` + jiraCommentBackendCanary + `"}`))
			return
		}
		if s.myselfBody != "" {
			_, _ = w.Write([]byte(s.myselfBody))
			return
		}
		_, _ = w.Write([]byte(`{"name":"alice","key":"user-1","displayName":"Alice","emailAddress":"private@example.test"}`))
	case r.Method == http.MethodGet && (r.URL.Path == "/rest/api/2/issue/PROJ-1" || r.URL.Path == "/rest/api/2/issue/101"):
		s.issue++
		if s.issueStatus != 0 {
			w.WriteHeader(s.issueStatus)
			_, _ = w.Write([]byte(`{"message":"` + jiraCommentBackendCanary + `"}`))
			return
		}
		updated := "2026-07-02T09:00:00.000+0000"
		if s.post > 0 {
			updated = "2026-07-02T10:00:01.000+0000"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "101", "key": "PROJ-1", "fields": map[string]any{"project": map[string]any{"key": "PROJ"}, "updated": updated}})
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/101/comment":
		s.list++
		_ = json.NewEncoder(w).Encode(map[string]any{"startAt": 0, "total": len(s.items), "comments": s.items})
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/101/comment":
		s.post++
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			s.t.Errorf("decode POST: %v", err)
		}
		if s.status >= 200 && s.status < 300 {
			created := map[string]any{
				"id": "20", "author": map[string]any{"name": "alice", "key": "user-1", "displayName": "Alice"},
				"created": "2026-07-02T10:00:00.000+0000", "updated": "2026-07-02T10:00:00.000+0000", "body": payload["body"],
			}
			s.items = append(s.items, created)
			w.WriteHeader(s.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "20"})
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
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil || preview.Status != "would_apply" || preview.BodyBytes != len("reviewed body") ||
		preview.Usage.Requests != 3 || preview.Usage.ResponseBytes <= 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}

	dryOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "add", "PROJ-1", "--from-file", path)
	var dry app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(dryOut), &dry); code != exitOK || err != nil || dry.Status != "would_apply" || dry.ProposalHash != preview.ProposalHash || dry.Usage.Requests != 3 {
		t.Fatalf("dry=%+v exit=%d err=%v out=%s", dry, code, err, dryOut)
	}
	if server.post != 0 {
		t.Fatalf("dry-run posted %d comments", server.post)
	}

	applyOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "add", "PROJ-1", "--from-file", path,
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	var applied app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(applyOut), &applied); code != exitOK || err != nil || applied.Status != "applied" || applied.CommentID != "20" || applied.Usage.Requests != 9 {
		t.Fatalf("applied=%+v exit=%d err=%v out=%s", applied, code, err, applyOut)
	}
	if server.myself != 4 || server.issue != 5 || server.list != 5 || server.post != 1 {
		t.Fatalf("requests myself=%d issue=%d list=%d post=%d, want 4/5/5/1", server.myself, server.issue, server.list, server.post)
	}
}

func TestJiraCommentMissingHashPrecedesInputAndService(t *testing.T) {
	out, _, code := runCLIFull(t, nil, "jira", "issue", "comment", "add", "PROJ-1",
		"--from-file", "/definitely/missing/comment.wiki", "--apply")
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
}

func TestJiraCommentInvocationFailsBeforeConfiguration(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"jira", "issue", "comment", "preview", "not a key", "--from-file", "missing"}, "issue key must be canonical"},
		{[]string{"jira", "issue", "comment", "preview", "PROJ-1", "--from-file", "one", "--from-md", "two"}, "mutually exclusive"},
		{[]string{"jira", "issue", "comment", "preview", "PROJ-1", "--from-md", ""}, "requires a file path"},
		{[]string{"jira", "issue", "comment", "add", "PROJ-1", "--from-file", "missing", "--expected-proposal-hash", strings.Repeat("a", 64)}, "requires --apply"},
		{[]string{"jira", "issue", "comment", "add", "PROJ-1", "--from-file", "missing", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)}, "lowercase 64-character SHA-256"},
	}
	for _, test := range tests {
		out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"}, test.args...)
		if codeFor(err) != exitUsage || out != "" || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args=%v error=%v stdout=%q", test.args, err, out)
		}
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

func TestJiraCommentBlockedQualificationAlwaysExitsCheckFailed(t *testing.T) {
	for _, test := range []struct {
		name         string
		myselfStatus int
		issueStatus  int
		myselfBody   string
		cause        error
	}{
		{name: "initial issue not found", issueStatus: http.StatusNotFound, cause: domain.ErrNotFound},
		{name: "predispatch issue forbidden", issueStatus: http.StatusForbidden, cause: domain.ErrForbidden},
		{name: "malformed initial actor", myselfBody: `{"name":42,"key":"user-1"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newJiraCommentCLIServer(t)
			server.myselfStatus, server.issueStatus = test.myselfStatus, test.issueStatus
			server.myselfBody = test.myselfBody
			out, stderr, execErr := executeCLIRaw(t, jiraEnv(server.srv), "jira", "issue", "comment", "preview", "PROJ-1",
				"--from-file", writeCommentBody(t, "reviewed body"))
			var result app.JiraCommentAddResult
			if err := json.Unmarshal([]byte(out), &result); err != nil || codeFor(execErr) != exitCheckFailed || stderr != "" || result.Status != "blocked" || result.Complete ||
				result.BackendSHA256 == "" || result.BodySHA256 == "" || result.BodyBytes != len("reviewed body") ||
				strings.Contains(out, jiraCommentBackendCanary) {
				t.Fatalf("result=%+v exit=%d decode=%v stdout=%q stderr=%q", result, codeFor(execErr), err, out, stderr)
			}
			if test.cause != nil && !errors.Is(execErr, test.cause) {
				t.Fatalf("error=%v, want preserved nested identity %v", execErr, test.cause)
			}

			var rendered bytes.Buffer
			writeError(&rendered, "json", execErr, codeFor(execErr))
			var envelope struct {
				Error       string              `json:"error"`
				Code        int                 `json:"code"`
				Kind        string              `json:"kind"`
				Remediation string              `json:"remediation"`
				Recovery    diagnostic.Recovery `json:"recovery"`
			}
			if err := json.Unmarshal(rendered.Bytes(), &envelope); err != nil {
				t.Fatalf("decode rendered stderr: %v raw=%q", err, rendered.String())
			}
			if envelope.Code != exitCheckFailed || envelope.Kind != "check_failed" || envelope.Remediation != "review_failed_check" ||
				envelope.Recovery.SchemaVersion != diagnostic.RecoverySchemaVersion || envelope.Recovery.Action != diagnostic.RecoveryInspectFailure ||
				envelope.Recovery.RetrySafe || !diagnostic.ValidateRecovery(envelope.Recovery) || strings.Contains(rendered.String(), jiraCommentBackendCanary) {
				t.Fatalf("rendered stderr=%q envelope=%+v", rendered.String(), envelope)
			}
		})
	}
}

func TestJiraCommentInvalidPreConfigPrecedesEligibleSelfUpdate(t *testing.T) {
	var updateRequests, jiraRequests atomic.Int64
	updateServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		updateRequests.Add(1)
	}))
	t.Cleanup(updateServer.Close)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		jiraRequests.Add(1)
	}))
	t.Cleanup(jiraServer.Close)
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
	t.Setenv("ATL_UPDATE_URL", updateServer.URL)
	t.Setenv("ATL_JIRA_URL", jiraServer.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")
	for _, path := range []string{"jira issue comment preview", "jira issue comment add"} {
		root := newRoot()
		command, _, err := root.Find(strings.Split(path, " "))
		if err != nil || command == nil || skipSelfUpdate(command) {
			t.Fatalf("%s self-update eligibility: command=%v err=%v skipped=%t", path, command, err, command != nil && skipSelfUpdate(command))
		}
		catalog, catalogErr := buildCommandEffectCatalog(commandEffectSelection{Command: path})
		if catalogErr != nil || len(catalog.Profiles) != 1 || catalog.Profiles[0].SelfUpdate != "possible" {
			t.Fatalf("%s effects=%+v err=%v, want self_update possible", path, catalog.Profiles, catalogErr)
		}
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "preview key", args: []string{"jira", "issue", "comment", "preview", "not a key", "--from-file", "missing"}, want: "usage error: issue key must be canonical and at most 64 bytes (for example PROJ-1)"},
		{name: "preview body source", args: []string{"jira", "issue", "comment", "preview", "PROJ-1", "--from-file", "one", "--from-md", "two"}, want: "usage error: --from-file and --from-md are mutually exclusive"},
		{name: "add proposal hash", args: []string{"jira", "issue", "comment", "add", "PROJ-1", "--from-file", "missing", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)}, want: "usage error: --expected-proposal-hash must be a lowercase 64-character SHA-256"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ATL_CONFIG_DIR", configDir)
			before := mutationGuardTreeSnapshot(t, configDir)
			updateRequests.Store(0)
			jiraRequests.Store(0)

			var stdout, stderr bytes.Buffer
			root := newRoot()
			setRootExecutionArgs(root, test.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			err := root.ExecuteContext(context.Background())
			if err == nil || err.Error() != test.want || !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage {
				t.Fatalf("err=%v code=%d, want exact usage %q", err, codeFor(err), test.want)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q, want both empty", stdout.String(), stderr.String())
			}
			if got := updateRequests.Load(); got != 0 {
				t.Fatalf("self-update requests=%d, want zero", got)
			}
			if got := jiraRequests.Load(); got != 0 {
				t.Fatalf("Jira requests=%d, want zero", got)
			}
			if after := mutationGuardTreeSnapshot(t, configDir); !reflect.DeepEqual(after, before) {
				t.Fatalf("config tree changed: before=%v after=%v", before, after)
			}
			if _, statErr := os.Stat(filepath.Join(configDir, ".update-check")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("self-update stamp stat error=%v, want absent", statErr)
			}
		})
	}
}

func TestJiraCommentEmitsOutcomeUnknownBeforeAmbiguousError(t *testing.T) {
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
	if err := json.Unmarshal([]byte(out), &result); code != exitCheckFailed || err != nil || result.Status != "outcome_unknown" || !result.Reconciled || !result.Complete {
		t.Fatalf("result=%+v exit=%d err=%v stdout=%q", result, code, err, out)
	}
	if server.post != 1 || server.list != 4 {
		t.Fatalf("requests list=%d post=%d, want 4/1", server.list, server.post)
	}
}

func TestJiraCommentOutputFailureAfterAttemptIsNoReplay(t *testing.T) {
	server := newJiraCommentCLIServer(t)
	path := writeCommentBody(t, "new body")
	previewOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "comment", "preview", "PROJ-1", "--from-file", path)
	if code != exitOK {
		t.Fatalf("preview exit=%d stdout=%q", code, previewOut)
	}
	var preview app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, jiraEnv(server.srv), cause,
		"jira", "issue", "comment", "add", "PROJ-1", "--from-file", path,
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") || server.post != 1 {
		t.Fatalf("error=%v post_calls=%d", err, server.post)
	}
}
