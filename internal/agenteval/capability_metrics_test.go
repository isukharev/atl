package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilityFamiliesAreGenericAndPrivacySafe(t *testing.T) {
	private := "SYNTHETIC-SENSITIVE-123"
	family, ok := CapabilityFamilyForCLI([]string{"jira", "epic", "digest", private, "--quarter", "2026-Q2"})
	if !ok || family != "jira.epic.digest" || strings.Contains(family, private) {
		t.Fatalf("family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_epic_digest"); !ok || family != "jira.epic.digest" {
		t.Fatalf("family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "field", "get", private, "--field", "customfield_1"}); !ok || family != "jira.issue.field" {
		t.Fatalf("CLI field family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_issue_field_get"); !ok || family != "jira.issue.field" {
		t.Fatalf("MCP field family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "refs", private}); !ok || family != "jira.issue.refs" {
		t.Fatalf("CLI refs family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_issue_refs"); !ok || family != "jira.issue.refs" {
		t.Fatalf("MCP refs family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "field", "preview", private, "--from-file", "customfield_1=value.txt"}); !ok || family != "jira.issue.field.preview" {
		t.Fatalf("CLI field preview family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "field", "set", private, "--apply"}); !ok || family != "jira.issue.field.set" {
		t.Fatalf("CLI field set family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "search", "--jql", private}); !ok || family != "jira.issue.search" {
		t.Fatalf("CLI Jira search family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"conf", "search", "--cql", private}); !ok || family != "confluence.search" {
		t.Fatalf("CLI Confluence search family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("confluence_search"); !ok || family != "confluence.search" {
		t.Fatalf("MCP Confluence search family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"conf", "diff", private, "--into", "mirror"}); !ok || family != "confluence.diff" {
		t.Fatalf("CLI Confluence diff family=%q ok=%t", family, ok)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"conf", "plan", "create", "mirror", "--out", "plan.json"}, "confluence.plan.create"},
		{[]string{"conf", "plan", "preview", "plan.json"}, "confluence.plan.preview"},
		{[]string{"conf", "plan", "apply", "plan.json", "--confirm", "APPLY"}, "confluence.plan.apply"},
		{[]string{"jira", "export", "--keys", "PROJ-1,PROJ-2", "--out", "-"}, "jira.issue.batch-read"},
		{[]string{"jira", "export", "--ids=1,2", "--out", "-"}, "jira.issue.batch-read"},
		{[]string{"jira", "export", "--jql", "project = DEMO", "--out", "-"}, "jira.export"},
		{[]string{"jira", "export", "diff", "old.jsonl", "new.jsonl"}, "jira.export.diff"},
		{[]string{"jira", "structure", "folders", "42"}, "jira.structure.folders"},
		{[]string{"jira", "structure", "rows", "42"}, "jira.structure.rows"},
		{[]string{"jira", "structure", "values", "42", "--rows", "100"}, "jira.structure.values"},
		{[]string{"jira", "issue", "worklog", "list", "PROJ-1"}, "jira.issue.worklog.list"},
		{[]string{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "30m"}, "jira.issue.worklog.add"},
		{[]string{"conf", "table", "extract", "page.csf", "--format", "json"}, "confluence.table.extract"},
		{[]string{"conf", "table", "summary", "--id", "123"}, "confluence.table.summary"},
	} {
		if family, ok := CapabilityFamilyForCLI(test.args); !ok || family != test.want {
			t.Fatalf("CLI family=%q ok=%t want=%q", family, ok, test.want)
		}
	}
	if _, ok := CapabilityFamilyForMCP("private_" + private); ok {
		t.Fatal("unknown MCP tool was attributed")
	}
	encoded, _ := json.Marshal([]CapabilityFamilyMetric{{Family: family, Invocations: 1, Successes: 1, OutputBytes: 42}})
	if strings.Contains(string(encoded), private) {
		t.Fatalf("metric leaked input: %s", encoded)
	}
}

func TestCapabilityFamilyForCLIRequiresExactBatchSelectorFlag(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "export", "--keys-file", "values.txt"},
		{"jira", "export", "--identity", "PROJ-1"},
	} {
		family, ok := CapabilityFamilyForCLI(args)
		if !ok || family != "jira.export" {
			t.Fatalf("args=%q family=%q ok=%t", args, family, ok)
		}
	}
}

func TestCapabilityFamilyValidationFailsClosed(t *testing.T) {
	for _, values := range [][]CapabilityFamilyMetric{
		{{Family: "jira.epic.digest", Invocations: 1, Successes: 0, Failures: 0}},
		{{Family: "private value", Invocations: 1, Successes: 1}},
		{{Family: "jira.fields", Invocations: 1, Successes: 1}, {Family: "jira.fields", Invocations: 1, Successes: 1}},
	} {
		if _, err := normalizeCapabilityFamilies(values); err == nil {
			t.Fatalf("accepted %+v", values)
		}
	}
}

func TestJiraIssueRefsCapabilityFamilyNormalizes(t *testing.T) {
	metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
		Family: "jira.issue.refs", Invocations: 1, Successes: 1, OutputBytes: 42,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Family != "jira.issue.refs" {
		t.Fatalf("metrics=%+v", metrics)
	}
}
