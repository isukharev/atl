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
