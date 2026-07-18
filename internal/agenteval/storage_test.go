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
	marker := filepath.Join(root, privateOutputRootMarker)
	markerInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode=%v", markerInfo.Mode())
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != privateOutputRootMarkerContents {
		t.Fatalf("marker=%q err=%v", data, err)
	}
	if second, err := PreparePrivateOutputRoot(root, repository); err != nil || second != root {
		t.Fatalf("reopen root=%q err=%v", second, err)
	}
	if _, err := PreparePrivateOutputRoot(filepath.Join(repository, "tracked"), repository); err == nil {
		t.Fatal("non-ignored output root passed")
	}
}

func TestPreparePrivateOutputRootRejectsUnmarkedOrLooseExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission boundary")
	}
	repository := t.TempDir()
	if err := os.Chmod(repository, 0o700); err != nil {
		t.Fatal(err)
	}

	nonempty := filepath.Join(t.TempDir(), "nonempty")
	if err := os.Mkdir(nonempty, 0o700); err != nil {
		t.Fatal(err)
	}
	privateFile := filepath.Join(nonempty, "keep.txt")
	if err := os.WriteFile(privateFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePrivateOutputRoot(nonempty, repository); err == nil {
		t.Fatal("unmarked nonempty root passed")
	}
	if data, err := os.ReadFile(privateFile); err != nil || string(data) != "keep" {
		t.Fatalf("existing data changed: %q err=%v", data, err)
	}

	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePrivateOutputRoot(loose, repository); err == nil {
		t.Fatal("non-private empty root passed")
	}
	info, err := os.Stat(loose)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("loose root was chmodded: mode=%v err=%v", info.Mode(), err)
	}
}

func TestPreparePrivateOutputRootRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	target := t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "output-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePrivateOutputRoot(link, t.TempDir()); err == nil {
		t.Fatal("symlink output root passed")
	}
	if _, err := os.Stat(filepath.Join(target, privateOutputRootMarker)); !os.IsNotExist(err) {
		t.Fatalf("symlink target was initialized: %v", err)
	}
}

func TestPreparePrivateOutputRootRejectsInvalidMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission boundary")
	}
	for _, test := range []struct {
		name       string
		mode       os.FileMode
		contents   string
		useSymlink bool
	}{
		{name: "wrong contents", mode: 0o600, contents: "not-an-atl-root\n"},
		{name: "loose permissions", mode: 0o644, contents: privateOutputRootMarkerContents},
		{name: "symlink", useSymlink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private-root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, privateOutputRootMarker)
			if test.useSymlink {
				outside := filepath.Join(t.TempDir(), "outside-marker")
				if err := os.WriteFile(outside, []byte(privateOutputRootMarkerContents), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, marker); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(marker, []byte(test.contents), test.mode); err != nil {
				t.Fatal(err)
			}

			if _, err := PreparePrivateOutputRoot(root, t.TempDir()); err == nil {
				t.Fatal("invalid private-root marker passed")
			}
			if !test.useSymlink {
				info, err := os.Stat(marker)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != test.mode {
					t.Fatalf("marker was chmodded: mode=%v", info.Mode())
				}
			}
		})
	}
}

func TestPrivateDirectoryValidationNeverChangesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission boundary")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mkdirPrivate(directory); err == nil {
		t.Fatal("loose existing directory passed")
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("directory was chmodded: mode=%v err=%v", info.Mode(), err)
	}
}

func TestPrivateRunDirectoryRejectsDescendantSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareMarkedPrivateRoot(root); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	scenarioLink := filepath.Join(root, "jira.synthetic")
	if err := os.Symlink(outside, scenarioLink); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(scenarioLink, "codex", "baseline", "run-01")
	if err := mkdirPrivateWithin(root, target); err == nil {
		t.Fatal("private run directory followed a descendant symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "codex")); !os.IsNotExist(err) {
		t.Fatalf("outside directory was created: %v", err)
	}
}

func TestPrivateRunDirectoryRejectsTargetOutsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "escaped-run")
	if err := mkdirPrivateWithin(root, outside); err == nil {
		t.Fatal("private run directory escaped its root")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside directory was created: %v", err)
	}
}

func TestWritePrivateFileAtomicallyReplacesFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "artifact.json")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(target, []byte("private")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
		t.Fatalf("symlink destination changed: %q err=%v", data, err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode=%v err=%v", info.Mode(), err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "private" {
		t.Fatalf("target=%q err=%v", data, err)
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

func TestPrivateRuntimePathsAreLimitedToMarkedEphemeralWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := prepareMarkedPrivateRoot(root); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(root, ".ephemeral", "execution-run-11111111111111111111111111111111", "live-config")
	denied := filepath.Join(root, "runs", "run-11111111111111111111111111111111", "live-config")
	for _, path := range []string{allowed, denied} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !privateRuntimePathAllowed(root, allowed) {
		t.Fatal("marked ephemeral runtime path was rejected")
	}
	if privateRuntimePathAllowed(root, denied) {
		t.Fatal("non-ephemeral workspace path was accepted")
	}
	if err := os.Remove(filepath.Join(root, privateOutputRootMarker)); err != nil {
		t.Fatal(err)
	}
	if privateRuntimePathAllowed(root, allowed) {
		t.Fatal("unmarked workspace path was accepted")
	}
}
