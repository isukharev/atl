package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

type repositorySamplingPair struct {
	Primary repositorySamplingCohort
	Holdout repositorySamplingCohort
}

type repositorySamplingCohort struct {
	Root     string
	Scenario Scenario
	Runs     map[string]RunSpec
}

type repositoryRunContract struct {
	Spec RunSpec
	Raw  map[string]json.RawMessage
}

func loadRepositorySamplingPairContract(t *testing.T, primaryName string) repositorySamplingPair {
	t.Helper()
	contract := loadEvaluatorBehaviorContract(t)
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	pair, err := resolveRepositorySamplingPairContract(contract, root, primaryName)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func resolveRepositorySamplingPairContract(
	contract evaluatorBehaviorContract,
	root, primaryName string,
) (repositorySamplingPair, error) {
	var pairContract evaluatorSamplingPairContract
	pairMatches := 0
	for _, pair := range contract.PairIdentity.Pairs {
		if pair.Primary == primaryName {
			pairContract = pair
			pairMatches++
		}
	}
	if pairMatches != 1 {
		return repositorySamplingPair{}, fmt.Errorf("sampling pair %q contract rows=%d want=1", primaryName, pairMatches)
	}

	var fileSet evaluatorRunFileSetContract
	fileSetMatches := 0
	for _, candidate := range contract.PairIdentity.RunFileSets {
		if candidate.Name == pairContract.RunFileSet {
			fileSet = candidate
			fileSetMatches++
		}
	}
	if fileSetMatches != 1 || len(fileSet.Primary) == 0 || len(fileSet.Holdout) == 0 {
		return repositorySamplingPair{}, fmt.Errorf("sampling pair %q run file set %q rows=%d primary=%v holdout=%v",
			primaryName, pairContract.RunFileSet, fileSetMatches, fileSet.Primary, fileSet.Holdout)
	}

	primaryRoot := filepath.Join(root, primaryName)
	holdoutRoot := primaryRoot + "-holdout"
	primaryRuns, err := readRunContracts(primaryRoot, 3)
	if err != nil {
		return repositorySamplingPair{}, fmt.Errorf("primary runs: %w", err)
	}
	holdoutRuns, err := readRunContracts(holdoutRoot, 1)
	if err != nil {
		return repositorySamplingPair{}, fmt.Errorf("holdout runs: %w", err)
	}
	primaryFiles, holdoutFiles := sortedMapKeys(primaryRuns), sortedMapKeys(holdoutRuns)
	wantPrimaryFiles, wantHoldoutFiles := slices.Clone(fileSet.Primary), slices.Clone(fileSet.Holdout)
	sort.Strings(wantPrimaryFiles)
	sort.Strings(wantHoldoutFiles)
	if !slices.Equal(primaryFiles, wantPrimaryFiles) || !slices.Equal(holdoutFiles, wantHoldoutFiles) {
		return repositorySamplingPair{}, fmt.Errorf("run file set %q drifted: primary=%v want=%v holdout=%v want=%v",
			pairContract.RunFileSet, primaryFiles, wantPrimaryFiles, holdoutFiles, wantHoldoutFiles)
	}
	scenarioFile, err := samplingPairScenarioFile(primaryRuns, holdoutRuns)
	if err != nil {
		return repositorySamplingPair{}, err
	}
	primaryScenario, primaryRaw, err := readScenarioContract(primaryRoot, scenarioFile)
	if err != nil {
		return repositorySamplingPair{}, fmt.Errorf("primary scenario: %w", err)
	}
	holdoutScenario, holdoutRaw, err := readScenarioContract(holdoutRoot, scenarioFile)
	if err != nil {
		return repositorySamplingPair{}, fmt.Errorf("holdout scenario: %w", err)
	}
	if primaryScenario.ID == holdoutScenario.ID {
		return repositorySamplingPair{}, fmt.Errorf("holdout reused the primary scenario id %q", primaryScenario.ID)
	}
	gotScenarioExceptions := differingJSONFields(primaryRaw, holdoutRaw, contract.PairIdentity.ScenarioFields)
	wantScenarioExceptions := slices.Clone(pairContract.ScenarioExceptions)
	sort.Strings(wantScenarioExceptions)
	if !slices.Equal(gotScenarioExceptions, wantScenarioExceptions) {
		return repositorySamplingPair{}, fmt.Errorf("scenario identity exceptions drifted: got=%v want=%v",
			gotScenarioExceptions, wantScenarioExceptions)
	}
	for _, name := range sortedMapKeys(primaryRuns) {
		primary := primaryRuns[name]
		holdout, ok := holdoutRuns[name]
		if !ok {
			continue
		}
		got := differingJSONFields(primary.Raw, holdout.Raw, contract.PairIdentity.RunFields)
		want := slices.Clone(pairContract.RunExceptions[name])
		sort.Strings(want)
		if !slices.Equal(got, want) {
			return repositorySamplingPair{}, fmt.Errorf("%s run identity exceptions drifted: got=%v want=%v", name, got, want)
		}
	}
	for _, name := range sortedMapKeys(pairContract.RunExceptions) {
		if _, ok := primaryRuns[name]; !ok {
			return repositorySamplingPair{}, fmt.Errorf("stale run exception for %s in primary", name)
		}
		if _, ok := holdoutRuns[name]; !ok {
			return repositorySamplingPair{}, fmt.Errorf("stale run exception for %s in holdout", name)
		}
	}

	return repositorySamplingPair{
		Primary: repositorySamplingCohort{
			Root: primaryRoot, Scenario: primaryScenario, Runs: runSpecs(primaryRuns),
		},
		Holdout: repositorySamplingCohort{
			Root: holdoutRoot, Scenario: holdoutScenario, Runs: runSpecs(holdoutRuns),
		},
	}, nil
}

func samplingPairScenarioFile(
	primary, holdout map[string]repositoryRunContract,
) (string, error) {
	bound := ""
	for _, item := range []struct {
		cohort string
		runs   map[string]repositoryRunContract
	}{
		{cohort: "primary", runs: primary},
		{cohort: "holdout", runs: holdout},
	} {
		for _, name := range sortedMapKeys(item.runs) {
			candidate := item.runs[name].Spec.ScenarioFile
			if candidate == "" || filepath.Base(candidate) != candidate {
				return "", fmt.Errorf("%s %s scenario file is missing or unsafe: %q", item.cohort, name, candidate)
			}
			if bound == "" {
				bound = candidate
				continue
			}
			if candidate != bound {
				return "", fmt.Errorf("sampling pair run scenario files drifted: %s %s=%q want=%q",
					item.cohort, name, candidate, bound)
			}
		}
	}
	if bound == "" {
		return "", fmt.Errorf("sampling pair run scenario file is missing")
	}
	return bound, nil
}

func readScenarioContract(root, name string) (Scenario, map[string]json.RawMessage, error) {
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, nil, fmt.Errorf("scenario inventory %s bound=%q: %w", filepath.Base(root), name, err)
	}
	scenario, err := DecodeScenario(bytes.NewReader(data))
	if err != nil {
		return Scenario{}, nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Scenario{}, nil, err
	}
	return scenario, raw, nil
}

