package agenteval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSyntheticMCPResourceInventoryIsExactAndClosed(t *testing.T) {
	valid := syntheticMCPResourceInventoryResultForTest()
	if err := validateSyntheticMCPResourceInventory(valid); err != nil {
		t.Fatalf("released resource inventory rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"missing resource": func(result map[string]any) {
			result["resources"] = result["resources"].([]any)[:1]
		},
		"extra resource": func(result map[string]any) {
			resources := result["resources"].([]any)
			result["resources"] = append(resources, resources[1])
		},
		"reordered resources": func(result map[string]any) {
			resources := result["resources"].([]any)
			result["resources"] = []any{resources[1], resources[0]}
		},
		"unknown result member": func(result map[string]any) {
			result["nextCursor"] = "more"
		},
		"missing ttl": func(result map[string]any) {
			delete(result, "ttlMs")
		},
		"null ttl": func(result map[string]any) {
			result["ttlMs"] = nil
		},
		"string ttl": func(result map[string]any) {
			result["ttlMs"] = "0"
		},
		"fractional ttl": func(result map[string]any) {
			result["ttlMs"] = 0.5
		},
		"null resources": func(result map[string]any) {
			result["resources"] = nil
		},
		"nonzero ttl": func(result map[string]any) {
			result["ttlMs"] = 1
		},
		"private cache": func(result map[string]any) {
			result["cacheScope"] = "private"
		},
		"missing cache": func(result map[string]any) {
			delete(result, "cacheScope")
		},
		"null cache": func(result map[string]any) {
			result["cacheScope"] = nil
		},
		"extra descriptor member": func(result map[string]any) {
			result["resources"].([]any)[1].(map[string]any)["size"] = 1
		},
		"null descriptor member": func(result map[string]any) {
			result["resources"].([]any)[1].(map[string]any)["name"] = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := syntheticMCPResourceInventoryValueForTest()
			mutate(result)
			if err := validateSyntheticMCPResourceInventory(marshalSyntheticMCPTestValue(t, result)); err == nil {
				t.Fatal("resource inventory drift passed")
			}
		})
	}

	duplicate := strings.Replace(string(valid), `"name":"atl-runtime"`,
		`"name":"atl-runtime","name":"atl-runtime"`, 1)
	if err := validateSyntheticMCPResourceInventory([]byte(duplicate)); err == nil {
		t.Fatal("duplicate resource descriptor member passed")
	}
	decimalTTL := strings.Replace(string(valid), `"ttlMs":0`, `"ttlMs":0.0`, 1)
	if err := validateSyntheticMCPResourceInventory([]byte(decimalTTL)); err == nil {
		t.Fatal("non-integer lexical cache TTL passed")
	}
	exponentTTL := strings.Replace(string(valid), `"ttlMs":0`, `"ttlMs":0e0`, 1)
	if err := validateSyntheticMCPResourceInventory([]byte(exponentTTL)); err == nil {
		t.Fatal("exponent cache TTL passed")
	}
	duplicateTTL := strings.Replace(string(valid), `"ttlMs":0`, `"ttlMs":0,"ttlMs":0`, 1)
	if err := validateSyntheticMCPResourceInventory([]byte(duplicateTTL)); err == nil {
		t.Fatal("duplicate cache TTL passed")
	}
}

func TestSyntheticMCPRuntimeProjectionIsExactForEveryService(t *testing.T) {
	for _, service := range []string{"default", "jira", "confluence", "offline"} {
		t.Run(service, func(t *testing.T) {
			result := syntheticMCPRuntimeReadResultForTest(service, "private", nil)
			if err := validateSyntheticMCPRuntimeResource(result, service); err != nil {
				t.Fatalf("released runtime resource rejected: %v", err)
			}
		})
	}
}

