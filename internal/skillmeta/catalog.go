// Package skillmeta validates the deliberately small metadata contract shared
// by atl's generated agent skills. It is intentionally not a general YAML
// parser: accepting a new key or scalar form requires an explicit code change.
package skillmeta

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// RoutingFileName is reserved for the provider-neutral routing corpus. It
	// belongs to the source catalog but is not emitted into provider skill trees.
	RoutingFileName = "routing.v1.json"

	maxSkillFileBytes        = 1 << 20
	maxOpenAIFileBytes       = 64 << 10
	maxSkillNameBytes        = 64
	maxDescriptionBytes      = 1024
	maxDisplayNameBytes      = 64
	minShortDescriptionRunes = 25
	maxShortDescriptionRunes = 64
	maxDefaultPromptBytes    = 1024
)

var (
	skillNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

// Skill describes one validated logical skill. Name is provider-neutral; a
// plugin namespace may be added by an installed provider without changing it.
type Skill struct {
	Name                   string
	Description            string
	DisableModelInvocation bool
	AllowedTools           string
	OpenAI                 OpenAI
}

// OpenAI is the strict Codex-facing metadata subset shipped by atl.
type OpenAI struct {
	DisplayName             string
	ShortDescription        string
	DefaultPrompt           string
	AllowImplicitInvocation bool
}

// Catalog is sorted by logical skill name.
type Catalog struct {
	Skills []Skill
}

// LoadSource validates a complete skills-src directory. Only skill
// directories and the reserved routing corpus are valid top-level entries.
func LoadSource(root string) (Catalog, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.IsDir() {
		return Catalog{}, fmt.Errorf("skill source catalog is not a plain directory")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return Catalog{}, fmt.Errorf("open skill source catalog: %w", err)
	}
	defer func() { _ = rootHandle.Close() }()
	openedRootInfo, err := rootHandle.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return Catalog{}, fmt.Errorf("skill source catalog changed while it was opened")
	}
	snapshot := make(map[string][]byte)
	err = fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("skill source entry %q is not a plain directory or regular file", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill source entry %q is not regular", path)
		}
		data, err := readRootRegularFile(rootHandle, filepath.FromSlash(path), maxSkillFileBytes, info)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(path)] = data
		return nil
	})
	if err != nil {
		return Catalog{}, fmt.Errorf("read skill source catalog: %w", err)
	}
	return LoadSnapshot(snapshot)
}

