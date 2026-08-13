package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type hardenedIOParityContract struct {
	SchemaVersion int `json:"schema_version"`
	Cases         []struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		Limit     int64  `json:"limit"`
		Want      string `json:"want,omitempty"`
		WantError bool   `json:"want_error,omitempty"`
	} `json:"cases"`
}

func hardenedTestNoTempLeak(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", directory, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file leaked in %q: %q", directory, entry.Name())
		}
	}
}

func hardenedTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestHardenedReadFileWithinLimitBoundsAndLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bounded.txt")
	if err := os.WriteFile(target, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := hardenedReadFileWithinLimit(root, target, 5); err != nil || string(got) != "12345" {
		t.Fatalf("exact limit: got=%q err=%v", got, err)
	}
	if _, err := hardenedReadFileWithinLimit(root, target, 4); err == nil || err.Error() != "file exceeds 4-byte read limit" {
		t.Fatalf("overflow error = %v", err)
	}
	if _, err := hardenedReadFileWithinLimit(root, filepath.Join(root, "..", "outside"), -1); err == nil || err.Error() != "invalid read limit -1" {
		t.Fatalf("negative-limit error order changed: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hardenedReadFileWithinLimitContext(canceled, root, target, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded read error = %v", err)
	}
	if _, err := hardenedReadFileWithinLimit(root, root, 16); err == nil {
		t.Fatal("root itself was accepted as a contained file target")
	}
	if _, err := hardenedReadFileWithinLimit(root, t.TempDir(), 16); err == nil {
		t.Fatal("outside target was accepted")
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardenedTestSymlink(t, outside, filepath.Join(root, "outside-link.txt"))
	if _, err := hardenedReadFileWithinLimit(root, filepath.Join(root, "outside-link.txt"), 16); err == nil {
		t.Fatal("bounded read followed an escaping final symlink")
	}

	outsideDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDirectory, "nested.txt"), []byte("outside nested"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardenedTestSymlink(t, outsideDirectory, filepath.Join(root, "outside-directory"))
	if _, err := hardenedReadFileWithinLimit(root, filepath.Join(root, "outside-directory", "nested.txt"), 32); err == nil {
		t.Fatal("bounded read followed an escaping intermediate symlink")
	}

	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Use a relative in-root alias to exercise the link policy that the bounded
	// reader intentionally delegates to the held root.
	hardenedTestSymlink(t, "real", filepath.Join(root, "inside-directory"))
	if got, err := hardenedReadFileWithinLimit(root, filepath.Join(root, "inside-directory", "inside.txt"), 6); err != nil || string(got) != "inside" {
		t.Fatalf("bounded read rejected an in-root link: got=%q err=%v", got, err)
	}
	hardenedTestSymlink(t, filepath.Join("real", "inside.txt"), filepath.Join(root, "inside-file.txt"))
	if got, err := hardenedReadFileWithinLimit(root, filepath.Join(root, "inside-file.txt"), 6); err != nil || string(got) != "inside" {
		t.Fatalf("bounded read rejected an in-root final link: got=%q err=%v", got, err)
	}
}

func TestHardenedReadFileWithinLimitSharedParityContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "safepath", "testdata", "io-parity.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract hardenedIOParityContract
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
	hardenedTestSymlink(t, filepath.Join("real", "inside.txt"), filepath.Join(root, "inside-file.txt"))
	hardenedTestSymlink(t, "real", filepath.Join(root, "inside-directory"))
	hardenedTestSymlink(t, outsideFile, filepath.Join(root, "outside-file.txt"))
	hardenedTestSymlink(t, outsideDirectory, filepath.Join(root, "outside-directory"))
	for _, test := range contract.Cases {
		t.Run(test.Name, func(t *testing.T) {
			got, err := hardenedReadFileWithinLimit(root, filepath.Join(root, filepath.FromSlash(test.Path)), test.Limit)
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

func TestHardenedMkdirStatAndReadDirPolicies(t *testing.T) {
	root := t.TempDir()
	if err := hardenedMkdirAllWithin(root, root, 0o700); err != nil {
		t.Fatalf("root mkdir: %v", err)
	}
	directory := filepath.Join(root, "listing", "nested")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		t.Fatalf("contained mkdir: %v", err)
	}
	if info, err := hardenedStatWithin(root, directory); err != nil || !info.IsDir() {
		t.Fatalf("contained stat: info=%v err=%v", info, err)
	}
	if _, err := hardenedStatWithin(root, root); err == nil {
		t.Fatal("root itself was accepted as a stat target")
	}
	if err := hardenedMkdirAllWithin(root, t.TempDir(), 0o700); err == nil {
		t.Fatal("outside mkdir was accepted")
	}

	listing := filepath.Join(root, "listing")
	for _, name := range []string{"z.json", "a.json", "m.json"} {
		if err := os.WriteFile(filepath.Join(listing, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := hardenedReadDirWithin(root, listing)
	if err != nil {
		t.Fatalf("contained read-dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"a.json", "m.json", "nested", "z.json"}) {
		t.Fatalf("directory entries = %q, want sorted names", names)
	}
	bounded, err := hardenedReadDirWithinLimitContext(context.Background(), root, listing, 4)
	if err != nil || len(bounded) != 4 {
		t.Fatalf("bounded directory entries=%d err=%v", len(bounded), err)
	}
	if _, err := hardenedReadDirWithinLimitContext(context.Background(), root, listing, 3); err == nil || err.Error() != "directory exceeds 3-entry read limit" {
		t.Fatalf("directory overflow error = %v", err)
	}
	if _, err := hardenedReadDirWithin(root, root); err != nil {
		t.Fatalf("root read-dir: %v", err)
	}
	if _, err := hardenedReadDirWithin(root, t.TempDir()); err == nil {
		t.Fatal("outside read-dir was accepted")
	}

	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	hardenedTestSymlink(t, realDirectory, filepath.Join(root, "inside-link"))
	if err := hardenedMkdirAllWithin(root, filepath.Join(root, "inside-link", "new"), 0o700); err == nil {
		t.Fatal("mkdir accepted an in-root directory symlink")
	}
	if _, err := hardenedStatWithin(root, filepath.Join(root, "inside-link")); err == nil {
		t.Fatal("stat accepted an in-root final symlink")
	}
	if _, err := hardenedReadDirWithin(root, filepath.Join(root, "inside-link")); err == nil {
		t.Fatal("read-dir accepted an in-root final symlink")
	}

	outside := t.TempDir()
	hardenedTestSymlink(t, outside, filepath.Join(root, "outside-link"))
	if _, err := hardenedStatWithin(root, filepath.Join(root, "outside-link", "artifact")); err == nil {
		t.Fatal("stat followed an escaping intermediate symlink")
	}
}

func TestHardenedWriteFileWithinReplacesFinalLinkAndRejectsDescendants(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "state.json")
	if err := os.WriteFile(target, []byte("loose"), 0o666); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := hardenedWriteFileWithin(root, target, []byte("fresh"), 0o600); err != nil {
		t.Fatalf("atomic replacement: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "fresh" {
		t.Fatalf("replacement contents=%q err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("replacement mode=%v err=%v", info, err)
		}
	}
	hardenedTestNoTempLeak(t, root)

	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedTarget := filepath.Join(root, "linked.json")
	hardenedTestSymlink(t, outside, linkedTarget)
	if err := hardenedWriteFileWithin(root, linkedTarget, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write over final symlink: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "original" {
		t.Fatalf("outside link target changed: %q err=%v", got, err)
	}
	if info, err := os.Lstat(linkedTarget); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("final link was not replaced: info=%v err=%v", info, err)
	}

	outsideDirectory := t.TempDir()
	hardenedTestSymlink(t, outsideDirectory, filepath.Join(root, "outside-directory"))
	if err := hardenedWriteFileWithin(root, filepath.Join(root, "outside-directory", "blocked.json"), []byte("x"), 0o600); err == nil {
		t.Fatal("write followed an escaping intermediate symlink")
	}
	if _, err := os.Stat(filepath.Join(outsideDirectory, "blocked.json")); !os.IsNotExist(err) {
		t.Fatalf("outside target was written: %v", err)
	}

	insideDirectory := filepath.Join(root, "inside")
	if err := os.Mkdir(insideDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	hardenedTestSymlink(t, insideDirectory, filepath.Join(root, "inside-directory"))
	if err := hardenedWriteFileWithin(root, filepath.Join(root, "inside-directory", "blocked.json"), []byte("x"), 0o600); err == nil {
		t.Fatal("write accepted an in-root intermediate symlink")
	}
	if _, err := os.Stat(filepath.Join(insideDirectory, "blocked.json")); !os.IsNotExist(err) {
		t.Fatalf("in-root alias was written: %v", err)
	}
	if err := hardenedWriteFileWithin(root, root, []byte("x"), 0o600); err == nil {
		t.Fatal("root itself was accepted as a write target")
	}
}

func TestHardenedWriteFileExclusiveWithinDoesNotClobberOrLeak(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "private")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "plan.json")
	if err := hardenedWriteFileExclusiveWithin(root, target, []byte("first"), 0o600); err != nil {
		t.Fatalf("first exclusive write: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("exclusive mode=%v err=%v", info, err)
		}
	}
	if err := hardenedWriteFileExclusiveWithin(root, target, []byte("second"), 0o600); !os.IsExist(err) {
		t.Fatalf("second exclusive write error=%v, want exists", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "first" {
		t.Fatalf("exclusive target changed: %q err=%v", got, err)
	}
	hardenedTestNoTempLeak(t, directory)

	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(directory, "final-link.json")
	hardenedTestSymlink(t, outside, finalLink)
	if err := hardenedWriteFileExclusiveWithin(root, finalLink, []byte("replacement"), 0o600); !os.IsExist(err) {
		t.Fatalf("exclusive write to final symlink error=%v, want exists", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "original" {
		t.Fatalf("outside final-link target changed: %q err=%v", got, err)
	}
	hardenedTestNoTempLeak(t, directory)

	outsideDirectory := t.TempDir()
	hardenedTestSymlink(t, outsideDirectory, filepath.Join(root, "outside-directory"))
	if err := hardenedWriteFileExclusiveWithin(root, filepath.Join(root, "outside-directory", "blocked.json"), []byte("x"), 0o600); err == nil {
		t.Fatal("exclusive write followed an escaping intermediate symlink")
	}
	if err := hardenedWriteFileExclusiveWithin(root, root, []byte("x"), 0o600); err == nil {
		t.Fatal("root itself was accepted as an exclusive target")
	}
}

func TestHardenedRenameAndRemoveContainment(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "source.json")
	if err := hardenedWriteFileWithin(root, source, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "destination.json")
	if err := hardenedRenameWithin(root, source, destination); err != nil {
		t.Fatalf("contained rename: %v", err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "contents" {
		t.Fatalf("renamed contents=%q err=%v", got, err)
	}
	if err := hardenedRemoveWithin(root, destination); err != nil {
		t.Fatalf("contained remove: %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("contained target remains after remove: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(directory, "final-link.json")
	hardenedTestSymlink(t, outside, finalLink)
	if err := hardenedRemoveWithin(root, finalLink); err != nil {
		t.Fatalf("remove final symlink: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "original" {
		t.Fatalf("remove followed final symlink: %q err=%v", got, err)
	}
	if _, err := os.Lstat(finalLink); !os.IsNotExist(err) {
		t.Fatalf("final symlink remains: %v", err)
	}

	if err := hardenedWriteFileWithin(root, source, []byte("rename"), 0o600); err != nil {
		t.Fatal(err)
	}
	finalDestination := filepath.Join(directory, "rename-link.json")
	hardenedTestSymlink(t, outside, finalDestination)
	if err := hardenedRenameWithin(root, source, finalDestination); err != nil {
		t.Fatalf("rename over final symlink: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "original" {
		t.Fatalf("rename followed final symlink: %q err=%v", got, err)
	}
	if info, err := os.Lstat(finalDestination); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("rename did not replace final symlink: info=%v err=%v", info, err)
	}

	outsideDirectory := t.TempDir()
	hardenedTestSymlink(t, outsideDirectory, filepath.Join(root, "outside-directory"))
	if err := hardenedRemoveWithin(root, filepath.Join(root, "outside-directory", "blocked")); err == nil {
		t.Fatal("remove followed an escaping intermediate symlink")
	}
	if err := hardenedRenameWithin(root, finalDestination, filepath.Join(root, "outside-directory", "blocked")); err == nil {
		t.Fatal("rename followed an escaping destination parent symlink")
	}
	if err := hardenedRenameWithin(root, finalDestination, t.TempDir()); err == nil {
		t.Fatal("rename accepted an outside destination")
	}
	if err := hardenedRemoveWithin(root, root); err == nil {
		t.Fatal("root itself was accepted as a remove target")
	}
}

func TestHardenedSyncDirectoryWithin(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		err := hardenedSyncDirectoryWithin(root, root)
		if !errors.Is(err, errHardenedUnsafePrivatePath) {
			t.Fatalf("windows directory sync error=%v", err)
		}
		return
	}
	if err := hardenedSyncDirectoryWithin(root, root); err != nil {
		t.Fatalf("root directory sync: %v", err)
	}
	directory := filepath.Join(root, "state")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := hardenedSyncDirectoryWithin(root, directory); err != nil {
		t.Fatalf("contained directory sync: %v", err)
	}
	file := filepath.Join(directory, "file")
	if err := hardenedWriteFileWithin(root, file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hardenedSyncDirectoryWithin(root, file); !errors.Is(err, errHardenedUnsafePrivatePath) {
		t.Fatalf("non-directory sync error=%v", err)
	}
	if err := hardenedSyncDirectoryWithin(root, t.TempDir()); err == nil {
		t.Fatal("outside directory sync was accepted")
	}
	hardenedTestSymlink(t, directory, filepath.Join(root, "directory-link"))
	if err := hardenedSyncDirectoryWithin(root, filepath.Join(root, "directory-link")); err == nil {
		t.Fatal("directory sync accepted a final symlink")
	}
}

func TestHardenedWriteFileAtomicPrivateHoldsOwnerOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX owner-only parent mode semantics are unavailable on windows")
	}
	privateDirectory := t.TempDir()
	if err := os.Chmod(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(privateDirectory, "artifact.json")
	if err := hardenedWriteFileAtomicPrivate(target, []byte("private\n"), 0o600); err != nil {
		t.Fatalf("private atomic write: %v", err)
	}
	if err := hardenedWriteFileAtomicPrivate(target, []byte("replacement\n"), 0o640); err != nil {
		t.Fatalf("private atomic replacement: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "replacement\n" {
		t.Fatalf("private target=%q err=%v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("private target mode=%v err=%v", info, err)
	}
	hardenedTestNoTempLeak(t, privateDirectory)

	publicDirectory := t.TempDir()
	if err := os.Chmod(publicDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := hardenedWriteFileAtomicPrivate(filepath.Join(publicDirectory, "artifact.json"), []byte("x"), 0o600); !errors.Is(err, errHardenedUnsafePrivatePath) {
		t.Fatalf("public parent error=%v", err)
	}

	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "private-link")
	hardenedTestSymlink(t, publicDirectory, link)
	if err := hardenedWriteFileAtomicPrivate(filepath.Join(link, "artifact.json"), []byte("x"), 0o600); !errors.Is(err, errHardenedUnsafePrivatePath) {
		t.Fatalf("public symlink parent error=%v", err)
	}
}

func TestHardenedTryLockFileWithinIsExclusiveAndIdempotent(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "workspace.lock")
	first, acquired, err := hardenedTryLockFileWithin(root, target, 0o600)
	if err != nil || !acquired || first == nil {
		t.Fatalf("first lock: lock=%v acquired=%t err=%v", first, acquired, err)
	}
	second, acquired, err := hardenedTryLockFileWithin(root, target, 0o600)
	if err != nil || acquired || second != nil {
		t.Fatalf("second lock: lock=%v acquired=%t err=%v", second, acquired, err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("idempotent unlock: %v", err)
	}
	third, acquired, err := hardenedTryLockFileWithin(root, target, 0o600)
	if err != nil || !acquired || third == nil {
		t.Fatalf("lock after release: lock=%v acquired=%t err=%v", third, acquired, err)
	}
	if err := third.Unlock(); err != nil {
		t.Fatalf("third unlock: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hardenedTestSymlink(t, outside, filepath.Join(directory, "link.lock"))
	if lock, acquired, err := hardenedTryLockFileWithin(root, filepath.Join(directory, "link.lock"), 0o600); err == nil || acquired || lock != nil {
		t.Fatalf("lock accepted final symlink: lock=%v acquired=%t err=%v", lock, acquired, err)
	}
	if lock, acquired, err := hardenedTryLockFileWithin(root, root, 0o600); err == nil || acquired || lock != nil {
		t.Fatalf("lock accepted root target: lock=%v acquired=%t err=%v", lock, acquired, err)
	}
}
