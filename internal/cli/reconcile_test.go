package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestConfluenceReconcileCLIJSONTextAndIDContract(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{ID: "42", Type: "page", Title: "Page", SpaceKey: "EX", Version: 2, Body: []byte("<p>base</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, slug+".csf")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","type":"page","title":"Page","space":{"key":"EX"},"version":{"number":2},"body":{"storage":{"value":"<p>base</p>"}}}`))
	}))
	defer server.Close()
	bindCLIMirrorBackend(t, root, "confluence", server.URL)

	out, code := runCLI(t, confEnv(server), "--read-only", "conf", "reconcile", "preview", path, "--into", root)
	if code != exitOK || requests != 1 || !strings.Contains(out, `"service": "confluence"`) || !strings.Contains(out, `"state": "unchanged"`) || strings.Contains(out, "<p>base</p>") {
		t.Fatalf("exit=%d requests=%d out=%q", code, requests, out)
	}
	text, textCode := runCLI(t, confEnv(server), "--read-only", "conf", "reconcile", "preview", path, "--into", root, "-o", "text")
	if textCode != exitOK || !strings.Contains(text, "unchanged") {
		t.Fatalf("text exit=%d out=%q", textCode, text)
	}
	before := requests
	if _, idCode := runCLI(t, confEnv(server), "--read-only", "conf", "reconcile", "preview", path, "--into", root, "-o", "id"); idCode != exitUsage || requests != before {
		t.Fatalf("id exit=%d requests=%d want pre-network usage refusal", idCode, requests)
	}
}

func TestJiraReconcileCLIReadsOnceWithoutContent(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "EX")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "EX-1.wiki")
	body := []byte("private base text")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(mirror.SyncState{ID: "EX-1", Hash: mirror.Hash(body), Path: filepath.Join("EX", "EX-1.wiki")})
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveBaseExt("EX-1", body, ".wiki"); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"10001","key":"EX-1","fields":{"description":"private base text","updated":"2026-08-02T12:34:56.000+0000"}}`))
	}))
	defer server.Close()
	bindCLIMirrorBackend(t, root, "jira", server.URL)
	out, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "reconcile", "preview", path, "--into", root)
	if code != exitOK || requests != 1 || !strings.Contains(out, `"service": "jira"`) || strings.Contains(out, "private base text") {
		t.Fatalf("exit=%d requests=%d out=%q", code, requests, out)
	}
}
