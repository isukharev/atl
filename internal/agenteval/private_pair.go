package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type PrivateRunPair struct {
	SchemaVersion int      `json:"schema_version"`
	Comparable    bool     `json:"comparable"`
	Provider      string   `json:"provider"`
	Transports    []string `json:"transports"`
}

type PrivateComparisonSet struct {
	SchemaVersion int      `json:"schema_version"`
	Comparable    bool     `json:"comparable"`
	Category      string   `json:"category"`
	Provider      string   `json:"provider"`
	Surfaces      []string `json:"surfaces"`
}

// ValidatePrivateRunComparisonSet proves that two or three private-live specs
// vary only in their interface surface and surface-specific execution policy.
// It deliberately returns no scenario or filesystem identity.
func ValidatePrivateRunComparisonSet(paths ...string) (PrivateComparisonSet, error) {
	if len(paths) < 2 || len(paths) > 3 {
		return PrivateComparisonSet{}, fmt.Errorf("private comparison set requires 2..3 run specs")
	}
	loaded := make([]loadedRun, 0, len(paths))
	for index, path := range paths {
		item, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return PrivateComparisonSet{}, fmt.Errorf("private comparison run %d: %w", index+1, err)
		}
		if item.spec.EffectiveBackendMode() != BackendModePrivateLive {
			return PrivateComparisonSet{}, fmt.Errorf("comparison runs must use backend_mode=private-live")
		}
		loaded = append(loaded, item)
	}

	base := loaded[0]
	seenSurfaces := map[string]struct{}{}
	seenVariants := map[string]struct{}{}
	surfaces := make([]string, 0, len(loaded))
	baseSemantic, err := semanticRunChecks(base.spec.Checks)
	if err != nil {
		return PrivateComparisonSet{}, err
	}
	if len(baseSemantic) == 0 {
		return PrivateComparisonSet{}, fmt.Errorf("private comparison set requires at least one semantic response check")
	}
	for index, item := range loaded {
		if item.spec.SkillActivationIdentity() == SkillActivationExplicit {
			return PrivateComparisonSet{}, fmt.Errorf("multi-surface comparisons require implicit cli-skill activation")
		}
		if item.specDir != base.specDir {
			return PrivateComparisonSet{}, fmt.Errorf("comparison run specs must use the same private case directory")
		}
		surface := item.spec.EffectiveSurface()
		if _, exists := seenSurfaces[surface]; exists {
			return PrivateComparisonSet{}, fmt.Errorf("comparison run specs require unique surfaces")
		}
		seenSurfaces[surface] = struct{}{}
		surfaces = append(surfaces, surface)
		if _, exists := seenVariants[item.spec.Variant]; exists {
			return PrivateComparisonSet{}, fmt.Errorf("comparison run specs require unique variant names")
		}
		seenVariants[item.spec.Variant] = struct{}{}
		semantic, err := semanticRunChecks(item.spec.Checks)
		if err != nil {
			return PrivateComparisonSet{}, fmt.Errorf("comparison run %d: %w", index+1, err)
		}
		comparisons := []struct {
			name  string
			equal bool
		}{
			{"category", item.spec.EffectiveCategory() == base.spec.EffectiveCategory()},
			{"provider", item.spec.Provider == base.spec.Provider},
			{"model", item.spec.Model == base.spec.Model},
			{"reasoning", item.spec.Reasoning == base.spec.Reasoning},
			{"workspace", item.spec.WorkspaceTemplate == base.spec.WorkspaceTemplate && item.workspace == base.workspace},
			{"scenario and budgets", equalPrivateComparisonJSON(item.scenario, base.scenario)},
			{"core prompt", bytes.Equal(item.prompt, base.prompt)},
			{"response schema", bytes.Equal(item.responseSchema, base.responseSchema)},
			{"qualitative rubric", equalPrivateComparisonJSON(item.rubric, base.rubric)},
			{"semantic response checks", equalPrivateComparisonJSON(semantic, baseSemantic)},
			{"repetitions", item.spec.Repetitions == base.spec.Repetitions},
			{"timeout", item.spec.TimeoutSeconds == base.spec.TimeoutSeconds},
			{"cost cap", item.spec.MaxEstimatedCostMicroUSD == base.spec.MaxEstimatedCostMicroUSD},
			{"pricing", item.spec.Pricing == base.spec.Pricing},
			{"data capabilities", equalStrings(item.spec.DataCapabilities, base.spec.DataCapabilities)},
		}
		for _, comparison := range comparisons {
			if !comparison.equal {
				return PrivateComparisonSet{}, fmt.Errorf("comparison runs differ in %s", comparison.name)
			}
		}
	}
	if _, includesExternal := seenSurfaces[SurfaceExternalMCP]; includesExternal {
		for _, metric := range base.scenario.RequiredMetrics {
			if externalMCPMetricIsOpaque(metric) {
				return PrivateComparisonSet{}, fmt.Errorf("external MCP comparison cannot require opaque backend metrics")
			}
		}
	}
	sort.Strings(surfaces)
	return PrivateComparisonSet{
		SchemaVersion: 1, Comparable: true, Category: base.spec.EffectiveCategory(),
		Provider: base.spec.Provider, Surfaces: surfaces,
	}, nil
}

