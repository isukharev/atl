package cli

import "testing"

func TestConfluenceCommentMutationValidationRunsBeforeSetup(t *testing.T) {
	for name, args := range map[string][]string{
		"missing ids": {"conf", "comment", "mutation", "preview", "--operation", "resolve"},
		"operation":   {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "delete"},
		"reply body":  {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "reply"},
	} {
		t.Run(name, func(t *testing.T) {
			_, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": t.TempDir()}, args...)
			if code != exitUsage {
				t.Fatalf("exit=%d want %d", code, exitUsage)
			}
		})
	}
}

func TestConfluenceCommentMutationApplyIsReadOnlyBlockedBeforeInputs(t *testing.T) {
	_, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1", "ATL_CONFIG_DIR": t.TempDir()},
		"conf", "comment", "mutation", "apply", "--id", "1", "--thread-id", "2", "--operation", "reply", "--from-file", "-")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d want %d", code, exitCheckFailed)
	}
}

func TestConfluenceCommentMutationIsJSONOnly(t *testing.T) {
	_, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": t.TempDir()},
		"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "resolve", "-o", "text")
	if code != exitUsage {
		t.Fatalf("exit=%d want %d", code, exitUsage)
	}
}
