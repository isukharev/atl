//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRunRejectsSpecialSourceFileBeforeRemovingOutputs(t *testing.T) {
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
	referenceRoot := filepath.Join(srcRoot, "demo", "reference")
	if err := os.MkdirAll(referenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(referenceRoot, "special.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGeneratorSentinels(t)

	if err := run(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("special source file passed: %v", err)
	}
	assertGeneratorSentinels(t)
}

func TestRunRejectsOutputPathReplacementBeforeSuccess(t *testing.T) {
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

	beforeOutputIdentityRebind = func(platformName string) {
		if platformName != "claude" {
			return
		}
		beforeOutputIdentityRebind = nil
		if err := os.Rename("skills", "detached-skills"); err != nil {
			t.Fatalf("detach output: %v", err)
		}
		if err := os.Mkdir("skills", 0o700); err != nil {
			t.Fatalf("replace output: %v", err)
		}
	}
	t.Cleanup(func() { beforeOutputIdentityRebind = nil })
	if err := run(); err == nil || !strings.Contains(err.Error(), "directory changed during publication") {
		t.Fatalf("replaced output path passed: %v", err)
	}
}

func TestRunRebindsEveryOutputAfterAllPlatformsPublish(t *testing.T) {
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

	beforeOutputIdentityRebind = func(platformName string) {
		if platformName != "codex" {
			return
		}
		beforeOutputIdentityRebind = nil
		if err := os.Rename("skills", "detached-skills"); err != nil {
			t.Fatalf("detach earlier output: %v", err)
		}
		if err := os.Mkdir("skills", 0o700); err != nil {
			t.Fatalf("replace earlier output: %v", err)
		}
	}
	t.Cleanup(func() { beforeOutputIdentityRebind = nil })
	if err := run(); err == nil || !strings.Contains(err.Error(), "directory changed after publication") {
		t.Fatalf("earlier output replacement passed: %v", err)
	}
}