func TestSyntheticMCPRuntimeProjectionRejectsSchemaAndSemanticDrift(t *testing.T) {
	for name, test := range map[string]struct {
		expected string
		cache    string
		mutate   func(map[string]any)
	}{
		"service mismatch": {
			expected: "jira", mutate: func(value map[string]any) { value["service_profile"] = "confluence" },
		},
		"public read cache": {expected: "jira", cache: "public"},
		"extra root member": {
			expected: "jira", mutate: func(value map[string]any) { value["extra"] = true },
		},
		"missing root member": {
			expected: "jira", mutate: func(value map[string]any) { delete(value, "lifecycle") },
		},
		"null scalar": {
			expected: "jira", mutate: func(value map[string]any) { value["access"] = nil },
		},
		"invalid service enum": {
			expected: "jira", mutate: func(value map[string]any) { value["service_profile"] = "all" },
		},
		"invalid policy enum": {
			expected: "jira", mutate: func(value map[string]any) {
				value["global_read_only_policy"].(map[string]any)["read_only_source"] = "ambient"
			},
		},
		"null false boolean": {
			expected: "jira", mutate: func(value map[string]any) {
				value["global_read_only_policy"].(map[string]any)["configured_read_only"] = nil
			},
		},
		"contradictory inactive policy": {
			expected: "jira", mutate: func(value map[string]any) {
				value["global_read_only_policy"].(map[string]any)["read_only_source"] = "none"
			},
		},
		"contradictory configuration source": {
			expected: "jira", mutate: func(value map[string]any) {
				value["global_read_only_policy"].(map[string]any)["read_only_source"] = "configuration"
			},
		},
		"valid but wrong synthetic policy": {
			expected: "jira", mutate: func(value map[string]any) {
				policy := value["global_read_only_policy"].(map[string]any)
				policy["configured_read_only"] = true
				policy["read_only_source"] = "configuration"
			},
		},
		"invalid plugin enum": {
			expected: "jira", mutate: func(value map[string]any) {
				value["plugin"].(map[string]any)["interface_contract"] = "unknown"
			},
		},
		"contradictory plugin status": {
			expected: "jira", mutate: func(value map[string]any) {
				value["plugin"].(map[string]any)["interface_contract"] = "compatible"
			},
		},
		"valid but marked plugin": {
			expected: "jira", mutate: func(value map[string]any) {
				plugin := value["plugin"].(map[string]any)
				plugin["interface_contract"] = "compatible"
				plugin["product_version"] = "match"
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cache := test.cache
			if cache == "" {
				cache = "private"
			}
			result := syntheticMCPRuntimeReadResultForTest(test.expected, cache, test.mutate)
			if err := validateSyntheticMCPRuntimeResource(result, test.expected); err == nil {
				t.Fatal("runtime resource drift passed")
			}
		})
	}

	projection := marshalSyntheticMCPTestValue(t, syntheticMCPRuntimeProjectionValueForTest("jira"))
	duplicate := strings.Replace(string(projection), `"access":"hard_read_only"`,
		`"access":"hard_read_only","access":"hard_read_only"`, 1)
	if err := validateSyntheticMCPRuntimeProjection([]byte(duplicate), "jira"); err == nil {
		t.Fatal("duplicate runtime projection member passed")
	}
	duplicateNested := strings.Replace(string(projection), `"read_only_source":"environment"`,
		`"read_only_source":"environment","read_only_source":"environment"`, 1)
	if err := validateSyntheticMCPRuntimeProjection([]byte(duplicateNested), "jira"); err == nil {
		t.Fatal("duplicate nested runtime projection member passed")
	}
}

