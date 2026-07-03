package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

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
