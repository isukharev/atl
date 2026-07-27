package agenteval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const claudeRouteProbeBashTool = `{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"}},"required":["command"]}}`

func TestClassifyClaudeRouteInventory(t *testing.T) {
	tests := []struct {
		name      string
		tools     string
		want      CLIRouteQualificationStatus
		wantRoute string
	}{
		{name: "missing member", tools: "", want: CLIRouteQualificationRouteMissing},
		{name: "null", tools: "null", want: CLIRouteQualificationRouteMissing},
		{name: "empty", tools: "[]", want: CLIRouteQualificationRouteMissing},
		{name: "unrelated", tools: `[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}]`, want: CLIRouteQualificationRouteMissing},
		{name: "shell route", tools: `[` + claudeRouteProbeBashTool + `]`, want: CLIRouteQualificationSupported, wantRoute: "bash"},
		{name: "shell route with siblings", tools: `[{"name":"Read","input_schema":{"type":"object"}},` + claudeRouteProbeBashTool + `]`, want: CLIRouteQualificationSupported, wantRoute: "bash"},
		{name: "duplicate", tools: `[` + claudeRouteProbeBashTool + `,` + claudeRouteProbeBashTool + `]`, want: CLIRouteQualificationAmbiguous},
		{name: "duplicate unrelated", tools: `[{"name":"Read","input_schema":{"type":"object"}},{"name":"Read","input_schema":{"type":"object"}}]`, want: CLIRouteQualificationAmbiguous},
		{name: "missing shell schema", tools: `[{"name":"Bash"}]`, want: CLIRouteQualificationAmbiguous},
		{name: "shell schema without command", tools: `[{"name":"Bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]`, want: CLIRouteQualificationAmbiguous},
		{name: "optional command", tools: `[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":[]}}]`, want: CLIRouteQualificationAmbiguous},
		{name: "non string command", tools: `[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"array"}},"required":["command"]}}]`, want: CLIRouteQualificationAmbiguous},
		{name: "invalid tools", tools: `{}`, want: CLIRouteQualificationSchemaFailed},
		{name: "invalid entry", tools: `[null]`, want: CLIRouteQualificationSchemaFailed},
		{name: "nameless entry", tools: `[{"input_schema":{"type":"object"}}]`, want: CLIRouteQualificationSchemaFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyClaudeRouteInventory(json.RawMessage(test.tools))
			if got.status != test.want || got.route != test.wantRoute {
				t.Fatalf("got=%+v want_status=%s want_route=%q", got, test.want, test.wantRoute)
			}
		})
	}
}

func TestCodexRouteStatusMapsTheClosedToolInventoryVocabulary(t *testing.T) {
	for status, want := range map[CodexCLIToolAvailabilityStatus]CLIRouteQualificationStatus{
		CodexCLIToolAvailabilitySupported:       CLIRouteQualificationSupported,
		CodexCLIToolAvailabilityMissing:         CLIRouteQualificationRouteMissing,
		CodexCLIToolAvailabilityAmbiguous:       CLIRouteQualificationAmbiguous,
		CodexCLIToolAvailabilitySchemaFailed:    CLIRouteQualificationSchemaFailed,
		CodexCLIToolAvailabilityProcessFailed:   CLIRouteQualificationProcessFailed,
		CodexCLIToolAvailabilityStatus("other"): CLIRouteQualificationProcessFailed,
	} {
		if got := codexRouteStatus(status); got != want {
			t.Fatalf("codex status %q mapped to %q want %q", status, got, want)
		}
	}
}