func TestSyntheticATLProcessRefusesRuntimeResourceDriftBeforeToolCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	for name, test := range map[string]struct {
		mutateList       func(map[string]any)
		mutateProjection func(map[string]any)
		readCache        string
		wantError        string
		failsAfterList   bool
	}{
		"service mismatch": {
			mutateProjection: func(value map[string]any) { value["service_profile"] = "confluence" },
			wantError:        "service_profile",
		},
		"public read cache": {readCache: "public", wantError: "cache scope"},
		"extra resource": {
			mutateList: func(result map[string]any) {
				resources := result["resources"].([]any)
				result["resources"] = append(resources, resources[1])
			},
			wantError:      "resource inventory",
			failsAfterList: true,
		},
		"extra runtime member": {
			mutateProjection: func(value map[string]any) { value["extra"] = true },
			wantError:        "unknown member",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateSyntheticScratch(t)
			marker := filepath.Join(root, "unexpected-followup")
			binary := filepath.Join(root, "atl-fake")
			list := syntheticMCPResourceInventoryValueForTest()
			if test.mutateList != nil {
				test.mutateList(list)
			}
			cache := test.readCache
			if cache == "" {
				cache = "private"
			}
			read := syntheticMCPRuntimeReadResultForTest("jira", cache, test.mutateProjection)
			script := syntheticMCPRuntimeDriftProcessScript(
				string(marshalSyntheticMCPTestValue(t, list)), string(read), marker, test.failsAfterList,
			)
			writeSyntheticExecutable(t, binary, script)
			scratch := filepath.Join(root, "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
			if !ok {
				t.Fatal("construct MCP invocation")
			}
			process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
				Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
				MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
			})
			if process != nil || err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runtime preflight process=%v err=%v want=%q", process, err, test.wantError)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("runtime preflight sent a post-failure request: %v", statErr)
			}
			if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
				t.Fatalf("runtime survived resource refusal: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestSyntheticATLProcessRuntimePreflightPrecedesOrdinaryToolCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	audit := filepath.Join(root, "mcp-sequence")
	binary := filepath.Join(root, "atl-fake")
	listResult := string(syntheticMCPResourceInventoryResultForTest())
	readResult := string(syntheticMCPRuntimeReadResultForTest("jira", "private", nil))
	script := "#!/bin/sh\n" + testATLCapabilityCatalogHandler() + fmt.Sprintf(`
if [ "$1" = "mcp" ]; then
  IFS= read -r request || exit 170
  case "$request" in *'"method":"initialize"'*) ;; *) exit 171 ;; esac
  printf 'initialize\n' >> %q
  printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r request || exit 172
  case "$request" in *'"method":"notifications/initialized"'*) ;; *) exit 173 ;; esac
  printf 'initialized\n' >> %q
  IFS= read -r request || exit 174
  case "$request" in *'"method":"resources/list"'*) ;; *) exit 175 ;; esac
  printf 'resources/list\n' >> %q
  printf '%%s\n' '{"jsonrpc":"2.0","id":2,"result":%s}'
  IFS= read -r request || exit 176
  case "$request" in *'"method":"resources/read"'*'"uri":"atl://runtime"'*) ;; *) exit 177 ;; esac
  printf 'resources/read\n' >> %q
  printf '%%s\n' '{"jsonrpc":"2.0","id":3,"result":%s}'
  IFS= read -r request || exit 178
  case "$request" in *'"method":"tools/call"'*'"name":"jira_fields"'*) ;; *) exit 179 ;; esac
  printf 'tools/call\n' >> %q
  printf '%%s\n' '{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"schema_version":1},"isError":false}}'
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 180
`, audit, audit, audit, listResult, audit, readResult, audit)
	writeSyntheticExecutable(t, binary, script)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct MCP invocation")
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	assertSyntheticMCPSequence(t, audit, []string{"initialize", "initialized", "resources/list", "resources/read"})
	if summary := process.Summary(); len(summary.HTTPMethods) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("runtime preflight reached a tool or backend: %+v", summary)
	}
	result, err := process.CallMCPJSON(t.Context(), invocation)
	if err != nil || result.IsError || string(result.StructuredContent) != `{"schema_version":1}` {
		t.Fatalf("ordinary MCP call result=%+v err=%v", result, err)
	}
	assertSyntheticMCPSequence(t, audit, []string{
		"initialize", "initialized", "resources/list", "resources/read", "tools/call",
	})
	if summary := process.Summary(); len(summary.HTTPMethods) != 0 || summary.MCPInvocations["jira_fields"] != 1 {
		t.Fatalf("ordinary MCP accounting drifted: %+v", summary)
	}
}

