package safepath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirHasTempLeak reports whether dir contains any leftover temp file matching
// the prefixes WriteFileAtomic uses for its in-progress writes.
func dirHasTempLeak(t *testing.T, dir string) bool {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			return true
		}
	}
	return false
}

func TestWriteFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	const perm = os.FileMode(0o600)

	if err := WriteFile(path, []byte("hello world"), perm); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q, want %q", got, "hello world")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 0600 is umask-safe to assert: umask only clears bits, and these are the
	// owner-only bits a default umask (022/002) never touches.
	if fi.Mode().Perm() != perm {
		t.Errorf("mode = %o, want %o", fi.Mode().Perm(), perm)
	}

	// Re-writing shorter content over a longer file must truncate (O_TRUNC).
	if err := WriteFile(path, []byte("hi"), perm); err != nil {
		t.Fatalf("WriteFile (truncate): %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("after truncate content = %q, want %q", got, "hi")
	}
}

// TestWriteFileRefusesSymlink is the core O_NOFOLLOW guarantee: a symlink
// planted at the target path must not be followed; the write fails and the link
// target stays untouched.
func TestWriteFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	err := WriteFile(link, []byte("ATTACK"), 0o600)
	if err == nil {
		t.Fatal("WriteFile through a symlink succeeded; O_NOFOLLOW not enforced")
	}

	// The link target must be untouched — the write must not have gone through.
	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("symlink target was overwritten: %q (write followed the symlink)", got)
	}
}

func TestWriteFileMissingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope", "f.txt") // parent "nope" does not exist
	if err := WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Error("WriteFile into a non-existent directory should fail")
	}
}

func TestWriteFileAtomicHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	const perm = os.FileMode(0o600)

	if err := WriteFileAtomic(path, []byte("atomic content"), perm); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "atomic content" {
		t.Errorf("content = %q, want %q", got, "atomic content")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != perm {
		t.Errorf("mode = %o, want %o", fi.Mode().Perm(), perm)
	}
	// No leftover temp file may remain after a successful atomic write.
	if dirHasTempLeak(t, dir) {
		t.Error("a .tmp-* file leaked after a successful WriteFileAtomic")
	}
}

// TestWriteFileAtomicReplacesSymlink: a symlink planted at path is replaced by a
// fresh regular file rather than followed, and the original link target is
// untouched.
func TestWriteFileAtomicReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "target.txt")
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("NEW"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic over a symlink: %v", err)
	}

	// path must now be a regular file (the symlink was replaced, not followed).
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("path is still a symlink after atomic write; the link was followed/kept")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Errorf("path content = %q, want NEW", got)
	}
	// The original link target must be untouched.
	vgot, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(vgot) != "ORIGINAL" {
		t.Errorf("symlink target was overwritten: %q", vgot)
	}
	if dirHasTempLeak(t, dir) {
		t.Error("a .tmp-* file leaked after WriteFileAtomic over a symlink")
	}
}

// TestWriteFileAtomicSupersedesLooserPerms: an existing 0666 file is replaced by
// a file created at 0600 (perms are taken from the new write, not inherited).
func TestWriteFileAtomicSupersedesLooserPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("OLD"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil { // defeat umask on the pre-existing file
		t.Fatal(err)
	}

	if err := WriteFileAtomic(path, []byte("NEW"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("final mode = %o, want 0600 (looser perms must not be inherited)", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Errorf("content = %q, want NEW", got)
	}
}

func TestWriteFileAtomicMissingDir(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope") // does not exist → CreateTemp fails
	path := filepath.Join(missing, "f.txt")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); err == nil {
		t.Error("WriteFileAtomic into a non-existent directory should fail")
	}
	// And it must not have leaked a temp file into the (existing) parent.
	if dirHasTempLeak(t, dir) {
		t.Error("a .tmp-* file leaked into the parent after a failed WriteFileAtomic")
	}
}

// TestWithinRelError targets the filepath.Rel error branch (Within line ~114):
// Rel returns an error when one path is absolute and the other is relative, so
// the two cannot be made relative to each other.
func TestWithinRelError(t *testing.T) {
	// Absolute root, relative target → filepath.Rel fails → Within is false.
	if Within("/srv/mirror", "relative/target") {
		t.Error("Within(abs, rel) should be false (Rel error path)")
	}
	// Relative root, absolute target → also a Rel error.
	if Within("relative/root", "/etc/passwd") {
		t.Error("Within(rel, abs) should be false (Rel error path)")
	}
	// And the plain escaping case (cleaned rel begins with "..").
	if Within("/srv/mirror", "/srv/other") {
		t.Error("Within should reject a sibling path that escapes the root")
	}
}
