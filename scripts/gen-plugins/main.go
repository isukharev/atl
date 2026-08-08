// Command gen-plugins renders the agent-plugin skill trees from the single
// source of truth in skills-src/.
//
// skills-src/ holds plain SKILL.md / reference *.md files with a handful of
// {{var}} placeholders for the few strings that differ per platform, plus
// codex-only agents/openai.yaml metadata. This tool substitutes the
// per-platform values and writes two committed output trees:
//
//	skills/             the Claude Code plugin (openai.yaml omitted)
//	plugins/atl/skills/ the Codex plugin (openai.yaml copied verbatim)
//
// It also copies the repository .mcp.json into the Codex plugin package.
// Output trees are regenerated wholesale (target dirs are recreated), while
// companion files use contained atomic replacement. Each generated .md carries
// a header comment pointing back at its source, and an unresolved {{var}} or an
// unexpected source file type is a hard error so a typo cannot silently ship
// half-rendered text. CI runs `make check-plugins`, which uses this command's
// --check mode to compare the rendered snapshot with the committed outputs
// without rewriting them.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/skillmeta"
	"github.com/isukharev/atl/internal/skillrouting"
)

const (
	srcRoot               = "skills-src"
	routingCorpus         = "benchmarks/agent-eval/skill-routing.v1.json"
	rootMCPConfigName     = ".mcp.json"
	pluginMCPConfigPath   = "plugins/atl/.mcp.json"
	codexSkillCatalogName = "skill-catalog.v1.json"
	maxSourceBytes        = 8 << 20
	codexCatalogSchema    = 1
	maxCodexCatalogBytes  = 1 << 20
	maxCodexCatalogSkills = 256
	maxCodexCatalogFiles  = 4096
	maxCodexSkillName     = 64
	maxCodexSkillPath     = 512
	maxCodexSkillFile     = 8 << 20
	maxCodexSkillTree     = 64 << 20
)

type platform struct {
	name       string
	outRoot    string
	copyOpenAI bool
	vars       map[string]string
}

type renderedFile struct {
	rel  string
	data []byte
}

type sourceFile struct {
	rel  string
	data []byte
}

type outputTarget struct {
	platform platform
	parent   *os.Root
	base     string
}

type publishedOutput struct {
	target outputTarget
	root   *os.Root
}

type codexSkillCatalog struct {
	SchemaVersion int                      `json:"schema_version"`
	Skills        []codexSkillCatalogSkill `json:"skills"`
	Files         []codexSkillCatalogFile  `json:"files"`
}

type codexSkillCatalogSkill struct {
	Name                    string `json:"name"`
	AllowImplicitInvocation bool   `json:"allow_implicit_invocation"`
}

type codexSkillCatalogFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

var (
	codexSkillCatalogPath = filepath.Join("plugins", "atl", codexSkillCatalogName)
	platforms             = []platform{
		{
			name:       "claude",
			outRoot:    "skills",
			copyOpenAI: false,
			vars: map[string]string{
				"setup_cmd":                  "/atl:setup",
				"agent_name":                 "Claude Code",
				"agent_short":                "Claude",
				"guidance_file":              "CLAUDE.md",
				"plugin_update_instructions": "Use Claude Code's `/plugin update atl` command.",
				"setup_invocation_note":      "",
			},
		},
		{
			name:       "codex",
			outRoot:    filepath.Join("plugins", "atl", "skills"),
			copyOpenAI: true,
			vars: map[string]string{
				"setup_cmd":                  "$setup",
				"agent_name":                 "Codex",
				"agent_short":                "Codex",
				"guidance_file":              "AGENTS.md",
				"plugin_update_instructions": "Run `codex plugin marketplace upgrade atl --json`. If it succeeds, run `codex plugin add atl@atl --json`. Then start a new Codex chat or CLI session before retrying.",
				"setup_invocation_note":      "Invocation: install/enable the atl plugin in Codex, then run this skill from `/skills` or with `$setup`.",
			},
		},
	}
)

// Placeholders use an "atl." prefix ({{atl.setup_cmd}}) so they can never
// collide with literal {{...}} content (Jira wiki markup renders {{text}}
// as monospace and the jira skill documents that syntax).
var varRe = regexp.MustCompile(`\{\{atl\.([a-z_]+)\}\}`)

