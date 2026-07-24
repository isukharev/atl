package agenteval

import (
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

const maxCorpusRunSpecs = 4096

var publicCorpusTaskClasses = map[string]struct{}{
	"confluence/edit": {}, "confluence/evidence": {}, "confluence/mirror": {}, "confluence/table-analytics": {},
	"jira/batch-analysis": {}, "jira/board-portfolio": {}, "jira/edit": {},
	"jira/evidence": {}, "jira/mirror": {}, "jira/portfolio": {}, "jira/structure-planning": {},
	"knowledge/search": {},
}

// CorpusClassInventory is deliberately aggregate-only: a successful private
// corpus validation does not expose scenario identities or filesystem paths.
type CorpusClassInventory struct {
	Category       string `json:"category"`
	TaskClass      string `json:"task_class"`
	Scenarios      int    `json:"scenarios"`
	Runs           int    `json:"runs"`
	ComparisonSets int    `json:"comparison_sets"`
}

type CorpusMCPProviderInventory struct {
	Provider                  string `json:"provider"`
	Specs                     int    `json:"specs"`
	Repetitions               int    `json:"repetitions"`
	N3PlusSpecs               int    `json:"n3_plus_specs"`
	N1Specs                   int    `json:"n1_specs"`
	DistinctHoldoutSpecs      int    `json:"distinct_holdout_specs"`
	ExactInvocationSpecs      int    `json:"exact_invocation_specs"`
	ExactN3PlusSpecs          int    `json:"exact_n3_plus_specs"`
	ExactDistinctHoldoutSpecs int    `json:"exact_distinct_holdout_specs"`
	ExactPrimaryScenarios     int    `json:"exact_primary_scenarios"`
	ExactHoldoutScenarios     int    `json:"exact_holdout_scenarios"`
}

type CorpusMCPToolInventory struct {
	Tool                 string                       `json:"tool"`
	Specs                int                          `json:"specs"`
	Repetitions          int                          `json:"repetitions"`
	ExactInvocationSpecs int                          `json:"exact_invocation_specs"`
	Providers            []CorpusMCPProviderInventory `json:"providers"`
}

type CorpusInventory struct {
	SchemaVersion int                      `json:"schema_version"`
	Scenarios     int                      `json:"scenarios"`
	Runs          int                      `json:"runs"`
	Classes       []CorpusClassInventory   `json:"classes"`
	MCPTools      []CorpusMCPToolInventory `json:"mcp_tools"`
}

// ValidateBenchmarkCorpus inventories every run.*.json below root and checks
// the contracts that make neutral-common runs comparable. Route-fixed and
// surface-native runs remain valid independent experiments; neutral-common
// runs must form provider/model/reasoning cohorts with two or three unique
// surfaces and an identical task, prompt, response schema, rubric, fixture,
// and semantic response checks.
func ValidateBenchmarkCorpus(root string) (CorpusInventory, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return CorpusInventory{}, fmt.Errorf("benchmark corpus root is invalid")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return CorpusInventory{}, fmt.Errorf("benchmark corpus root is unreadable")
	}
	byDirectory := map[string][]loadedRun{}
	runCount := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("benchmark corpus entry is unreadable")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "run.") || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		runCount++
		if runCount > maxCorpusRunSpecs {
			return fmt.Errorf("benchmark corpus exceeds %d run specs", maxCorpusRunSpecs)
		}
		loaded, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return fmt.Errorf("benchmark corpus contains an invalid run spec")
		}
		byDirectory[loaded.specDir] = append(byDirectory[loaded.specDir], loaded)
		return nil
	})
	if err != nil {
		return CorpusInventory{}, fmt.Errorf("benchmark corpus validation failed: %s", err)
	}
	if runCount == 0 {
		return CorpusInventory{}, fmt.Errorf("benchmark corpus contains no run specs")
	}

	type classKey struct{ category, taskClass string }
	classes := map[classKey]CorpusClassInventory{}
	seenScenarioIDs := map[string]string{}
	scenarioCount := 0
	for directory, runs := range byDirectory {
		base := runs[0]
		if _, ok := publicCorpusTaskClasses[base.scenario.TaskClass]; !ok {
			return CorpusInventory{}, fmt.Errorf("benchmark corpus uses a non-public task class")
		}
		if previous, exists := seenScenarioIDs[base.scenario.ID]; exists && previous != directory {
			return CorpusInventory{}, fmt.Errorf("benchmark scenario id is duplicated across directories")
		}
		seenScenarioIDs[base.scenario.ID] = directory
		scenarioCount++
		for _, run := range runs[1:] {
			if !equalPrivateComparisonJSON(run.scenario, base.scenario) {
				return CorpusInventory{}, fmt.Errorf("benchmark directory mixes scenario contracts")
			}
		}

		key := classKey{base.scenario.EffectiveCategory(), base.scenario.TaskClass}
		class := classes[key]
		class.Category, class.TaskClass = key.category, key.taskClass
		class.Scenarios++
		class.Runs += len(runs)
		if key.category == BenchmarkCategoryNeutralCommon {
			sets, err := validateNeutralCommonCohorts(runs)
			if err != nil {
				return CorpusInventory{}, err
			}
			class.ComparisonSets += sets
		}
		classes[key] = class
	}

	keys := make([]classKey, 0, len(classes))
	for key := range classes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].category != keys[j].category {
			return keys[i].category < keys[j].category
		}
		return keys[i].taskClass < keys[j].taskClass
	})
	result := CorpusInventory{
		SchemaVersion: 2, Scenarios: scenarioCount, Runs: runCount,
		Classes:  make([]CorpusClassInventory, 0, len(keys)),
		MCPTools: corpusMCPToolInventory(byDirectory),
	}
	for _, key := range keys {
		result.Classes = append(result.Classes, classes[key])
	}
	return result, nil
}

