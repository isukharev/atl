package agenteval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	oracleSchema          = `{"type":"object"}`
	oracleCLIInstructions = "This is an evidence task. Use the literal atl executable through the shell tool to retrieve the evidence required for the answer. Make only the minimum necessary invocation or invocations allowed by the reviewed command policy. Base the answer on the returned evidence; a no-tool answer or an answer based on assumptions is invalid for this benchmark. Never use apply_patch, Edit, Write, or direct filesystem operations to create, inspect, or modify command-broker manifests or request/response files. If evidence retrieval through atl fails, do not invent or use an alternate broker-file protocol; return the failure through the required response schema."

	oracleDefaultCLIProjection          = `["PATH","ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_REAL_BINARY","ATL_EVAL_COUNTER","ATL_EVAL_ALLOWED_COMMANDS"]`
	oracleOrdinaryMCPProjection         = `["PATH","LANG","LC_ALL","TERM"]`
	oracleOrdinaryExternalMCPProjection = `["PATH","LANG","LC_ALL","TERM","NO_PROXY","no_proxy","ATL_EVAL_EXTERNAL_MCP_TOKEN"]`
	oraclePrivateMCPProjection          = `["PATH","LANG","LC_ALL","TERM","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	oraclePrivateExternalMCPProjection  = `["PATH","LANG","LC_ALL","TERM","NO_PROXY","no_proxy","ATL_EVAL_EXTERNAL_MCP_TOKEN","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	oracleConfinedCLIProjection         = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	oracleReviewedWriteCLIProjection    = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_ALLOW_REVIEWED_WRITES","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	oracleSyntheticWriteCLIProjection   = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_EVAL_ALLOW_SYNTHETIC_WRITES","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	oracleLegacyCalibrationProjection   = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
)

func TestWrapperOracleEnvironmentProjections(t *testing.T) {
	for _, test := range []struct {
		id   wrapperEnvironmentProjectionID
		want string
	}{
		{id: wrapperProjectionSyntheticCLI, want: oracleDefaultCLIProjection},
		{id: wrapperProjectionMCP, want: oracleOrdinaryMCPProjection},
		{id: wrapperProjectionExternalMCP, want: oracleOrdinaryExternalMCPProjection},
		{id: wrapperProjectionPrivateMCP, want: oraclePrivateMCPProjection},
		{id: wrapperProjectionPrivateExternalMCP, want: oraclePrivateExternalMCPProjection},
		{id: wrapperProjectionConfinedCLI, want: oracleConfinedCLIProjection},
		{id: wrapperProjectionPrivateReviewedWriteCLI, want: oracleReviewedWriteCLIProjection},
		{id: wrapperProjectionSyntheticWriteCLI, want: oracleSyntheticWriteCLIProjection},
		{id: wrapperProjectionLegacyCalibration, want: oracleLegacyCalibrationProjection},
	} {
		t.Run(string(test.id), func(t *testing.T) {
			if got := renderWrapperEnvironmentProjection(test.id); got != test.want {
				t.Fatalf("projection=%s\ngot:  %s\nwant: %s", test.id, got, test.want)
			}
		})
	}
}

