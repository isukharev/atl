package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestRunRejectsMissingAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"evaluate"}, {"aggregate"}, {"inventory"}, {"inventory", "one", "two"}, {"validate-pair"}, {"validate-pair", "one.json"}} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
	}
	if err := run([]string{"inventory", "does-not-exist"}); err == nil {
		t.Fatal("inventory accepted a missing corpus")
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
		"export ATL_READ_ONLY=1",
		"command -v atl",
		"export ATL_READ_ONLY=1\ncommand -v atl\natl config show\natl jira epic digest PROJ-1 --quarter 2026-Q2",
		"export ATL_READ_ONLY=1; command -v atl && atl config show && atl jira epic digest PROJ-1 --quarter 2026-Q2",
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
		"export ATL_READ_ONLY=0",
		"export FOO=1",
		"command -v atl\ncommand -v atl",
		"atl config show\nexport ATL_READ_ONLY=1",
		"export ATL_READ_ONLY=1\natl config show\ncat /etc/passwd",
		"export ATL_READ_ONLY=1 || atl config show",
		"export ATL_READ_ONLY=1 & atl config show",
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
	if string(data) != "{\"decision\":\"allow\",\"family\":\"atl\"}\n" {
		t.Fatalf("counter=%q", data)
	}
}

func TestToolGuardBlocksEveryNonMCPToolInMCPMode(t *testing.T) {
	t.Setenv("ATL_EVAL_GUARD_MODE", "mcp-only")
	t.Setenv("ATL_EVAL_ALLOWED_MCP_TOOLS", `["mcp__atl__jira_fields"]`)
	counter := t.TempDir() + "/guard.jsonl"
	t.Setenv("ATL_EVAL_GUARD_COUNTER", counter)
	tests := []struct {
		input, decision string
	}{
		{`{"tool_name":"mcp__atl__jira_fields","tool_input":{}}`, "allow"},
		{`{"tool_name":"StructuredOutput","tool_input":{}}`, "allow"},
		{`{"tool_name":"mcp__atl__jira_issue_search","tool_input":{}}`, "deny"},
		{`{"tool_name":"Skill","tool_input":{"skill":"atl:jira"}}`, "deny"},
		{`{"tool_name":"ToolSearch","tool_input":{"query":"atl"}}`, "deny"},
		{`{"tool_name":"Bash","tool_input":{"command":"atl jira fields"}}`, "deny"},
		{`{"tool_name":"apply_patch","tool_input":{"patch":"synthetic"}}`, "deny"},
	}
	for _, test := range tests {
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(test.input), &output, &errorOutput); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errorOutput.String())
		}
		if !strings.Contains(output.String(), `"permissionDecision":"`+test.decision+`"`) {
			t.Fatalf("input=%s output=%s", test.input, output.String())
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	var records []guardRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record guardRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != len(tests) {
		t.Fatalf("records=%v", records)
	}
	for index, test := range tests {
		if records[index].Decision != test.decision {
			t.Fatalf("record %d=%q want=%q", index, records[index].Decision, test.decision)
		}
	}
}

