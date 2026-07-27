package agenteval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrivateCLIGatewayKeepsSourceCredentialsOutOfChildConfig(t *testing.T) {
	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamAuth = request.Header.Get("Authorization")
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	source := filepath.Join(t.TempDir(), "source")
	child := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "config.json"), `{"jira_url":`+quotedJSON(t, upstream.URL+`/jira`)+`,"confluence_url":"https://unused.example.invalid","update_base_url":"https://updates.example.invalid","render":{"display_time_zone":"UTC"}}`, 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"), `{"jira":"upstream-secret","confluence":"unused-secret"}`, 0o600)
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
	scenario := validScenario()
	scenario.Budgets.MaxBackendRequests = 2
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(auditDir, "audit.jsonl")
	gateway, err := startPrivateLiveGateway(source, child, audit, spec, scenario)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close(context.Background()) }()
	if gateway.state.config.MaxConcurrent != 1 {
		t.Fatalf("private CLI gateway concurrency=%d, want serialized command boundary", gateway.state.config.MaxConcurrent)
	}
	endpoint := gateway.Endpoints()["jira"]
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if upstreamAuth != "Bearer upstream-secret" {
		t.Fatalf("upstream auth=%q", upstreamAuth)
	}
	configData, err := os.ReadFile(filepath.Join(child, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	credentialData, err := os.ReadFile(filepath.Join(child, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	combined := string(configData) + string(credentialData)
	for _, forbidden := range []string{upstream.URL, "upstream-secret", "unused-secret", "unused.example.invalid", "updates.example.invalid"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("child config contains source value %q: %s", forbidden, combined)
		}
	}
	var childConfig map[string]json.RawMessage
	if err := json.Unmarshal(configData, &childConfig); err != nil {
		t.Fatal(err)
	}
	if string(childConfig["read_only"]) != "true" || childConfig["render"] == nil {
		t.Fatalf("child config=%s", configData)
	}
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPrivateCLIGatewayRequiresEveryScopedSourceService(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "config.json"), `{}`, 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"), `{}`, 0o600)
	spec := validRunSpec()
	spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "api", PathPrefix: "/rest/api/2"}}}
	spec.GatewayMaxResponseBytes = 1024
	spec.GatewayMaxTotalBytes = 1024
	scenario := validScenario()
	scenario.Budgets.MaxBackendRequests = 1
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrivateLiveGateway(source, filepath.Join(t.TempDir(), "child"), filepath.Join(auditDir, "audit"), spec, scenario); err == nil || !strings.Contains(strings.ToLower(err.Error()), "jira") {
		t.Fatalf("err=%v", err)
	}
}

func TestGatewayBackedInternalMCPIsolatesSourceCredentialsAndCountsQueryWrites(t *testing.T) {
	var upstreamAuthorizations []string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamAuthorizations = append(upstreamAuthorizations, request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"rows":[]}`))
	}))
	defer upstream.Close()

	source := filepath.Join(t.TempDir(), "source")
	child := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	const sourceCanary = "SYNTHETIC-SOURCE-CREDENTIAL-CANARY"
	writeTestFile(t, filepath.Join(source, "config.json"),
		`{"jira_url":`+quotedJSON(t, upstream.URL+`/jira`)+`,"confluence_url":"https://unused.example.invalid","render":{"display_time_zone":"UTC"}}`, 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"), `{"jira":"`+sourceCanary+`"}`, 0o600)

	_, spec, scenario := privateLiveQueryOnlyPair()
	if !gatewayBackedInternalMCP(spec) {
		t.Fatal("internal MCP spec is not gateway-backed")
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	spec.AllowedGatewayRoutes["jira"][2].MaxRequests = 2
	scenario.Budgets.MaxRemoteWrites = 2
	scenario.Budgets.MaxBackendRequests = 5
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "gateway-audit.jsonl")
	gateway, err := startPrivateLiveGateway(source, child, auditPath, spec, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.state.config.MaxConcurrent != 2 {
		t.Fatalf("internal MCP gateway concurrency=%d, want reviewed interface budget 2", gateway.state.config.MaxConcurrent)
	}

	// The child sees only the disposable loopback boundary: no upstream origin,
	// no source credential, and no insecure-transport carry-over that could let
	// it reach the real backend directly.
	configData, err := os.ReadFile(filepath.Join(child, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	credentialData, err := os.ReadFile(filepath.Join(child, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	combined := string(configData) + string(credentialData)
	for _, forbidden := range []string{sourceCanary, upstream.URL, "unused.example.invalid"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("child config leaked %q: %s", forbidden, combined)
		}
	}
	endpoint := gateway.Endpoints()["jira"]
	if !strings.Contains(string(configData), endpoint.BaseURL) || !strings.Contains(string(credentialData), endpoint.Token) {
		t.Fatalf("child config is not bound to the loopback gateway: %s", combined)
	}

	// The MCP child environment carries no upstream URL or PAT name, no
	// insecure-transport switch, and no HTTP guard file: the gateway is the only
	// audited transport in this path.
	environment := map[string]string{
		"ATL_READ_ONLY": "1", "ATL_NO_UPDATE": "1",
		"ATL_CONFIG_DIR": child, "ATL_MIRROR_ROOT": filepath.Join(child, "mirror"),
		"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
	}
	if err := validateGatewayMCPEnvironment(environment); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ATL_EVAL_HTTP_GUARD_FILE", "ATL_JIRA_URL", "ATL_CONFLUENCE_URL", "ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_ALLOW_INSECURE"} {
		candidate := map[string]string{forbidden: "x"}
		for name, value := range environment {
			candidate[name] = value
		}
		if err := validateGatewayMCPEnvironment(candidate); err == nil {
			t.Fatalf("gateway-backed MCP environment accepted %s", forbidden)
		}
	}

	for range 2 {
		request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/rest/structure/2.0/value", strings.NewReader(`{"rows":[1]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("query-only POST status=%d", response.StatusCode)
		}
	}
	if len(upstreamAuthorizations) != 2 || upstreamAuthorizations[0] != "Bearer "+sourceCanary {
		t.Fatalf("upstream authorizations=%v", upstreamAuthorizations)
	}

	methods, _, observed, err := closeAndReadLiveGatewayRecords(gateway)
	if err != nil || !observed {
		t.Fatalf("gateway records observed=%t err=%v", observed, err)
	}
	if methods["POST"] != 2 {
		t.Fatalf("methods=%v", methods)
	}
	observation := validObservation()
	observation.ScenarioID = scenario.ID
	observation.HTTPMethods = methods
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics.RemoteWrites != 2 {
		t.Fatalf("remote writes=%d, want the conservative transport count", result.Metrics.RemoteWrites)
	}
}