func TestCLIRouteQualificationReportValidation(t *testing.T) {
	identity := "binary-sha256:" + strings.Repeat("a", 64)
	base := CLIRouteQualificationReport{
		SchemaVersion:     CLIRouteQualificationSchemaVersion,
		Provider:          "codex",
		Surface:           SurfaceCLISkill,
		AgentIdentity:     identity,
		ContractSHA256:    strings.Repeat("b", 64),
		Status:            CLIRouteQualificationSupported,
		Route:             "exec_command",
		RequestObserved:   true,
		SyntheticRequests: 1,
	}
	for _, route := range []string{"exec_command", "shell_command", "exec"} {
		candidate := base
		candidate.Route = route
		if err := candidate.Validate(); err != nil || !candidate.Supported() {
			t.Fatalf("valid codex route %q rejected: %v", route, err)
		}
	}
	claude := base
	claude.Provider = "claude-code"
	claude.Route = "bash"
	claude.AuxiliaryRequests = 1
	if err := claude.Validate(); err != nil || !claude.Supported() {
		t.Fatalf("valid claude report rejected: %+v err=%v", claude, err)
	}

	for name, mutate := range map[string]func(*CLIRouteQualificationReport){
		"schema version":       func(value *CLIRouteQualificationReport) { value.SchemaVersion++ },
		"provider":             func(value *CLIRouteQualificationReport) { value.Provider = "openai" },
		"surface":              func(value *CLIRouteQualificationReport) { value.Surface = SurfaceATLMCP },
		"agent identity":       func(value *CLIRouteQualificationReport) { value.AgentIdentity = "sha256:" + strings.Repeat("a", 64) },
		"contract digest":      func(value *CLIRouteQualificationReport) { value.ContractSHA256 = "short" },
		"raw route":            func(value *CLIRouteQualificationReport) { value.Route = "arbitrary" },
		"cross provider route": func(value *CLIRouteQualificationReport) { value.Route = "bash" },
		"missing request":      func(value *CLIRouteQualificationReport) { value.RequestObserved = false },
		"two requests":         func(value *CLIRouteQualificationReport) { value.SyntheticRequests = 2 },
		"codex auxiliary":      func(value *CLIRouteQualificationReport) { value.AuxiliaryRequests = 1 },
		"provider request":     func(value *CLIRouteQualificationReport) { value.ProviderRequests = 1 },
		"backend request":      func(value *CLIRouteQualificationReport) { value.BackendRequests = 1 },
		"remote write":         func(value *CLIRouteQualificationReport) { value.RemoteWrites = 1 },
		"unknown status":       func(value *CLIRouteQualificationReport) { value.Status = "unknown" },
		"missing with route": func(value *CLIRouteQualificationReport) {
			value.Status = CLIRouteQualificationRouteMissing
		},
		"process failure with request": func(value *CLIRouteQualificationReport) {
			value.Status, value.Route = CLIRouteQualificationProcessFailed, ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if candidate.Validate() == nil || candidate.Supported() {
				t.Fatalf("invalid report passed: %+v", candidate)
			}
		})
	}

	for name, candidate := range map[string]CLIRouteQualificationReport{
		"unavailable route": {SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: "claude-code", Surface: SurfaceCLISkill,
			AgentIdentity: identity, ContractSHA256: strings.Repeat("b", 64), Status: CLIRouteQualificationRouteMissing,
			RequestObserved: true, SyntheticRequests: 1},
		"never launched": {SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: "codex", Surface: SurfaceCLISkill,
			AgentIdentity: identity, ContractSHA256: strings.Repeat("b", 64), Status: CLIRouteQualificationProcessFailed},
		"repeated request": {SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: "codex", Surface: SurfaceCLISkill,
			AgentIdentity: identity, ContractSHA256: strings.Repeat("b", 64), Status: CLIRouteQualificationProcessFailed,
			RequestObserved: true, SyntheticRequests: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if err := candidate.Validate(); err != nil {
				t.Fatalf("valid non-supported report rejected: %v", err)
			}
			if candidate.Supported() {
				t.Fatalf("non-supported report claimed support: %+v", candidate)
			}
		})
	}
}

