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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const srcRoot = "skills-src"

type platform struct {
	name       string
	outRoot    string
	copyOpenAI bool
	vars       map[string]string
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
			"setup_invocation_note": "Invocation: install/enable the atl plugin in Codex, then run this skill from `/skills` or with `$setup`.",
		},
	},
}

// Placeholders use an "atl." prefix ({{atl.setup_cmd}}) so they can never
// collide with literal {{...}} content (Jira wiki markup renders {{text}}
// as monospace and the jira skill documents that syntax).
var varRe = regexp.MustCompile(`\{\{atl\.([a-z_]+)\}\}`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-plugins:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(srcRoot); err != nil {
		return fmt.Errorf("source tree %s not found (run from the repo root): %w", srcRoot, err)
	}
	var files []string
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, pl := range platforms {
		if err := os.RemoveAll(pl.outRoot); err != nil {
			return err
		}
		for _, src := range files {
			rel, err := filepath.Rel(srcRoot, src)
			if err != nil {
				return err
			}
			out, err := renderFile(src, rel, pl)
			if err != nil {
				return fmt.Errorf("%s (%s): %w", src, pl.name, err)
			}
			if out == nil {
				continue // file not emitted for this platform
			}
			dst := filepath.Join(pl.outRoot, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, out, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderFile returns the bytes to write for one source file on one platform,
// or nil when the file is intentionally not emitted for that platform.
func renderFile(src, rel string, pl platform) ([]byte, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(rel, ".md"):
		rendered, err := render(string(data), pl.vars)
		if err != nil {
			return nil, err
		}
		return []byte(withHeader(rendered, rel)), nil
	case strings.HasSuffix(filepath.ToSlash(rel), "agents/openai.yaml"):
		if !pl.copyOpenAI {
			return nil, nil
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unexpected file type in %s — teach gen-plugins how to handle it", srcRoot)
	}
}

// render substitutes {{var}} placeholders. A line that consists solely of a
// placeholder whose value is empty is dropped (and a resulting run of blank
// lines is collapsed), so optional per-platform notes leave no gap behind.
// Any placeholder left unresolved is an error.
func render(s string, vars map[string]string) (string, error) {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := varRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
			if v, ok := vars[m[1]]; ok && v == "" {
				// Drop the placeholder-only line; avoid double blanks.
				if len(out) > 0 && out[len(out)-1] == "" {
					continue
				}
				continue
			}
		}
		replaced := varRe.ReplaceAllStringFunc(line, func(match string) string {
			name := varRe.FindStringSubmatch(match)[1]
			if v, ok := vars[name]; ok {
				return v
			}
			return match // left as-is; caught below
		})
		out = append(out, replaced)
	}
	res := strings.Join(out, "\n")
	res = collapseBlankRuns(res)
	if m := varRe.FindString(res); m != "" {
		return "", fmt.Errorf("unknown placeholder %s", m)
	}
	return res, nil
}

// collapseBlankRuns reduces runs of 3+ newlines (left by dropped
// placeholder-only lines) back to a single blank line.
func collapseBlankRuns(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// withHeader inserts the generated-file marker. YAML frontmatter must stay at
// byte 0 for skill loaders, so when the file starts with a frontmatter block
// the marker goes right after its closing delimiter; otherwise it goes on top.
func withHeader(s, rel string) string {
	header := fmt.Sprintf("<!-- Generated from %s/%s — edit the source and run 'make gen-plugins'. -->",
		srcRoot, filepath.ToSlash(rel))
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---\n"); end >= 0 {
			cut := 4 + end + len("\n---\n")
			return s[:cut] + header + "\n" + s[cut:]
		}
	}
	return header + "\n" + s
}
