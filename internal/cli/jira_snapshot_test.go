package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/httpx"
)

func scaffoldJiraSnapshotMirror(t *testing.T, root, body string) string {
	t.Helper()
	wiki := scaffoldJiraMirror(t, root, "PROJ-1", body)
	base := strings.TrimSuffix(wiki, ".wiki")
	if err := os.WriteFile(base+".json", []byte(issueJSON("PROJ-1", body)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".md", []byte("<!-- atl:document jira-issue v3 -->\n# Private summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return wiki
}

func TestJiraSnapshotIsOfflineContentFreeAndDeterministic(t *testing.T) {
	root := t.TempDir()
	wiki := scaffoldJiraSnapshotMirror(t, root, "Sensitive body")
	if err := os.WriteFile(wiki, []byte("Local edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, nil, "--read-only", "jira", "snapshot", root)
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.JiraMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete || !got.Reconciled || got.Local.Present != 1 || got.Local.LocallyEdited != 1 ||
		got.Native.Modified != 1 || got.Snapshot.KeyMatched != 1 || got.Render.Current != 1 || got.Remote.Attempted != 0 {
		t.Fatalf("snapshot=%+v", got)
	}
	for _, private := range []string{"PROJ-1", "Private summary", "Sensitive body", "Local edit", root, wiki} {
		if strings.Contains(out, private) {
			t.Fatalf("snapshot leaked %q: %s", private, out)
		}
	}
	assertGolden(t, "jira_snapshot.json", []byte(out))

	textOut, code := runCLI(t, nil, "--read-only", "jira", "snapshot", root, "-o", "text")
	if code != exitOK || !strings.Contains(textOut, "complete=true reconciled=true total=1 present=1 edited=1") {
		t.Fatalf("text exit=%d output=%q", code, textOut)
	}
}

func TestJiraSnapshotIncompleteRemotePreflightNeedsNoBackendConfig(t *testing.T) {
	root := t.TempDir()
	scaffoldJiraSnapshotMirror(t, root, "base")
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "PROJ-1.wiki"), []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "--read-only", "jira", "snapshot", root, "--remote")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.JiraMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete || !got.RemoteRequested || !got.Remote.Requested || got.Remote.Attempted != 0 || got.Remote.NotAttempted != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
	_, snapshotErr := app.SnapshotJiraMirror(root)
	if snapshotErr == nil {
		t.Fatal("baseline mismatch did not return an error")
	}
	var renderedError bytes.Buffer
	writeError(&renderedError, "json", snapshotErr, codeFor(snapshotErr))
	for _, private := range []string{"PROJ-1", root} {
		if strings.Contains(renderedError.String(), private) {
			t.Fatalf("rendered snapshot error leaked %q: %s", private, renderedError.String())
		}
	}
}

func TestJiraSnapshotRemoteMakesOneIssueRequest(t *testing.T) {
	root := t.TempDir()
	scaffoldJiraSnapshotMirror(t, root, "base")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("method=%s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueJSON("PROJ-1", "base")))
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.JiraMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !got.RemoteRequested || got.Remote.Attempted != 1 || got.Remote.Checked != 1 || got.Remote.InSync != 1 || !got.Remote.Reconciled {
		t.Fatalf("requests=%d snapshot=%+v", requests, got)
	}
}

func TestJiraSnapshotVerboseRemoteTraceIsContentFree(t *testing.T) {
	t.Cleanup(func() { httpx.SetTrace(nil) })
	root := t.TempDir()
	scaffoldJiraSnapshotMirror(t, root, "private body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueJSON("PROJ-1", "private body")))
	}))
	t.Cleanup(srv.Close)

	stdout, stderr, code := runCLIFull(t, jiraEnv(srv), "--verbose", "--read-only", "jira", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, private := range []string{"PROJ-1", "private body", root, "/rest/api/"} {
		if strings.Contains(stdout, private) || strings.Contains(stderr, private) {
			t.Fatalf("verbose snapshot leaked %q: stdout=%q stderr=%q", private, stdout, stderr)
		}
	}
	if !strings.Contains(stderr, "<redacted>") {
		t.Fatalf("verbose trace was not visibly redacted: %q", stderr)
	}
}

func TestJiraSnapshotRemoteDisablesAutomaticGETRetries(t *testing.T) {
	root := t.TempDir()
	scaffoldJiraSnapshotMirror(t, root, "base")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"errorMessages":["retryable"]}`))
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.JiraMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || got.Complete || got.Remote.Attempted != 1 || got.Remote.Unavailable != 1 || got.Remote.Checked != 0 {
		t.Fatalf("requests=%d snapshot=%+v", requests, got)
	}
}
