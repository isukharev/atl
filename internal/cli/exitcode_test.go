package cli

import (
	"context"
	"io"
	"testing"
)

// runRoot executes the root command with args in an isolated config dir and no
// self-update, returning the mapped exit code for the resulting error.
func runRoot(t *testing.T, args ...string) int {
	t.Helper()
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return codeFor(root.ExecuteContext(context.Background()))
}

// Flag-group violations and flag-parse errors must exit 2 (usage), not 1.
func TestMutuallyExclusiveFlagsExitUsage(t *testing.T) {
	if code := runRoot(t, "conf", "pull", "--id", "1", "--space", "SP"); code != exitUsage {
		t.Fatalf("mutually-exclusive selectors: got exit %d, want %d", code, exitUsage)
	}
}

func TestUnknownFlagExitsUsage(t *testing.T) {
	if code := runRoot(t, "conf", "pull", "--definitely-not-a-flag"); code != exitUsage {
		t.Fatalf("unknown flag: got exit %d, want %d", code, exitUsage)
	}
}

func TestInvalidOutputFormatExitsUsage(t *testing.T) {
	if code := runRoot(t, "--output", "yaml", "version"); code != exitUsage {
		t.Fatalf("invalid --output: got exit %d, want %d", code, exitUsage)
	}
}
