package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunRejectsMissingAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"evaluate"}, {"aggregate"}} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
	}
	if err := run([]string{"validate", "does-not-exist.json"}); err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaudeBashGuardAllowsOnlyReviewedSingleATLCommands(t *testing.T) {
	prefixes := []string{"atl config show", "atl jira issue fields", "atl jira epic digest"}
	for _, command := range []string{
		"atl config show",
		"atl jira issue fields PROJ-1 --metadata-only",
		"export ATL_READ_ONLY=1; atl jira epic digest PROJ-1 --quarter 2026-Q2",
		"command -v atl",
	} {
		if !allowedGuardCommand(command, prefixes) {
			t.Errorf("expected allow: %q", command)
		}
	}
	for _, command := range []string{
		"cat /etc/passwd", "atl version; cat /etc/passwd", "atl config show | jq .",
		"atl jira issue fields PROJ-1\natl version", "atl conf validate /etc/passwd",
		"atl jira issue fields $(cat /etc/passwd)",
	} {
		if allowedGuardCommand(command, prefixes) {
			t.Errorf("expected deny: %q", command)
		}
	}
}

func TestClaudeBashGuardEmitsPreToolDecision(t *testing.T) {
	t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", `["atl jira issue fields"]`)
	input := `{"tool_name":"Bash","tool_input":{"command":"atl jira issue fields PROJ-1 --metadata-only"}}`
	var output, errorOutput bytes.Buffer
	if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errorOutput.String())
	}
	var result struct {
		Hook struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Hook.Decision != "allow" {
		t.Fatalf("output=%s", output.String())
	}
}
