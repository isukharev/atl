package cli

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestMCPServeRejectsInvalidServiceBeforeRun(t *testing.T) {
	tests := [][]string{
		{"mcp", "serve", "--service", "unknown"},
		{"mcp", "serve", "--service="},
		{"mcp", "serve", "--service", "jira", "--service", "confluence"},
	}
	for _, args := range tests {
		root := newRoot()
		root.SetArgs(args)
		err := root.Execute()
		if !errors.Is(err, domain.ErrUsage) {
			t.Errorf("args=%v error=%v, want usage error", args, err)
		}
	}
}

func TestMCPServiceFlagAcceptsEachClosedProfileOnce(t *testing.T) {
	for _, value := range []string{"jira", "confluence", "offline"} {
		var flag mcpServiceFlag
		if err := flag.Set(value); err != nil {
			t.Errorf("Set(%q): %v", value, err)
		}
		if flag.String() != value {
			t.Errorf("Set(%q) stored %q", value, flag.String())
		}
		if err := flag.Set(value); err == nil {
			t.Errorf("second Set(%q) succeeded", value)
		}
	}
}
