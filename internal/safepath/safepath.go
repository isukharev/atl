// Package safepath sanitizes externally-controlled strings that are used as
// filesystem path components and asserts that a computed path stays within a
// root directory. Page ids, issue keys, space keys and attachment filenames all
// originate from the Confluence/Jira server, so a hostile or compromised
// backend (or anyone who can edit a page or attach a file) must not be able to
// make `atl` write outside the mirror tree.
package safepath

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// ErrUnsafePrivatePath classifies a caller-selected private artifact path that
// fails containment, parent-stability, or owner-only mode validation.
var ErrUnsafePrivatePath = errors.New("unsafe private artifact path")

// FileLock is an OS advisory lock held by an open root-contained file. Closing
// a process releases it automatically, so a crash cannot leave a stale owner.
type FileLock struct {
	file   *os.File
	unlock func() error
}

// Unlock releases and closes the advisory lock.
func (l *FileLock) Unlock() error {
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

// Segment reduces s to a single safe path component. Path separators, drive
// colons, NUL and other control characters become '-'; a value that would be
// empty, "." or ".." (which could traverse upward) becomes "_"; and a leading
// dot is escaped so a segment can never collide with an internal state
// directory such as ".atl". The result never contains a separator and is never
// a traversal token.
func Segment(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == 0:
			return '-'
		case r < 0x20 || r == 0x7f: // other control characters
			return '-'
		default:
			return r
		}
	}, s)
	mapped = strings.TrimSpace(mapped)
	if mapped == "" || mapped == "." || mapped == ".." {
		return "_"
	}
	if strings.HasPrefix(mapped, ".") {
		mapped = "_" + mapped
	}
	return mapped
}

// Base reduces a possibly path-ful, externally supplied filename to a single
// safe base name. ok is false when the name has no usable basename (empty, "."
// or ".."), so callers can skip/reject rather than write to a surprising path.
func Base(name string) (string, bool) {
	b := filepath.Base(filepath.Clean(name))
	if b == "" || b == "." || b == ".." || b == string(filepath.Separator) {
		return "", false
	}
	return Segment(b), true
}

// WriteFile writes data to path like os.WriteFile but opens the final path
// component with O_NOFOLLOW, so a symlink planted at that exact path cannot
// redirect the write. This guards only the final component, not an intermediate
// directory symlink; `atl` never creates symlinks under the mirror, so the
// residual risk is a pre-existing on-disk symlink, never server-controlled data.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// WriteFileAtomic writes data to a fresh temp file in the same directory (mode
// perm, created O_EXCL so it never follows a symlink), fsyncs it, then renames
// it over path. The rename is atomic and replaces the destination's inode, so a
// concurrent reader never sees a half-written file, a pre-planted symlink at
// path is replaced rather than followed, and a pre-existing file with looser
// permissions is superseded by one created at perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	_, err := WriteReaderAtomic(path, bytes.NewReader(data), perm)
	return err
}

// WriteFileAtomicPrivate writes target through one held parent-directory
// handle after proving that the opened directory itself is owner-only. The
// caller is responsible for reserving a collision-safe basename/extension.
// Mode validation, temp creation, and rename all use the same os.Root, so a
// parent path swap cannot redirect the checked handle's later write.
func WriteFileAtomicPrivate(target string, data []byte, perm os.FileMode) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	parentAbs, base := filepath.Dir(targetAbs), filepath.Base(targetAbs)
	if base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
		return fmt.Errorf("%w: invalid artifact name %q", ErrUnsafePrivatePath, base)
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
		return fmt.Errorf("%w: parent must have mode 0700 or stricter", ErrUnsafePrivatePath)
	}
	tmp, tmpName, err := createRootTemp(r, perm)
	if err != nil {
		return err
	}
	defer func() { _ = r.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
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

// MkdirAllWithin creates target beneath root using os.Root containment. The
// selected root itself may be a symlink (it is the caller's trust anchor), but
// a symlink in any descendant component cannot escape that root.
func MkdirAllWithin(root, target string, perm os.FileMode) error {
	if err := os.MkdirAll(root, perm); err != nil {
		return err
	}
	rootAbs, rootErr := filepath.Abs(root)
	targetAbs, targetErr := filepath.Abs(target)
	if rootErr == nil && targetErr == nil && rootAbs == targetAbs {
		return nil
	}
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, rel); err != nil {
		return err
	}
	return r.MkdirAll(rel, perm)
}

// WriteFileWithin atomically replaces target beneath root. Root-scoped path
// resolution prevents descendant symlinks from escaping the trust anchor;
// atomic rename also replaces rather than follows a final-component symlink.
func WriteFileWithin(root, target string, data []byte, perm os.FileMode) error {
	_, err := WriteReaderAtomicWithin(root, target, bytes.NewReader(data), perm)
	return err
}

