package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestPlanningCSVNeutralizesFormulaCellsByDefault(t *testing.T) {
	rows := []PlanningIssueQuality{{
		Key: "=KEY", Level: "+level", Gaps: []string{"@gap", "-risk"}, Children: []string{"=CHILD"},
	}}
	safe, err := renderPlanningCSV(rows, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'=KEY", "'+level", "'@gap", "'=CHILD"} {
		if !strings.Contains(string(safe), want) {
			t.Fatalf("safe CSV missing %q: %q", want, safe)
		}
	}
	raw, err := renderPlanningCSV(rows, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "'=KEY") || !strings.Contains(string(raw), "=KEY") {
		t.Fatalf("raw CSV = %q", raw)
	}
}

func TestPlanningRawCSVRequiresCSVPath(t *testing.T) {
	svc := &JiraService{}
	_, err := svc.PlanningReport(context.Background(), PlanningReportOpts{JQL: "project = PROJ", RawCSV: true})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
}

func TestExtractPlanningRefsClassifiesAndDedupes(t *testing.T) {
	refs := ExtractPlanningRefs("See https://figma.com/file/abc and https://docs.example.com/spec. Again https://figma.com/file/abc")
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2 unique refs", refs)
	}
	kinds := refs[0].Kind + "," + refs[1].Kind
	if !strings.Contains(kinds, "design") || !strings.Contains(kinds, "doc") {
		t.Fatalf("refs = %+v, want design and doc", refs)
	}
}

func TestPlanningReportScoresGapsRefsAndEpicChildren(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "planning.csv")
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{
			Key:      "PROJ-1",
			Summary:  "Parent",
			Type:     "Epic",
			Assignee: "Alice",
			Body:     "Context https://docs.example.com/spec",
			Fields:   map[string]any{"estimate": 8},
		},
		{
			Key:      "PROJ-2",
			Summary:  "Child",
			Type:     "Story",
			Assignee: "",
			Body:     "",
			Fields:   map[string]any{"epic": "PROJ-1"},
		},
	}}}

	report, err := svc.PlanningReport(context.Background(), PlanningReportOpts{
		JQL:           "project = PROJ",
		Required:      []string{"estimate"},
		EstimateField: "estimate",
		EpicField:     "epic",
		Limit:         100,
		CSVPath:       csvPath,
	})
	if err != nil {
		t.Fatalf("PlanningReport: %v", err)
	}
	if report.Count != 2 || report.CSVPath != csvPath {
		t.Fatalf("report = %+v, want count/csv path", report)
	}
	if _, err := os.ReadFile(csvPath); err != nil {
		t.Fatalf("csv was not written: %v", err)
	}
	epic := report.Issues[0]
	if epic.Key != "PROJ-1" || strings.Join(epic.Children, ",") != "PROJ-2" || len(epic.Refs) != 1 {
		t.Fatalf("epic row = %+v, want child PROJ-2 and one ref", epic)
	}
	child := report.Issues[1]
	gaps := strings.Join(child.Gaps, ",")
	for _, want := range []string{"missing_description", "missing_assignee", "missing_estimate", "missing_artifact_ref"} {
		if !strings.Contains(gaps, want) {
			t.Fatalf("child gaps = %v, want %s", child.Gaps, want)
		}
	}
}

func TestIssueRefsSupportsKeyAndJQL(t *testing.T) {
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{
			Key:     "PROJ-2",
			Summary: "Second",
			Type:    "Story",
			Body:    "Spec https://docs.example.com/spec",
			Comments: []domain.Comment{{
				Body: "Design https://figma.com/file/abc",
			}},
		},
		{
			Key:     "PROJ-1",
			Summary: "First",
			Type:    "Bug",
			Body:    "No links",
		},
	}}}

	one, err := svc.IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-2"})
	if err != nil {
		t.Fatalf("IssueRefs key: %v", err)
	}
	if one.Count != 1 || one.Issues[0].Key != "PROJ-2" || len(one.Issues[0].Refs) != 2 {
		t.Fatalf("one refs = %+v, want two refs for PROJ-2", one)
	}

	all, err := svc.IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 10})
	if err != nil {
		t.Fatalf("IssueRefs jql: %v", err)
	}
	if all.Count != 2 || all.Issues[0].Key != "PROJ-1" || all.Issues[1].Key != "PROJ-2" {
		t.Fatalf("all refs = %+v, want sorted issue rows", all.Issues)
	}
}

func TestIssueTreeGroupsEpicsExternalEpicsAndOrphans(t *testing.T) {
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{Key: "PROJ-2", Summary: "Child", Type: "Story", Fields: map[string]any{"epic": "PROJ-1"}},
		{Key: "PROJ-1", Summary: "Parent", Type: "Epic", Fields: map[string]any{}},
		{Key: "PROJ-3", Summary: "External child", Type: "Story", Fields: map[string]any{"epic": "PROJ-X"}},
		{Key: "PROJ-4", Summary: "Orphan", Type: "Task", Fields: map[string]any{}},
	}}}

	tree, err := svc.IssueTree(context.Background(), JiraIssueTreeOpts{JQL: "project = PROJ", EpicField: "epic", Limit: 10})
	if err != nil {
		t.Fatalf("IssueTree: %v", err)
	}
	if tree.Count != 4 || tree.EpicField != "epic" {
		t.Fatalf("tree header = %+v, want count/epic field", tree)
	}
	if len(tree.Epics) != 1 || tree.Epics[0].Key != "PROJ-1" || len(tree.Epics[0].Children) != 1 || tree.Epics[0].Children[0].Key != "PROJ-2" {
		t.Fatalf("epics = %+v, want PROJ-1 -> PROJ-2", tree.Epics)
	}
	if len(tree.ExternalEpics) != 1 || tree.ExternalEpics[0].Key != "PROJ-X" || !tree.ExternalEpics[0].External || tree.ExternalEpics[0].Children[0].Key != "PROJ-3" {
		t.Fatalf("external epics = %+v, want external PROJ-X -> PROJ-3", tree.ExternalEpics)
	}
	if len(tree.Orphans) != 1 || tree.Orphans[0].Key != "PROJ-4" {
		t.Fatalf("orphans = %+v, want PROJ-4", tree.Orphans)
	}
}
