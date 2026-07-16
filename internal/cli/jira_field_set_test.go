package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func fieldSetServer(t *testing.T, currentUpdated string, putFields *map[string]any, putCalls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
			_, _ = io.WriteString(w, `[
				{"id":"customfield_1","name":"Notes","custom":true,"schema":{"type":"string"}},
				{"id":"customfield_2","name":"Choice","custom":true,"schema":{"type":"option"}}
			]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1":
			_, _ = io.WriteString(w, `{"key":"ENG-1","fields":{"updated":"`+currentUpdated+`","customfield_1":"old","customfield_2":{"id":"1"}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/ENG-1":
			*putCalls++
			var payload struct {
				Fields map[string]any `json:"fields"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				t.Fatalf("decode PUT: %v", err)
			}
			*putFields = payload.Fields
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestJiraIssueFieldSetDryRunConvertsMarkdownAndCapturesUpdated(t *testing.T) {
	mdPath := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(mdPath, []byte("# Progress\n\nDone."), 0o644); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	putCalls := 0
	srv := fieldSetServer(t, "2026-07-10T10:00:00.000+0000", &put, &putCalls)
	t.Cleanup(srv.Close)

	out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "field", "set", "ENG-1",
		"--from-md", "customfield_1="+mdPath, "--allow-fields", "customfield_1")
	if code != exitOK {
		t.Fatalf("field set dry-run: exit=%d stdout=%s", code, out)
	}
	var res app.JiraFieldSetResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if res.Status != "would_apply" || res.ExpectedUpdated != "2026-07-10T10:00:00.000+0000" || putCalls != 0 {
		t.Fatalf("result=%+v putCalls=%d", res, putCalls)
	}
	if len(res.ProposalHash) != 64 {
		t.Fatalf("proposal hash = %q", res.ProposalHash)
	}
	if len(res.Fields) != 1 || res.Fields[0].Value != "h1. Progress\n\nDone." || res.Fields[0].Kind != "string" {
		t.Fatalf("Markdown proposal = %+v", res.Fields)
	}
}

func TestJiraIssueFieldPreviewRunsUnderReadOnlyPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("reviewed"), 0o644); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	putCalls := 0
	srv := fieldSetServer(t, "fresh", &put, &putCalls)
	t.Cleanup(srv.Close)
	env := jiraEnv(srv)
	env["ATL_READ_ONLY"] = "1"
	out, code := runCLI(t, env, "jira", "issue", "field", "preview", "ENG-1",
		"--from-file", "customfield_1="+path, "--allow-fields", "customfield_1")
	if code != exitOK || putCalls != 0 || !strings.Contains(out, `"status": "would_apply"`) {
		t.Fatalf("preview: exit=%d puts=%d output=%s", code, putCalls, out)
	}
	if _, code := runCLI(t, env, "jira", "issue", "field", "set", "ENG-1",
		"--from-file", "customfield_1="+path, "--allow-fields", "customfield_1"); code != exitCheckFailed {
		t.Fatalf("field set dry-run must remain classified as mutating, exit=%d", code)
	}
}

func TestJiraIssueFieldSetApplySendsExplicitStringAndObject(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "text.wiki")
	objectPath := filepath.Join(dir, "option.json")
	if err := os.WriteFile(textPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, []byte(`{"id":"2","large":9007199254740993}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	putCalls := 0
	srv := fieldSetServer(t, "fresh", &put, &putCalls)
	t.Cleanup(srv.Close)
	baseArgs := []string{
		"jira", "issue", "field", "set", "ENG-1",
		"--from-md", "customfield_1=" + textPath,
		"--from-file", "customfield_2=" + objectPath,
		"--allow-fields", "customfield_1,customfield_2",
	}
	previewOut, code := runCLI(t, jiraEnv(srv), baseArgs...)
	if code != exitOK {
		t.Fatalf("field set preview: exit=%d stdout=%s", code, previewOut)
	}
	var preview app.JiraFieldSetResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}

	applyArgs := append(append([]string(nil), baseArgs...),
		"--expected-updated", "fresh", "--expected-proposal-hash", preview.ProposalHash, "--apply")
	out, code := runCLI(t, jiraEnv(srv), applyArgs...)
	if code != exitOK || putCalls != 1 {
		t.Fatalf("field set apply: exit=%d putCalls=%d stdout=%s", code, putCalls, out)
	}
	if got, ok := put["customfield_1"].(string); !ok || got != `\{\}` {
		t.Fatalf("Markdown string = %#v, want literal converted wiki", put["customfield_1"])
	}
	if got, ok := put["customfield_2"].(map[string]any); !ok || got["id"] != "2" || got["large"] != json.Number("9007199254740993") {
		t.Fatalf("object value = %#v", put["customfield_2"])
	}
}

func TestJiraIssueFieldSetStaleApplyReturnsExit8WithoutWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	putCalls := 0
	srv := fieldSetServer(t, "fresh", &put, &putCalls)
	t.Cleanup(srv.Close)
	baseArgs := []string{
		"jira", "issue", "field", "set", "ENG-1", "--from-file", "customfield_1=" + path,
		"--allow-fields", "customfield_1",
	}
	previewOut, code := runCLI(t, jiraEnv(srv), baseArgs...)
	if code != exitOK {
		t.Fatalf("preview: exit=%d stdout=%s", code, previewOut)
	}
	var preview app.JiraFieldSetResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}

	applyArgs := append(append([]string(nil), baseArgs...),
		"--expected-updated", "stale", "--expected-proposal-hash", preview.ProposalHash, "--apply")
	out, code := runCLI(t, jiraEnv(srv), applyArgs...)
	if code != exitCheckFailed || putCalls != 0 || !strings.Contains(out, `"status": "blocked"`) {
		t.Fatalf("stale apply: exit=%d putCalls=%d stdout=%s", code, putCalls, out)
	}
}

func TestJiraIssueFieldSetChangedFileFailsProposalHashBeforeWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(path, []byte("reviewed"), 0o644); err != nil {
		t.Fatal(err)
	}
	var put map[string]any
	putCalls := 0
	srv := fieldSetServer(t, "fresh", &put, &putCalls)
	t.Cleanup(srv.Close)
	baseArgs := []string{
		"jira", "issue", "field", "set", "ENG-1", "--from-file", "customfield_1=" + path,
		"--allow-fields", "customfield_1",
	}
	previewOut, code := runCLI(t, jiraEnv(srv), baseArgs...)
	if code != exitOK {
		t.Fatalf("preview: exit=%d stdout=%s", code, previewOut)
	}
	var preview app.JiraFieldSetResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed after review"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyArgs := append(append([]string(nil), baseArgs...),
		"--expected-updated", "fresh", "--expected-proposal-hash", preview.ProposalHash, "--apply")
	out, code := runCLI(t, jiraEnv(srv), applyArgs...)
	if code != exitCheckFailed || putCalls != 0 || !strings.Contains(out, `"status": "blocked"`) {
		t.Fatalf("changed-file apply: exit=%d puts=%d stdout=%s", code, putCalls, out)
	}
}

