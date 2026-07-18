package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalMCPDryRunValidatesProfileWithoutCredentialsOrNetwork(t *testing.T) {
	caseDir := t.TempDir()
	_ = os.Chmod(caseDir, 0o700)
	workspace := filepath.Join(caseDir, "workspace")
	_ = os.Mkdir(workspace, 0o700)
	writeTestFile(t, filepath.Join(workspace, "README.md"), "private task\n", 0o600)
	scenario := validScenario()
	scenario.ID = "external.private"
	scenario.DataClass = "private-local"
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredChecks = []string{"answer", "interface_ok", "guard", "no_delegate", "used"}
	scenario.RequiredSemanticChecks = []string{"answer"}
	scenario.RequiredMetrics = []string{"interface_invocations"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 4
	scenario.Budgets.MaxInterfaceInvocations = 2
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	filteredMetrics := scenario.RequiredMetrics[:0]
	for _, metric := range scenario.RequiredMetrics {
		if !externalMCPMetricIsOpaque(metric) {
			filteredMetrics = append(filteredMetrics, metric)
		}
	}
	scenario.RequiredMetrics = filteredMetrics
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)
	writeTestFile(t, filepath.Join(caseDir, "prompt.md"), "Use the available interface.\n", 0o600)
	writeTestFile(t, filepath.Join(caseDir, "response.json"), `{"type":"object","properties":{"answer":{"type":"boolean"}},"required":["answer"],"additionalProperties":false}`, 0o600)
	rubric := Rubric{SchemaVersion: 1, ID: "external", ScenarioID: scenario.ID, MinimumScoreBPS: 5000, Criteria: []RubricCriterion{{ID: "grounded", Description: "Grounded.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"missing"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "rubric.json"), rubric)
	checks := []RunCheck{{Name: "answer", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`true`)}, {Name: "interface_ok", Kind: "interface_all_succeeded"}, {Name: "guard", Kind: "guard_no_denials"}, {Name: "no_delegate", Kind: "delegations_none"}, {Name: "used", Kind: "interface_invocations_min", Minimum: 1}}
	spec := RunSpec{SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModePrivateLive, Category: BenchmarkCategoryNeutralCommon, Surface: SurfaceExternalMCP, ScenarioFile: "scenario.json", Provider: "codex", Variant: "external", Model: "test", PromptFile: "prompt.md", ResponseSchemaFile: "response.json", QualitativeRubricFile: "rubric.json", WorkspaceTemplate: "workspace", Repetitions: 1, TimeoutSeconds: 30, MaxEstimatedCostMicroUSD: 10_000_000, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}, ToolTransport: "mcp", AllowedMCPTools: []string{"safe_lookup"}, DataCapabilities: []string{"jira.issue.field"}, Checks: checks}
	specPath := filepath.Join(caseDir, "run.json")
	writeJSONTestFile(t, specPath, spec)
	profile := validExternalTestProfile()
	profile.UpstreamURL = "https://unreachable.invalid/mcp"
	profile.Tools[0].Name = "safe_lookup"
	profilePath := filepath.Join(caseDir, "profile.json")
	writeJSONTestFile(t, profilePath, profile)
	live := t.TempDir()
	_ = os.Chmod(live, 0o700)
	writeTestFile(t, filepath.Join(live, "config.json"), "not-json", 0o600)
	writeTestFile(t, filepath.Join(live, "credentials.json"), "not-json", 0o600)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	if err := os.Chmod(outputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := RunHeadless(context.Background(), RunOptions{SpecPath: specPath, OutputRoot: outputRoot, RepositoryRoot: repo, AgentBinary: executable, ATLBinary: executable, PluginRoot: repo, WrapperExecutable: executable, LiveConfigDir: live, ExternalMCPProfile: profilePath, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(output.Preview)
	if bytes.Contains(encoded, []byte(profile.UpstreamURL)) || bytes.Contains(encoded, []byte(profile.CatalogSHA256)) || bytes.Contains(encoded, []byte("safe_lookup")) {
		t.Fatalf("preview leaked private profile: %s", encoded)
	}

	opaqueScenario := scenario
	opaqueScenario.RequiredMetrics = append(append([]string(nil), scenario.RequiredMetrics...), "backend_requests")
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), opaqueScenario)
	_, err = RunHeadless(context.Background(), RunOptions{SpecPath: specPath, OutputRoot: outputRoot, RepositoryRoot: repo, AgentBinary: executable, ATLBinary: executable, PluginRoot: repo, WrapperExecutable: executable, LiveConfigDir: live, ExternalMCPProfile: profilePath, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "opaque backend metrics") {
		t.Fatalf("external run accepted unobservable required metric: %v", err)
	}
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)

	spec.DataCapabilities = []string{"jira.issue.list"}
	writeJSONTestFile(t, specPath, spec)
	_, err = RunHeadless(context.Background(), RunOptions{SpecPath: specPath, OutputRoot: outputRoot, RepositoryRoot: repo, AgentBinary: executable, ATLBinary: executable, PluginRoot: repo, WrapperExecutable: executable, LiveConfigDir: live, ExternalMCPProfile: profilePath, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "external MCP profile data capabilities do not match the reviewed run") {
		t.Fatalf("mismatched external MCP data capability passed dry-run: %v", err)
	}
}

func TestExternalMCPProviderConfigurationContainsOnlyProxyIdentity(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "mcp.json")
	if err := writeClaudeExternalMCPConfig(path, "http://127.0.0.1:1234/mcp", "disposable"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, forbidden := range []string{"upstream.example", "real-pat"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("source secret leaked")
		}
	}
	spec := RunSpec{SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModePrivateLive, Surface: SurfaceExternalMCP, ScenarioFile: "a", Provider: "codex", Variant: "external", Model: "test", PromptFile: "b", ResponseSchemaFile: "c", QualitativeRubricFile: "d", WorkspaceTemplate: "e", Repetitions: 1, TimeoutSeconds: 1, MaxEstimatedCostMicroUSD: 1, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}, ToolTransport: "mcp", AllowedMCPTools: []string{"safe_lookup"}, Checks: []RunCheck{{Name: "x", Kind: "interface_all_succeeded"}}, mcpServerURL: "http://127.0.0.1:1234/mcp", mcpBearerTokenEnv: "ATL_EVAL_EXTERNAL_MCP_TOKEN"}
	plan, err := BuildProviderCommand(spec, "codex", "/bin/atl", "/bin/guard", "/tmp/work", "/tmp/schema", "/tmp/final", "", "", "", ProviderConfinement{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Args, " ")
	if strings.Contains(joined, "upstream.example") || strings.Contains(joined, "real-pat") || !strings.Contains(joined, "127.0.0.1:1234") {
		t.Fatalf("plan=%s", joined)
	}
}

func TestOpaqueMCPAssuranceSurvivesEvaluation(t *testing.T) {
	scenario := validScenario()
	observation := validObservation()
	observation.Surface = SurfaceExternalMCP
	observation.BackendObservation = BackendObservationOpaqueMCP
	observation.SafetyAssurance = SafetyAssuranceReviewedROMCP
	observation.Coverage["backend_requests"] = false
	observation.Coverage["duplicate_backend_requests"] = false
	observation.Coverage["remote_writes"] = false
	observation.HTTPMethods = map[string]int{}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackendObservation != BackendObservationOpaqueMCP || result.SafetyAssurance != SafetyAssuranceReviewedROMCP || result.Coverage["backend_requests"] {
		t.Fatalf("result=%+v", result)
	}
}

func TestExternalMCPProfileRejectsNeutralNamedWriteCapability(t *testing.T) {
	profile := validExternalTestProfile()
	profile.Tools[0].Name = "execute_query"
	profile.Tools[0].Capability = "jira.issue.field.set"
	if err := profile.Validate(); err == nil {
		t.Fatal("neutral-named write capability passed as read-only")
	}
	profile = validExternalTestProfile()
	profile.Tools[0].Name = "set_issue"
	if err := profile.Validate(); err == nil {
		t.Fatal("mutation-named tool passed as read-only")
	}
	profile = validExternalTestProfile()
	profile.Tools[0].Name = "read_dataset"
	if err := profile.Validate(); err != nil {
		t.Fatalf("safe token containing set was rejected: %v", err)
	}
}

func TestExternalMCPProfileRejectsHopByHopHeadersAndUnsafeValues(t *testing.T) {
	profile := validExternalTestProfile()
	profile.Headers[0].Name = "Connection"
	if err := profile.Validate(); err == nil {
		t.Fatal("hop-by-hop external MCP header passed")
	}
	for _, value := range []string{"", "token\nsmuggled", "token\x7f"} {
		if safeExternalMCPHeaderValue(value) {
			t.Fatalf("unsafe header value %q passed", value)
		}
	}
}

func TestExternalMCPProfileRejectsInvalidCatalogDigestVariants(t *testing.T) {
	for name, variants := range map[string][]string{
		"duplicate_primary":   {strings.Repeat("0", 64)},
		"duplicate_alternate": {strings.Repeat("1", 64), strings.Repeat("1", 64)},
		"malformed":           {"not-a-digest"},
		"oversized": {
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64),
			strings.Repeat("5", 64), strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("8", 64),
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := validExternalTestProfile()
			profile.CatalogSHA256Alternates = variants
			if err := profile.Validate(); err == nil {
				t.Fatal("invalid catalog digest variants passed")
			}
		})
	}
}

