//go:build !windows

package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const extensionUnixTrustedTemporaryBase = "/tmp"

type extensionRuntimeRootGuard struct {
	base *os.File
	root *os.File
}

type extensionExecutableLaunchGuard struct {
	file *os.File
}

// makePrivateExtensionRuntimeRoot deliberately ignores an ambient temporary
// base. It creates beneath the canonical root-owned sticky system temporary
// directory and verifies that directory's identity across creation.
func makePrivateExtensionRuntimeRoot(_ string, prefix string) (string, *extensionRuntimeRootGuard, error) {
	if !validExtensionRuntimePrefix(prefix) {
		return "", nil, errExtensionInvalidExecutable
	}
	base, before, err := trustedExtensionUnixTemporaryBase()
	if err != nil {
		return "", nil, errExtensionInvalidExecutable
	}
	baseFile, err := os.Open(base)
	if err != nil {
		return "", nil, errExtensionInvalidExecutable
	}
	guard := &extensionRuntimeRootGuard{base: baseFile}
	fail := func(root string) (string, *extensionRuntimeRootGuard, error) {
		var cleanupErr error
		if root != "" && guard.root != nil {
			cleanupErr = guard.remove(root)
		} else {
			cleanupErr = errors.Join(guard.close(), removeExtensionUnixEmptyRuntimeRoot(root))
		}
		if cleanupErr != nil {
			return "", nil, errExtensionAdmissionCleanup
		}
		return "", nil, errExtensionInvalidExecutable
	}
	openedBase, err := baseFile.Stat()
	if err != nil || !os.SameFile(before, openedBase) {
		return fail("")
	}
	root, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return fail("")
	}
	after, err := os.Stat(base)
	if err != nil || !os.SameFile(before, after) {
		return fail(root)
	}
	if err := os.Chmod(root, 0o700); err != nil || verifyExtensionUnixPath(root, true, 0o700) != nil {
		return fail(root)
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return fail(root)
	}
	guard.root = rootFile
	opened, err := rootFile.Stat()
	current, currentErr := os.Lstat(root)
	if err != nil || currentErr != nil || !os.SameFile(opened, current) || verifyExtensionUnixPath(root, true, 0o700) != nil {
		return fail(root)
	}
	return root, guard, nil
}

func removeExtensionUnixEmptyRuntimeRoot(path string) error {
	if path == "" {
		return nil
	}
	if !validExtensionUnixAbsolutePath(path) || os.Remove(path) != nil {
		return errExtensionAdmissionCleanup
	}
	return nil
}

func preparePrivateExtensionRuntimeDirectory(path string) error {
	file, err := openExtensionUnixPath(path, true)
	if err != nil {
		return errExtensionInvalidExecutable
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.IsDir() || !extensionUnixOwnedByCurrentUser(opened) || file.Chmod(0o700) != nil {
		_ = file.Close()
		return errExtensionInvalidExecutable
	}
	current, lstatErr := os.Lstat(path)
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if lstatErr != nil || afterErr != nil || closeErr != nil || !os.SameFile(opened, current) ||
		!os.SameFile(opened, after) || after.Mode().Perm() != 0o700 {
		return errExtensionInvalidExecutable
	}
	return nil
}

func preparePrivateExtensionRuntimeExecutable(path, expectedSHA256 string) (*extensionExecutableLaunchGuard, error) {
	if !validSHA256(expectedSHA256) {
		return nil, errExtensionInvalidExecutable
	}
	file, err := openExtensionUnixPath(path, false)
	if err != nil {
		return nil, errExtensionInvalidExecutable
	}
	guard := &extensionExecutableLaunchGuard{file: file}
	fail := func() (*extensionExecutableLaunchGuard, error) {
		_ = guard.close()
		return nil, errExtensionInvalidExecutable
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !extensionUnixOwnedByCurrentUser(opened) ||
		opened.Size() < 1 || opened.Size() > extensionExecutableMaxBytes || file.Chmod(0o500) != nil {
		return fail()
	}
	current, lstatErr := os.Lstat(path)
	after, afterErr := file.Stat()
	if lstatErr != nil || afterErr != nil || !os.SameFile(opened, current) || !os.SameFile(opened, after) || after.Mode().Perm() != 0o500 {
		return fail()
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, extensionExecutableMaxBytes+1))
	if err != nil || written != opened.Size() || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail()
	}
	return guard, nil
}

func openExtensionUnixPath(path string, directory bool) (*os.File, error) {
	if !validExtensionUnixAbsolutePath(path) {
		return nil, errExtensionInvalidExecutable
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, errExtensionInvalidExecutable
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errExtensionInvalidExecutable
	}
	return file, nil
}

func (g *extensionRuntimeRootGuard) close() error {
	if g == nil {
		return nil
	}
	var errs []error
	if g.root != nil {
		errs = append(errs, g.root.Close())
		g.root = nil
	}
	if g.base != nil {
		errs = append(errs, g.base.Close())
		g.base = nil
	}
	return errors.Join(errs...)
}

func (g *extensionRuntimeRootGuard) remove(rootPath string) error {
	if g == nil || g.root == nil || g.base == nil || !validExtensionUnixAbsolutePath(rootPath) {
		return errExtensionAdmissionCleanup
	}
	opened, statErr := g.root.Stat()
	current, lstatErr := os.Lstat(rootPath)
	if statErr != nil || lstatErr != nil || !os.SameFile(opened, current) {
		_ = g.close()
		return errExtensionAdmissionCleanup
	}
	closeErr := g.close()
	removeErr := os.RemoveAll(rootPath)
	if closeErr != nil || removeErr != nil {
		return errExtensionAdmissionCleanup
	}
	return nil
}

func (g *extensionExecutableLaunchGuard) close() error {
	if g == nil || g.file == nil {
		return nil
	}
	err := g.file.Close()
	g.file = nil
	return err
}

func extensionPlatformEnvironment() (map[string]string, error) {
	return map[string]string{}, nil
}

func trustedExtensionUnixTemporaryBase() (string, os.FileInfo, error) {
	base, err := filepath.EvalSymlinks(extensionUnixTrustedTemporaryBase)
	if err != nil || !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return "", nil, errExtensionInvalidExecutable
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSticky == 0 || !extensionUnixOwnedByRoot(info) {
		return "", nil, errExtensionInvalidExecutable
	}
	return base, info, nil
}

func verifyExtensionUnixPath(path string, directory bool, permissions os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || info.Mode().Perm() != permissions || !extensionUnixOwnedByCurrentUser(info) {
		return errExtensionInvalidExecutable
	}
	return nil
}

func extensionUnixOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}

func extensionUnixOwnedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func validExtensionUnixAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && strings.IndexByte(path, 0) < 0
}

func validExtensionRuntimePrefix(prefix string) bool {
	return prefix != "" && len(prefix) <= 128 && filepath.Base(prefix) == prefix &&
		!strings.ContainsAny(prefix, `/\\`) && strings.IndexByte(prefix, 0) < 0
}
