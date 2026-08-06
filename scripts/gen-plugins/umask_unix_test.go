//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRunNormalizesGeneratedModesUnderRestrictiveUmask(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	writeValidGeneratorSkill(t)
	if err := os.MkdirAll(filepath.Join("plugins", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}

	previousUmask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previousUmask) })
	if err := run(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join("skills", "demo", "SKILL.md"),
		filepath.Join("plugins", "atl", "skills", "demo", "SKILL.md"),
		codexSkillCatalogPath,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("generated mode for %s = %v, err=%v", path, info, err)
		}
	}
}
