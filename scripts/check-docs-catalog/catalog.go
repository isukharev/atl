package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const catalogSchemaVersion = 1

type catalog struct {
	SchemaVersion int             `json:"schema_version"`
	Documents     []catalogEntry  `json:"documents"`
	Exclusions    []exclusionRule `json:"exclusions"`
}

type catalogEntry struct {
	Path            string   `json:"path"`
	Lane            string   `json:"lane"`
	Topic           string   `json:"topic"`
	LandingPage     string   `json:"landing_page"`
	Language        string   `json:"language"`
	TranslationOf   string   `json:"translation_of,omitempty"`
	RequiredAnchors []string `json:"required_anchors,omitempty"`
}

type exclusionRule struct {
	Path   string   `json:"path,omitempty"`
	Prefix string   `json:"prefix,omitempty"`
	Except []string `json:"except,omitempty"`
	Reason string   `json:"reason"`
}

type context7Config struct {
	Schema           string            `json:"$schema"`
	ProjectTitle     string            `json:"projectTitle"`
	Description      string            `json:"description"`
	Branch           string            `json:"branch"`
	URL              string            `json:"url"`
	PublicKey        string            `json:"public_key"`
	PreviousVersions []context7Version `json:"previousVersions"`
	Folders          []string          `json:"folders"`
	ExcludeFolders   []string          `json:"excludeFolders"`
	ExcludeFiles     []string          `json:"excludeFiles"`
	Rules            []string          `json:"rules"`
	Disallow         bool              `json:"disallow"`
	Redirect         string            `json:"redirect"`
}

type context7Version struct {
	Tag string `json:"tag"`
}

func validateRepository(root string) (report, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return report{}, fmt.Errorf("resolve repository root: %w", err)
	}
	root = filepath.Clean(root)
	loaded, err := loadCatalog(filepath.Join(root, "docs", "catalog.v1.json"))
	if err != nil {
		return report{}, err
	}
	tracked, err := trackedMarkdown(root)
	if err != nil {
		return report{}, err
	}
	result, entries, err := validateCatalogInventory(root, loaded, tracked)
	if err != nil {
		return result, err
	}
	links, parsed, err := validateDocumentLinks(root, loaded.Documents)
	result.Links = links
	if err != nil {
		return result, err
	}
	if err := validateRequiredAnchors(loaded.Documents, parsed); err != nil {
		return result, err
	}
	if err := validateLandingPages(loaded.Documents, parsed); err != nil {
		return result, err
	}
	if err := validateTranslations(loaded.Documents, parsed); err != nil {
		return result, err
	}
	if err := validateContext7(root, entries); err != nil {
		return result, err
	}
	return result, nil
}

func loadCatalog(path string) (catalog, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return catalog{}, fmt.Errorf("read documentation catalog: %w", err)
	}
	var value catalog
	if err := decodeStrict(body, &value); err != nil {
		return catalog{}, fmt.Errorf("decode documentation catalog: %w", err)
	}
	if value.SchemaVersion != catalogSchemaVersion || value.Documents == nil || value.Exclusions == nil {
		return catalog{}, errors.New("documentation catalog requires schema_version 1 and non-null documents/exclusions")
	}
	return value, nil
}

