package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSyntheticATLProcessRunsSelectedBinaryCLIAndMCPContracts(t *testing.T) {
	binary, err := filepath.Abs(filepath.Join("..", "..", "atl"))
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(binary); statErr != nil || !info.Mode().IsRegular() {
		t.Skip("repository ATL binary is built by make agent-eval-compat")
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{"summary_only": true, "max_bytes": 1024})
	if !ok {
		t.Fatal("construct MCP invocation")
	}
	response := MockResponse{Status: 200, Body: json.RawMessage(`[
        {"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
        {"id":"customfield_1","name":"Delivery Notes","custom":true,"schema":{"type":"string"}}
    ]`)}
	fixture := MockFixture{
		SchemaVersion: MockFixtureSchemaVersion, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Name: "fields", Method: "GET", Path: "/jira/rest/api/2/field",
			Responses: []MockResponse{response, response},
		}},
		RequestSequence: []string{"fields", "fields"},
	}
	scratch := privateSyntheticScratch(t)
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: fixture, ScratchRoot: scratch,
		CLIPolicy: CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
			Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 1,
		}}},
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })

	cli, err := process.RunCLIJSON(context.Background(), "jira", "fields")
	if err != nil || cli.ExitCode != 0 || len(cli.Stderr) != 0 || !json.Valid(cli.JSON) {
		t.Fatalf("CLI result=%+v err=%v", cli, err)
	}
	mcpResult, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil || mcpResult.IsError || !json.Valid(mcpResult.StructuredContent) ||
		!bytes.Contains(mcpResult.StructuredContent, []byte(`"projection":"summary"`)) {
		t.Fatalf("MCP result=%+v err=%v", mcpResult, err)
	}
	summary := process.Summary()
	if summary.HTTPMethods["GET"] != 2 || summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 1 ||
		summary.CLIInvocations["fields"] != 1 || summary.MCPInvocations["jira_fields"] != 1 ||
		!process.RequestSequenceComplete() {
		t.Fatalf("summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

func TestSyntheticATLProcessSeedsMirrorTemplateBeforeMCPLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	template := filepath.Join(root, "mirror-template")
	if err := os.Mkdir(template, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := digestWorkspaceTree(template)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "mcp" ]; then
  [ "$2" = "serve" ] && [ "$3" = "--service" ] && [ "$4" = "offline" ] || exit 81
  [ -d "$ATL_MIRROR_ROOT" ] && [ ! -L "$ATL_MIRROR_ROOT" ] && [ -f "$ATL_MIRROR_ROOT/seed.txt" ] || exit 82
  [ "$(/bin/cat "$ATL_MIRROR_ROOT/seed.txt")" = "seed" ] || exit 83
  IFS= read -r initialize || exit 84
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 85
  IFS= read -r call || exit 86
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"schema_version\":1}"}],"structuredContent":{"schema_version":1},"isError":false}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 87
`))
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_mirror_snapshot", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch, MirrorTemplate: template,
		MCPService: "offline", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeMirror := filepath.Join(process.runtimeRoot, "mirror")
	inside, containmentErr := pathWithin(process.runtimeRoot, runtimeMirror)
	if containmentErr != nil || !inside || environmentMap(process.environment)["ATL_MIRROR_ROOT"] != runtimeMirror {
		t.Fatalf("mirror runtime escaped process root: root=%q mirror=%q env=%q err=%v",
			process.runtimeRoot, runtimeMirror, environmentMap(process.environment)["ATL_MIRROR_ROOT"], containmentErr)
	}
	templateInfo, err := os.Stat(template)
	if err != nil {
		t.Fatal(err)
	}
	runtimeInfo, err := os.Stat(runtimeMirror)
	if err != nil || os.SameFile(templateInfo, runtimeInfo) {
		t.Fatalf("runtime mirror did not isolate template: info=%v err=%v", runtimeInfo, err)
	}
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil || result.IsError || string(result.StructuredContent) != `{"schema_version":1}` ||
		len(result.TextContent) != 1 || result.TextContent[0] != `{"schema_version":1}` {
		t.Fatalf("MCP result=%+v err=%v", result, err)
	}
	after, err := digestWorkspaceTree(template)
	if err != nil || after != before {
		t.Fatalf("mirror template changed: before=%q after=%q err=%v", before, after, err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("runtime survived close: entries=%v err=%v", entries, err)
	}
}

func TestSyntheticATLProcessRejectsOfflineToolInventoryMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, "#!/bin/sh\n"+testATLCapabilityCatalogHandler()+`
if [ "$1" = "mcp" ]; then
  [ "$4" = "offline" ] || exit 89
  IFS= read -r initialize || exit 90
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 91
  IFS= read -r listed || exit 92
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"jira_mirror_snapshot"}]}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 93
`)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_mirror_snapshot", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		MCPService: "offline", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
	})
	if process != nil || err == nil || !strings.Contains(err.Error(), "tool inventory") {
		t.Fatalf("offline inventory mismatch process=%v err=%v", process, err)
	}
	if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
		t.Fatalf("runtime survived tool inventory mismatch: entries=%v err=%v", entries, readErr)
	}
}

func TestSyntheticATLProcessRejectsExecutionCopyMutationDuringMCPStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, "#!/bin/sh\n"+testATLCapabilityCatalogHandler()+`
if [ "$1" = "mcp" ]; then
  : > "$ATL_CONFIG_DIR/../startup-ready"
  while [ ! -f "$ATL_CONFIG_DIR/../startup-release" ]; do /bin/sleep 0.01; done
  IFS= read -r initialize || exit 94
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 95
`)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	type startResult struct {
		process *SyntheticATLProcess
		err     error
	}
	started := make(chan startResult, 1)
	go func() {
		process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
			Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
			MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
		})
		started <- startResult{process: process, err: err}
	}()

	var runtimeRoot string
	deadline := time.Now().Add(2 * time.Second)
	for runtimeRoot == "" && time.Now().Before(deadline) {
		select {
		case result := <-started:
			t.Fatalf("process stopped before startup mutation: process=%v err=%v", result.process, result.err)
		default:
		}
		entries, err := os.ReadDir(scratch)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			candidate := filepath.Join(scratch, entry.Name())
			if _, err := os.Stat(filepath.Join(candidate, "startup-ready")); err == nil {
				runtimeRoot = candidate
				break
			}
		}
		if runtimeRoot == "" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if runtimeRoot == "" {
		t.Fatal("MCP startup marker was not created")
	}
	executionPath := filepath.Join(runtimeRoot, "selected-atl")
	if err := os.Chmod(executionPath, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(executionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# startup mutation\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "startup-release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-started
	if result.process != nil || result.err == nil || !strings.Contains(result.err.Error(), "private ATL execution copy changed") {
		t.Fatalf("startup mutation process=%v err=%v", result.process, result.err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("runtime survived execution-copy rejection: entries=%v err=%v", entries, err)
	}
}

func TestSyntheticATLProcessRejectsSymlinkMirrorTemplateAndCleansRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	template := filepath.Join(root, "mirror-template")
	if err := os.Mkdir(template, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(template, "escape")); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(root, "mirror-template-link")
	if err := os.Symlink(template, rootLink); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, syntheticATLTestScript("exit 88\n"))
	invocation, ok := newMCPInvocation("jira_mirror_snapshot", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	for name, source := range map[string]string{"nested": template, "root": rootLink} {
		t.Run(name, func(t *testing.T) {
			scratch := filepath.Join(root, "scratch-"+name)
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
				Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch, MirrorTemplate: source,
				MCPService: "offline", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
			}); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("unsafe mirror template error=%v", err)
			}
			if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
				t.Fatalf("runtime survived unsafe template rejection: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestSyntheticATLProcessConfinesEnvironmentAndExactAdmissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	t.Setenv("HOME", "/must-not-reach-child")
	t.Setenv("HTTPS_PROXY", "http://must-not-reach-child.invalid")
	t.Setenv("ATL_PRIVATE_SENTINEL", "must-not-reach-child")
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	script := syntheticATLTestScript(`
[ -z "$HOME" ] || exit 51
[ -z "$HTTPS_PROXY" ] || exit 52
[ -z "$ATL_PRIVATE_SENTINEL" ] || exit 53
[ "$ATL_NO_UPDATE" = "1" ] || exit 54
[ "$ATL_READ_ONLY" = "1" ] || exit 55
[ "$ATL_JIRA_PAT" = "synthetic-jira-token" ] || exit 56
[ -d "$ATL_CONFIG_DIR" ] || exit 57
if [ "$1" = "version" ]; then
  printf '%s\n' '{"version":"synthetic"}'
  exit 0