// Test seams exercise source and publication replacement windows. Production
// runs leave both nil.
var (
	afterSourceSnapshotValidated func()
	beforeOutputIdentityRebind   func(platformName string)
	afterGeneratedTempClosed     func(name string)
	beforeGeneratedPublishRename func(temporary, destination string)
	beforeCodexCatalogPublish    func()
)

func main() {
	check, err := parseMode(os.Args[1:])
	if err == nil {
		err = runMode(check)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-plugins:", err)
		os.Exit(1)
	}
}

func run() error {
	return runMode(false)
}

func parseMode(args []string) (bool, error) {
	switch len(args) {
	case 0:
		return false, nil
	case 1:
		if args[0] == "--check" {
			return true, nil
		}
	}
	return false, fmt.Errorf("usage: gen-plugins [--check]")
}

func runMode(check bool) error {
	sourceInfo, err := os.Lstat(srcRoot)
	if err != nil {
		return fmt.Errorf("source tree %s not found (run from the repo root): %w", srcRoot, err)
	}
	if sourceInfo.Mode()&fs.ModeSymlink != 0 || !sourceInfo.IsDir() {
		return fmt.Errorf("source tree %s must be a plain directory", srcRoot)
	}
	sourceRoot, err := os.OpenRoot(srcRoot)
	if err != nil {
		return fmt.Errorf("open source tree: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	openedSourceInfo, err := sourceRoot.Stat(".")
	if err != nil || !openedSourceInfo.IsDir() || !os.SameFile(sourceInfo, openedSourceInfo) {
		return fmt.Errorf("source tree changed while it was opened")
	}
	var files []sourceFile
	snapshot := make(map[string][]byte)
	err = fs.WalkDir(sourceRoot.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("source entry %s is a symlink", path)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("inspect source entry %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source entry %s is not a regular file", path)
		}
		rel := filepath.FromSlash(path)
		data, err := readSourceFile(sourceRoot, rel, info)
		if err != nil {
			return fmt.Errorf("read source entry %s: %w", path, err)
		}
		files = append(files, sourceFile{rel: rel, data: data})
		snapshot[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	// Validate the exact immutable snapshot that will be rendered for both
	// providers. A later source-tree mutation cannot change publication bytes.
	catalog, err := skillmeta.LoadSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("validate source skill metadata: %w", err)
	}
	routingData, ok := snapshot[skillmeta.RoutingFileName]
	if !ok {
		return fmt.Errorf("validate source skill routing: %s is missing", skillmeta.RoutingFileName)
	}
	registry, err := skillrouting.LoadRegistry(bytes.NewReader(routingData))
	if err != nil {
		return fmt.Errorf("validate source skill routing: %w", err)
	}
	corpus, err := skillrouting.LoadCorpusFile(routingCorpus)
	if err != nil {
		return fmt.Errorf("validate source skill routing: %w", err)
	}
	if _, err := skillrouting.ValidateCatalog(catalog, registry, corpus); err != nil {
		return fmt.Errorf("validate source skill routing: %w", err)
	}
	if afterSourceSnapshotValidated != nil {
		afterSourceSnapshotValidated()
	}

	// Render every source for every platform in memory before replacing any
	// committed tree. Unknown file types, placeholder drift, or malformed
	// frontmatter therefore leave both existing outputs intact.
	rendered := make([][]renderedFile, len(platforms))
	codexPlatformIndex := -1
	for platformIndex, pl := range platforms {
		if pl.name == "codex" {
			if codexPlatformIndex >= 0 {
				return fmt.Errorf("codex output platform is duplicated")
			}
			codexPlatformIndex = platformIndex
		}
		for _, source := range files {
			out, err := renderFile(source.data, source.rel, pl)
			if err != nil {
				return fmt.Errorf("%s (%s): %w", filepath.Join(srcRoot, source.rel), pl.name, err)
			}
			if out == nil {
				continue // file not emitted for this platform
			}
			rendered[platformIndex] = append(rendered[platformIndex], renderedFile{rel: source.rel, data: out})
		}
	}
	if codexPlatformIndex < 0 {
		return fmt.Errorf("codex output platform is missing")
	}
	codexSkillCatalogData, err := buildCodexSkillCatalog(catalog, rendered[codexPlatformIndex])
	if err != nil {
		return fmt.Errorf("build codex skill catalog: %w", err)
	}

	repositoryRoot, err := os.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("open repository root: %w", err)
	}
	defer func() { _ = repositoryRoot.Close() }()
	rootMCPConfigData, err := readExpectedRepositoryFile(repositoryRoot, rootMCPConfigName)
	if err != nil {
		return fmt.Errorf("read plugin MCP config source: %w", err)
	}
	// Validate every output path before replacing the first tree. This keeps a
	// symlinked intermediate directory from redirecting publication and keeps
	// one invalid platform target from partially regenerating the other.
	for _, pl := range platforms {
		if err := validateOutputRoot(repositoryRoot, pl.outRoot); err != nil {
			return fmt.Errorf("validate %s output root: %w", pl.name, err)
		}
	}
	if err := validateGeneratedFileDestination(repositoryRoot, codexSkillCatalogPath); err != nil {
		return fmt.Errorf("validate codex skill catalog output: %w", err)
	}
	if err := validateGeneratedFileDestination(repositoryRoot, pluginMCPConfigPath); err != nil {
		return fmt.Errorf("validate plugin MCP config output: %w", err)
	}
	targets := make([]outputTarget, 0, len(platforms))
	for _, pl := range platforms {
		target, err := openOutputTarget(repositoryRoot, pl)
		if err != nil {
			for _, opened := range targets {
				_ = opened.parent.Close()
			}
			return fmt.Errorf("pin %s output parent: %w", pl.name, err)
		}
		targets = append(targets, target)
	}
	defer func() {
		for _, target := range targets {
			_ = target.parent.Close()
		}
	}()
	if check {
		if err := checkGeneratedOutputs(repositoryRoot, targets, rendered, codexSkillCatalogData, rootMCPConfigData); err != nil {
			return fmt.Errorf("generated plugin outputs are stale or hand-edited (edit %s/, run 'make gen-plugins', and commit every generated output): %w", srcRoot, err)
		}
		return nil
	}

	published := make([]publishedOutput, 0, len(targets))
	closePublished := func() error {
		var first error
		for _, output := range published {
			if err := output.root.Close(); err != nil && first == nil {
				first = err
			}
		}
		published = nil
		return first
	}
	defer func() { _ = closePublished() }()
	for platformIndex, target := range targets {
		if err := target.parent.RemoveAll(target.base); err != nil {
			return fmt.Errorf("remove %s output root: %w", target.platform.name, err)
		}
		if err := target.parent.Mkdir(target.base, 0o755); err != nil {
			return fmt.Errorf("create %s output root: %w", target.platform.name, err)
		}
		outputRoot, err := target.parent.OpenRoot(target.base)
		if err != nil {
			return fmt.Errorf("pin %s output root: %w", target.platform.name, err)
		}
		info, infoErr := target.parent.Lstat(target.base)
		openedInfo, openedErr := outputRoot.Stat(".")
		if infoErr != nil || openedErr != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(info, openedInfo) {
			_ = outputRoot.Close()
			return fmt.Errorf("pin %s output root: directory changed during publication", target.platform.name)
		}
		for _, output := range rendered[platformIndex] {
			if err := outputRoot.MkdirAll(filepath.Dir(output.rel), 0o755); err != nil {
				_ = outputRoot.Close()
				return fmt.Errorf("create %s output directory: %w", target.platform.name, err)
			}
			if err := outputRoot.WriteFile(output.rel, output.data, 0o644); err != nil {
				_ = outputRoot.Close()
				return fmt.Errorf("write %s output %s: %w", target.platform.name, output.rel, err)
			}
			if err := outputRoot.Chmod(output.rel, 0o644); err != nil {
				_ = outputRoot.Close()
				return fmt.Errorf("set %s output %s mode: %w", target.platform.name, output.rel, err)
			}
		}
		if beforeOutputIdentityRebind != nil {
			beforeOutputIdentityRebind(target.platform.name)
		}
		publishedInfo, infoErr := target.parent.Lstat(target.base)
		openedPublishedInfo, openedErr := outputRoot.Stat(".")
		if infoErr != nil || openedErr != nil || !publishedInfo.IsDir() || publishedInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(publishedInfo, openedPublishedInfo) {
			_ = outputRoot.Close()
			return fmt.Errorf("publish %s output root: directory changed during publication", target.platform.name)
		}
		published = append(published, publishedOutput{target: target, root: outputRoot})
	}
	// Keep every published root pinned until both trees are complete. Otherwise
	// an earlier provider path could be replaced while a later tree is written
	// and the generator could return success for a path it no longer owns.
	for _, output := range published {
		pathInfo, pathErr := output.target.parent.Lstat(output.target.base)
		openedInfo, openedErr := output.root.Stat(".")
		if pathErr != nil || openedErr != nil || !pathInfo.IsDir() || pathInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(pathInfo, openedInfo) {
			return fmt.Errorf("publish %s output root: directory changed after publication", output.target.platform.name)
		}
	}
	var codexOutputParent, codexOutputRoot *os.Root
	for _, output := range published {
		if output.target.platform.name == "codex" {
			codexOutputParent = output.target.parent
			codexOutputRoot = output.root
			break
		}
	}
	if codexOutputParent == nil || codexOutputRoot == nil ||
		filepath.Dir(codexSkillCatalogPath) != filepath.Dir(platforms[codexPlatformIndex].outRoot) ||
		filepath.Dir(pluginMCPConfigPath) != filepath.Dir(platforms[codexPlatformIndex].outRoot) {
		return fmt.Errorf("publish codex skill catalog: output parent mismatch")
	}
	if err := verifyPublishedSkillTree(codexOutputRoot, rendered[codexPlatformIndex]); err != nil {
		return fmt.Errorf("verify codex skill tree before companion publication: %w", err)
	}
	if err := writeGeneratedFile(codexOutputParent, filepath.Base(pluginMCPConfigPath), rootMCPConfigData); err != nil {
		return fmt.Errorf("publish plugin MCP config: %w", err)
	}
	if beforeCodexCatalogPublish != nil {
		beforeCodexCatalogPublish()
	}
	if err := verifyPublishedSkillTree(codexOutputRoot, rendered[codexPlatformIndex]); err != nil {
		return fmt.Errorf("verify codex skill tree before catalog publication: %w", err)
	}
	if err := writeGeneratedFile(codexOutputParent, filepath.Base(codexSkillCatalogPath), codexSkillCatalogData); err != nil {
		return fmt.Errorf("publish codex skill catalog: %w", err)
	}
	if err := verifyPublishedSkillTree(codexOutputRoot, rendered[codexPlatformIndex]); err != nil {
		return fmt.Errorf("verify codex skill tree after catalog publication: %w", err)
	}
	if err := closePublished(); err != nil {
		return fmt.Errorf("close published output roots: %w", err)
	}
	return nil
}

func checkGeneratedOutputs(repositoryRoot *os.Root, targets []outputTarget, rendered [][]renderedFile, codexSkillCatalogData, rootMCPConfigData []byte) error {
	opened := make([]publishedOutput, 0, len(targets))
	defer func() {
		for _, output := range opened {
			_ = output.root.Close()
		}
	}()
	for platformIndex, target := range targets {
		pathInfo, err := target.parent.Lstat(target.base)
		if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("check %s output root: expected a plain directory", target.platform.name)
		}
		outputRoot, err := target.parent.OpenRoot(target.base)
		if err != nil {
			return fmt.Errorf("check %s output root: %w", target.platform.name, err)
		}
		openedInfo, err := outputRoot.Stat(".")
		if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
			_ = outputRoot.Close()
			return fmt.Errorf("check %s output root: directory changed while it was opened", target.platform.name)
		}
		if err := verifyPublishedSkillTree(outputRoot, rendered[platformIndex]); err != nil {
			_ = outputRoot.Close()
			return fmt.Errorf("check %s output root: %w", target.platform.name, err)
		}
		opened = append(opened, publishedOutput{target: target, root: outputRoot})
	}

	if err := verifyExpectedGeneratedFile(repositoryRoot, codexSkillCatalogPath, codexSkillCatalogData); err != nil {
		return fmt.Errorf("check codex skill catalog: %w", err)
	}
	if err := verifyExpectedGeneratedFile(repositoryRoot, pluginMCPConfigPath, rootMCPConfigData); err != nil {
		return fmt.Errorf("check plugin MCP config: %w", err)
	}

	// Retain pinned roots until every output has been checked, then rebind each
	// repository path and repeat byte verification. A concurrent replacement
	// must not let check mode report a stale or redirected tree as current.
	for platformIndex, output := range opened {
		pathInfo, pathErr := output.target.parent.Lstat(output.target.base)
		openedInfo, openedErr := output.root.Stat(".")
		if pathErr != nil || openedErr != nil || !pathInfo.IsDir() || pathInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(pathInfo, openedInfo) {
			return fmt.Errorf("check %s output root: directory changed during verification", output.target.platform.name)
		}
		if err := verifyPublishedSkillTree(output.root, rendered[platformIndex]); err != nil {
			return fmt.Errorf("check %s output root: %w", output.target.platform.name, err)
		}
	}
	if err := verifyExpectedGeneratedFile(repositoryRoot, codexSkillCatalogPath, codexSkillCatalogData); err != nil {
		return fmt.Errorf("check codex skill catalog: %w", err)
	}
	if err := verifyExpectedGeneratedFile(repositoryRoot, pluginMCPConfigPath, rootMCPConfigData); err != nil {
		return fmt.Errorf("check plugin MCP config: %w", err)
	}
	return nil
}

func readExpectedRepositoryFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("expected a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) ||
		!finalInfo.ModTime().Equal(openedInfo.ModTime()) || len(data) > maxSourceBytes {
		return nil, fmt.Errorf("file changed while it was read")
	}
	return data, nil
}

func verifyExpectedGeneratedFile(root *os.Root, name string, data []byte) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("expected a regular file")
	}
	return verifyGeneratedFile(root, name, info, data)
}

func buildCodexSkillCatalog(catalog skillmeta.Catalog, rendered []renderedFile) ([]byte, error) {
	if len(catalog.Skills) == 0 || len(rendered) == 0 {
		return nil, fmt.Errorf("catalog and generated file inventory must be non-empty")
	}
	if len(catalog.Skills) > maxCodexCatalogSkills || len(rendered) > maxCodexCatalogFiles {
		return nil, fmt.Errorf("catalog exceeds schema-v1 cardinality bounds")
	}
	value := codexSkillCatalog{SchemaVersion: codexCatalogSchema}
	value.Skills = make([]codexSkillCatalogSkill, 0, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		if len(skill.Name) == 0 || len(skill.Name) > maxCodexSkillName {
			return nil, fmt.Errorf("skill name exceeds schema-v1 bounds")
		}
		value.Skills = append(value.Skills, codexSkillCatalogSkill{
			Name:                    skill.Name,
			AllowImplicitInvocation: skill.OpenAI.AllowImplicitInvocation,
		})
	}
	sort.Slice(value.Skills, func(i, j int) bool { return value.Skills[i].Name < value.Skills[j].Name })
	for index := 1; index < len(value.Skills); index++ {
		if value.Skills[index-1].Name == value.Skills[index].Name {
			return nil, fmt.Errorf("duplicate skill %q", value.Skills[index].Name)
		}
	}

	value.Files = make([]codexSkillCatalogFile, 0, len(rendered))
	var treeBytes int64
	for _, file := range rendered {
		clean := filepath.Clean(file.rel)
		path := filepath.ToSlash(clean)
		if clean != file.rel || path == "." || path == "" || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "\\") || filepath.IsAbs(file.rel) {
			return nil, fmt.Errorf("generated file path %q is not relative to the skill root", file.rel)
		}
		if len(path) > maxCodexSkillPath || len(file.data) > maxCodexSkillFile {
			return nil, fmt.Errorf("generated file exceeds schema-v1 bounds")
		}
		treeBytes += int64(len(file.data))
		if treeBytes > maxCodexSkillTree {
			return nil, fmt.Errorf("generated skill tree exceeds schema-v1 byte bound")
		}
		digest := sha256.Sum256(file.data)
		value.Files = append(value.Files, codexSkillCatalogFile{Path: path, SHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(value.Files, func(i, j int) bool { return value.Files[i].Path < value.Files[j].Path })
	for index := 1; index < len(value.Files); index++ {
		if value.Files[index-1].Path == value.Files[index].Path {
			return nil, fmt.Errorf("duplicate generated file %q", value.Files[index].Path)
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maxCodexCatalogBytes {
		return nil, fmt.Errorf("generated catalog exceeds schema-v1 byte bound")
	}
	return data, nil
}

func verifyPublishedSkillTree(root *os.Root, rendered []renderedFile) error {
	expectedFiles := make(map[string]renderedFile, len(rendered))
	expectedDirectories := make(map[string]struct{}, len(rendered))
	for _, file := range rendered {
		name := filepath.ToSlash(file.rel)
		expectedFiles[name] = file
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			expectedDirectories[directory] = struct{}{}
		}
	}
	seenFiles := make(map[string]struct{}, len(expectedFiles))
	seenDirectories := make(map[string]struct{}, len(expectedDirectories))
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("published tree contains a symbolic link")
		}
		if entry.IsDir() {
			if _, ok := expectedDirectories[name]; !ok {
				return fmt.Errorf("published tree contains an unexpected directory")
			}
			seenDirectories[name] = struct{}{}
			return nil
		}
		want, ok := expectedFiles[name]
		if !ok {
			return fmt.Errorf("published tree contains an unexpected file")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			return fmt.Errorf("published tree contains a special file")
		}
		if err := verifyGeneratedFile(root, filepath.FromSlash(name), info, want.data); err != nil {
			return fmt.Errorf("published file changed: %w", err)
		}
		seenFiles[name] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seenFiles) != len(expectedFiles) || !sameGeneratedDirectorySet(expectedDirectories, seenDirectories) {
		return fmt.Errorf("published tree is incomplete")
	}
	return nil
}

func sameGeneratedDirectorySet(expected, observed map[string]struct{}) bool {
	if len(expected) != len(observed) {
		return false
	}
	for name := range expected {
		if _, ok := observed[name]; !ok {
			return false
		}
	}
	return true
}

func writeGeneratedFile(root *os.Root, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return fmt.Errorf("generated file name is invalid")
	}
	temporary := "." + name + ".tmp"
	backup := "." + name + ".bak"
	if _, err := root.Lstat(backup); err == nil {
		return fmt.Errorf("generated file backup already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if info, err := root.Lstat(temporary); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("generated temporary destination is not regular")
		}
		if err := root.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(temporary)
		}
	}()
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil {
		writeErr = file.Chmod(0o644)
	}
	temporaryInfo, statErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if statErr != nil || !temporaryInfo.Mode().IsRegular() || temporaryInfo.Size() != int64(len(data)) {
		return fmt.Errorf("generated temporary file changed while it was written")
	}
	if closeErr != nil {
		return closeErr
	}
	if afterGeneratedTempClosed != nil {
		afterGeneratedTempClosed(temporary)
	}
	if err := verifyGeneratedFile(root, temporary, temporaryInfo, data); err != nil {
		return fmt.Errorf("verify generated temporary file: %w", err)
	}
	var priorInfo fs.FileInfo
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("generated file destination is not regular")
		}
		priorInfo = info
	} else if !os.IsNotExist(err) {
		return err
	}
	if priorInfo != nil {
		if err := root.Rename(name, backup); err != nil {
			return fmt.Errorf("preserve previous generated file: %w", err)
		}
		backupInfo, err := root.Lstat(backup)
		if err != nil || !backupInfo.Mode().IsRegular() || backupInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(priorInfo, backupInfo) {
			return fmt.Errorf("preserve previous generated file: identity changed")
		}
	}
	if beforeGeneratedPublishRename != nil {
		beforeGeneratedPublishRename(temporary, name)
	}
	if err := root.Rename(temporary, name); err != nil {
		restoreErr := restoreGeneratedBackup(root, name, backup, priorInfo, nil)
		if restoreErr != nil {
			return fmt.Errorf("publish generated file: %v; restore previous file: %w", err, restoreErr)
		}
		return err
	}
	cleanup = false
	if err := verifyGeneratedFile(root, name, temporaryInfo, data); err != nil {
		restoreErr := restoreGeneratedBackup(root, name, backup, priorInfo, temporaryInfo)
		if restoreErr != nil {
			return fmt.Errorf("verify published generated file: %v; restore previous file: %w", err, restoreErr)
		}
		return fmt.Errorf("verify published generated file: %w", err)
	}
	if priorInfo != nil {
		if err := root.Remove(backup); err != nil {
			return fmt.Errorf("remove previous generated file backup: %w", err)
		}
	}
	return nil
}

