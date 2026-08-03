// Command check-repository-skills validates ATL's repository-only skill and
// maintainer-instruction boundary.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	catalogPath    = ".agents/skills/catalog.v1.json"
	maxAgentsBytes = 12 * 1024
	maxClaudeBytes = 4 * 1024
	maxSkillBytes  = 4 * 1024
	maxSkillLines  = 80
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type catalog struct {
	SchemaVersion int         `json:"schema_version"`
	Skills        []skillSpec `json:"skills"`
}

type skillSpec struct {
	Name                     string             `json:"name"`
	Runbook                  string             `json:"runbook"`
	RequiredDescriptionTerms []string           `json:"required_description_terms"`
	ActivationExamples       activationExamples `json:"activation_examples"`
}

type activationExamples struct {
	ShouldActivate    []string `json:"should_activate"`
	ShouldNotActivate []string `json:"should_not_activate"`
}

type docsCatalog struct {
	SchemaVersion int             `json:"schema_version"`
	Documents     []docsEntry     `json:"documents"`
	Exclusions    []docsExclusion `json:"exclusions"`
}

type docsEntry struct {
	Path string `json:"path"`
	Lane string `json:"lane"`
}

type docsExclusion struct {
	Path   string   `json:"path,omitempty"`
	Prefix string   `json:"prefix,omitempty"`
	Except []string `json:"except,omitempty"`
	Reason string   `json:"reason"`
}

type report struct {
	Skills   int
	Runbooks int
	Bytes    int
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-repository-skills: unexpected arguments")
		os.Exit(1)
	}
	result, err := validateRepository(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-repository-skills:", err)
		os.Exit(1)
	}
	fmt.Printf("repository guidance: %d skills, %d runbooks, %d instruction bytes\n",
		result.Skills, result.Runbooks, result.Bytes)
}

func validateRepository(root string) (report, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return report{}, fmt.Errorf("resolve root: %w", err)
	}
	root = filepath.Clean(root)

	value, err := loadCatalog(filepath.Join(root, filepath.FromSlash(catalogPath)))
	if err != nil {
		return report{}, err
	}
	if err := validateCatalog(value); err != nil {
		return report{}, err
	}
	docs, err := loadDocsCatalog(filepath.Join(root, "docs", "catalog.v1.json"))
	if err != nil {
		return report{}, err
	}
	if err := validateDocsBoundary(docs, value); err != nil {
		return report{}, err
	}

	result := report{Skills: len(value.Skills), Runbooks: len(value.Skills)}
	if err := validateSkillRoot(root, value); err != nil {
		return result, err
	}
	for _, spec := range value.Skills {
		bytes, err := validateSkill(root, spec)
		if err != nil {
			return result, fmt.Errorf("skill %s: %w", spec.Name, err)
		}
		result.Bytes += bytes
	}
	instructionBytes, err := validateInstructions(root, value)
	if err != nil {
		return result, err
	}
	result.Bytes += instructionBytes
	return result, nil
}

func loadCatalog(path string) (catalog, error) {
	var value catalog
	if _, err := readRegular(path); err != nil {
		return value, fmt.Errorf("load repository skill catalog: %w", err)
	}
	if err := decodeStrict(path, &value); err != nil {
		return value, fmt.Errorf("load repository skill catalog: %w", err)
	}
	if value.SchemaVersion != 1 || value.Skills == nil {
		return value, errors.New("repository skill catalog requires schema_version 1 and a non-null skills array")
	}
	return value, nil
}

func loadDocsCatalog(path string) (docsCatalog, error) {
	var value docsCatalog
	body, err := os.ReadFile(path)
	if err != nil {
		return value, fmt.Errorf("load documentation catalog: %w", err)
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return value, fmt.Errorf("load documentation catalog: %w", err)
	}
	if value.SchemaVersion != 1 || value.Documents == nil || value.Exclusions == nil {
		return value, errors.New("documentation catalog has an unsupported or incomplete schema")
	}
	return value, nil
}

