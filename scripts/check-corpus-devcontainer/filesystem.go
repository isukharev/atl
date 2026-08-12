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
	entries := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := fmt.Sprintf("%s:%o:%d", filepath.ToSlash(relative), info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
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
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(path)
		return bytes.Contains(data, []byte(marker)), err
	}
	found := false
	err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			return err
		}
		found = found || bytes.Contains(data, []byte(marker))
		return nil
	})
	return found, err
}
