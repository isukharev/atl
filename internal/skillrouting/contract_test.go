package skillrouting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRepositoryRoutingContract(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	summary, err := Validate(registry, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if summary != (Summary{
		SchemaVersion: 1, Skills: 11, ImplicitSkills: 9, ExplicitOnlySkills: 2,
		Cases: 26, RoutedCases: 15, NoActivationCases: 11,
	}) {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestRoutingUsesReviewedTaskClassNotPromptWords(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	corpus.Cases[0].Prompt = "Fix ordinary source code without using any Atlassian service."
	if _, err := Validate(registry, corpus); err != nil {
		t.Fatalf("prompt prose incorrectly affected deterministic routing: %v", err)
	}
}

func TestRoutingRejectsAmbiguousRoute(t *testing.T) {
	registry, corpus := syntheticRoutingContract("synthetic/shared", "alpha")
	registry.Skills[1].PositiveTaskClasses = []string{"synthetic/shared"}
	registry.Skills[1].NegativeTaskClasses = []string{"synthetic/not-beta"}
	if _, _, err := selectSkill(registry.Skills, corpus.Cases[0]); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous route passed: %v", err)
	}
}

func TestRoutingRejectsConflictingBoundaries(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	for index := range registry.Skills {
		if registry.Skills[index].Name == "jira" {
			registry.Skills[index].NegativeTaskClasses = append(registry.Skills[index].NegativeTaskClasses, "jira/direct-read")
			sort.Strings(registry.Skills[index].NegativeTaskClasses)
		}
	}
	if _, err := Validate(registry, corpus); err == nil || !strings.Contains(err.Error(), "conflicting boundaries") {
		t.Fatalf("conflict passed: %v", err)
	}
}

func TestRoutingRejectsExpectedAndForbiddenDrift(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	t.Run("expected", func(t *testing.T) {
		drifted := cloneCorpus(t, corpus)
		wrong := "jira"
		drifted.Cases[0].ExpectedSkill = &wrong
		if _, err := Validate(registry, drifted); err == nil || !strings.Contains(err.Error(), "selected") {
			t.Fatalf("wrong expectation passed: %v", err)
		}
	})
	t.Run("forbidden boundary", func(t *testing.T) {
		syntheticRegistry, syntheticCorpus := syntheticRoutingContract("synthetic/shared", "alpha")
		syntheticRegistry.Skills[1].NegativeTaskClasses = []string{"synthetic/not-beta"}
		if _, err := Validate(syntheticRegistry, syntheticCorpus); err == nil || !strings.Contains(err.Error(), "does not declare an exclusion") {
			t.Fatalf("missing forbidden boundary passed: %v", err)
		}
	})
}

func syntheticRoutingContract(taskClass, expected string) (Registry, Corpus) {
	return Registry{SchemaVersion: 1, Skills: []Skill{
			{Name: "alpha", Implicit: true, PositiveTaskClasses: []string{taskClass}, NegativeTaskClasses: []string{"synthetic/not-alpha"}, implicitPresent: true},
			{Name: "beta", Implicit: true, PositiveTaskClasses: []string{"synthetic/beta"}, NegativeTaskClasses: []string{taskClass}, implicitPresent: true},
		}}, Corpus{SchemaVersion: 1, Cases: []RoutingCase{
			{ID: "shared-route", Prompt: "A bounded synthetic routing prompt.", TaskClass: taskClass, Invocation: "implicit", ExpectedSkill: &expected, expectedSkillPresent: true, ForbiddenSkills: []string{"beta"}},
			{ID: "beta-route", Prompt: "A second bounded synthetic routing prompt.", TaskClass: "synthetic/beta", Invocation: "implicit", ExpectedSkill: stringPointer("beta"), expectedSkillPresent: true},
			{ID: "negative-alpha", Prompt: "A negative synthetic routing prompt.", TaskClass: "synthetic/not-alpha", Invocation: "implicit", ExpectedSkill: nil, expectedSkillPresent: true, ForbiddenSkills: []string{"alpha"}},
		}}
}

func stringPointer(value string) *string { return &value }

func TestRoutingRequiresEveryBoundaryAndExplicitRefusal(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	t.Run("owned route", func(t *testing.T) {
		drifted := cloneRegistry(t, registry)
		for index := range drifted.Skills {
			if drifted.Skills[index].Name == "atl" {
				drifted.Skills[index].PositiveTaskClasses = append(drifted.Skills[index].PositiveTaskClasses, "atlassian/unused")
				sort.Strings(drifted.Skills[index].PositiveTaskClasses)
			}
		}
		if _, err := Validate(drifted, corpus); err == nil || !strings.Contains(err.Error(), "unexercised owned route") {
			t.Fatalf("unexercised owned route passed: %v", err)
		}
	})
	t.Run("exclusion", func(t *testing.T) {
		drifted := cloneRegistry(t, registry)
		for index := range drifted.Skills {
			if drifted.Skills[index].Name == "atl" {
				drifted.Skills[index].NegativeTaskClasses = append(drifted.Skills[index].NegativeTaskClasses, "codebase/unused")
				sort.Strings(drifted.Skills[index].NegativeTaskClasses)
			}
		}
		if _, err := Validate(drifted, corpus); err == nil || !strings.Contains(err.Error(), "unexercised exclusion") {
			t.Fatalf("unexercised exclusion passed: %v", err)
		}
	})
	t.Run("explicit-only implicit refusal", func(t *testing.T) {
		drifted := cloneCorpus(t, corpus)
		cases := drifted.Cases[:0]
		for _, testCase := range drifted.Cases {
			if testCase.ID != "onboarding-implicit-refusal" {
				cases = append(cases, testCase)
			}
		}
		drifted.Cases = cases
		if _, err := Validate(registry, drifted); err == nil || !strings.Contains(err.Error(), "has no implicit refusal case") {
			t.Fatalf("explicit-only skill without refusal passed: %v", err)
		}
	})
}

func TestStrictRoutingDocumentsRejectMalformedInput(t *testing.T) {
	registryData, err := os.ReadFile(filepath.Join("..", "..", "skills-src", "routing.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(registryData), `"skills":`, `"unknown": true, "skills":`, 1)
	if _, err := LoadRegistry(strings.NewReader(withUnknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown registry field passed: %v", err)
	}
	missingImplicit := strings.Replace(string(registryData), "      \"implicit\": true,\n", "", 1)
	if _, err := LoadRegistry(strings.NewReader(missingImplicit)); err == nil || !strings.Contains(err.Error(), "implicit is required") {
		t.Fatalf("missing implicit passed: %v", err)
	}
	duplicateSchema := strings.Replace(string(registryData), `"schema_version": 1`, `"schema_version": 1, "schema_version": 1`, 1)
	if _, err := LoadRegistry(strings.NewReader(duplicateSchema)); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate registry key passed: %v", err)
	}

	missingExpected := `{
  "schema_version": 1,
  "cases": [{
    "id": "missing-expected",
    "prompt": "A bounded synthetic prompt.",
    "task_class": "synthetic/example",
    "invocation": "implicit"
  }]
}`
	if _, err := LoadCorpus(strings.NewReader(missingExpected)); err == nil || !strings.Contains(err.Error(), "expected_skill is required") {
		t.Fatalf("missing expected_skill passed: %v", err)
	}

	_, repositoryCorpus := loadRepositoryContract(t)
	corpusData, err := json.Marshal(repositoryCorpus)
	if err != nil {
		t.Fatal(err)
	}
	wrongInvocation := strings.Replace(string(corpusData), `"invoked_skill":"onboarding"`, `"invoked_skill":"setup"`, 1)
	if _, err := LoadCorpus(strings.NewReader(wrongInvocation)); err == nil || !strings.Contains(err.Error(), "prompt does not invoke") {
		t.Fatalf("wrong explicit invocation passed: %v", err)
	}
	duplicateExpected := strings.Replace(string(corpusData), `"expected_skill":"atl"`, `"expected_skill":"atl","expected_skill":"jira"`, 1)
	if _, err := LoadCorpus(strings.NewReader(duplicateExpected)); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("duplicate nested corpus key passed: %v", err)
	}
}

func TestFileLoadersRejectSymlinksAndNonRegularFiles(t *testing.T) {
	for name, load := range map[string]func(string) error{
		"registry": func(path string) error {
			_, err := LoadRegistryFile(path)
			return err
		},
		"corpus": func(path string) error { _, err := LoadCorpusFile(path); return err },
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("directory", func(t *testing.T) {
				if err := load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
					t.Fatalf("directory routing document passed: %v", err)
				}
			})
			t.Run("symlink", func(t *testing.T) {
				target := filepath.Join(t.TempDir(), "routing.json")
				if err := os.WriteFile(target, []byte(`{"schema_version":1}`), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(t.TempDir(), "routing.json")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
				if err := load(link); err == nil || !strings.Contains(err.Error(), "not a regular file") {
					t.Fatalf("symlinked routing document passed: %v", err)
				}
			})
		})
	}
}

func TestRoutingRejectsUnknownAndUnclassifiedReferences(t *testing.T) {
	registry, corpus := loadRepositoryContract(t)
	t.Run("unknown expected", func(t *testing.T) {
		drifted := cloneCorpus(t, corpus)
		unknown := "not-shipped"
		drifted.Cases[0].ExpectedSkill = &unknown
		if _, err := Validate(registry, drifted); err == nil || !strings.Contains(err.Error(), "unknown expected skill") {
			t.Fatalf("unknown expected skill passed: %v", err)
		}
	})
	t.Run("unclassified task", func(t *testing.T) {
		drifted := cloneCorpus(t, corpus)
		drifted.Cases[0].TaskClass = "unclassified/task"
		drifted.Cases[0].ExpectedSkill = nil
		if _, err := Validate(registry, drifted); err == nil || !strings.Contains(err.Error(), "unclassified task_class") {
			t.Fatalf("unclassified task passed: %v", err)
		}
	})
}

func loadRepositoryContract(t *testing.T) (Registry, Corpus) {
	t.Helper()
	registry, err := LoadRegistryFile(filepath.Join("..", "..", "skills-src", "routing.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := LoadCorpusFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "skill-routing.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return registry, corpus
}

func cloneRegistry(t *testing.T, registry Registry) Registry {
	t.Helper()
	var out Registry
	cloneJSON(t, registry, &out)
	return out
}

func cloneCorpus(t *testing.T, corpus Corpus) Corpus {
	t.Helper()
	var out Corpus
	cloneJSON(t, corpus, &out)
	return out
}

func cloneJSON(t *testing.T, source, target any) {
	t.Helper()
	data, err := jsonMarshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeStrict(strings.NewReader(string(data)), target); err != nil {
		t.Fatal(err)
	}
}

// jsonMarshal is a seam so clone helpers exercise RoutingCase.UnmarshalJSON
// and retain the required-field presence bit.
var jsonMarshal = func(value any) ([]byte, error) {
	return json.Marshal(value)
}
