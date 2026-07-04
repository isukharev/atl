package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJiraPlanningReportCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"key":"PROJ-1","fields":{"summary":"Parent","description":"Context https://docs.example.com/spec","issuetype":{"name":"Epic"},"assignee":{"displayName":"Alice"},"estimate":8}},
			{"key":"PROJ-2","fields":{"summary":"Child","description":"","issuetype":{"name":"Story"},"epic":"PROJ-1"}}
		],
		"startAt":0,
		"maxResults":50,
		"total":2
	}`)
	csvPath := filepath.Join(t.TempDir(), "planning.csv")

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "planning", "report", "--jql", "project=PROJ", "--estimate-field", "estimate", "--epic-field", "epic", "--csv", csvPath)
	if code != exitOK {
		t.Fatalf("planning report: exit %d, want 0 (stdout=%q)", code, out)
	}
	var report struct {
		Count  int `json:"count"`
		Issues []struct {
			Key      string   `json:"key"`
			Children []string `json:"children"`
			Gaps     []string `json:"gaps"`
			Refs     []struct {
				Kind string `json:"kind"`
			} `json:"refs"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if report.Count != 2 || len(report.Issues) != 2 {
		t.Fatalf("report = %+v, want two issues", report)
	}
	if strings.Join(report.Issues[0].Children, ",") != "PROJ-2" || len(report.Issues[0].Refs) != 1 || report.Issues[0].Refs[0].Kind != "doc" {
		t.Fatalf("epic issue = %+v, want child and doc ref", report.Issues[0])
	}
	if !strings.Contains(strings.Join(report.Issues[1].Gaps, ","), "missing_description") {
		t.Fatalf("child gaps = %+v, want missing_description", report.Issues[1].Gaps)
	}
	if b, err := os.ReadFile(csvPath); err != nil || !strings.Contains(string(b), "PROJ-1") {
		t.Fatalf("csv not written/useful: bytes=%q err=%v", b, err)
	}
}

func TestJiraPlanningReportRequiresJQLBeforeNetwork(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "planning", "report")
	if code != exitUsage {
		t.Fatalf("missing --jql exit = %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("sent %d requests, want none", len(js.requests()))
	}
}

func TestJiraIssueRefsCLIForKeyAndJQL(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK, `{
		"key":"PROJ-1",
		"fields":{
			"summary":"First",
			"description":"Spec https://docs.example.com/spec",
			"issuetype":{"name":"Story"},
			"comment":{"comments":[{"body":"Design https://figma.com/file/abc"}]}
		}
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "refs", "PROJ-1")
	if code != exitOK {
		t.Fatalf("issue refs key: exit %d, want 0 (stdout=%q)", code, out)
	}
	var one struct {
		Count  int `json:"count"`
		Issues []struct {
			Key  string `json:"key"`
			Refs []struct {
				Kind string `json:"kind"`
				URL  string `json:"url"`
			} `json:"refs"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatalf("decode refs: %v\n%s", err, out)
	}
	if one.Count != 1 || one.Issues[0].Key != "PROJ-1" || len(one.Issues[0].Refs) != 2 {
		t.Fatalf("refs = %+v, want two refs for PROJ-1", one)
	}

	js = newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[{"key":"PROJ-2","fields":{"summary":"Second","description":"Doc https://docs.example.com/other","issuetype":{"name":"Task"}}}],
		"startAt":0,
		"maxResults":50,
		"total":1
	}`)
	out, code = runCLI(t, jiraEnv(js.srv), "jira", "issue", "refs", "--jql", "project=PROJ")
	if code != exitOK {
		t.Fatalf("issue refs jql: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"key": "PROJ-2"`) || !strings.Contains(js.requests()[0].query, "project%3DPROJ") {
		t.Fatalf("refs jql output/query = %q / %+v, want PROJ-2 and encoded JQL", out, js.requests())
	}
}

func TestJiraIssueTreeCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, `{
		"issues":[
			{"key":"PROJ-1","fields":{"summary":"Parent","issuetype":{"name":"Epic"}}},
			{"key":"PROJ-2","fields":{"summary":"Child","issuetype":{"name":"Story"},"epic":"PROJ-1"}},
			{"key":"PROJ-3","fields":{"summary":"External child","issuetype":{"name":"Story"},"epic":"PROJ-X"}},
			{"key":"PROJ-4","fields":{"summary":"Orphan","issuetype":{"name":"Task"}}}
		],
		"startAt":0,
		"maxResults":50,
		"total":4
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "tree", "--jql", "project=PROJ", "--epic-field", "epic")
	if code != exitOK {
		t.Fatalf("issue tree: exit %d, want 0 (stdout=%q)", code, out)
	}
	var tree struct {
		Count int `json:"count"`
		Epics []struct {
			Key      string `json:"key"`
			Children []struct {
				Key string `json:"key"`
			} `json:"children"`
		} `json:"epics"`
		ExternalEpics []struct {
			Key      string `json:"key"`
			External bool   `json:"external"`
			Children []struct {
				Key string `json:"key"`
			} `json:"children"`
		} `json:"external_epics"`
		Orphans []struct {
			Key string `json:"key"`
		} `json:"orphans"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("decode tree: %v\n%s", err, out)
	}
	if tree.Count != 4 || tree.Epics[0].Key != "PROJ-1" || tree.Epics[0].Children[0].Key != "PROJ-2" {
		t.Fatalf("tree epics = %+v, want PROJ-1 -> PROJ-2", tree.Epics)
	}
	if tree.ExternalEpics[0].Key != "PROJ-X" || !tree.ExternalEpics[0].External || tree.ExternalEpics[0].Children[0].Key != "PROJ-3" {
		t.Fatalf("external epics = %+v, want PROJ-X -> PROJ-3", tree.ExternalEpics)
	}
	if tree.Orphans[0].Key != "PROJ-4" {
		t.Fatalf("orphans = %+v, want PROJ-4", tree.Orphans)
	}

	text, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "tree", "--jql", "project=PROJ", "--epic-field", "epic", "-o", "text")
	if code != exitOK {
		t.Fatalf("issue tree text: exit %d, want 0 (stdout=%q)", code, text)
	}
	if !strings.Contains(text, "epics") || !strings.Contains(text, "external_epics") || !strings.Contains(text, "orphans") {
		t.Fatalf("text tree = %q, want section labels", text)
	}
}
