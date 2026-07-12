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
	"regexp"
	"sort"
	"strings"
)

type context7Config struct {
	Schema           string            `json:"$schema"`
	ProjectTitle     string            `json:"projectTitle"`
	Description      string            `json:"description"`
	Branch           string            `json:"branch"`
	Folders          []string          `json:"folders"`
	ExcludeFolders   []string          `json:"excludeFolders"`
	ExcludeFiles     []string          `json:"excludeFiles"`
	Rules            []string          `json:"rules"`
	Disallow         bool              `json:"disallow"`
	Redirect         string            `json:"redirect"`
	PreviousVersions []context7Version `json:"previousVersions"`
	URL              string            `json:"url"`
	PublicKey        string            `json:"public_key"`
}

type context7Version struct {
	Tag string `json:"tag"`
}

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type report struct {
	Documents int
	Snippets  int
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	report, err := validateRepository(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Context7 docs: %d selected documents, %d named fenced snippets\n", report.Documents, report.Snippets)
}

func validateRepository(root string) (report, error) {
	report, err := validate(root)
	if err != nil {
		return report, err
	}
	if err := validateAutomation(root); err != nil {
		return report, err
	}
	return report, nil
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
	if cfg.Branch != "stable" {
		return report{}, errors.New("context7.json must parse the release-bound stable branch")
	}
	if len(cfg.PreviousVersions) == 0 || len(cfg.PreviousVersions) > 20 {
		return report{}, errors.New("context7.json must expose between 1 and 20 release tag versions")
	}
	versionBytes, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return report{}, fmt.Errorf("read VERSION: %w", err)
	}
	wantCurrentTag := "v" + strings.TrimSpace(string(versionBytes))
	seenVersions := map[string]bool{}
	for i, version := range cfg.PreviousVersions {
		if !releaseTagPattern.MatchString(version.Tag) {
			return report{}, fmt.Errorf("context7.json previousVersions[%d] has invalid release tag %q", i, version.Tag)
		}
		if seenVersions[version.Tag] {
			return report{}, fmt.Errorf("context7.json repeats release tag %q", version.Tag)
		}
		seenVersions[version.Tag] = true
	}
	if cfg.PreviousVersions[0].Tag != wantCurrentTag {
		return report{}, fmt.Errorf("context7.json newest version must be %q from VERSION", wantCurrentTag)
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
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		rootSpecific := strings.HasPrefix(pattern, "./")
		pattern = strings.TrimPrefix(pattern, "./")
		if pattern == "" {
			continue
		}
		if path == pattern || strings.HasPrefix(path, pattern+"/") {
			return true
		}
		// Simple names match a directory at any depth, matching Context7's
		// documented exclusion semantics.
		if !rootSpecific && !strings.Contains(pattern, "/") {
			for _, segment := range strings.Split(path, "/") {
				matched, matchErr := filepath.Match(pattern, segment)
				if matchErr == nil && matched {
					return true
				}
			}
		}
		// Path-shaped and root-specific glob patterns are anchored at the
		// repository root. The leading ./ changes scope; it is not discarded
		// into the simple-name any-depth behavior above.
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

func validateAutomation(root string) error {
	read := func(path string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	require := func(path, scope, content string, fragments ...string) error {
		content = yamlActiveContent(content)
		for _, fragment := range fragments {
			if !strings.Contains(content, fragment) {
				return fmt.Errorf("%s %s must contain %q", path, scope, fragment)
			}
		}
		return nil
	}
	releasePath := ".github/workflows/release.yml"
	release, err := read(releasePath)
	if err != nil {
		return err
	}
	releaseJob, err := yamlChildBlock(release, "jobs", "refresh-context7")
	if err != nil {
		return fmt.Errorf("%s: %w", releasePath, err)
	}
	if err := require(releasePath, "job refresh-context7", releaseJob,
		"environment: context7", "refs/heads/stable", "secrets.CONTEXT7_API_KEY",
		"https://context7.com/api/v1/refresh", "continue-on-error: true"); err != nil {
		return err
	}
	manualPath := ".github/workflows/context7-refresh.yml"
	manual, err := read(manualPath)
	if err != nil {
		return err
	}
	trigger, err := yamlChildBlock(manual, "on", "workflow_dispatch")
	if err != nil {
		return fmt.Errorf("%s: %w", manualPath, err)
	}
	if err := require(manualPath, "trigger workflow_dispatch", trigger, "workflow_dispatch:"); err != nil {
		return err
	}
	manualJob, err := yamlChildBlock(manual, "jobs", "refresh")
	if err != nil {
		return fmt.Errorf("%s: %w", manualPath, err)
	}
	return require(manualPath, "job refresh", manualJob,
		"environment: context7", "secrets.CONTEXT7_API_KEY",
		"https://context7.com/api/v1/refresh")
}

func yamlActiveContent(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		single, double, escaped := false, false, false
		for offset, char := range line {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' && double {
				escaped = true
				continue
			}
			if char == '\'' && !double {
				single = !single
				continue
			}
			if char == '"' && !single {
				double = !double
				continue
			}
			if char == '#' && !single && !double {
				lines[index] = line[:offset]
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// yamlChildBlock returns one direct child's indentation-bounded YAML block.
// Workflow validation deliberately needs only this small structural boundary:
// fragments in sibling jobs must never satisfy a target job's controls.
func yamlChildBlock(content, parent, child string) (string, error) {
	lines := strings.Split(content, "\n")
	parentLine, parentIndent := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leadingSpaces(line) == 0 && strings.TrimSuffix(trimmed, ":") == parent && strings.HasSuffix(trimmed, ":") {
			parentLine, parentIndent = i, leadingSpaces(line)
			break
		}
	}
	if parentLine < 0 {
		return "", fmt.Errorf("missing YAML mapping %q", parent)
	}
	childLine, childIndent, directIndent := -1, -1, -1
	for i := parentLine + 1; i < len(lines); i++ {
		line, trimmed := lines[i], strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= parentIndent {
			break
		}
		if directIndent < 0 {
			directIndent = indent
		}
		if indent == directIndent && strings.TrimSuffix(trimmed, ":") == child && strings.HasSuffix(trimmed, ":") {
			childLine, childIndent = i, indent
			break
		}
	}
	if childLine < 0 {
		return "", fmt.Errorf("YAML mapping %q is missing child %q", parent, child)
	}
	end := len(lines)
	for i := childLine + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leadingSpaces(lines[i]) <= childIndent {
			end = i
			break
		}
	}
	return strings.Join(lines[childLine:end], "\n"), nil
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
