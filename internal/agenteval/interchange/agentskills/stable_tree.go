package agentskills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type stableTreeHooks struct {
	afterFirstInventory func()
	beforeFileRead      func(int, string)
}

type capturedTree struct {
	entries []capturedEntry
	files   map[string]capturedEntry
	bytes   int64
}

type capturedEntry struct {
	path   string
	info   fs.FileInfo
	isDir  bool
	digest string
	data   []byte
}

func readStableTree(rootPath string) (capturedTree, error) {
	return readStableTreeWithHooks(rootPath, stableTreeHooks{})
}

func readStableTreeWithHooks(rootPath string, hooks stableTreeHooks) (capturedTree, error) {
	if rootPath == "" {
		return capturedTree{}, contractError(ErrorInvalidRequest, nil)
	}
	initial, err := os.Lstat(rootPath)
	if err != nil || !initial.IsDir() || initial.Mode()&fs.ModeSymlink != 0 {
		return capturedTree{}, contractError(ErrorInvalidRoot, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return capturedTree{}, contractError(ErrorInvalidRoot, err)
	}
	defer func() { _ = root.Close() }()
	if err := verifyStableTreeRoot(rootPath, initial, root); err != nil {
		return capturedTree{}, contractError(ErrorUnstableSource, err)
	}
	result, err := readStableTreeRootWithHooks(root, hooks)
	if err != nil {
		return capturedTree{}, err
	}
	if err := verifyStableTreeRoot(rootPath, initial, root); err != nil {
		return capturedTree{}, contractError(ErrorUnstableSource, err)
	}
	return result, nil
}

func readStableTreeRoot(root *os.Root) (capturedTree, error) {
	return readStableTreeRootWithHooks(root, stableTreeHooks{})
}

func readStableTreeRootWithHooks(root *os.Root, hooks stableTreeHooks) (capturedTree, error) {
	if root == nil {
		return capturedTree{}, contractError(ErrorUnstableSource, fmt.Errorf("nil root"))
	}
	initial, err := root.Stat(".")
	if err != nil || !initial.IsDir() || initial.Mode()&fs.ModeSymlink != 0 {
		return capturedTree{}, contractError(ErrorUnstableSource, err)
	}
	first, err := inventoryStableTree(root, 1, hooks)
	if err != nil {
		return capturedTree{}, classifyTreeError(err)
	}
	if hooks.afterFirstInventory != nil {
		hooks.afterFirstInventory()
	}
	middle, err := root.Stat(".")
	if err != nil || !sameStableInfo(initial, middle) {
		return capturedTree{}, contractError(ErrorUnstableSource, fmt.Errorf("opened root changed"))
	}
	second, err := inventoryStableTree(root, 2, hooks)
	if err != nil {
		return capturedTree{}, classifyTreeError(err)
	}
	if !sameCapturedTree(first, second) {
		return capturedTree{}, contractError(ErrorUnstableSource, fmt.Errorf("tree changed"))
	}
	final, err := root.Stat(".")
	if err != nil || !sameStableInfo(initial, final) {
		return capturedTree{}, contractError(ErrorUnstableSource, fmt.Errorf("opened root changed"))
	}
	return first, nil
}

type treeLimitError struct{ message string }

func (e *treeLimitError) Error() string { return e.message }

func classifyTreeError(err error) error {
	if _, ok := err.(*treeLimitError); ok {
		return contractError(ErrorLimitExceeded, err)
	}
	return contractError(ErrorUnstableSource, err)
}

func verifyStableTreeRoot(rootPath string, initial fs.FileInfo, root *os.Root) error {
	ambient, ambientErr := os.Lstat(rootPath)
	opened, openedErr := root.Stat(".")
	if ambientErr != nil || openedErr != nil || !ambient.IsDir() || ambient.Mode()&fs.ModeSymlink != 0 ||
		!sameStableInfo(initial, ambient) || !sameStableInfo(initial, opened) {
		return fmt.Errorf("root changed")
	}
	return nil
}

func inventoryStableTree(root *os.Root, pass int, hooks stableTreeHooks) (capturedTree, error) {
	result := capturedTree{files: make(map[string]capturedEntry)}
	err := fs.WalkDir(root.FS(), ".", func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if len(path) > MaxPathBytes {
			return &treeLimitError{message: "path bound"}
		}
		if !validSourcePath(path) {
			return fmt.Errorf("invalid path")
		}
		if len(result.entries) >= MaxTreeEntries {
			return &treeLimitError{message: "entry bound"}
		}
		if directoryEntry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink entry")
		}
		entryInfo, err := directoryEntry.Info()
		if err != nil {
			return err
		}
		rootInfo, err := root.Lstat(filepath.FromSlash(path))
		if err != nil || !sameStableInfo(entryInfo, rootInfo) {
			return fmt.Errorf("entry changed")
		}
		entry := capturedEntry{path: path, info: rootInfo, isDir: rootInfo.IsDir()}
		if entry.isDir {
			result.entries = append(result.entries, entry)
			return nil
		}
		if rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.Mode().IsRegular() || rootInfo.Size() < 0 {
			return fmt.Errorf("non-regular entry")
		}
		if rootInfo.Size() > MaxFileBytes || result.bytes > MaxTreeBytes-rootInfo.Size() {
			return &treeLimitError{message: "byte bound"}
		}
		if hooks.beforeFileRead != nil {
			hooks.beforeFileRead(pass, path)
		}
		data, err := readStableTreeFile(root, path, rootInfo)
		if err != nil {
			return err
		}
		entry.data = data
		entry.digest = digestBytes(data)
		result.entries = append(result.entries, entry)
		result.files[path] = entry
		result.bytes += rootInfo.Size()
		return nil
	})
	if err != nil {
		return capturedTree{}, err
	}
	sort.Slice(result.entries, func(i, j int) bool { return result.entries[i].path < result.entries[j].path })
	return result, nil
}

func readStableTreeFile(root *os.Root, slashPath string, expected fs.FileInfo) ([]byte, error) {
	file, err := root.Open(filepath.FromSlash(slashPath))
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !sameStableInfo(expected, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("entry changed")
	}
	reader := &io.LimitedReader{R: file, N: expected.Size() + 1}
	data, readErr := io.ReadAll(reader)
	final, finalErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) != expected.Size() || reader.N == 0 || finalErr != nil || !sameStableInfo(opened, final) {
		return nil, fmt.Errorf("entry changed")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func sameCapturedTree(first, second capturedTree) bool {
	if first.bytes != second.bytes || len(first.entries) != len(second.entries) {
		return false
	}
	for index := range first.entries {
		left, right := first.entries[index], second.entries[index]
		if left.path != right.path || left.isDir != right.isDir || left.digest != right.digest ||
			!sameStableInfo(left.info, right.info) {
			return false
		}
	}
	return true
}

func sameStableInfo(first, second fs.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Mode() == second.Mode() &&
		first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}

func snapshotFiles(tree capturedTree, include func(string) bool) []SnapshotFile {
	files := make([]SnapshotFile, 0, len(tree.files))
	for _, entry := range tree.files {
		if include != nil && !include(entry.path) {
			continue
		}
		files = append(files, SnapshotFile{
			Path: entry.path, SHA256: entry.digest, SizeBytes: uint64(len(entry.data)),
			Data: append([]byte(nil), entry.data...),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}
