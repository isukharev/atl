package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEveryExecutableDeclaresArgsPolicy covers ordinary leaves, generated pure
// group fallbacks, and intentional group/leaf hybrids. A nil policy can let a
// hybrid such as `jira export` silently consume an unknown token.
func TestEveryExecutableDeclaresArgsPolicy(t *testing.T) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			p := path + " " + strings.Fields(sub.Use)[0]
			if len(sub.Commands()) > 0 {
				walk(sub, p)
			}
			if (sub.Run != nil || sub.RunE != nil) && sub.Args == nil {
				t.Errorf("executable command %q (use %q) has no Args policy: declare its arity", p, sub.Use)
			}
		}
	}
	root := newRoot()
	if root.Args == nil {
		t.Error("root command has no Args policy")
	}
	walk(root, "atl")
}

func TestHybridCommandArgsPolicies(t *testing.T) {
	root := newRoot()
	transition, _, err := root.Find([]string{"jira", "issue", "transition"})
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Args(transition, []string{"PROJ-1"}); err != nil {
		t.Fatalf("transition rejected its one key: %v", err)
	}
	if err := transition.Args(transition, []string{"PROJ-1", "STRAY"}); err == nil {
		t.Fatal("transition accepted more than its declared key")
	}

	export, _, err := root.Find([]string{"jira", "export"})
	if err != nil {
		t.Fatal(err)
	}
	if err := export.Args(export, []string{"STRAY"}); err == nil {
		t.Fatal("jira export silently accepted an unknown positional token")
	} else if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("jira export error=%v, want Cobra unknown-command usage", err)
	}
}

// TestStrayPositionalArgExits2 pins the behavior change: a stray positional
// on a flag-only command is a usage error (exit 2), not silently dropped.
// Args validation runs before any config/network access, so no env is needed.
func TestStrayPositionalArgExits2(t *testing.T) {
	cases := [][]string{
		{"conf", "search", "--cql", "type=page", "STRAY"},
		{"conf", "pull", "--id", "1", "STRAY"},
		{"conf", "page", "get", "--id", "1", "STRAY"},
		{"conf", "page", "create", "--space", "K", "--title", "T", "STRAY"},
		{"conf", "comment", "list", "--id", "1", "STRAY"},
		{"conf", "space", "tree", "--space", "K", "STRAY"},
		{"jira", "issue", "search", "--jql", "project=X", "PROJ-1"},
		{"jira", "issue", "create", "--project", "P", "--type", "T", "--summary", "S", "STRAY"},
		{"jira", "pull", "--jql", "project=X", "STRAY"},
		{"jira", "fields", "STRAY"},
		{"jira", "transitions", "--key", "PROJ-1", "STRAY"},
		{"config", "show", "STRAY"},
		{"version", "STRAY"},
	}
	for _, args := range cases {
		if code := runRoot(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d (stray arg must be a usage error)",
				args, code, exitUsage)
		}
	}
}
