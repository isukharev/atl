package agenteval

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreparePrivateOutputRootRequiresIgnoreInsideRepository(t *testing.T) {
	repository := t.TempDir()
	if err := exec.Command("git", "-C", repository, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := PreparePrivateOutputRoot(filepath.Join(repository, "private", "runs"), repository)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%v", info.Mode())
	}
	if _, err := PreparePrivateOutputRoot(filepath.Join(repository, "tracked"), repository); err == nil {
		t.Fatal("non-ignored output root passed")
	}
}

func TestPrivateWorkspaceRejectsProviderControlFiles(t *testing.T) {
	for _, relative := range []string{"AGENTS.md", "CLAUDE.md", ".mcp.json", ".agents/skills/custom/SKILL.md", ".claude/settings.json", ".codex/config.toml"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("unreviewed provider control"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validatePrivateWorkspaceTemplate(root); err == nil || !strings.Contains(err.Error(), "provider control") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.md"), []byte("reviewed evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateWorkspaceTemplate(root); err != nil {
		t.Fatal(err)
	}
}

func TestCopyWorkspaceRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	source := t.TempDir()
	if err := os.Symlink("outside", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := copyWorkspace(source, filepath.Join(t.TempDir(), "copy")); err == nil {
		t.Fatal("symlink workspace passed")
	}
}

func TestPrivateLiveInputsRequireIgnoredSpecAndExternalOwnerOnlyConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission boundary")
	}
	repository := t.TempDir()
	if err := exec.Command("git", "-C", repository, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trackedDir := filepath.Join(repository, "tracked")
	privateDir := filepath.Join(repository, "private")
	if err := os.MkdirAll(trackedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	trackedSpec := filepath.Join(trackedDir, "run.json")
	privateSpec := filepath.Join(privateDir, "run.json")
	if err := os.WriteFile(trackedSpec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateSpec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if err := os.WriteFile(filepath.Join(config, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := requirePrivateLiveInputs(privateSpec, config, repository); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateLiveInputs(trackedSpec, config, repository); err == nil {
		t.Fatal("tracked private-live spec passed")
	}
	repositoryAlias := filepath.Join(t.TempDir(), "repository-alias")
	if err := os.Symlink(repository, repositoryAlias); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateLiveInputs(trackedSpec, config, repositoryAlias); err == nil {
		t.Fatal("tracked private-live spec passed through a repository symlink")
	}
	if err := requirePrivateLiveInputs(privateSpec, privateDir, repository); err == nil {
		t.Fatal("repository-contained live config passed")
	}
	if err := os.Chmod(filepath.Join(config, "credentials.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateLiveInputs(privateSpec, config, repository); err == nil {
		t.Fatal("world-readable credentials passed")
	}
}
