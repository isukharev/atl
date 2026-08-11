package agenteval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureHeadlessTrajectoryRejectsExternalCanaryAndUsesProxyAuditMetrics(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(directory, "transcript.jsonl")
	stderrPath := filepath.Join(directory, "agent.stderr")
	finalPath := filepath.Join(directory, "final.json")
	auditPath := filepath.Join(directory, "external-audit.jsonl")
	counterPath := filepath.Join(directory, "atl-invocations.jsonl")
	writeTestFile(t, transcriptPath,
		`{"type":"item.completed","item":{"id":"provider-1","type":"mcp_tool_call","server":"external_ro","tool":"provider_only","status":"completed","result":{"provider":true}}}`+"\n"+
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`+"\n", 0o600)
	writeTestFile(t, finalPath, `{"answer":"ok"}`+"\n", 0o600)
	writeTestFile(t, auditPath, `{"sequence":1,"capability":"jira.issue.field","decision":"allow","success":true,"response_bytes":17}`+"\n", 0o600)
	writeTestFile(t, counterPath, `{"command_family":"jira.issue","stdout_bytes":99,"stderr_bytes":0,"exit_code":1}`+"\n", 0o600)
	contract := resolvedRunContract{spec: RunSpec{Provider: "codex", Surface: SurfaceExternalMCP, ToolTransport: "mcp"}}
	input := headlessTrajectoryCaptureInput{
		contract: contract, transcriptPath: transcriptPath, stderrPath: stderrPath, finalPath: finalPath,
		externalAuditPath: auditPath, counterPath: counterPath,
		guardCounterPath: filepath.Join(directory, "missing-guard.jsonl"), httpGuardPath: filepath.Join(directory, "missing-http.jsonl"),
		externalCanaries: []string{"protected-canary"},
	}
	writeTestFile(t, stderrPath, "protected-canary\n", 0o600)
	if _, err := captureHeadlessTrajectory(input); err == nil || !strings.Contains(err.Error(), "protected material reached a provider-visible artifact") {
		t.Fatalf("canary error=%v", err)
	}
	writeTestFile(t, stderrPath, "safe\n", 0o600)
	trajectory, err := captureHeadlessTrajectory(input)
	if err != nil {
		t.Fatal(err)
	}
	if trajectory.atlInvocations != 1 || trajectory.failedATL != 0 || trajectory.guardDenials != 0 ||
		trajectory.providerMetrics.MCPToolCalls != 1 || trajectory.providerMetrics.FailedMCPToolCalls != 0 ||
		trajectory.externalOutputBytes != 17 || len(trajectory.externalFamilies) != 1 ||
		trajectory.externalFamilies[0].Family != "jira.issue.field" || trajectory.externalFamilies[0].Invocations != 1 ||
		len(trajectory.proxyRecords) != 0 || trajectory.cliExitCodes != nil || trajectory.cliErrorContracts != nil {
		t.Fatalf("trajectory=%+v", trajectory)
	}
	writeTestFile(t, auditPath, "{\"invalid\":true}\n", 0o600)
	partial, err := captureHeadlessTrajectory(input)
	if err == nil {
		t.Fatal("invalid external audit was accepted")
	}
	usage := providerMetricsAttemptUsage(partial.providerMetrics, Pricing{
		InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 1_000_000,
	})
	if usage.InputTokens.Value == nil || *usage.InputTokens.Value != 10 ||
		usage.OutputTokens.Value == nil || *usage.OutputTokens.Value != 2 ||
		usage.EstimatedCostMicroUSD.Value == nil || *usage.EstimatedCostMicroUSD.Value != 12 {
		t.Fatalf("partial usage=%+v err=%v", usage, err)
	}
}
