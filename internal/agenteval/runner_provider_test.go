package agenteval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareHeadlessProviderResourcesCleansTemporaryConfigAfterPartialFailure(t *testing.T) {
	scratchRoot := t.TempDir()
	if err := os.Chmod(scratchRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	evalDir := t.TempDir()
	if err := os.Chmod(evalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	contract := resolvedRunContract{
		spec: RunSpec{
			BackendMode: BackendModePrivateLive, ToolTransport: "cli",
			AllowedGatewayRoutes: map[string][]LiveGatewayRoute{"jira": {{
				Name: "read", PathPrefix: "/rest/api/2", Methods: []string{"GET"}, MaxRequests: 1,
			}}},
		},
		scenario: Scenario{Budgets: Budgets{MaxBackendRequests: 1}},
	}
	layout := headlessAttemptLayout{
		privateCLI: true, evalDir: evalDir, workspace: t.TempDir(), mirrorRoot: filepath.Join(evalDir, "mirror"),
		atlConfigDir: filepath.Join(evalDir, "atl-config"),
	}
	_, err := prepareHeadlessProviderResources(context.Background(), contract, runAttemptBindings{
		scratchRoot: scratchRoot, liveConfigDir: filepath.Join(scratchRoot, "missing-source"),
	}, layout)
	if err == nil {
		t.Fatal("partial provider setup unexpectedly succeeded")
	}
	entries, readErr := os.ReadDir(scratchRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("partial provider setup retained temporary config: %v", entries)
	}
}

func TestHeadlessProviderDeferredCleanupRemovesTemporaryConfigIdempotently(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	temporaryConfig := filepath.Join(directory, "live-config")
	if err := os.Mkdir(temporaryConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	resources := &headlessProviderResources{temporaryConfigDir: temporaryConfig}
	resources.closeDeferred()
	resources.closeDeferred()
	if _, err := os.Stat(temporaryConfig); !os.IsNotExist(err) {
		t.Fatalf("deferred cleanup retained temporary config: %v", err)
	}
}

func TestHeadlessProviderDeferredCleanupClosesSyntheticBackendIdempotently(t *testing.T) {
	backend, err := StartMockBackend(MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: []byte(`[]`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := backend.HTTPServer()
	resources := &headlessProviderResources{backend: backend}
	resources.closeDeferred()
	resources.closeDeferred()
	if response, err := server.Client().Get(server.URL + "/jira/rest/api/2/field"); err == nil {
		_ = response.Body.Close()
		t.Fatal("deferred cleanup retained the synthetic backend listener")
	}
}
