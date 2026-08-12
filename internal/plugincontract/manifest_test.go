package plugincontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestGeneratedMCPConfigsDeriveEachConsumerVersion(t *testing.T) {
	claudeManifest := manifestFixture(t, "1.2.3", expectedManifestMCPServers)
	codexManifest := manifestFixture(t, "4.5.6-rc.1", expectedManifestMCPServers)
	claude, codex, err := GeneratedMCPConfigs(claudeManifest, codexManifest)
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedMCPArgs(t, claude, "1.2.3")
	assertGeneratedMCPArgs(t, codex, "4.5.6-rc.1")
}

func TestGeneratedMCPConfigsRequireBothExactManifestConsumers(t *testing.T) {
	for _, test := range []struct {
		name, claudeVersion, claudeMCP, codexVersion, codexMCP string
	}{
		{name: "missing Claude version", claudeMCP: expectedManifestMCPServers, codexVersion: "1.2.3", codexMCP: expectedManifestMCPServers},
		{name: "wrong Claude MCP reference", claudeVersion: "1.2.3", claudeMCP: "other.json", codexVersion: "1.2.3", codexMCP: expectedManifestMCPServers},
		{name: "missing Codex version", claudeVersion: "1.2.3", claudeMCP: expectedManifestMCPServers, codexMCP: expectedManifestMCPServers},
		{name: "wrong Codex MCP reference", claudeVersion: "1.2.3", claudeMCP: expectedManifestMCPServers, codexVersion: "1.2.3", codexMCP: "other.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			claudeManifest := manifestFixture(t, test.claudeVersion, test.claudeMCP)
			codexManifest := manifestFixture(t, test.codexVersion, test.codexMCP)
			if _, _, err := GeneratedMCPConfigs(claudeManifest, codexManifest); err == nil {
				t.Fatal("invalid manifest consumer passed")
			}
		})
	}
}

func TestGeneratedMCPConfigRejectsDuplicateManifestVersion(t *testing.T) {
	data := []byte(`{"version":"1.2.3","version":"9.9.9","mcpServers":"./.mcp.json"}`)
	if _, err := generatedMCPConfigForManifest(data); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate manifest version passed: %v", err)
	}
}

func assertGeneratedMCPArgs(t *testing.T, data []byte, productVersion string) {
	t.Helper()
	var config generatedMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server, ok := config.MCPServers["atl"]
	want := []string{
		"mcp", "serve", "--" + InterfaceFlagName + "=1", "--" + ProductFlagName + "=" + productVersion,
	}
	wantEnv := map[string]string{codexMCPProtocolEnvironmentName: codexMCPProtocolVersion}
	if len(config.MCPServers) != 1 || !ok || server.Command != "atl" || !reflect.DeepEqual(server.Args, want) ||
		!reflect.DeepEqual(server.Env, wantEnv) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("generated MCP config=%s, want args=%v", data, want)
	}
}

func manifestFixture(t *testing.T, version, mcpServers string) []byte {
	t.Helper()
	data, err := json.Marshal(pluginManifest{Version: version, MCPServers: mcpServers})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
