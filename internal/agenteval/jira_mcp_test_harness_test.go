package agenteval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startRepositoryJiraHistoryMCPBackend remains the in-process harness for the
// Jira history evaluator family that has not crossed the selected ATL process
// boundary yet. Graph and snapshot-reconciliation families must not use it.
func startRepositoryJiraHistoryMCPBackend(
	t *testing.T,
	fixture MockFixture,
) (*MockBackend, *mcp.ClientSession) {
	t.Helper()
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	for name, value := range backend.Environment() {
		t.Setenv(name, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_NO_UPDATE", "1")
	return backend, connectRepositoryMCPClient(t)
}

func callRepositoryJiraHistoryMCP(
	t *testing.T,
	client *mcp.ClientSession,
	invocation MCPInvocation,
) *mcp.CallToolResult {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: invocation.Tool, Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
