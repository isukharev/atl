package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

type guardedEditHTTPFixture struct {
	srv            *httptest.Server
	mu             sync.Mutex
	written        bool
	puts           int
	requests       int
	putPath        string
	putBody        string
	before         string
	after          string
	readbackStatus int
}

func newGuardedEditHTTPFixture(t *testing.T) *guardedEditHTTPFixture {
	t.Helper()
	return newGuardedEditHTTPFixtureBodies(t, "h2. Params\n\n* timeout = 300\n\nh2. Check", "h2. Params\n\n* timeout = 600\n\nh2. Check")
}

func newGuardedEditHTTPFixtureBodies(t *testing.T, before, after string) *guardedEditHTTPFixture {
	t.Helper()
	fixture := &guardedEditHTTPFixture{before: before, after: after}
	fixture.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if fixture.written && fixture.readbackStatus != 0 {
				w.WriteHeader(fixture.readbackStatus)
				_, _ = w.Write([]byte(`{"errorMessages":["PRIVATE_BACKEND_CANARY"]}`))
				return
			}
			description := fixture.before
			updated := "2026-08-22T10:00:00.000+0000"
			if fixture.written {
				description = fixture.after
				updated = "2026-08-22T10:00:01.000+0000"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "10007", "key": "ENG-7", "fields": map[string]any{"description": description, "updated": updated}})
		case http.MethodPut:
			fixture.puts++
			fixture.putPath = r.URL.Path
			fixture.putBody = string(body)
			fixture.written = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fixture.srv.Close)
	return fixture
}

func TestJiraEditFullDescriptionClearSendsEmptyString(t *testing.T) {
	fixture := newGuardedEditHTTPFixtureBodies(t, "obsolete", "")
	previewOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "obsolete", "--new", "")
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil || preview.ProposalHash == "" {
		t.Fatalf("preview exit=%d out=%q", code, previewOut)
	}
	applyOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "obsolete", "--new", "", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	fields := jiraFields(t, fixture.putBody)
	description, present := fields["description"]
	if code != exitOK || fixture.puts != 1 || len(fields) != 1 || !present || description != "" || !strings.Contains(applyOut, `"status": "applied"`) {
		t.Fatalf("exit=%d puts=%d fields=%#v out=%q", code, fixture.puts, fields, applyOut)
	}
}