func TestToolGuardRejectsMalformedToolNames(t *testing.T) {
	t.Setenv("ATL_EVAL_GUARD_MODE", "mcp-only")
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
	for _, input := range []string{
		`{"tool_name":"","tool_input":{}}`,
		`{"tool_name":"mcp__atl__tool\nInjected","tool_input":{}}`,
	} {
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 {
			if !strings.Contains(errorOutput.String(), "malformed") {
				t.Fatalf("code=%d stderr=%s", code, errorOutput.String())
			}
		} else {
			t.Fatalf("malformed input allowed: %s", input)
		}
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
		"cat " + first + " " + second,
		"sed -n '1,240p' " + first,
		"sed -n '1,240p' " + first + " && sed -n '1,260p' " + second,
		"sed -n '1,240p' " + first + "\nsed -n '1,260p' " + second,
		"wc -l " + first + " " + second,
	} {
		if !allowedSkillReadCommand(command, string(roots)) {
			t.Errorf("expected allow: %s", command)
		}
	}
	for _, command := range []string{
		"cat /etc/passwd", "sed -n '1,20p' /etc/passwd",
		"cat " + first + "; env", "cat " + first + "\nenv", "cat $(env)", "head " + first, "wc -c " + first,
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

func TestPrivateLiveCLIGuardAllowsOnlyOneATLCommandShape(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(skill, []byte("reviewed skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{root})
	t.Setenv("ATL_EVAL_GUARD_MODE", "private-cli")
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))

	for _, input := range []string{
		`{"tool_name":"Read","tool_input":{"file_path":` + strconv.Quote(skill) + `}}`,
		`{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote("sed -n '1,20p' "+skill) + `}}`,
		`{"tool_name":"Bash","tool_input":{"command":"export ATL_READ_ONLY=1; atl jira epic digest PROJ-1 --quarter 2026-Q2"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"export ATL_READ_ONLY=1\ncommand -v atl\natl config show\natl capabilities --task jira/evidence"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"command -v atl"}}`,
	} {
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"allow"`) {
			t.Fatalf("code=%d output=%s stderr=%s", code, output.String(), errorOutput.String())
		}
	}
	for _, command := range []string{
		"sed -n '1,20p' /etc/passwd",
		"cat " + skill + "; env",
		"export ATL_READ_ONLY=1\natl config show\nenv",
		"atl config show\natl capabilities --task jira/evidence; env",
		"atl jira epic digest PROJ-1 | env",
		"atl jira epic digest $(env)",
		"atl jira epic digest PROJ-1; env",
		"env ATL_READ_ONLY=1 atl jira epic digest PROJ-1",
		"/tmp/atl jira epic digest PROJ-1",
	} {
		input := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("command=%q code=%d output=%s stderr=%s", command, code, output.String(), errorOutput.String())
		}
	}
	for _, patch := range []string{
		"*** Begin Patch\n*** Add File: /private/requests/command.json\n+{\"command\":\"atl jira fields\",\"cwd\":\"/workspace\",\"timeout_ms\":30000}\n*** End Patch",
		"*** Begin Patch\n*** Add File: /private/requests/request-00000000000000000000000000000000.json\n+{\"schema_version\":1,\"id\":\"00000000000000000000000000000000\",\"capability\":\"synthetic\",\"args\":[\"jira\",\"fields\"]}\n*** End Patch",
		"*** Begin Patch\n*** Add File: /private/requests/../outside.json\n+{}\n*** End Patch",
		"*** Begin Patch\n*** Add File: /private/requests/one.json\n+{}\n*** Add File: /private/requests/two.json\n+{}\n*** End Patch",
		"*** Begin Patch\n*** Update File: /private/requests/existing.json\n@@\n-{}\n+{\"command\":\"atl jira fields\"}\n*** End Patch",
		"*** Begin Patch\n*** Delete File: /private/requests/existing.json\n*** End Patch",
	} {
		input := `{"tool_name":"apply_patch","tool_input":{"patch":` + strconv.Quote(patch) + `}}`
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("patch=%q code=%d output=%s stderr=%s", patch, code, output.String(), errorOutput.String())
		}
	}
}