func decodeStrict(path string, target any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
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

func validateCatalog(value catalog) error {
	if len(value.Skills) == 0 {
		return errors.New("repository skill catalog is empty")
	}
	previous := ""
	for index, spec := range value.Skills {
		if !skillNamePattern.MatchString(spec.Name) || spec.Name <= previous {
			return fmt.Errorf("catalog skill %d has an invalid or unsorted name", index+1)
		}
		previous = spec.Name
		if !canonicalRunbook(spec.Runbook) || len(spec.RequiredDescriptionTerms) == 0 {
			return fmt.Errorf("catalog skill %s has an invalid runbook or empty trigger terms", spec.Name)
		}
		seen := map[string]bool{}
		for _, term := range spec.RequiredDescriptionTerms {
			if strings.TrimSpace(term) != term || term == "" || seen[strings.ToLower(term)] {
				return fmt.Errorf("catalog skill %s has an invalid trigger term", spec.Name)
			}
			seen[strings.ToLower(term)] = true
		}
		if err := validateActivationExamples(spec.ActivationExamples); err != nil {
			return fmt.Errorf("catalog skill %s has invalid activation examples: %w", spec.Name, err)
		}
	}
	return nil
}

func validateActivationExamples(examples activationExamples) error {
	if len(examples.ShouldActivate) < 2 || len(examples.ShouldNotActivate) < 2 {
		return errors.New("at least two positive and two negative tasks are required")
	}
	seen := map[string]bool{}
	for _, tasks := range [][]string{examples.ShouldActivate, examples.ShouldNotActivate} {
		for _, task := range tasks {
			if strings.TrimSpace(task) != task || len(task) < 20 || len(task) > 200 || seen[strings.ToLower(task)] {
				return errors.New("tasks must be unique, trimmed, and contain 20 to 200 bytes")
			}
			seen[strings.ToLower(task)] = true
		}
	}
	return nil
}

func canonicalRunbook(path string) bool {
	return strings.HasPrefix(path, "docs/maintainers/") && filepath.Ext(path) == ".md" &&
		path == filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) && !strings.Contains(path, "\\")
}

func validateDocsBoundary(value docsCatalog, skills catalog) error {
	maintained := map[string]docsEntry{}
	for _, entry := range value.Documents {
		maintained[entry.Path] = entry
	}
	for _, spec := range skills.Skills {
		entry, ok := maintained[spec.Runbook]
		if !ok {
			return fmt.Errorf("runbook %s is absent from docs/catalog.v1.json", spec.Runbook)
		}
		if entry.Lane != "maintainers" {
			return fmt.Errorf("runbook %s must remain in the maintainers documentation lane", spec.Runbook)
		}
	}
	for _, exclusion := range value.Exclusions {
		if exclusion.Prefix == ".agents/skills/" && exclusion.Path == "" && len(exclusion.Except) == 0 && exclusion.Reason == "repository-maintainer-skill" {
			return nil
		}
	}
	return errors.New("docs catalog must explicitly exclude repository-maintainer skills")
}

func validateSkillRoot(root string, value catalog) error {
	directory := filepath.Join(root, ".agents", "skills")
	if err := requireDirectory(directory); err != nil {
		return fmt.Errorf("skill root: %w", err)
	}
	expected := map[string]bool{"catalog.v1.json": true}
	for _, spec := range value.Skills {
		expected[spec.Name] = true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return fmt.Errorf("undeclared repository skill entry %s", entry.Name())
		}
		delete(expected, entry.Name())
	}
	if len(expected) != 0 {
		return errors.New("repository skill catalog references a missing entry")
	}
	return nil
}

