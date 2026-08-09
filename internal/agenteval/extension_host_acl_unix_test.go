//go:build !windows

package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateExtensionRuntimeRootUsesTrustedSystemTemporaryDirectory(t *testing.T) {
	spoofed := t.TempDir()
	t.Setenv("TMPDIR", spoofed)
	root, guard, err := makePrivateExtensionRuntimeRoot(spoofed, extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = guard.close()
		_ = os.RemoveAll(root)
	})
	trustedBase, _, err := trustedExtensionUnixTemporaryBase()
	if err != nil {
		t.Fatalf("trusted temporary base: %v", err)
	}
	if filepath.Dir(root) != trustedBase {
		t.Fatalf("runtime root parent=%q, want trusted base %q", filepath.Dir(root), trustedBase)
	}
	if filepath.Dir(root) == spoofed {
		t.Fatalf("runtime root trusted ambient TMPDIR %q", spoofed)
	}
	if err := verifyExtensionUnixPath(root, true, 0o700); err != nil {
		t.Fatalf("verify runtime root: %v", err)
	}
}

func TestPrivateExtensionRuntimePathsAreOwnerOnly(t *testing.T) {
	root, rootGuard, err := makePrivateExtensionRuntimeRoot("", extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = rootGuard.close()
		_ = os.RemoveAll(root)
	})
	directory := filepath.Join(root, "work")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatalf("make working directory: %v", err)
	}
	if err := preparePrivateExtensionRuntimeDirectory(directory); err != nil {
		t.Fatalf("prepare working directory: %v", err)
	}
	if err := verifyExtensionUnixPath(directory, true, 0o700); err != nil {
		t.Fatalf("verify working directory: %v", err)
	}

	executable := filepath.Join(root, "component")
	payload := []byte("synthetic executable bytes")
	if err := os.WriteFile(executable, payload, 0o777); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	digest := sha256.Sum256(payload)
	guard, err := preparePrivateExtensionRuntimeExecutable(executable, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("prepare executable: %v", err)
	}
	if err := guard.close(); err != nil {
		t.Fatalf("close executable guard: %v", err)
	}
	if err := verifyExtensionUnixPath(executable, false, 0o500); err != nil {
		t.Fatalf("verify executable: %v", err)
	}
}

func TestPrivateExtensionRuntimeRejectsSymlinks(t *testing.T) {
	root, rootGuard, err := makePrivateExtensionRuntimeRoot("", extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = rootGuard.close()
		_ = os.RemoveAll(root)
	})
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("make target: %v", err)
	}
	if err := os.Chmod(target, 0o750); err != nil {
		t.Fatalf("set target mode: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("make symlink: %v", err)
	}
	if err := preparePrivateExtensionRuntimeDirectory(link); err == nil {
		t.Fatal("symlink working directory was admitted")
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("rejected directory symlink changed target mode: info=%v err=%v", info, err)
	}

	executableTarget := filepath.Join(root, "executable-target")
	payload := []byte("synthetic executable bytes")
	if err := os.WriteFile(executableTarget, payload, 0o600); err != nil {
		t.Fatalf("write executable target: %v", err)
	}
	executableLink := filepath.Join(root, "executable-link")
	if err := os.Symlink(executableTarget, executableLink); err != nil {
		t.Fatalf("make executable symlink: %v", err)
	}
	digest := sha256.Sum256(payload)
	if _, err := preparePrivateExtensionRuntimeExecutable(executableLink, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("symlink executable was admitted")
	}
	if info, err := os.Stat(executableTarget); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("rejected executable symlink changed target mode: info=%v err=%v", info, err)
	}
}

func TestExtensionPlatformEnvironmentIsEmptyOnUnix(t *testing.T) {
	values, err := extensionPlatformEnvironment()
	if err != nil {
		t.Fatalf("platform environment: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("platform environment=%v, want empty", values)
	}
}
