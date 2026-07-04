package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestCreateCLI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write page: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "manifest.json")

	out, code := runCLI(t, map[string]string{"JIRA_URL": "https://jira.example.com"},
		"manifest", "create", "--root", root, "--out", outPath, "--service", "jira", "--selector", "jql=project=PROJ", "--fields", "summary,status")
	if code != exitOK {
		t.Fatalf("manifest create: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Path     string `json:"path"`
		Manifest struct {
			Counts struct {
				Files int `json:"files"`
			} `json:"counts"`
			Backend []struct {
				URLHash string `json:"url_hash"`
			} `json:"backend"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if res.Path != outPath || res.Manifest.Counts.Files != 1 || len(res.Manifest.Backend) != 1 || !strings.HasPrefix(res.Manifest.Backend[0].URLHash, "sha256:") {
		t.Fatalf("result = %+v, want path/count/backend hash", res)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(data), "jira.example.com") {
		t.Fatalf("manifest leaked backend URL:\n%s", data)
	}
}
