package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type boundedStructureReader struct{}

func (boundedStructureReader) GetStructure(context.Context, int64) (*domain.Structure, error) {
	return &domain.Structure{ID: 1, Name: "Synthetic"}, nil
}

func (boundedStructureReader) StructureForest(context.Context, int64) (*domain.StructureForest, error) {
	return &domain.StructureForest{Formula: "10:0:10001,11:0:10002", Version: domain.StructureVersion{Version: 1}}, nil
}

func (boundedStructureReader) StructureValues(context.Context, int64, []int64, []string) (*domain.StructureValues, error) {
	return &domain.StructureValues{InaccessibleRows: []int64{}}, nil
}

type scanBoundStructureReader struct {
	valuesCalls *int
}

func (scanBoundStructureReader) GetStructure(context.Context, int64) (*domain.Structure, error) {
	return &domain.Structure{ID: 1, Name: "Synthetic"}, nil
}

func (scanBoundStructureReader) StructureForest(context.Context, int64) (*domain.StructureForest, error) {
	return &domain.StructureForest{
		Formula:   "10:0:1/root,11:1:10001,12:0:1/other",
		ItemTypes: map[string]string{"1": "folder"},
		Version:   domain.StructureVersion{Version: 1},
	}, nil
}

func (r scanBoundStructureReader) StructureValues(context.Context, int64, []int64, []string) (*domain.StructureValues, error) {
	(*r.valuesCalls)++
	return &domain.StructureValues{InaccessibleRows: []int64{}}, nil
}

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

func TestStructureSnapshotValuesNeverJoinNonIssueNumericCollision(t *testing.T) {
	issues := map[string]JiraIssueSnapshot{"10001": {Key: "PROJ-1", ID: "10001", Fields: map[string]any{"summary": "Issue"}}}
	row := domain.StructureRow{RowID: 7, ItemType: "folder", ItemID: "10001"}
	values := structureSnapshotValues(row, []string{"key", "summary"}, issues, map[int64]string{7: "Folder"})
	if values["key"] != nil || values["summary"] != "Folder" {
		t.Fatalf("values=%+v, want folder label without colliding issue fields", values)
	}
}

func TestMarkdownTableCellPreservesPunctuationAndBackslash(t *testing.T) {
	got := markdownTableCell(`owner's "plan" \\| <draft>`)
	want := `owner's "plan" \\\\\| &lt;draft&gt;`
	if got != want {
		t.Fatalf("markdownTableCell=%q, want %q", got, want)
	}
}

func TestSnapshotTextMarksUnknownNonEmptyObject(t *testing.T) {
	got := snapshotText(map[string]any{"self": "https://example.invalid/private", "opaque": true})
	if got != "[object]" {
		t.Fatalf("snapshotText=%q, want explicit non-empty object marker", got)
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

func TestStructureSnapshotRejectsInvalidMaxRowsBeforeBackendAccess(t *testing.T) {
	svc := &JiraService{}
	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{MaxRows: -1})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
}

func TestStructureSnapshotRejectsInvalidMaxScanRowsBeforeBackendAccess(t *testing.T) {
	svc := &JiraService{}
	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{MaxScanRows: -1})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
}

func TestStructureSnapshotEnforcesMaxRowsBeforeIssueExpansion(t *testing.T) {
	svc := &JiraService{structure: boundedStructureReader{}}
	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{Attributes: []string{"key"}, MaxRows: 1})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "exceeds max rows") {
		t.Fatalf("error = %v, want bounded check failure", err)
	}
}

