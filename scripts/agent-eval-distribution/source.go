package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func hashSelectedTree(root string, paths []string) (string, map[string][]byte, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&fs.ModeSymlink != 0 {
		return "", nil, errors.New("source root must be a plain directory")
	}
	files := map[string]fileEntry{}
	snapshot := map[string][]byte{}
	var total int64
	visitedEntries := 0
	for _, selected := range paths {
		if selected == "" || filepath.IsAbs(selected) || filepath.Clean(selected) != selected || strings.HasPrefix(selected, ".."+string(filepath.Separator)) || selected == ".." {
			return "", nil, errors.New("source selection must be relative and contained")
		}
		if sensitiveSourcePath(selected) {
			return "", nil, errors.New("source selection contains a sensitive path")
		}
		absolute := filepath.Join(root, selected)
		if err := rejectSymlinkComponents(root, selected); err != nil {
			return "", nil, err
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", nil, errors.New("source selection contains a symlink")
		}
		if info.IsDir() {
			err = walkSourceDirectory(root, absolute, files, snapshot, &total, &visitedEntries)
		} else {
			err = addTreeFile(files, snapshot, filepath.ToSlash(selected), absolute, &total)
		}
		if err != nil {
			return "", nil, err
		}
	}
	if len(files) == 0 || len(files) > maxSourceFiles || total > maxSourceTreeBytes {
		return "", nil, errors.New("source tree exceeds bounded selection")
	}
	entries := make([]fileEntry, 0, len(files))
	for _, entry := range files {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", entry.Name, entry.SizeBytes, entry.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil)), snapshot, nil
}

func rejectSymlinkComponents(root, relative string) error {
	current := root
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("source selection contains a symlink component")
		}
	}
	return nil
}

func requireSelectedSourceFile(root string, selections []string, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	relativeSlash, err := relativeSourcePath(root, target)
	if err != nil {
		return errors.New("input must be inside the selected source root")
	}
	relative := filepath.FromSlash(relativeSlash)
	if err := rejectSymlinkComponents(filepath.Clean(rootAbs), relative); err != nil {
		return err
	}
	for _, selected := range selections {
		selected = filepath.Clean(selected)
		selectedAbs := filepath.Join(filepath.Clean(rootAbs), selected)
		selectedRelative, relErr := filepath.Rel(filepath.Clean(rootAbs), selectedAbs)
		if relErr != nil {
			continue
		}
		if relative == selectedRelative || strings.HasPrefix(relative, selectedRelative+string(filepath.Separator)) {
			return nil
		}
	}
	return errors.New("input is not covered by source selection")
}

func relativeSourcePath(root, target string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(targetAbs))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("path is outside the selected source root")
	}
	return filepath.ToSlash(relative), nil
}

func selectedSourceData(root string, selections []string, snapshot map[string][]byte, target string) ([]byte, error) {
	if err := requireSelectedSourceFile(root, selections, target); err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(targetAbs))
	if err != nil {
		return nil, err
	}
	data, ok := snapshot[filepath.ToSlash(relative)]
	if !ok {
		return nil, errors.New("input snapshot is missing selected file")
	}
	return data, nil
}

func walkSourceDirectory(sourceRoot, directoryPath string, files map[string]fileEntry, snapshot map[string][]byte, total *int64, visitedEntries *int) error {
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(maxSourceFiles*2 + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) > maxSourceFiles*2 {
		return errors.New("source tree exceeds bounded entry selection")
	}
	for _, entry := range entries {
		*visitedEntries = *visitedEntries + 1
		if *visitedEntries > maxSourceFiles*2 || entry.Type()&fs.ModeSymlink != 0 || sensitiveSourceName(entry.Name()) {
			return errors.New("source tree contains an unsafe or oversized entry")
		}
		path := filepath.Join(directoryPath, entry.Name())
		if entry.IsDir() {
			if err := walkSourceDirectory(sourceRoot, path, files, snapshot, total, visitedEntries); err != nil {
				return err
			}
			continue
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if err := addTreeFile(files, snapshot, filepath.ToSlash(relative), path, total); err != nil {
			return err
		}
	}
	return nil
}

func addTreeFile(files map[string]fileEntry, snapshot map[string][]byte, relative, absolute string, total *int64) error {
	if !safeName(relative) || sensitiveSourcePath(relative) {
		return errors.New("source tree path is not canonical")
	}
	if _, exists := files[relative]; exists {
		return nil
	}
	if len(files) >= maxSourceFiles {
		return errors.New("source tree exceeds bounded file selection")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("source tree member is not a regular file")
	}
	if info.Size() < 0 || info.Size() > maxArtifactBytes || *total > maxSourceTreeBytes-info.Size() {
		return errors.New("source tree exceeds bounded byte selection")
	}
	data, err := readFileBounded(absolute, maxArtifactBytes)
	if err != nil {
		return err
	}
	files[relative] = fileEntry{Name: relative, SizeBytes: int64(len(data)), SHA256: sha256Bytes(data)}
	snapshot[relative] = data
	*total += int64(len(data))
	return nil
}

func sensitiveSourceName(name string) bool {
	lower := strings.ToLower(name)
	return lower == ".git" || lower == ".hg" || lower == ".svn" || lower == ".ephemeral" || lower == ".env" || lower == ".coverage" || lower == ".pytest_cache" || lower == ".ds_store" || lower == "coverage.out" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".env") || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx")
}

func sensitiveSourcePath(name string) bool {
	for _, component := range strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool { return r == '/' }) {
		lower := strings.ToLower(component)
		if sensitiveSourceName(component) || lower == "vendor" || lower == "node_modules" || lower == ".cache" || lower == ".idea" || lower == ".vscode" || lower == "tmp" {
			return true
		}
	}
	return false
}
