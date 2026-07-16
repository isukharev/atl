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
	gateway, err := startPrivateCLIGateway(source, child, audit, spec, scenario)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close(context.Background()) }()
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
	if _, err := startPrivateCLIGateway(source, filepath.Join(t.TempDir(), "child"), filepath.Join(auditDir, "audit"), spec, scenario); err == nil || !strings.Contains(strings.ToLower(err.Error()), "jira") {
		t.Fatalf("err=%v", err)
	}
}