// LoadSnapshot validates the exact immutable bytes that a generator or other
// caller will consume. Paths are slash-separated and relative to skills-src.
func LoadSnapshot(snapshot map[string][]byte) (Catalog, error) {
	topLevel := make(map[string]bool)
	for path := range snapshot {
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean != path || clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(path) {
			return Catalog{}, fmt.Errorf("skill source snapshot contains invalid path %q", path)
		}
		parts := strings.Split(clean, "/")
		if len(parts) == 1 {
			if clean != RoutingFileName {
				return Catalog{}, fmt.Errorf("unexpected skill source entry %q", clean)
			}
			continue
		}
		topLevel[parts[0]] = true
	}
	directories := make([]string, 0, len(topLevel))
	for directory := range topLevel {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	seen := make(map[string]string, len(directories))
	var skills []Skill
	for _, directory := range directories {
		if !validSkillName(directory) {
			return Catalog{}, fmt.Errorf("skill directory %q is not a valid logical id", directory)
		}
		skillData, ok := snapshot[directory+"/SKILL.md"]
		if !ok {
			return Catalog{}, fmt.Errorf("skill %q: SKILL.md is missing", directory)
		}
		if len(skillData) > maxSkillFileBytes {
			return Catalog{}, fmt.Errorf("skill %q: metadata file exceeds %d bytes", directory, maxSkillFileBytes)
		}
		skill, err := parseSkillFrontmatter(skillData)
		if err != nil {
			return Catalog{}, fmt.Errorf("skill %q: %w", directory, err)
		}
		if previous, exists := seen[skill.Name]; exists {
			return Catalog{}, fmt.Errorf("duplicate logical skill id %q in %q and %q", skill.Name, previous, directory)
		}
		seen[skill.Name] = directory
		if skill.Name != directory {
			return Catalog{}, fmt.Errorf("skill directory %q does not match frontmatter name %q", directory, skill.Name)
		}
		openAIData, ok := snapshot[directory+"/agents/openai.yaml"]
		if !ok {
			return Catalog{}, fmt.Errorf("skill %q: agents metadata directory is not a plain directory", directory)
		}
		if len(openAIData) > maxOpenAIFileBytes {
			return Catalog{}, fmt.Errorf("skill %q: metadata file exceeds %d bytes", directory, maxOpenAIFileBytes)
		}
		skill.OpenAI, err = parseOpenAI(openAIData, skill.Name)
		if err != nil {
			return Catalog{}, fmt.Errorf("skill %q: %w", directory, err)
		}
		if skill.DisableModelInvocation == skill.OpenAI.AllowImplicitInvocation {
			return Catalog{}, fmt.Errorf("skill %q explicit-only policy differs between SKILL.md and openai.yaml", directory)
		}
		skills = append(skills, skill)
	}
	if len(skills) == 0 {
		return Catalog{}, fmt.Errorf("skill source catalog is empty")
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return Catalog{Skills: skills}, nil
}

func readRootRegularFile(root *os.Root, path string, maximum int64, info fs.FileInfo) ([]byte, error) {
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("metadata file is not regular")
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("metadata file exceeds %d bytes", maximum)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("metadata file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("metadata file exceeds %d bytes", maximum)
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) || !finalInfo.ModTime().Equal(openedInfo.ModTime()) {
		return nil, fmt.Errorf("metadata file changed while it was read")
	}
	return data, nil
}

func validSkillName(name string) bool {
	return name != "" && len(name) <= maxSkillNameBytes && skillNameRE.MatchString(name)
}

func parseSkillFrontmatter(data []byte) (Skill, error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return Skill{}, fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	end := bytes.Index(data[4:], []byte("\n---\n"))
	if end < 0 {
		return Skill{}, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	block := data[4 : 4+end]
	values := make(map[string]string, 4)
	scanner := bufio.NewScanner(bytes.NewReader(block))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return Skill{}, fmt.Errorf("SKILL.md frontmatter supports only top-level scalar fields")
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != key || key == "" {
			return Skill{}, fmt.Errorf("SKILL.md frontmatter line is invalid")
		}
		switch key {
		case "name", "description", "disable-model-invocation", "allowed-tools":
		default:
			return Skill{}, fmt.Errorf("SKILL.md frontmatter field %q is unknown", key)
		}
		if _, exists := values[key]; exists {
			return Skill{}, fmt.Errorf("SKILL.md frontmatter field %q is duplicated", key)
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md frontmatter: %w", err)
	}
	name := values["name"]
	if !validSkillName(name) {
		return Skill{}, fmt.Errorf("SKILL.md name is missing or invalid")
	}
	description := values["description"]
	if description == "" || len(description) > maxDescriptionBytes {
		return Skill{}, fmt.Errorf("SKILL.md description must contain 1..%d bytes", maxDescriptionBytes)
	}
	negativeBoundary := "DO NOT USE WHEN"
	positiveText := strings.ReplaceAll(description, negativeBoundary, "")
	if !strings.Contains(positiveText, "USE WHEN") || !strings.Contains(description, negativeBoundary) {
		return Skill{}, fmt.Errorf("SKILL.md description must declare USE WHEN and DO NOT USE WHEN boundaries")
	}
	disable := false
	if raw, exists := values["disable-model-invocation"]; exists {
		parsed, err := strconv.ParseBool(raw)
		if err != nil || raw != strconv.FormatBool(parsed) {
			return Skill{}, fmt.Errorf("SKILL.md disable-model-invocation must be true or false")
		}
		disable = parsed
	}
	return Skill{
		Name:                   name,
		Description:            description,
		DisableModelInvocation: disable,
		AllowedTools:           values["allowed-tools"],
	}, nil
}

func parseOpenAI(data []byte, skillName string) (OpenAI, error) {
	if !validSkillName(skillName) {
		return OpenAI{}, fmt.Errorf("logical skill id is invalid")
	}
	sections := map[string]bool{}
	values := map[string]string{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "\t") {
			return OpenAI{}, fmt.Errorf("openai.yaml tabs are not supported")
		}
		if !strings.HasPrefix(line, " ") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(key) != key || strings.TrimSpace(value) != "" {
				return OpenAI{}, fmt.Errorf("openai.yaml top-level section is invalid")
			}
			if key != "interface" && key != "policy" {
				return OpenAI{}, fmt.Errorf("openai.yaml section %q is unknown", key)
			}
			if sections[key] {
				return OpenAI{}, fmt.Errorf("openai.yaml section %q is duplicated", key)
			}
			sections[key] = true
			section = key
			continue
		}
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") || section == "" {
			return OpenAI{}, fmt.Errorf("openai.yaml supports exactly two indentation levels")
		}
		key, raw, ok := strings.Cut(strings.TrimPrefix(line, "  "), ":")
		if !ok || strings.TrimSpace(key) != key || key == "" {
			return OpenAI{}, fmt.Errorf("openai.yaml field is invalid")
		}
		qualified := section + "." + key
		switch qualified {
		case "interface.display_name", "interface.short_description", "interface.default_prompt", "policy.allow_implicit_invocation":
		default:
			return OpenAI{}, fmt.Errorf("openai.yaml field %q is unknown", qualified)
		}
		if _, exists := values[qualified]; exists {
			return OpenAI{}, fmt.Errorf("openai.yaml field %q is duplicated", qualified)
		}
		values[qualified] = strings.TrimSpace(raw)
	}
	if err := scanner.Err(); err != nil {
		return OpenAI{}, fmt.Errorf("read openai.yaml: %w", err)
	}
	for _, required := range []string{"interface", "policy"} {
		if !sections[required] {
			return OpenAI{}, fmt.Errorf("openai.yaml section %q is missing", required)
		}
	}
	readQuoted := func(key string, maximum int) (string, error) {
		raw, exists := values[key]
		if !exists {
			return "", fmt.Errorf("openai.yaml field %q is missing", key)
		}
		value, err := strconv.Unquote(raw)
		if err != nil || raw == "" || raw[0] != '"' || value == "" || !utf8.ValidString(value) || len(value) > maximum || strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("openai.yaml field %q must be a non-empty quoted scalar of at most %d bytes", key, maximum)
		}
		return value, nil
	}
	display, err := readQuoted("interface.display_name", maxDisplayNameBytes)
	if err != nil {
		return OpenAI{}, err
	}
	short, err := readQuoted("interface.short_description", maxDefaultPromptBytes)
	if err != nil {
		return OpenAI{}, err
	}
	shortRunes := utf8.RuneCountInString(short)
	if shortRunes < minShortDescriptionRunes || shortRunes > maxShortDescriptionRunes {
		return OpenAI{}, fmt.Errorf("openai.yaml field %q must contain %d..%d characters", "interface.short_description", minShortDescriptionRunes, maxShortDescriptionRunes)
	}
	prompt, err := readQuoted("interface.default_prompt", maxDefaultPromptBytes)
	if err != nil {
		return OpenAI{}, err
	}
	if !hasExactBareSkillInvocation(prompt, skillName) {
		return OpenAI{}, fmt.Errorf("openai.yaml default_prompt must invoke exactly its own bare $%s skill", skillName)
	}
	rawImplicit, exists := values["policy.allow_implicit_invocation"]
	if !exists {
		return OpenAI{}, fmt.Errorf("openai.yaml field %q is missing", "policy.allow_implicit_invocation")
	}
	implicit, err := strconv.ParseBool(rawImplicit)
	if err != nil || rawImplicit != strconv.FormatBool(implicit) {
		return OpenAI{}, fmt.Errorf("openai.yaml allow_implicit_invocation must be true or false")
	}
	return OpenAI{
		DisplayName: display, ShortDescription: short, DefaultPrompt: prompt,
		AllowImplicitInvocation: implicit,
	}, nil
}

func hasExactBareSkillInvocation(prompt, skillName string) bool {
	expected := "$" + skillName
	if strings.Count(prompt, "$") != 1 {
		return false
	}
	index := strings.Index(prompt, expected)
	if index < 0 {
		return false
	}
	after := index + len(expected)
	if after == len(prompt) {
		return true
	}
	next := prompt[after]
	switch {
	case next >= 'a' && next <= 'z', next >= 'A' && next <= 'Z',
		next >= '0' && next <= '9', next == '_', next == '-', next == ':':
		return false
	default:
		return true
	}
}