func TestExternalMCPProfileAggregateCallCapCannotExceedScenario(t *testing.T) {
	profile := validExternalTestProfile()
	profile.Tools[0].MaxInvocations = 2
	spec := RunSpec{AllowedMCPTools: []string{profile.Tools[0].Name}}
	scenario := validScenario()
	scenario.Budgets.MaxInterfaceInvocations = 1
	scenario.Budgets.MaxOutputBytes = profile.MaxTotalResponseBytes
	if err := validateExternalMCPProfileForRun(profile, spec, scenario); err == nil {
		t.Fatal("aggregate external MCP call cap exceeded the scenario budget")
	}
}

func TestExternalMCPBackendAssuranceCannotBeForged(t *testing.T) {
	base := validObservation()
	base.Coverage["remote_writes"] = true

	observationCases := map[string]func(Observation) Observation{
		"external_missing_assurance": func(value Observation) Observation {
			value.Surface = SurfaceExternalMCP
			return value
		},
		"external_claims_http": func(value Observation) Observation {
			value.Surface = SurfaceExternalMCP
			value.BackendObservation = BackendObservationHTTP
			value.SafetyAssurance = SafetyAssuranceObservedHTTP
			return value
		},
		"external_claims_opaque_backend_coverage": func(value Observation) Observation {
			value.Surface = SurfaceExternalMCP
			value.BackendObservation = BackendObservationOpaqueMCP
			value.SafetyAssurance = SafetyAssuranceReviewedROMCP
			return value
		},
		"cli_claims_opaque": func(value Observation) Observation {
			value.Surface = SurfaceCLISkill
			value.BackendObservation = BackendObservationOpaqueMCP
			value.SafetyAssurance = SafetyAssuranceReviewedROMCP
			return value
		},
		"remote_write_without_backend": func(value Observation) Observation {
			value.Surface = SurfaceExternalMCP
			value.BackendObservation = BackendObservationOpaqueMCP
			value.SafetyAssurance = SafetyAssuranceReviewedROMCP
			value.Coverage["backend_requests"] = false
			value.Coverage["duplicate_backend_requests"] = false
			value.Coverage["remote_writes"] = true
			value.HTTPMethods = map[string]int{}
			return value
		},
	}
	for name, mutate := range observationCases {
		t.Run("observation_"+name, func(t *testing.T) {
			value := mutate(cloneObservationForExternalTest(base))
			if err := value.Validate(); err == nil {
				t.Fatal("forged observation passed")
			}
		})
	}

	scenario := validScenario()
	result, err := Evaluate(scenario, validObservation())
	if err != nil {
		t.Fatal(err)
	}
	resultCases := map[string]func(Result) Result{
		"external_missing_assurance": func(value Result) Result {
			value.Surface = SurfaceExternalMCP
			return value
		},
		"external_claims_http": func(value Result) Result {
			value.Surface = SurfaceExternalMCP
			value.BackendObservation = BackendObservationHTTP
			value.SafetyAssurance = SafetyAssuranceObservedHTTP
			return value
		},
		"cli_claims_opaque": func(value Result) Result {
			value.Surface = SurfaceCLISkill
			value.BackendObservation = BackendObservationOpaqueMCP
			value.SafetyAssurance = SafetyAssuranceReviewedROMCP
			return value
		},
		"arbitrary_assurance": func(value Result) Result {
			value.Surface = SurfaceExternalMCP
			value.BackendObservation = "private-backend-name"
			value.SafetyAssurance = "trusted-because-user-said-so"
			return value
		},
	}
	for name, mutate := range resultCases {
		t.Run("result_"+name, func(t *testing.T) {
			value := mutate(result)
			if err := value.Validate(); err == nil {
				t.Fatal("forged result passed")
			}
		})
	}
}

func cloneObservationForExternalTest(value Observation) Observation {
	copy := value
	copy.Coverage = make(map[string]bool, len(value.Coverage))
	for key, present := range value.Coverage {
		copy.Coverage[key] = present
	}
	copy.HTTPMethods = make(map[string]int, len(value.HTTPMethods))
	for method, count := range value.HTTPMethods {
		copy.HTTPMethods[method] = count
	}
	return copy
}

func validExternalTestProfile() ExternalMCPProfile {
	return ExternalMCPProfile{SchemaVersion: 1, UpstreamURL: "https://example.invalid/mcp", ProtocolVersion: "2025-06-18", CatalogSHA256: strings.Repeat("0", 64), ReviewedRO: true, Headers: []ExternalMCPHeader{{Name: "X-Test-Auth", ValueFrom: "jira.credential"}}, Tools: []ExternalMCPToolPolicy{{Name: "safe_lookup", Capability: "jira.issue.field", InputSchemaSHA256: strings.Repeat("1", 64), MaxInvocations: 1, AllowedArguments: []json.RawMessage{json.RawMessage(`{"key":"A"}`)}}}, MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxTotalResponseBytes: 2048, MaxConcurrent: 1, TimeoutSeconds: 1}
}