func readRunContracts(root string, wantRepetitions int) (map[string]repositoryRunContract, error) {
	paths, err := filepath.Glob(filepath.Join(root, "run.*.json"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("run inventory %s: paths=%v err=%v", filepath.Base(root), paths, err)
	}
	out := make(map[string]repositoryRunContract, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		spec, err := DecodeRunSpec(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if spec.Repetitions != wantRepetitions {
			return nil, fmt.Errorf("%s repetitions=%d want=%d", filepath.Base(path), spec.Repetitions, wantRepetitions)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		out[filepath.Base(path)] = repositoryRunContract{Spec: spec, Raw: raw}
	}
	return out, nil
}

func runSpecs(contracts map[string]repositoryRunContract) map[string]RunSpec {
	specs := make(map[string]RunSpec, len(contracts))
	for name, contract := range contracts {
		specs[name] = contract.Spec
	}
	return specs
}

func TestRepositorySamplingPairContractRejectsMutations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, contract *evaluatorBehaviorContract, root string)
		wantErr string
	}{
		{
			name: "missing pair",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.Pairs = nil
			},
			wantErr: `contract rows=0 want=1`,
		},
		{
			name: "duplicate pair",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.Pairs = append(contract.PairIdentity.Pairs, contract.PairIdentity.Pairs[0])
			},
			wantErr: `contract rows=2 want=1`,
		},
		{
			name: "missing run file set",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.RunFileSets = nil
			},
			wantErr: `run file set "mcp" rows=0`,
		},
		{
			name: "duplicate run file set",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.RunFileSets = append(
					contract.PairIdentity.RunFileSets,
					contract.PairIdentity.RunFileSets[0],
				)
			},
			wantErr: `run file set "mcp" rows=2`,
		},
		{
			name: "missing scenario",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, "generic-pair-holdout", "scenario.v1.json")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: `holdout scenario: scenario inventory`,
		},
		{
			name: "same scenario id",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairScenario(t, filepath.Join(root, "generic-pair-holdout"), "generic.primary", "jira/evidence")
			},
			wantErr: `holdout reused the primary scenario id`,
		},
		{
			name: "run scenario binding drift",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairRunWithScenario(t, filepath.Join(root, "generic-pair-holdout"),
					"run.mcp.codex.json", 1, "gpt-test-1", "scenario.v2.json")
			},
			wantErr: `sampling pair run scenario files drifted`,
		},
		{
			name: "wrong run file set",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairRun(t, filepath.Join(root, "generic-pair"), "run.mcp.extra.json", 3, "gpt-test-1")
			},
			wantErr: `run file set "mcp" drifted`,
		},
		{
			name: "wrong primary repetitions",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairRun(t, filepath.Join(root, "generic-pair"), "run.mcp.codex.json", 2, "gpt-test-1")
			},
			wantErr: `repetitions=2 want=3`,
		},
		{
			name: "wrong holdout repetitions",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairRun(t, filepath.Join(root, "generic-pair-holdout"), "run.mcp.codex.json", 2, "gpt-test-1")
			},
			wantErr: `repetitions=2 want=1`,
		},
		{
			name: "undeclared scenario exception",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairScenario(t, filepath.Join(root, "generic-pair-holdout"), "generic.holdout", "jira/other-evidence")
			},
			wantErr: `scenario identity exceptions drifted: got=[task_class] want=[]`,
		},
		{
			name: "stale scenario exception",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.Pairs[0].ScenarioExceptions = []string{"task_class"}
			},
			wantErr: `scenario identity exceptions drifted: got=[] want=[task_class]`,
		},
		{
			name: "undeclared run exception",
			mutate: func(t *testing.T, _ *evaluatorBehaviorContract, root string) {
				writeSamplingPairRun(t, filepath.Join(root, "generic-pair-holdout"), "run.mcp.codex.json", 1, "gpt-test-2")
			},
			wantErr: `run.mcp.codex.json run identity exceptions drifted: got=[model] want=[]`,
		},
		{
			name: "declared run exception is not present",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.Pairs[0].RunExceptions = map[string][]string{
					"run.mcp.codex.json": {"model"},
				}
			},
			wantErr: `run.mcp.codex.json run identity exceptions drifted: got=[] want=[model]`,
		},
		{
			name: "stale run exception file",
			mutate: func(_ *testing.T, contract *evaluatorBehaviorContract, _ string) {
				contract.PairIdentity.Pairs[0].RunExceptions = map[string][]string{
					"run.mcp.missing.json": {"model"},
				}
			},
			wantErr: `stale run exception for run.mcp.missing.json in primary`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			contract := syntheticSamplingPairContract()
			writeSyntheticSamplingPair(t, root, contract.PairIdentity.RunFileSets[0])
			test.mutate(t, &contract, root)
			_, err := resolveRepositorySamplingPairContract(contract, root, "generic-pair")
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error=%v want fragment %q", err, test.wantErr)
			}
		})
	}
}

