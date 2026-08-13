package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func treeDigest(root string) (string, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootHandle.Close() }()

	entries := make([]string, 0)
	err = fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		record := fmt.Sprintf("%s:%o:%d", filepath.ToSlash(path), info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			data, err := rootHandle.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			record += ":" + hex.EncodeToString(sum[:])
		}
		entries = append(entries, record)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func verifyPrivateTree(root string) error {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rootHandle.Close() }()
	return fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("runtime directory %s has mode %04o, want 0700", filepath.Base(path), info.Mode().Perm())
			}
			return nil
		}
		// Ordinary mirror views may request 0644. They remain inaccessible to
		// other users because every ancestor from root is an exact 0700
		// directory, which this same walk verifies before accepting the file.
		if !info.Mode().IsRegular() || (info.Mode().Perm() != 0o600 && info.Mode().Perm() != 0o644) {
			return fmt.Errorf("runtime artifact %s has unsafe mode %04o", filepath.Base(path), info.Mode().Perm())
		}
		return nil
	})
}

func containsMarker(path, marker string) (bool, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(path)
	info, err := parent.Lstat(name)
	if err != nil {
		return false, err
	}
	if info.Mode().IsRegular() {
		data, err := parent.ReadFile(name)
		return bytes.Contains(data, []byte(marker)), err
	}
	if !info.IsDir() {
		return false, nil
	}
	rootHandle, err := parent.OpenRoot(name)
	if err != nil {
		return false, err
	}
	defer func() { _ = rootHandle.Close() }()
	found := false
	err = fs.WalkDir(rootHandle.FS(), ".", func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := rootHandle.ReadFile(candidate)
		if err != nil {
			return err
		}
		found = found || bytes.Contains(data, []byte(marker))
		return nil
	})
	return found, err
}
