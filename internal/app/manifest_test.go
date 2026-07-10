package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
