package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestConfStatusCorruptMirrorMetadataMapsToExitEight(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.csf"), []byte("<p>x</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runCLI(t, confEnv(srv), "conf", "status", root); code != exitCheckFailed {
		t.Fatalf("conf status exit=%d stdout=%q, want %d", code, out, exitCheckFailed)
	}
}

func TestConfStatusLocalDoesNotRequireBackendConfig(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	page := &domain.Resource{ID: "123", Title: "Offline", SpaceKey: "DOCS", Version: 1, Body: []byte("<p>x</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, nil, "conf", "status", root)
	if code != exitOK || !strings.Contains(out, `"entries"`) {
		t.Fatalf("local conf status exit=%d out=%q", code, out)
	}
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"confluence_url":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, "conf", "status", root); code != exitOK || !strings.Contains(out, `"entries"`) {
		t.Fatalf("local conf status with malformed config exit=%d out=%q", code, out)
	}
	if out, code := runCLI(t, nil, "conf", "status", root, "--remote"); code != exitConfig || out != "" {
		t.Fatalf("remote conf status without config exit=%d out=%q, want %d and empty stdout", code, out, exitConfig)
	}
}

func TestConfStatusIdentifiesNonCanonicalRelocationCopy(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	root := t.TempDir()
	m := mirror.New(root)
	old := &domain.Resource{ID: "123", Title: "Old", SpaceKey: "S", Version: 1, Body: []byte("<p>x</p>")}
	oldDir, oldSlug := m.PageDir(old.SpaceKey, nil, old.Title)
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	newPage := *old
	newPage.Title = "New"
	newDir, newSlug := m.PageDir(newPage.SpaceKey, nil, newPage.Title)
	if err := m.Write(newDir, newSlug, &newPage, nil); err != nil {
		t.Fatal(err)
	}
	textOut, code := runCLI(t, confEnv(srv), "conf", "status", root, "-o", "text")
	if code != exitOK || !strings.Contains(textOut, "S! 123") || !strings.Contains(textOut, "(canonical: "+filepath.Join(newDir, newSlug+".csf")+")") {
		t.Fatalf("status text exit=%d out=%q", code, textOut)
	}
	jsonOut, code := runCLI(t, confEnv(srv), "conf", "status", root)
	if code != exitOK {
		t.Fatalf("status JSON exit=%d out=%q", code, jsonOut)
	}
	var payload struct {
		Entries []struct {
			NonCanonical  bool   `json:"non_canonical"`
			CanonicalPath string `json:"canonical_path"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range payload.Entries {
		found = found || entry.NonCanonical && entry.CanonicalPath != ""
	}
	if len(payload.Entries) != 2 || !found {
		t.Fatalf("status JSON did not expose stale identity: %s", jsonOut)
	}
}