func TestCLIRouteQualificationContractBindsTheReviewedLaunch(t *testing.T) {
	identity := "binary-sha256:" + strings.Repeat("a", 64)
	base := CLIRouteQualificationOptions{
		Provider: "claude-code", Surface: SurfaceCLISkill, Model: "synthetic-model", Reasoning: "high",
		AllowedTools:         []string{"Bash(atl *)", "Skill"},
		PluginSHA256:         strings.Repeat("1", 64),
		PluginManifestSHA256: strings.Repeat("2", 64),
		SettingsSHA256:       strings.Repeat("3", 64),
		ResponseSchemaSHA256: strings.Repeat("4", 64),
		PromptContractSHA256: strings.Repeat("5", 64),
		TimeoutSeconds:       30,
	}
	first := cliRouteQualificationContractSHA256(identity, base)
	for name, mutate := range map[string]func(*CLIRouteQualificationOptions){
		"provider":        func(value *CLIRouteQualificationOptions) { value.Provider = "codex" },
		"model":           func(value *CLIRouteQualificationOptions) { value.Model = "other-model" },
		"reasoning":       func(value *CLIRouteQualificationOptions) { value.Reasoning = "medium" },
		"tool inventory":  func(value *CLIRouteQualificationOptions) { value.AllowedTools = []string{"Bash(atl *)"} },
		"plugin":          func(value *CLIRouteQualificationOptions) { value.PluginSHA256 = strings.Repeat("9", 64) },
		"plugin manifest": func(value *CLIRouteQualificationOptions) { value.PluginManifestSHA256 = strings.Repeat("9", 64) },
		"settings":        func(value *CLIRouteQualificationOptions) { value.SettingsSHA256 = strings.Repeat("9", 64) },
		"response schema": func(value *CLIRouteQualificationOptions) { value.ResponseSchemaSHA256 = strings.Repeat("9", 64) },
		"prompt envelope": func(value *CLIRouteQualificationOptions) { value.PromptContractSHA256 = strings.Repeat("9", 64) },
		"timeout":         func(value *CLIRouteQualificationOptions) { value.TimeoutSeconds++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if second := cliRouteQualificationContractSHA256(identity, candidate); second == first {
				t.Fatalf("%s did not change the cli route qualification contract", name)
			}
		})
	}
	if second := cliRouteQualificationContractSHA256("binary-sha256:"+strings.Repeat("c", 64), base); second == first {
		t.Fatal("agent identity did not change the cli route qualification contract")
	}
	unset := base
	unset.TimeoutSeconds = 0
	if cliRouteQualificationContractSHA256(identity, unset) != first {
		t.Fatal("default timeout is not the bound timeout")
	}
}

func TestQualifyCLIRouteRejectsUnboundOptions(t *testing.T) {
	base := CLIRouteQualificationOptions{Provider: "codex", Surface: SurfaceCLISkill,
		AgentBinary: "agent", ScratchRoot: "scratch", Model: "synthetic-model", TimeoutSeconds: 10}
	for name, mutate := range map[string]func(*CLIRouteQualificationOptions){
		"provider": func(value *CLIRouteQualificationOptions) { value.Provider = "openai" },
		"surface":  func(value *CLIRouteQualificationOptions) { value.Surface = SurfaceATLMCP },
		"agent":    func(value *CLIRouteQualificationOptions) { value.AgentBinary = "" },
		"scratch":  func(value *CLIRouteQualificationOptions) { value.ScratchRoot = "" },
		"model":    func(value *CLIRouteQualificationOptions) { value.Model = "" },
		"timeout":  func(value *CLIRouteQualificationOptions) { value.TimeoutSeconds = maxCLIRouteProbeTimeout + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if _, err := QualifyCLIRoute(context.Background(), candidate); err == nil {
				t.Fatalf("unbound options accepted: %+v", candidate)
			}
		})
	}
	if _, err := QualifyCLIRoute(nil, base); err == nil { //nolint:staticcheck // the nil context is the control under test
		t.Fatal("nil context accepted")
	}
}

type cliRouteProbeFixtureConfig struct {
	Body          string `json:"body"`
	RequestCount  int    `json:"request_count"`
	Auxiliary     int    `json:"auxiliary"`
	Path          string `json:"path"`
	APIKey        string `json:"api_key"`
	Authorization string `json:"authorization"`
	Block         bool   `json:"block"`
}

