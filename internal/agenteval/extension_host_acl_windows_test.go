//go:build windows

package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const extensionWindowsGuardHelper = "ATL_AGENT_EVAL_WINDOWS_GUARD_HELPER"

func TestPrivateExtensionWindowsRuntimeACLsAreProtected(t *testing.T) {
	root, rootGuard, err := makePrivateExtensionRuntimeRoot(t.TempDir(), extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = rootGuard.close()
		_ = os.RemoveAll(root)
	})
	if err := verifyPrivateWindowsPathForTest(root, true); err != nil {
		t.Fatalf("verify runtime root: %v", err)
	}
	directory := filepath.Join(root, "work")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatalf("make working directory: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	beforeOwner, err := extensionWindowsOwnerForTest(directory, true)
	if err != nil {
		t.Fatalf("working directory owner before preparation: %v", err)
	}
	beforeDiffers := !beforeOwner.Equals(user.User.Sid)
	if beforeDiffers {
		t.Log("working directory received a token-default owner distinct from the current user")
	}
	if err := preparePrivateExtensionRuntimeDirectory(directory); err != nil {
		t.Fatalf("prepare working directory: %v", err)
	}
	afterOwner, err := extensionWindowsOwnerForTest(directory, true)
	if err != nil {
		t.Fatalf("working directory owner after preparation: %v", err)
	}
	if !afterOwner.Equals(user.User.Sid) {
		t.Fatal("working directory preparation did not assign the current user as owner")
	}
	if beforeDiffers && afterOwner.Equals(beforeOwner) {
		t.Fatal("working directory retained its distinct token-default owner")
	}
	if err := verifyPrivateWindowsPathForTest(directory, true); err != nil {
		t.Fatalf("verify working directory: %v", err)
	}
}