func externalMCPMetricIsOpaque(metric string) bool {
	switch metric {
	case "backend_requests", "duplicate_backend_requests", "remote_writes":
		return true
	default:
		return false
	}
}

// ValidatePrivateRunPair preserves the original CLI/atl-MCP contract and
// output shape while using the stricter comparison-set validation.
func ValidatePrivateRunPair(firstPath, secondPath string) (PrivateRunPair, error) {
	set, err := ValidatePrivateRunComparisonSet(firstPath, secondPath)
	if err != nil {
		return PrivateRunPair{}, err
	}
	want := []string{SurfaceATLMCP, SurfaceCLISkill}
	if !equalPrivateComparisonJSON(set.Surfaces, want) {
		return PrivateRunPair{}, fmt.Errorf("paired runs require cli-skill and atl-mcp surfaces")
	}
	first, err := loadRunInputs(RunOptions{SpecPath: firstPath})
	if err != nil {
		return PrivateRunPair{}, err
	}
	second, err := loadRunInputs(RunOptions{SpecPath: secondPath})
	if err != nil {
		return PrivateRunPair{}, err
	}
	if !equalPrivateComparisonJSON(first.spec.Checks, second.spec.Checks) {
		return PrivateRunPair{}, fmt.Errorf("paired runs differ in run checks")
	}
	return PrivateRunPair{SchemaVersion: 1, Comparable: true, Provider: set.Provider, Transports: []string{"cli", "mcp"}}, nil
}

func semanticRunChecks(checks []RunCheck) ([]RunCheck, error) {
	semantic := make([]RunCheck, 0, len(checks))
	for _, check := range checks {
		switch runCheckClass(check.Kind) {
		case "semantic":
			semantic = append(semantic, check)
		case "mechanical":
			// Surface-specific safety and trajectory controls may differ, but
			// their kinds stay in this closed classification.
		default:
			return nil, fmt.Errorf("run check kind %q has no comparison classification", check.Kind)
		}
	}
	sort.Slice(semantic, func(i, j int) bool { return semantic[i].Name < semantic[j].Name })
	return semantic, nil
}

func runCheckClass(kind string) string {
	switch kind {
	case "json_equals", "json_present", "json_equals_workspace_json":
		return "semantic"
	case "atl_invocations_min", "atl_invocations_max", "atl_all_succeeded", "atl_failures_equals",
		"interface_invocations_min", "interface_invocations_max", "interface_all_succeeded", "interface_failures_equals",
		"skill_invocations_min", "mock_no_unexpected", "delegations_min", "delegations_none",
		"guard_no_denials", "http_methods_observed", "http_methods_equal":
		return "mechanical"
	default:
		return ""
	}
}

func equalPrivateComparisonJSON(left, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}