func TestQualifyCLIRouteCapturesOneExactModelRequestPerProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native private agent qualification requires POSIX owner-only runtime")
	}
	codexShell := `{"model":"synthetic-model","stream":true,"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`
	claudeShell := `{"model":"synthetic-model","stream":true,"messages":[{"role":"user","content":"x"}],"tools":[` + claudeRouteProbeBashTool + `]}`
	tests := []struct {
		name      string
		provider  string
		config    cliRouteProbeFixtureConfig
		want      CLIRouteQualificationStatus
		wantRoute string
		wantAux   int
	}{
		{name: "codex supported", provider: "codex", want: CLIRouteQualificationSupported, wantRoute: "exec_command",
			config: cliRouteProbeFixtureConfig{Body: codexShell, RequestCount: 1, Block: true}},
		{name: "codex missing", provider: "codex", want: CLIRouteQualificationRouteMissing,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model","stream":true,"tools":[]}`, RequestCount: 1, Block: true}},
		{name: "codex ambiguous", provider: "codex", want: CLIRouteQualificationAmbiguous,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model","stream":true,"tools":[{"type":"custom","name":"exec_command","parameters":{}}]}`, RequestCount: 1, Block: true}},
		{name: "codex malformed", provider: "codex", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model","stream":true,"tools":`, RequestCount: 1, Block: true}},
		{name: "codex wrong model", provider: "codex", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"other-model","stream":true,"tools":[]}`, RequestCount: 1, Block: true}},
		{name: "codex credential", provider: "codex", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: codexShell, RequestCount: 1, Block: true, Authorization: "Bearer forbidden"}},
		{name: "codex never launched", provider: "codex", want: CLIRouteQualificationProcessFailed,
			config: cliRouteProbeFixtureConfig{}},
		{name: "codex unexpected route", provider: "codex", want: CLIRouteQualificationProcessFailed,
			config: cliRouteProbeFixtureConfig{Body: codexShell, RequestCount: 1, Block: true, Path: "/chat/completions"}},

		{name: "claude supported", provider: "claude-code", want: CLIRouteQualificationSupported, wantRoute: "bash",
			config: cliRouteProbeFixtureConfig{Body: claudeShell, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		{name: "claude auxiliary probe", provider: "claude-code", want: CLIRouteQualificationSupported, wantRoute: "bash", wantAux: 1,
			config: cliRouteProbeFixtureConfig{Body: claudeShell, RequestCount: 1, Block: true, APIKey: "synthetic", Auxiliary: 1}},
		{name: "claude missing", provider: "claude-code", want: CLIRouteQualificationRouteMissing,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model","stream":true,"messages":[{"role":"user"}],"tools":[]}`, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		{name: "claude ambiguous", provider: "claude-code", want: CLIRouteQualificationAmbiguous,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model","stream":true,"messages":[{"role":"user"}],"tools":[` + claudeRouteProbeBashTool + `,` + claudeRouteProbeBashTool + `]}`, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		{name: "claude malformed", provider: "claude-code", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"synthetic-model",`, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		{name: "claude wrong model", provider: "claude-code", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: `{"model":"other-model","stream":true,"messages":[{"role":"user"}],"tools":[]}`, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		{name: "claude no key", provider: "claude-code", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: claudeShell, RequestCount: 1, Block: true}},
		{name: "claude second auxiliary", provider: "claude-code", want: CLIRouteQualificationProcessFailed, wantAux: 1,
			config: cliRouteProbeFixtureConfig{Body: claudeShell, RequestCount: 1, Block: true, APIKey: "synthetic", Auxiliary: 2}},
		{name: "claude unexpected route", provider: "claude-code", want: CLIRouteQualificationProcessFailed,
			config: cliRouteProbeFixtureConfig{Body: claudeShell, RequestCount: 1, Block: true, APIKey: "synthetic", Path: "/v1/messages"}},
	}
	agents := map[string]string{
		"codex":       buildCLIRouteProbeTestAgent(t, "codex"),
		"claude-code": buildCLIRouteProbeTestAgent(t, "claude-code"),
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := agents[test.provider]
			writeCLIRouteProbeFixtureConfig(t, agent, test.config)
			scratch := t.TempDir()
			if err := os.Chmod(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			report, err := QualifyCLIRoute(context.Background(), CLIRouteQualificationOptions{
				Provider: test.provider, Surface: SurfaceCLISkill, AgentBinary: agent, ScratchRoot: scratch,
				Model: "synthetic-model", AllowedTools: []string{"Bash(atl *)", "Skill"}, TimeoutSeconds: 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.Validate() != nil || report.Status != test.want || report.Route != test.wantRoute ||
				report.AuxiliaryRequests != test.wantAux {
				t.Fatalf("report=%+v want_status=%s want_route=%q want_aux=%d", report, test.want, test.wantRoute, test.wantAux)
			}
			if report.Provider != test.provider || report.Surface != SurfaceCLISkill {
				t.Fatalf("report is not provider-scoped: %+v", report)
			}
			assertCLIRouteProbeNeverGotAModelResponse(t, agent)
			if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
				t.Fatalf("probe runtime was retained: entries=%d err=%v", len(entries), readErr)
			}
		})
	}
}

func TestFinalizeCLIRouteProbeRefusesRepeatedModelRequest(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) {
			base := CLIRouteQualificationReport{
				SchemaVersion:  CLIRouteQualificationSchemaVersion,
				Provider:       provider,
				Surface:        SurfaceCLISkill,
				AgentIdentity:  "binary-sha256:" + strings.Repeat("a", 64),
				ContractSHA256: strings.Repeat("b", 64),
			}
			report, err := finalizeCLIRouteProbe(base, []cliRouteProbeObservation{
				{status: CLIRouteQualificationSupported, route: map[string]string{"codex": "exec_command", "claude-code": "bash"}[provider]},
				{status: CLIRouteQualificationSupported},
			}, 0, false)
			if err != nil || report.Validate() != nil || report.Status != CLIRouteQualificationProcessFailed ||
				!report.RequestObserved || report.SyntheticRequests != 2 || report.Route != "" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestClaudeRouteProbeAuxiliaryContract(t *testing.T) {
	if cliRouteProbeTerminationStatus != http.StatusBadRequest {
		t.Fatalf("termination status=%d want=%d", cliRouteProbeTerminationStatus, http.StatusBadRequest)
	}
	binding := cliRouteProbeEndpoint("claude-code", "127.0.0.1:1", "nonce")
	for _, path := range []string{"/", "/api/hello", "/nonce", "/nonce/", "/nonce/api/hello"} {
		request := httptest.NewRequest(http.MethodHead, path, nil)
		if !binding.auxiliary(request) {
			t.Errorf("expected auxiliary route %q to be admitted", path)
		}
	}
	for _, test := range []struct {
		method, path string
	}{
		{method: http.MethodGet, path: "/"},
		{method: http.MethodPost, path: "/api/hello"},
		{method: http.MethodHead, path: "/other"},
		{method: http.MethodHead, path: "/nonce/api/hello?retry=1"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if binding.auxiliary(request) {
			t.Errorf("unexpected auxiliary route %s %q", test.method, test.path)
		}
	}
	for _, test := range []struct {
		name                   string
		modelStarted, admitted bool
		auxiliary              int
		ignored                bool
	}{
		{name: "first before model", admitted: true},
		{name: "second before model", auxiliary: 1},
		{name: "after model starts", modelStarted: true, ignored: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			admitted, ignored := classifyCLIRouteAuxiliary(test.modelStarted, test.auxiliary)
			if admitted != test.admitted || ignored != test.ignored {
				t.Fatalf("admitted=%t ignored=%t", admitted, ignored)
			}
		})
	}
}

func TestQualifyCLIRouteTerminatesTheChildAfterCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native private agent qualification requires POSIX owner-only runtime")
	}
	agent := buildCLIRouteProbeTestAgent(t, "claude-code")
	body := `{"model":"synthetic-model","stream":true,"messages":[{"role":"user","content":"x"}],"tools":[` + claudeRouteProbeBashTool + `]}`
	writeCLIRouteProbeFixtureConfig(t, agent, cliRouteProbeFixtureConfig{Body: body, RequestCount: 1, APIKey: "synthetic", Block: true})
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	// The child deliberately never exits. Only the post-capture cancellation can
	// end it, and it must do so well inside the bounded timeout.
	report, err := QualifyCLIRoute(context.Background(), CLIRouteQualificationOptions{
		Provider: "claude-code", Surface: SurfaceCLISkill, AgentBinary: agent, ScratchRoot: scratch,
		Model: "synthetic-model", AllowedTools: []string{"Bash(atl *)"}, TimeoutSeconds: 60,
	})
	if err != nil || !report.Supported() || report.Route != "bash" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertCLIRouteProbeNeverGotAModelResponse(t, agent)
}

func TestQualifyCLIRouteTimesOutClosedWithoutAModelRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native private agent qualification requires POSIX owner-only runtime")
	}
	agent := buildCLIRouteProbeTestAgent(t, "codex")
	writeCLIRouteProbeFixtureConfig(t, agent, cliRouteProbeFixtureConfig{Block: true})
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := QualifyCLIRoute(context.Background(), CLIRouteQualificationOptions{
		Provider: "codex", Surface: SurfaceCLISkill, AgentBinary: agent, ScratchRoot: scratch,
		Model: "synthetic-model", TimeoutSeconds: 1,
	})
	if err != nil || report.Validate() != nil || report.Status != CLIRouteQualificationProcessFailed ||
		report.RequestObserved || report.SyntheticRequests != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if entries, readErr := os.ReadDir(scratch); readErr != nil || len(entries) != 0 {
		t.Fatalf("probe runtime was retained after a timeout: entries=%d err=%v", len(entries), readErr)
	}
}