func TestWrapperOracleProviderCommands(t *testing.T) {
	claudeCLI := validRunSpec()
	claudeCLI.Provider = "claude-code"
	claudeCLI.Pricing = Pricing{}
	assertOracleProviderCommand(t, claudeCLI, ProviderConfinement{}, "/mcp.json", ProviderCommand{
		Path: "agent",
		Args: []string{
			"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
			"--model", "gpt-test-1", "--max-budget-usd", "10.000000", "--permission-mode", "auto",
			"--strict-mcp-config", "--no-chrome", "--setting-sources", "project",
			"--tools", "Bash", "--allowed-tools", "Bash(atl *),Bash(export ATL_READ_ONLY=1),Bash(command -v atl)",
			"--json-schema", oracleSchema, "--plugin-dir", "/plugin", "--settings", "/settings",
		},
	})

	claudeMCP := oracleMCPRunSpec("claude-code", BackendModeSynthetic, SurfaceATLMCP)
	assertOracleProviderCommand(t, claudeMCP, ProviderConfinement{}, "/mcp.json", ProviderCommand{
		Path: "agent",
		Args: []string{
			"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
			"--model", "gpt-test-1", "--max-budget-usd", "10.000000", "--permission-mode", "auto",
			"--strict-mcp-config", "--no-chrome", "--setting-sources", "project", "--tools", "",
			"--json-schema", oracleSchema, "--mcp-config", "/mcp.json", "--plugin-dir", "/plugin", "--settings", "/settings",
		},
	})

	defaultCLI := validRunSpec()
	assertOracleProviderCommand(t, defaultCLI, ProviderConfinement{}, "", ProviderCommand{Path: "agent", Args: oracleCodexBaseArgs(true, true, oracleDefaultCLIProjection)})

	ordinaryMCP := oracleMCPRunSpec("codex", BackendModeSynthetic, SurfaceATLMCP)
	ordinaryMCPArgs := oracleCodexBaseArgs(true, true, oracleOrdinaryMCPProjection)
	ordinaryMCPArgs = append(ordinaryMCPArgs[:len(ordinaryMCPArgs)-1],
		"--dangerously-bypass-hook-trust", "-c", `web_search="disabled"`,
		"-c", `mcp_servers.atl.command="/atl"`, "-c", `mcp_servers.atl.args=["mcp","serve"]`,
		"-c", `mcp_servers.atl.required=true`, "-c", `mcp_servers.atl.enabled_tools=["jira_fields"]`,
		"-c", `mcp_servers.atl.default_tools_approval_mode="approve"`,
		"-c", `mcp_servers.atl.env_vars=["ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE"]`,
		"-c", oracleHookConfig("/guard"), "-")
	assertOracleProviderCommand(t, ordinaryMCP, ProviderConfinement{}, "", ProviderCommand{Path: "agent", Args: ordinaryMCPArgs})

	privateMCP := oracleMCPRunSpec("codex", BackendModePrivateLive, SurfaceATLMCP)
	privateMCPConfinement := privateMCPHookConfinement("atl", "jira_fields")
	privateMCPArgs := oracleCodexBaseArgs(false, true, oraclePrivateMCPProjection)
	privateMCPArgs = oracleInsertCodexLocalRoute(privateMCPArgs)
	privateMCPArgs = append(privateMCPArgs[:len(privateMCPArgs)-1],
		"--dangerously-bypass-hook-trust", "-c", `web_search="disabled"`,
		"-c", `mcp_servers.atl.command="/atl"`, "-c", `mcp_servers.atl.args=["mcp","serve"]`,
		"-c", `mcp_servers.atl.required=true`, "-c", `mcp_servers.atl.enabled_tools=["jira_fields"]`,
		"-c", `mcp_servers.atl.default_tools_approval_mode="approve"`,
		"-c", `mcp_servers.atl.env_vars=["ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","NO_PROXY","no_proxy"]`,
		"-c", oracleHookConfig(oraclePrivateHookCommand("mcp-with-skill-read", `["mcp__atl__jira_fields"]`)), "-")
	assertOracleProviderCommand(t, privateMCP, privateMCPConfinement, "", ProviderCommand{Path: "agent", Args: privateMCPArgs})

	privateExternal := oracleMCPRunSpec("codex", BackendModePrivateLive, SurfaceExternalMCP)
	privateExternal.mcpServerURL = "http://127.0.0.1:1234/mcp"
	privateExternal.mcpBearerTokenEnv = "ATL_EVAL_EXTERNAL_MCP_TOKEN"
	privateExternalConfinement := privateMCPHookConfinement("external_ro", "jira_fields")
	privateExternalArgs := oracleCodexBaseArgs(false, true, oraclePrivateExternalMCPProjection)
	privateExternalArgs = oracleInsertCodexLocalRoute(privateExternalArgs)
	privateExternalArgs = append(privateExternalArgs[:len(privateExternalArgs)-1],
		"--dangerously-bypass-hook-trust", "-c", `web_search="disabled"`,
		"-c", `mcp_servers.external_ro.url="http://127.0.0.1:1234/mcp"`,
		"-c", `mcp_servers.external_ro.bearer_token_env_var="ATL_EVAL_EXTERNAL_MCP_TOKEN"`,
		"-c", `mcp_servers.external_ro.required=true`, "-c", `mcp_servers.external_ro.enabled_tools=["jira_fields"]`,
		"-c", `mcp_servers.external_ro.default_tools_approval_mode="approve"`,
		"-c", oracleHookConfig(oraclePrivateHookCommand("mcp-with-skill-read", `["mcp__external_ro__jira_fields"]`)), "-")
	assertOracleProviderCommand(t, privateExternal, privateExternalConfinement, "", ProviderCommand{Path: "agent", Args: privateExternalArgs})

	confinedReadOnly := validRunSpec()
	confinedReadOnly.AllowedATLCommands = nil
	confinedReadOnly.AllowedCLICommands = validCLICommandPolicy().Rules
	assertOracleProviderCommand(t, confinedReadOnly, oracleSyntheticCLIConfinement(), "", ProviderCommand{
		Path: "agent", Args: oracleSyntheticConfinedCLIArgs(oracleConfinedCLIProjection),
	})

	syntheticWrite := confinedReadOnly
	syntheticWrite.AllowSyntheticWrites = true
	assertOracleProviderCommand(t, syntheticWrite, oracleSyntheticCLIConfinement(), "", ProviderCommand{
		Path: "agent", Args: oracleSyntheticConfinedCLIArgs(oracleSyntheticWriteCLIProjection),
	})

	privateWrite := oraclePrivateCLIRunSpec()
	privateWrite.AllowLiveWrites = true
	privateWrite.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {
		{Name: "issue_read", PathPrefix: "/rest/api/2/issue/TEST-1", Exact: true, Methods: []string{"GET"}, MaxRequests: 1},
		{Name: "issue_write", PathPrefix: "/rest/api/2/issue/TEST-1", Exact: true, Methods: []string{"PUT"}, MaxRequests: 1, MaxRequestBytes: 1 << 20},
	}}
	privateWrite.GatewayMaxRequestBytes = 1 << 20
	privateWrite.GatewayMaxTotalRequestBytes = 1 << 20
	privateArgs := oracleCodexPrivateCLIArgs(oracleReviewedWriteCLIProjection, "private-cli")
	assertOracleProviderCommand(t, privateWrite, privateCLIHookConfinement(), "", ProviderCommand{Path: "agent", Args: privateArgs})
}

