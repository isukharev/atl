package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/capability"
)

func TestCapabilityReferencesResolveContainedRegularSourceFiles(t *testing.T) {
	root := capabilityReferenceFixture(t)
	definitions := []capability.Definition{
		{ID: "jira.command", Skill: "jira", Reference: "reference/commands.md"},
		{ID: "jira.skill", Skill: "jira", Reference: "SKILL.md"},
	}
	if err := validateCapabilityReferences(root, definitions); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityReferencesRejectPathAndFileDrift(t *testing.T) {
	for _, test := range []struct {
		name       string
		definition capability.Definition
		mutate     func(*testing.T, string)
		wantError  string
	}{
		{
			name: "missing reference", definition: capability.Definition{ID: "missing", Skill: "jira", Reference: "reference/missing.md"},
			wantError: "unavailable",
		},
		{
			name: "escaping reference", definition: capability.Definition{ID: "escape", Skill: "jira", Reference: "../outside.md"},
			wantError: "contained canonical",
		},
		{
			name: "nested skill", definition: capability.Definition{ID: "skill", Skill: "nested/jira", Reference: "SKILL.md"},
			wantError: "canonical source skill",
		},
		{
			name: "directory target", definition: capability.Definition{ID: "directory", Skill: "jira", Reference: "reference/directory.md"},
			mutate: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "jira", "reference", "directory.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "not a regular file",
		},
		{
			name: "symlinked reference", definition: capability.Definition{ID: "symlink", Skill: "jira", Reference: "reference/commands.md"},
			mutate: func(t *testing.T, root string) {
				target := filepath.Join(root, "jira", "reference", "commands.md")
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "outside.md"), target); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
			wantError: "symbolic link",
		},
		{
			name: "symlinked parent", definition: capability.Definition{ID: "symlink-parent", Skill: "jira", Reference: "reference/commands.md"},
			mutate: func(t *testing.T, root string) {
				reference := filepath.Join(root, "jira", "reference")
				if err := os.RemoveAll(reference); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(root, "outside-reference")
				if err := os.Mkdir(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outside, "commands.md"), []byte("# Outside\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, reference); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
			wantError: "symbolic link",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := capabilityReferenceFixture(t)
			if test.mutate != nil {
				test.mutate(t, root)
			}
			err := validateCapabilityReferences(root, []capability.Definition{test.definition})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error=%v want substring %q", err, test.wantError)
			}
		})
	}
}

func capabilityReferenceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		filepath.Join(root, "jira", "SKILL.md"):                 "# Jira\n",
		filepath.Join(root, "jira", "reference", "commands.md"): "# Commands\n",
		filepath.Join(root, "outside.md"):                       "# Outside\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
