package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJiraStructureGetCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Release plan","readOnly":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "get", "123")
	if code != exitOK {
		t.Fatalf("structure get: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.ID != 123 || got.Name != "Release plan" || !got.ReadOnly {
		t.Fatalf("output = %+v, want structure metadata", got)
	}
	if !strings.Contains(js.requests()[0].query, "withPermissions=true") {
		t.Errorf("query = %q, want withPermissions=true", js.requests()[0].query)
	}
}

func TestJiraStructureRowsCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,101:1:10002",
		"version":{"signature":55,"version":7}
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123")
	if code != exitOK {
		t.Fatalf("structure rows: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		StructureID int64 `json:"structure_id"`
		Rows        []struct {
			RowID       int64  `json:"row_id"`
			ParentRowID int64  `json:"parent_row_id"`
			ItemType    string `json:"item_type"`
			ItemID      string `json:"item_id"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.StructureID != 123 || len(got.Rows) != 2 || got.Rows[1].ParentRowID != 100 || got.Rows[1].ItemType != "issue" {
		t.Fatalf("rows output = %+v, want parsed issue hierarchy", got)
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123", "-o", "id")
	if code != exitOK {
		t.Fatalf("structure rows -o id: exit %d, want 0 (stdout=%q)", code, idOut)
	}
	if idOut != "100\n101\n" {
		t.Fatalf("id output = %q, want row ids", idOut)
	}
}

func TestJiraStructureRowsCLIFiltersRootByValues(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:1/root-a,101:1:10001,102:2:10002,200:0:1/root-b,201:1:20001",
		"itemTypes":{"1":"folder"},
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{
		"responses":[{"rows":[100,101,102,200,201],"data":[{"summary":"Release root"},{"summary":"Child A"},{"summary":"Child B"},{"summary":"Other root"},{"summary":"Other child"}]}],
		"inaccessibleRows":[]
	}`)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123", "--root", "Release root")
	if code != exitOK {
		t.Fatalf("structure rows --root: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		Rows []struct {
			RowID int64 `json:"row_id"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if len(got.Rows) != 3 || got.Rows[0].RowID != 100 || got.Rows[2].RowID != 102 {
		t.Fatalf("filtered rows = %+v, want rows 100,101,102", got.Rows)
	}
}

func TestJiraStructureRowsCLIFiltersIssueRootByStableItemIDJoin(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,101:1:10002,200:0:20001",
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"id":"10001","key":"PROJ-1","fields":{"summary":"First root"}},
			{"id":"10002","key":"PROJ-2","fields":{"summary":"Child"}},
			{"id":"20001","key":"PROJ-3","fields":{"summary":"Second root"}}
		],
		"startAt":0,"maxResults":50,"total":3
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123", "--root", "Second root")
	if code != exitOK {
		t.Fatalf("structure rows issue root: exit %d (stdout=%q)", code, out)
	}
	var got struct {
		Rows []struct {
			RowID int64 `json:"row_id"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Rows) != 1 || got.Rows[0].RowID != 200 {
		t.Fatalf("rows=%+v err=%v", got.Rows, err)
	}
}

func TestJiraStructurePullIssuesCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:1/root-a,101:1:10001,102:1:10002,103:1:2/generator",
		"itemTypes":{"1":"folder","2":"generator"},
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"id":"10001","key":"PROJ-1","fields":{"summary":"First"}},
			{"id":"10002","key":"PROJ-2","fields":{"summary":"Second"}}
		],
		"startAt":0,
		"maxResults":50,
		"total":2
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "pull-issues", "123", "--fields", "summary")
	if code != exitOK {
		t.Fatalf("structure pull-issues: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		IssueIDs []string `json:"issue_ids"`
		Issues   []struct {
			Key string `json:"key"`
		} `json:"issues"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if strings.Join(got.IssueIDs, ",") != "10001,10002" || got.Count != 2 || got.Issues[1].Key != "PROJ-2" {
		t.Fatalf("pull result = %+v, want two issue snapshots", got)
	}
	var sawIDJQL bool
	for _, req := range js.requests() {
		if req.path == "/rest/api/2/search" && strings.Contains(req.query, "id+in+%2810001%2C10002%29") {
			sawIDJQL = true
		}
	}
	if !sawIDJQL {
		t.Fatalf("requests = %+v, want generated id in (...) search", js.requests())
	}
}

func TestJiraStructureExportCLIWritesCSV(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Release plan","readOnly":true}`)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,101:1:1/folder-a",
		"itemTypes":{"1":"folder"},
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{
		"responses":[{"rows":[100,101],"forestVersion":{"signature":55,"version":7},"itemsVersion":{"signature":9,"version":2},"data":[
			{"attribute":{"id":"key","format":"text"},"values":["PROJ-1",null]},
			{"attribute":{"id":"summary","format":"text"},"values":["WRONG ROW VALUE","+Folder"]},
			{"attribute":{"id":"status","format":"text"},"values":["Open",null]}
		]}],
		"inaccessibleRows":[]
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"=HYPERLINK(\"https://example.invalid\")","status":{"name":"Open"}}}],
		"startAt":0,"maxResults":50,"total":1
	}`)
	outPath := filepath.Join(t.TempDir(), "structure.csv")

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "export", "123", "--format", "csv", "--out", outPath, "--fields", "summary,status")
	if code != exitOK {
		t.Fatalf("structure export: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Path       string `json:"path"`
		Format     string `json:"format"`
		RowCount   int    `json:"row_count"`
		IssueCount int    `json:"issue_count"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if res.Path != outPath || res.Format != "csv" || res.RowCount != 2 || res.IssueCount != 1 {
		t.Fatalf("export result = %+v, want path/csv/counts", res)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "row_id,depth,parent_row_id,position,item_type,item_id,accessible,summary,status") || !strings.Contains(text, "folder,folder-a,true,'+Folder") {
		t.Fatalf("csv export = %q, want normalized header and non-issue row values", text)
	}
	if !strings.Contains(text, "'=HYPERLINK") {
		t.Fatalf("csv export did not neutralize formula: %q", text)
	}
	if strings.Contains(text, "WRONG ROW VALUE") {
		t.Fatalf("issue fields were joined through ephemeral Structure row ids: %q", text)
	}

	rawPath := filepath.Join(t.TempDir(), "structure-raw.csv")
	_, code = runCLI(t, jiraEnv(js.srv), "jira", "structure", "export", "123", "--format", "csv", "--out", rawPath, "--fields", "summary,status", "--raw-csv")
	if code != exitOK {
		t.Fatalf("raw structure export exit %d", code)
	}
	raw, err := os.ReadFile(rawPath)
	if err != nil || strings.Contains(string(raw), "'=HYPERLINK") || !strings.Contains(string(raw), "=HYPERLINK") {
		t.Fatalf("raw csv = %q err=%v", raw, err)
	}
}

