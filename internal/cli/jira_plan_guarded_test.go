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
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func jiraPlanCLIFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.csv")
	data := "schema_version,operation,source,target,type,field,value\n2,label_add,OPS-1,,,,new\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJiraPlanPreviewApplyLifecycleAndOutput(t *testing.T) {
	var mu sync.Mutex
	put, reads, writes := false, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			put, writes = true, writes+1
			w.WriteHeader(http.StatusNoContent)
			return
		}
		reads++
		labels, updated := `["old","private-current"]`, "2026-08-22T10:00:00Z"
		if put {
			labels, updated = `["new","old","private-current"]`, "2026-08-22T10:00:01Z"
		}
		_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":`+labels+`,"updated":"`+updated+`"}}`)
	}))
	t.Cleanup(server.Close)
	path := jiraPlanCLIFile(t)
	previewOut, code := runCLI(t, jiraEnv(server), "jira", "issue", "plan", "preview", "--csv", path, "--allow-ops", "label_add")
	var preview app.JiraPlanResult
	if code != exitOK || json.Unmarshal([]byte(previewOut), &preview) != nil || preview.Mode != "preview" || preview.Status != "would_apply" || !preview.Complete || preview.ProposalHash == "" {
		t.Fatalf("preview exit=%d output=%s decoded=%+v", code, previewOut, preview)
	}
	if strings.Contains(previewOut, path) || strings.Contains(previewOut, `"value"`) || strings.Contains(previewOut, "private-current") {
		t.Fatalf("preview leaked local or remote content: %s", previewOut)
	}
	applyOut, code := runCLI(t, jiraEnv(server), "jira", "issue", "plan", "apply", "--csv", path, "--allow-ops", "label_add", "--confirm", "APPLY", "--expected-proposal-hash", preview.ProposalHash)
	var applied app.JiraPlanResult
	if code != exitOK || json.Unmarshal([]byte(applyOut), &applied) != nil || applied.Mode != "apply" || applied.Status != "applied" || !applied.Complete || applied.ProposalHash != preview.ProposalHash {
		t.Fatalf("apply exit=%d output=%s decoded=%+v", code, applyOut, applied)
	}
	mu.Lock()
	if reads != 4 || writes != 1 {
		mu.Unlock()
		t.Fatalf("reads=%d writes=%d", reads, writes)
	}
	mu.Unlock()
	textOut, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "plan", "preview", "--csv", path, "--allow-ops", "label_add")
	if code != exitOK || !strings.HasPrefix(textOut, "plan\tmode=preview\tstatus=already_satisfied") || strings.Contains(textOut, "new") {
		t.Fatalf("text exit=%d output=%q", code, textOut)
	}
}

func TestJiraPlanGuardsPrecedeOpenConfigAndNetwork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.csv")
	for _, args := range [][]string{
		{"jira", "issue", "plan", "apply", "--csv", missing},
		{"jira", "issue", "plan", "apply", "--csv", missing, "--confirm", "WRONG", "--expected-proposal-hash", strings.Repeat("a", 64)},
		{"jira", "issue", "plan", "apply", "--csv", missing, "--confirm", "APPLY", "--expected-proposal-hash", strings.Repeat("A", 64)},
	} {
		out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"}, args...)
		if codeFor(err) != exitUsage || out != "" {
			t.Fatalf("args=%v output=%q err=%v", args, out, err)
		}
	}
	out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"}, "--read-only", "jira", "issue", "plan", "apply", "--csv", missing, "--confirm", "APPLY", "--expected-proposal-hash", strings.Repeat("a", 64))
	if codeFor(err) != exitCheckFailed || out != "" {
		t.Fatalf("read-only output=%q err=%v", out, err)
	}
}

func TestJiraPlanRawPolicyDenyPrecedesConfigurationAndJira(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := jiraPlanCLIFile(t)
	out, _, err := executeCLIRaw(t, map[string]string{
		"ATL_CONFIG_DIR": configDir,
		"ATL_POLICY":     `{"schema_version":1,"rules":[{"id":"deny-plan-target","effect":"deny","verbs":["update"],"resource":{"service":"jira","key":"OPS-1"}}]}`,
	}, "jira", "issue", "plan", "apply", "--csv", path, "--allow-ops", "label_add", "--confirm", "APPLY", "--expected-proposal-hash", strings.Repeat("a", 64))
	if !errors.Is(err, domain.ErrCheckFailed) || out != "" {
		t.Fatalf("output=%q err=%v", out, err)
	}
}

