package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRouteLessInternalMCPUsesReadOnlyCompatibilityGateway(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		if request.Method != http.MethodGet || request.URL.RequestURI() != "/jira/rest/api/2/field?selector=PRIVATE-SELECTOR" {
			t.Errorf("unexpected upstream request: %s %s", request.Method, request.URL.RequestURI())
		}
		if request.Header.Get("Authorization") != "Bearer jira-upstream-secret" {
			t.Errorf("unexpected upstream authorization")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"private":"PRIVATE-RESPONSE"}`)
	}))
	defer upstream.Close()

	source := filepath.Join(t.TempDir(), "source")
	child := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "config.json"),
		`{"jira_url":`+quotedJSON(t, upstream.URL+`/jira`)+`,"confluence_url":`+quotedJSON(t, upstream.URL+`/confluence`)+`}`, 0o600)
	writeTestFile(t, filepath.Join(source, "credentials.json"),
		`{"jira":"jira-upstream-secret","confluence":"confluence-upstream-secret"}`, 0o600)
	_, spec, scenario := privateLiveQueryOnlyPair()
	spec.AllowedGatewayRoutes = nil
	spec.GatewayMaxResponseBytes = 0
	spec.GatewayMaxTotalBytes = 0
	spec.GatewayMaxRequestBytes = 0
	spec.GatewayMaxTotalRequestBytes = 0
	scenario.Budgets.MaxBackendRequests = 3
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.AllowedHTTPMethods = []string{http.MethodGet, http.MethodHead}
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "gateway-audit.jsonl")

	gateway, err := startPrivateLiveGateway(source, child, auditPath, spec, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.state.config.MaxRequests != scenario.Budgets.MaxBackendRequests ||
		gateway.state.config.MaxResponseBytes != compatibilityGatewayMaxResponseBytes ||
		gateway.state.config.MaxTotalResponseBytes != compatibilityGatewayMaxTotalResponseBytes {
		t.Fatalf("compatibility budgets=%+v", gateway.state.config)
	}
	for _, service := range []string{"jira", "confluence"} {
		routes := gateway.state.config.Services[service].Routes
		if len(routes) != 1 || !routes[0].compatibilityRoot ||
			!reflect.DeepEqual(routes[0].Methods, []string{http.MethodGet, http.MethodHead}) ||
			routes[0].MaxRequests != 0 {
			t.Fatalf("%s compatibility routes=%+v", service, routes)
		}
	}
	configData, err := os.ReadFile(filepath.Join(child, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	credentialData, err := os.ReadFile(filepath.Join(child, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	childData := append(configData, credentialData...)
	for _, forbidden := range []string{upstream.URL, "jira-upstream-secret", "confluence-upstream-secret", "ATL_EVAL_HTTP_GUARD_FILE"} {
		if bytes.Contains(childData, []byte(forbidden)) {
			t.Fatalf("child config leaked %q: %s", forbidden, childData)
		}
	}
	endpoint := gateway.Endpoints()["jira"]
	post, err := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/rest/api/2/field", strings.NewReader(`{"private":"PRIVATE-BODY"}`))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Authorization", "Bearer "+endpoint.Token)
	postResponse, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusMethodNotAllowed || upstreamCalls != 0 {
		t.Fatalf("POST status=%d upstream calls=%d", postResponse.StatusCode, upstreamCalls)
	}

	get, err := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field?selector=PRIVATE-SELECTOR", nil)
	if err != nil {
		t.Fatal(err)
	}
	get.Header.Set("Authorization", "Bearer "+endpoint.Token)
	getResponse, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(getResponse.Body)
	closeErr := getResponse.Body.Close()
	if readErr != nil || closeErr != nil || getResponse.StatusCode != http.StatusOK || upstreamCalls != 1 {
		t.Fatalf("GET status=%d upstream calls=%d read=%v close=%v", getResponse.StatusCode, upstreamCalls, readErr, closeErr)
	}
	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{
		"PRIVATE-SELECTOR", "PRIVATE-BODY", "PRIVATE-RESPONSE", upstream.URL,
		"jira-upstream-secret", "confluence-upstream-secret", endpoint.Token,
	} {
		if bytes.Contains(auditData, []byte(privateValue)) {
			t.Fatalf("gateway audit leaked %q: %s", privateValue, auditData)
		}
	}
}

func TestExplicitGatewayPolicyPassesThroughUnchanged(t *testing.T) {
	_, spec, _ := privateLiveQueryOnlyPair()
	inputs := liveGatewayInputs{}
	routes, maxResponse, maxTotal, err := effectiveLiveGatewayPolicy(inputs, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(routes, spec.AllowedGatewayRoutes) ||
		maxResponse != spec.GatewayMaxResponseBytes || maxTotal != spec.GatewayMaxTotalBytes {
		t.Fatalf("effective policy routes=%+v response=%d total=%d", routes, maxResponse, maxTotal)
	}
}

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
	scenario.Budgets.MaxInterfaceInvocations = 9
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "gateway-audit.jsonl")
	gateway, err := startPrivateLiveGateway(source, child, auditPath, spec, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.state.config.MaxConcurrent != 4 {
		t.Fatalf("internal MCP gateway concurrency=%d, want hard cap 4", gateway.state.config.MaxConcurrent)
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

	// The selected process receives only the disposable child config; ambient
	// direct-backend overlays never enter the subprocess environment.
	environment := map[string]string{
		"ATL_CONFIG_DIR":  child,
		"ATL_MIRROR_ROOT": filepath.Join(child, "mirror"),
		"ATL_READ_ONLY":   "1",
		"ATL_NO_UPDATE":   "1",
		"NO_PROXY":        "127.0.0.1,localhost",
		"no_proxy":        "127.0.0.1,localhost",
	}
	if err := validateGatewayMCPEnvironment(environment); err != nil {
		t.Fatal(err)
	}
	invocation := mustMCPInvocation(t, "jira_structure_view", map[string]any{
		"structure_id": 95, "fields": []string{"key", "summary", "status"},
		"folder_row": 714, "expected_forest_signature": 9501, "expected_forest_version": 21,
		"max_rows": 50, "max_bytes": 65536,
	})
	result := callGatewaySelectedATL(t, environment, invocation)
	if result.IsError {
		t.Fatalf("production jira_structure_view failed through gateway: %+v", result.TextContent)
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

// callGatewaySelectedATL runs one exact internal-MCP invocation through an
// attested private copy of the selected ATL executable. The gateway child
// config is the only backend authority; ambient process state is not inherited.
func callGatewaySelectedATL(t *testing.T, environment map[string]string, invocation MCPInvocation) SyntheticMCPResult {
	t.Helper()
	if err := validateGatewayMCPEnvironment(environment); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), defaultSyntheticATLTimeout)
	defer cancel()
	binary, err := inspectSelectedSyntheticATLBinary(repositorySyntheticATLBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyATLCapabilityCatalog(ctx, binary.canonicalPath, attemptLedgerRootForTest(t)); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := privateSyntheticATLScratch(t)
	binary, err = materializeSelectedSyntheticATLBinary(binary, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment)+3)
	for name, value := range environment {
		values[name] = value
	}
	temporary := filepath.Join(runtimeRoot, "tmp")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	values["TMPDIR"], values["TMP"], values["TEMP"] = temporary, temporary, temporary
	process, err := startBoundedMCPCommand(
		ctx, binary.executionPath, []string{"mcp", "serve", "--service", "jira"},
		runtimeRoot, flattenEnvironment(values), defaultSyntheticATLTimeout,
		defaultSyntheticATLMCPBytes, defaultSyntheticATLStderrBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, callErr := process.call(ctx, invocation)
	closeErr := process.Close()
	verifyErr := binary.verify()
	if callErr != nil {
		t.Fatal(callErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	return result
}
