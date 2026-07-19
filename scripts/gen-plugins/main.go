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
// Outputs are regenerated wholesale (target dirs are recreated), each
// generated .md carries a header comment pointing back at its source, and an
// unresolved {{var}} or an unexpected source file type is a hard error so a
// typo cannot silently ship half-rendered text. CI runs `make check-plugins`
// (regenerate + `git status --porcelain`) to reject stale or hand-edited
// outputs.
package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/skillmeta"
	"github.com/isukharev/atl/internal/skillrouting"
)

const (
	srcRoot        = "skills-src"
	routingCorpus  = "benchmarks/agent-eval/skill-routing.v1.json"
	maxSourceBytes = 8 << 20
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

var platforms = []platform{
	{
		name:       "claude",
		outRoot:    "skills",
		copyOpenAI: false,
		vars: map[string]string{
			"setup_cmd":             "/atl:setup",
			"agent_name":            "Claude Code",
			"agent_short":           "Claude",
			"guidance_file":         "CLAUDE.md",
			"plugin_update_cmd":     "/plugin update atl",
			"setup_invocation_note": "",
		},
	},
	{
		name:       "codex",
		outRoot:    filepath.Join("plugins", "atl", "skills"),
		copyOpenAI: true,
		vars: map[string]string{
			"setup_cmd":             "$setup",
			"agent_name":            "Codex",
			"agent_short":           "Codex",
			"guidance_file":         "AGENTS.md",
			"plugin_update_cmd":     "codex plugin update atl",
			"setup_invocation_note": "Invocation: install/enable the atl plugin in Codex, then run this skill from `/skills` or with `$setup`.",
		},
	},
}

// Placeholders use an "atl." prefix ({{atl.setup_cmd}}) so they can never
// collide with literal {{...}} content (Jira wiki markup renders {{text}}
// as monospace and the jira skill documents that syntax).
var varRe = regexp.MustCompile(`\{\{atl\.([a-z_]+)\}\}`)

// Test seams exercise source and publication replacement windows. Production
// runs leave both nil.
var (
	afterSourceSnapshotValidated func()
	beforeOutputIdentityRebind   func(platformName string)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-plugins:", err)
		os.Exit(1)
	}
}

func run() error {
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
	for platformIndex, pl := range platforms {
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

	repositoryRoot, err := os.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("open repository root: %w", err)
	}
	defer func() { _ = repositoryRoot.Close() }()
	// Validate every output path before replacing the first tree. This keeps a
	// symlinked intermediate directory from redirecting publication and keeps
	// one invalid platform target from partially regenerating the other.
	for _, pl := range platforms {
		if err := validateOutputRoot(repositoryRoot, pl.outRoot); err != nil {
			return fmt.Errorf("validate %s output root: %w", pl.name, err)
		}
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
	if err := closePublished(); err != nil {
		return fmt.Errorf("close published output roots: %w", err)
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