func decodeStrict(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func trackedMarkdown(root string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.md")
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate tracked Markdown: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := filepath.ToSlash(string(part))
		if !approvedMarkdownPath(path) {
			return nil, fmt.Errorf("tracked Markdown %q is outside the approved documentation roots", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func approvedMarkdownPath(path string) bool {
	if !strings.Contains(path, "/") {
		return true
	}
	for _, prefix := range []string{
		".agents/skills/", ".github/", "benchmarks/agent-eval/", "docs/", "internal/cli/testdata/",
		"internal/agenteval/interchange/agentskills/testdata/", "plugins/atl/skills/", "skills-src/", "skills/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func validateCatalogInventory(root string, value catalog, tracked []string) (report, map[string]catalogEntry, error) {
	result := report{Documents: len(value.Documents)}
	trackedSet := make(map[string]bool, len(tracked))
	for _, path := range tracked {
		trackedSet[path] = true
	}
	entries := make(map[string]catalogEntry, len(value.Documents))
	topics := map[string]string{}
	previous := ""
	for index, entry := range value.Documents {
		if _, exists := entries[entry.Path]; exists {
			return result, nil, fmt.Errorf("document path %q is duplicated", entry.Path)
		}
		if entry.Path <= previous {
			return result, nil, fmt.Errorf("documents must be sorted by canonical path at entry %d", index+1)
		}
		previous = entry.Path
		if !canonicalRelativePath(entry.Path) || !trackedSet[entry.Path] {
			return result, nil, fmt.Errorf("document entry %d has a stale or noncanonical path %q", index+1, entry.Path)
		}
		if entry.Topic == "" {
			return result, nil, fmt.Errorf("document %q has an empty topic", entry.Path)
		}
		if prior, exists := topics[entry.Topic]; exists {
			return result, nil, fmt.Errorf("topic %q is duplicated by %q and %q", entry.Topic, prior, entry.Path)
		}
		if !oneOf(entry.Lane, "start", "concepts", "reference", "operations", "maintainers") ||
			!oneOf(entry.Language, "en", "ru") {
			return result, nil, fmt.Errorf("document %q has an unsupported lane or language", entry.Path)
		}
		if err := requireCanonicalFile(root, entry.Path); err != nil {
			return result, nil, fmt.Errorf("document %q: %w", entry.Path, err)
		}
		entries[entry.Path] = entry
		topics[entry.Topic] = entry.Path
	}

	matchedRules := make([]int, len(value.Exclusions))
	previous = ""
	for index, rule := range value.Exclusions {
		key, err := validateExclusionRule(root, rule)
		if err != nil {
			return result, nil, fmt.Errorf("exclusion %d: %w", index+1, err)
		}
		if key <= previous {
			return result, nil, fmt.Errorf("exclusions must be sorted by canonical selector at entry %d", index+1)
		}
		previous = key
		for _, exception := range rule.Except {
			if !trackedSet[exception] {
				return result, nil, fmt.Errorf("exclusion %d has a stale except path %q", index+1, exception)
			}
		}
	}

	var problems []string
	for _, path := range tracked {
		classified := 0
		if _, ok := entries[path]; ok {
			classified++
		}
		for index, rule := range value.Exclusions {
			if ruleMatches(rule, path) {
				classified++
				matchedRules[index]++
			}
		}
		switch {
		case classified == 0:
			problems = append(problems, fmt.Sprintf("tracked Markdown %q is missing from documents and exclusions", path))
		case classified > 1:
			problems = append(problems, fmt.Sprintf("tracked Markdown %q is classified more than once", path))
		default:
			if _, maintained := entries[path]; !maintained {
				result.Excluded++
			}
		}
	}
	for index, count := range matchedRules {
		if count == 0 {
			problems = append(problems, fmt.Sprintf("exclusion %d is stale and matches no tracked Markdown", index+1))
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return result, nil, errors.New("documentation catalog inventory failed:\n- " + strings.Join(problems, "\n- "))
	}
	return result, entries, nil
}

func validateExclusionRule(root string, rule exclusionRule) (string, error) {
	if (rule.Path == "") == (rule.Prefix == "") {
		return "", errors.New("exactly one of path or prefix is required")
	}
	if !oneOf(rule.Reason, "template", "benchmark-material", "client-skill-source", "generated-client-skill", "repository-maintainer-skill", "testdata") {
		return "", errors.New("reason is not in the closed exclusion vocabulary")
	}
	if rule.Path != "" {
		if rule.Except != nil {
			return "", errors.New("except is allowed only with prefix")
		}
		if !canonicalRelativePath(rule.Path) {
			return "", errors.New("path is noncanonical")
		}
		if err := requireCanonicalFile(root, rule.Path); err != nil {
			return "", err
		}
		return "path:" + rule.Path, nil
	}
	if !strings.HasSuffix(rule.Prefix, "/") || !canonicalRelativePath(strings.TrimSuffix(rule.Prefix, "/")) {
		return "", errors.New("prefix is noncanonical")
	}
	if err := requireCanonicalDirectory(root, strings.TrimSuffix(rule.Prefix, "/")); err != nil {
		return "", err
	}
	if rule.Except == nil {
		rule.Except = []string{}
	}
	previous := ""
	for _, exception := range rule.Except {
		if exception <= previous || !canonicalRelativePath(exception) || !strings.HasPrefix(exception, rule.Prefix) {
			return "", errors.New("except paths must be sorted, canonical, and contained by prefix")
		}
		if err := requireCanonicalFile(root, exception); err != nil {
			return "", fmt.Errorf("except path %q: %w", exception, err)
		}
		previous = exception
	}
	return "prefix:" + rule.Prefix, nil
}

func ruleMatches(rule exclusionRule, path string) bool {
	if rule.Path != "" {
		return path == rule.Path
	}
	if !strings.HasPrefix(path, rule.Prefix) {
		return false
	}
	for _, exception := range rule.Except {
		if path == exception {
			return false
		}
	}
	return true
}

func canonicalRelativePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && !strings.Contains(path, "\\") &&
		path == filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) && path != "." &&
		path != ".." && !strings.HasPrefix(path, "../")
}

func requireCanonicalFile(root, relative string) error {
	path, err := canonicalPath(root, relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("target is not a regular non-symlink file")
	}
	return nil
}

func requireCanonicalDirectory(root, relative string) error {
	path, err := canonicalPath(root, relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("target is not a non-symlink directory")
	}
	return nil
}

func canonicalPath(root, relative string) (string, error) {
	if !canonicalRelativePath(relative) {
		return "", errors.New("noncanonical relative path")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if !within(root, target) {
		return "", errors.New("path escapes repository root")
	}
	current := root
	for _, component := range strings.Split(relative, "/") {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", errors.New("inspect path")
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == component {
				found = true
				break
			}
		}
		if !found {
			return "", errors.New("path case does not match the filesystem")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("path contains a symlink or missing component")
		}
	}
	return target, nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validateLandingPages(entries []catalogEntry, parsed map[string]markdownDocument) error {
	byPath := map[string]catalogEntry{}
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for _, entry := range entries {
		if entry.Path == "README.md" {
			if entry.LandingPage != "" {
				return errors.New("README.md is the only graph root and must have an empty landing_page")
			}
			continue
		}
		if !canonicalRelativePath(entry.LandingPage) {
			return fmt.Errorf("document %q has a missing or noncanonical landing_page", entry.Path)
		}
		if _, ok := byPath[entry.LandingPage]; !ok {
			return fmt.Errorf("document %q has an uncataloged landing_page %q", entry.Path, entry.LandingPage)
		}
		linked := false
		for _, link := range parsed[entry.LandingPage].Links {
			if link.Local && link.Relative && link.Path == entry.Path {
				linked = true
				break
			}
		}
		if !linked {
			return fmt.Errorf("landing_page %q does not contain a real relative link to %q", entry.LandingPage, entry.Path)
		}
	}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(path string) error {
		if path == "README.md" {
			return nil
		}
		if seen[path] {
			return fmt.Errorf("landing graph contains a cycle at %q", path)
		}
		seen[path] = true
		parent := byPath[path].LandingPage
		if err := visit(parent); err != nil {
			return err
		}
		delete(seen, path)
		return nil
	}
	for path := range byPath {
		if err := visit(path); err != nil {
			return err
		}
	}
	return nil
}

func validateTranslations(entries []catalogEntry, parsed map[string]markdownDocument) error {
	byPath := map[string]catalogEntry{}
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for _, entry := range entries {
		if entry.Language == "en" && entry.TranslationOf != "" {
			return fmt.Errorf("canonical English document %q cannot declare translation_of", entry.Path)
		}
		if entry.Language == "ru" && entry.TranslationOf == "" {
			return fmt.Errorf("russian document %q requires translation_of", entry.Path)
		}
		if entry.TranslationOf == "" {
			continue
		}
		target, ok := byPath[entry.TranslationOf]
		if !ok || target.Language != "en" || entry.Language != "ru" || target.TranslationOf != "" {
			return fmt.Errorf("translation %q does not point to one canonical English document", entry.Path)
		}
	}
	en, enOK := byPath["README.md"]
	ru, ruOK := byPath["README.ru.md"]
	if !enOK || !ruOK || en.Language != "en" || ru.Language != "ru" || ru.TranslationOf != "README.md" ||
		en.LandingPage != "" || ru.LandingPage != "README.md" {
		return errors.New("README English/Russian translation metadata is incomplete")
	}
	if !linksTo(parsed[en.Path], ru.Path) || !linksTo(parsed[ru.Path], en.Path) {
		return errors.New("README English/Russian language switch must be mutual")
	}
	enLinks := parityDestinations(parsed[en.Path], "README.ru.md")
	ruLinks := parityDestinations(parsed[ru.Path], "README.md")
	if len(enLinks) != len(ruLinks) {
		return errors.New("README English/Russian link parity differs")
	}
	for index := range enLinks {
		if enLinks[index] != ruLinks[index] {
			return errors.New("README English/Russian link parity differs")
		}
	}
	return nil
}

func linksTo(document markdownDocument, path string) bool {
	for _, link := range document.Links {
		if link.Local && link.Relative && link.Path == path && link.Fragment == "" {
			return true
		}
	}
	return false
}

func parityDestinations(document markdownDocument, counterpart string) []string {
	values := make([]string, 0, len(document.Links))
	for _, link := range document.Links {
		value := link.Destination
		if link.Local && link.Path == counterpart && link.Fragment == "" {
			value = "$translation-counterpart"
		}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func validateContext7(root string, entries map[string]catalogEntry) error {
	body, err := os.ReadFile(filepath.Join(root, "context7.json"))
	if err != nil {
		return fmt.Errorf("read context7.json: %w", err)
	}
	var config context7Config
	if err := decodeStrict(body, &config); err != nil {
		return fmt.Errorf("decode context7.json: %w", err)
	}
	excludedFiles := map[string]bool{}
	for _, name := range config.ExcludeFiles {
		excludedFiles[name] = true
	}
	selected := map[string]bool{}
	// Context7 considers root Markdown even when folders narrows the indexed
	// corpus. Model that implicit selection so a maintainer root document cannot
	// leak merely because an excludeFiles entry was removed.
	for path := range entries {
		if !strings.Contains(path, "/") && strings.EqualFold(filepath.Ext(path), ".md") &&
			!excludedFiles[filepath.Base(path)] {
			selected[path] = true
		}
	}
	for _, folder := range config.Folders {
		folder = filepath.ToSlash(filepath.Clean(filepath.FromSlash(folder)))
		if !canonicalRelativePath(folder) {
			return errors.New("context7.json contains a noncanonical folder")
		}
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(folder)), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				if relative != folder && context7ExcludedDirectory(relative, config.ExcludeFolders) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".md") && !excludedFiles[entry.Name()] {
				selected[relative] = true
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("enumerate Context7 selection: %w", err)
		}
	}
	for path := range selected {
		entry, ok := entries[path]
		if !ok {
			return fmt.Errorf("Context7 selects uncataloged Markdown %q", path)
		}
		if entry.Lane == "maintainers" {
			return fmt.Errorf("Context7 selects maintainer document %q", path)
		}
	}
	return nil
}

func context7ExcludedDirectory(path string, patterns []string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		rootSpecific := strings.HasPrefix(pattern, "./")
		pattern = strings.TrimPrefix(pattern, "./")
		if pattern == "" {
			continue
		}
		if path == pattern || strings.HasPrefix(path, pattern+"/") {
			return true
		}
		if !rootSpecific && !strings.Contains(pattern, "/") {
			for _, component := range strings.Split(path, "/") {
				if matched, _ := filepath.Match(pattern, component); matched {
					return true
				}
			}
		}
		if (rootSpecific || strings.Contains(pattern, "/")) && directoryGlobMatch(path, pattern) {
			return true
		}
	}
	return false
}

func directoryGlobMatch(path, pattern string) bool {
	var expression strings.Builder
	expression.WriteString("^")
	for index := 0; index < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[index:], "**/"):
			expression.WriteString("(?:.*/)?")
			index += 3
		case strings.HasPrefix(pattern[index:], "**"):
			expression.WriteString(".*")
			index += 2
		case pattern[index] == '*':
			expression.WriteString("[^/]*")
			index++
		case pattern[index] == '?':
			expression.WriteString("[^/]")
			index++
		default:
			expression.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	expression.WriteString("(?:/.*)?$")
	matched, err := regexp.MatchString(expression.String(), path)
	return err == nil && matched
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