fi
if [ "$1" = "doctor" ]; then
  printf '%s\n' '{"error":"synthetic"}' >&2
  exit 7
fi
if [ "$1" = "mcp" ]; then
  [ "$2" = "serve" ] && [ "$3" = "--service" ] && [ "$4" = "jira" ] || exit 58
  IFS= read -r initialize || exit 59
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 60
  IFS= read -r call || exit 61
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"schema_version":1,"complete":true},"isError":false}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 62
`)
	writeSyntheticExecutable(t, binary, script)
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct MCP invocation")
	}
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		CLIPolicy: CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
			{Name: "version", Command: []string{"version"}, MaxInvocations: 1},
			{Name: "doctor", Command: []string{"doctor"}, MaxInvocations: 1},
		}},
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := process.RunCLIJSON(context.Background(), "version")
	if err != nil || cli.ExitCode != 0 || string(cli.JSON) != `{"version":"synthetic"}` {
		t.Fatalf("version=%+v err=%v", cli, err)
	}
	if _, err := process.RunCLIJSON(context.Background(), "version"); err == nil || !strings.Contains(err.Error(), "reviewed budget") {
		t.Fatalf("second version error=%v", err)
	}
	failed, err := process.RunCLIJSON(context.Background(), "doctor")
	if err != nil || failed.ExitCode != 7 || failed.JSON != nil || !bytes.Contains(failed.Stderr, []byte(`"error":"synthetic"`)) {
		t.Fatalf("doctor=%+v err=%v", failed, err)
	}
	mcpResult, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil || mcpResult.IsError || string(mcpResult.StructuredContent) != `{"schema_version":1,"complete":true}` ||
		len(mcpResult.TextContent) != 1 || mcpResult.TextContent[0] != "ok" {
		t.Fatalf("MCP result=%+v err=%v", mcpResult, err)
	}
	if _, err := process.CallMCPJSON(context.Background(), invocation); err == nil || !strings.Contains(err.Error(), "reviewed budget") {
		t.Fatalf("second MCP error=%v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch entries=%v err=%v", entries, err)
	}
}

func TestSyntheticATLProcessPreservesOpaqueCLIBytesAndSingleAccounting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "raw" ]; then
  printf '\377\000raw \n'
  printf '\376\000warning\n' >&2
  exit 0
fi
if [ "$1" = "failed" ]; then
  printf '\375failure-out\000'
  printf '\374failure-err\000' >&2
  exit 7
fi
exit 78
`))
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		CLIPolicy: CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
			{Name: "raw", Command: []string{"raw"}, MaxInvocations: 1},
			{Name: "failed", Command: []string{"failed"}, MaxInvocations: 1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })

	raw, err := process.RunCLIBytes(context.Background(), "raw")
	if err != nil || raw.ExitCode != 0 ||
		!bytes.Equal(raw.Stdout, []byte{0xff, 0x00, 'r', 'a', 'w', ' ', '\n'}) ||
		!bytes.Equal(raw.Stderr, []byte{0xfe, 0x00, 'w', 'a', 'r', 'n', 'i', 'n', 'g', '\n'}) {
		t.Fatalf("raw result=%+v err=%v", raw, err)
	}
	failed, err := process.RunCLIBytes(context.Background(), "failed")
	if err != nil || failed.ExitCode != 7 ||
		!bytes.Equal(failed.Stdout, append([]byte{0xfd}, []byte("failure-out\x00")...)) ||
		!bytes.Equal(failed.Stderr, append([]byte{0xfc}, []byte("failure-err\x00")...)) {
		t.Fatalf("failed raw result=%+v err=%v", failed, err)
	}
	if _, err := process.RunCLIBytes(context.Background(), "other"); err == nil ||
		!strings.Contains(err.Error(), "reviewed budget") {
		t.Fatalf("unadmitted raw command error=%v", err)
	}
	if _, err := process.RunCLIJSON(context.Background(), "raw"); err == nil ||
		!strings.Contains(err.Error(), "reviewed budget") {
		t.Fatalf("shared raw/JSON budget error=%v", err)
	}
	if summary := process.Summary(); !equalHTTPMethods(summary.CLIInvocations, map[string]int{"raw": 1, "failed": 1}) ||
		len(summary.HTTPMethods) != 0 {
		t.Fatalf("raw accounting drifted: %+v", summary)
	}
}

