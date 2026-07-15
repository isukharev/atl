package agenteval

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
