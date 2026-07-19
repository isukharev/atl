package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/skillmeta"
	"github.com/isukharev/atl/internal/skillrouting"
)

func TestRepositorySkillRoutingContract(t *testing.T) {
	root := repositoryRoot(t)
	summary, err := validateRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Skills != 11 || summary.Cases != 26 || summary.RoutedCases != 15 || summary.NoActivationCases != 11 {
		t.Fatalf("summary=%+v", summary)
	}

	var output bytes.Buffer
	if err := run(root, &output); err != nil {
		t.Fatal(err)
	}
	var decoded skillrouting.Summary
	decoder := json.NewDecoder(&output)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != summary {
		t.Fatalf("output=%+v want=%+v", decoded, summary)
	}
	for _, privateDetail := range []string{"prompt", "task_class", "expected_skill", "forbidden_skills"} {
		if strings.Contains(output.String(), privateDetail) {
			t.Fatalf("aggregate output retained case detail %q: %s", privateDetail, output.String())
		}
	}
}

func TestSkillRoutingRejectsRegistryInventoryDrift(t *testing.T) {
	catalog, registry, corpus := loadContracts(t)
	t.Run("missing", func(t *testing.T) {
		drifted := registry
		drifted.Skills = append([]skillrouting.Skill(nil), registry.Skills[1:]...)
		if _, err := validateContracts(catalog, drifted, corpus); err == nil || !strings.Contains(err.Error(), "inventories differ") {
			t.Fatalf("missing registry skill passed: %v", err)
		}
	})
	t.Run("extra", func(t *testing.T) {
		drifted := registry
		drifted.Skills = append([]skillrouting.Skill(nil), registry.Skills...)
		drifted.Skills = append(drifted.Skills, skillrouting.Skill{
			Name: "unexpected", Implicit: true,
			PositiveTaskClasses: []string{"synthetic/positive"},
			NegativeTaskClasses: []string{"synthetic/negative"},
		})
		if _, err := validateContracts(catalog, drifted, corpus); err == nil || !strings.Contains(err.Error(), "inventories differ") {
			t.Fatalf("extra registry skill passed: %v", err)
		}
	})
}

func TestSkillRoutingRejectsImplicitPolicyDrift(t *testing.T) {
	catalog, registry, corpus := loadContracts(t)
	drifted := registry
	drifted.Skills = append([]skillrouting.Skill(nil), registry.Skills...)
	drifted.Skills[0].Implicit = !drifted.Skills[0].Implicit
	if _, err := validateContracts(catalog, drifted, corpus); err == nil || !strings.Contains(err.Error(), "implicit policy differs") {
		t.Fatalf("implicit policy drift passed: %v", err)
	}
}

func loadContracts(t *testing.T) (skillmeta.Catalog, skillrouting.Registry, skillrouting.Corpus) {
	t.Helper()
	root := repositoryRoot(t)
	catalog, err := skillmeta.LoadSource(filepath.Join(root, "skills-src"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := skillrouting.LoadRegistryFile(filepath.Join(root, "skills-src", skillmeta.RoutingFileName))
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := skillrouting.LoadCorpusFile(filepath.Join(root, "benchmarks", "agent-eval", "skill-routing.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return catalog, registry, corpus
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
