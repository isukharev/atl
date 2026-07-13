package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type generatedExportTracker struct {
	domain.Tracker
	total int
}

type duplicateExportTracker struct{ domain.Tracker }

type failingSecondPageExportTracker struct{ domain.Tracker }

type orderedExportTracker struct{ domain.Tracker }

func (orderedExportTracker) Search(_ context.Context, jql string, _ []string, _ int, cursor string) ([]domain.Issue, string, error) {
	issue := func(id, key string) domain.Issue {
		return domain.Issue{ID: id, Key: key, Fields: map[string]any{"summary": key}}
	}
	switch jql {
	case `key in ("PROJ-3","proj-1","PROJ-4")`, `key in ("PROJ-3","PROJ-1","PROJ-4")`:
		if cursor == "" {
			return []domain.Issue{issue("4", "PROJ-4"), issue("1", "PROJ-1")}, "next", nil
		}
		return []domain.Issue{issue("3", "PROJ-3")}, "", nil
	case `key in ("PROJ-404","PROJ-2")`:
		return []domain.Issue{issue("2", "PROJ-2")}, "", nil
	case "id in (0002,1)":
		return []domain.Issue{issue("1", "PROJ-1"), issue("2", "PROJ-2")}, "", nil
	case "project=PROJ ORDER BY rank":
		return []domain.Issue{issue("2", "PROJ-2"), issue("1", "PROJ-1")}, "", nil
	default:
		return nil, "", fmt.Errorf("unexpected JQL %q", jql)
	}
}

func (failingSecondPageExportTracker) Search(_ context.Context, _ string, _ []string, _ int, cursor string) ([]domain.Issue, string, error) {
	if cursor != "" {
		return nil, "", errors.New("later page failed")
	}
	return []domain.Issue{{ID: "1", Key: "PROJ-1", Fields: map[string]any{"summary": "first"}}}, "next", nil
}

func (duplicateExportTracker) Search(_ context.Context, _ string, _ []string, _ int, _ string) ([]domain.Issue, string, error) {
	return []domain.Issue{{ID: "1", Key: "PROJ-1", Fields: map[string]any{"summary": "same"}}}, "", nil
}

func (t generatedExportTracker) Search(_ context.Context, _ string, _ []string, limit int, cursor string) ([]domain.Issue, string, error) {
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	end := start + limit
	if end > t.total {
		end = t.total
	}
	issues := make([]domain.Issue, 0, end-start)
	for i := start; i < end; i++ {
		issues = append(issues, domain.Issue{ID: strconv.Itoa(i + 1), Key: fmt.Sprintf("PROJ-%d", i+1), Fields: map[string]any{"summary": fmt.Sprintf("Issue %d", i+1)}})
	}
	next := ""
	if end < t.total {
		next = strconv.Itoa(end)
	}
	return issues, next, nil
}