func TestPrivateLiveCLIStagesAndBoundsLargeClaudeResults(t *testing.T) {
	resultRoot := t.TempDir()
	if err := os.Chmod(resultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_EVAL_CLI_RESULT_DIR", resultRoot)

	var inline bytes.Buffer
	if err := emitBrokeredCLIStdout([]byte("small\n"), &inline); err != nil || inline.String() != "small\n" {
		t.Fatalf("inline=%q err=%v", inline.String(), err)
	}
	if entries, err := os.ReadDir(resultRoot); err != nil || len(entries) != 0 {
		t.Fatalf("small output was staged: entries=%v err=%v", entries, err)
	}

	payload := bytes.Repeat([]byte("{\"synthetic\":true}\n"), 2_000)
	var pointer bytes.Buffer
	if err := emitBrokeredCLIStdout(payload, &pointer); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(resultRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	resultPath := filepath.Join(resultRoot, entries[0].Name())
	info, err := os.Lstat(resultPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
		t.Fatalf("staged result mode=%v err=%v", info, err)
	}
	staged, err := os.ReadFile(resultPath)
	if err != nil || !bytes.Equal(staged, payload) || !strings.Contains(pointer.String(), resultPath) || bytes.Contains(pointer.Bytes(), payload[:100]) {
		t.Fatalf("staged result binding failed: pointer=%q err=%v", pointer.String(), err)
	}

	guardCounter := filepath.Join(t.TempDir(), "guard.jsonl")
	t.Setenv("ATL_EVAL_GUARD_MODE", "private-cli")
	t.Setenv("ATL_EVAL_GUARD_COUNTER", guardCounter)
	readRoots, _ := json.Marshal([]string{t.TempDir()})
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(readRoots))
	guard := func(path string, offset, limit int) string {
		input, _ := json.Marshal(map[string]any{"tool_name": "Read", "tool_input": map[string]any{
			"file_path": path, "offset": offset, "limit": limit,
		}})
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(bytes.NewReader(input), &output, &errorOutput); code != 0 {
			t.Fatalf("guard code=%d stderr=%s", code, errorOutput.String())
		}
		return output.String()
	}
	if decision := guard(resultPath, 0, privateCLIResultReadLines+1); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("oversized line window admitted: %s", decision)
	}
	outsideRoot := t.TempDir()
	if err := os.Chmod(outsideRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_EVAL_CLI_RESULT_DIR", outsideRoot)
	var outsidePointer bytes.Buffer
	if err := emitBrokeredCLIStdout(payload, &outsidePointer); err != nil {
		t.Fatal(err)
	}
	outsideEntries, _ := os.ReadDir(outsideRoot)
	t.Setenv("ATL_EVAL_CLI_RESULT_DIR", resultRoot)
	if decision := guard(filepath.Join(outsideRoot, outsideEntries[0].Name()), 0, 100); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("cross-run result admitted: %s", decision)
	}
	if err := os.Chmod(resultPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if decision := guard(resultPath, 0, 100); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("mutable result admitted: %s", decision)
	}
	if err := os.WriteFile(resultPath, append([]byte("tampered\n"), payload...), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resultPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if decision := guard(resultPath, 0, 100); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("digest-mismatched result admitted: %s", decision)
	}
	if err := os.Chmod(resultPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, payload, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resultPath, 0o400); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < privateCLIResultReadLimit; attempt++ {
		if decision := guard(resultPath, attempt*100, 100); !strings.Contains(decision, `"permissionDecision":"allow"`) {
			t.Fatalf("bounded result read %d denied: %s", attempt+1, decision)
		}
	}
	if decision := guard(resultPath, 0, 100); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("result-read replay budget was not enforced: %s", decision)
	}
	secondPayload := bytes.Repeat([]byte("{\"synthetic\":false}\n"), 2_000)
	var secondPointer bytes.Buffer
	if err := emitBrokeredCLIStdout(secondPayload, &secondPointer); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(resultRoot)
	if err != nil || len(entries) != 2 {
		t.Fatalf("second staged result entries=%v err=%v", entries, err)
	}
	var secondPath string
	for _, entry := range entries {
		candidate := filepath.Join(resultRoot, entry.Name())
		if candidate != resultPath {
			secondPath = candidate
		}
	}
	if decision := guard(secondPath, 0, 100); !strings.Contains(decision, `"permissionDecision":"deny"`) {
		t.Fatalf("second result bypassed the run-global read budget: %s", decision)
	}
	records, err := os.ReadFile(guardCounter)
	if err != nil || !bytes.Contains(records, []byte(`"family":"tool_result_read"`)) || bytes.Contains(records, []byte(resultPath)) {
		t.Fatalf("content-free result-read audit failed: %s err=%v", records, err)
	}
}