func TestGatewayBackedInternalMCPRunsProductionStructureViewRoute(t *testing.T) {
	fixture := loadRepositoryMockFixture(t, filepath.Join("..", "..", "benchmarks", "agent-eval",
		"jira-structure-folder-selection-recovery-mcp", "fixture.json"))
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	backendEnvironment := backend.Environment()

	source := filepath.Join(t.TempDir(), "source")
	child := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "config.json"),
		`{"jira_url":`+quotedJSON(t, backendEnvironment["ATL_JIRA_URL"])+`}`, 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"),
		`{"jira":`+quotedJSON(t, backendEnvironment["ATL_JIRA_PAT"])+`}`, 0o600)

	_, spec, scenario := privateLiveQueryOnlyPair()
	if spec.AllowedMCPTools[0] != "jira_structure_view" || scenario.Budgets.MaxRemoteWrites != 2 {
		t.Fatalf("production seam spec drifted: tools=%v budgets=%+v", spec.AllowedMCPTools, scenario.Budgets)
	}
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, err := startPrivateLiveGateway(source, child, filepath.Join(auditDir, "gateway-audit.jsonl"), spec, scenario)
	if err != nil {
		t.Fatal(err)
	}

	// ProductionDependencies must discover only the disposable child config;
	// ambient direct-backend overlays would bypass the boundary under test.
	t.Setenv("ATL_CONFIG_DIR", child)
	for _, name := range []string{"ATL_JIRA_URL", "JIRA_URL", "ATL_JIRA_PAT", "JIRA_PAT", "ATL_ALLOW_INSECURE"} {
		t.Setenv(name, "")
	}
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_NO_UPDATE", "1")
	client := connectRepositoryMCPClient(t)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jira_structure_view",
		Arguments: map[string]any{
			"structure_id": 95, "fields": []string{"key", "summary", "status"},
			"folder_row": 714, "expected_forest_signature": 9501, "expected_forest_version": 21,
			"max_rows": 50, "max_bytes": 65536,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("production jira_structure_view failed through gateway: %+v", result.Content)
	}
	methods, duplicates, observed, err := closeAndReadLiveGatewayRecords(gateway)
	if err != nil || !observed {
		t.Fatalf("gateway audit observed=%t duplicates=%d err=%v", observed, duplicates, err)
	}
	if methods["GET"] != 3 || methods["POST"] != 2 || len(methods) != 2 {
		t.Fatalf("production MCP route methods=%v, want GET=3 POST=2", methods)
	}
	backendMethods, unexpected, _ := backend.Summary()
	if unexpected != 0 || backendMethods["GET"] != 3 || backendMethods["POST"] != 2 {
		t.Fatalf("upstream production route methods=%v unexpected=%d", backendMethods, unexpected)
	}
}
