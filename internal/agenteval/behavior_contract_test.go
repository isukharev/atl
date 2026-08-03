package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

type evaluatorBehaviorContract struct {
	SchemaVersion int `json:"schema_version"`
	Environment   struct {
		Variables []struct {
			Name        string   `json:"name"`
			Producers   []string `json:"producers"`
			Consumers   []string `json:"consumers"`
			ValueType   string   `json:"value_type"`
			Modes       []string `json:"modes"`
			Sensitivity string   `json:"sensitivity"`
		} `json:"variables"`
		TestOnlyVariables []string `json:"test_only_variables"`
	} `json:"environment"`
	Schemas []struct {
		Name       string `json:"name"`
		Readable   []int  `json:"readable"`
		Emitted    []int  `json:"emitted"`
		Executable []int  `json:"executable"`
	} `json:"schemas"`
	CorpusVersions []struct {
		Artifact string `json:"artifact"`
		Version  int    `json:"version"`
		Count    int    `json:"count"`
	} `json:"corpus_versions"`
	PairIdentity struct {
		ScenarioFields []string `json:"scenario_fields"`
		RunFields      []string `json:"run_fields"`
		RunFileSets    []struct {
			Name    string   `json:"name"`
			Primary []string `json:"primary"`
			Holdout []string `json:"holdout"`
		} `json:"run_file_sets"`
		Pairs []struct {
			Primary            string              `json:"primary"`
			ScenarioExceptions []string            `json:"scenario_exceptions"`
			RunFileSet         string              `json:"run_file_set"`
			RunExceptions      map[string][]string `json:"run_exceptions"`
		} `json:"pairs"`
	} `json:"pair_identity"`
	Artifacts struct {
		PublicRootFiles      []string                 `json:"public_root_files"`
		PublicTrackedClasses []evaluatorArtifactClass `json:"public_tracked_classes"`
		PrivateOnlyClasses   []evaluatorArtifactClass `json:"private_only_classes"`
	} `json:"artifacts"`
}

type evaluatorArtifactClass struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

func loadEvaluatorBehaviorContract(t *testing.T) evaluatorBehaviorContract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "behavior-contract.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract evaluatorBehaviorContract
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 1 {
		t.Fatalf("behavior contract schema=%d", contract.SchemaVersion)
	}
	return contract
}