func TestQualifyCLIRouteKeepsAmbientClaudeCredentialsOutOfTheProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native private agent qualification requires POSIX owner-only runtime")
	}
	const ambient = "AMBIENT-OPERATOR-CREDENTIAL"
	t.Setenv("ANTHROPIC_API_KEY", ambient)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", ambient)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", ambient)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "operator-claude"))
	t.Setenv("HTTPS_PROXY", "https://operator-proxy.invalid")
	t.Setenv("https_proxy", "https://operator-proxy.invalid")

	agent := buildCLIRouteProbeTestAgent(t, "claude-code")
	body := `{"model":"synthetic-model","stream":true,"messages":[{"role":"user","content":"x"}],"tools":[` + claudeRouteProbeBashTool + `]}`
	tests := []struct {
		name   string
		config cliRouteProbeFixtureConfig
		want   CLIRouteQualificationStatus
	}{
		{name: "allowlisted key", want: CLIRouteQualificationSupported,
			config: cliRouteProbeFixtureConfig{Body: body, RequestCount: 1, Block: true, APIKey: "synthetic"}},
		// The body stays exactly the one the supported case uses, so only the
		// credential control can reject it.
		{name: "ambient key forwarded", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: body, RequestCount: 1, Block: true, APIKey: ambient}},
		{name: "ambient bearer forwarded", want: CLIRouteQualificationSchemaFailed,
			config: cliRouteProbeFixtureConfig{Body: body, RequestCount: 1, Block: true, APIKey: "synthetic", Authorization: "Bearer " + ambient}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeCLIRouteProbeFixtureConfig(t, agent, test.config)
			scratch := t.TempDir()
			if err := os.Chmod(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			report, err := QualifyCLIRoute(context.Background(), CLIRouteQualificationOptions{
				Provider: "claude-code", Surface: SurfaceCLISkill, AgentBinary: agent, ScratchRoot: scratch,
				Model: "synthetic-model", AllowedTools: []string{"Bash(atl *)"}, TimeoutSeconds: 20,
			})
			if err != nil || report.Validate() != nil || report.Status != test.want {
				t.Fatalf("report=%+v err=%v want=%s", report, err, test.want)
			}
			environment := readCLIRouteProbeEnvironment(t, agent)
			if environment["ANTHROPIC_API_KEY"] != cliRouteProbeSyntheticAPIKey {
				t.Fatalf("probe did not receive the fixed synthetic key: %q", environment["ANTHROPIC_API_KEY"])
			}
			for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "HTTPS_PROXY", "https_proxy"} {
				if environment[name] != "" {
					t.Fatalf("ambient %s crossed into the probe environment", name)
				}
			}
			if config := environment["CLAUDE_CONFIG_DIR"]; config == "" || strings.Contains(config, "operator-claude") {
				t.Fatalf("probe inherited the operator credential directory: %q", config)
			}
		})
	}
}