func TestSyntheticATLProcessPreflightsBeforeRuntimeAndRevalidatesBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture and symlink assertions are Unix-only")
	}
	root := privateSyntheticScratch(t)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(root, "invalid-atl")
	writeSyntheticExecutable(t, invalid, "#!/bin/sh\nprintf '%s\\n' '{}'\n")
	_, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: invalid, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		CLIPolicy: oneSyntheticVersionPolicy(),
	})
	if err == nil || !strings.Contains(err.Error(), "capability catalog") {
		t.Fatalf("preflight error=%v", err)
	}
	if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
		t.Fatalf("runtime existed before preflight: entries=%v err=%v", entries, readErr)
	}

	target := filepath.Join(root, "atl-target")
	writeSyntheticExecutable(t, target, syntheticATLTestScript(`
if [ "$1" = "version" ]; then printf '%s\n' '{"version":"one"}'; exit 0; fi
exit 63
`))
	selected := filepath.Join(root, "atl-selected")
	if err := os.Symlink(target, selected); err != nil {
		t.Fatal(err)
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: selected, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		CLIPolicy: oneSyntheticVersionPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# changed\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := process.RunCLIBytes(context.Background(), "version"); err == nil || !strings.Contains(err.Error(), "binary changed") {
		t.Fatalf("changed binary error=%v", err)
	}
	if err := process.Close(); err == nil || !strings.Contains(err.Error(), "binary changed") {
		t.Fatalf("close changed binary error=%v", err)
	}
	if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
		t.Fatalf("runtime survived changed-binary close: entries=%v err=%v", entries, readErr)
	}
}

