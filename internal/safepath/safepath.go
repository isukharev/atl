// Package safepath sanitizes externally-controlled strings that are used as
// filesystem path components and asserts that a computed path stays within a
// root directory. Page ids, issue keys, space keys and attachment filenames all
// originate from the Confluence/Jira server, so a hostile or compromised
// backend (or anyone who can edit a page or attach a file) must not be able to
// make `atl` write outside the mirror tree.
package safepath

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

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
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
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
	return os.Rename(tmpName, path)
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
