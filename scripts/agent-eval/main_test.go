package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
		"atl --read-only jira issue fields PROJ-1 --metadata-only",
		"atl jira issue fields PROJ-1 --metadata-only",
		"ATL_READ_ONLY=1 atl jira issue fields PROJ-1 --metadata-only",
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
		"ATL_READ_ONLY=0 atl jira issue fields PROJ-1",
		"FOO=1 atl jira issue fields PROJ-1",
	} {
		if allowedGuardCommand(command, prefixes) {
			t.Errorf("expected deny: %q", command)
		}
	}
}

func TestClaudeBashGuardEmitsPreToolDecision(t *testing.T) {
	t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", `["atl jira issue fields"]`)
	counter := t.TempDir() + "/guard.jsonl"
	t.Setenv("ATL_EVAL_GUARD_COUNTER", counter)
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
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"decision\":\"allow\"}\n" {
		t.Fatalf("counter=%q", data)
	}
}

func TestToolGuardBlocksEveryNonMCPToolInMCPMode(t *testing.T) {
	t.Setenv("ATL_EVAL_GUARD_MODE", "mcp-only")
	t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", `["atl jira fields"]`)
	counter := t.TempDir() + "/guard.jsonl"
	t.Setenv("ATL_EVAL_GUARD_COUNTER", counter)
	for _, input := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"atl jira fields"}}`,
		`{"tool_name":"apply_patch","tool_input":{"patch":"synthetic"}}`,
	} {
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errorOutput.String())
		}
		if !strings.Contains(output.String(), `"permissionDecision":"deny"`) || !strings.Contains(output.String(), "typed-MCP") {
			t.Fatalf("output=%s", output.String())
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"decision\":\"deny\"}\n{\"decision\":\"deny\"}\n" {
		t.Fatalf("counter=%q", data)
	}
}

func TestPrivateLiveGuardAllowsOnlyConfinedSkillReaders(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "jira", "SKILL.md")
	second := filepath.Join(root, "confluence", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(first), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(second), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("jira\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("confluence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{root})
	for _, command := range []string{
		"cat " + first,
		"sed -n '1,240p' " + first,
		"sed -n '1,240p' " + first + " && sed -n '1,260p' " + second,
		"wc -l " + first + " " + second,
	} {
		if !allowedSkillReadCommand(command, string(roots)) {
			t.Errorf("expected allow: %s", command)
		}
	}
	for _, command := range []string{
		"cat /etc/passwd", "sed -n '1,20p' /etc/passwd",
		"cat " + first + "; env", "cat $(env)", "head " + first, "wc -c " + first,
	} {
		if allowedSkillReadCommand(command, string(roots)) {
			t.Errorf("expected deny: %s", command)
		}
	}
	t.Setenv("ATL_EVAL_GUARD_MODE", "mcp-with-skill-read")
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
	input := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote("sed -n '1,240p' "+first) + `}}`
	var output, errorOutput bytes.Buffer
	if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"allow"`) {
		t.Fatalf("code=%d output=%s stderr=%s", code, output.String(), errorOutput.String())
	}
}

func TestPrivateSkillReaderCannotEscapeRoots(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "SKILL.md")
	outside := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(inside, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{root})
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	var output, errorOutput bytes.Buffer
	if code := runSkillReader("sed", []string{"-n", "2,3p", inside}, &output, &errorOutput); code != 0 || output.String() != "two\nthree\n" {
		t.Fatalf("code=%d output=%q stderr=%s", code, output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := runSkillReader("cat", []string{outside}, &output, &errorOutput); code == 0 || output.Len() != 0 {
		t.Fatalf("outside read code=%d output=%q", code, output.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := runSkillReader("wc", []string{"-l", inside}, &output, &errorOutput); code != 0 || !strings.HasPrefix(output.String(), "3 ") {
		t.Fatalf("wc code=%d output=%q stderr=%s", code, output.String(), errorOutput.String())
	}
}

func TestClaudeGuardEnforcesDelegationLimitWithoutRecordingInput(t *testing.T) {
	counter := t.TempDir() + "/guard.jsonl"
	t.Setenv("ATL_EVAL_GUARD_COUNTER", counter)
	t.Setenv("ATL_EVAL_MAX_DELEGATIONS", "1")
	input := `{"tool_name":"Agent","tool_input":{"prompt":"synthetic secret that must not be retained"}}`
	for index, want := range []string{"allow", "deny"} {
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 {
			t.Fatalf("run %d code=%d stderr=%s", index, code, errorOutput.String())
		}
		if !strings.Contains(output.String(), `"permissionDecision":"`+want+`"`) {
			t.Fatalf("run %d output=%s", index, output.String())
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"decision\":\"allow\"}\n{\"decision\":\"deny\"}\n" {
		t.Fatalf("counter=%q", data)
	}
	if bytes.Contains(data, []byte("synthetic secret")) {
		t.Fatal("guard retained tool input")
	}
}

func TestClaudeGuardReadPolicyContainsResolvedTargets(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "reference.md")
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(inside, []byte("public reference"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{root})
	allowed, err := allowedReadPath(inside, string(roots))
	if err != nil || !allowed {
		t.Fatalf("inside allowed=%v err=%v", allowed, err)
	}
	allowed, err = allowedReadPath(outside, string(roots))
	if err != nil || allowed {
		t.Fatalf("outside allowed=%v err=%v", allowed, err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err == nil {
		allowed, err = allowedReadPath(link, string(roots))
		if err != nil || allowed {
			t.Fatalf("symlink allowed=%v err=%v", allowed, err)
		}
	}
}
