package agentskills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadStableTreeRejectsSymlinkRootAndEntries(t *testing.T) {
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "regular.txt"), "inside\n")
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := readStableTree(rootLink)
	requireErrorCode(t, err, ErrorInvalidRoot)

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("descendant symlink unavailable: %v", err)
	}
	_, err = readStableTree(root)
	requireErrorCode(t, err, ErrorUnstableSource)
}

func TestReadStableTreeDetectsSameSizeContentMutationAcrossInventories(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "value.txt")
	writeFile(t, name, "alpha")
	initial, err := os.Stat(name)
	if err != nil {
		t.Fatalf("Stat(): %v", err)
	}
	var hookErr error
	_, err = readStableTreeWithHooks(root, stableTreeHooks{
		afterFirstInventory: func() {
			hookErr = os.WriteFile(name, []byte("bravo"), 0o600)
			if hookErr == nil {
				hookErr = os.Chtimes(name, initial.ModTime(), initial.ModTime())
			}
		},
	})
	if hookErr != nil {
		t.Fatalf("mutation hook: %v", hookErr)
	}
	requireErrorCode(t, err, ErrorUnstableSource)
}

func TestReadStableTreeReturnsDetachedDeterministicBytes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "z.txt"), "zeta")
	writeFile(t, filepath.Join(root, "nested", "a.txt"), "alpha")

	first, err := readStableTree(root)
	if err != nil {
		t.Fatalf("readStableTree() error = %v", err)
	}
	second, err := readStableTree(root)
	if err != nil {
		t.Fatalf("second readStableTree() error = %v", err)
	}
	firstFiles := snapshotFiles(first, nil)
	secondFiles := snapshotFiles(second, nil)
	if got, want := snapshotPaths(firstFiles), []string{"nested/a.txt", "z.txt"}; !equalStrings(got, want) {
		t.Fatalf("snapshot paths = %#v, want %#v", got, want)
	}
	if digestSnapshotFiles("test", firstFiles) != digestSnapshotFiles("test", secondFiles) {
		t.Fatal("stable reads produced different digests")
	}
	firstFiles[0].Data[0] = 'X'
	if second.files["nested/a.txt"].data[0] != 'a' {
		t.Fatal("snapshot bytes alias another capture")
	}
}

func TestReadStableTreeRootInventoriesHeldDirectoryAfterAmbientRename(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "source")
	moved := filepath.Join(parent, "moved-source")
	writeFile(t, filepath.Join(original, "original.txt"), "original\n")
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatalf("OpenRoot(): %v", err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Rename(original, moved); err != nil {
		t.Fatalf("Rename(): %v", err)
	}
	writeFile(t, filepath.Join(original, "replacement.txt"), "replacement\n")

	tree, err := readStableTreeRoot(root)
	if err != nil {
		t.Fatalf("readStableTreeRoot() error = %v", err)
	}
	if _, ok := tree.files["original.txt"]; !ok || len(tree.files) != 1 {
		t.Fatalf("held-root inventory = %#v", tree.files)
	}
}

func TestReadStableTreeRejectsNonDirectoryAndMissingRoots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "data")
	for _, root := range []string{file, filepath.Join(t.TempDir(), "missing")} {
		_, err := readStableTree(root)
		requireErrorCode(t, err, ErrorInvalidRoot)
		if errors.Is(err, os.ErrNotExist) && err.Error() != string(ErrorInvalidRoot) {
			t.Fatalf("rendered error leaked ambient details: %q", err)
		}
	}
}

func TestReadStableTreeEnforcesFileBoundBeforeReading(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "oversized.bin")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(): %v", err)
	}
	if err := file.Truncate(MaxFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate(): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	_, err = readStableTree(root)
	requireErrorCode(t, err, ErrorLimitExceeded)
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
