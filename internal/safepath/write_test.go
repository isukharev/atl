package safepath

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type ioParityContract struct {
	SchemaVersion int `json:"schema_version"`
	Cases         []struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		Limit     int64  `json:"limit"`
		Want      string `json:"want,omitempty"`
		WantError bool   `json:"want_error,omitempty"`
	} `json:"cases"`
}

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

// TestWriteFileRefusesSymlink is the core final-component no-follow guarantee:
// a symlink planted at the target path must not be followed; the write fails
// and the link target stays untouched.
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
		t.Fatal("WriteFile through a symlink succeeded; final-component no-follow not enforced")
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

// failingReader errors after yielding a prefix — a mid-download transport
// failure.
type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		r.n++
		copy(p, "partial-")
		return 8, nil
	}
	return 0, errors.New("connection reset mid-body")
}

// TestWriteReaderAtomicPartialFailureLeavesNothing: a reader failing mid-copy
// must leave neither the destination file nor a temp leak — an interrupted
// download can never plant a truncated file.
func TestWriteReaderAtomicPartialFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.bin")
	if _, err := WriteReaderAtomic(path, &failingReader{}, 0o644); err == nil {
		t.Fatal("mid-copy failure must propagate")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("truncated destination exists after failed copy (stat err %v)", err)
	}
	if dirHasTempLeak(t, dir) {
		t.Error("temp file leaked after failed copy")
	}
}