func TestEvaluatorEnvironmentABIContract(t *testing.T) {
	contract := loadEvaluatorBehaviorContract(t)
	repository := filepath.Join("..", "..")
	variableRE := regexp.MustCompile(`ATL_EVAL_[A-Z0-9_]+`)
	production := map[string]bool{}
	testOnlyOccurrences := map[string]bool{}
	for _, root := range []string{"internal/agenteval", "scripts/agent-eval"} {
		err := filepath.WalkDir(filepath.Join(repository, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, name := range variableRE.FindAllString(string(data), -1) {
				if strings.HasSuffix(path, "_test.go") {
					testOnlyOccurrences[name] = true
				} else {
					production[name] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	contractNames := make([]string, 0, len(contract.Environment.Variables))
	for _, variable := range contract.Environment.Variables {
		contractNames = append(contractNames, variable.Name)
		if variable.ValueType == "" || len(variable.Modes) == 0 || variable.Sensitivity == "" {
			t.Errorf("%s has an incomplete semantic contract", variable.Name)
		}
		if variable.Name == "ATL_EVAL_HTTP_GUARD_FILE" {
			if len(variable.Producers) != 0 || len(variable.Consumers) != 0 || production[variable.Name] {
				t.Errorf("forbidden ambient variable became an active ABI member: %+v", variable)
			}
			continue
		}
		if !production[variable.Name] {
			t.Errorf("contracted production variable %s is absent", variable.Name)
		}
		if len(variable.Producers) == 0 || len(variable.Consumers) == 0 {
			t.Errorf("%s must name producer and consumer owners", variable.Name)
		}
		for _, owner := range append(slices.Clone(variable.Producers), variable.Consumers...) {
			data, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(owner)))
			if err != nil {
				t.Errorf("%s owner %s: %v", variable.Name, owner, err)
				continue
			}
			if !bytes.Contains(data, []byte(variable.Name)) {
				t.Errorf("%s no longer occurs in declared owner %s", variable.Name, owner)
			}
		}
	}
	sort.Strings(contractNames)
	productionNames := make([]string, 0, len(production)+1)
	for name := range production {
		productionNames = append(productionNames, name)
	}
	productionNames = append(productionNames, "ATL_EVAL_HTTP_GUARD_FILE")
	sort.Strings(productionNames)
	if !slices.Equal(contractNames, productionNames) {
		t.Fatalf("environment ABI drifted: contract=%v production=%v", contractNames, productionNames)
	}

	for _, name := range contract.Environment.TestOnlyVariables {
		if production[name] {
			t.Errorf("test-only evaluator variable entered production: %s", name)
		}
		if !testOnlyOccurrences[name] {
			t.Errorf("test-only exclusion is stale: %s", name)
		}
	}
	onlyInTests := make([]string, 0)
	for name := range testOnlyOccurrences {
		if !production[name] {
			onlyInTests = append(onlyInTests, name)
		}
	}
	wantOnlyInTests := append(slices.Clone(contract.Environment.TestOnlyVariables), "ATL_EVAL_HTTP_GUARD_FILE")
	sort.Strings(onlyInTests)
	sort.Strings(wantOnlyInTests)
	if !slices.Equal(onlyInTests, wantOnlyInTests) {
		t.Fatalf("test-only evaluator environment inventory drifted: got=%v want=%v", onlyInTests, wantOnlyInTests)
	}
}

func TestEvaluatorSchemaCompatibilityAndCorpusDistribution(t *testing.T) {
	contract := loadEvaluatorBehaviorContract(t)
	wantSchemas := map[string]struct {
		readable, emitted, executable []int
	}{
		"run_spec":    {[]int{5, 6, 7}, []int{7}, []int{5, 6, 7}},
		"result":      {[]int{3, 4, 5, 6, 7, 8}, []int{8}, []int{3, 4, 5, 6, 7, 8}},
		"observation": {[]int{5}, []int{5}, []int{5}},
		"scenario":    {[]int{1}, []int{1}, []int{1}},
	}
	for _, schema := range contract.Schemas {
		want, ok := wantSchemas[schema.Name]
		if !ok || !slices.Equal(schema.Readable, want.readable) || !slices.Equal(schema.Emitted, want.emitted) || !slices.Equal(schema.Executable, want.executable) {
			t.Fatalf("schema compatibility drifted: %+v", schema)
		}
		delete(wantSchemas, schema.Name)
	}
	if len(wantSchemas) != 0 {
		t.Fatalf("missing schema contracts: %v", wantSchemas)
	}

	for _, version := range []int{LegacyPromptChannelRunSpecVersion, LegacyRunSpecSchemaVersion, RunSpecSchemaVersion} {
		spec := validRunSpec()
		spec.SchemaVersion = version
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeRunSpec(bytes.NewReader(data)); err != nil {
			t.Fatalf("run spec v%d is not readable/executable: %v", version, err)
		}
	}
	for _, version := range []int{LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion, LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion} {
		data := minimalResultContractJSON(t, version)
		decoded, err := DecodeResult(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("result v%d is not readable: %v", version, err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("result v%d is not executable: %v", version, err)
		}
	}
	observationData, err := json.Marshal(validObservation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeObservation(bytes.NewReader(observationData)); err != nil {
		t.Fatalf("observation v%d is not readable/executable: %v", ObservationSchemaVersion, err)
	}

	observed := map[string]map[int]int{"scenario": {}, "run_spec": {}}
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		base := filepath.Base(path)
		artifact := ""
		switch {
		case strings.HasPrefix(base, "scenario.v") && strings.HasSuffix(base, ".json"):
			artifact = "scenario"
		case strings.HasPrefix(base, "run.") && strings.HasSuffix(base, ".json"):
			artifact = "run_spec"
		default:
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var header struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return err
		}
		observed[artifact][header.SchemaVersion]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDistribution := map[string]map[int]int{"scenario": {}, "run_spec": {}}
	for _, row := range contract.CorpusVersions {
		wantDistribution[row.Artifact][row.Version] = row.Count
	}
	if !reflect.DeepEqual(observed, wantDistribution) {
		t.Fatalf("reviewed corpus distribution drifted: got=%v want=%v", observed, wantDistribution)
	}
}

func TestEvaluatorSamplingPairIdentityContract(t *testing.T) {
	contract := loadEvaluatorBehaviorContract(t)
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	runFileSets := make(map[string]struct{ primary, holdout []string }, len(contract.PairIdentity.RunFileSets))
	for _, set := range contract.PairIdentity.RunFileSets {
		if set.Name == "" || len(set.Primary) == 0 || len(set.Holdout) == 0 {
			t.Fatalf("invalid run file set: %+v", set)
		}
		if _, duplicate := runFileSets[set.Name]; duplicate {
			t.Fatalf("duplicate run file set %q", set.Name)
		}
		primary, holdout := slices.Clone(set.Primary), slices.Clone(set.Holdout)
		sort.Strings(primary)
		sort.Strings(holdout)
		runFileSets[set.Name] = struct{ primary, holdout []string }{primary, holdout}
	}
	actualPairs, err := filepath.Glob(filepath.Join(root, "*-holdout"))
	if err != nil {
		t.Fatal(err)
	}
	actualNames := make([]string, 0, len(actualPairs))
	for _, path := range actualPairs {
		actualNames = append(actualNames, strings.TrimSuffix(filepath.Base(path), "-holdout"))
	}
	sort.Strings(actualNames)
	contractNames := make([]string, 0, len(contract.PairIdentity.Pairs))
	for _, pair := range contract.PairIdentity.Pairs {
		contractNames = append(contractNames, pair.Primary)
	}
	sort.Strings(contractNames)
	if !slices.Equal(actualNames, contractNames) || len(contractNames) != 41 {
		t.Fatalf("sampling pair inventory drifted: actual=%v contract=%v", actualNames, contractNames)
	}

	for _, pair := range contract.PairIdentity.Pairs {
		t.Run(pair.Primary, func(t *testing.T) {
			expectedFiles, ok := runFileSets[pair.RunFileSet]
			if !ok {
				t.Fatalf("unknown run file set %q", pair.RunFileSet)
			}
			primaryRoot := filepath.Join(root, pair.Primary)
			holdoutRoot := primaryRoot + "-holdout"
			primaryScenario, primaryRaw := loadLatestScenarioContract(t, primaryRoot)
			holdoutScenario, holdoutRaw := loadLatestScenarioContract(t, holdoutRoot)
			if primaryScenario.ID == holdoutScenario.ID {
				t.Fatal("holdout reused the primary scenario id")
			}
			gotScenarioExceptions := differingJSONFields(primaryRaw, holdoutRaw, contract.PairIdentity.ScenarioFields)
			wantScenarioExceptions := slices.Clone(pair.ScenarioExceptions)
			sort.Strings(wantScenarioExceptions)
			if !slices.Equal(gotScenarioExceptions, wantScenarioExceptions) {
				t.Fatalf("scenario identity exceptions drifted: got=%v want=%v", gotScenarioExceptions, wantScenarioExceptions)
			}

			primaryRuns := loadRunContracts(t, primaryRoot, 3)
			holdoutRuns := loadRunContracts(t, holdoutRoot, 1)
			primaryFiles, holdoutFiles := sortedMapKeys(primaryRuns), sortedMapKeys(holdoutRuns)
			if !slices.Equal(primaryFiles, expectedFiles.primary) || !slices.Equal(holdoutFiles, expectedFiles.holdout) {
				t.Fatalf("run file set %q drifted: primary=%v want=%v holdout=%v want=%v",
					pair.RunFileSet, primaryFiles, expectedFiles.primary, holdoutFiles, expectedFiles.holdout)
			}
			for name, primary := range primaryRuns {
				holdout, ok := holdoutRuns[name]
				if !ok {
					continue
				}
				got := differingJSONFields(primary, holdout, contract.PairIdentity.RunFields)
				want := slices.Clone(pair.RunExceptions[name])
				sort.Strings(want)
				if !slices.Equal(got, want) {
					t.Errorf("%s run identity exceptions drifted: got=%v want=%v", name, got, want)
				}
			}
			for name := range pair.RunExceptions {
				if _, ok := primaryRuns[name]; !ok {
					t.Errorf("stale run exception for %s", name)
				}
				if _, ok := holdoutRuns[name]; !ok {
					t.Errorf("stale run exception for %s", name)
				}
			}
		})
	}
}

func minimalResultContractJSON(t *testing.T, version int) []byte {
	t.Helper()
	// This fixture intentionally contains only fields required by every readable
	// result generation. It proves legacy decoding does not accidentally start
	// requiring fields introduced by newer schema generations.
	value := map[string]any{
		"schema_version": version,
		"scenario_id":    "jira.epic-evidence",
		"task_class":     "jira/evidence",
		"data_class":     "synthetic",
		"variant":        "baseline",
		"runtime":        map[string]any{"provider": "deterministic", "atl_version": "0.4.0"},
		"status":         "pass",
		"metrics": map[string]any{
			"atl_invocations": 2, "backend_requests": 7, "output_bytes": 4096,
		},
		"coverage": map[string]any{
			"atl_invocations": true, "backend_requests": true, "output_bytes": true,
		},
		"http_methods": map[string]any{"GET": 7},
		"checks":       map[string]any{"answer_correct": true, "sources_complete": true},
		"violations":   []any{},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEvaluatorArtifactClassBoundary(t *testing.T) {
	contract := loadEvaluatorBehaviorContract(t)
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var rootFiles []string
	for _, entry := range rootEntries {
		if !entry.IsDir() {
			rootFiles = append(rootFiles, entry.Name())
		}
	}
	sort.Strings(rootFiles)
	wantRootFiles := slices.Clone(contract.Artifacts.PublicRootFiles)
	sort.Strings(wantRootFiles)
	if !slices.Equal(rootFiles, wantRootFiles) {
		t.Fatalf("public evaluator root artifact classes drifted: got=%v want=%v", rootFiles, wantRootFiles)
	}

	publicPatterns := compileArtifactPatterns(t, contract.Artifacts.PublicTrackedClasses)
	privatePatterns := compileArtifactPatterns(t, contract.Artifacts.PrivateOnlyClasses)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Dir(path) == root {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return fmt.Errorf("unexpected evaluator artifact path %s", rel)
		}
		cellRelative := strings.Join(parts[1:], "/")
		matched := 0
		for _, pattern := range publicPatterns {
			if pattern.MatchString(cellRelative) {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("public evaluator artifact %s matched %d tracked classes", rel, matched)
		}
		base := filepath.Base(path)
		for _, pattern := range privatePatterns {
			if pattern.MatchString(base) {
				return fmt.Errorf("private-only evaluator artifact class entered public corpus: %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func loadLatestScenarioContract(t *testing.T, root string) (Scenario, map[string]json.RawMessage) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "scenario.v*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("scenario inventory %s: paths=%v err=%v", filepath.Base(root), paths, err)
	}
	sort.Strings(paths)
	data, err := os.ReadFile(paths[len(paths)-1])
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := DecodeScenario(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return scenario, raw
}

func loadRunContracts(t *testing.T, root string, wantRepetitions int) map[string]map[string]json.RawMessage {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "run.*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("run inventory %s: paths=%v err=%v", filepath.Base(root), paths, err)
	}
	out := make(map[string]map[string]json.RawMessage, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := DecodeRunSpec(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if spec.Repetitions != wantRepetitions {
			t.Errorf("%s repetitions=%d want=%d", filepath.Base(path), spec.Repetitions, wantRepetitions)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(path)] = raw
	}
	return out
}

func differingJSONFields(a, b map[string]json.RawMessage, fields []string) []string {
	var different []string
	for _, field := range fields {
		var av, bv any
		if len(a[field]) != 0 {
			_ = json.Unmarshal(a[field], &av)
		}
		if len(b[field]) != 0 {
			_ = json.Unmarshal(b[field], &bv)
		}
		if !reflect.DeepEqual(av, bv) {
			different = append(different, field)
		}
	}
	sort.Strings(different)
	return different
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compileArtifactPatterns(t *testing.T, rows []evaluatorArtifactClass) []*regexp.Regexp {
	t.Helper()
	patterns := make([]*regexp.Regexp, 0, len(rows))
	for _, row := range rows {
		pattern, err := regexp.Compile(row.Pattern)
		if err != nil {
			t.Fatalf("artifact class %s: %v", row.Name, err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns
}