func TestWrapperOracleCurrentAndLegacyCalibrationCommands(t *testing.T) {
	options := CodexCLICalibrationOptions{
		Model: "test-model", TimeoutSeconds: 60, MaxEstimatedCostMicroUSD: 500_000,
		Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
	}
	currentConfinement := ProviderConfinement{
		RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses",
		GuardMode: "provider-calibration", GuardCounterPath: "/private/guard.jsonl",
		WorkspaceReadRoot: "/private/workspace", AllowedReadRoots: []string{"/private/skills", "/private/workspace"},
		SkillReadRoots: []string{"/private/skills"},
	}
	current, err := BuildProviderCommand(calibrationRunSpec(options), "codex", "/private/atl", "/private/guard", "/private/workspace", "/private/schema", "/private/final", "", "", "", currentConfinement, codexCLICalibrationSchema)
	if err != nil {
		t.Fatal(err)
	}
	currentHookCommand := "ATL_EVAL_GUARD_MODE='provider-calibration' ATL_EVAL_GUARD_COUNTER='/private/guard.jsonl' ATL_EVAL_ALLOWED_MCP_TOOLS='null' ATL_EVAL_WORKSPACE_ROOT='/private/workspace' ATL_EVAL_ALLOWED_READ_ROOTS='[\"/private/skills\",\"/private/workspace\"]' ATL_EVAL_SKILL_READ_ROOTS='[\"/private/skills\"]' '/private/guard'"
	wantCurrent := ProviderCommand{Path: "codex", Args: oracleCurrentCalibrationArgs(currentHookCommand)}
	if current.Path != wantCurrent.Path || !slices.Equal(current.Args, wantCurrent.Args) {
		t.Fatalf("current calibration command drifted\ngot:  %#v\nwant: %#v", current, wantCurrent)
	}

	legacyConfinement := ProviderConfinement{
		RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses",
		GuardMode: "provider-calibration", GuardCounterPath: "/private/guard.jsonl",
		WorkspaceReadRoot: "/private/workspace", AllowedReadRoots: []string{"/private/installed-plugin-skills", "/private/workspace"},
	}
	legacy, err := buildLegacyToolQualifiedCalibrationProviderCommand("test-model", "high", legacyConfinement)
	if err != nil {
		t.Fatal(err)
	}
	legacyHookCommand := "ATL_EVAL_GUARD_MODE='provider-calibration' ATL_EVAL_GUARD_COUNTER='/private/guard.jsonl' ATL_EVAL_ALLOWED_MCP_TOOLS='null' ATL_EVAL_WORKSPACE_ROOT='/private/workspace' ATL_EVAL_ALLOWED_READ_ROOTS='[\"/private/installed-plugin-skills\",\"/private/workspace\"]' '/private/guard'"
	wantLegacy := ProviderCommand{Path: "codex", Args: append(oracleLegacyCalibrationArgs(legacyHookCommand), "-")}
	if legacy.Path != wantLegacy.Path || !slices.Equal(legacy.Args, wantLegacy.Args) {
		t.Fatalf("legacy calibration command drifted\ngot:  %#v\nwant: %#v", legacy, wantLegacy)
	}
}