func TestRepositorySamplingPairContractReadsExactRunBoundScenario(t *testing.T) {
	root := t.TempDir()
	contract := syntheticSamplingPairContract()
	writeSyntheticSamplingPair(t, root, contract.PairIdentity.RunFileSets[0])
	for _, item := range []struct {
		root, id string
	}{
		{root: filepath.Join(root, "generic-pair"), id: "ignored.latest.primary"},
		{root: filepath.Join(root, "generic-pair-holdout"), id: "ignored.latest.holdout"},
	} {
		scenario := validScenario()
		scenario.ID = item.id
		scenario.TaskClass = "jira/other-evidence"
		writeSamplingPairJSON(t, filepath.Join(item.root, "scenario.v2.json"), scenario)
	}

	pair, err := resolveRepositorySamplingPairContract(contract, root, "generic-pair")
	if err != nil {
		t.Fatal(err)
	}
	if pair.Primary.Scenario.ID != "generic.primary" || pair.Holdout.Scenario.ID != "generic.holdout" {
		t.Fatalf("pair selected an unbound retained scenario: primary=%q holdout=%q",
			pair.Primary.Scenario.ID, pair.Holdout.Scenario.ID)
	}
}

func TestRepositorySamplingPairContractPreservesLegacyAsymmetricRunSets(t *testing.T) {
	root := t.TempDir()
	contract := syntheticSamplingPairContract()
	contract.PairIdentity.RunFileSets[0] = evaluatorRunFileSetContract{
		Name: "legacy",
		Primary: []string{
			"run.cli.claude.json",
			"run.mcp.codex.json",
		},
		Holdout: []string{
			"run.mcp.claude.json",
			"run.mcp.codex.json",
		},
	}
	contract.PairIdentity.Pairs[0].RunFileSet = "legacy"
	writeSyntheticSamplingPair(t, root, contract.PairIdentity.RunFileSets[0])

	pair, err := resolveRepositorySamplingPairContract(contract, root, "generic-pair")
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedMapKeys(pair.Primary.Runs); !slices.Equal(got, []string{"run.cli.claude.json", "run.mcp.codex.json"}) {
		t.Fatalf("primary legacy files=%v", got)
	}
	if got := sortedMapKeys(pair.Holdout.Runs); !slices.Equal(got, []string{"run.mcp.claude.json", "run.mcp.codex.json"}) {
		t.Fatalf("holdout legacy files=%v", got)
	}
}

