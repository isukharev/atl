package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
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

type valueRootStructureReader struct{}

func (valueRootStructureReader) GetStructure(context.Context, int64) (*domain.Structure, error) {
	return &domain.Structure{ID: 1, Name: "Synthetic"}, nil
}

func (valueRootStructureReader) StructureForest(context.Context, int64) (*domain.StructureForest, error) {
	return &domain.StructureForest{
		Formula: "10:0:10001,11:1:10002,12:0:10003",
		Version: domain.StructureVersion{Version: 1},
	}, nil
}

func (valueRootStructureReader) StructureValues(context.Context, int64, []int64, []string) (*domain.StructureValues, error) {
	return &domain.StructureValues{InaccessibleRows: []int64{}}, nil
}

type valueRootTracker struct {
	domain.Tracker
	searchCalls int
	queries     []string
}

func (t *valueRootTracker) Search(_ context.Context, jql string, _ []string, _ int, _ string) ([]domain.Issue, string, error) {
	t.searchCalls++
	t.queries = append(t.queries, jql)
	return []domain.Issue{
		{ID: "10001", Key: "PROJ-1", Fields: map[string]any{"summary": "Selected root"}},
		{ID: "10002", Key: "PROJ-2", Fields: map[string]any{"summary": "Selected child"}},
		{ID: "10003", Key: "PROJ-3", Fields: map[string]any{"summary": "Other root"}},
	}, "", nil
}

// forestVersionReader serves one folder + one issue row at a fixed version.
type forestVersionReader struct {
	forestCalls *int
	valuesCalls *int
	version     domain.StructureVersion
}

func (forestVersionReader) GetStructure(context.Context, int64) (*domain.Structure, error) {
	return &domain.Structure{ID: 1, Name: "Synthetic"}, nil
}

func (r forestVersionReader) StructureForest(context.Context, int64) (*domain.StructureForest, error) {
	(*r.forestCalls)++
	return &domain.StructureForest{
		Formula:   "10:0:1/f-root,11:1:10001",
		ItemTypes: map[string]string{"1": "folder"},
		Version:   r.version,
	}, nil
}

func (r forestVersionReader) StructureValues(context.Context, int64, []int64, []string) (*domain.StructureValues, error) {
	(*r.valuesCalls)++
	return &domain.StructureValues{
		Responses: []map[string]any{{
			"rows": []any{float64(10)},
			"data": []any{map[string]any{"attribute": map[string]any{"id": "summary"}, "values": []any{"Folder"}}},
		}},
		InaccessibleRows: []int64{},
	}, nil
}

func newForestVersionService(version domain.StructureVersion) (*JiraService, *valueRootTracker, *int, *int) {
	forestCalls, valuesCalls := 0, 0
	tracker := &valueRootTracker{}
	svc := &JiraService{tr: tracker, structure: forestVersionReader{
		forestCalls: &forestCalls, valuesCalls: &valuesCalls, version: version,
	}}
	return svc, tracker, &forestCalls, &valuesCalls
}

func TestStructureSnapshotBindsMatchingExpectedForestVersion(t *testing.T) {
	svc, _, forestCalls, _ := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:            []string{"key", "summary"},
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ForestVersionGated {
		t.Fatal("matching expected forest version was not reported as gated")
	}
	if got.ForestVersion != (domain.StructureVersion{Signature: 55, Version: 7}) || got.RowCount != 2 {
		t.Fatalf("snapshot version=%+v rows=%d", got.ForestVersion, got.RowCount)
	}
	if *forestCalls != 1 {
		t.Fatalf("forest reads = %d, want one initial read", *forestCalls)
	}
}

func TestStructureSnapshotReportsUngatedReadWithoutExtraForestRequest(t *testing.T) {
	svc, _, forestCalls, _ := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{Attributes: []string{"key"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ForestVersionGated {
		t.Fatal("view without an expected pair was reported as gated")
	}
	if *forestCalls != 1 {
		t.Fatalf("forest reads = %d, want no extra forest read", *forestCalls)
	}
}

func TestStructureSnapshotRejectsStaleExpectedForestVersionBeforeProjection(t *testing.T) {
	svc, tracker, forestCalls, valuesCalls := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:            []string{"key", "summary"},
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 6},
	})
	var mismatch *StructureForestVersionMismatchError
	if got != nil || !errors.As(err, &mismatch) || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("snapshot=%v err=%v, want a typed check failure and no snapshot", got, err)
	}
	if mismatch.Expected != (domain.StructureVersion{Signature: 55, Version: 6}) ||
		mismatch.Current != (domain.StructureVersion{Signature: 55, Version: 7}) {
		t.Fatalf("mismatch=%+v, want the supplied and observed pairs", mismatch)
	}
	if *forestCalls != 1 {
		t.Fatalf("forest reads = %d, want only the initial read before failing closed", *forestCalls)
	}
	if *valuesCalls != 0 || tracker.searchCalls != 0 {
		t.Fatalf("value calls=%d search calls=%d, want no projection work on a stale expectation", *valuesCalls, tracker.searchCalls)
	}
}