// WriteFileExclusiveWithin creates target beneath root without replacing an
// existing path. It uses one held root/parent identity, refuses descendant
// symlinks, fsyncs a fresh temporary inode, then atomically links that inode at
// the final name. Readers therefore cannot observe a partial target.
func WriteFileExclusiveWithin(root, target string, data []byte, perm os.FileMode) error {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
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
	f, tempName, err := createRootTemp(parent, perm)
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

// RenameWithin atomically renames oldTarget to newTarget beneath one trust
// root, refusing descendant symlinks in either parent path.
func RenameWithin(root, oldTarget, newTarget string) error {
	oldRel, err := relativeToRoot(root, oldTarget)
	if err != nil {
		return err
	}
	newRel, err := relativeToRoot(root, newTarget)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, filepath.Dir(oldRel)); err != nil {
		return err
	}
	if err := rejectSymlinkComponents(r, filepath.Dir(newRel)); err != nil {
		return err
	}
	return r.Rename(oldRel, newRel)
}

// TryLockFileWithin non-blockingly acquires an exclusive advisory lock on a
// regular file beneath root. acquired=false is not an error: another process
// currently owns the lock.
func TryLockFileWithin(root, target string, perm os.FileMode) (lock *FileLock, acquired bool, err error) {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return nil, false, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, rel); err != nil {
		return nil, false, err
	}
	f, err := r.OpenFile(rel, os.O_RDWR|os.O_CREATE, perm)
	if err != nil {
		return nil, false, err
	}
	unlock, acquired, err := tryAdvisoryLock(f)
	if err != nil || !acquired {
		_ = f.Close()
		return nil, acquired, err
	}
	return &FileLock{file: f, unlock: unlock}, true, nil
}

// ReadFileWithin reads a regular mirror-owned file without following any
// descendant symlink, including at the final component.
func ReadFileWithin(root, target string) ([]byte, error) {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, rel); err != nil {
		return nil, err
	}
	return r.ReadFile(rel)
}

// ReadFileWithinLimit reads a contained regular file while bounding allocation.
// It uses the same held os.Root containment as ReadFileWithin and reads one
// byte past max so callers can distinguish an exact-limit file from overflow.
func ReadFileWithinLimit(root, target string, max int64) ([]byte, error) {
	if max < 0 {
		return nil, fmt.Errorf("invalid read limit %d", max)
	}
	rel, err := relativeToRoot(root, target)
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

// StatWithin returns metadata for a mirror-owned path without following any
// descendant symlink. It shares the same held-root containment as ReadFileWithin
// so callers can preserve file modes without reopening a path through the
// ambient filesystem namespace.
func StatWithin(root, target string) (os.FileInfo, error) {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, rel); err != nil {
		return nil, err
	}
	return r.Stat(rel)
}

// ReadDirWithin lists a mirror-owned directory without following a descendant
// symlink at any component.
func ReadDirWithin(root, target string) ([]os.DirEntry, error) {
	rootAbs, rootErr := filepath.Abs(root)
	targetAbs, targetErr := filepath.Abs(target)
	rel := "."
	var err error
	if rootErr != nil || targetErr != nil || rootAbs != targetAbs {
		rel, err = relativeToRoot(root, target)
		if err != nil {
			return nil, err
		}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, rel); err != nil {
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

// RemoveWithin removes target through the same root-contained resolver used by
// writes. It never follows an escaping descendant symlink.
func RemoveWithin(root, target string) error {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
		return err
	}
	return r.Remove(rel)
}

// WriteReaderAtomicWithin is the root-contained counterpart of
// WriteReaderAtomic. The destination parent must already exist beneath root.
func WriteReaderAtomicWithin(root, target string, reader io.Reader, perm os.FileMode) (int64, error) {
	rel, err := relativeToRoot(root, target)
	if err != nil {
		return 0, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = r.Close() }()
	if err := rejectSymlinkComponents(r, filepath.Dir(rel)); err != nil {
		return 0, err
	}
	parent := r
	closeParent := false
	dir, base := filepath.Dir(rel), filepath.Base(rel)
	if dir != "." {
		parent, err = r.OpenRoot(dir)
		if err != nil {
			return 0, err
		}
		closeParent = true
	}
	if closeParent {
		defer func() { _ = parent.Close() }()
	}
	tmp, tmpName, err := createRootTemp(parent, perm)
	if err != nil {
		return 0, err
	}
	defer func() { _ = parent.Remove(tmpName) }()
	n, err := io.Copy(tmp, reader)
	if err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	return n, parent.Rename(tmpName, base)
}

func relativeToRoot(root, target string) (string, error) {
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

func createRootTemp(root *os.Root, perm os.FileMode) (*os.File, string, error) {
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

// rejectSymlinkComponents rejects every existing component in rel. os.Root is
// still the race-safe containment boundary; this stricter check keeps mirror
// scans and writes consistent by forbidding even in-root descendant aliases.
func rejectSymlinkComponents(root *os.Root, rel string) error {
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

// WriteReaderAtomic streams r into a fresh temp file in the same directory,
// then commits it exactly like WriteFileAtomic (chmod, fsync, atomic rename).
// On any failure — including a read error from r mid-stream — the temp file is
// removed and path is left untouched, so an interrupted download can never
// leave a truncated file at the destination. Returns the bytes written.
func WriteReaderAtomic(path string, r io.Reader, perm os.FileMode) (int64, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	n, err := io.Copy(tmp, r)
	if err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	return n, os.Rename(tmpName, path)
}

// Within reports whether target (after cleaning) is root itself or lies inside
// root. Use it as a containment assertion after filepath.Join, so even an
// unforeseen sanitizer gap cannot escape the tree.
func Within(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