func corpusMCPToolInventory(byDirectory map[string][]loadedRun) []CorpusMCPToolInventory {
	type providerKey struct{ tool, provider string }
	type providerScenarios struct {
		primary map[string]struct{}
		holdout map[string]struct{}
	}
	tools := map[string]CorpusMCPToolInventory{}
	providers := map[providerKey]CorpusMCPProviderInventory{}
	scenarios := map[providerKey]providerScenarios{}
	for _, runs := range byDirectory {
		for _, run := range runs {
			if run.spec.EffectiveToolTransport() != "mcp" {
				continue
			}
			exactTools := corpusExactMCPTools(run.spec)
			isHoldout := corpusScenarioHasToken(run.scenario.ID, "holdout")
			for _, tool := range run.spec.AllowedMCPTools {
				inventory := tools[tool]
				inventory.Tool = tool
				inventory.Specs++
				inventory.Repetitions += run.spec.Repetitions
				if exactTools[tool] {
					inventory.ExactInvocationSpecs++
				}
				tools[tool] = inventory

				key := providerKey{tool: tool, provider: run.spec.Provider}
				provider := providers[key]
				provider.Provider = run.spec.Provider
				provider.Specs++
				provider.Repetitions += run.spec.Repetitions
				if run.spec.Repetitions >= 3 {
					provider.N3PlusSpecs++
				}
				if run.spec.Repetitions == 1 {
					provider.N1Specs++
					if isHoldout {
						provider.DistinctHoldoutSpecs++
					}
				}
				if exactTools[tool] {
					provider.ExactInvocationSpecs++
					if run.spec.Repetitions >= 3 && !isHoldout {
						provider.ExactN3PlusSpecs++
						coverage := scenarios[key]
						if coverage.primary == nil {
							coverage.primary = map[string]struct{}{}
						}
						coverage.primary[run.scenario.ID] = struct{}{}
						scenarios[key] = coverage
					}
					if run.spec.Repetitions == 1 && isHoldout {
						provider.ExactDistinctHoldoutSpecs++
						coverage := scenarios[key]
						if coverage.holdout == nil {
							coverage.holdout = map[string]struct{}{}
						}
						coverage.holdout[run.scenario.ID] = struct{}{}
						scenarios[key] = coverage
					}
				}
				providers[key] = provider
			}
		}
	}
	toolNames := make([]string, 0, len(tools))
	for tool := range tools {
		toolNames = append(toolNames, tool)
	}
	sort.Strings(toolNames)
	result := make([]CorpusMCPToolInventory, 0, len(toolNames))
	for _, tool := range toolNames {
		inventory := tools[tool]
		for key, provider := range providers {
			if key.tool == tool {
				provider.ExactPrimaryScenarios = len(scenarios[key].primary)
				provider.ExactHoldoutScenarios = len(scenarios[key].holdout)
				inventory.Providers = append(inventory.Providers, provider)
			}
		}
		sort.Slice(inventory.Providers, func(i, j int) bool {
			return inventory.Providers[i].Provider < inventory.Providers[j].Provider
		})
		result = append(result, inventory)
	}
	return result
}

func corpusScenarioHasToken(scenarioID, token string) bool {
	for _, part := range strings.FieldsFunc(scenarioID, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	}) {
		if strings.EqualFold(part, token) {
			return true
		}
	}
	return false
}

