package agenteval

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// errHardenedUnsafePrivatePath classifies a caller-selected private artifact
// path that fails containment, parent-stability, or owner-only mode validation.
var errHardenedUnsafePrivatePath = errors.New("unsafe private artifact path")

// hardenedFileLock is an OS advisory lock held by an open root-contained file.
// Closing a process releases it automatically, so a crash cannot leave a stale
// owner.
type hardenedFileLock struct {
	file   *os.File
	unlock func() error
}

// Unlock releases and closes the advisory lock.
func (l *hardenedFileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.unlock()
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

// hardenedMkdirAllWithin creates target beneath root using os.Root
// containment. The selected root itself may be a symlink (it is the caller's
// trust anchor), but a symlink in any descendant component cannot escape that
// root.
func hardenedMkdirAllWithin(root, target string, perm os.FileMode) error {
	if err := os.MkdirAll(root, perm); err != nil {
		return err
	}
	rootAbs, rootErr := filepath.Abs(root)
	targetAbs, targetErr := filepath.Abs(target)
	if rootErr == nil && targetErr == nil && rootAbs == targetAbs {
		return nil
	}
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, rel); err != nil {
		return err
	}
	return r.MkdirAll(rel, perm)
}

// hardenedReadFileWithinLimit reads a contained regular file while bounding
// allocation. It uses a held os.Root and reads one byte past max so callers can
// distinguish an exact-limit file from overflow.
func hardenedReadFileWithinLimit(root, target string, max int64) ([]byte, error) {
	if max < 0 {
		return nil, fmt.Errorf("invalid read limit %d", max)
	}
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("file exceeds %d-byte read limit", max)
	}
	return b, nil
}

// hardenedStatWithin returns metadata for a mirror-owned path without
// following any descendant symlink. It shares held-root containment with the
// evaluator’s other hardened operations.
func hardenedStatWithin(root, target string) (os.FileInfo, error) {
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, rel); err != nil {
		return nil, err
	}
	return r.Stat(rel)
}

// hardenedReadDirWithin lists a mirror-owned directory without following a
// descendant symlink at any component.
func hardenedReadDirWithin(root, target string) ([]os.DirEntry, error) {
	rootAbs, rootErr := filepath.Abs(root)
	targetAbs, targetErr := filepath.Abs(target)
	rel := "."
	var err error
	if rootErr != nil || targetErr != nil || rootAbs != targetAbs {
		rel, err = hardenedRelativeToRoot(root, target)
		if err != nil {
			return nil, err
		}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, rel); err != nil {
		return nil, err
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// hardenedRemoveWithin removes target through the same root-contained resolver
// used by writes. It never follows an escaping descendant symlink.
func hardenedRemoveWithin(root, target string) error {
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
		return err
	}
	return r.Remove(rel)
}

// hardenedRenameWithin atomically renames oldTarget to newTarget beneath one
// trust root, refusing descendant symlinks in either parent path.
func hardenedRenameWithin(root, oldTarget, newTarget string) error {
	oldRel, err := hardenedRelativeToRoot(root, oldTarget)
	if err != nil {
		return err
	}
	newRel, err := hardenedRelativeToRoot(root, newTarget)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, filepath.Dir(oldRel)); err != nil {
		return err
	}
	if err := hardenedRejectSymlinkComponents(r, filepath.Dir(newRel)); err != nil {
		return err
	}
	return r.Rename(oldRel, newRel)
}

// hardenedSyncDirectoryWithin fsyncs a directory beneath root after refusing
// symlink components. Callers use it to make already-fsynced file creation,
// rename, or removal directory entries durable before a destructive
// transaction advances.
func hardenedSyncDirectoryWithin(root, target string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%w: directory durability is unsupported on windows", errHardenedUnsafePrivatePath)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing path %q outside root %q", target, root)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, rel); err != nil {
		return err
	}
	directory, err := r.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: sync target is not a directory", errHardenedUnsafePrivatePath)
	}
	return directory.Sync()
}

