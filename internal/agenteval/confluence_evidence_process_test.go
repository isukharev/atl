package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func startRepositoryConfluenceEvidenceProcess(
	t *testing.T,
	fixture MockFixture,
	admissions []MCPInvocation,
) *SyntheticATLProcess {
	t.Helper()
	if len(admissions) == 0 {
		t.Fatal("Confluence evidence process requires exact MCP admissions")
	}
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: fixture,
		ScratchRoot: privateSyntheticATLScratch(t),
		MCPService:  "confluence", MCPInvocations: admissions,
		// A maximum-size released section can expand once in structured JSON and
		// again in the mandatory text projection carried by the MCP frame.
		MaxMCPBytes: 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Confluence evidence process: %v", err)
		}
	})
	return process
}

func callRepositoryConfluenceEvidence(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) (SyntheticMCPResult, string, bool) {
	t.Helper()
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil {
		return SyntheticMCPResult{}, err.Error(), false
	}
	if result.IsError {
		return result, strings.Join(result.TextContent, "\n"), false
	}
	assertRepositoryMCPTextMatchesStructured(t, result)
	return result, "", true
}

func assertRepositoryMCPTextMatchesStructured(t *testing.T, result SyntheticMCPResult) {
	t.Helper()
	if len(result.TextContent) != 1 {
		t.Fatalf("MCP result carried %d text projections, want one", len(result.TextContent))
	}
	structured, err := canonicalJSON(result.StructuredContent)
	if err != nil {
		t.Fatalf("canonicalize MCP structured content: %v", err)
	}
	text, err := canonicalJSON(json.RawMessage(result.TextContent[0]))
	if err != nil {
		t.Fatalf("MCP text projection is not strict JSON: %v", err)
	}
	if !bytes.Equal(text, structured) {
		t.Fatal("MCP text projection diverged from the bounded structured content")
	}
}
