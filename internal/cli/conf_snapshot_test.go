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
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func writeSnapshotPage(t *testing.T, root, id, title string) string {
	t.Helper()
	m := mirror.New(root)
	page := &domain.Resource{ID: id, Title: title, SpaceKey: "DOC", Version: 3, Body: []byte(`<p>Sensitive body</p>`)}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, slug+".csf")
}

func TestConfSnapshotIsOfflineContentFreeAndDeterministic(t *testing.T) {
	root := t.TempDir()
	path := writeSnapshotPage(t, root, "901", "Private title")
	if err := os.WriteFile(path, []byte(`<p>Local edit</p>`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, nil, "--read-only", "conf", "snapshot", root)
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.ConfluenceMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete || !got.Reconciled || got.Local.Present != 1 || got.Local.LocallyEdited != 1 ||
		got.Native.Modified != 1 || got.Validation.Valid != 1 || got.Remote.Attempted != 0 {
		t.Fatalf("snapshot=%+v", got)
	}
	for _, private := range []string{"901", "Private title", "Sensitive body", "Local edit", root, path} {
		if strings.Contains(out, private) {
			t.Fatalf("snapshot leaked %q: %s", private, out)
		}
	}
	assertGolden(t, "conf_snapshot.json", []byte(out))

	textOut, code := runCLI(t, nil, "--read-only", "conf", "snapshot", root, "-o", "text")
	if code != exitOK || !strings.Contains(textOut, "complete=true reconciled=true total=1 present=1 edited=1") {
		t.Fatalf("text exit=%d output=%q", code, textOut)
	}
}

func TestConfSnapshotIncompleteRemotePreflightNeedsNoBackendConfig(t *testing.T) {
	root := t.TempDir()
	writeSnapshotPage(t, root, "904", "Missing baseline")
	if err := os.Remove(filepath.Join(root, ".atl", "base", "904.csf")); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "--read-only", "conf", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.ConfluenceMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete || !got.RemoteRequested || !got.Remote.Requested || got.Remote.Attempted != 0 || got.Remote.NotAttempted != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestConfSnapshotBaselineMismatchEmitsQualifiedExitEight(t *testing.T) {
	root := t.TempDir()
	writeSnapshotPage(t, root, "902", "Blocked")
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "902.csf"), []byte(`<p>wrong base</p>`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, nil, "--read-only", "conf", "snapshot", root, "--remote")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.ConfluenceMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Complete || !got.Reconciled || got.Native.BaselineMismatch != 1 || !got.RemoteRequested || got.Remote.Attempted != 0 {
		t.Fatalf("snapshot=%+v", got)
	}
	_, snapshotErr := app.SnapshotConfluenceMirror(root)
	if snapshotErr == nil {
		t.Fatal("baseline mismatch did not return an error")
	}
	var renderedError bytes.Buffer
	writeError(&renderedError, "json", snapshotErr, codeFor(snapshotErr))
	for _, private := range []string{"902", "Blocked", root} {
		if strings.Contains(renderedError.String(), private) {
			t.Fatalf("rendered snapshot error leaked %q: %s", private, renderedError.String())
		}
	}
}

func TestConfSnapshotRemoteMakesOneMetadataRequest(t *testing.T) {
	root := t.TempDir()
	writeSnapshotPage(t, root, "903", "Remote")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("method=%s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"903","title":"Remote","space":{"key":"DOC"},"version":{"number":3}}`))
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, confEnv(srv), "--read-only", "conf", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.ConfluenceMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !got.RemoteRequested || got.Remote.Attempted != 1 || got.Remote.Checked != 1 || got.Remote.InSync != 1 || !got.Remote.Reconciled {
		t.Fatalf("requests=%d snapshot=%+v", requests, got)
	}
}

func TestConfSnapshotRemoteDisablesAutomaticGETRetries(t *testing.T) {
	root := t.TempDir()
	writeSnapshotPage(t, root, "905", "Unavailable")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"retryable"}`))
	}))
	t.Cleanup(srv.Close)

	out, code := runCLI(t, confEnv(srv), "--read-only", "conf", "snapshot", root, "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var got app.ConfluenceMirrorSnapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || got.Complete || got.Remote.Attempted != 1 || got.Remote.Unavailable != 1 || got.Remote.Checked != 0 {
		t.Fatalf("requests=%d snapshot=%+v", requests, got)
	}
}
