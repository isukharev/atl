package capability

import (
	"reflect"
	"testing"
)

func TestTaskClassesIsExactSortedCatalogProjection(t *testing.T) {
	want := []string{
		"confluence/attachment-discovery",
		"confluence/comments",
		"confluence/edit",
		"confluence/evidence",
		"confluence/mirror",
		"confluence/space-hierarchy",
		"confluence/table-analytics",
		"jira/batch-analysis",
		"jira/batch-edit",
		"jira/board-portfolio",
		"jira/create",
		"jira/edit",
		"jira/evidence",
		"jira/graph-evidence",
		"jira/inverse-reference",
		"jira/link",
		"jira/mirror",
		"jira/portfolio",
		"jira/setup",
		"jira/structure-planning",
		"knowledge/search",
	}
	if got := TaskClasses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TaskClasses()=%v want=%v", got, want)
	}

	fromDefinitions := map[string]bool{}
	for _, definition := range Definitions() {
		fromDefinitions[definition.TaskClass] = true
	}
	if len(fromDefinitions) != len(want) {
		t.Fatalf("definition task classes=%v want=%v", fromDefinitions, want)
	}
	for _, taskClass := range want {
		if !fromDefinitions[taskClass] {
			t.Fatalf("TaskClasses contains %q without a definition", taskClass)
		}
	}
}

func TestTaskClassesReturnsDefensiveCopy(t *testing.T) {
	first := TaskClasses()
	first[0] = "changed"
	if second := TaskClasses(); second[0] == "changed" {
		t.Fatal("TaskClasses shared caller-mutable storage")
	}
}
