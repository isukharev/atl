package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestNormalizeStructureValueRowsMapsAttributeMatrix(t *testing.T) {
	values := &domain.StructureValues{Responses: []map[string]any{{
		"rows": []any{float64(100), float64(101)},
		"data": []any{
			map[string]any{"attribute": map[string]any{"id": "summary", "format": "text"}, "values": []any{"Folder", "Issue"}},
			map[string]any{"attribute": map[string]any{"id": "status", "format": "text"}, "values": []any{nil, "Open"}},
		},
	}}}

	rows, seen, err := normalizeStructureValueRows(values)
	if err != nil {
		t.Fatal(err)
	}
	if !seen[100] || !seen[101] || rows[100]["summary"] != "Folder" || rows[101]["status"] != "Open" {
		t.Fatalf("normalized rows=%+v seen=%+v", rows, seen)
	}
}

func TestRenderStructureSnapshotIsCompactAndStreamFriendly(t *testing.T) {
	snapshot := &StructureSnapshot{
		SchemaVersion: 1,
		Structure:     StructureSnapshotMetadata{ID: 123, Name: "Quarter | plan"},
		ForestVersion: domain.StructureVersion{Signature: 55, Version: 7},
		Projection: StructureProjection{
			Kind: "atl-attributes-v1", Source: "explicit", Attributes: []string{"key", "summary", "status"},
		},
		Rows: []StructureSnapshotRow{{
			RowID: 100, ItemType: "issue", ItemID: "10001", Accessible: true, Values: map[string]any{
				"key": "PROJ-1", "summary": "Line one\nline | two", "status": map[string]any{"name": "Open", "self": "https://example.invalid/private"},
			},
		}},
		RowCount: 1, IssueCount: 1, Complete: true, InaccessibleRows: []int64{},
	}

	md := string(renderStructureSnapshotMarkdown(snapshot))
	if !strings.Contains(md, `Line one line \| two`) || !strings.Contains(md, "| Open |") || strings.Contains(md, "example.invalid") {
		t.Fatalf("Markdown is not compact/safe:\n%s", md)
	}
	jsonl, err := renderStructureSnapshot("jsonl", snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		StructureID int64                `json:"structure_id"`
		Projection  StructureProjection  `json:"projection"`
		Row         StructureSnapshotRow `json:"row"`
	}
	if err := json.Unmarshal(jsonl, &record); err != nil {
		t.Fatalf("JSONL record: %v\n%s", err, jsonl)
	}
	if record.StructureID != 123 || record.Projection.Attributes[2] != "status" || record.Row.RowID != 100 {
		t.Fatalf("JSONL record=%+v", record)
	}
}

func TestStructureExportCSVNeutralizesFormulaCellsByDefault(t *testing.T) {
	snapshot := &StructureSnapshot{
		Projection: StructureProjection{Attributes: []string{"summary", "=field"}},
		Rows: []StructureSnapshotRow{{RowID: 1, ItemType: "@folder", ItemID: "+item", Values: map[string]any{
			"summary": "=HYPERLINK(\"https://example.invalid\")", "=field": "-formula",
		}}},
	}
	safe, err := renderStructureSnapshotCSV(snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'@folder", "'+item", "'=field", "'=HYPERLINK", "'-formula"} {
		if !strings.Contains(string(safe), want) {
			t.Fatalf("safe CSV missing %q: %q", want, safe)
		}
	}
	raw, err := renderStructureSnapshotCSV(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "'=HYPERLINK") || !strings.Contains(string(raw), "=HYPERLINK") {
		t.Fatalf("raw CSV = %q", raw)
	}
}

func TestStructureExportRawCSVRequiresCSVFormat(t *testing.T) {
	svc := &JiraService{}
	_, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{Format: "json", Out: "out.json", RawCSV: true})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
}

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
