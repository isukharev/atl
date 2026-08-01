package cli

import "testing"

func TestConfluenceCommentMutationValidationRunsBeforeSetup(t *testing.T) {
	for name, args := range map[string][]string{
		"missing page":        {"conf", "comment", "mutation", "preview", "--operation", "resolve"},
		"missing thread":      {"conf", "comment", "mutation", "preview", "--id", "1", "--operation", "resolve"},
		"operation":           {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "delete"},
		"internal operation":  {"conf", "comment", "mutation", "preview", "--id", "1", "--operation", "inline_create", "--from-file", "body.csf", "--selection-file", "selection.txt"},
		"reply body":          {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "reply"},
		"inline inputs":       {"conf", "comment", "mutation", "preview", "--id", "1", "--operation", "inline-create"},
		"inline thread":       {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "inline-create", "--from-file", "body.csf", "--selection-file", "selection.txt"},
		"selection for reply": {"conf", "comment", "mutation", "preview", "--id", "1", "--thread-id", "2", "--operation", "reply", "--from-file", "body.csf", "--selection-file", "selection.txt"},
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