func TestJiraStructureViewCLIEmitsNormalizedMarkdownAndIDs(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Quarter plan"}`)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:1/root,101:1:10001",
		"itemTypes":{"1":"folder"},
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{
		"responses":[{"rows":[100,101],"forestVersion":{"signature":55,"version":7},"itemsVersion":{"signature":9,"version":2},"data":[
			{"attribute":{"id":"key","format":"text"},"values":[null,"PROJ-1"]},
			{"attribute":{"id":"summary","format":"text"},"values":["Planning | root","First issue"]},
			{"attribute":{"id":"status","format":"text"},"values":[null,"Open"]},
			{"attribute":{"id":"assignee","format":"text"},"values":[null,"owner"]},
			{"attribute":{"id":"priority","format":"text"},"values":[null,"High"]},
			{"attribute":{"id":"issuetype","format":"text"},"values":[null,"Story"]}
		]}]
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"First issue","status":{"name":"Open"},"assignee":{"displayName":"Owner"},"priority":{"name":"High"},"issuetype":{"name":"Story"}}}],
		"startAt":0,"maxResults":50,"total":1
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "view", "123", "-o", "text")
	if code != exitOK {
		t.Fatalf("structure view text: exit %d, want 0 (stdout=%q)", code, out)
	}
	for _, want := range []string{"browser saved-view columns are not reproduced", "| Row | Tree | Type | Accessible |", `Planning \| root`, "↳ PROJ-1 — First issue"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Markdown missing %q:\n%s", want, out)
		}
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "view", "123", "-o", "id")
	if code != exitOK || idOut != "100\n101\n" {
		t.Fatalf("structure view ids: exit=%d output=%q", code, idOut)
	}
}