func TestJiraExportWritesJSONLAndSanitizedManifest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "issues.jsonl")
	svc := &JiraService{
		tr: partialTracker{issues: []domain.Issue{{
			ID:     "10001",
			Key:    "PROJ-1",
			Fields: map[string]any{"summary": "First", "customfield_10001": "team-a"},
		}}},
		baseURL: "https://jira.example.com",
	}

	res, err := svc.Export(context.Background(), JiraExportOpts{
		JQL:     "project = PROJ",
		Out:     out,
		Format:  "jsonl",
		Limit:   100,
		Fields:  []string{"customfield_10001"},
		Version: "test",
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Count != 1 || res.Path != out || res.ManifestPath != out+".manifest.json" {
		t.Fatalf("result = %+v, want paths and count", res)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("jsonl lines = %d, want 1: %q", len(lines), data)
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal([]byte(lines[0]), &snap); err != nil {
		t.Fatalf("decode jsonl: %v", err)
	}
	if snap.Key != "PROJ-1" || snap.ID != "10001" || snap.Fields["customfield_10001"] != "team-a" {
		t.Fatalf("snapshot = %+v, want identity and custom field", snap)
	}

	mb, err := os.ReadFile(out + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(mb), "jira.example.com") {
		t.Fatalf("manifest leaked backend hostname:\n%s", mb)
	}
	var manifest JiraExportManifest
	if err := json.Unmarshal(mb, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Backend.Service != "jira" || !strings.HasPrefix(manifest.Backend.URLHash, "sha256:") {
		t.Fatalf("backend manifest = %+v, want jira hash without URL", manifest.Backend)
	}
	if manifest.Count != 1 || manifest.Format != "jsonl" || manifest.JQL != "project = PROJ" {
		t.Fatalf("manifest = %+v, want count/format/jql", manifest)
	}
	if manifest.RowOrder != "backend" || manifest.MissingIdentityBehavior != "" {
		t.Fatalf("manifest ordering = %+v, want backend JQL order", manifest)
	}
}

func TestJiraExportTransientRequiresExplicitWriter(t *testing.T) {
	_, err := (&JiraService{tr: partialTracker{}}).Export(context.Background(), JiraExportOpts{JQL: "project = PROJ", Out: "-"})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v, want ErrUsage", err)
	}
}

func TestJiraExportTransientReturnsErrorAfterWrittenPrefix(t *testing.T) {
	var out bytes.Buffer
	_, err := (&JiraService{tr: failingSecondPageExportTracker{}}).Export(context.Background(), JiraExportOpts{
		JQL: "project = PROJ", Out: "-", Format: "jsonl", Writer: &out,
	})
	if err == nil || !strings.Contains(out.String(), `"key":"PROJ-1"`) {
		t.Fatalf("err=%v output=%q", err, out.String())
	}
}

func TestRenderJiraExportCSVUsesDeterministicHeader(t *testing.T) {
	data, err := renderJiraExport("csv", []JiraIssueSnapshot{{
		Key: "PROJ-1",
		ID:  "10001",
		Fields: map[string]any{
			"summary":           "First",
			"customfield_10001": "team-a",
			"status":            map[string]any{"name": "Open"},
		},
	}}, []string{"customfield_10001"}, JiraExportManifest{}, false, false)
	if err != nil {
		t.Fatalf("render csv: %v", err)
	}
	got := string(data)
	wantHeader := "key,id,summary,status,issuetype,project,customfield_10001\n"
	if !strings.HasPrefix(got, wantHeader) {
		t.Fatalf("csv = %q, want header %q", got, wantHeader)
	}
	if !strings.Contains(got, `PROJ-1,10001,First,"{""name"":""Open""}",,`) {
		t.Fatalf("csv row did not render scalar/nested fields deterministically: %q", got)
	}
}

func TestRenderJiraExportCSVNeutralizesFormulasUnlessRaw(t *testing.T) {
	issues := []JiraIssueSnapshot{{Key: "PROJ-1", ID: "1", Fields: map[string]any{"summary": "=1+1", "status": "-2"}}}
	safe, err := renderJiraExportCSV(issues, []string{"summary", "status"}, false)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(safe)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[1][2] != "'=1+1" || records[1][3] != "'-2" {
		t.Fatalf("safe CSV row = %#v", records[1])
	}
	raw, err := renderJiraExportCSV(issues, []string{"summary", "status"}, true)
	if err != nil {
		t.Fatal(err)
	}
	records, _ = csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if records[1][2] != "=1+1" || records[1][3] != "-2" {
		t.Fatalf("raw CSV row = %#v", records[1])
	}
}

func TestJiraRowExportsStreamLargeSelection(t *testing.T) {
	for _, format := range []string{"jsonl", "csv"} {
		out := filepath.Join(t.TempDir(), "issues."+format)
		res, err := (&JiraService{tr: generatedExportTracker{total: 2500}}).Export(context.Background(), JiraExportOpts{
			JQL: "project = PROJ", Out: out, Format: format, Limit: 0,
		})
		if err != nil {
			t.Fatalf("%s export: %v", format, err)
		}
		if res.Count != 2500 {
			t.Fatalf("%s count=%d", format, res.Count)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Count(string(data), "\n")
		want := 2500
		if format == "csv" {
			want++ // header
		}
		if lines != want {
			t.Fatalf("%s lines=%d want=%d", format, lines, want)
		}
	}
}

func TestJiraAggregateJSONEnforcesIssueCap(t *testing.T) {
	out := filepath.Join(t.TempDir(), "issues.json")
	_, err := (&JiraService{tr: generatedExportTracker{total: jiraAggregateExportMaxIssues + 1}}).Export(context.Background(), JiraExportOpts{
		JQL: "project = PROJ", Out: out, Format: "json", Limit: 0,
	})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("aggregate cap error=%v, want ErrUsage", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("capped aggregate left output: %v", statErr)
	}
}

func TestJiraAggregateJSONEnforcesSerializedByteCap(t *testing.T) {
	tracker := partialTracker{issues: []domain.Issue{{ID: "1", Key: "PROJ-1", Fields: map[string]any{"summary": strings.Repeat("x", 200)}}}}
	_, err := (&JiraService{tr: tracker}).collectAggregateExportIssues(context.Background(), []string{"project=PROJ"}, nil, 1, 100)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("byte cap error=%v, want ErrUsage", err)
	}
}

func TestJiraStreamingExportDeduplicatesIdentityAcrossQueries(t *testing.T) {
	var snapshots []JiraIssueSnapshot
	count, err := (&JiraService{tr: duplicateExportTracker{}}).forEachExportIssue(context.Background(), []string{"first", "overlap"}, nil, 0, func(snapshot JiraIssueSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	})
	if err != nil || count != 1 || len(snapshots) != 1 {
		t.Fatalf("count=%d snapshots=%d error=%v", count, len(snapshots), err)
	}
	queries, _, err := exportQueries(JiraExportOpts{Keys: []string{"PROJ-1,proj-1"}})
	if err != nil || len(queries) != 1 || strings.Contains(queries[0], "proj-1") {
		t.Fatalf("case-overlap queries=%v error=%v", queries, err)
	}
}

func TestJiraExplicitKeyExportPreservesSelectorOrderAcrossFormatsAndBatches(t *testing.T) {
	want := "PROJ-3,PROJ-1,PROJ-4,PROJ-2"
	for _, format := range []string{"jsonl", "json", "csv"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			_, err := (&JiraService{tr: orderedExportTracker{}}).Export(context.Background(), JiraExportOpts{
				Keys:      []string{"PROJ-3,proj-1,PROJ-3,PROJ-4,PROJ-404,PROJ-2"},
				BatchSize: 3,
				Out:       "-",
				Format:    format,
				Limit:     0,
				Writer:    &out,
			})
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if got := exportedKeys(t, format, out.Bytes()); strings.Join(got, ",") != want {
				t.Fatalf("keys=%v, want %s\n%s", got, want, out.String())
			}
		})
	}
}

