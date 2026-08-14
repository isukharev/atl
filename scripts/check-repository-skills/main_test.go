package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryGuidance(t *testing.T) {
	root := repositoryRoot(t)
	result, err := validateRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadCatalog(filepath.Join(root, filepath.FromSlash(catalogPath)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Skills != len(catalog.Skills) || result.Runbooks != len(catalog.Skills) || result.Bytes == 0 {
		t.Fatalf("unexpected report: %+v", result)
	}
}

func TestParseSkillRequiresClosedFrontmatter(t *testing.T) {
	valid := []byte("---\nname: example\ndescription: Example skill.\n---\n\n# Example\n")
	name, description, instructions, err := parseSkill(valid)
	if err != nil || name != "example" || description != "Example skill." || !strings.Contains(instructions, "# Example") {
		t.Fatalf("valid skill parsed incorrectly: %q %q %q %v", name, description, instructions, err)
	}
	for _, body := range [][]byte{
		[]byte("name: example\n"),
		[]byte("---\nname: example\n---\n"),
		[]byte("---\nname: example\ndescription: fine\nextra: no\n---\nbody\n"),
	} {
		if _, _, _, err := parseSkill(body); err == nil {
			t.Fatalf("invalid skill accepted: %q", body)
		}
	}
}

func TestRepositoryGuidanceRejectsRegressions(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, string)
		wantError string
	}{
		{
			name: "activation boundary removed",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, ".agents/skills/atl-develop/SKILL.md"), "Do not use", "Avoid")
			},
			wantError: "activation boundary term",
		},
		{
			name: "unexpected skill",
			mutate: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, ".agents/skills/undeclared"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "undeclared repository skill entry",
		},
		{
			name: "symlinked runbook",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "docs/maintainers/development.md")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("README.md", path); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "runbook",
		},
		{
			name: "client tree collision",
			mutate: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, "skills-src/atl-develop"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "collides with shipped client tree",
		},
		{
			name: "cursor overlay drops AGENTS deferral",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, "CURSOR.md"), "(AGENTS.md)", "(README.md)")
			},
			wantError: "CURSOR.md does not defer to AGENTS.md",
		},
		{
			name: "instruction budget",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "AGENTS.md")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				body = append(body, bytes.Repeat([]byte("x"), maxAgentsBytes)...)
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "instruction budget exceeded",
		},
		{
			name: "missing catalog exclusion",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "docs/catalog.v1.json")
				replaceFile(t, path, "    {\"prefix\":\".agents/skills/\",\"reason\":\"repository-maintainer-skill\"},\n", "")
			},
			wantError: "explicitly exclude",
		},
		{
			name: "runbook leaves maintainer lane",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "docs/catalog.v1.json")
				replaceFile(t, path, `"path":"docs/maintainers/development.md","lane":"maintainers"`, `"path":"docs/maintainers/development.md","lane":"start"`)
			},
			wantError: "maintainers documentation lane",
		},
		{
			name: "activation examples removed",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, ".agents/skills/catalog.v1.json")
				replaceFile(t, path, `"should_activate": ["Fix a bug in an ATL adapter and add regression tests.", "Reorganize ATL public documentation and update its checks."]`, `"should_activate": []`)
			},
			wantError: "positive and two negative",
		},
		{
			name: "wrong catalog exclusion reason",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "docs/catalog.v1.json")
				replaceFile(t, path, `{"prefix":".agents/skills/","reason":"repository-maintainer-skill"}`, `{"prefix":".agents/skills/","reason":"testdata"}`)
			},
			wantError: "explicitly exclude",
		},
		{
			name: "default prompt loses invocation",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, ".agents/skills/atl-develop/agents/openai.yaml"), "$atl-develop", "atl-develop")
			},
			wantError: "default_prompt",
		},
		{
			name: "automation gate removed",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, ".github/workflows/ci.yml"), "run: make check-repository-skills", "run: echo skipped")
			},
			wantError: "exact repository-skill check block",
		},
		{
			name: "make gate bypasses isolated Go environment",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, "Makefile"), "$(GO_ENV) go run ./scripts/check-repository-skills", "go run ./scripts/check-repository-skills")
			},
			wantError: "exact repository-skill check target",
		},
		{
			name: "source ownership direction reversed",
			mutate: func(t *testing.T, root string) {
				replaceFile(t, filepath.Join(root, "docs/plugins.md"), "SOURCE OF TRUTH: edit here, and only here", "generated copy")
			},
			wantError: "client-skill ownership is missing semantic boundary",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyFixture(t)
			test.mutate(t, root)
			_, err := validateRepository(root)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func copyFixture(t *testing.T) string {
	t.Helper()
	source := repositoryRoot(t)
	target := t.TempDir()
	for _, relative := range []string{
		"AGENTS.md", "CLAUDE.md", "CURSOR.md", "Makefile", ".github/workflows/ci.yml", ".github/workflows/release.yml",
		"docs/README.md", "docs/catalog.v1.json", "docs/plugins.md",
		"docs/maintainers", ".agents/skills",
	} {
		if err := copyPath(filepath.Join(source, filepath.FromSlash(relative)), filepath.Join(target, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("copy %s: %v", relative, err)
		}
	}
	return target
}

func copyPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("fixture source contains a symlink")
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fs.ErrInvalid
	}
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, body, 0o600)
}

func replaceFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(body, []byte(old), []byte(replacement), 1)
	if bytes.Equal(body, updated) {
		t.Fatalf("fixture replacement %q was not found in %s", old, path)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}