func TestJiraPlanPreviewCanonicalPolicyDenyReturnsQualifiedResult(t *testing.T) {
	reads, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		reads++
		_, _ = io.WriteString(w, `{"id":"10","key":"MOVED-2","fields":{"project":{"key":"MOVED"},"labels":[],"updated":"2026-08-22T10:00:00Z"}}`)
	}))
	t.Cleanup(server.Close)
	environment := jiraEnv(server)
	environment["ATL_POLICY"] = `{"schema_version":1,"rules":[{"id":"deny-qualified-plan-target","effect":"deny","verbs":["update"],"resource":{"service":"jira","key":"MOVED-2"}}]}`
	out, code := runCLI(t, environment, "jira", "issue", "plan", "preview", "--csv", jiraPlanCLIFile(t), "--allow-ops", "label_add")
	var result app.JiraPlanResult
	if code != exitCheckFailed || json.Unmarshal([]byte(out), &result) != nil || result.Status != "blocked" || result.ProposalHash != "" || result.Rows[0].Reason != "policy_denied" || reads != 1 || writes != 0 {
		t.Fatalf("exit=%d output=%q result=%+v reads=%d writes=%d", code, out, result, reads, writes)
	}
}

func TestJiraPlanApplyEmitsClosedResultBeforeHashMismatchError(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":"2026-08-22T10:00:00Z"}}`)
	}))
	t.Cleanup(server.Close)
	path := jiraPlanCLIFile(t)
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "plan", "apply", "--csv", path, "--allow-ops", "label_add", "--confirm", "APPLY", "--expected-proposal-hash", strings.Repeat("0", 64))
	var result app.JiraPlanResult
	if code != exitCheckFailed || json.Unmarshal([]byte(out), &result) != nil || result.Status != "blocked" || result.Rows[0].Reason != "proposal_changed" || writes != 0 {
		t.Fatalf("exit=%d output=%q result=%+v writes=%d", code, out, result, writes)
	}
}

func TestJiraPlanLeavesHaveDedicatedNoUpdateEffects(t *testing.T) {
	root := newRoot()
	for _, item := range []struct{ path, access, effect string }{
		{"jira issue plan preview", "read-only", "guarded-plan-preview"},
		{"jira issue plan apply", "mutating", "guarded-plan-apply"},
	} {
		command, _, err := root.Find(strings.Fields(item.path))
		if err != nil || command.Annotations[accessAnnotation] != item.access || command.Annotations[effectProfileAnnotation] != item.effect || !skipSelfUpdate(command) {
			t.Fatalf("%s command=%v access=%q effect=%q skip=%t err=%v", item.path, command, command.Annotations[accessAnnotation], command.Annotations[effectProfileAnnotation], skipSelfUpdate(command), err)
		}
	}
}

func TestJiraPlanRootReuseClearsConsumedAndInjectedDocumentSlots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"10","key":"OPS-1","fields":{"project":{"key":"OPS"},"labels":[],"updated":"2026-08-22T10:00:00Z"}}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_JIRA_URL", server.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")
	root := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	args := []string{"jira", "issue", "plan", "preview", "--csv", jiraPlanCLIFile(t), "--allow-ops", "label_add"}
	for invocation := 0; invocation < 2; invocation++ {
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("invocation %d: %v", invocation, err)
		}
		command, _, err := root.Find(args[:4])
		if err != nil {
			t.Fatal(err)
		}
		runtime := invocationRuntimeFor(command)
		if runtime.jiraPlanDocument != nil || runtime.jiraPlanCommand != "" {
			t.Fatalf("invocation %d retained consumed document", invocation)
		}
		if invocation == 0 {
			runtime.jiraPlanDocument = &app.JiraPlanDocument{}
			runtime.jiraPlanCommand = "apply"
		}
		output.Reset()
	}
}