func TestJiraExplicitIDExportUsesNumericFirstOccurrenceOrder(t *testing.T) {
	var out bytes.Buffer
	_, err := (&JiraService{tr: orderedExportTracker{}}).Export(context.Background(), JiraExportOpts{
		IDs: []string{"0002,2,1"}, Out: "-", Format: "json", Writer: &out,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var snapshots []JiraIssueSnapshot
	if err := json.Unmarshal(out.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].ID != "2" || snapshots[1].ID != "1" {
		t.Fatalf("snapshots=%+v, want ids 2,1", snapshots)
	}
}

func TestJiraJQLExportRetainsBackendOrder(t *testing.T) {
	var out bytes.Buffer
	_, err := (&JiraService{tr: orderedExportTracker{}}).Export(context.Background(), JiraExportOpts{
		JQL: "project=PROJ ORDER BY rank", Out: "-", Format: "json", Writer: &out,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got := exportedKeys(t, "json", out.Bytes()); strings.Join(got, ",") != "PROJ-2,PROJ-1" {
		t.Fatalf("keys=%v, want backend order", got)
	}
}

func TestJiraExplicitExportManifestDeclaresOrderingAndMissingBehavior(t *testing.T) {
	out := filepath.Join(t.TempDir(), "issues.jsonl")
	res, err := (&JiraService{tr: orderedExportTracker{}}).Export(context.Background(), JiraExportOpts{
		Keys: []string{"PROJ-3,PROJ-1,PROJ-4,PROJ-404,PROJ-2"}, BatchSize: 3, Out: out, Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Manifest.RowOrder != "selector" || res.Manifest.MissingIdentityBehavior != "omit" || res.Count != 4 {
		t.Fatalf("manifest=%+v", res.Manifest)
	}
	artifact, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := exportedKeys(t, "jsonl", artifact); strings.Join(got, ",") != "PROJ-3,PROJ-1,PROJ-4,PROJ-2" {
		t.Fatalf("file keys=%v", got)
	}
	data, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"row_order": "selector"`) || !strings.Contains(string(data), `"missing_identity_behavior": "omit"`) {
		t.Fatalf("manifest file does not declare ordering behavior:\n%s", data)
	}
}

func TestJiraExplicitExportBoundsReorderBatchBytes(t *testing.T) {
	plan, err := planJiraExport(JiraExportOpts{Keys: []string{"PROJ-3"}})
	if err != nil {
		t.Fatal(err)
	}
	tracker := partialTracker{issues: []domain.Issue{{
		ID: "3", Key: "PROJ-3", Fields: map[string]any{"summary": strings.Repeat("x", 200)},
	}}}
	yielded := false
	_, err = (&JiraService{}).forEachExplicitExportIssue(context.Background(), plan, nil, 0, jiraRowExportMaxIdentities, 100, tracker.Search, func(JiraIssueSnapshot) error {
		yielded = true
		return nil
	})
	if !errors.Is(err, domain.ErrUsage) || yielded {
		t.Fatalf("error=%v yielded=%t, want pre-yield byte-cap refusal", err, yielded)
	}
}

func exportedKeys(t *testing.T, format string, data []byte) []string {
	t.Helper()
	var keys []string
	switch format {
	case "json":
		var snapshots []JiraIssueSnapshot
		if err := json.Unmarshal(data, &snapshots); err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range snapshots {
			keys = append(keys, snapshot.Key)
		}
	case "jsonl":
		for lineNo, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var snapshot JiraIssueSnapshot
			if err := json.Unmarshal([]byte(line), &snapshot); err != nil {
				t.Fatalf("line %d: %v", lineNo+1, err)
			}
			keys = append(keys, snapshot.Key)
		}
	case "csv":
		records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records[1:] {
			keys = append(keys, record[0])
		}
	default:
		t.Fatalf("unsupported format %q", format)
	}
	return keys
}

func TestJiraStreamingExportBoundsIdentityIndex(t *testing.T) {
	tracker := generatedExportTracker{total: 3}
	count, err := (&JiraService{tr: tracker}).forEachExportIssueWithIdentityCap(context.Background(), []string{"project=PROJ"}, nil, 0, 2, func(JiraIssueSnapshot) error { return nil })
	if !errors.Is(err, domain.ErrUsage) || count != 2 {
		t.Fatalf("count=%d error=%v, want two rows then bounded-memory refusal", count, err)
	}
}

func TestJiraExportRejectsNegativeLimitBeforeSearch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "issues.jsonl")
	_, err := (&JiraService{tr: generatedExportTracker{total: 1}}).Export(context.Background(), JiraExportOpts{
		JQL: "project=PROJ", Out: out, Limit: -1,
	})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("negative limit error=%v, want ErrUsage", err)
	}
}

func TestWriteUserFileStreamIsAtomicOnWriterError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("stop")
	err := writeUserFileStream(path, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("atomic writer replaced target with %q", got)
	}
}

func TestWriteUserFileStreamSyncsBeforeRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("sync failed")
	syncCalls := 0
	err := writeUserFileStreamWithSync(path, func(w io.Writer) error {
		_, err := io.WriteString(w, "complete")
		return err
	}, func(*os.File) error {
		syncCalls++
		return wantErr
	})
	if !errors.Is(err, wantErr) || syncCalls != 1 {
		t.Fatalf("error=%v syncCalls=%d", err, syncCalls)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("sync failure replaced target with %q", got)
	}
}

func TestExportQueriesBatchesIDsAndKeys(t *testing.T) {
	queries, mode, err := exportQueries(JiraExportOpts{IDs: []string{"10001,10002", "10003"}, BatchSize: 2})
	if err != nil {
		t.Fatalf("exportQueries ids: %v", err)
	}
	if mode != "ids" || strings.Join(queries, "|") != "id in (10001,10002)|id in (10003)" {
		t.Fatalf("ids mode/queries = %s %v", mode, queries)
	}

	queries, mode, err = exportQueries(JiraExportOpts{Keys: []string{"PROJ-1,PROJ-2"}, BatchSize: 1})
	if err != nil {
		t.Fatalf("exportQueries keys: %v", err)
	}
	if mode != "keys" || strings.Join(queries, "|") != `key in ("PROJ-1")|key in ("PROJ-2")` {
		t.Fatalf("keys mode/queries = %s %v", mode, queries)
	}
}

func TestDiffJiraExportsReportsAddedRemovedChanged(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jsonl")
	newPath := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(oldPath, []byte(
		`{"key":"PROJ-1","id":"10001","fields":{"summary":"same"}}`+"\n"+
			`{"key":"PROJ-2","id":"10002","fields":{"summary":"old"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(
		`{"key":"PROJ-1","id":"10001","fields":{"summary":"same"}}`+"\n"+
			`{"key":"PROJ-3","id":"10003","fields":{"summary":"new"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	diff, err := DiffJiraExports(oldPath, newPath)
	if err != nil {
		t.Fatalf("DiffJiraExports: %v", err)
	}
	if strings.Join(diff.Added, ",") != "PROJ-3" || strings.Join(diff.Removed, ",") != "PROJ-2" || len(diff.Changed) != 0 {
		t.Fatalf("diff = %+v, want added PROJ-3 removed PROJ-2", diff)
	}

	if err := os.WriteFile(newPath, []byte(
		`{"key":"PROJ-1","id":"10001","fields":{"summary":"changed"}}`+"\n"+
			`{"key":"PROJ-2","id":"10002","fields":{"summary":"old"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite new: %v", err)
	}
	diff, err = DiffJiraExports(oldPath, newPath)
	if err != nil {
		t.Fatalf("DiffJiraExports changed: %v", err)
	}
	if strings.Join(diff.Changed, ",") != "PROJ-1" || len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("changed diff = %+v, want changed PROJ-1 only", diff)
	}
}