func TestStructureSnapshotRejectsIncompleteExpectedForestVersionBeforeBackendAccess(t *testing.T) {
	for _, expected := range []domain.StructureVersion{
		{Signature: 0, Version: 7},
		{Signature: 55, Version: 0},
		{Signature: 55, Version: -1},
	} {
		svc := &JiraService{}
		_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{ExpectedForestVersion: &expected})
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("expected=%+v error=%v, want usage before any backend read", expected, err)
		}
	}
}

func TestStructureRowsWithOptionsBindsExactForestVersion(t *testing.T) {
	svc, _, forestCalls, valuesCalls := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	gated, err := svc.StructureRowsWithOptions(t.Context(), 1, StructureRowsOpts{
		ExpectedForestVersion:   &domain.StructureVersion{Signature: 55, Version: 7},
		StructureFolderSelector: StructureFolderSelector{FolderID: "f-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !gated.ForestVersionGated || gated.Version == nil || *gated.Version != (domain.StructureVersion{Signature: 55, Version: 7}) {
		t.Fatalf("gated rows=%+v, want the observed pair reported as gated", gated)
	}
	if len(gated.Rows) != 2 || gated.Selection == nil || *valuesCalls != 1 {
		t.Fatalf("rows=%d selection=%+v value calls=%d, want the matching pair to permit folder selection", len(gated.Rows), gated.Selection, *valuesCalls)
	}

	ungated, err := svc.StructureRowsWithOptions(t.Context(), 1, StructureRowsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if ungated.ForestVersionGated || len(ungated.Rows) != 2 {
		t.Fatalf("ungated rows=%+v, want backward-compatible ungated output", ungated)
	}
	if *forestCalls != 2 {
		t.Fatalf("forest reads = %d, want one read per call", *forestCalls)
	}
}

func TestStructureRowsWithOptionsRejectsStaleForestVersionBeforeSelectorWork(t *testing.T) {
	for _, test := range []struct {
		name string
		opts StructureRowsOpts
	}{
		{name: "folder selector", opts: StructureRowsOpts{StructureFolderSelector: StructureFolderSelector{FolderID: "f-root"}}},
		{name: "root filter", opts: StructureRowsOpts{Root: "Folder", RootFields: []string{"key", "summary"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, tracker, forestCalls, valuesCalls := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})
			opts := test.opts
			opts.ExpectedForestVersion = &domain.StructureVersion{Signature: 55, Version: 6}

			got, err := svc.StructureRowsWithOptions(t.Context(), 1, opts)
			var mismatch *StructureForestVersionMismatchError
			if got != nil || !errors.As(err, &mismatch) || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("rows=%v err=%v, want a typed check failure and no rows", got, err)
			}
			if mismatch.Expected != (domain.StructureVersion{Signature: 55, Version: 6}) ||
				mismatch.Current != (domain.StructureVersion{Signature: 55, Version: 7}) {
				t.Fatalf("mismatch=%+v, want the supplied and observed pairs", mismatch)
			}
			if *forestCalls != 1 || *valuesCalls != 0 || tracker.searchCalls != 0 {
				t.Fatalf("forest=%d value=%d search=%d, want only the initial forest read", *forestCalls, *valuesCalls, tracker.searchCalls)
			}
		})
	}
}

func TestStructurePullIssuesGatesForestBeforeJiraReadsAndOutput(t *testing.T) {
	stalePath := t.TempDir() + "/stale.json"
	svc, tracker, _, valuesCalls := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	got, err := svc.StructurePullIssues(t.Context(), 1, StructureIssuePullOpts{
		Fields:                []string{"summary"},
		Out:                   stalePath,
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 6},
	})
	var mismatch *StructureForestVersionMismatchError
	if got != nil || !errors.As(err, &mismatch) || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("pull=%v err=%v, want a typed check failure and no result", got, err)
	}
	if tracker.searchCalls != 0 || *valuesCalls != 0 {
		t.Fatalf("search=%d value=%d, want no Jira or value reads on a stale expectation", tracker.searchCalls, *valuesCalls)
	}
	if _, statErr := os.Stat(stalePath); !os.IsNotExist(statErr) {
		t.Fatalf("stale pull created %q (stat err=%v)", stalePath, statErr)
	}

	livePath := t.TempDir() + "/pull.json"
	live, err := svc.StructurePullIssues(t.Context(), 1, StructureIssuePullOpts{
		Fields:                []string{"summary"},
		Out:                   livePath,
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !live.ForestVersionGated || live.Version == nil || *live.Version != (domain.StructureVersion{Signature: 55, Version: 7}) ||
		live.Path != livePath || tracker.searchCalls != 1 {
		t.Fatalf("gated pull=%+v search=%d, want gated provenance and one issue read", live, tracker.searchCalls)
	}
	liveData, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(liveData), `"forest_version_gated": true`) {
		t.Fatalf("pulled snapshot lost forest provenance: %s", liveData)
	}

	ungated, err := svc.StructurePullIssues(t.Context(), 1, StructureIssuePullOpts{Fields: []string{"summary"}})
	if err != nil {
		t.Fatal(err)
	}
	if ungated.ForestVersionGated || ungated.Version == nil {
		t.Fatalf("ungated pull=%+v, want backward-compatible ungated output that keeps version", ungated)
	}
}