// hardenedTryLockFileWithin non-blockingly acquires an exclusive advisory lock
// on a regular file beneath root. acquired=false is not an error: another
// process currently owns the lock.
func hardenedTryLockFileWithin(root, target string, perm os.FileMode) (lock *hardenedFileLock, acquired bool, err error) {
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return nil, false, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, rel); err != nil {
		return nil, false, err
	}
	f, err := r.OpenFile(rel, os.O_RDWR|os.O_CREATE, perm)
	if err != nil {
		return nil, false, err
	}
	unlock, acquired, err := hardenedTryAdvisoryLock(f)
	if err != nil || !acquired {
		_ = f.Close()
		return nil, acquired, err
	}
	return &hardenedFileLock{file: f, unlock: unlock}, true, nil
}

// hardenedWriteFileAtomicPrivate writes target through one held
// parent-directory handle after proving that the opened directory itself is
// owner-only. The caller is responsible for reserving a collision-safe
// basename/extension. Mode validation, temp creation, and rename all use the
// same os.Root, so a parent path swap cannot redirect the checked handle's
// later write.
func hardenedWriteFileAtomicPrivate(target string, data []byte, perm os.FileMode) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	parentAbs, base := filepath.Dir(targetAbs), filepath.Base(targetAbs)
	if base == "." || base == ".." || strings.ContainsAny(base, `/\\`) {
		return fmt.Errorf("%w: invalid artifact name %q", errHardenedUnsafePrivatePath, base)
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
	if !openedInfo.IsDir() || openedInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent must have mode 0700 or stricter", errHardenedUnsafePrivatePath)
	}
	tmp, tmpName, err := hardenedCreateRootTemp(r, perm)
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
	return r.Rename(tmpName, base)
}

// hardenedWriteFileWithin atomically replaces target beneath root. Root-scoped
// path resolution prevents descendant symlinks from escaping the trust anchor;
// atomic rename also replaces rather than follows a final-component symlink.
func hardenedWriteFileWithin(root, target string, data []byte, perm os.FileMode) error {
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
		return err
	}
	parent := r
	closeParent := false
	dir, base := filepath.Dir(rel), filepath.Base(rel)
	if dir != "." {
		parent, err = r.OpenRoot(dir)
		if err != nil {
			return err
		}
		closeParent = true
	}
	if closeParent {
		defer func() { _ = parent.Close() }()
	}
	tmp, tmpName, err := hardenedCreateRootTemp(parent, perm)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Remove(tmpName) }()
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
	return parent.Rename(tmpName, base)
}

// hardenedWriteFileExclusiveWithin creates target beneath root without
// replacing an existing path. It uses one held root/parent identity, refuses
// descendant symlinks, fsyncs a fresh temporary inode, then atomically links
// that inode at the final name. Readers therefore cannot observe a partial
// target.
func hardenedWriteFileExclusiveWithin(root, target string, data []byte, perm os.FileMode) error {
	rel, err := hardenedRelativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := hardenedRejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
		return err
	}
	parent := r
	closeParent := false
	dir, base := filepath.Dir(rel), filepath.Base(rel)
	if dir != "." {
		parent, err = r.OpenRoot(dir)
		if err != nil {
			return err
		}
		closeParent = true
	}
	if closeParent {
		defer func() { _ = parent.Close() }()
	}
	f, tempName, err := hardenedCreateRootTemp(parent, perm)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Remove(tempName) }()
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return parent.Link(tempName, base)
}

func hardenedRelativeToRoot(root, target string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing path %q outside root %q", target, root)
	}
	return rel, nil
}

func hardenedCreateRootTemp(root *os.Root, perm os.FileMode) (*os.File, string, error) {
	for range 100 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, "", err
		}
		name := ".tmp-" + hex.EncodeToString(suffix[:])
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("could not allocate temporary file")
}

// hardenedRejectSymlinkComponents rejects every existing component in rel.
// os.Root remains the race-safe containment boundary; this stricter check keeps
// evaluator scans and writes consistent by forbidding even in-root descendant
// aliases where the calling operation requires that policy.
func hardenedRejectSymlinkComponents(root *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	current := ""
	for _, component := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing descendant symlink %q", current)
		}
	}
	return nil
}