func restoreGeneratedBackup(root *os.Root, name, backup string, priorInfo, publishedInfo fs.FileInfo) error {
	if publishedInfo != nil {
		current, err := root.Lstat(name)
		if err != nil || !os.SameFile(publishedInfo, current) {
			return fmt.Errorf("published destination identity changed")
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	if priorInfo == nil {
		return nil
	}
	if _, err := root.Lstat(name); err == nil {
		return fmt.Errorf("destination unexpectedly exists during restore")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := root.Rename(backup, name); err != nil {
		return err
	}
	restored, err := root.Lstat(name)
	if err != nil || !restored.Mode().IsRegular() || restored.Mode()&fs.ModeSymlink != 0 || !os.SameFile(priorInfo, restored) {
		return fmt.Errorf("restored destination identity changed")
	}
	return nil
}

func verifyGeneratedFile(root *os.Root, name string, expected fs.FileInfo, data []byte) error {
	pathInfo, err := root.Lstat(name)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&fs.ModeSymlink != 0 || pathInfo.Mode().Perm() != 0o644 || !os.SameFile(expected, pathInfo) {
		return fmt.Errorf("generated file identity changed")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o644 || !os.SameFile(expected, openedInfo) {
		return fmt.Errorf("generated file identity changed")
	}
	got, err := io.ReadAll(io.LimitReader(file, int64(len(data))+1))
	if err != nil {
		return err
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) ||
		!finalInfo.ModTime().Equal(openedInfo.ModTime()) || !bytes.Equal(got, data) {
		return fmt.Errorf("generated file bytes changed")
	}
	return nil
}

func validateGeneratedFileDestination(root *os.Root, path string) error {
	clean := filepath.Clean(path)
	if clean != path || clean == "." || clean == ".." || filepath.IsAbs(path) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("generated file path is invalid")
	}
	temporary := filepath.Join(filepath.Dir(clean), "."+filepath.Base(clean)+".tmp")
	backup := filepath.Join(filepath.Dir(clean), "."+filepath.Base(clean)+".bak")
	for _, candidate := range []string{clean, temporary, backup} {
		info, err := root.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("generated file destination is not regular")
		}
	}
	return nil
}

func openOutputTarget(repositoryRoot *os.Root, pl platform) (outputTarget, error) {
	parentPath := filepath.Dir(pl.outRoot)
	base := filepath.Base(pl.outRoot)
	parent, err := repositoryRoot.OpenRoot(parentPath)
	if err != nil {
		return outputTarget{}, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = parent.Close()
		}
	}()
	// Revalidate after pinning the directory. If an intermediate component was
	// swapped during OpenRoot, either the symlink check or identity comparison
	// observes the mismatch; later swaps cannot redirect the pinned handle.
	if parentPath != "." {
		if err := validateOutputRoot(repositoryRoot, parentPath); err != nil {
			return outputTarget{}, err
		}
	}
	pathInfo, err := repositoryRoot.Lstat(parentPath)
	if err != nil {
		return outputTarget{}, err
	}
	openedInfo, err := parent.Stat(".")
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(pathInfo, openedInfo) {
		return outputTarget{}, fmt.Errorf("output parent changed while it was opened")
	}
	valid = true
	return outputTarget{platform: pl, parent: parent, base: base}, nil
}

// validateOutputRoot rejects every existing non-directory or symlinked path
// component. Missing suffixes are safe: os.Root keeps subsequent creation and
// publication contained beneath the already-open repository root.
func validateOutputRoot(root *os.Root, path string) error {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("output root %q is not repository-relative", path)
	}
	current := ""
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("output root %q has an invalid component", path)
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect %s: %w", current, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("output path component %s is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("output path component %s is not a directory", current)
		}
	}
	return nil
}

