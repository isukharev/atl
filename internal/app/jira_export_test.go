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