func TestStructureSnapshotEnforcesScanBoundBeforeFolderValueQuery(t *testing.T) {
	valuesCalls := 0
	svc := &JiraService{structure: scanBoundStructureReader{valuesCalls: &valuesCalls}}
	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes: []string{"key"}, MaxRows: 2, MaxScanRows: 2, StructureFolderSelector: StructureFolderSelector{FolderID: "root"},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "exceeds max scan rows") {
		t.Fatalf("error = %v, want bounded scan failure", err)
	}
	if valuesCalls != 0 {
		t.Fatalf("StructureValues calls = %d, want none before scan bound", valuesCalls)
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

func TestBuildStructureFoldersCalculatesPathsAndOccurrenceStats(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "f-root"},
		{RowID: 11, Depth: 1, ItemType: "issue", ItemID: "10001"},
		{RowID: 12, Depth: 1, ItemType: "folder", ItemID: "f-child"},
		{RowID: 13, Depth: 2, ItemType: "issue", ItemID: "10001"},
		{RowID: 14, Depth: 2, ItemType: "issue", ItemID: "10002"},
		{RowID: 20, Depth: 0, ItemType: "folder", ItemID: "f-other"},
	}
	folders := buildStructureFolders(rows, map[int64]string{10: "Plans", 12: "Quarter", 20: "Plans"})
	if len(folders) != 3 || strings.Join(folders[1].Path, "/") != "Plans/Quarter" || folders[1].ParentFolderID != "f-root" {
		t.Fatalf("folders=%+v", folders)
	}
	stats := folders[0].Stats
	if stats.DescendantRows != 4 || stats.IssueRows != 3 || stats.UniqueIssues != 2 || stats.Subfolders != 1 || stats.MaxRelativeDepth != 2 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestSelectStructureFolderIsExactFailClosedAndRelative(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "f-root"},
		{RowID: 11, Depth: 1, ItemType: "folder", ItemID: "f-child"},
		{RowID: 12, Depth: 2, ItemType: "issue", ItemID: "10001"},
		{RowID: 20, Depth: 0, ItemType: "folder", ItemID: "f-other"},
		{RowID: 21, Depth: 1, ItemType: "folder", ItemID: "f-child-2"},
	}
	folders := buildStructureFolders(rows, map[int64]string{10: "Plans", 11: "Quarter", 20: "Archive", 21: "Quarter"})
	selected, selection, err := selectStructureFolder(rows, folders, true, StructureFolderSelector{FolderPath: " plans / quarter "})
	if err != nil || len(selected) != 2 || selection.FolderID != "f-child" || selected[0].RelativeDepth == nil || *selected[0].RelativeDepth != 0 || *selected[1].RelativeDepth != 1 || selected[0].Depth != 1 {
		t.Fatalf("selected=%+v selection=%+v err=%v", selected, selection, err)
	}
	if _, _, err := selectStructureFolder(rows, folders, true, StructureFolderSelector{FolderPath: "Quarter"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("non-exact path error=%v", err)
	}
	if _, _, err := selectStructureFolder(rows, folders, false, StructureFolderSelector{FolderPath: "Plans/Quarter"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("incomplete labels error=%v", err)
	}
}

func TestSelectStructureFolderRejectsDuplicateStableIDOccurrences(t *testing.T) {
	rows := []domain.StructureRow{{RowID: 10, ItemType: "folder", ItemID: "same"}, {RowID: 20, ItemType: "folder", ItemID: "same"}}
	folders := buildStructureFolders(rows, map[int64]string{10: "A", 20: "B"})
	if _, _, err := selectStructureFolder(rows, folders, true, StructureFolderSelector{FolderID: "same"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("duplicate id error=%v", err)
	}
}

func TestSelectStructureFolderRejectsDuplicateExactPathsAndNonFolderRows(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "root"},
		{RowID: 11, Depth: 1, ItemType: "folder", ItemID: "a"},
		{RowID: 12, Depth: 1, ItemType: "folder", ItemID: "b"},
		{RowID: 13, Depth: 1, ItemType: "issue", ItemID: "10001"},
	}
	folders := buildStructureFolders(rows, map[int64]string{10: "Plans", 11: "Same", 12: "Same"})
	if _, _, err := selectStructureFolder(rows, folders, true, StructureFolderSelector{FolderPath: "Plans/Same"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("duplicate path error=%v", err)
	}
	if _, _, err := selectStructureFolder(rows, folders, true, StructureFolderSelector{FolderRow: 13}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("non-folder row error=%v", err)
	}
}