func TestWrapperOracleHookCommands(t *testing.T) {
	plain, err := codexDenyNonMCPHook("/guard", validRunSpec(), ProviderConfinement{})
	if err != nil || plain != oracleHookConfig("/guard") {
		t.Fatalf("plain hook=%q err=%v", plain, err)
	}

	for _, test := range []struct {
		name        string
		spec        RunSpec
		confinement ProviderConfinement
		wantCommand string
	}{
		{name: "private internal MCP", spec: oracleMCPRunSpec("codex", BackendModePrivateLive, SurfaceATLMCP), confinement: privateMCPHookConfinement("atl", "jira_fields"), wantCommand: oraclePrivateHookCommand("mcp-with-skill-read", `["mcp__atl__jira_fields"]`)},
		{name: "private external MCP", spec: oracleMCPRunSpec("codex", BackendModePrivateLive, SurfaceExternalMCP), confinement: privateMCPHookConfinement("external_ro", "jira_fields"), wantCommand: oraclePrivateHookCommand("mcp-with-skill-read", `["mcp__external_ro__jira_fields"]`)},
		{name: "private CLI", spec: oraclePrivateCLIRunSpec(), confinement: privateCLIHookConfinement(), wantCommand: oraclePrivateHookCommand("private-cli", `null`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := codexDenyNonMCPHook("/guard", test.spec, test.confinement)
			if err != nil || got != oracleHookConfig(test.wantCommand) {
				t.Fatalf("hook=%q err=%v want=%q", got, err, oracleHookConfig(test.wantCommand))
			}
		})
	}
}

