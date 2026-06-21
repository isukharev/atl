package cli

import (
	"testing"
)

// runRoot executes the root command with args in an isolated config dir and no
// self-update, returning the mapped exit code for the resulting error. It is a
// thin wrapper over runCLI (defined in cli_contract_test.go) for the cases that
// only care about the exit code.
func runRoot(t *testing.T, args ...string) int {
	t.Helper()
	_, code := runCLI(t, nil, args...)
	return code
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
