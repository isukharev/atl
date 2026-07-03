package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

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

func TestRenderJiraExportCSVUsesDeterministicHeader(t *testing.T) {
	data, err := renderJiraExport("csv", []JiraIssueSnapshot{{
		Key: "PROJ-1",
		ID:  "10001",
		Fields: map[string]any{
			"summary":           "First",
			"customfield_10001": "team-a",
			"status":            map[string]any{"name": "Open"},
		},
	}}, []string{"customfield_10001"}, JiraExportManifest{})
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
