package cli

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestJiraExportCLIWritesArtifactAndManifest(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"First","project":{"key":"PROJ"},"customfield_10001":"team-a"}}],
		"startAt":0,
		"maxResults":50,
		"total":1
	}`)
	outPath := filepath.Join(t.TempDir(), "issues.json")

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "export", "--jql", "project=PROJ", "--out", outPath, "--format", "json", "--fields", "customfield_10001")
	if code != exitOK {
		t.Fatalf("jira export: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Path         string `json:"path"`
		ManifestPath string `json:"manifest_path"`
		Format       string `json:"format"`
		Count        int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if res.Path != outPath || res.ManifestPath != outPath+".manifest.json" || res.Format != "json" || res.Count != 1 {
		t.Fatalf("result = %+v, want export paths/count", res)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.Contains(string(data), `"key": "PROJ-1"`) || !strings.Contains(string(data), `"manifest"`) {
		t.Fatalf("export data = %s, want manifest and issue", data)
	}
	mb, err := os.ReadFile(outPath + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(mb), js.srv.URL) || strings.Contains(string(mb), "127.0.0.1") {
		t.Fatalf("manifest leaked backend URL:\n%s", mb)
	}
	if !strings.Contains(string(mb), `"url_hash": "sha256:`) {
		t.Fatalf("manifest missing backend hash:\n%s", mb)
	}
}

func TestJiraExportCLICSVFormulaSafetyAndRawEscapeHatch(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"=1+1"}}],
		"startAt":0,"total":1
	}`)
	for _, tc := range []struct {
		name string
		raw  bool
		want string
	}{{"safe", false, "'=1+1"}, {"raw", true, "=1+1"}} {
		path := filepath.Join(t.TempDir(), tc.name+".csv")
		args := []string{"jira", "export", "--jql", "project=PROJ", "--out", path, "--format", "csv"}
		if tc.raw {
			args = append(args, "--raw-csv")
		}
		if _, code := runCLI(t, jiraEnv(js.srv), args...); code != exitOK {
			t.Fatalf("%s CSV exit=%d", tc.name, code)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		records, err := csv.NewReader(f).ReadAll()
		_ = f.Close()
		if err != nil || len(records) != 2 || records[1][2] != tc.want {
			t.Fatalf("%s records=%#v error=%v", tc.name, records, err)
		}
	}
}

func TestJiraExportCLIRequiresOut(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "export", "--jql", "project=PROJ")
	if code != exitUsage {
		t.Fatalf("missing --out exit = %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("sent %d requests, want none", len(js.requests()))
	}
}

func TestJiraExportCLIGeneratesKeyBatches(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"First"}}],
		"startAt":0,
		"maxResults":50,
		"total":1
	}`)
	outPath := filepath.Join(t.TempDir(), "issues.jsonl")

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "export", "--keys", "PROJ-1,PROJ-2", "--batch-size", "1", "--out", outPath)
	if code != exitOK {
		t.Fatalf("jira export --keys: exit %d, want 0", code)
	}
	reqs := js.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 generated batches: %+v", len(reqs), reqs)
	}
	if !strings.Contains(reqs[0].query, "key+in+%28%22PROJ-1%22%29") ||
		!strings.Contains(reqs[1].query, "key+in+%28%22PROJ-2%22%29") {
		t.Fatalf("queries = %q / %q, want one key per generated batch", reqs[0].query, reqs[1].query)
	}
}

func TestJiraExportCLIStreamsArtifactOnlyToStdout(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK,
		`[{"id":"customfield_10001","name":"Delivery Notes","custom":true,"schema":{"type":"string"}}]`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"id":"10001","key":"PROJ-1","fields":{"summary":"=formula","customfield_10001":"ready"}},
			{"id":"10002","key":"PROJ-2","fields":{"summary":"Second","customfield_10001":"done"}}
		],"startAt":0,"maxResults":100,"total":2
	}`)
	for _, path := range []string{"-", "-.manifest.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("test requires absent sentinel path %q: %v", path, err)
		}
	}

	jsonl, code := runCLI(t, jiraEnv(js.srv), "jira", "export", "--keys", "PROJ-1,PROJ-2", "--fields", "Delivery Notes", "--format", "jsonl", "--out", "-")
	if code != exitOK || strings.Count(strings.TrimSpace(jsonl), "\n") != 1 || strings.Contains(jsonl, "manifest_path") {
		t.Fatalf("jsonl exit=%d output=%q", code, jsonl)
	}
	for i, line := range strings.Split(strings.TrimSpace(jsonl), "\n") {
		var snapshot app.JiraIssueSnapshot
		if err := json.Unmarshal([]byte(line), &snapshot); err != nil || snapshot.Key == "" {
			t.Fatalf("jsonl line %d: snapshot=%+v err=%v", i+1, snapshot, err)
		}
	}

	aggregate, code := runCLI(t, jiraEnv(js.srv), "jira", "export", "--keys", "PROJ-1,PROJ-2", "--format", "json", "--out", "-")
	var snapshots []app.JiraIssueSnapshot
	if code != exitOK || json.Unmarshal([]byte(aggregate), &snapshots) != nil || len(snapshots) != 2 || strings.Contains(aggregate, `"manifest"`) {
		t.Fatalf("json exit=%d snapshots=%+v output=%q", code, snapshots, aggregate)
	}
	assertGolden(t, "jira_export_stdout.json", []byte(aggregate))

	csvOut, code := runCLI(t, jiraEnv(js.srv), "jira", "export", "--keys", "PROJ-1,PROJ-2", "--format", "csv", "--out", "-")
	records, csvErr := csv.NewReader(strings.NewReader(csvOut)).ReadAll()
	if code != exitOK || csvErr != nil || len(records) != 3 || records[1][2] != "'=formula" {
		t.Fatalf("csv exit=%d records=%#v err=%v output=%q", code, records, csvErr, csvOut)
	}

	for _, path := range []string{"-", "-.manifest.json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("transient export created sentinel path %q: %v", path, err)
		}
	}
	var resolved bool
	for _, request := range js.requests() {
		if request.path == "/rest/api/2/search" && strings.Contains(request.query, "customfield_10001") {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("display-name field was not resolved before search: %+v", js.requests())
	}
}

func TestJiraExportDiffCLI(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jsonl")
	newPath := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(oldPath, []byte(`{"key":"PROJ-1","fields":{"summary":"old"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(`{"key":"PROJ-1","fields":{"summary":"new"}}`+"\n"+`{"key":"PROJ-2","fields":{}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	out, code := runCLI(t, nil, "jira", "export", "diff", oldPath, newPath)
	if code != exitOK {
		t.Fatalf("jira export diff: exit %d, want 0 (stdout=%q)", code, out)
	}
	var diff struct {
		Added   []string `json:"added"`
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &diff); err != nil {
		t.Fatalf("decode diff: %v\n%s", err, out)
	}
	if strings.Join(diff.Added, ",") != "PROJ-2" || strings.Join(diff.Changed, ",") != "PROJ-1" {
		t.Fatalf("diff = %+v, want added PROJ-2 changed PROJ-1", diff)
	}
}