// renderFile returns the bytes to write for one source file on one platform,
// or nil when the file is intentionally not emitted for that platform.
func readSourceFile(root *os.Root, rel string, expected fs.FileInfo) ([]byte, error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return nil, fmt.Errorf("source entry changed after validation")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("source entry exceeds %d bytes", maxSourceBytes)
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, finalInfo) || finalInfo.Size() != int64(len(data)) || !finalInfo.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("source entry changed while it was read")
	}
	return data, nil
}

func renderFile(data []byte, rel string, pl platform) ([]byte, error) {
	if filepath.ToSlash(rel) == skillmeta.RoutingFileName {
		return nil, nil
	}
	switch {
	case strings.HasSuffix(rel, ".md"):
		rendered, err := render(string(data), pl.vars)
		if err != nil {
			return nil, err
		}
		withHdr, err := withHeader(rendered, rel)
		if err != nil {
			return nil, err
		}
		return []byte(withHdr), nil
	case strings.HasSuffix(filepath.ToSlash(rel), "agents/openai.yaml"):
		if !pl.copyOpenAI {
			return nil, nil
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unexpected file type in %s — teach gen-plugins how to handle it", srcRoot)
	}
}

// strayRe catches placeholder remnants that varRe's strict form would let
// through — casing or spacing typos like {{atl.Setup_cmd}} or {{ atl.x }} —
// so a typo cannot silently ship half-rendered text.
var strayRe = regexp.MustCompile(`(?i)\{\{\s*atl`)

// render substitutes {{atl.var}} placeholders. A line that consists solely of
// a placeholder whose value is empty is dropped — together with the blank
// line that followed it when it sat between two blanks — so optional
// per-platform notes leave no gap behind. Blank lines elsewhere (including
// inside code fences) are never touched. Any placeholder left unresolved,
// including near-miss typos, is an error.
func render(s string, vars map[string]string) (string, error) {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if m := varRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
			if v, ok := vars[m[1]]; ok && v == "" {
				// Drop the placeholder-only line; when it was framed by blank
				// lines, consume the following blank too so no gap is left.
				if len(out) > 0 && out[len(out)-1] == "" && i+1 < len(lines) && lines[i+1] == "" {
					i++
				}
				continue
			}
		}
		out = append(out, varRe.ReplaceAllStringFunc(line, func(match string) string {
			name := varRe.FindStringSubmatch(match)[1]
			if v, ok := vars[name]; ok {
				return v
			}
			return match // left as-is; caught below
		}))
	}
	res := strings.Join(out, "\n")
	if m := varRe.FindString(res); m != "" {
		return "", fmt.Errorf("unknown placeholder %s", m)
	}
	if loc := strayRe.FindStringIndex(res); loc != nil {
		end := loc[0] + 24
		if end > len(res) {
			end = len(res)
		}
		return "", fmt.Errorf("stray unresolved placeholder near %q", res[loc[0]:end])
	}
	return res, nil
}

// withHeader inserts the generated-file marker. YAML frontmatter must stay at
// byte 0 for skill loaders, so when the file starts with a frontmatter block
// the marker goes right after its closing delimiter; otherwise it goes on
// top. A frontmatter opener with no closing delimiter is a hard error —
// placing the header above it would silently break the skill.
func withHeader(s, rel string) (string, error) {
	header := fmt.Sprintf("<!-- Generated from %s/%s — edit the source and run 'make gen-plugins'. -->",
		srcRoot, filepath.ToSlash(rel))
	if strings.HasPrefix(s, "---\n") {
		end := strings.Index(s[4:], "\n---\n")
		if end < 0 {
			return "", fmt.Errorf("frontmatter opened but never closed")
		}
		cut := 4 + end + len("\n---\n")
		return s[:cut] + header + "\n" + s[cut:], nil
	}
	return header + "\n" + s, nil
}