func TestSyntheticATLProcessRejectsCLIOutputAndTimeBounds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for name, test := range map[string]struct {
		body string
		raw  bool
	}{
		"multiple JSON":   {body: `printf '%s\n' '{}' '{}'; exit 0`},
		"duplicate JSON":  {body: `printf '%s\n' '{"value":1,"value":2}'; exit 0`},
		"stdout overflow": {body: `i=0; while [ "$i" -lt 100 ]; do printf '0123456789'; i=$((i + 1)); done; exit 0`, raw: true},
		"stderr overflow": {body: `i=0; while [ "$i" -lt 100 ]; do printf '0123456789' >&2; i=$((i + 1)); done; exit 0`, raw: true},
		"timeout":         {body: `exec /bin/sleep 10`, raw: true},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateSyntheticScratch(t)
			binary := filepath.Join(root, "atl-fake")
			writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "version" ]; then `+test.body+`; fi
exit 64
`))
			scratch := filepath.Join(root, "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
				Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
				CLIPolicy: oneSyntheticVersionPolicy(), Timeout: time.Second,
				MaxStdoutBytes: 64, MaxStderrBytes: 64,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.raw {
				_, err = process.RunCLIBytes(context.Background(), "version")
			} else {
				_, err = process.RunCLIJSON(context.Background(), "version")
			}
			if err == nil {
				t.Fatal("invalid bounded CLI result passed")
			}
			if _, retryErr := process.RunCLIJSON(context.Background(), "version"); retryErr == nil ||
				!strings.Contains(retryErr.Error(), "reviewed budget") {
				t.Fatalf("failed attempt did not consume its budget: %v", retryErr)
			}
			if summary := process.Summary(); !equalHTTPMethods(summary.CLIInvocations, map[string]int{"version": 1}) {
				t.Fatalf("failed JSON/raw interpretation executed more than once: %+v", summary)
			}
			if closeErr := process.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestSyntheticATLProcessBoundsAndReapsRunningMCPChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	validResponsePrefix := `printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