func TestStructureExportGatesForestBeforeRenderingAndFileCreation(t *testing.T) {
	stalePath := t.TempDir() + "/stale.json"
	svc, tracker, _, _ := newForestVersionService(domain.StructureVersion{Signature: 55, Version: 7})

	_, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{
		Format:                "json",
		Out:                   stalePath,
		Fields:                []string{"key", "summary"},
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 6},
	})
	var mismatch *StructureForestVersionMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("export err=%v, want a typed check failure", err)
	}
	if _, statErr := os.Stat(stalePath); !os.IsNotExist(statErr) {
		t.Fatalf("stale export created %q (stat err=%v)", stalePath, statErr)
	}
	if tracker.searchCalls != 0 {
		t.Fatalf("search calls = %d, want none on a stale expectation", tracker.searchCalls)
	}

	livePath := t.TempDir() + "/export.json"
	live, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{
		Format:                "json",
		Out:                   livePath,
		Fields:                []string{"key", "summary"},
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !live.ForestVersionGated || live.ForestVersion != (domain.StructureVersion{Signature: 55, Version: 7}) || live.RowCount != 2 {
		t.Fatalf("export result=%+v, want auditable gated provenance", live)
	}
	data, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"forest_version_gated": true`) {
		t.Fatalf("exported JSON lost snapshot provenance: %s", data)
	}

	csvPath := t.TempDir() + "/export.csv"
	csvResult, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{
		Format:                "csv",
		Out:                   csvPath,
		Fields:                []string{"key", "summary"},
		ExpectedForestVersion: &domain.StructureVersion{Signature: 55, Version: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	csvData, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !csvResult.ForestVersionGated || csvResult.ForestVersion != (domain.StructureVersion{Signature: 55, Version: 7}) {
		t.Fatalf("csv export result=%+v, want the gate reported on the command result", csvResult)
	}
	if !strings.HasPrefix(string(csvData), "row_id,depth,relative_depth,parent_row_id,position,item_type,item_id,accessible,key,summary\n") ||
		strings.Contains(string(csvData), "forest_version") {
		t.Fatalf("csv cell contract changed: %s", csvData)
	}

	ungatedPath := t.TempDir() + "/ungated.json"
	ungated, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{Format: "json", Out: ungatedPath, Fields: []string{"key"}})
	if err != nil {
		t.Fatal(err)
	}
	if ungated.ForestVersionGated || ungated.ForestVersion != (domain.StructureVersion{Signature: 55, Version: 7}) {
		t.Fatalf("ungated export=%+v, want the observed forest reported without a gate", ungated)
	}
}

func TestStructureSelectorConsumersRejectIncompleteExpectedForestVersionBeforeBackendAccess(t *testing.T) {
	for _, consumer := range []struct {
		name string
		run  func(svc *JiraService, expected *domain.StructureVersion, out string) error
	}{
		{name: "rows", run: func(svc *JiraService, expected *domain.StructureVersion, _ string) error {
			_, err := svc.StructureRowsWithOptions(t.Context(), 1, StructureRowsOpts{ExpectedForestVersion: expected})
			return err
		}},
		{name: "pull-issues", run: func(svc *JiraService, expected *domain.StructureVersion, out string) error {
			_, err := svc.StructurePullIssues(t.Context(), 1, StructureIssuePullOpts{Fields: []string{"summary"}, Out: out, ExpectedForestVersion: expected})
			return err
		}},
		{name: "export", run: func(svc *JiraService, expected *domain.StructureVersion, out string) error {
			_, err := svc.StructureExport(t.Context(), 1, StructureExportOpts{Format: "json", Out: out, ExpectedForestVersion: expected})
			return err
		}},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			for _, expected := range []domain.StructureVersion{
				{Signature: 0, Version: 7},
				{Signature: 55, Version: 0},
				{Signature: 55, Version: -1},
			} {
				out := t.TempDir() + "/out.json"
				if err := consumer.run(&JiraService{}, &expected, out); !errors.Is(err, domain.ErrUsage) {
					t.Fatalf("expected=%+v error=%v, want usage before any backend read", expected, err)
				}
				if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
					t.Fatalf("expected=%+v created %q (stat err=%v)", expected, out, statErr)
				}
			}
		})
	}
}

func TestStructureForestVersionMismatchCarriesOnlyIntegerVersionEvidence(t *testing.T) {
	err := newStructureForestVersionMismatch(
		domain.StructureVersion{Signature: 55, Version: 7},
		domain.StructureVersion{Signature: 56, Version: 8},
	)
	want := "check failed: Structure forest version mismatch: expected signature=55 version=7, got signature=56 version=8"
	if err.Error() != want {
		t.Fatalf("message=%q want %q", err.Error(), want)
	}
	if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sentinel mapping lost: %v", err)
	}
	versionType := reflect.TypeOf(domain.StructureVersion{})
	errorType := reflect.TypeOf(*err)
	for i := range errorType.NumField() {
		if field := errorType.Field(i); field.Type != versionType {
			t.Fatalf("field %q has type %s, want an integer version pair", field.Name, field.Type)
		}
	}
	for i := range versionType.NumField() {
		if field := versionType.Field(i); field.Type.Kind() != reflect.Int64 {
			t.Fatalf("version field %q has kind %s, want int64", field.Name, field.Type.Kind())
		}
	}
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
		SchemaVersion:      1,
		Structure:          StructureSnapshotMetadata{ID: 123, Name: "Quarter | plan"},
		ForestVersion:      domain.StructureVersion{Signature: 55, Version: 7},
		ForestVersionGated: true,
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
	if !strings.Contains(md, `Line one line \| two`) || !strings.Contains(md, "| Open |") ||
		!strings.Contains(md, "gated: `true`") ||
		!strings.Contains(md, "separately timed") || strings.Contains(md, "example.invalid") {
		t.Fatalf("Markdown is not compact/safe:\n%s", md)
	}
	jsonl, err := renderStructureSnapshot("jsonl", snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		StructureID        int64                `json:"structure_id"`
		ForestVersionGated bool                 `json:"forest_version_gated"`
		Projection         StructureProjection  `json:"projection"`
		Row                StructureSnapshotRow `json:"row"`
	}
	if err := json.Unmarshal(jsonl, &record); err != nil {
		t.Fatalf("JSONL record: %v\n%s", err, jsonl)
	}
	if record.StructureID != 123 || !record.ForestVersionGated ||
		record.Projection.Attributes[2] != "status" || record.Row.RowID != 100 {
		t.Fatalf("JSONL record=%+v", record)
	}
}

func TestStructureSnapshotMetadataPreservesBothReadOnlyStates(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		encoded, err := json.Marshal(StructureSnapshotMetadata{ID: 123, Name: "Plan", ReadOnly: readOnly})
		if err != nil {
			t.Fatal(err)
		}
		want := `"read_only":false`
		if readOnly {
			want = `"read_only":true`
		}
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("read_only=%t encoded as %s, want explicit boolean", readOnly, encoded)
		}
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

func TestStructureSnapshotPreservesTypedFolderSelectionError(t *testing.T) {
	valuesCalls := 0
	svc := &JiraService{structure: scanBoundStructureReader{valuesCalls: &valuesCalls}}
	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes: []string{"key"}, MaxRows: 3, MaxScanRows: 3,
		StructureFolderSelector: StructureFolderSelector{FolderID: "missing"},
	})
	var selection *StructureFolderSelectionError
	if !errors.As(err, &selection) ||
		selection.Reason != StructureFolderSelectionNotFound ||
		selection.Matches != 0 || selection.Available != 2 {
		t.Fatalf("selection error = %#v, err = %v", selection, err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	if valuesCalls != 1 {
		t.Fatalf("StructureValues calls = %d, want one folder-label projection", valuesCalls)
	}
}

func TestStructureSnapshotResolvesValueRootBeforeSelectedRowBound(t *testing.T) {
	for _, root := range []string{"PROJ-1", "Selected root"} {
		t.Run(root, func(t *testing.T) {
			tracker := &valueRootTracker{}
			svc := &JiraService{tr: tracker, structure: valueRootStructureReader{}}

			got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
				Attributes:  []string{"key", "summary"},
				Root:        root,
				MaxRows:     2,
				MaxScanRows: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.RowCount != 2 || got.Rows[0].Values["key"] != "PROJ-1" || got.Rows[1].Values["key"] != "PROJ-2" {
				t.Fatalf("selected rows = %+v, want the two-row value-selected subtree", got.Rows)
			}
			if tracker.searchCalls != 1 {
				t.Fatalf("Search calls = %d, want one bounded value expansion", tracker.searchCalls)
			}
		})
	}
}

func TestStructureSnapshotIdentityRootExpandsOnlySelectedIssueIDs(t *testing.T) {
	tracker := &valueRootTracker{}
	svc := &JiraService{tr: tracker, structure: valueRootStructureReader{}}

	got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:  []string{"key"},
		Root:        "10001",
		MaxRows:     2,
		MaxScanRows: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 2 || tracker.searchCalls != 1 || len(tracker.queries) != 1 {
		t.Fatalf("result rows=%d Search calls=%d queries=%v", got.RowCount, tracker.searchCalls, tracker.queries)
	}
	if strings.Contains(tracker.queries[0], "10003") {
		t.Fatalf("identity-selected issue query includes unselected id: %s", tracker.queries[0])
	}
}

func TestStructureSnapshotValueRootScansBeyondSelectedRowBound(t *testing.T) {
	tracker := &valueRootTracker{}
	svc := &JiraService{tr: tracker, structure: valueRootStructureReader{}}

	got, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:  []string{"key"},
		Root:        "PROJ-3",
		MaxRows:     1,
		MaxScanRows: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 1 || got.Rows[0].Values["key"] != "PROJ-3" {
		t.Fatalf("selected rows = %+v, want the last scan-bounded root", got.Rows)
	}
}

func TestStructureSnapshotEnforcesSelectedRowBoundAfterValueRootResolution(t *testing.T) {
	tracker := &valueRootTracker{}
	svc := &JiraService{tr: tracker, structure: valueRootStructureReader{}}

	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:  []string{"key"},
		Root:        "PROJ-1",
		MaxRows:     1,
		MaxScanRows: 3,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "exceeds max rows") {
		t.Fatalf("error = %v, want selected-row check failure", err)
	}
	if tracker.searchCalls != 1 {
		t.Fatalf("Search calls = %d, want one bounded value expansion", tracker.searchCalls)
	}
}

func TestStructureSnapshotEnforcesScanBoundBeforeValueRootExpansion(t *testing.T) {
	tracker := &valueRootTracker{}
	svc := &JiraService{tr: tracker, structure: valueRootStructureReader{}}

	_, err := svc.StructureSnapshot(t.Context(), 1, StructureSnapshotOpts{
		Attributes:  []string{"key"},
		Root:        "PROJ-1",
		MaxRows:     2,
		MaxScanRows: 2,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "exceeds max scan rows") {
		t.Fatalf("error = %v, want scan-bound check failure", err)
	}
	if tracker.searchCalls != 0 {
		t.Fatalf("Search calls = %d, want none before scan bound", tracker.searchCalls)
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

func TestStructureFolderJSONPreservesEmptyIdentityFields(t *testing.T) {
	folders := buildStructureFolders(
		[]domain.StructureRow{{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "f-root"}},
		map[int64]string{},
	)
	encoded, err := json.Marshal(folders)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name           *string `json:"name"`
		ParentFolderID *string `json:"parent_folder_id"`
		Path           []string
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name == nil || *got[0].Name != "" ||
		got[0].ParentFolderID == nil || *got[0].ParentFolderID != "" ||
		len(got[0].Path) != 1 || got[0].Path[0] != "folder:f-root" {
		t.Fatalf("folders=%s", encoded)
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

func TestSelectStructureFolderSelectionErrorsAreTypedAndPreserveMessages(t *testing.T) {
	staleRows := []domain.StructureRow{
		{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "f-root"},
		{RowID: 11, Depth: 1, ItemType: "folder", ItemID: "f-child"},
		{RowID: 12, Depth: 2, ItemType: "issue", ItemID: "10001"},
		{RowID: 20, Depth: 0, ItemType: "folder", ItemID: "f-other"},
		{RowID: 21, Depth: 1, ItemType: "folder", ItemID: "f-child-2"},
	}
	staleFolders := buildStructureFolders(staleRows, map[int64]string{10: "Plans", 11: "Quarter", 20: "Archive", 21: "Quarter"})
	ambiguousRows := []domain.StructureRow{
		{RowID: 10, ItemType: "folder", ItemID: "same"},
		{RowID: 20, ItemType: "folder", ItemID: "same"},
	}
	ambiguousFolders := buildStructureFolders(ambiguousRows, map[int64]string{10: "A", 20: "B"})

	for _, test := range []struct {
		name               string
		rows               []domain.StructureRow
		folders            []StructureFolder
		complete           bool
		selector           StructureFolderSelector
		reason             StructureFolderSelectionReason
		matches, available int
		sentinel, other    error
		message            string
	}{
		{
			name: "stale selector", rows: staleRows, folders: staleFolders, complete: true,
			selector: StructureFolderSelector{FolderID: "f-missing"},
			reason:   StructureFolderSelectionNotFound, matches: 0, available: 4,
			sentinel: domain.ErrNotFound, other: domain.ErrCheckFailed,
			message: "not found: exact Structure folder was not found",
		},
		{
			name: "ambiguous selector", rows: ambiguousRows, folders: ambiguousFolders, complete: true,
			selector: StructureFolderSelector{FolderID: "same"},
			reason:   StructureFolderSelectionAmbiguous, matches: 2, available: 2,
			sentinel: domain.ErrCheckFailed, other: domain.ErrNotFound,
			message: "check failed: exact Structure folder selector is ambiguous: folder=same row=10, folder=same row=20",
		},
		{
			name: "incomplete labels", rows: staleRows, folders: staleFolders, complete: false,
			selector: StructureFolderSelector{FolderPath: "Plans/Quarter"},
			reason:   StructureFolderSelectionLabelsIncomplete, matches: 0, available: 4,
			sentinel: domain.ErrCheckFailed, other: domain.ErrNotFound,
			message: "check failed: exact folder path cannot be validated because folder labels are incomplete; use --folder-id or --folder-row",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := selectStructureFolder(test.rows, test.folders, test.complete, test.selector)
			var selection *StructureFolderSelectionError
			if !errors.As(err, &selection) {
				t.Fatalf("error = %#v, want *StructureFolderSelectionError", err)
			}
			if selection.Reason != test.reason || selection.Matches != test.matches || selection.Available != test.available {
				t.Fatalf("selection=%+v", selection)
			}
			if !errors.Is(err, test.sentinel) || errors.Is(err, test.other) {
				t.Fatalf("sentinel mapping lost: %v", err)
			}
			if err.Error() != test.message {
				t.Fatalf("message=%q want %q", err.Error(), test.message)
			}
		})
	}
}

func TestSelectStructureFolderKeepsUnrelatedFailuresUntyped(t *testing.T) {
	rows := []domain.StructureRow{
		{RowID: 10, Depth: 0, ItemType: "folder", ItemID: "root"},
		{RowID: 13, Depth: 1, ItemType: "issue", ItemID: "10001"},
	}
	folders := buildStructureFolders(rows, map[int64]string{10: "Plans"})
	for _, test := range []struct {
		name     string
		selector StructureFolderSelector
		sentinel error
		message  string
	}{
		{
			name: "non-folder row", selector: StructureFolderSelector{FolderRow: 13},
			sentinel: domain.ErrUsage, message: "usage error: Structure row 13 is not a stored folder",
		},
		{
			name: "empty path segment", selector: StructureFolderSelector{FolderPath: "Plans//Quarter"},
			sentinel: domain.ErrUsage, message: "usage error: --folder-path contains an empty segment",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := selectStructureFolder(rows, folders, true, test.selector)
			var selection *StructureFolderSelectionError
			if errors.As(err, &selection) {
				t.Fatalf("unrelated failure must not be a selection error: %#v", selection)
			}
			if !errors.Is(err, test.sentinel) || err.Error() != test.message {
				t.Fatalf("err=%v", err)
			}
		})
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