func validateSkill(root string, spec skillSpec) (int, error) {
	directory := filepath.Join(root, ".agents", "skills", spec.Name)
	if err := requireDirectory(directory); err != nil {
		return 0, err
	}
	if err := validateExactTree(directory); err != nil {
		return 0, err
	}
	skillPath := filepath.Join(directory, "SKILL.md")
	body, err := readRegular(skillPath)
	if err != nil {
		return 0, err
	}
	if len(body) > maxSkillBytes || bytes.Count(body, []byte{'\n'})+1 > maxSkillLines {
		return 0, errors.New("SKILL.md exceeds the repository context budget")
	}
	name, description, instructions, err := parseSkill(body)
	if err != nil {
		return 0, err
	}
	if name != spec.Name {
		return 0, errors.New("frontmatter name does not match its directory")
	}
	for _, term := range spec.RequiredDescriptionTerms {
		if !strings.Contains(strings.ToLower(description), strings.ToLower(term)) {
			return 0, fmt.Errorf("description is missing activation boundary term %q", term)
		}
	}
	if strings.Contains(string(body), "TODO") {
		return 0, errors.New("skill contains an unfinished TODO")
	}
	expectedLink, err := filepath.Rel(directory, filepath.Join(root, filepath.FromSlash(spec.Runbook)))
	if err != nil {
		return 0, err
	}
	expectedLink = filepath.ToSlash(expectedLink)
	if !strings.Contains(instructions, "]("+expectedLink+")") {
		return 0, fmt.Errorf("skill does not link its canonical runbook with %s", expectedLink)
	}
	if _, err := readRegular(filepath.Join(root, filepath.FromSlash(spec.Runbook))); err != nil {
		return 0, fmt.Errorf("runbook: %w", err)
	}
	if err := validateOpenAIYAML(filepath.Join(directory, "agents", "openai.yaml"), spec.Name); err != nil {
		return 0, err
	}
	for _, generatedRoot := range []string{"skills-src", "skills", filepath.Join("plugins", "atl", "skills")} {
		if _, err := os.Lstat(filepath.Join(root, generatedRoot, spec.Name)); err == nil {
			return 0, fmt.Errorf("repository skill collides with shipped client tree %s", generatedRoot)
		} else if !os.IsNotExist(err) {
			return 0, fmt.Errorf("inspect shipped client tree %s: %w", generatedRoot, err)
		}
	}
	return len(body), nil
}

func validateExactTree(root string) error {
	want := map[string]string{
		".": "dir", "SKILL.md": "file", "agents": "dir", "agents/openai.yaml": "file",
	}
	seen := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		kind, ok := want[relative]
		if !ok {
			return fmt.Errorf("unexpected skill path %s", relative)
		}
		if info.Mode()&os.ModeSymlink != 0 || kind == "dir" && !info.IsDir() || kind == "file" && !info.Mode().IsRegular() {
			return fmt.Errorf("invalid skill path type %s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(want) {
		return errors.New("skill tree is incomplete")
	}
	return nil
}

func parseSkill(body []byte) (string, string, string, error) {
	if !bytes.HasPrefix(body, []byte("---\n")) {
		return "", "", "", errors.New("SKILL.md is missing opening frontmatter")
	}
	end := bytes.Index(body[4:], []byte("\n---\n"))
	if end < 0 {
		return "", "", "", errors.New("SKILL.md is missing closing frontmatter")
	}
	frontmatter := string(body[4 : 4+end])
	instructions := string(body[4+end+5:])
	fields := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(frontmatter))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 || !oneOf(parts[0], "name", "description") || strings.TrimSpace(parts[1]) == "" || fields[parts[0]] != "" {
			return "", "", "", errors.New("SKILL.md frontmatter must contain only one name and description")
		}
		fields[parts[0]] = strings.TrimSpace(parts[1])
	}
	if err := scanner.Err(); err != nil || fields["name"] == "" || fields["description"] == "" || strings.TrimSpace(instructions) == "" {
		return "", "", "", errors.New("SKILL.md has incomplete frontmatter or instructions")
	}
	return fields["name"], fields["description"], instructions, nil
}

func validateOpenAIYAML(path, name string) error {
	body, err := readRegular(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != 4 || lines[0] != "interface:" {
		return errors.New("agents/openai.yaml must contain one four-line interface block")
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 || !oneOf(parts[0], "display_name", "short_description", "default_prompt") || values[parts[0]] != "" {
			return errors.New("agents/openai.yaml has an unexpected interface field")
		}
		var value string
		if err := json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &value); err != nil || value == "" {
			return errors.New("agents/openai.yaml values must be non-empty quoted strings")
		}
		values[parts[0]] = value
	}
	if utf8.RuneCountInString(values["short_description"]) < 25 || utf8.RuneCountInString(values["short_description"]) > 64 {
		return errors.New("short_description must contain 25 to 64 characters")
	}
	if !strings.Contains(values["default_prompt"], "$"+name) {
		return errors.New("default_prompt must explicitly invoke its skill")
	}
	return nil
}

