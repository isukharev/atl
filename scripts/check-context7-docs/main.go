// Command check-context7-docs validates the repository's Context7 parsing
// boundary and snippet density without calling Context7 or requiring a token.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type context7Config struct {
	Schema           string          `json:"$schema"`
	ProjectTitle     string          `json:"projectTitle"`
	Description      string          `json:"description"`
	Branch           string          `json:"branch"`
	Folders          []string        `json:"folders"`
	ExcludeFolders   []string        `json:"excludeFolders"`
	ExcludeFiles     []string        `json:"excludeFiles"`
	Rules            []string        `json:"rules"`
	Disallow         bool            `json:"disallow"`
	Redirect         string          `json:"redirect"`
	PreviousVersions json.RawMessage `json:"previousVersions"`
	URL              string          `json:"url"`
	PublicKey        string          `json:"public_key"`
}

type report struct {
	Documents int
	Snippets  int
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	report, err := validate(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Context7 docs: %d selected documents, %d named fenced snippets\n", report.Documents, report.Snippets)
}

func validate(root string) (report, error) {
	configPath := filepath.Join(root, "context7.json")
	b, err := os.ReadFile(configPath)
	if err != nil {
		return report{}, err
	}
	var cfg context7Config
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return report{}, fmt.Errorf("decode context7.json: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return report{}, errors.New("context7.json must contain exactly one JSON value")
	}
	if cfg.Schema != "https://context7.com/schema/context7.json" {
		return report{}, errors.New("context7.json must declare the official schema")
	}
	if !contains(cfg.Folders, "docs") {
		return report{}, errors.New("context7.json must include the maintained docs folder")
	}
	if len(cfg.Rules) == 0 {
		return report{}, errors.New("context7.json must carry agent-facing rules")
	}
	if cfg.URL != "https://context7.com/isukharev/atl" {
		return report{}, errors.New("context7.json must carry the canonical Context7 ownership URL")
	}
	if !strings.HasPrefix(cfg.PublicKey, "pk_") || len(cfg.PublicKey) <= len("pk_") {
		return report{}, errors.New("context7.json must carry the public ownership verifier")
	}

	excludedFiles := stringSet(cfg.ExcludeFiles)
	var problems []string
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return report{}, err
	}
	selected := []string{}
	for _, entry := range rootEntries {
		if entry.IsDir() || !isMarkdown(entry.Name()) {
			continue
		}
		if entry.Name() == "README.md" {
			selected = append(selected, filepath.Join(root, entry.Name()))
			continue
		}
		if !excludedFiles[entry.Name()] {
			problems = append(problems, fmt.Sprintf("root Markdown %s is implicitly indexed by Context7; add it to excludeFiles or explicitly approve it", entry.Name()))
		}
	}

	docsRoot := filepath.Join(root, "docs")
	err = filepath.WalkDir(docsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "docs" && excludedDirectory(rel, cfg.ExcludeFolders) {
				return filepath.SkipDir
			}
			return nil
		}
		if isContext7Document(entry.Name()) && !excludedFiles[entry.Name()] {
			if !isMarkdown(entry.Name()) {
				problems = append(problems, fmt.Sprintf("selected document %s is not Markdown; extend the local snippet validator or exclude it explicitly", rel))
				return nil
			}
			selected = append(selected, path)
		}
		return nil
	})
	if err != nil {
		return report{}, err
	}

	sort.Strings(selected)
	result := report{Documents: len(selected)}
	selectedRelative := map[string]bool{}
	for _, path := range selected {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		selectedRelative[rel] = true
		count, err := namedFencedSnippets(path)
		if err != nil {
			return report{}, err
		}
		if count == 0 {
			problems = append(problems, fmt.Sprintf("selected document %s has no non-empty named fenced snippet", rel))
		}
		if rel == "docs/agent-recipes.md" && count < 8 {
			problems = append(problems, fmt.Sprintf("selected document %s has only %d named snippets; keep the high-frequency recipe corpus task-complete", rel, count))
		}
		result.Snippets += count
	}
	for _, required := range []string{
		"README.md", "docs/README.md", "docs/agent-recipes.md",
		"docs/usage.md", "docs/OUTPUT_CONTRACT.md", "docs/csf-and-fragments.md",
		"docs/jira-guarded-writeback.md", "docs/self-update.md",
	} {
		if !selectedRelative[required] {
			problems = append(problems, fmt.Sprintf("required runtime document %s is not selected", required))
		}
	}
	if len(problems) > 0 {
		return report{}, errors.New("Context7 documentation check failed:\n- " + strings.Join(problems, "\n- "))
	}
	return result, nil
}

func namedFencedSnippets(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	// Documentation examples can be large JSON fixtures; keep the check bounded
	// but well above bufio.Scanner's small default token limit.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	inside, named, content := false, false, false
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !inside && strings.HasPrefix(line, "```") {
			inside = true
			named = strings.TrimSpace(strings.TrimPrefix(line, "```")) != ""
			content = false
			continue
		}
		if inside && line == "```" {
			if named && content {
				count++
			}
			inside, named, content = false, false, false
			continue
		}
		if inside && line != "" {
			content = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func excludedDirectory(path string, patterns []string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	for _, pattern := range patterns {
		pattern = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pattern)), "./")
		if pattern == "" {
			continue
		}
		if path == pattern || strings.HasPrefix(path, pattern+"/") {
			return true
		}
		// Simple names match a directory at any depth, matching Context7's
		// documented exclusion semantics.
		if !strings.Contains(pattern, "/") {
			for _, segment := range strings.Split(path, "/") {
				matched, matchErr := filepath.Match(pattern, segment)
				if matchErr == nil && matched {
					return true
				}
			}
		}
	}
	return false
}

func isMarkdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".mdx" || ext == ".markdown"
}

func isContext7Document(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".mdx", ".markdown", ".rst", ".txt", ".ipynb", ".html", ".htm":
		return true
	default:
		return false
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