func corpusExactMCPTools(spec RunSpec) map[string]bool {
	result := map[string]bool{}
	for _, check := range spec.Checks {
		switch check.Kind {
		case "mcp_invocations_equal":
			invocations, ok := expectedMCPInvocations(check.Expected)
			if !ok {
				continue
			}
			for _, invocation := range invocations {
				result[invocation.Tool] = true
			}
		case "mcp_route_one_of":
			alternatives, ok := expectedMCPRouteAlternatives(check.Expected)
			if !ok || len(alternatives) == 0 {
				continue
			}
			required := map[string]bool{}
			for _, invocation := range alternatives[0].Invocations {
				required[invocation.Tool] = true
			}
			for _, alternative := range alternatives[1:] {
				present := map[string]bool{}
				for _, invocation := range alternative.Invocations {
					present[invocation.Tool] = true
				}
				for tool := range required {
					if !present[tool] {
						delete(required, tool)
					}
				}
			}
			for tool := range required {
				result[tool] = true
			}
		}
	}
	return result
}

func validateNeutralCommonCohorts(runs []loadedRun) (int, error) {
	base := runs[0]
	for _, run := range runs[1:] {
		if err := compareNeutralCommonTaskContract(base, run); err != nil {
			return 0, err
		}
	}
	type cohortKey struct{ provider, model, reasoning, backend string }
	cohorts := map[cohortKey][]loadedRun{}
	for _, run := range runs {
		key := cohortKey{run.spec.Provider, run.spec.Model, run.spec.Reasoning, run.spec.EffectiveBackendMode()}
		cohorts[key] = append(cohorts[key], run)
	}
	for _, cohort := range cohorts {
		if len(cohort) < 2 || len(cohort) > 3 {
			return 0, fmt.Errorf("neutral-common comparison cohort requires 2..3 surfaces")
		}
		base := cohort[0]
		for _, run := range cohort[1:] {
			if err := compareNeutralCommonExecutionContract(base, run); err != nil {
				return 0, err
			}
		}
		seen := map[string]struct{}{}
		variants := map[string]struct{}{}
		for _, run := range cohort {
			surface := run.spec.EffectiveSurface()
			if _, exists := seen[surface]; exists {
				return 0, fmt.Errorf("neutral-common comparison cohort requires unique surfaces")
			}
			seen[surface] = struct{}{}
			if _, exists := variants[run.spec.Variant]; exists {
				return 0, fmt.Errorf("neutral-common comparison cohort requires unique variants")
			}
			variants[run.spec.Variant] = struct{}{}
		}
	}
	return len(cohorts), nil
}

func compareNeutralCommonTaskContract(base, candidate loadedRun) error {
	semanticBase, err := semanticRunChecks(base.spec.Checks)
	if err != nil {
		return err
	}
	semanticCandidate, err := semanticRunChecks(candidate.spec.Checks)
	if err != nil {
		return err
	}
	comparisons := []struct {
		name  string
		equal bool
	}{
		{"scenario and budgets", equalPrivateComparisonJSON(base.scenario, candidate.scenario)},
		{"core prompt", bytes.Equal(base.prompt, candidate.prompt)},
		{"response schema", bytes.Equal(base.responseSchema, candidate.responseSchema)},
		{"qualitative rubric", equalPrivateComparisonJSON(base.rubric, candidate.rubric)},
		{"fixture", equalPrivateComparisonJSON(base.fixture, candidate.fixture)},
		{"semantic response checks", equalPrivateComparisonJSON(semanticBase, semanticCandidate)},
		{"data capabilities", equalStrings(base.spec.DataCapabilities, candidate.spec.DataCapabilities)},
		{"backend mode", base.spec.EffectiveBackendMode() == candidate.spec.EffectiveBackendMode()},
		{"workspace", base.spec.WorkspaceTemplate == candidate.spec.WorkspaceTemplate && base.workspace == candidate.workspace},
	}
	for _, comparison := range comparisons {
		if !comparison.equal {
			return fmt.Errorf("neutral-common runs differ in %s", comparison.name)
		}
	}
	return nil
}

func compareNeutralCommonExecutionContract(base, candidate loadedRun) error {
	comparisons := []struct {
		name  string
		equal bool
	}{
		{"repetitions", base.spec.Repetitions == candidate.spec.Repetitions},
		{"timeout", base.spec.TimeoutSeconds == candidate.spec.TimeoutSeconds},
		{"cost cap", base.spec.MaxEstimatedCostMicroUSD == candidate.spec.MaxEstimatedCostMicroUSD},
		{"pricing", base.spec.Pricing == candidate.spec.Pricing},
	}
	for _, comparison := range comparisons {
		if !comparison.equal {
			return fmt.Errorf("neutral-common cohort runs differ in %s", comparison.name)
		}
	}
	return nil
}
