package agenteval

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This closed evidence class has no paired-provider corpus coverage.
// TestSelectedProjectPageCLIAndMCPProcessOracle in the product brokerserver
// package supplies positive selected CLI/MCP -> TLS Broker -> authority -> Jira
// conformance. The evaluator-owned test below supplies direct-mode refusal and
// exact startup inventory proof without importing product implementation.
const brokerSelectedProcessOnlyMCPTool = "jira_project_issue_page"

func TestBrokerProjectPageFamilyIsDistinctFromNeutralDirectSearch(t *testing.T) {
	want := "jira.project.issue-page"
	cli, cliOK := CapabilityFamilyForCLI([]string{"jira", "issue", "project-page", "--project", "PROJ"})
	mcp, mcpOK := CapabilityFamilyForMCP(brokerSelectedProcessOnlyMCPTool)
	if !cliOK || !mcpOK || cli != want || mcp != want {
		t.Fatalf("project page families CLI=%q/%t MCP=%q/%t", cli, cliOK, mcp, mcpOK)
	}
	if _, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{Family: want, Invocations: 1, Failures: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []RunSpec{
		{AllowedATLCommands: []string{"atl jira issue project-page --project PROJ"}},
		{AllowedMCPTools: []string{brokerSelectedProcessOnlyMCPTool}},
	} {
		if _, err := deriveRunDataCapabilities(spec); err == nil {
			t.Fatal("Broker-qualified offset page was treated as neutral direct search")
		}
	}
}

func TestSelectedBrokerProjectPageMCPRefusesDirectFixtureWithoutHTTP(t *testing.T) {
	binary, err := filepath.Abs(filepath.Join("..", "..", "atl"))
	if err != nil {
		t.Fatal(err)
	}
	invocation, ok := newMCPInvocation(brokerSelectedProcessOnlyMCPTool, map[string]any{
		"project_key": "PROJ", "fields": []string{"summary", "description"}, "limit": 2,
		"cursor": "0", "max_bytes": 8192,
	})
	if !ok {
		t.Fatal("construct exact project-page invocation")
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: binary, Fixture: minimalSyntheticFixture(), ScratchRoot: privateSyntheticScratch(t),
		MCPService: "jira", MCPInvocations: []MCPInvocation{invocation}, VerifyMCPToolInventory: true,
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	})
	result, err := process.CallMCPJSON(t.Context(), invocation)
	if err != nil || !result.IsError || len(result.StructuredContent) != 0 || len(result.TextContent) != 1 {
		t.Fatalf("direct-mode refusal result=%+v err=%v", result, err)
	}
	var refusal struct {
		Kind        string          `json:"kind"`
		Remediation string          `json:"remediation"`
		Message     string          `json:"message"`
		Recovery    json.RawMessage `json:"recovery"`
	}
	decoder := json.NewDecoder(strings.NewReader(result.TextContent[0]))
	decoder.DisallowUnknownFields()
	if validateJSONNoDuplicateKeys([]byte(result.TextContent[0])) != nil || decoder.Decode(&refusal) != nil || decoder.Decode(new(any)) != io.EOF ||
		refusal.Kind != "usage_error" || refusal.Remediation != "fix_request" || refusal.Message == "" || !validCLIErrorRecoveryJSON(refusal.Recovery) {
		t.Fatal("direct-mode refusal did not preserve its exact typed error contract")
	}
	var recovery struct {
		SchemaVersion int    `json:"schema_version"`
		Action        string `json:"action"`
		RetrySafe     bool   `json:"retry_safe"`
	}
	decoder = json.NewDecoder(strings.NewReader(string(refusal.Recovery)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&recovery) != nil || recovery.SchemaVersion != 1 || recovery.Action != "adjust_request" || recovery.RetrySafe {
		t.Fatal("direct-mode refusal suggested replay or an unsupported recovery route")
	}
	counts := process.Summary()
	if len(counts.HTTPMethods) != 0 || counts.UnexpectedRequests != 0 || counts.DuplicateRequests != 0 || len(counts.CLIInvocations) != 0 ||
		len(counts.MCPInvocations) != 1 || counts.MCPInvocations[brokerSelectedProcessOnlyMCPTool] != 1 {
		t.Fatalf("direct-mode refusal used an unadmitted route: %+v", counts)
	}
}