func validateInstructions(root string, value catalog) (int, error) {
	agents, err := readRegular(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return 0, err
	}
	claude, err := readRegular(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		return 0, err
	}
	maintainerIndex, err := readRegular(filepath.Join(root, "docs", "maintainers", "README.md"))
	if err != nil {
		return 0, err
	}
	docsIndex, err := readRegular(filepath.Join(root, "docs", "README.md"))
	if err != nil {
		return 0, err
	}
	pluginGuide, err := readRegular(filepath.Join(root, "docs", "plugins.md"))
	if err != nil {
		return 0, err
	}
	if len(agents) > maxAgentsBytes || len(claude) > maxClaudeBytes {
		return 0, fmt.Errorf("instruction budget exceeded: AGENTS.md=%d/%d CLAUDE.md=%d/%d",
			len(agents), maxAgentsBytes, len(claude), maxClaudeBytes)
	}
	for _, required := range []string{
		"docs/maintainers/README.md", ".agents/skills/", "internal/domain", "skills-src/",
		"docs/reference/cli/", "docs/reference/output/", "Session recovery", "Live validation",
	} {
		if !bytes.Contains(agents, []byte(required)) {
			return 0, fmt.Errorf("AGENTS.md is missing canonical route or invariant %q", required)
		}
	}
	if !bytes.Contains(claude, []byte("(AGENTS.md)")) {
		return 0, errors.New("CLAUDE.md does not defer to AGENTS.md")
	}
	for _, spec := range value.Skills {
		if !bytes.Contains(claude, []byte(spec.Runbook)) {
			return 0, fmt.Errorf("CLAUDE.md does not route to %s", spec.Runbook)
		}
		if !bytes.Contains(maintainerIndex, []byte(filepath.Base(spec.Runbook))) {
			return 0, fmt.Errorf("maintainer index does not route to %s", spec.Runbook)
		}
	}
	if !bytes.Contains(docsIndex, []byte("maintainers/README.md")) {
		return 0, errors.New("documentation index does not route to maintainer workflows")
	}
	for _, contract := range []struct {
		name string
		body []byte
		want []string
	}{
		{
			name: "AGENTS.md client-skill ownership",
			body: agents,
			want: []string{"`skills-src/` is the source of truth", "`skills/` and", "`plugins/atl/skills/` are generated", "never edit them by hand"},
		},
		{
			name: "docs/plugins.md client-skill ownership",
			body: pluginGuide,
			want: []string{"skills-src/                 ← SOURCE OF TRUTH: edit here, and only here", "skills/                     ← GENERATED:", "plugins/atl/skills/         ← GENERATED:", "Edit files under `skills-src/`."},
		},
	} {
		for _, phrase := range contract.want {
			if !bytes.Contains(contract.body, []byte(phrase)) {
				return 0, fmt.Errorf("%s is missing semantic boundary %q", contract.name, phrase)
			}
		}
	}
	makefile, err := readRegular(filepath.Join(root, "Makefile"))
	if err != nil {
		return 0, err
	}
	makeContract := []byte("check-repository-skills:\n\tgo run ./scripts/check-repository-skills -root .\n")
	if bytes.Count(makefile, makeContract) != 1 {
		return 0, errors.New("makefile must contain one exact repository-skill check target")
	}
	for _, workflow := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		body, err := readRegular(filepath.Join(root, filepath.FromSlash(workflow)))
		if err != nil {
			return 0, err
		}
		block := []byte("- name: Repository maintainer skills\n        run: make check-repository-skills")
		if bytes.Count(body, block) != 1 {
			return 0, fmt.Errorf("%s must contain one exact repository-skill check block", workflow)
		}
	}
	return len(agents) + len(claude), nil
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("missing, non-directory, or symlink directory")
	}
	return nil
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("missing, non-regular, or symlink file")
	}
	return os.ReadFile(path)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
