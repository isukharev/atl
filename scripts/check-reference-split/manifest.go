package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const manifestSchemaVersion = 1

type splitManifest struct {
	SchemaVersion int     `json:"schema_version"`
	Routes        []route `json:"routes"`
}

type route struct {
	LegacyPath        string `json:"legacy_path"`
	HeadingText       string `json:"heading_text"`
	HeadingLevel      int    `json:"heading_level"`
	SourceOrder       int    `json:"source_order"`
	DestinationPath   string `json:"destination_path"`
	DestinationAnchor string `json:"destination_anchor"`
}

func loadManifest(path string) (splitManifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return splitManifest{}, fmt.Errorf("read reference split manifest: %w", err)
	}
	var value splitManifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return splitManifest{}, fmt.Errorf("decode reference split manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return splitManifest{}, errors.New("reference split manifest contains multiple JSON values")
		}
		return splitManifest{}, fmt.Errorf("decode trailing reference split manifest data: %w", err)
	}
	if value.SchemaVersion != manifestSchemaVersion || value.Routes == nil {
		return splitManifest{}, errors.New("reference split manifest requires schema_version 1 and a non-null routes array")
	}
	if len(value.Routes) == 0 {
		return splitManifest{}, errors.New("reference split manifest requires at least one route")
	}
	return value, nil
}

func validateManifest(root string, value splitManifest) (map[string][]route, error) {
	previousPath := ""
	expectedOrder := 0
	for index, item := range value.Routes {
		if item.LegacyPath < previousPath {
			return nil, fmt.Errorf("manifest routes must be sorted by legacy_path and source_order at route %d", index+1)
		}
		if item.LegacyPath != previousPath {
			previousPath = item.LegacyPath
			expectedOrder = 1
		} else {
			expectedOrder++
		}
		if item.SourceOrder != expectedOrder {
			return nil, fmt.Errorf("route %d for %q has source_order %d, want contiguous order %d",
				index+1, item.LegacyPath, item.SourceOrder, expectedOrder)
		}
	}

	groups := make(map[string][]route)
	for index, item := range value.Routes {
		if err := validateRoute(root, item); err != nil {
			return nil, fmt.Errorf("route %d: %w", index+1, err)
		}
		groups[item.LegacyPath] = append(groups[item.LegacyPath], item)
	}
	return groups, nil
}

func validateRoute(root string, item route) error {
	if !canonicalMarkdownPath(item.LegacyPath) {
		return fmt.Errorf("legacy_path %q is not a canonical Markdown path", item.LegacyPath)
	}
	if !canonicalMarkdownPath(item.DestinationPath) {
		return fmt.Errorf("destination_path %q is not a canonical Markdown path", item.DestinationPath)
	}
	if item.LegacyPath == item.DestinationPath {
		return errors.New("legacy_path and destination_path must differ")
	}
	if err := requireCanonicalFile(root, item.LegacyPath); err != nil {
		return fmt.Errorf("legacy_path %q: %w", item.LegacyPath, err)
	}
	if err := requireCanonicalFile(root, item.DestinationPath); err != nil {
		return fmt.Errorf("destination_path %q: %w", item.DestinationPath, err)
	}
	if item.HeadingLevel < 1 || item.HeadingLevel > 6 {
		return fmt.Errorf("heading_level %d is outside 1..6", item.HeadingLevel)
	}
	if item.HeadingText == "" || strings.TrimSpace(item.HeadingText) != item.HeadingText ||
		strings.ContainsAny(item.HeadingText, "\r\n") {
		return errors.New("heading_text must be non-empty, single-line, and have no surrounding whitespace")
	}
	line := strings.Repeat("#", item.HeadingLevel) + " " + item.HeadingText
	level, text, ok := parseATXHeading(line)
	if !ok || level != item.HeadingLevel || text != item.HeadingText {
		return fmt.Errorf("heading_text %q does not round-trip as an exact ATX heading", item.HeadingText)
	}
	if githubHeadingSlug(item.HeadingText) == "" {
		return fmt.Errorf("heading_text %q has no stable GitHub anchor", item.HeadingText)
	}
	if item.DestinationAnchor == "" || strings.TrimSpace(item.DestinationAnchor) != item.DestinationAnchor ||
		strings.ContainsAny(item.DestinationAnchor, "#?\r\n") {
		return errors.New("destination_anchor must be a non-empty decoded anchor without surrounding whitespace, #, or ?")
	}
	return nil
}

func canonicalMarkdownPath(path string) bool {
	return canonicalRelativePath(path) && filepath.Ext(path) == ".md"
}

func canonicalRelativePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && !strings.Contains(path, "\\") &&
		path == filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) && path != "." &&
		path != ".." && !strings.HasPrefix(path, "../")
}

func requireCanonicalFile(root, relative string) error {
	if !canonicalRelativePath(relative) {
		return errors.New("noncanonical relative path")
	}
	current := root
	for _, component := range strings.Split(relative, "/") {
		entries, err := os.ReadDir(current)
		if err != nil {
			return errors.New("inspect path")
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == component {
				found = true
				break
			}
		}
		if !found {
			return errors.New("path case does not match the filesystem")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path contains a symlink or missing component")
		}
	}
	info, err := os.Lstat(current)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("target is not a regular file")
	}
	return nil
}
