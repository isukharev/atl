package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectIsPathAndValueFree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	secretHost := "private-host.example"
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"jira_url":"https://`+secretHost+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Inspect()
	if got.Status != "valid" || got.JiraURLSource != "config_file" {
		t.Fatalf("Inspect() = %+v", got)
	}
	if !got.File.OwnerOnly || !got.File.PermissionKnown {
		t.Fatalf("file permissions = %+v", got.File)
	}
	if text := strings.Join([]string{got.DirectorySource, got.Status, got.Reason, got.JiraURLSource}, " "); strings.Contains(text, dir) || strings.Contains(text, secretHost) {
		t.Fatalf("serialized inspection fields leaked private data: %q", text)
	}
}

func TestInspectMalformedFileStillKeepsEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	t.Setenv("ATL_JIRA_URL", "https://configured.example")
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	got := Inspect()
	if got.Status != "invalid" || got.Reason != "invalid_configuration" {
		t.Fatalf("Inspect() = %+v", got)
	}
	if got.JiraURLSource != "environment" || got.Effective.JiraURL == "" {
		t.Fatalf("environment overlay was lost: %+v", got)
	}
	if got.File.OwnerOnly {
		t.Fatal("world-readable config reported owner-only")
	}
}

func TestInspectMissingConfigIsQualified(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	got := Inspect()
	if got.Status != "missing" || got.File.Status != "missing" || got.File.Present {
		t.Fatalf("Inspect() = %+v", got)
	}
}

func TestOwnerOnlyPermissionIsUnknownOnWindows(t *testing.T) {
	if ownerOnly, known := ownerOnlyPermission(0o666, "windows"); ownerOnly || known {
		t.Fatalf("Windows permission = owner_only:%t known:%t, want false/false", ownerOnly, known)
	}
	if ownerOnly, known := ownerOnlyPermission(0o600, "linux"); !ownerOnly || !known {
		t.Fatalf("POSIX permission = owner_only:%t known:%t, want true/true", ownerOnly, known)
	}
}
