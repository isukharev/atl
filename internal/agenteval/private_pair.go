package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type PrivateRunPair struct {
	SchemaVersion int      `json:"schema_version"`
	Comparable    bool     `json:"comparable"`
	Provider      string   `json:"provider"`
	Transports    []string `json:"transports"`
}

func ValidatePrivateRunPair(firstPath, secondPath string) (PrivateRunPair, error) {
	first, err := loadRunInputs(RunOptions{SpecPath: firstPath})
	if err != nil {
		return PrivateRunPair{}, fmt.Errorf("first private run spec: %w", err)
	}
	second, err := loadRunInputs(RunOptions{SpecPath: secondPath})
	if err != nil {
		return PrivateRunPair{}, fmt.Errorf("second private run spec: %w", err)
	}
	if first.spec.EffectiveBackendMode() != BackendModePrivateLive || second.spec.EffectiveBackendMode() != BackendModePrivateLive {
		return PrivateRunPair{}, fmt.Errorf("paired runs must both use backend_mode=private-live")
	}
	if first.specDir != second.specDir {
		return PrivateRunPair{}, fmt.Errorf("paired run specs must use the same private case directory")
	}
	byTransport := map[string]loadedRun{}
	for _, loaded := range []loadedRun{first, second} {
		if _, exists := byTransport[loaded.spec.ToolTransport]; exists {
			return PrivateRunPair{}, fmt.Errorf("paired runs require exactly one cli and one mcp transport")
		}
		byTransport[loaded.spec.ToolTransport] = loaded
	}
	cli, cliOK := byTransport["cli"]
	mcp, mcpOK := byTransport["mcp"]
	if !cliOK || !mcpOK {
		return PrivateRunPair{}, fmt.Errorf("paired runs require exactly one cli and one mcp transport")
	}
	if cli.spec.Variant == mcp.spec.Variant {
		return PrivateRunPair{}, fmt.Errorf("paired runs require distinct variant names")
	}
	comparisons := []struct {
		name  string
		equal bool
	}{
		{"provider", cli.spec.Provider == mcp.spec.Provider},
		{"model", cli.spec.Model == mcp.spec.Model},
		{"reasoning", cli.spec.Reasoning == mcp.spec.Reasoning},
		{"workspace", cli.spec.WorkspaceTemplate == mcp.spec.WorkspaceTemplate && cli.workspace == mcp.workspace},
		{"scenario", equalPrivatePairJSON(cli.scenario, mcp.scenario)},
		{"prompt", bytes.Equal(cli.prompt, mcp.prompt)},
		{"response schema", bytes.Equal(cli.responseSchema, mcp.responseSchema)},
		{"qualitative rubric", equalPrivatePairJSON(cli.rubric, mcp.rubric)},
		{"run checks", equalPrivatePairJSON(cli.spec.Checks, mcp.spec.Checks)},
		{"repetitions", cli.spec.Repetitions == mcp.spec.Repetitions},
		{"timeout", cli.spec.TimeoutSeconds == mcp.spec.TimeoutSeconds},
		{"cost cap", cli.spec.MaxEstimatedCostMicroUSD == mcp.spec.MaxEstimatedCostMicroUSD},
		{"pricing", cli.spec.Pricing == mcp.spec.Pricing},
	}
	for _, comparison := range comparisons {
		if !comparison.equal {
			return PrivateRunPair{}, fmt.Errorf("paired runs differ in %s", comparison.name)
		}
	}
	return PrivateRunPair{SchemaVersion: 1, Comparable: true, Provider: cli.spec.Provider, Transports: []string{"cli", "mcp"}}, nil
}

func equalPrivatePairJSON(left, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}