func syntheticSamplingPairContract() evaluatorBehaviorContract {
	var contract evaluatorBehaviorContract
	contract.SchemaVersion = 1
	contract.PairIdentity.ScenarioFields = []string{
		"category", "task_class", "data_class", "required_capabilities", "required_checks",
		"required_semantic_checks", "required_metrics", "budgets",
	}
	contract.PairIdentity.RunFields = []string{
		"provider", "model", "reasoning", "variant", "category", "surface", "tool_transport",
	}
	contract.PairIdentity.RunFileSets = []evaluatorRunFileSetContract{{
		Name: "mcp", Primary: []string{"run.mcp.codex.json"}, Holdout: []string{"run.mcp.codex.json"},
	}}
	contract.PairIdentity.Pairs = []evaluatorSamplingPairContract{{Primary: "generic-pair", RunFileSet: "mcp"}}
	return contract
}

func writeSyntheticSamplingPair(t *testing.T, root string, fileSet evaluatorRunFileSetContract) {
	t.Helper()
	primaryRoot := filepath.Join(root, "generic-pair")
	holdoutRoot := primaryRoot + "-holdout"
	writeSamplingPairScenario(t, primaryRoot, "generic.primary", "jira/evidence")
	writeSamplingPairScenario(t, holdoutRoot, "generic.holdout", "jira/evidence")
	for _, name := range fileSet.Primary {
		writeSamplingPairRun(t, primaryRoot, name, 3, "gpt-test-1")
	}
	for _, name := range fileSet.Holdout {
		writeSamplingPairRun(t, holdoutRoot, name, 1, "gpt-test-1")
	}
}

func writeSamplingPairScenario(t *testing.T, root, id, taskClass string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	scenario := validScenario()
	scenario.ID = id
	scenario.TaskClass = taskClass
	writeSamplingPairJSON(t, filepath.Join(root, "scenario.v1.json"), scenario)
}

func writeSamplingPairRun(t *testing.T, root, name string, repetitions int, model string) {
	writeSamplingPairRunWithScenario(t, root, name, repetitions, model, "scenario.v1.json")
}

func writeSamplingPairRunWithScenario(
	t *testing.T,
	root, name string,
	repetitions int,
	model, scenarioFile string,
) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := validRunSpec()
	spec.Repetitions = repetitions
	spec.Model = model
	spec.ScenarioFile = scenarioFile
	writeSamplingPairJSON(t, filepath.Join(root, name), spec)
}

func writeSamplingPairJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