func TestWrapperOracleProxyRecordJSONAndReaderSemantics(t *testing.T) {
	full := atlProxyRecord{
		CommandFamily: "jira.fields", CalibrationObservationSHA256: strings.Repeat("a", 64),
		ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access", Denied: true,
		StdoutBytes: 12, StderrBytes: 34, ExitCode: 4,
	}
	for _, test := range []struct {
		name string
		in   atlProxyRecord
		want string
	}{
		{name: "zero", in: atlProxyRecord{}, want: `{"stdout_bytes":0,"stderr_bytes":0,"exit_code":0}`},
		{name: "full", in: full, want: `{"command_family":"jira.fields","calibration_observation_sha256":"` + strings.Repeat("a", 64) + `","error_kind":"not_found","error_remediation":"verify_identifier_or_access","denied":true,"stdout_bytes":12,"stderr_bytes":34,"exit_code":4}`},
		{name: "denied", in: atlProxyRecord{Denied: true, ExitCode: 2}, want: `{"denied":true,"stdout_bytes":0,"stderr_bytes":0,"exit_code":2}`},
		{name: "classified", in: atlProxyRecord{ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access", StderrBytes: 10, ExitCode: 4}, want: `{"error_kind":"not_found","error_remediation":"verify_identifier_or_access","stdout_bytes":0,"stderr_bytes":10,"exit_code":4}`},
		{name: "calibration", in: atlProxyRecord{CommandFamily: "atl_version", CalibrationObservationSHA256: strings.Repeat("b", 64), StdoutBytes: 8}, want: `{"command_family":"atl_version","calibration_observation_sha256":"` + strings.Repeat("b", 64) + `","stdout_bytes":8,"stderr_bytes":0,"exit_code":0}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.in)
			if err != nil || string(got) != test.want {
				t.Fatalf("marshal=%q err=%v want=%q", got, err, test.want)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	records, err := readProxyRecords(missing)
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("missing records=%#v err=%v", records, err)
	}

	path := filepath.Join(t.TempDir(), "records.jsonl")
	data := " \n" +
		`{"command_family":"first","unknown":true,"stdout_bytes":1,"stderr_bytes":0,"exit_code":0}` + "\n" +
		`{"command_family":"old","command_family":"duplicate-last","stdout_bytes":2,"stderr_bytes":3,"exit_code":4}` + "\n" +
		`{"denied":true,"stdout_bytes":0,"stderr_bytes":0,"exit_code":2}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err = readProxyRecords(path)
	if err != nil || len(records) != 3 || records[0].CommandFamily != "first" || records[1].CommandFamily != "duplicate-last" || records[1].ExitCode != 4 || !records[2].Denied {
		t.Fatalf("ordered historical records=%#v err=%v", records, err)
	}

	if err := os.WriteFile(path, []byte("\nnot-json\n"+data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProxyRecords(path); err == nil || !strings.Contains(err.Error(), "decode atl proxy record") {
		t.Fatalf("malformed first nonblank line err=%v", err)
	}

	if err := os.WriteFile(path, make([]byte, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProxyRecords(path); err == nil || !strings.Contains(err.Error(), "file exceeds 1048576 bytes") {
		t.Fatalf("oversized record file err=%v", err)
	}
}

func assertOracleProviderCommand(t *testing.T, spec RunSpec, confinement ProviderConfinement, mcpConfig string, want ProviderCommand) {
	t.Helper()
	got, err := BuildProviderCommand(spec, "agent", "/atl", "/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", mcpConfig, confinement, []byte(oracleSchema))
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != want.Path || !slices.Equal(got.Args, want.Args) {
		t.Fatalf("provider command drifted\ngot:  %#v\nwant: %#v", got, want)
	}
}

func oracleMCPRunSpec(provider, mode, surface string) RunSpec {
	spec := validRunSpec()
	spec.Provider = provider
	spec.ToolTransport = "mcp"
	spec.Surface = surface
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields"}
	if provider == "claude-code" {
		spec.Pricing = Pricing{}
	}
	if mode == BackendModePrivateLive {
		spec.BackendMode = mode
		spec.FixtureFile = ""
		spec.Repetitions = 1
	}
	return spec
}

func oraclePrivateCLIRunSpec() RunSpec {
	spec := validRunSpec()
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.ToolTransport = "cli"
	spec.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
	spec.AllowedATLCommands = nil
	spec.AllowedCLICommands = validCLICommandPolicy().Rules
	spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
	spec.GatewayMaxResponseBytes = 1 << 20
	spec.GatewayMaxTotalBytes = 2 << 20
	spec.SkillActivation = SkillActivationImplicit
	return spec
}

func oracleSyntheticCLIConfinement() ProviderConfinement {
	return ProviderConfinement{
		RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses",
		GuardMode: "private-cli", GuardCounterPath: "/guard-decisions.jsonl",
		WorkspaceReadRoot: "/workspace", AllowedReadRoots: []string{"/workspace"},
		SkillReadRoots: []string{"/workspace/.agents/skills"},
	}
}

func oracleCodexBaseArgs(ignoreUserConfig, sandbox bool, projection string) []string {
	args := []string{"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--model", "gpt-test-1"}
	if ignoreUserConfig {
		args = append(args, "--ignore-user-config")
	}
	args = append(args,
		"--disable", "apps", "--disable", "browser_use", "--disable", "computer_use",
		"--disable", "image_generation", "--disable", "remote_plugin")
	if sandbox {
		args = append(args, "--sandbox", "read-only")
	}
	args = append(args,
		"-C", "/workspace", "--output-schema", "/schema", "--output-last-message", "/final",
		"-c", `project_doc_max_bytes=0`, "-c", `shell_environment_policy.inherit="all"`,
		"-c", `shell_environment_policy.include_only=`+projection, "-")
	return args
}

func oracleInsertCodexLocalRoute(args []string) []string {
	index := slices.Index(args, "--sandbox")
	if index < 0 {
		index = slices.Index(args, "-C")
	}
	result := append([]string(nil), args[:index]...)
	result = append(result, "--enable", "shell_tool", "--enable", "unified_exec")
	return append(result, args[index:]...)
}

func oracleSyntheticConfinedCLIArgs(projection string) []string {
	args := oracleCodexBaseArgs(true, false, projection)
	return append(args[:len(args)-1],
		"--ignore-rules", "--dangerously-bypass-hook-trust", "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"-c", oracleHookConfig("ATL_EVAL_GUARD_MODE='private-cli' ATL_EVAL_GUARD_COUNTER='/guard-decisions.jsonl' ATL_EVAL_ALLOWED_MCP_TOOLS='null' ATL_EVAL_WORKSPACE_ROOT='/workspace' ATL_EVAL_ALLOWED_READ_ROOTS='[\"/workspace\"]' ATL_EVAL_SKILL_READ_ROOTS='[\"/workspace/.agents/skills\"]' '/guard'"),
		"-c", `default_permissions="atl_agent_eval"`, "-c", `permissions.atl_agent_eval.extends=":workspace"`,
		"-c", `permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`, "-")
}

func oracleCodexPrivateCLIArgs(projection, mode string) []string {
	args := oracleCodexBaseArgs(false, false, projection)
	args = oracleInsertCodexLocalRoute(args)
	return append(args[:len(args)-1],
		"--ignore-rules", "--dangerously-bypass-hook-trust", "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"-c", oracleHookConfig(oraclePrivateHookCommand(mode, `null`)),
		"-c", `plugins."atl@atl".enabled=true`, "-c", `developer_instructions=`+strconv.Quote(oracleCLIInstructions),
		"-c", `default_permissions="atl_agent_eval"`, "-c", `permissions.atl_agent_eval.extends=":workspace"`,
		"-c", `permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`, "-")
}

func oraclePrivateHookCommand(mode, tools string) string {
	return "ATL_EVAL_GUARD_MODE='" + mode + "' ATL_EVAL_GUARD_COUNTER='/guard-decisions.jsonl' ATL_EVAL_ALLOWED_MCP_TOOLS='" + tools + "' ATL_EVAL_WORKSPACE_ROOT='/workspace' ATL_EVAL_ALLOWED_READ_ROOTS='[\"/skills\",\"/workspace\"]' ATL_EVAL_SKILL_READ_ROOTS='[\"/skills\"]' '/guard'"
}

func oracleHookConfig(command string) string {
	return `hooks.PreToolUse=[{matcher="^(Bash|apply_patch|Edit|Write|Read|Agent)$",hooks=[{type="command",command=` + strconv.Quote(command) + `,timeout=5}]}]`
}

func oracleLegacyCalibrationArgs(hookCommand string) []string {
	return []string{
		"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--model", "test-model",
		"--disable", "apps", "--disable", "browser_use", "--disable", "computer_use", "--disable", "image_generation", "--disable", "remote_plugin",
		"--enable", "shell_tool", "--enable", "unified_exec",
		"-C", "/private/workspace", "--output-schema", "/private/response-schema.json", "--output-last-message", "/private/final.json",
		"-c", `project_doc_max_bytes=0`, "-c", `shell_environment_policy.inherit="all"`, "-c", `shell_environment_policy.include_only=` + oracleLegacyCalibrationProjection,
		"--ignore-rules", "--dangerously-bypass-hook-trust", "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"-c", `plugins."atl@atl".enabled=true`, "-c", `developer_instructions=` + strconv.Quote(oracleCLIInstructions),
		"-c", oracleHookConfig(hookCommand),
		"-c", `default_permissions="atl_agent_eval"`, "-c", `permissions.atl_agent_eval.extends=":workspace"`,
		"-c", `permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`,
		"-c", `model_reasoning_effort="high"`,
	}
}

func oracleCurrentCalibrationArgs(hookCommand string) []string {
	return []string{
		"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--model", "test-model",
		"--disable", "apps", "--disable", "browser_use", "--disable", "computer_use", "--disable", "image_generation", "--disable", "remote_plugin",
		"--enable", "shell_tool", "--enable", "unified_exec",
		"-C", "/private/workspace", "--output-schema", "/private/schema", "--output-last-message", "/private/final",
		"-c", `project_doc_max_bytes=0`, "-c", `shell_environment_policy.inherit="all"`, "-c", `shell_environment_policy.include_only=` + oracleConfinedCLIProjection,
		"--ignore-rules", "--dangerously-bypass-hook-trust", "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"-c", oracleHookConfig(hookCommand),
		"-c", `plugins."atl@atl".enabled=true`, "-c", `developer_instructions=` + strconv.Quote(oracleCLIInstructions),
		"-c", `default_permissions="atl_agent_eval"`, "-c", `permissions.atl_agent_eval.extends=":workspace"`,
		"-c", `permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`, "-",
	}
}