func TestJiraEditParentDefaultsToPreviewAndMatchesReadOnlyChild(t *testing.T) {
	parentServer := newGuardedEditHTTPFixture(t)
	parentOut, code := runCLI(t, jiraEnv(parentServer.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
	if code != exitOK || parentServer.puts != 0 {
		t.Fatalf("parent preview: exit=%d puts=%d out=%q", code, parentServer.puts, parentOut)
	}
	childOut, code := runCLI(t, map[string]string{"ATL_JIRA_URL": parentServer.srv.URL, "ATL_JIRA_PAT": "test-pat", "ATL_READ_ONLY": "1"}, "jira", "issue", "edit", "preview", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
	if code != exitOK || parentServer.puts != 0 {
		t.Fatalf("child preview: exit=%d puts=%d out=%q", code, parentServer.puts, childOut)
	}
	if parentOut != childOut || !strings.Contains(parentOut, `"status": "would_apply"`) || strings.Contains(parentOut, "Params") || strings.Contains(parentOut, "timeout") {
		t.Fatalf("preview mismatch or content leak:\nparent=%s\nchild=%s", parentOut, childOut)
	}
}

func TestJiraEditApplyRequiresReviewedHashAndUsesIDDescriptionOnly(t *testing.T) {
	fixture := newGuardedEditHTTPFixture(t)
	previewOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil || preview.ProposalHash == "" {
		t.Fatalf("preview exit=%d out=%q", code, previewOut)
	}
	applyOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600", "--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || fixture.puts != 1 || fixture.putPath != "/rest/api/2/issue/10007" {
		t.Fatalf("apply exit=%d puts=%d path=%q out=%q", code, fixture.puts, fixture.putPath, applyOut)
	}
	fields := jiraFields(t, fixture.putBody)
	if len(fields) != 1 || fields["description"] != "h2. Params\n\n* timeout = 600\n\nh2. Check" || !strings.Contains(applyOut, `"status": "applied"`) || !strings.Contains(applyOut, `"write_attempted": true`) {
		t.Fatalf("fields=%#v out=%q", fields, applyOut)
	}
}

func TestJiraEditDryRunAndApplyValidationBeforeConfiguration(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": configDir}
	tests := []struct {
		name string
		args []string
	}{
		{name: "explicit false", args: []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--dry-run=false"}},
		{name: "dry-run and apply", args: []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--dry-run", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64)}},
		{name: "missing hash", args: []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--apply"}},
		{name: "malformed hash", args: []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)}},
		{name: "hash without apply", args: []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--expected-proposal-hash", strings.Repeat("a", 64)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := executeCLIRaw(t, env, test.args...)
			if !errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrConfig) || codeFor(err) != exitUsage || stdout != "" || stderr != "" {
				t.Fatalf("err=%v exit=%d stdout=%q stderr=%q", err, codeFor(err), stdout, stderr)
			}
		})
	}
}

func TestJiraEditAmbiguousReadbackKeepsTerminalCheckFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fixture := newGuardedEditHTTPFixture(t)
			previewOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
			var preview struct {
				ProposalHash string `json:"proposal_hash"`
			}
			if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil {
				t.Fatalf("preview exit=%d out=%q", code, previewOut)
			}
			fixture.readbackStatus = status
			stdout, _, execErr := executeCLIRaw(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600", "--apply", "--expected-proposal-hash", preview.ProposalHash)
			var result struct {
				Status         string `json:"status"`
				WriteAttempted bool   `json:"write_attempted"`
			}
			if json.Unmarshal([]byte(stdout), &result) != nil || result.Status != "outcome_unknown" || !result.WriteAttempted || codeFor(execErr) != exitCheckFailed {
				t.Fatalf("result=%+v exit=%d err=%v stdout=%q", result, codeFor(execErr), execErr, stdout)
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if !errors.Is(execErr, domain.ErrCheckFailed) || !errors.As(execErr, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
				t.Fatalf("terminal ambiguity lost: %v", execErr)
			}
			if status == http.StatusUnauthorized && !errors.Is(execErr, domain.ErrAuth) || status == http.StatusForbidden && !errors.Is(execErr, domain.ErrForbidden) {
				t.Fatalf("safe nested status identity lost: status=%d err=%v", status, execErr)
			}
			var rendered strings.Builder
			writeErrorWithContext(&rendered, "json", execErr, codeFor(execErr), diagnostic.OperationWrite)
			var envelope struct {
				Code     int                 `json:"code"`
				Kind     string              `json:"kind"`
				Recovery diagnostic.Recovery `json:"recovery"`
			}
			if json.Unmarshal([]byte(rendered.String()), &envelope) != nil || envelope.Code != exitCheckFailed || envelope.Kind != "check_failed" || envelope.Recovery.Action != diagnostic.RecoveryReconcileWriteOutcome || strings.Contains(rendered.String(), "PRIVATE_BACKEND_CANARY") {
				t.Fatalf("terminal diagnostic=%s", rendered.String())
			}
		})
	}
}

func TestJiraEditDryRunTrueAliasesPreview(t *testing.T) {
	fixture := newGuardedEditHTTPFixture(t)
	out, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600", "--dry-run=true")
	if code != exitOK || fixture.puts != 0 || !strings.Contains(out, `"mode": "dry-run"`) {
		t.Fatalf("exit=%d puts=%d out=%q", code, fixture.puts, out)
	}
}

func TestJiraEditEmissionFailureAfterPUTRemainsClosed(t *testing.T) {
	fixture := newGuardedEditHTTPFixture(t)
	previewOut, code := runCLI(t, jiraEnv(fixture.srv), "jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil {
		t.Fatalf("preview exit=%d out=%q", code, previewOut)
	}
	root := newRoot()
	root.SetOut(errWriter{cause: errors.New("stdout unavailable")})
	setRootExecutionArgs(root, []string{"jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600", "--apply", "--expected-proposal-hash", preview.ProposalHash})
	err := root.ExecuteContext(context.Background())
	if fixture.puts != 1 || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("puts=%d err=%v", fixture.puts, err)
	}
}