func TestJiraStructureExportMarksExplicitPermissionGaps(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Quarter plan"}`)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,101:0:10002",
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{
		"responses":[{"rows":[100],"forestVersion":{"signature":55,"version":7},"data":[
			{"attribute":{"id":"key","format":"text"},"values":["PROJ-1"]},
			{"attribute":{"id":"summary","format":"text"},"values":["Visible"]}
		]}],
		"inaccessibleRows":[101]
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"id":"10001","key":"PROJ-1","fields":{"summary":"Visible"}}],
		"startAt":0,"maxResults":50,"total":1
	}`)
	outPath := filepath.Join(t.TempDir(), "partial.jsonl")

	_, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "export", "123", "--format", "jsonl", "--out", outPath, "--fields", "summary")
	if code != exitOK {
		t.Fatalf("partial export exit=%d, want %d", code, exitOK)
	}
	data, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(data), `"complete":false`) || !strings.Contains(string(data), `"accessible":false`) {
		t.Fatalf("partial JSONL=%q err=%v", data, err)
	}
}

func TestJiraStructureViewCountsIssuesAfterRootFiltering(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Plan"}`)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,200:0:20001",
		"version":{"signature":55,"version":7}
	}`)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"id":"10001","key":"PROJ-1","fields":{"summary":"First"}},
			{"id":"20001","key":"PROJ-2","fields":{"summary":"Second"}}
		],
		"startAt":0,"maxResults":50,"total":2
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "view", "123", "--root", "Second")
	if code != exitOK {
		t.Fatalf("structure view root: exit=%d output=%q", code, out)
	}
	var got struct {
		RowCount   int `json:"row_count"`
		IssueCount int `json:"issue_count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.RowCount != 1 || got.IssueCount != 1 {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
}

func TestJiraStructureValuesCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{"responses":[{"rows":[100],"data":[]}],"inaccessibleRows":[]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "values", "123", "--rows", "100", "--fields", "key,summary")
	if code != exitOK {
		t.Fatalf("structure values: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"responses"`) {
		t.Fatalf("values output = %q, want responses", out)
	}
	if !strings.Contains(out, `"inaccessible_rows": []`) {
		t.Fatalf("values output = %q, want explicit empty inaccessible_rows", out)
	}
	reqs := js.writeReqsTo("/rest/structure/2.0/value")
	if len(reqs) != 1 {
		t.Fatalf("writes = %d, want one value POST", len(reqs))
	}
	var payload struct {
		Requests []struct {
			Rows       []int64 `json:"rows"`
			Attributes []struct {
				ID string `json:"id"`
			} `json:"attributes"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(reqs[0].body), &payload); err != nil {
		t.Fatalf("decode value request: %v", err)
	}
	if len(payload.Requests) != 1 || len(payload.Requests[0].Rows) != 1 || payload.Requests[0].Rows[0] != 100 {
		t.Fatalf("rows payload = %+v, want row 100", payload)
	}
	if len(payload.Requests[0].Attributes) != 2 || payload.Requests[0].Attributes[0].ID != "key" || payload.Requests[0].Attributes[1].ID != "summary" {
		t.Fatalf("attributes payload = %+v, want key,summary", payload.Requests[0].Attributes)
	}
}

func TestJiraStructureRejectsBadIDsBeforeNetwork(t *testing.T) {
	js := newJiraServer(t)

	_, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "get", "nope")
	if code != exitUsage {
		t.Fatalf("bad structure id exit = %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("sent %d requests, want none", len(js.requests()))
	}
}
