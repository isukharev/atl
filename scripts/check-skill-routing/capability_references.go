package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/capability"
)

func validateCapabilityReferences(sourcePath string, definitions []capability.Definition) error {
	rootInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect capability skill source root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("capability skill source root must be a plain directory")
	}
	root, err := os.OpenRoot(sourcePath)
	if err != nil {
		return fmt.Errorf("open capability skill source root: %w", err)
	}
	defer func() { _ = root.Close() }()
	openedInfo, err := root.Stat(".")
	if err != nil || !openedInfo.IsDir() {
		return fmt.Errorf("capability skill source root changed while it was opened")
	}

	for _, definition := range definitions {
		if definition.ID == "" {
			return fmt.Errorf("capability definition has no id")
		}
		if err := validateCapabilitySkillName(definition.Skill); err != nil {
			return fmt.Errorf("capability %s skill: %w", definition.ID, err)
		}
		if err := validateCapabilityReference(definition.Reference); err != nil {
			return fmt.Errorf("capability %s reference: %w", definition.ID, err)
		}
		if err := validateContainedRegularFile(root, path.Join(definition.Skill, "SKILL.md")); err != nil {
			return fmt.Errorf("capability %s skill %q: %w", definition.ID, definition.Skill, err)
		}
		if err := validateContainedRegularFile(root, path.Join(definition.Skill, definition.Reference)); err != nil {
			return fmt.Errorf("capability %s reference %q: %w", definition.ID, definition.Reference, err)
		}
	}
	return nil
}

func validateCapabilitySkillName(value string) error {
	if value == "" || value == "." || value == ".." || path.Clean(value) != value ||
		strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("%q is not one canonical source skill name", value)
	}
	return nil
}

func validateCapabilityReference(value string) error {
	if value == "" || value == "." || value == ".." || path.IsAbs(value) ||
		path.Clean(value) != value || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") {
		return fmt.Errorf("%q is not a contained canonical source reference", value)
	}
	if path.Ext(value) != ".md" {
		return fmt.Errorf("%q is not a Markdown source reference", value)
	}
	return nil
}

func validateContainedRegularFile(root *os.Root, relative string) error {
	clean := path.Clean(relative)
	if clean != relative || clean == "." || clean == ".." || path.IsAbs(clean) ||
		strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
		return fmt.Errorf("source path is not contained")
	}
	components := strings.Split(clean, "/")
	current := ""
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("source path has an invalid component")
		}
		current = path.Join(current, component)
		info, err := root.Lstat(filepath.FromSlash(current))
		if err != nil {
			return fmt.Errorf("source file is unavailable: %w", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("source path contains a symbolic link")
		}
		if index == len(components)-1 {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("source target is not a regular file")
			}
		} else if !info.IsDir() {
			return fmt.Errorf("source path parent is not a directory")
		}
	}
	return nil
}