func TestProviderCalibrationGuardAllowsOnlyLiteralATLVersion(t *testing.T) {
	t.Setenv("ATL_EVAL_GUARD_MODE", "provider-calibration")
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "atl version", want: "allow"},
		{command: " atl version", want: "deny"},
		{command: "atl version\n", want: "deny"},
		{command: "command -v atl\natl version", want: "deny"},
		{command: "export ATL_READ_ONLY=1\natl version", want: "deny"},
		{command: "atl version; atl version", want: "deny"},
	} {
		input := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(test.command) + `}}`
		var output, errorOutput bytes.Buffer
		if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 ||
			!strings.Contains(output.String(), `"permissionDecision":"`+test.want+`"`) {
			t.Fatalf("command=%q code=%d output=%s stderr=%s", test.command, code, output.String(), errorOutput.String())
		}
	}
}

func TestATLProxyEnforcesExactPrivateCLIArgumentsAndBudget(t *testing.T) {
	directory := t.TempDir()
	realBinary := filepath.Join(directory, "real-atl")
	executions := filepath.Join(directory, "executions")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nprintf 'executed\\n' >>\"$ATL_EVAL_TEST_EXECUTIONS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := agenteval.CLICommandPolicy{
		SchemaVersion: agenteval.CLICommandPolicySchemaVersion,
		Rules: []agenteval.CLICommandRule{{
			Name: "jira_digest", Command: []string{"jira", "epic", "digest"},
			Positionals:    []agenteval.CLIArgumentRule{{Values: []string{"PROJ-1"}}},
			Flags:          []agenteval.CLIFlagRule{{Name: "--quarter", Values: []string{"2026-Q2"}, Required: true}},
			MaxInvocations: 1,
		}},
	}
	data, err := agenteval.EncodeCLICommandPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(directory, "counter.jsonl")
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", realBinary)
	t.Setenv("ATL_EVAL_COUNTER", counter)
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_TEST_EXECUTIONS", executions)
	allowed := []string{"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2"}
	if code := runATLProxy(allowed); code != 0 {
		t.Fatalf("first invocation code=%d", code)
	}
	if code := runATLProxy(allowed); code == 0 {
		t.Fatal("exhausted invocation budget passed")
	}
	changed := append([]string(nil), allowed...)
	changed[3] = "PROJ-2"
	if code := runATLProxy(changed); code == 0 {
		t.Fatal("changed target passed")
	}
	executed, err := os.ReadFile(executions)
	if err != nil || string(executed) != "executed\n" {
		t.Fatalf("executions=%q err=%v", executed, err)
	}
	record, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(record, []byte(`"command_family":"jira.epic.digest"`)) || bytes.Count(record, []byte(`"denied":true`)) != 2 || bytes.Contains(record, []byte("PROJ-1")) || bytes.Contains(record, []byte("2026-Q2")) {
		t.Fatalf("unsafe or incomplete counter record: %s", record)
	}
}

func TestATLProxySyntheticWritesRequireLoopbackAndReviewedPrefix(t *testing.T) {
	directory := t.TempDir()
	realBinary := filepath.Join(directory, "real-atl")
	executions := filepath.Join(directory, "executions")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nprintf 'executed\\n' >>\"$ATL_EVAL_TEST_EXECUTIONS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_READ_ONLY", "")
	t.Setenv("ATL_EVAL_ALLOW_SYNTHETIC_WRITES", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", realBinary)
	t.Setenv("ATL_EVAL_COUNTER", filepath.Join(directory, "counter.jsonl"))
	t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", `["atl jira issue field set"]`)
	t.Setenv("ATL_EVAL_TEST_EXECUTIONS", executions)
	t.Setenv("ATL_JIRA_URL", "http://127.0.0.1:1234/jira")
	t.Setenv("ATL_CONFLUENCE_URL", "http://localhost:5678/wiki")
	if code := runATLProxy([]string{"jira", "issue", "field", "set", "PROJ-1"}); code != 0 {
		t.Fatalf("reviewed synthetic write code=%d", code)
	}
	if code := runATLProxy([]string{"jira", "issue", "delete", "PROJ-1"}); code == 0 {
		t.Fatal("command outside the reviewed prefix passed")
	}
	t.Setenv("ATL_JIRA_URL", "https://jira.example.test")
	if code := runATLProxy([]string{"jira", "issue", "field", "set", "PROJ-1"}); code == 0 {
		t.Fatal("non-loopback synthetic write passed")
	}
	executed, err := os.ReadFile(executions)
	if err != nil || string(executed) != "executed\n" {
		t.Fatalf("executions=%q err=%v", executed, err)
	}
}

