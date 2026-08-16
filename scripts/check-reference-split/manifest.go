package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestSchemaVersion             = 1
	permanentHistoricalRouteCount     = 152
	permanentHistoricalRouteInventory = "f1a150946ef86b40d1617ec5cd9155a5b9f72608baa19031a8d315d51f7ca42b"
)

type historicalInventory struct {
	Routes int
	SHA256 string
}

type historicalRoute struct {
	LegacyPath   string `json:"legacy_path"`
	HeadingText  string `json:"heading_text"`
	HeadingLevel int    `json:"heading_level"`
	SourceOrder  int    `json:"source_order"`
}

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

func historicalInventoryForRoutes(routes []route) historicalInventory {
	entries := make([]historicalRoute, 0, len(routes))
	for _, item := range routes {
		entries = append(entries, historicalRoute{
			LegacyPath: item.LegacyPath, HeadingText: item.HeadingText,
			HeadingLevel: item.HeadingLevel, SourceOrder: item.SourceOrder,
		})
	}
	body, err := json.Marshal(entries)
	if err != nil {
		panic("historical route inventory contains only JSON-safe scalar fields")
	}
	digest := sha256.Sum256(body)
	return historicalInventory{Routes: len(entries), SHA256: hex.EncodeToString(digest[:])}
}

func validateHistoricalInventory(routes []route, expected historicalInventory) error {
	actual := historicalInventoryForRoutes(routes)
	if actual.Routes != expected.Routes {
		return fmt.Errorf("historical route inventory changed: got %d routes, want the permanent %d-route baseline",
			actual.Routes, expected.Routes)
	}
	if actual.SHA256 != expected.SHA256 {
		return errors.New("historical route identity changed; published legacy paths, headings, levels, and source order are immutable")
	}
	return nil
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
