package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
	"github.com/isukharev/atl/internal/version"
)

type fieldSetBackend struct {
	mu          sync.Mutex
	requests    []string
	putFields   map[string]any
	putStatus   int
	commit      bool
	current     map[string]any
	desired     map[string]any
	initialTime string
	updatedTime string
}

func (b *fieldSetBackend) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.requests = append(b.requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
			_, _ = io.WriteString(w, `[{"id":"customfield_1","custom":true},{"id":"plugin.vendor","custom":true}]`)
		case r.Method == http.MethodGet && (r.URL.Path == "/rest/api/2/issue/ENG-1" || r.URL.Path == "/rest/api/2/issue/10001"):
			values, updated := b.current, b.initialTime
			if b.commit {
				values, updated = b.desired, b.updatedTime
			}
			fields := map[string]any{"project": map[string]any{"key": "ENG"}, "updated": updated}
			for field, value := range values {
				fields[field] = value
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "10001", "key": "ENG-1", "fields": fields})
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/10001":
			var payload struct {
				Fields map[string]any `json:"fields"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				t.Fatalf("decode PUT: %v", err)
			}
			b.putFields = payload.Fields
			if b.putStatus == 0 || b.putStatus < 300 {
				b.commit = true
			}
			status := b.putStatus
			if status == 0 {
				status = http.StatusNoContent
			}
			w.WriteHeader(status)
			if status >= 400 {
				_, _ = io.WriteString(w, `{"errorMessages":["synthetic rejection"]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func newFieldSetBackend() *fieldSetBackend {
	return &fieldSetBackend{
		current:     map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}},
		desired:     map[string]any{"customfield_1": `\{\}`, "plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}},
		initialTime: "2026-08-23T10:00:00.000+0000", updatedTime: "2026-08-23T10:01:00.000+0000",
	}
}

func fieldSetFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	markdown := filepath.Join(dir, "text.md")
	object := filepath.Join(dir, "option.json")
	if err := os.WriteFile(markdown, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte(`{"id":"2","large":9007199254740993}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return markdown, object
}

func fieldSetArgs(markdown, object string) []string {
	return []string{"jira", "issue", "field", "set", "ENG-1", "--from-md", "customfield_1=" + markdown, "--from-file", "plugin.vendor=" + object, "--allow-fields", "customfield_1,plugin.vendor"}
}

func decodeFieldResult(t *testing.T, output string) app.JiraFieldSetResult {
	t.Helper()
	var result app.JiraFieldSetResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output)
	}
	return result
}

func TestJiraGuardedFieldShapesNeverStartSelfUpdate(t *testing.T) {
	var updateRequests atomic.Int64
	updateServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { updateRequests.Add(1) }))
	t.Cleanup(updateServer.Close)
	backend := newFieldSetBackend()
	jiraServer := backend.serve(t)
	t.Cleanup(jiraServer.Close)
	markdown, object := fieldSetFiles(t)
	configDir := t.TempDir()
	oldVersion := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })
	for _, key := range []string{"ATL_NO_UPDATE", "ATL_READ_ONLY", "ATL_UPDATE_DEBUG", "ATL_VERBOSE", "ATL_POLICY", "ATL_POLICY_FILE", "ATL_POLICY_SHA256", "ATL_POLICY_REQUIRED"} {
		t.Setenv(key, "")
	}
	t.Setenv("ATL_UPDATE_URL", updateServer.URL)
	t.Setenv("ATL_CONFIG_DIR", configDir)
	t.Setenv("ATL_JIRA_URL", jiraServer.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")

	previewArgs := append([]string{"jira", "issue", "field", "preview"}, fieldSetArgs(markdown, object)[4:]...)
	preview := executeFieldShape(t, previewArgs)
	previewResult := decodeFieldResult(t, preview)
	applyArgs := append(fieldSetArgs(markdown, object), "--expected-updated", previewResult.ExpectedUpdated,
		"--expected-proposal-hash", previewResult.ProposalHash, "--apply")
	for _, test := range []struct {
		name string
		args []string
	}{
		{"preview", previewArgs}, {"set dry-run", fieldSetArgs(markdown, object)}, {"set apply", applyArgs},
	} {
		t.Run(test.name, func(t *testing.T) {
			updateRequests.Store(0)
			_ = executeFieldShape(t, test.args)
			if got := updateRequests.Load(); got != 0 {
				t.Fatalf("self-update requests=%d, want zero", got)
			}
			if _, err := os.Stat(filepath.Join(configDir, ".update-check")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("self-update stamp stat error=%v, want absent", err)
			}
		})
	}
}

func executeFieldShape(t *testing.T, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRoot()
	setRootExecutionArgs(root, args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute %v: %v stderr=%s", args, err, stderr.String())
	}
	return stdout.String()
}

func TestJiraIssueFieldPreviewIsReadOnlyAndStrictlyBounded(t *testing.T) {
	markdown, object := fieldSetFiles(t)
	backend := newFieldSetBackend()
	server := backend.serve(t)
	t.Cleanup(server.Close)
	env := jiraEnv(server)
	env["ATL_READ_ONLY"] = "1"
	args := fieldSetArgs(markdown, object)
	args[3] = "preview"
	out, code := runCLI(t, env, args...)
	result := decodeFieldResult(t, out)
	if code != exitOK || result.Status != "would_apply" || !result.Complete || result.WriteAttempted || result.Usage.Requests != 2 {
		t.Fatalf("exit=%d result=%+v", code, result)
	}
	if _, code := runCLI(t, env, fieldSetArgs(markdown, object)...); code != exitCheckFailed {
		t.Fatalf("field set dry-run must remain mutation-classified, exit=%d", code)
	}
}

func TestJiraIssueFieldSetApplyUsesDynamicHashAndExactGeometry(t *testing.T) {
	markdown, object := fieldSetFiles(t)
	backend := newFieldSetBackend()
	server := backend.serve(t)
	t.Cleanup(server.Close)
	args := fieldSetArgs(markdown, object)
	previewOut, code := runCLI(t, jiraEnv(server), args...)
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, previewOut)
	}
	preview := decodeFieldResult(t, previewOut)
	applyArgs := append(append([]string(nil), args...), "--apply", "--expected-updated", preview.ActualUpdated, "--expected-proposal-hash", preview.ProposalHash)
	out, code := runCLI(t, jiraEnv(server), applyArgs...)
	result := decodeFieldResult(t, out)
	if code != exitOK || result.Status != "applied" || !result.WriteAttempted || !result.Reconciled || !result.Complete {
		t.Fatalf("exit=%d result=%+v", code, result)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	gets, puts := 0, 0
	for _, request := range backend.requests {
		if strings.HasPrefix(request, "GET ") {
			gets++
		}
		if strings.HasPrefix(request, "PUT ") {
			puts++
		}
	}
	if gets != 7 || puts != 1 || backend.putFields["customfield_1"] != `\{\}` || backend.putFields["plugin.vendor"].(map[string]any)["large"] != json.Number("9007199254740993") {
		t.Fatalf("GET/PUT=%d/%d payload=%#v requests=%v", gets, puts, backend.putFields, backend.requests)
	}
}

func TestJiraIssueFieldSetBlockedUnknownAndDefinitiveFailedExits(t *testing.T) {
	markdown, object := fieldSetFiles(t)
	for _, test := range []struct {
		name       string
		putStatus  int
		expected   string
		wantStatus string
		wantExit   int
		ambiguous  bool
		terminal   bool
		cause      error
	}{
		{name: "stale updated", expected: "2026-08-23T09:59:00.000+0000", wantStatus: "blocked", wantExit: exitCheckFailed, terminal: true, cause: domain.ErrCheckFailed},
		{name: "ambiguous 500", putStatus: 500, wantStatus: "unknown", wantExit: exitGeneric, ambiguous: true},
		{name: "definitive 400", putStatus: 400, wantStatus: "failed", wantExit: exitUsage, cause: domain.ErrUsage},
	} {
		for _, output := range []string{"json", "text"} {
			t.Run(test.name+"/"+output, func(t *testing.T) {
				backend := newFieldSetBackend()
				backend.putStatus = test.putStatus
				server := backend.serve(t)
				defer server.Close()
				args := fieldSetArgs(markdown, object)
				previewOut, code := runCLI(t, jiraEnv(server), args...)
				if code != exitOK {
					t.Fatalf("preview exit=%d out=%s", code, previewOut)
				}
				preview := decodeFieldResult(t, previewOut)
				expected := preview.ActualUpdated
				if test.expected != "" {
					expected = test.expected
				}
				applyArgs := append(append([]string(nil), args...), "--apply", "--expected-updated", expected, "--expected-proposal-hash", preview.ProposalHash)
				if output == "text" {
					applyArgs = append([]string{"--output", "text"}, applyArgs...)
				}
				out, stderr, execErr := executeCLIRaw(t, jiraEnv(server), applyArgs...)
				if stderr != "" || codeFor(execErr) != test.wantExit {
					t.Fatalf("exit=%d stderr=%q err=%v", codeFor(execErr), stderr, execErr)
				}
				if test.cause != nil && !errors.Is(execErr, test.cause) {
					t.Fatalf("error=%v does not preserve %v", execErr, test.cause)
				}
				var terminal interface{ DiagnosticTerminalCheckFailure() bool }
				if got := errors.As(execErr, &terminal) && terminal.DiagnosticTerminalCheckFailure(); got != test.terminal {
					t.Fatalf("terminal marker=%t want=%t err=%v", got, test.terminal, execErr)
				}
				if test.ambiguous {
					assertLegacyMarkerOnlyAmbiguousExit(t, execErr)
				}
				if output == "json" {
					result := decodeFieldResult(t, out)
					if result.Status != test.wantStatus || result.ExpectedUpdated != expected || result.ActualUpdated != preview.ActualUpdated {
						t.Fatalf("result=%+v", result)
					}
					return
				}
				expectedResult := preview
				expectedResult.Mode, expectedResult.Status, expectedResult.ExpectedUpdated = "apply", test.wantStatus, expected
				switch test.wantStatus {
				case "blocked":
					expectedResult.WriteAttempted, expectedResult.Reconciled, expectedResult.Complete = false, false, true
				case "unknown", "failed":
					expectedResult.WriteAttempted, expectedResult.Reconciled, expectedResult.Complete = true, true, true
				}
				if want := jiraFieldSetText(&expectedResult) + "\n"; out != want {
					t.Fatalf("text output=%q want=%q", out, want)
				}
			})
		}
	}
}

func TestJiraIssueFieldLocalUsageHasNoResultAndNoBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(path, []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newFieldSetBackend()
	server := backend.serve(t)
	defer server.Close()
	for _, args := range [][]string{
		{"jira", "issue", "field", "preview", "ENG-1", "--from-file", "project=" + path, "--allow-fields", "project"},
		{"jira", "issue", "field", "preview", "ENG-1", "--from-file", "customfield_1=" + path, "--allow-fields", "customfield_1,customfield_1"},
		{"jira", "issue", "field", "set", "ENG-1", "--from-file", "customfield_1=" + path, "--allow-fields", "customfield_1", "--expected-updated", "2026-08-23T10:00:00.000+0000"},
	} {
		stdout, _, err := executeCLIRaw(t, jiraEnv(server), args...)
		if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage || stdout != "" {
			t.Fatalf("args=%v stdout=%q err=%v", args, stdout, err)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.requests) != 0 {
		t.Fatalf("local usage reached backend: %v", backend.requests)
	}
}

func TestRawJiraFieldClassificationContract(t *testing.T) {
	if domain.JiraGuardedFieldMaxJSONNestingDepth != strictjson.MaxNestingDepth {
		t.Fatalf("product/evaluator JSON nesting bounds drifted: %d != %d", domain.JiraGuardedFieldMaxJSONNestingDepth, strictjson.MaxNestingDepth)
	}
	if domain.JiraGuardedFieldMaxValueNestingDepth != strictjson.MaxNestingDepth-3 {
		t.Fatalf("guarded value/result envelope depth=%d want=%d", domain.JiraGuardedFieldMaxValueNestingDepth, strictjson.MaxNestingDepth-3)
	}
	tests := []struct {
		name    string
		body    []byte
		kind    string
		wantErr bool
	}{
		{name: "object", body: []byte(" \t{\"n\":9007199254740993}\r\n"), kind: "object"},
		{name: "array", body: []byte(`[1,{"x":"\uD83D\uDE00"}]`), kind: "array"},
		{name: "scalar", body: []byte(" true "), kind: "string"},
		{name: "null", body: []byte(" null "), kind: "string"},
		{name: "malformed", body: []byte(`{"a":}`), kind: "string"},
		{name: "incomplete duplicate", body: []byte(`{"a":1,"a":`), kind: "string"},
		{name: "incomplete surrogate", body: []byte(`{"x":"\uD800`), kind: "string"},
		{name: "unicode whitespace", body: []byte("\u00a0{\"a\":1}"), kind: "string"},
		{name: "duplicate", body: []byte(`{"a":1,"a":2}`), wantErr: true},
		{name: "nested duplicate", body: []byte(`{"a":{"b":1,"b":2}}`), wantErr: true},
		{name: "surrogate", body: []byte(`{"x":"\uD800"}`), wantErr: true},
		{name: "trailing", body: []byte(`{"a":1} []`), wantErr: true},
		{name: "invalid utf8", body: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, wantErr: true},
		{name: "invalid utf8 before scalar classification", body: []byte{'x', 0xff}, wantErr: true},
		{name: "maximum value nesting", body: []byte(strings.Repeat("[", domain.JiraGuardedFieldMaxValueNestingDepth) + "0" + strings.Repeat("]", domain.JiraGuardedFieldMaxValueNestingDepth)), kind: "array"},
		{name: "over value nesting", body: []byte(strings.Repeat("[", domain.JiraGuardedFieldMaxValueNestingDepth+1) + "0" + strings.Repeat("]", domain.JiraGuardedFieldMaxValueNestingDepth+1)), wantErr: true},
		{name: "over value nesting incomplete", body: []byte(strings.Repeat("[", domain.JiraGuardedFieldMaxValueNestingDepth+1)), wantErr: true},
		{name: "maximum value nesting incomplete", body: []byte(strings.Repeat("[", domain.JiraGuardedFieldMaxValueNestingDepth)), kind: "string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := rawJiraFieldValue(test.body)
			if (err != nil) != test.wantErr {
				t.Fatalf("value=%#v err=%v", value, err)
			}
			if err != nil {
				return
			}
			kind := "string"
			switch value.(type) {
			case map[string]any:
				kind = "object"
			case []any:
				kind = "array"
			}
			if kind != test.kind {
				t.Fatalf("kind=%s value=%#v", kind, value)
			}
			if kind == "string" && value != string(test.body) {
				t.Fatalf("string bytes changed: %#v", value)
			}
		})
	}
}

func TestRawJiraFieldStrictErrorIsContentFree(t *testing.T) {
	secret := "private_json_member_marker"
	_, err := rawJiraFieldValue([]byte(`{"` + secret + `":1,"` + secret + `":2}`))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), `"`+secret+`"`) {
		t.Fatalf("strict desired JSON diagnostic leaked structure: %v", err)
	}
}

func TestJiraFieldInputAggregateBoundAndDuplicateStdin(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("12"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("34"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jiraFieldProposalsWithLimit([]string{"customfield_1=" + a, "plugin.vendor=" + b}, nil, 3); err == nil {
		t.Fatal("aggregate two-file limit should be refused")
	}
	if _, err := jiraFieldProposalsWithLimit([]string{"customfield_1=-", "plugin.vendor=-"}, nil, 3); err == nil {
		t.Fatal("duplicate stdin should be refused before reading")
	}
}
