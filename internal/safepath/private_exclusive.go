package safepath

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileExclusivePrivate creates target through one held parent-directory
// handle after proving that the opened directory is owner-only. It never
// replaces an existing final path; a regular file, directory, symlink, or
// reparse point at that name makes the operation fail.
func WriteFileExclusivePrivate(target string, data []byte, perm os.FileMode) error {
	return writeFileExclusivePrivate("", target, data, perm)
}

// WriteFileExclusivePrivateOutsideRoot has the same exclusive owner-private
// contract as WriteFileExclusivePrivate and additionally refuses a target
// whose resolved parent is the protected root or one of its descendants. The
// resolved parent identity is bound to the directory handle used for the final
// link, so a symlink alias or path swap cannot redirect the write into the
// protected tree after the overlap check.
func WriteFileExclusivePrivateOutsideRoot(protectedRoot, target string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(protectedRoot) == "" {
		return fmt.Errorf("%w: protected root is required", ErrUnsafePrivatePath)
	}
	return writeFileExclusivePrivate(protectedRoot, target, data, perm)
}

func writeFileExclusivePrivate(protectedRoot, target string, data []byte, perm os.FileMode) error {
	return writeFileExclusivePrivateWithHook(protectedRoot, target, data, perm, nil)
}

func writeFileExclusivePrivateWithHook(protectedRoot, target string, data []byte, perm os.FileMode, beforeParentOpen func() error) error {
	if perm.Perm()&0o077 != 0 {
		return fmt.Errorf("%w: artifact mode must be owner-only", ErrUnsafePrivatePath)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	parentAbs, base := filepath.Dir(targetAbs), filepath.Base(targetAbs)
	if base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
		return fmt.Errorf("%w: invalid artifact name %q", ErrUnsafePrivatePath, base)
	}
	var resolvedParentInfo os.FileInfo
	if protectedRoot != "" {
		protectedAbs, protectedErr := filepath.Abs(protectedRoot)
		resolvedProtected, resolveProtectedErr := filepath.EvalSymlinks(protectedAbs)
		resolvedParent, resolveParentErr := filepath.EvalSymlinks(parentAbs)
		if protectedErr != nil || resolveProtectedErr != nil || resolveParentErr != nil {
			return fmt.Errorf("%w: protected path could not be resolved", ErrUnsafePrivatePath)
		}
		resolvedParentInfo, err = os.Lstat(resolvedParent)
		if err != nil || !resolvedParentInfo.IsDir() {
			return fmt.Errorf("%w: artifact parent is not a stable directory", ErrUnsafePrivatePath)
		}
		resolvedTarget := filepath.Join(resolvedParent, base)
		if Within(resolvedProtected, resolvedTarget) {
			return fmt.Errorf("%w: artifact target overlaps protected root", ErrUnsafePrivatePath)
		}
	}
	if beforeParentOpen != nil {
		if err := beforeParentOpen(); err != nil {
			return err
		}
	}
	r, err := os.OpenRoot(parentAbs)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	openedInfo, err := r.Stat(".")
	if err != nil {
		return err
	}
	if resolvedParentInfo != nil && !os.SameFile(resolvedParentInfo, openedInfo) {
		return fmt.Errorf("%w: artifact parent changed during validation", ErrUnsafePrivatePath)
	}
	if !openedInfo.IsDir() || openedInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent must have mode 0700 or stricter", ErrUnsafePrivatePath)
	}
	tmp, tmpName, err := createRootTemp(r, perm)
	if err != nil {
		return err
	}
	defer func() { _ = r.Remove(tmpName) }()
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return r.Link(tmpName, base)
}