func TestJiraIssueFieldSetLargeIntegerAlreadySatisfiedExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.json")
	if err := os.WriteFile(path, []byte(`{"large":9007199254740993}`), 0o644); err != nil {
		t.Fatal(err)
	}
	putCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
			_, _ = io.WriteString(w, `[{"id":"customfield_1","name":"Data","custom":true,"schema":{"type":"object"}}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ENG-1":
			_, _ = io.WriteString(w, `{"key":"ENG-1","fields":{"updated":"fresh","customfield_1":{"large":9007199254740993}}}`)
		case r.Method == http.MethodPut:
			putCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "field", "set", "ENG-1",
		"--from-file", "customfield_1="+path, "--allow-fields", "customfield_1")
	if code != exitOK || putCalls != 0 || !strings.Contains(out, `"status": "already_satisfied"`) {
		t.Fatalf("large integer no-op: exit=%d putCalls=%d stdout=%s", code, putCalls, out)
	}
}

func TestJiraFieldInputParsingAndBound(t *testing.T) {
	if got, ok := rawJiraFieldValue([]byte(` {"id":"2","large":9007199254740993} `)).(map[string]any); !ok || got["id"] != "2" || got["large"] != json.Number("9007199254740993") {
		t.Fatalf("object parsing = %#v", got)
	}
	if got := rawJiraFieldValue([]byte(`true`)); got != "true" {
		t.Fatalf("scalar JSON must stay string, got %#v", got)
	}
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readJiraFieldInput(path, 3); err == nil {
		t.Fatal("oversized field file should be refused")
	}
	a := filepath.Join(t.TempDir(), "a.txt")
	b := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(a, []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("34"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := jiraFieldProposalsWithLimit([]string{"customfield_1=" + a, "customfield_2=" + b}, nil, 3); err == nil {
		t.Fatal("aggregate two-file limit should be refused")
	}
	if _, err := jiraFieldProposalsWithLimit([]string{"customfield_1=-", "customfield_2=-"}, nil, 3); err == nil {
		t.Fatal("duplicate stdin should be refused before reading")
	}
}
