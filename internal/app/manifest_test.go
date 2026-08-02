package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestCreateManifestWritesCountsAndHashesBackend(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}

	res, err := CreateManifest(ManifestOpts{
		Root:      root,
		Command:   "atl test",
		Service:   "jira",
		Selectors: []string{"jql=project=PROJ"},
		Fields:    []string{"summary,status"},
		Version:   "test",
		BackendURLs: map[string]string{
			"jira": "https://jira.example.com",
		},
	})
	if err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	if res.Manifest.Counts.Files != 2 || res.Manifest.Counts.Bytes == 0 || res.Manifest.Counts.Extensions[".md"] != 1 || res.Manifest.Counts.Extensions[".json"] != 1 {
		t.Fatalf("counts = %+v, want two files with extensions", res.Manifest.Counts)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(data), "jira.example.com") || !strings.Contains(string(data), `"url_hash": "sha256:`) {
		t.Fatalf("manifest backend identity not hashed:\n%s", data)
	}
	var decoded MirrorManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, data)
	}
	if decoded.Command != "atl test" || decoded.ATLVersion != "test" {
		t.Fatalf("decoded = %+v, want command/version", decoded)
	}
}

func TestCreateManifestCanonicalizesBackendIdentity(t *testing.T) {
	root := t.TempDir()
	first, err := CreateManifest(ManifestOpts{Root: root, Out: filepath.Join(root, "one.json"), Service: "jira", BackendURLs: map[string]string{"jira": "HTTPS://EXAMPLE.test:443/jira/"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateManifest(ManifestOpts{Root: root, Out: filepath.Join(root, "two.json"), Service: "jira", BackendURLs: map[string]string{"jira": "https://example.test/jira"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Manifest.Backend) != 1 || len(second.Manifest.Backend) != 1 || first.Manifest.Backend[0].URLHash != second.Manifest.Backend[0].URLHash {
		t.Fatalf("canonical backend hashes differ: %+v %+v", first.Manifest.Backend, second.Manifest.Backend)
	}
}

func TestCreateManifestRejectsAmbiguousBackendIdentity(t *testing.T) {
	root := t.TempDir()
	_, err := CreateManifest(ManifestOpts{Root: root, Out: filepath.Join(root, "manifest.json"), Service: "jira", BackendURLs: map[string]string{"jira": "https://example.test/jira?tenant=private"}})
	if !errors.Is(err, domain.ErrUsage) || strings.Contains(fmt.Sprint(err), "tenant") || strings.Contains(fmt.Sprint(err), "example.test") {
		t.Fatalf("error = %v", err)
	}
}