IFS= read -r initialized || exit 70
IFS= read -r call || exit 71
`
	for name, test := range map[string]struct {
		body       string
		maxMCP     int64
		maxStderr  int64
		wantError  string
		closeFails bool
	}{
		"total stdout": {
			maxMCP: 2048, maxStderr: 4096, wantError: "total output bound",
			body: validResponsePrefix + `printf '%s' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"'
i=0; while [ "$i" -lt 1800 ]; do printf 'x'; i=$((i + 1)); done
printf '%s\n' '"}],"structuredContent":{"schema_version":1,"complete":true},"isError":false}}'
exec /bin/sleep 10`,
		},
		"stderr": {
			maxMCP: 4096, maxStderr: 64, wantError: "stderr exceeded",
			body: validResponsePrefix + `i=0; while [ "$i" -lt 100 ]; do printf '0123456789' >&2; i=$((i + 1)); done
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"schema_version":1},"isError":false}}'
exec /bin/sleep 10`,
		},
		"read timeout": {
			maxMCP: 4096, maxStderr: 4096, wantError: "deadline exceeded",
			body: validResponsePrefix + `exec /bin/sleep 10`,
		},
		"unexpected nonzero exit": {
			maxMCP: 4096, maxStderr: 4096, wantError: "read ATL MCP response", closeFails: true,
			body: validResponsePrefix + `exit 9`,
		},
		"notification overflow": {
			maxMCP: 8192, maxStderr: 4096, wantError: "notification bound",
			body: validResponsePrefix + `i=0; while [ "$i" -lt 33 ]; do
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"synthetic","progress":1}}'
  i=$((i + 1))
done
exec /bin/sleep 10`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateSyntheticScratch(t)
			binary := filepath.Join(root, "atl-fake")
			writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 69
  `+test.body+`
fi
exit 72
`))
			scratch := filepath.Join(root, "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
			if !ok {
				t.Fatal("construct invocation")
			}
			process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
				Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
				MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
				MaxMCPBytes: test.maxMCP, MaxStderrBytes: test.maxStderr,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := process.CallMCPJSON(context.Background(), invocation); err == nil ||
				!strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("MCP runtime error=%v want=%q", err, test.wantError)
			}
			if _, retryErr := process.CallMCPJSON(context.Background(), invocation); retryErr == nil ||
				!strings.Contains(retryErr.Error(), "reviewed budget") {
				t.Fatalf("failed MCP attempt did not consume its budget: %v", retryErr)
			}
			closeErr := process.Close()
			if test.closeFails != (closeErr != nil) {
				t.Fatalf("close error=%v want_failure=%t", closeErr, test.closeFails)
			}
			if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
				t.Fatalf("runtime survived MCP failure: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestSyntheticATLProcessHonorsConfiguredMCPMessageBoundAboveOneMiB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 69
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 70
  IFS= read -r call || exit 71
  printf '%s' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"payload":"'
  head -c 1100000 /dev/zero | tr '\000' x
  printf '%s\n' '"},"isError":false}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 72
`))
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: 10 * time.Second,
		MaxMCPBytes: 2 << 20, MaxStderrBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.StructuredContent) <= 1<<20 {
		t.Fatalf("structured content bytes=%d, want above the former fixed ceiling", len(result.StructuredContent))
	}
}

func TestSyntheticATLProcessPreservesMCPApplicationError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 73
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 74
  IFS= read -r call || exit 75
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"bounded failure"}],"isError":true}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 76
`))
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct invocation")
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil || !result.IsError || result.StructuredContent != nil ||
		len(result.TextContent) != 1 || result.TextContent[0] != "bounded failure" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticATLProcessRejectsSelectedSymlinkRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture and symlink assertions are Unix-only")
	}
	root := privateSyntheticScratch(t)
	script := syntheticATLTestScript(`
if [ "$1" = "version" ]; then printf '%s\n' '{"version":"one"}'; exit 0; fi
exit 77
`)
	first := filepath.Join(root, "atl-first")
	second := filepath.Join(root, "atl-second")
	writeSyntheticExecutable(t, first, script)
	writeSyntheticExecutable(t, second, script)
	selected := filepath.Join(root, "atl-selected")
	if err := os.Symlink(first, selected); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: selected, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		CLIPolicy: oneSyntheticVersionPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, selected); err != nil {
		t.Fatal(err)
	}
	if _, err := process.RunCLIJSON(context.Background(), "version"); err == nil || !strings.Contains(err.Error(), "binary changed") {
		t.Fatalf("retarget error=%v", err)
	}
	if err := process.Close(); err == nil || !strings.Contains(err.Error(), "binary changed") {
		t.Fatalf("retarget close error=%v", err)
	}
}

