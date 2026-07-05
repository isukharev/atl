package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEveryLeafDeclaresArgsPolicy locks issue #67: after defaultNoArgs, no
// leaf command may be left with a nil Args policy — either it is flag-only
// (NoArgs, applied by the walk) or it declares its positional arity
// explicitly next to a `<KEY>`-style Use line. A nil here means a new
// command slipped in with placeholders in Use but no arity declared.
func TestEveryLeafDeclaresArgsPolicy(t *testing.T) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			p := path + " " + strings.Fields(sub.Use)[0]
			if len(sub.Commands()) > 0 {
				walk(sub, p)
				continue
			}
			if sub.Args == nil {
				t.Errorf("leaf command %q (use %q) has no Args policy: declare its arity", p, sub.Use)
			}
		}
	}
	walk(newRoot(), "atl")
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