func writeCLIRouteProbeFixtureConfig(t *testing.T, agent string, config cliRouteProbeFixtureConfig) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent+".config", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(agent + ".events"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func readCLIRouteProbeEvents(t *testing.T, agent string) []string {
	t.Helper()
	data, err := os.ReadFile(agent + ".events")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func readCLIRouteProbeEnvironment(t *testing.T, agent string) map[string]string {
	t.Helper()
	for _, line := range readCLIRouteProbeEvents(t, agent) {
		value, found := strings.CutPrefix(line, "env ")
		if !found {
			continue
		}
		environment := map[string]string{}
		if err := json.Unmarshal([]byte(value), &environment); err != nil {
			t.Fatal(err)
		}
		return environment
	}
	t.Fatal("probe recorded no environment")
	return nil
}

// assertCLIRouteProbeNeverGotAModelResponse proves the loopback endpoint never
// fabricates a model answer: every model request the child observed came back
// as a non-success status with an empty JSON-free body.
func assertCLIRouteProbeNeverGotAModelResponse(t *testing.T, agent string) {
	t.Helper()
	for _, line := range readCLIRouteProbeEvents(t, agent) {
		value, found := strings.CutPrefix(line, "model ")
		if !found {
			continue
		}
		status, body, _ := strings.Cut(value, " ")
		if status == "200" || strings.Contains(body, "\"content\"") || strings.Contains(body, "\"output_text\"") {
			t.Fatalf("probe fabricated a model response: %s", line)
		}
	}
}

func buildCLIRouteProbeTestAgent(t *testing.T, provider string) string {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	binary := filepath.Join(directory, "agent")
	baseExpression := `os.Getenv("ANTHROPIC_BASE_URL")`
	modelPath := "/v1/messages?beta=true"
	if provider == "codex" {
		baseExpression = "codexBaseURL()"
		modelPath = "/responses"
	}
	program := fmt.Sprintf(`package main

import ("bytes"; "encoding/json"; "io"; "net/http"; "os"; "path/filepath"; "strconv"; "strings")

const configPath = %q
const eventsPath = %q
const provider = %q
const defaultModelPath = %q

type config struct {
	Body          string `+"`json:\"body\"`"+`
	RequestCount  int    `+"`json:\"request_count\"`"+`
	Auxiliary     int    `+"`json:\"auxiliary\"`"+`
	Path          string `+"`json:\"path\"`"+`
	APIKey        string `+"`json:\"api_key\"`"+`
	Authorization string `+"`json:\"authorization\"`"+`
	Block         bool   `+"`json:\"block\"`"+`
}

func event(line string) {
	file, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil { os.Exit(90) }
	_, _ = file.WriteString(line + "\n")
	_ = file.Sync()
	_ = file.Close()
}

func codexBaseURL() string {
	for index := 1; index+1 < len(os.Args); index++ {
		if os.Args[index] == "-c" && strings.HasPrefix(os.Args[index+1], "model_providers.atl_tool_probe.base_url=") {
			value, _ := strconv.Unquote(strings.TrimPrefix(os.Args[index+1], "model_providers.atl_tool_probe.base_url="))
			return value
		}
	}
	return ""
}

func main() {
	executable, _ := os.Executable()
	if !strings.HasPrefix(filepath.Base(filepath.Dir(executable)), "cli-route-qualification-") { os.Exit(6) }
	environment := map[string]string{}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "HOME", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY"} {
		environment[name] = os.Getenv(name)
	}
	encoded, _ := json.Marshal(environment)
	event("env " + string(encoded))

	data, err := os.ReadFile(configPath)
	if err != nil { os.Exit(70) }
	var settings config
	if json.Unmarshal(data, &settings) != nil { os.Exit(71) }
	base := %s
	if base == "" { os.Exit(72) }

	for index := 0; index < settings.Auxiliary; index++ {
		request, err := http.NewRequest(http.MethodHead, base+"/api/hello", nil)
		if err != nil { os.Exit(73) }
		response, err := http.DefaultClient.Do(request)
		if err != nil { break }
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		event("aux " + strconv.Itoa(response.StatusCode))
	}

	modelPath := settings.Path
	if modelPath == "" { modelPath = defaultModelPath }
	post := func() {
		request, err := http.NewRequest(http.MethodPost, base+modelPath, bytes.NewBufferString(settings.Body))
		if err != nil { return }
		request.Header.Set("Content-Type", "application/json")
		if provider == "claude-code" {
			switch settings.APIKey {
			case "":
			case "synthetic":
				request.Header.Set("X-Api-Key", os.Getenv("ANTHROPIC_API_KEY"))
			default:
				request.Header.Set("X-Api-Key", settings.APIKey)
			}
		}
		if settings.Authorization != "" { request.Header.Set("Authorization", settings.Authorization) }
		response, err := http.DefaultClient.Do(request)
		if err != nil { return }
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
		event("model " + strconv.Itoa(response.StatusCode) + " " + strings.ReplaceAll(strings.TrimSpace(string(body)), "\n", " "))
	}
	if settings.RequestCount == 1 {
		post()
	}
	if settings.Block { select {} }
}
`, binary+".config", binary+".events", provider, modelPath, baseExpression)
	writeTestFile(t, source, program, 0o600)
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build native cli route probe fixture: %v: %s", err, output)
	}
	return binary
}