func TestSyntheticATLProcessReapsMCPOnBoundOrTimeoutFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for name, mcpBody := range map[string]string{
		"message overflow": `i=0; while [ "$i" -lt 200 ]; do printf '0123456789'; i=$((i + 1)); done; printf '\n'; exec /bin/sleep 10`,
		"timeout":          `exec /bin/sleep 10`,
	} {
		t.Run(name, func(t *testing.T) {
			root := privateSyntheticScratch(t)
			binary := filepath.Join(root, "atl-fake")
			writeSyntheticExecutable(t, binary, syntheticATLTestScript(`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 65
  `+mcpBody+`
fi
exit 66
`))
			scratch := filepath.Join(root, "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
			if !ok {
				t.Fatal("construct invocation")
			}
			_, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
				Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
				MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
				MaxMCPBytes: 512, MaxStderrBytes: 64,
			})
			if err == nil {
				t.Fatal("failed MCP initialization passed")
			}
			if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
				t.Fatalf("MCP runtime survived failure: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func privateSyntheticScratch(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSyntheticExecutable(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
		t.Fatal(err)
	}
}

func syntheticATLTestScript(body string) string {
	body = strings.ReplaceAll(body, "IFS= read -r initialized", `IFS= read -r initialized
  IFS= read -r listed || exit 127
  synthetic_mcp_tools_list "$4"`)
	body = strings.ReplaceAll(body, `"id":2`, `"id":3`)
	return "#!/bin/sh\n" + testATLCapabilityCatalogHandler() + testSyntheticMCPToolInventoryHandler() + body
}

func testSyntheticMCPToolInventoryHandler() string {
	var script strings.Builder
	script.WriteString("synthetic_mcp_tools_list() {\n  case \"$1\" in\n")
	for _, profile := range []string{"jira", "confluence", "offline"} {
		script.WriteString("    " + profile + ") printf '%s\\n' '")
		script.WriteString(syntheticMCPToolInventoryResponse(profile))
		script.WriteString("' ;;\n")
	}
	script.WriteString("    *) exit 127 ;;\n  esac\n}\n")
	return script.String()
}

func syntheticMCPToolInventoryResponse(profile string) string {
	expected, ok := PinnedCapabilityCatalog().mcpToolsForProfile(profile)
	if !ok {
		panic("unknown synthetic MCP profile")
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]map[string]string, len(names))
	for index, name := range names {
		tools[index] = map[string]string{"name": name}
	}
	result, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		panic(err)
	}
	return `{"jsonrpc":"2.0","id":2,"result":` + string(result) + `}`
}

func minimalSyntheticFixture() MockFixture {
	return MockFixture{
		SchemaVersion: MockFixtureSchemaVersion, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Name: "unused", Method: "GET", Path: "/jira/rest/api/2/unused",
			Status: 200, Body: json.RawMessage(`{}`),
		}},
	}
}

func oneSyntheticVersionPolicy() CLICommandPolicy {
	return CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "version", Command: []string{"version"}, MaxInvocations: 1,
	}}}
}
