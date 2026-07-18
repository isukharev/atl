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
	"confluence/edit": {}, "confluence/evidence": {}, "confluence/table-analytics": {},
	"jira/batch-analysis": {}, "jira/board-portfolio": {}, "jira/edit": {},
	"jira/evidence": {}, "jira/portfolio": {}, "jira/structure-planning": {},
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

type CorpusInventory struct {
	SchemaVersion int                    `json:"schema_version"`
	Scenarios     int                    `json:"scenarios"`
	Runs          int                    `json:"runs"`
	Classes       []CorpusClassInventory `json:"classes"`
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
	result := CorpusInventory{SchemaVersion: 1, Scenarios: scenarioCount, Runs: runCount, Classes: make([]CorpusClassInventory, 0, len(keys))}
	for _, key := range keys {
		result.Classes = append(result.Classes, classes[key])
	}
	return result, nil
}

func validateNeutralCommonCohorts(runs []loadedRun) (int, error) {
	base := runs[0]
	for _, run := range runs[1:] {
		if err := compareNeutralCommonContract(base, run); err != nil {
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

func compareNeutralCommonContract(base, candidate loadedRun) error {
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
		{"repetitions", base.spec.Repetitions == candidate.spec.Repetitions},
		{"timeout", base.spec.TimeoutSeconds == candidate.spec.TimeoutSeconds},
		{"cost cap", base.spec.MaxEstimatedCostMicroUSD == candidate.spec.MaxEstimatedCostMicroUSD},
		{"pricing", base.spec.Pricing == candidate.spec.Pricing},
		{"data capabilities", equalStrings(base.spec.DataCapabilities, candidate.spec.DataCapabilities)},
	}
	for _, comparison := range comparisons {
		if !comparison.equal {
			return fmt.Errorf("neutral-common runs differ in %s", comparison.name)
		}
	}
	return nil
}