func TestPrivateExtensionWindowsRootGuardBlocksDeleteUntilClose(t *testing.T) {
	base := t.TempDir()
	root, rootGuard, err := makePrivateExtensionRuntimeRoot(base, extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	if err := os.Remove(root); err == nil {
		_ = rootGuard.close()
		t.Fatal("runtime root delete succeeded while no-share-delete guard was held")
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatalf("make nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "content"), []byte("synthetic"), 0o600); err != nil {
		t.Fatalf("write nested content: %v", err)
	}
	if err := rootGuard.remove(root); err != nil {
		t.Fatalf("anchored runtime root removal: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("runtime root remains after anchored removal: %v", err)
	}
}

func TestPrivateExtensionWindowsRuntimeRootAcceptsTrailingBaseSeparator(t *testing.T) {
	base := t.TempDir() + string(filepath.Separator)
	root, rootGuard, err := makePrivateExtensionRuntimeRoot(base, extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make runtime root beneath trailing-separator base: %v", err)
	}
	if err := rootGuard.remove(root); err != nil {
		t.Fatalf("remove runtime root: %v", err)
	}
}

func TestPrivateExtensionWindowsExecutableGuardBlocksReplacementAndLaunchesAdmittedBytes(t *testing.T) {
	if os.Getenv(extensionWindowsGuardHelper) == "1" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			os.Exit(2)
		}
		digest := sha256.Sum256(data)
		_, _ = fmt.Fprintln(os.Stdout, hex.EncodeToString(digest[:]))
		os.Exit(0)
	}

	root, rootGuard, err := makePrivateExtensionRuntimeRoot(t.TempDir(), extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = rootGuard.close()
		_ = os.RemoveAll(root)
	})
	binDirectory := filepath.Join(root, "bin")
	if err := os.Mkdir(binDirectory, 0o700); err != nil {
		t.Fatalf("make bin directory: %v", err)
	}
	if err := preparePrivateExtensionRuntimeDirectory(binDirectory); err != nil {
		t.Fatalf("prepare bin directory: %v", err)
	}
	source, err := os.Executable()
	if err != nil {
		t.Fatalf("current executable: %v", err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read current executable: %v", err)
	}
	executable := filepath.Join(binDirectory, "component.exe")
	if err := os.WriteFile(executable, data, 0o500); err != nil {
		t.Fatalf("copy executable: %v", err)
	}
	digest := sha256.Sum256(data)
	wantDigest := hex.EncodeToString(digest[:])
	guard, err := preparePrivateExtensionRuntimeExecutable(executable, wantDigest)
	if err != nil {
		t.Fatalf("prepare executable: %v", err)
	}
	guardOpen := true
	t.Cleanup(func() {
		if guardOpen {
			_ = guard.close()
		}
	})

	if err := os.WriteFile(executable, []byte("replacement"), 0o500); err == nil {
		t.Fatal("write succeeded while executable launch guard was held")
	}
	if err := os.Remove(executable); err == nil {
		t.Fatal("delete succeeded while executable launch guard was held")
	}
	replacement := filepath.Join(binDirectory, "replacement.exe")
	if err := os.WriteFile(replacement, data, 0o500); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	from, _ := windows.UTF16PtrFromString(replacement)
	to, _ := windows.UTF16PtrFromString(executable)
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING); err == nil {
		t.Fatal("replacement rename succeeded while executable launch guard was held")
	}

	command := exec.Command(executable, "-test.run=^TestPrivateExtensionWindowsExecutableGuardBlocksReplacementAndLaunchesAdmittedBytes$")
	command.Env = append(os.Environ(), extensionWindowsGuardHelper+"=1")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stdout
	if err := command.Start(); err != nil {
		t.Fatalf("start guarded executable: %v", err)
	}
	if err := guard.close(); err != nil {
		t.Fatalf("close launch guard after Start: %v", err)
	}
	guardOpen = false
	if err := command.Wait(); err != nil {
		t.Fatalf("wait guarded executable: %v; output=%q", err, stdout.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != wantDigest {
		t.Fatalf("launched digest=%q, want %q", got, wantDigest)
	}
	if err := os.Remove(executable); err != nil {
		t.Fatalf("delete executable after launch guard close: %v", err)
	}
}

func TestPrivateExtensionWindowsRejectsPermissiveOrInheritedACL(t *testing.T) {
	root, rootGuard, err := makePrivateExtensionRuntimeRoot(t.TempDir(), extensionRuntimePrefix)
	if err != nil {
		t.Fatalf("make private runtime root: %v", err)
	}
	t.Cleanup(func() {
		_ = rootGuard.close()
		_ = os.RemoveAll(root)
	})
	directory := filepath.Join(root, "work")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("make working directory: %v", err)
	}
	if err := preparePrivateExtensionRuntimeDirectory(directory); err != nil {
		t.Fatalf("prepare working directory: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	sddl := "O:" + user.User.Sid.String() + "D:P" +
		"(A;OICI;FA;;;" + user.User.Sid.String() + ")" +
		"(A;OICIID;FA;;;WD)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("permissive descriptor: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("permissive DACL: %v", err)
	}
	handle, err := openExtensionWindowsPath(
		directory,
		extensionWindowsDirectoryAccess|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		true,
	)
	if err != nil {
		t.Fatalf("open working directory: %v", err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		_ = windows.CloseHandle(handle)
		t.Fatalf("set permissive DACL: %v", err)
	}
	if err := verifyExtensionWindowsHandle(handle, true); err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("permissive inherited ACE was admitted")
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatalf("close working directory: %v", err)
	}
}

func TestPrivateExtensionWindowsRejectsReparseDirectory(t *testing.T) {
	root, rootGuard, err := makePrivateExtensionRuntimeRoot(t.TempDir(), extensionRuntimePrefix)
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
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	if err := preparePrivateExtensionRuntimeDirectory(link); err == nil {
		t.Fatal("reparse working directory was admitted")
	}
}

func TestExtensionPlatformEnvironmentIgnoresAmbientWindowsDirectory(t *testing.T) {
	t.Setenv("SYSTEMROOT", `C:\spoofed-systemroot`)
	t.Setenv("WINDIR", `C:\spoofed-windir`)
	want, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatalf("Windows directory: %v", err)
	}
	values, err := extensionPlatformEnvironment()
	if err != nil {
		t.Fatalf("platform environment: %v", err)
	}
	if values["SYSTEMROOT"] != want || values["WINDIR"] != want {
		t.Fatalf("platform environment=%v, want OS directory %q", values, want)
	}
}

func verifyPrivateWindowsPathForTest(path string, directory bool) error {
	access := uint32(extensionWindowsExecutableAccess)
	share := uint32(windows.FILE_SHARE_READ)
	if directory {
		access = extensionWindowsDirectoryAccess
		share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	}
	handle, err := openExtensionWindowsPath(path, access, share, directory)
	if err != nil {
		return err
	}
	verifyErr := verifyExtensionWindowsHandle(handle, directory)
	closeErr := windows.CloseHandle(handle)
	return errors.Join(verifyErr, closeErr)
}

func extensionWindowsOwnerForTest(path string, directory bool) (*windows.SID, error) {
	access := uint32(extensionWindowsExecutableAccess)
	share := uint32(windows.FILE_SHARE_READ)
	if directory {
		access = extensionWindowsDirectoryAccess
		share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	}
	handle, err := openExtensionWindowsPath(path, access, share, directory)
	if err != nil {
		return nil, err
	}
	sd, readErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	var ownerCopy *windows.SID
	if readErr == nil && sd == nil {
		readErr = errExtensionInvalidExecutable
	}
	if readErr == nil {
		owner, _, ownerErr := sd.Owner()
		if ownerErr != nil || owner == nil {
			readErr = errors.Join(ownerErr, errExtensionInvalidExecutable)
		} else {
			ownerCopy, readErr = owner.Copy()
		}
	}
	closeErr := windows.CloseHandle(handle)
	return ownerCopy, errors.Join(readErr, closeErr)
}
