package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

type jiraGuardedCreateCLIServer struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	posts  int
	reads  int
	ack    string
	body   string
	status int
}

func newJiraGuardedCreateCLIServer(t *testing.T) *jiraGuardedCreateCLIServer {
	t.Helper()
	fixture := &jiraGuardedCreateCLIServer{t: t, ack: `{"id":"11"}`, status: http.StatusCreated}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *jiraGuardedCreateCLIServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/project":
		_, _ = io.WriteString(w, `[{"id":"7","key":"OPS","name":"Operations","archived":false}]`)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/createmeta/OPS/issuetypes":
		_, _ = io.WriteString(w, `{"startAt":0,"total":1,"isLast":true,"values":[{"id":"3","name":"Task","subtask":false}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/createmeta/OPS/issuetypes/3":
		_, _ = io.WriteString(w, jiraGuardedCreateCLIMetadata())
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue":
		s.posts++
		body, _ := io.ReadAll(r.Body)
		s.body = string(body)
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.ack)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/11":
		var submitted struct {
			Fields map[string]any `json:"fields"`
		}
		decoder := json.NewDecoder(strings.NewReader(s.body))
		decoder.UseNumber()
		_ = decoder.Decode(&submitted)
		fields := map[string]any{}
		for _, field := range strings.Split(r.URL.Query().Get("fields"), ",") {
			fields[field] = nil
		}
		fields["project"] = map[string]any{"id": "7", "key": "OPS"}
		fields["issuetype"] = map[string]any{"id": "3", "name": "Task"}
		fields["summary"] = submitted.Fields["summary"]
		if description, ok := submitted.Fields["description"]; ok {
			fields["description"] = description
		}
		fields["created"] = "2026-08-22T10:00:00.000+0000"
		fields["updated"] = "2026-08-22T10:00:01.000+0000"
		if value, ok := submitted.Fields["customfield_1"]; ok {
			fields["customfield_1"] = value
		}
		encoded, _ := json.Marshal(map[string]any{"id": "11", "key": "OPS-1", "fields": fields})
		_, _ = w.Write(encoded)
	default:
		http.NotFound(w, r)
	}
}

func jiraGuardedCreateCLIMetadata() string {
	field := func(id, typ string, required bool) string {
		return `{"fieldId":"` + id + `","name":"` + id + `","required":` + fmt.Sprint(required) + `,"schema":{"type":"` + typ + `","system":"` + id + `"},"hasDefaultValue":false,"allowedValues":[],"autoCompleteUrl":null}`
	}
	return `{"startAt":0,"total":5,"isLast":true,"values":[` +
		field("project", "project", true) + `,` + field("issuetype", "issuetype", true) + `,` +
		field("summary", "string", true) + `,` + field("description", "string", false) + `,` +
		`{"fieldId":"customfield_1","name":"Number","required":false,"schema":{"type":"number","custom":"number","customId":1},"hasDefaultValue":false,"allowedValues":[],"autoCompleteUrl":null}]}`
}

func guardedCreateCLIArgs() []string {
	return []string{"jira", "issue", "create", "--project", "OPS", "--type", "Task", "--summary", "Reviewed", "--from-file", "-", "--field-json", "customfield_1=9007199254740993"}
}

func runJiraGuardedCreatePreview(t *testing.T, fixture *jiraGuardedCreateCLIServer, child bool) (string, app.JiraGuardedCreateResult) {
	t.Helper()
	args := guardedCreateCLIArgs()
	if child {
		args = append(args[:3:3], append([]string{"preview"}, args[3:]...)...)
	}
	var out string
	var code int
	withStdin(t, "wiki", func() { out, code = runCLI(t, jiraEnv(fixture.server), args...) })
	if code != exitOK {
		t.Fatalf("preview exit=%d output=%s", code, out)
	}
	var result app.JiraGuardedCreateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode preview: %v\n%s", err, out)
	}
	return out, result
}

func TestJiraGuardedCreateParentAndChildPreviewAreEquivalentAndReadOnly(t *testing.T) {
	fixture := newJiraGuardedCreateCLIServer(t)
	parent, result := runJiraGuardedCreatePreview(t, fixture, false)
	child, childResult := runJiraGuardedCreatePreview(t, fixture, true)
	if parent != child || result.Status != "would_apply" || childResult.ProposalHash != result.ProposalHash || fixture.posts != 0 {
		t.Fatalf("parent=%s child=%s posts=%d", parent, child, fixture.posts)
	}
	if result.WriteAttempted || result.ReadbackReconciled || result.ProposalHash == "" {
		t.Fatalf("preview result=%+v", result)
	}
	assertGuardedCreateJSONKeys(t, parent, []string{
		"backend_sha256", "bounds", "description", "fields", "issue_type", "metadata_count", "metadata_sha256", "mode", "operation", "project", "proposal_hash", "readback_reconciled", "registration_requested", "request_bytes", "request_sha256", "requested_project", "schema_version", "status", "summary", "type_selector", "usage", "write_attempted",
	})
}

func TestJiraGuardedCreateApplyAndIDOutput(t *testing.T) {
	fixture := newJiraGuardedCreateCLIServer(t)
	_, preview := runJiraGuardedCreatePreview(t, fixture, false)
	args := append(guardedCreateCLIArgs(), "--apply", "--expected-proposal-hash", preview.ProposalHash)
	var out string
	var code int
	withStdin(t, "wiki", func() { out, code = runCLI(t, jiraEnv(fixture.server), args...) })
	if code != exitOK {
		t.Fatalf("apply exit=%d output=%s", code, out)
	}
	var result app.JiraGuardedCreateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Status != "applied" || result.Issue == nil || result.Issue.Key != "OPS-1" || !result.WriteAttempted || !result.ReadbackReconciled {
		t.Fatalf("result=%+v err=%v output=%s", result, err, out)
	}
	if fixture.posts != 1 || !strings.Contains(fixture.body, `"customfield_1":9007199254740993`) {
		t.Fatalf("posts=%d body=%s", fixture.posts, fixture.body)
	}
	normalized := strings.ReplaceAll(out, result.BackendSHA256, "<server-origin-sha256>")
	normalized = strings.ReplaceAll(normalized, result.ProposalHash, "<server-origin-bound-proposal-hash>")
	assertGolden(t, "jira_issue_create.json", []byte(normalized))
	assertGuardedCreateJSONKeys(t, out, []string{
		"acknowledgement", "backend_sha256", "bounds", "description", "fields", "issue", "issue_type", "metadata_count", "metadata_sha256", "mode", "operation", "project", "proposal_hash", "readback_reconciled", "registration_requested", "request_bytes", "request_sha256", "requested_project", "schema_version", "status", "summary", "type_selector", "usage", "write_attempted",
	})

	_, next := runJiraGuardedCreatePreview(t, fixture, false)
	idArgs := append([]string{"-o", "id"}, guardedCreateCLIArgs()...)
	idArgs = append(idArgs, "--apply", "--expected-proposal-hash", next.ProposalHash)
	withStdin(t, "wiki", func() { out, code = runCLI(t, jiraEnv(fixture.server), idArgs...) })
	if code != exitOK || out != "OPS-1\n" || fixture.posts != 2 {
		t.Fatalf("id apply exit=%d output=%q posts=%d", code, out, fixture.posts)
	}
}

func TestJiraGuardedCreateIDOutputSuppressesUnprovedIdentifier(t *testing.T) {
	fixture := newJiraGuardedCreateCLIServer(t)
	_, preview := runJiraGuardedCreatePreview(t, fixture, false)
	fixture.ack = `{}`
	args := append([]string{"-o", "id"}, guardedCreateCLIArgs()...)
	args = append(args, "--apply", "--expected-proposal-hash", preview.ProposalHash)
	var out string
	var code int
	withStdin(t, "wiki", func() { out, code = runCLI(t, jiraEnv(fixture.server), args...) })
	if code != exitCheckFailed || out != "" || fixture.posts != 1 {
		t.Fatalf("exit=%d output=%q posts=%d", code, out, fixture.posts)
	}
}

func TestJiraGuardedCreatePureValidationAndPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "create", "--project", "OPS", "--type", "Task", "--summary", "x", "--field-json", "customfield_1=not-json"},
		{"jira", "issue", "create", "--project", "OPS", "--type", "Task", "--summary", "x", "--apply"},
		{"-o", "id", "jira", "issue", "create", "--project", "OPS", "--type", "Task", "--summary", "x"},
	} {
		out, _, code := runCLIFull(t, nil, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d output=%q", args, code, out)
		}
	}

	fixture := newJiraGuardedCreateCLIServer(t)
	args := append([]string{"--read-only"}, guardedCreateCLIArgs()...)
	var out string
	var code int
	withStdin(t, "wiki", func() { out, code = runCLI(t, jiraEnv(fixture.server), args...) })
	if code != exitOK || !strings.Contains(out, `"status": "would_apply"`) || fixture.posts != 0 {
		t.Fatalf("read-only preview exit=%d output=%s posts=%d", code, out, fixture.posts)
	}
	before := fixture.reads
	args = append(args, "--apply", "--expected-proposal-hash", strings.Repeat("a", 64))
	withStdin(t, "wiki", func() { _, code = runCLI(t, jiraEnv(fixture.server), args...) })
	if code != exitCheckFailed || fixture.reads != before || fixture.posts != 0 {
		t.Fatalf("read-only apply exit=%d reads=%d want=%d posts=%d", code, fixture.reads, before, fixture.posts)
	}
}

func TestJiraGuardedCreatePreviewValidationDoesNotConsumeStdin(t *testing.T) {
	tests := [][]string{
		{"jira", "issue", "create", "preview", "--type", "Task", "--summary", "S", "--from-file", "-"},
		{"jira", "issue", "create", "preview", "--project", "OPS", "--summary", "S", "--from-file", "-"},
		{"jira", "issue", "create", "preview", "--project", "OPS", "--type", "Task", "--from-file", "-"},
	}
	for _, args := range tests {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString("unconsumed")
		_ = w.Close()
		original := os.Stdin
		os.Stdin = r
		root := newRoot()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		execErr := root.Execute()
		remaining, readErr := io.ReadAll(r)
		_ = r.Close()
		os.Stdin = original
		if readErr != nil || codeFor(execErr) != exitUsage || string(remaining) != "unconsumed" {
			t.Fatalf("args=%v err=%v readErr=%v remaining=%q", args, execErr, readErr, remaining)
		}
	}
}

func TestJiraGuardedCreateStdoutFailurePreservesClosedOutcome(t *testing.T) {
	fixture := newJiraGuardedCreateCLIServer(t)
	_, preview := runJiraGuardedCreatePreview(t, fixture, false)
	fixture.status, fixture.ack = http.StatusForbidden, `{"error":"private"}`
	args := append(guardedCreateCLIArgs(), "--apply", "--expected-proposal-hash", preview.ProposalHash)
	var rejectErr error
	withStdin(t, "wiki", func() {
		rejectErr = runCLIWithFailingStdoutEnv(t, jiraEnv(fixture.server), errors.New("stdout unavailable"), args...)
	})
	if !errors.Is(rejectErr, domain.ErrForbidden) || errors.Is(rejectErr, domain.ErrCheckFailed) || codeFor(rejectErr) != exitForbidden {
		t.Fatalf("definitive stdout failure=%v code=%d", rejectErr, codeFor(rejectErr))
	}

	fixture.status, fixture.ack = http.StatusCreated, `{}`
	_, preview = runJiraGuardedCreatePreview(t, fixture, false)
	args = append(guardedCreateCLIArgs(), "--apply", "--expected-proposal-hash", preview.ProposalHash)
	var unknownErr error
	withStdin(t, "wiki", func() {
		unknownErr = runCLIWithFailingStdoutEnv(t, jiraEnv(fixture.server), errors.New("stdout unavailable"), args...)
	})
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.Is(unknownErr, domain.ErrCheckFailed) || !errors.As(unknownErr, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || codeFor(unknownErr) != exitCheckFailed {
		t.Fatalf("unknown stdout failure=%v code=%d", unknownErr, codeFor(unknownErr))
	}
}

func TestJiraGuardedCreateAccessAndStartupRegistration(t *testing.T) {
	root := newRoot()
	parent, _, err := root.Find([]string{"jira", "issue", "create"})
	if err != nil || parent.Annotations[accessAnnotation] != "mutating" || !skipSelfUpdate(parent) {
		t.Fatalf("parent access=%q skip=%t err=%v", parent.Annotations[accessAnnotation], skipSelfUpdate(parent), err)
	}
	preview, _, err := root.Find([]string{"jira", "issue", "create", "preview"})
	if err != nil || preview.Annotations[accessAnnotation] != "read-only" || !skipSelfUpdate(preview) {
		t.Fatalf("preview access=%q skip=%t err=%v", preview.Annotations[accessAnnotation], skipSelfUpdate(preview), err)
	}
}

func TestJiraGuardedCreateRootExecutionNeverChecksForStartupUpdates(t *testing.T) {
	var updateRequests atomic.Int64
	updateServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		updateRequests.Add(1)
	}))
	t.Cleanup(updateServer.Close)
	fixture := newJiraGuardedCreateCLIServer(t)
	configDir := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "description.wiki")
	if err := os.WriteFile(bodyPath, []byte("wiki"), 0o600); err != nil {
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
	t.Setenv("ATL_JIRA_URL", fixture.server.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")
	args := guardedCreateCLIArgs()
	for index := range args {
		if args[index] == "-" {
			args[index] = bodyPath
		}
	}
	execute := func(args []string) (string, error) {
		var stdout, stderr bytes.Buffer
		root := newRoot()
		setRootExecutionArgs(root, args)
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		err := root.ExecuteContext(context.Background())
		if stderr.Len() != 0 {
			t.Fatalf("stderr=%q", stderr.String())
		}
		return stdout.String(), err
	}
	parent, err := execute(args)
	if err != nil {
		t.Fatal(err)
	}
	childArgs := append(args[:3:3], append([]string{"preview"}, args[3:]...)...)
	child, err := execute(childArgs)
	if err != nil || child != parent {
		t.Fatalf("child err=%v output=%s parent=%s", err, child, parent)
	}
	var preview app.JiraGuardedCreateResult
	if err := json.Unmarshal([]byte(parent), &preview); err != nil {
		t.Fatal(err)
	}
	applyArgs := append(append([]string(nil), args...), "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if _, err := execute(applyArgs); err != nil {
		t.Fatal(err)
	}
	if got := updateRequests.Load(); got != 0 {
		t.Fatalf("startup update requests=%d, want zero", got)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".update-check")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("update stamp stat error=%v, want absent", err)
	}
	if fixture.posts != 1 {
		t.Fatalf("Jira create posts=%d, want one apply POST", fixture.posts)
	}
}

func assertGuardedCreateJSONKeys(t *testing.T, output string, want []string) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode guarded create JSON: %v\n%s", err, output)
	}
	got := make([]string, 0, len(envelope))
	for key := range envelope {
		got = append(got, key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("guarded create JSON keys=%v want=%v\n%s", got, want, output)
	}
}