func TestATLProxyDoesNotStartATLWhenCommandBrokerIsMissing(t *testing.T) {
	directory := t.TempDir()
	realBinary := filepath.Join(directory, "real-atl")
	executions := filepath.Join(directory, "executions")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nprintf 'executed\\n' >>\"$ATL_EVAL_TEST_EXECUTIONS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policyData, err := agenteval.EncodeCLICommandPolicy(agenteval.CLICommandPolicy{
		SchemaVersion: agenteval.CLICommandPolicySchemaVersion,
		Rules:         []agenteval.CLICommandRule{{Name: "jira_fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", realBinary)
	t.Setenv("ATL_EVAL_COUNTER", filepath.Join(directory, "counter.jsonl"))
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", filepath.Join(directory, "missing.json"))
	t.Setenv("ATL_EVAL_TEST_EXECUTIONS", executions)
	if code := runATLProxy([]string{"jira", "fields"}); code == 0 {
		t.Fatal("missing command broker passed")
	}
	if _, err := os.Stat(executions); !os.IsNotExist(err) {
		t.Fatalf("real atl started before confinement: %v", err)
	}
}

func TestATLProxyUsesParentCommandBrokerWithoutRealBinaryInEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(directory, "requests")
	responses := filepath.Join(directory, "responses")
	for _, path := range []string{requests, responses} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realBinary := filepath.Join(directory, "real-atl")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nprintf 'brokered output\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := agenteval.CLICommandPolicy{
		SchemaVersion: agenteval.CLICommandPolicySchemaVersion,
		Rules:         []agenteval.CLICommandRule{{Name: "jira_fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}},
	}
	policyData, err := agenteval.EncodeCLICommandPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(directory, "broker.json")
	broker, err := agenteval.StartCommandBroker(agenteval.CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: realBinary, Policy: policy, MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	counter := filepath.Join(requests, "counter.jsonl")
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", "")
	t.Setenv("ATL_EVAL_COUNTER", counter)
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", manifest)
	var output bytes.Buffer
	previous := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	code := runATLProxy([]string{"jira", "fields"})
	_ = writeEnd.Close()
	os.Stdout = previous
	_, _ = io.Copy(&output, readEnd)
	_ = readEnd.Close()
	if code != 0 || output.String() != "brokered output\n" {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
	record, err := os.ReadFile(counter)
	if err != nil || !bytes.Contains(record, []byte(`"command_family":"jira.fields"`)) || bytes.Contains(record, []byte("brokered output")) {
		t.Fatalf("record=%s err=%v", record, err)
	}
}

func TestATLProxyCalibrationRecordsSemanticObservationWithoutValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(directory, "requests")
	responses := filepath.Join(directory, "responses")
	for _, path := range []string{requests, responses} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	versionOutput := []byte("{\n  \"version\": \"test\",\n  \"commit\": \"abc\",\n  \"build_state\": \"clean\"\n}\n")
	realBinary := filepath.Join(directory, "real-atl")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"version\":\"test\",\"commit\":\"abc\",\"build_state\":\"clean\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := agenteval.CLICommandPolicy{SchemaVersion: agenteval.CLICommandPolicySchemaVersion,
		Rules: []agenteval.CLICommandRule{{Name: "atl_version", Command: []string{"version"}, MaxInvocations: 1}}}
	policyData, err := agenteval.EncodeCLICommandPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(directory, "broker.json")
	broker, err := agenteval.StartCommandBroker(agenteval.CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: realBinary, Policy: policy, MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	counter := filepath.Join(requests, "counter.jsonl")
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", "")
	t.Setenv("ATL_EVAL_COUNTER", counter)
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", manifest)
	t.Setenv("ATL_EVAL_GUARD_MODE", "provider-calibration")
	previous := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	code := runATLProxy([]string{"version"})
	_ = writeEnd.Close()
	os.Stdout = previous
	_, _ = io.Copy(io.Discard, readEnd)
	_ = readEnd.Close()
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	recordData, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	var record proxyRecord
	if err := json.Unmarshal(bytes.TrimSpace(recordData), &record); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := agenteval.CalibrationVersionObservationSHA256(versionOutput)
	if err != nil || record.CommandFamily != "atl_version" || record.CalibrationObservationSHA256 != wantDigest ||
		bytes.Contains(recordData, []byte(`"version"`)) || bytes.Contains(recordData, []byte(`"commit"`)) || bytes.Contains(recordData, []byte(`"build_state"`)) {
		t.Fatalf("record=%s want_digest=%q err=%v", recordData, wantDigest, err)
	}
}

func TestCalibrationProxyObservationIsReservedToCalibrationMode(t *testing.T) {
	response := agenteval.CommandBrokerResponse{Status: "executed", Stdout: []byte(`{"version":"test","commit":"abc","build_state":"clean"}`)}
	digest, err := calibrationProxyObservation("atl_version", "provider-calibration", response)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	for _, input := range []struct {
		family, mode string
	}{
		{family: "atl_version", mode: "private-cli"},
		{family: "jira_fields", mode: "provider-calibration"},
	} {
		if got, gotErr := calibrationProxyObservation(input.family, input.mode, response); gotErr != nil || got != "" {
			t.Fatalf("family=%q mode=%q digest=%q err=%v", input.family, input.mode, got, gotErr)
		}
	}
}

func TestCommandBrokerProbeRequiresReadyParentBroker(t *testing.T) {
	directory := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS", listener.Addr().String())
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", filepath.Join(directory, "missing.json"))
	if code := runCommandBrokerProbe(io.Discard); code == 0 {
		t.Fatal("missing broker passed readiness probe")
	}
}

func TestCommandBrokerProbeRejectsAvailableCommandNetwork(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS", listener.Addr().String())
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", filepath.Join(t.TempDir(), "unused.json"))
	if code := runCommandBrokerProbe(io.Discard); code == 0 {
		t.Fatal("probe passed while direct command networking was available")
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
	if code := runSkillReader("cat", []string{inside, outside}, &output, &errorOutput); code == 0 || output.Len() != 0 {
		t.Fatalf("mixed read code=%d output=%q", code, output.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := runSkillReader("cat", []string{inside, inside}, &output, &errorOutput); code != 0 || output.String() != "one\ntwo\nthree\none\ntwo\nthree\n" {
		t.Fatalf("multi read code=%d output=%q stderr=%s", code, output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := runSkillReader("wc", []string{"-l", inside}, &output, &errorOutput); code != 0 || !strings.HasPrefix(output.String(), "3 ") {
		t.Fatalf("wc code=%d output=%q stderr=%s", code, output.String(), errorOutput.String())
	}
}

func TestPrivateSkillReaderResolvesRelativePathsFromReviewedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "jira")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skill, []byte("reviewed skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.md"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{workspace})
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	t.Setenv("ATL_EVAL_WORKSPACE_ROOT", workspace)
	t.Chdir(t.TempDir())
	relative := filepath.Join(".agents", "skills", "jira", "SKILL.md")
	if !allowedSkillReadCommand("sed -n '1,240p' "+relative, string(roots)) {
		t.Fatal("reviewed workspace-relative skill read was denied")
	}
	t.Setenv("ATL_EVAL_GUARD_MODE", "mcp-with-skill-read")
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
	input := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote("sed -n '1,240p' "+relative) + `}}`
	var guardOutput, guardError bytes.Buffer
	if code := runClaudeBashGuard(strings.NewReader(input), &guardOutput, &guardError); code != 0 || !strings.Contains(guardOutput.String(), `"permissionDecision":"allow"`) {
		t.Fatalf("relative guard code=%d output=%s stderr=%s", code, guardOutput.String(), guardError.String())
	}
	var output, errorOutput bytes.Buffer
	if code := runSkillReader("sed", []string{"-n", "1,240p", relative}, &output, &errorOutput); code != 0 || output.String() != "reviewed skill\n" {
		t.Fatalf("relative reader code=%d output=%q stderr=%s", code, output.String(), errorOutput.String())
	}
	if allowedSkillReadCommand("cat "+filepath.Join("escape", "outside.md"), string(roots)) {
		t.Fatal("workspace-relative symlink escape was allowed")
	}
	parentOutside := filepath.Join(filepath.Dir(workspace), "outside.md")
	if err := os.WriteFile(parentOutside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if allowedSkillReadCommand("cat "+filepath.Join("..", "outside.md"), string(roots)) {
		t.Fatal("workspace-relative traversal escape was allowed")
	}
	for _, name := range []string{"&", "atl", "jira", "fields"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if allowedSkillReadCommand("cat "+relative+" & atl jira fields", string(roots)) {
		t.Fatal("standalone shell background operator was allowed")
	}
	t.Setenv("ATL_EVAL_WORKSPACE_ROOT", "")
	if allowedSkillReadCommand("cat "+relative, string(roots)) {
		t.Fatal("relative read without a reviewed workspace was allowed")
	}
	guardOutput.Reset()
	guardError.Reset()
	if code := runClaudeBashGuard(strings.NewReader(input), &guardOutput, &guardError); code != 0 || !strings.Contains(guardOutput.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("missing-base guard code=%d output=%s stderr=%s", code, guardOutput.String(), guardError.String())
	}
	readInput := `{"tool_name":"Read","tool_input":{"file_path":` + strconv.Quote(relative) + `}}`
	guardOutput.Reset()
	guardError.Reset()
	if code := runClaudeBashGuard(strings.NewReader(readInput), &guardOutput, &guardError); code != 0 || !strings.Contains(guardOutput.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("missing-base Read guard code=%d output=%s stderr=%s", code, guardOutput.String(), guardError.String())
	}
}

func TestSyntheticReadGuardKeepsReviewedCWDFallback(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	roots, _ := json.Marshal([]string{workspace})
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	t.Setenv("ATL_EVAL_WORKSPACE_ROOT", "")
	t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
	input := `{"tool_name":"Read","tool_input":{"file_path":"README.md"}}`
	var output, errorOutput bytes.Buffer
	if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"allow"`) {
		t.Fatalf("synthetic relative read code=%d output=%s stderr=%s", code, output.String(), errorOutput.String())
	}
}

func TestPrivateSkillCatAppliesCombinedByteCap(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "one.md"), filepath.Join(root, "two.md")
	if err := os.WriteFile(first, bytes.Repeat([]byte{'a'}, 600<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, bytes.Repeat([]byte{'b'}, 600<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal([]string{root})
	t.Setenv("ATL_EVAL_ALLOWED_READ_ROOTS", string(roots))
	var output, errorOutput bytes.Buffer
	if code := runSkillReader("cat", []string{first, second}, &output, &errorOutput); code == 0 || output.Len() != 0 {
		t.Fatalf("code=%d bytes=%d", code, output.Len())
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
	if string(data) != "{\"decision\":\"allow\",\"family\":\"agent\"}\n{\"decision\":\"deny\",\"family\":\"other\"}\n" {
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
