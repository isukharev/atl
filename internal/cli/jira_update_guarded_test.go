package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	capabilitydef "github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

type jiraGuardedUpdateCLIServer struct {
	mu         sync.Mutex
	server     *httptest.Server
	written    bool
	puts       int
	body       []byte
	putStatus  int
	advance    bool
	issueReads int
}

func newJiraGuardedUpdateCLIServer(t *testing.T) *jiraGuardedUpdateCLIServer {
	t.Helper()
	fixture := &jiraGuardedUpdateCLIServer{putStatus: http.StatusNoContent, advance: true}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *jiraGuardedUpdateCLIServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
		_, _ = io.WriteString(w, `[{"id":"customfield_1","name":"Estimate","custom":true}]`)
	case r.Method == http.MethodGet && (r.URL.Path == "/rest/api/2/issue/PROJ-1" || r.URL.Path == "/rest/api/2/issue/10001"):
		s.issueReads++
		fields := map[string]any{
			"project": map[string]any{"key": "PROJ"}, "updated": "2026-09-08T10:00:00.000+0000",
			"summary": "Old summary", "description": "old body", "customfield_1": json.Number("1"),
		}
		if s.written {
			var payload struct {
				Fields map[string]any `json:"fields"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(s.body)))
			decoder.UseNumber()
			_ = decoder.Decode(&payload)
			for field, value := range payload.Fields {
				fields[field] = value
			}
			if s.advance {
				fields["updated"] = "2026-09-08T10:01:00.000+0000"
			}
		}
		encoded, _ := json.Marshal(map[string]any{"id": "10001", "key": "PROJ-1", "fields": fields})
		_, _ = w.Write(encoded)
	case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/10001":
		s.body, _ = io.ReadAll(r.Body)
		s.puts++
		s.written = true
		w.WriteHeader(s.putStatus)
	default:
		http.NotFound(w, r)
	}
}

func TestJiraGuardedUpdateHTTPOutcomeSemantics(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		advance    bool
		wantStatus string
		wantCode   int
		wantReads  int
	}{
		{name: "nonadvancing success is terminal unknown", status: http.StatusNoContent, wantStatus: "outcome_unknown", wantCode: exitCheckFailed, wantReads: 4},
		{name: "ambiguous 500 recovered", status: http.StatusInternalServerError, advance: true, wantStatus: "recovered", wantCode: exitOK, wantReads: 4},
		{name: "definitive 400 skips readback", status: http.StatusBadRequest, advance: true, wantStatus: "not_applied", wantCode: exitUsage, wantReads: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newJiraGuardedUpdateCLIServer(t)
			fixture.putStatus, fixture.advance = test.status, test.advance
			path := filepath.Join(t.TempDir(), "description.wiki")
			if err := os.WriteFile(path, []byte("h2. New body"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := jiraEnv(fixture.server)
			_, preview := runJiraGuardedUpdatePreview(t, fixture, path, false, env)
			args := append(guardedUpdateCLIArgs(path), "--apply", "--expected-proposal-hash", preview.ProposalHash)
			out, code := runCLI(t, env, args...)
			var result app.JiraGuardedUpdateResult
			if err := json.Unmarshal([]byte(out), &result); err != nil || code != test.wantCode || result.Status != test.wantStatus || fixture.issueReads != test.wantReads {
				t.Fatalf("exit=%d result=%+v decode=%v reads=%d output=%s", code, result, err, fixture.issueReads, out)
			}
		})
	}
}

func guardedUpdateCLIArgs(path string) []string {
	return []string{
		"jira", "issue", "update", "PROJ-1", "--summary", "New summary", "--from-file", path,
		"--field-json", "customfield_1=9007199254740993",
	}
}

func runJiraGuardedUpdatePreview(t *testing.T, _ *jiraGuardedUpdateCLIServer, path string, child bool, env map[string]string) (string, app.JiraGuardedUpdateResult) {
	t.Helper()
	args := guardedUpdateCLIArgs(path)
	if child {
		args = append(args[:3:3], append([]string{"preview"}, args[3:]...)...)
	}
	out, code := runCLI(t, env, args...)
	if code != exitOK {
		t.Fatalf("preview exit=%d output=%s", code, out)
	}
	var result app.JiraGuardedUpdateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode preview: %v\n%s", err, out)
	}
	return out, result
}

func TestJiraGuardedUpdateParentAndChildPreviewAndApply(t *testing.T) {
	fixture := newJiraGuardedUpdateCLIServer(t)
	description := filepath.Join(t.TempDir(), "description.wiki")
	if err := os.WriteFile(description, []byte("h2. New body"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := jiraEnv(fixture.server)
	parent, preview := runJiraGuardedUpdatePreview(t, fixture, description, false, env)
	child, childPreview := runJiraGuardedUpdatePreview(t, fixture, description, true, env)
	if parent != child || preview.Status != "would_apply" || preview.ProposalHash == "" || childPreview.ProposalHash != preview.ProposalHash || fixture.puts != 0 {
		t.Fatalf("parent=%s child=%s puts=%d", parent, child, fixture.puts)
	}
	for _, value := range []string{"Old summary", "old body", "New summary", "h2. New body", "9007199254740993"} {
		if strings.Contains(parent, value) {
			t.Fatalf("value %q leaked in preview: %s", value, parent)
		}
	}
	args := append(guardedUpdateCLIArgs(description), "--apply", "--expected-proposal-hash", preview.ProposalHash)
	out, code := runCLI(t, env, args...)
	var applied app.JiraGuardedUpdateResult
	if err := json.Unmarshal([]byte(out), &applied); code != exitOK || err != nil || applied.Status != "applied" || !applied.WriteAttempted || !applied.Reconciled || !applied.Complete {
		t.Fatalf("apply exit=%d result=%+v err=%v output=%s", code, applied, err, out)
	}
	if fixture.puts != 1 || !strings.Contains(string(fixture.body), `"customfield_1":9007199254740993`) {
		t.Fatalf("puts=%d body=%s", fixture.puts, fixture.body)
	}
}

func TestJiraGuardedUpdateReadOnlyPolicyKeepsChildAvailable(t *testing.T) {
	fixture := newJiraGuardedUpdateCLIServer(t)
	description := filepath.Join(t.TempDir(), "description.wiki")
	if err := os.WriteFile(description, []byte("new body"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := jiraEnv(fixture.server)
	env["ATL_READ_ONLY"] = "1"
	if out, code := runCLI(t, env, guardedUpdateCLIArgs(description)...); code != exitCheckFailed || out != "" {
		t.Fatalf("parent exit=%d output=%q", code, out)
	}
	_, result := runJiraGuardedUpdatePreview(t, fixture, description, true, env)
	if result.Status != "would_apply" || fixture.puts != 0 {
		t.Fatalf("result=%+v puts=%d", result, fixture.puts)
	}
}

func TestJiraGuardedUpdateRejectsUnsafeInputsBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "update", "PROJ-1", "--summary", ""},
		{"jira", "issue", "update", "PROJ-1", "--field", `summary=override`},
		{"jira", "issue", "update", "PROJ-1", "--field", `priority={"name":"High"}`},
		{"jira", "issue", "update", "PROJ-1", "--field-json", `customfield_1={"a":1,"a":2}`},
		{"jira", "issue", "update", "PROJ-1", "--summary", "new", "--from-md", ""},
		{"jira", "issue", "update", "PROJ-1", "--summary", "new", "--apply"},
		{"jira", "issue", "update", "PROJ-1", "--summary", "new", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)},
	} {
		if out, code := runCLI(t, map[string]string{}, args...); code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d output=%q", args, code, out)
		}
	}
}

func TestJiraGuardedUpdateMalformedFieldErrorDoesNotEchoValue(t *testing.T) {
	const privateText = "PRIVATE-CANARY.example.invalid/secret"
	_, _, err := executeCLIRaw(t, map[string]string{}, "jira", "issue", "update", "PROJ-1", "--field", privateText)
	if err == nil || !errors.Is(err, domain.ErrUsage) || strings.Contains(err.Error(), privateText) {
		t.Fatalf("error=%q", err)
	}
	var rendered bytes.Buffer
	writeError(&rendered, "json", err, codeFor(err))
	if strings.Contains(rendered.String(), privateText) {
		t.Fatalf("JSON diagnostic leaked input: %s", rendered.String())
	}
}

type guardedUpdateAmbiguousCLIError struct{}

func (guardedUpdateAmbiguousCLIError) Error() string                  { return "unknown" }
func (guardedUpdateAmbiguousCLIError) Unwrap() error                  { return domain.ErrCheckFailed }
func (guardedUpdateAmbiguousCLIError) DiagnosticAmbiguousWrite() bool { return true }

func TestGuardedUpdateResultErrPreservesOutcomeEvidence(t *testing.T) {
	ambiguous := guardedUpdateAmbiguousCLIError{}
	joined := guardedUpdateResultErr(ambiguous, errors.New("stdout failed"), true)
	var marker interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.Is(joined, domain.ErrCheckFailed) || !errors.As(joined, &marker) || !marker.DiagnosticAmbiguousWrite() {
		t.Fatalf("joined error lost ambiguity: %v", joined)
	}
	if err := guardedUpdateResultErr(nil, errors.New("stdout failed"), true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("attempted successful write lost no-replay classification: %v", err)
	}
	definitive := guardedUpdateHTTPErrorForCLI(400)
	if err := guardedUpdateResultErr(definitive, errors.New("stdout failed"), true); !errors.Is(err, definitive) {
		t.Fatalf("definitive cause lost: %v", err)
	}
}

type guardedUpdateHTTPErrorForCLI int

func (e guardedUpdateHTTPErrorForCLI) Error() string   { return "rejected" }
func (e guardedUpdateHTTPErrorForCLI) HTTPStatus() int { return int(e) }

func TestJiraGuardedUpdateEffectProfilesDisableStartupUpdate(t *testing.T) {
	wants := map[string]string{
		"jira issue update":         capabilitydef.EffectGuardedUpdateApply,
		"jira issue update preview": capabilitydef.EffectGuardedUpdatePreview,
	}
	root := newRoot()
	for path, profileID := range wants {
		command, args, err := root.Find(strings.Fields(path))
		if err != nil || len(args) != 0 || !skipSelfUpdate(command) {
			t.Fatalf("path=%s command=%v args=%v err=%v skipped=%t", path, command, args, err, command != nil && skipSelfUpdate(command))
		}
		catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: path})
		if err != nil || len(catalog.Profiles) != 1 || catalog.Profiles[0].ID != profileID || catalog.Profiles[0].SelfUpdate != "disabled" {
			t.Fatalf("path=%s catalog=%+v err=%v", path, catalog, err)
		}
	}
}

func TestJiraGuardedUpdatePreconfigAndNoStartupUpdateWithMalformedConfig(t *testing.T) {
	var updateRequests atomic.Int64
	updateServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		updateRequests.Add(1)
	}))
	t.Cleanup(updateServer.Close)
	jiraServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("Jira must not be contacted with malformed configuration")
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
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", configDir)
	t.Setenv("ATL_UPDATE_URL", updateServer.URL)
	t.Setenv("ATL_JIRA_URL", jiraServer.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")

	for _, test := range []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "strict JSON before config", args: []string{"jira", "issue", "update", "PROJ-1", "--field-json", `customfield_1={"a":1,"a":2}`}, wantCode: exitUsage},
		{name: "strict hash before config", args: []string{"jira", "issue", "update", "PROJ-1", "--summary", "new", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)}, wantCode: exitUsage},
		{name: "valid input reaches config", args: []string{"jira", "issue", "update", "PROJ-1", "--summary", "new"}, wantCode: exitConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			updateRequests.Store(0)
			var stdout, stderr bytes.Buffer
			root := newRoot()
			setRootExecutionArgs(root, test.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			err := root.ExecuteContext(context.Background())
			if err == nil || codeFor(err) != test.wantCode || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("err=%v code=%d stdout=%q stderr=%q", err, codeFor(err), stdout.String(), stderr.String())
			}
			if updateRequests.Load() != 0 {
				t.Fatalf("startup update requests=%d", updateRequests.Load())
			}
		})
	}
}