func TestSyntheticATLProcessRuntimePreflightMatchesSelectedBinaryProfiles(t *testing.T) {
	tools := map[string]string{
		"default": "jira_fields", "jira": "jira_fields",
		"confluence": "confluence_mirror_snapshot", "offline": "jira_mirror_snapshot",
	}
	for _, service := range []string{"default", "jira", "confluence", "offline"} {
		t.Run(service, func(t *testing.T) {
			invocation, ok := newMCPInvocation(tools[service], map[string]any{})
			if !ok {
				t.Fatal("construct MCP invocation")
			}
			process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
				Binary: repositorySyntheticATLBinary(t), Fixture: minimalSyntheticFixture(),
				ScratchRoot: privateSyntheticATLScratch(t), MCPService: service,
				MCPInvocations: []MCPInvocation{invocation}, Timeout: 10 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if summary := process.Summary(); len(summary.HTTPMethods) != 0 || len(summary.MCPInvocations) != 0 {
				t.Fatalf("selected-binary runtime preflight reached a tool or backend: %+v", summary)
			}
			if err := process.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSyntheticATLProcessRuntimePreflightAdvancesDurableAdapterIdentity(t *testing.T) {
	root := privateSyntheticScratch(t)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct MCP invocation")
	}
	config, _, _, err := normalizeSyntheticATLProcessConfig(SyntheticATLProcessConfig{
		Fixture: minimalSyntheticFixture(), ScratchRoot: scratch, MCPService: "jira",
		MCPInvocations: []MCPInvocation{invocation},
	})
	if err != nil {
		t.Fatal(err)
	}
	binary := selectedSyntheticATLBinary{sha256: strings.Repeat("a", 64)}
	session, err := prepareSyntheticATLProcessAttemptSession(config, binary)
	if err != nil {
		t.Fatal(err)
	}
	want, err := contentMinimizedAttemptDigest("synthetic-atl-process-adapter", []string{
		"selected-atl-process-v2", binary.sha256,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := contentMinimizedAttemptDigest("synthetic-atl-process-adapter", []string{
		"selected-atl-process-v1", binary.sha256,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := session.plan.Binding.Identity.AdapterSHA256
	if got != want || got == stale {
		t.Fatalf("selected-process adapter identity=%q want=%q stale=%q", got, want, stale)
	}
}

type syntheticMCPStartResult struct {
	process *SyntheticATLProcess
	err     error
}

func TestSyntheticATLProcessRevalidatesExecutionCopyAfterRuntimePreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := privateSyntheticScratch(t)
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary,
		"#!/bin/sh\n"+testATLCapabilityCatalogHandler()+testSyntheticMCPRuntimeResourceHandler()+`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 181
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 182
  IFS= read -r resources_list || exit 183
  : > "$ATL_CONFIG_DIR/../runtime-preflight-ready"
  while [ ! -f "$ATL_CONFIG_DIR/../runtime-preflight-release" ]; do /bin/sleep 0.01; done
  synthetic_mcp_resources_list
  IFS= read -r runtime_read || exit 184
  synthetic_mcp_runtime_read "$4"
  while IFS= read -r ignored; do :; done
  exit 0
fi
exit 185
`)
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation("jira_fields", map[string]any{})
	if !ok {
		t.Fatal("construct MCP invocation")
	}
	started := make(chan syntheticMCPStartResult, 1)
	go func() {
		process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
			Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: scratch,
			MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, Timeout: time.Second,
		})
		started <- syntheticMCPStartResult{process: process, err: err}
	}()
	runtimeRoot := waitForSyntheticRuntimeMarker(t, started, scratch, "runtime-preflight-ready")
	executionPath := filepath.Join(runtimeRoot, "selected-atl")
	if err := os.Chmod(executionPath, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(executionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# runtime preflight mutation\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "runtime-preflight-release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-started
	if result.process != nil || result.err == nil || !strings.Contains(result.err.Error(), "private ATL execution copy changed") {
		t.Fatalf("post-preflight mutation process=%v err=%v", result.process, result.err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("runtime survived post-preflight identity rejection: entries=%v err=%v", entries, err)
	}
}

func waitForSyntheticRuntimeMarker(
	t *testing.T,
	started <-chan syntheticMCPStartResult,
	scratch string,
	marker string,
) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case result := <-started:
			t.Fatalf("process stopped before runtime marker: process=%v err=%v", result.process, result.err)
		default:
		}
		entries, err := os.ReadDir(scratch)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			candidate := filepath.Join(scratch, entry.Name())
			if _, err := os.Stat(filepath.Join(candidate, marker)); err == nil {
				return candidate
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("synthetic runtime marker %q was not created", marker)
	return ""
}

func assertSyntheticMCPSequence(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(got) != len(want) {
		t.Fatalf("MCP sequence=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("MCP sequence=%v want=%v", got, want)
		}
	}
}

func syntheticMCPResourceInventoryValueForTest() map[string]any {
	return map[string]any{
		"resources": []any{
			map[string]any{
				"uri": "atl://capabilities", "name": "atl-capabilities", "title": "atl capability routes",
				"description": "Static content-free CLI and MCP capability routing metadata.",
				"mimeType":    "application/json",
			},
			map[string]any{
				"uri": "atl://runtime", "name": "atl-runtime", "title": "atl runtime safety projection",
				"description": "Immutable content-free startup safety and compatibility metadata for this atl MCP invocation.",
				"mimeType":    "application/json",
			},
		},
		"ttlMs": 0, "cacheScope": "public",
	}
}

func syntheticMCPResourceInventoryResultForTest() json.RawMessage {
	data, err := json.Marshal(syntheticMCPResourceInventoryValueForTest())
	if err != nil {
		panic(err)
	}
	return data
}

func syntheticMCPRuntimeProjectionValueForTest(service string) map[string]any {
	return map[string]any{
		"schema_version":    1,
		"access":            "hard_read_only",
		"lifecycle":         "startup_only",
		"change_activation": "restart_required",
		"service_profile":   service,
		"global_read_only_policy": map[string]any{
			"configured_read_only": false,
			"effective_read_only":  true,
			"read_only_source":     "environment",
		},
		"plugin": map[string]any{
			"interface_contract": "unverified",
			"product_version":    "unverified",
		},
	}
}

func syntheticMCPRuntimeReadResultForTest(
	service string,
	cacheScope string,
	mutate func(map[string]any),
) json.RawMessage {
	projection := syntheticMCPRuntimeProjectionValueForTest(service)
	if mutate != nil {
		mutate(projection)
	}
	projectionBytes, err := json.Marshal(projection)
	if err != nil {
		panic(err)
	}
	result := map[string]any{
		"contents": []any{map[string]any{
			"uri": "atl://runtime", "mimeType": "application/json",
			"text": string(projectionBytes),
		}},
		"ttlMs": 0, "cacheScope": cacheScope,
	}
	data, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return data
}

func marshalSyntheticMCPTestValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func syntheticMCPRuntimeDriftProcessScript(listResult, readResult, marker string, failsAfterList bool) string {
	afterList := fmt.Sprintf(`
  IFS= read -r runtime_read || exit 164
  case "$runtime_read" in *'"method":"resources/read"'*'"uri":"atl://runtime"'*) ;; *) exit 165 ;; esac
  printf '%%s\n' '{"jsonrpc":"2.0","id":3,"result":%s}'
`, readResult)
	if failsAfterList {
		afterList = ""
	}
	return "#!/bin/sh\n" + testATLCapabilityCatalogHandler() + fmt.Sprintf(`
if [ "$1" = "mcp" ]; then
  IFS= read -r initialize || exit 160
  printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}'
  IFS= read -r initialized || exit 161
  IFS= read -r resources_list || exit 162
  case "$resources_list" in *'"method":"resources/list"'*) ;; *) exit 163 ;; esac
  printf '%%s\n' '{"jsonrpc":"2.0","id":2,"result":%s}'
%s
  if IFS= read -r unexpected; then
    printf 'unexpected\n' > %q
  fi
  exit 0
fi
exit 166
`, listResult, afterList, marker)
}