// TestWriteReaderAtomicDoesNotClobberOnFailure: an existing good file at path
// survives a failed re-download byte-for-byte.
func TestWriteReaderAtomicDoesNotClobberOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.bin")
	if err := os.WriteFile(path, []byte("good old bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteReaderAtomic(path, &failingReader{}, 0o644); err == nil {
		t.Fatal("mid-copy failure must propagate")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "good old bytes" {
		t.Errorf("existing file clobbered by failed download: %q (err %v)", got, err)
	}
}

// TestWriteReaderAtomicStreamsAndReportsSize pins the happy path: bytes,
// count, and mode.
func TestWriteReaderAtomicStreamsAndReportsSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	n, err := WriteReaderAtomic(path, strings.NewReader("stream me"), 0o600)
	if err != nil {
		t.Fatalf("WriteReaderAtomic: %v", err)
	}
	if n != int64(len("stream me")) {
		t.Errorf("n = %d", n)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "stream me" {
		t.Errorf("content = %q", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
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

func TestRootContainedWritersRefuseEscapingParentSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "project")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	target := filepath.Join(link, "issue.wiki")
	if err := MkdirAllWithin(root, filepath.Join(link, "assets"), 0o755); err == nil {
		t.Fatal("MkdirAllWithin followed an escaping parent symlink")
	}
	if err := WriteFileWithin(root, target, []byte("secret"), 0o644); err == nil {
		t.Fatal("WriteFileWithin followed an escaping parent symlink")
	}
	if err := RenameWithin(root, filepath.Join(link, "source"), target); err == nil {
		t.Fatal("RenameWithin followed an escaping parent symlink")
	}
	if _, err := WriteReaderAtomicWithin(root, target, strings.NewReader("secret"), 0o644); err == nil {
		t.Fatal("WriteReaderAtomicWithin followed an escaping parent symlink")
	}
	if err := RemoveWithin(root, target); err == nil {
		t.Fatal("RemoveWithin followed an escaping parent symlink")
	}
	if _, err := ReadFileWithin(root, target); err == nil {
		t.Fatal("ReadFileWithin followed an escaping parent symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "issue.wiki")); !os.IsNotExist(err) {
		t.Fatalf("outside target was created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "assets")); !os.IsNotExist(err) {
		t.Fatalf("outside directory was created: %v", err)
	}
}

func TestRenameWithin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := MkdirAllWithin(root, dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "txn")
	dest := filepath.Join(dir, "pending")
	if err := WriteFileWithin(root, source, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameWithin(root, source, dest); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFileWithin(root, dest); err != nil || string(got) != "first" {
		t.Fatalf("renamed content=%q err=%v", got, err)
	}
}

func TestSyncDirectoryWithinRejectsNonDirectoryAndSymlink(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		if err := SyncDirectoryWithin(root, root); err == nil {
			t.Fatal("directory sync unexpectedly succeeded on windows")
		}
		return
	}
	if err := SyncDirectoryWithin(root, root); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "state")
	if err := MkdirAllWithin(root, dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectoryWithin(root, dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := WriteFileWithin(root, file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectoryWithin(root, file); err == nil {
		t.Fatal("regular file was accepted as a directory sync target")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectoryWithin(root, link); err == nil {
		t.Fatal("symlink directory was accepted as a sync target")
	}
}

func TestTryLockFileWithinIsExclusiveAndCrashScoped(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := MkdirAllWithin(root, dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "issue.lock")
	if err := WriteFileWithin(root, path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	first, acquired, err := TryLockFileWithin(root, path, 0o600)
	if err != nil || !acquired {
		t.Fatalf("first lock: acquired=%v err=%v", acquired, err)
	}
	if second, acquired, err := TryLockFileWithin(root, path, 0o600); err != nil || acquired || second != nil {
		t.Fatalf("second lock: lock=%v acquired=%v err=%v", second, acquired, err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	third, acquired, err := TryLockFileWithin(root, path, 0o600)
	if err != nil || !acquired {
		t.Fatalf("lock after release: acquired=%v err=%v", acquired, err)
	}
	_ = third.Unlock()
}

func TestTrySharedLockExistingFileWithinDoesNotCreateAndCoordinatesWithWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mirror.lock")
	if lock, acquired, err := TrySharedLockExistingFileWithin(root, path); !os.IsNotExist(err) || acquired || lock != nil {
		t.Fatalf("missing shared lock: lock=%v acquired=%t err=%v", lock, acquired, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("shared lock created missing file: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	first, acquired, err := TrySharedLockExistingFileWithin(root, path)
	if err != nil || !acquired || first == nil {
		t.Fatalf("first shared lock: acquired=%t err=%v", acquired, err)
	}
	defer func() { _ = first.Unlock() }()
	second, acquired, err := TrySharedLockExistingFileWithin(root, path)
	if err != nil || !acquired || second == nil {
		t.Fatalf("second shared lock: acquired=%t err=%v", acquired, err)
	}
	if writer, acquired, err := TryLockFileWithin(root, path, 0o600); err != nil || acquired || writer != nil {
		t.Fatalf("writer while readers active: lock=%v acquired=%t err=%v", writer, acquired, err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	writer, acquired, err := TryLockFileWithin(root, path, 0o600)
	if err != nil || !acquired || writer == nil {
		t.Fatalf("writer after readers: acquired=%t err=%v", acquired, err)
	}
	if err := writer.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileWithinReplacesFinalSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "view.md")
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFileWithin(root, target, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Fatalf("outside target changed: %q", got)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target was not replaced with a regular file: info=%v err=%v", info, err)
	}
}

func TestWriteFileOwnedAtomicWithinUsesOnlyDeclaredSibling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "state.json")
	for _, invalid := range []string{".", "..", "state.json", "nested/temp", "drive:temp", "control\ntemp"} {
		if err := WriteFileOwnedAtomicWithin(root, target, invalid, []byte("x"), 0o600); err == nil {
			t.Fatalf("accepted invalid owned temp name %q", invalid)
		}
	}
	temp := ".atl-cp-0123456789abcdef0123456789abcdef-sidecar.tmp"
	if err := os.WriteFile(filepath.Join(dir, temp), []byte("surviving residue"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileOwnedAtomicWithin(root, target, temp, []byte("new"), 0o600); !os.IsExist(err) {
		t.Fatalf("declared residue was reused implicitly: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, temp)); err != nil || string(got) != "surviving residue" {
		t.Fatalf("declared residue changed: %q err=%v", got, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target appeared after O_EXCL refusal: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, temp)); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileOwnedAtomicWithin(root, target, temp, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
		t.Fatalf("target=%q err=%v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("target mode=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, temp)); !os.IsNotExist(err) {
		t.Fatalf("owned temp survived rename: %v", err)
	}
}

func TestWriteFileExclusiveWithinNeverReplacesExistingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "private", "plan.json")
	if err := MkdirAllWithin(root, filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusiveWithin(root, target, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusiveWithin(root, target, []byte("second"), 0o600); !os.IsExist(err) {
		t.Fatalf("second write error = %v, want existence refusal", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "first" {
		t.Fatalf("target = %q, err=%v", data, err)
	}
}

func TestWriteFileExclusiveWithinRejectsDescendantSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "private")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFileExclusiveWithin(root, filepath.Join(link, "plan.json"), []byte("secret"), 0o600); err == nil {
		t.Fatal("exclusive write followed a descendant symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "plan.json")); !os.IsNotExist(err) {
		t.Fatalf("outside target exists: %v", err)
	}
}

func TestStatWithinRefusesIntermediateSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "swapped")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := StatWithin(root, filepath.Join(link, "victim")); err == nil {
		t.Fatal("StatWithin followed an intermediate symlink outside root")
	}
}

func TestRootContainedWritersRejectInRootDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFileWithin(root, filepath.Join(link, "view.md"), []byte("x"), 0o644); err == nil {
		t.Fatal("WriteFileWithin accepted an in-root directory symlink")
	}
	if _, err := os.Stat(filepath.Join(realDir, "view.md")); !os.IsNotExist(err) {
		t.Fatalf("aliased target was written: %v", err)
	}
}

func TestWriteFileAtomicPrivateValidatesHeldParent(t *testing.T) {
	privateDir := t.TempDir()
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(privateDir, "artifact.json")
	if err := WriteFileAtomicPrivate(target, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "private\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info, err)
	}

	publicDir := t.TempDir()
	if err := os.Chmod(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicPrivate(filepath.Join(publicDir, "artifact.json"), []byte("x"), 0o600); err == nil {
		t.Fatal("public parent unexpectedly accepted")
	}
}

func TestWriteFileAtomicPrivateChecksOpenedSymlinkTargetMode(t *testing.T) {
	publicTarget := t.TempDir()
	if err := os.Chmod(publicTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "private-link")
	if err := os.Symlink(publicTarget, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFileAtomicPrivate(filepath.Join(link, "artifact.json"), []byte("x"), 0o600); err == nil {
		t.Fatal("symlink into public directory unexpectedly accepted")
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

func TestReadFileWithinLimit(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bounded.txt")
	if err := os.WriteFile(target, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFileWithinLimit(root, target, 5); err != nil || string(got) != "12345" {
		t.Fatalf("exact limit got=%q err=%v", got, err)
	}
	if _, err := ReadFileWithinLimit(root, target, 4); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("overflow err=%v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadFileWithinLimit(root, link, 16); err == nil {
		t.Fatal("bounded read followed an escaping symlink")
	}
}

func TestReadFileWithinLimitSharedParityContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "io-parity.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract ioParityContract
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil || contract.SchemaVersion != 1 || len(contract.Cases) == 0 {
		t.Fatalf("decode shared I/O parity contract: %+v, %v", contract, err)
	}
	fixtureRoot := t.TempDir()
	root := filepath.Join(fixtureRoot, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(fixtureRoot, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDirectory := filepath.Join(fixtureRoot, "outside-directory-target")
	if err := os.Mkdir(outsideDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDirectory, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for target, link := range map[string]string{
		filepath.Join("real", "inside.txt"): filepath.Join(root, "inside-file.txt"),
		"real":                              filepath.Join(root, "inside-directory"),
		outsideFile:                         filepath.Join(root, "outside-file.txt"),
		outsideDirectory:                    filepath.Join(root, "outside-directory"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, test := range contract.Cases {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ReadFileWithinLimit(root, filepath.Join(root, filepath.FromSlash(test.Path)), test.Limit)
			if test.WantError {
				if err == nil {
					t.Fatalf("read returned %q without an error", got)
				}
				return
			}
			if err != nil || string(got) != test.Want {
				t.Fatalf("read=%q err=%v, want %q", got, err, test.Want)
			}
		})
	}
}
