package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectNeverReturnsCredentialValueOrPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	secret := "not-for-output"
	body, err := json.Marshal(map[string]string{"jira": secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got := Inspect()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), dir) {
		t.Fatalf("inspection leaked credential material: %s", encoded)
	}
	if !got.Jira.Present || got.Jira.Source != "credentials_file" || !got.Store.OwnerOnly {
		t.Fatalf("Inspect() = %+v", got)
	}
}

func TestInspectEnvironmentCredentialSurvivesMalformedStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	t.Setenv("ATL_CONFLUENCE_PAT", "present")
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Inspect()
	if got.Store.Status != "invalid" || got.Store.OwnerOnly {
		t.Fatalf("store = %+v", got.Store)
	}
	if !got.Confluence.Present || got.Confluence.Source != "environment" || got.Confluence.Status != "available" {
		t.Fatalf("confluence = %+v", got.Confluence)
	}
	if got.Jira.Status != "credentials_unavailable" {
		t.Fatalf("jira = %+v", got.Jira)
	}
}

func TestInspectMissingCredentialsIsQualified(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	for _, key := range append(envKeysFor(Confluence), envKeysFor(Jira)...) {
		t.Setenv(key, "")
	}
	got := Inspect()
	if got.Store.Status != "missing" || got.Confluence.Status != "missing" || got.Jira.Status != "missing" {
		t.Fatalf("Inspect() = %+v", got)
	}
}
