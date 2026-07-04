package app

import (
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestParseStructureRowsBuildsHierarchyAndItemTypes(t *testing.T) {
	forest := &domain.StructureForest{
		Formula:   "100:0:10001,101:1:10002:done,102:1:1/200,103:2:2//folder-A",
		ItemTypes: map[string]string{"1": "folder", "2": "generator"},
	}

	rows, err := ParseStructureRows(forest)
	if err != nil {
		t.Fatalf("ParseStructureRows: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}
	if rows[0].RowID != 100 || rows[0].Depth != 0 || rows[0].ItemType != "issue" || rows[0].ItemID != "10001" || rows[0].ParentRowID != 0 {
		t.Errorf("root row = %+v, want issue 10001 without parent", rows[0])
	}
	if rows[1].ParentRowID != 100 || rows[1].Semantic != "done" {
		t.Errorf("child issue row = %+v, want parent 100 and semantic done", rows[1])
	}
	if rows[2].ItemType != "folder" || rows[2].ItemID != "200" || rows[2].ParentRowID != 100 {
		t.Errorf("folder row = %+v, want mapped type folder item 200 parent 100", rows[2])
	}
	if rows[3].ItemType != "generator" || rows[3].ItemID != "folder-A" || rows[3].ParentRowID != 102 {
		t.Errorf("string-id row = %+v, want mapped type generator item folder-A parent 102", rows[3])
	}
}

func TestParseStructureRowsRejectsBadFormulaComponent(t *testing.T) {
	_, err := ParseStructureRows(&domain.StructureForest{Formula: "not-enough"})
	if err == nil {
		t.Fatal("ParseStructureRows(bad): want error, got nil")
	}
}

func TestFilterStructureRowsKeepsFirstMatchingSubtree(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 100, Depth: 0, ItemType: "folder", ItemID: "root-a", Position: 0},
		{RowID: 101, Depth: 1, ParentRowID: 100, ItemType: "issue", ItemID: "10001", Position: 1},
		{RowID: 102, Depth: 2, ParentRowID: 101, ItemType: "issue", ItemID: "10002", Position: 2},
		{RowID: 103, Depth: 1, ParentRowID: 100, ItemType: "issue", ItemID: "10003", Position: 3},
		{RowID: 200, Depth: 0, ItemType: "folder", ItemID: "root-b", Position: 4},
		{RowID: 201, Depth: 1, ParentRowID: 200, ItemType: "issue", ItemID: "20001", Position: 5},
	}

	filtered := FilterStructureRows(rows, "Release Root", map[int64]string{100: `{"summary":"Release root"}`})
	if len(filtered) != 4 {
		t.Fatalf("filtered len = %d, want first subtree of 4 rows: %+v", len(filtered), filtered)
	}
	if filtered[0].RowID != 100 || filtered[3].RowID != 103 {
		t.Fatalf("filtered = %+v, want rows 100..103", filtered)
	}
}

func TestStructureExportDocumentAttachesIssueFields(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 100, Depth: 0, ItemType: "issue", ItemID: "10001"},
		{RowID: 101, Depth: 1, ParentRowID: 100, ItemType: "folder", ItemID: "folder-a"},
	}
	doc := structureExportDocument(123, nil, rows, []string{"10001"}, []JiraIssueSnapshot{{
		Key:    "PROJ-1",
		ID:     "10001",
		Fields: map[string]any{"summary": "First"},
	}})

	if len(doc.Rows) != 2 || doc.Rows[0].IssueKey != "PROJ-1" || doc.Rows[0].Fields["summary"] != "First" {
		t.Fatalf("doc rows = %+v, want issue fields attached to row 100", doc.Rows)
	}
	if doc.Rows[1].IssueKey != "" || doc.Rows[1].Fields != nil {
		t.Fatalf("folder row = %+v, want no issue attachment", doc.Rows[1])
	}
}
