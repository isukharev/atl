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
	if family, ok := CapabilityFamilyForMCP("jira_structure_get"); !ok || family != "jira.structure.get" {
		t.Fatalf("MCP Structure metadata family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_structure_view"); !ok || family != "jira.structure.view" {
		t.Fatalf("MCP Structure view family=%q ok=%t", family, ok)
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
	if family, ok := CapabilityFamilyForMCP("confluence_table_summary"); !ok || family != "confluence.table.summary" {
		t.Fatalf("MCP Confluence table summary family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("confluence_table_extract"); !ok || family != "confluence.table.extract" {
		t.Fatalf("MCP Confluence table extract family=%q ok=%t", family, ok)
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
		{[]string{"jira", "structure", "get", "42"}, "jira.structure.get"},
		{[]string{"jira", "structure", "forest", "42"}, "jira.structure.forest"},
		{[]string{"jira", "structure", "folders", "42"}, "jira.structure.folders"},
		{[]string{"jira", "structure", "rows", "42"}, "jira.structure.rows"},
		{[]string{"jira", "structure", "values", "42", "--rows", "100"}, "jira.structure.values"},
		{[]string{"jira", "structure", "pull-issues", "42", "--root", "100"}, "jira.structure.pull-issues"},
		{[]string{"jira", "structure", "export", "42", "--format", "json", "--out", "snapshot.json"}, "jira.structure.export"},
		{[]string{"jira", "structure", "view", "snapshot.json"}, "jira.structure.view"},
		{[]string{"jira", "board", "list", "--project", "DEMO"}, "jira.board.list"},
		{[]string{"--read-only", "--config-dir", "/tmp/config", "jira", "board", "list", "--limit", "20"}, "jira.board.list"},
		{[]string{"jira", "board", "get", "42"}, "jira.board.get"},
		{[]string{"jira", "board", "config", "42"}, "jira.board.config"},
		{[]string{"jira", "board", "issues", "42", "--limit", "10"}, "jira.board.issues"},
		{[]string{"jira", "board", "backlog", "42", "--limit", "10"}, "jira.board.backlog"},
		{[]string{"jira", "board", "view", "42", "--scope", "all"}, "jira.board.view"},
		{[]string{"jira", "board", "export", "42", "--scope", "board", "--out", "snapshot.json"}, "jira.board.export"},
		{[]string{"jira", "issue", "worklog", "list", "PROJ-1"}, "jira.issue.worklog.list"},
		{[]string{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "30m"}, "jira.issue.worklog.add"},
		{[]string{"jira", "issue", "history", "PROJ-1", "--field", "status"}, "jira.issue.history"},
		{[]string{"jira", "planning", "report", "--jql", "project = DEMO"}, "jira.planning.report"},
		{[]string{"jira", "quality-report", "--jql", "project = DEMO"}, "jira.planning.report"},
		{[]string{"jira", "pull", "--jql", "project = DEMO", "--into", "mirror"}, "jira.pull"},
		{[]string{"jira", "status", "mirror", "--remote"}, "jira.status"},
		{[]string{"conf", "table", "extract", "page.csf", "--format", "json"}, "confluence.table.extract"},
		{[]string{"conf", "table", "summary", "--id", "123"}, "confluence.table.summary"},
		{[]string{"conf", "page", "meta", "--id", "123"}, "confluence.page.meta"},
		{[]string{"conf", "page", "history", "--id", "123"}, "confluence.page.history"},
		{[]string{"conf", "page", "view", "123"}, "confluence.page.view"},
		{[]string{"conf", "attachment", "list", "--id", "123"}, "confluence.attachment.list"},
		{[]string{"conf", "pull", "--id", "123", "--into", "mirror"}, "confluence.pull"},
		{[]string{"conf", "status", "mirror", "--remote"}, "confluence.status"},
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

func TestJiraPlanningReportCapabilityFamilyNormalizes(t *testing.T) {
	metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
		Family: "jira.planning.report", Invocations: 1, Successes: 1, OutputBytes: 42,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Family != "jira.planning.report" {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestJiraBoardListCapabilityFamilyNormalizes(t *testing.T) {
	metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
		Family: "jira.board.list", Invocations: 1, Successes: 1, OutputBytes: 42,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Family != "jira.board.list" {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestJiraBoardReadCapabilityFamiliesNormalize(t *testing.T) {
	for _, family := range []string{
		"jira.board.get",
		"jira.board.config",
		"jira.board.issues",
		"jira.board.backlog",
		"jira.board.export",
	} {
		t.Run(family, func(t *testing.T) {
			metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
				Family: family, Invocations: 1, Successes: 1, OutputBytes: 42,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || metrics[0].Family != family {
				t.Fatalf("metrics=%+v", metrics)
			}
		})
	}
}

func TestJiraBoardReadCapabilityFamiliesFailClosed(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "board", "inspect", "42"},
		{"jira", "board", "configuration", "42"},
		{"jira", "boards", "get", "42"},
	} {
		family, ok := CapabilityFamilyForCLI(args)
		if ok {
			t.Fatalf("unknown route classified: args=%q family=%q", args, family)
		}
	}
}

func TestMirrorReadLifecycleCapabilityFamiliesNormalize(t *testing.T) {
	for _, family := range []string{
		"confluence.pull",
		"confluence.status",
		"jira.pull",
		"jira.status",
	} {
		t.Run(family, func(t *testing.T) {
			metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
				Family: family, Invocations: 1, Successes: 1, OutputBytes: 42,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || metrics[0].Family != family {
				t.Fatalf("metrics=%+v", metrics)
			}
		})
	}
}

func TestMirrorReadLifecycleCapabilityFamiliesDoNotClassifyWrites(t *testing.T) {
	for _, args := range [][]string{
		{"conf", "push", "mirror/page.csf"},
		{"jira", "push", "mirror/issue.wiki"},
		{"conf", "apply", "mirror"},
		{"jira", "apply", "mirror"},
	} {
		if family, ok := CapabilityFamilyForCLI(args); ok {
			t.Fatalf("write route classified: args=%q family=%q", args, family)
		}
	}
}

func TestJiraStructureCapabilityFamiliesNormalize(t *testing.T) {
	for _, family := range []string{
		"jira.structure.get",
		"jira.structure.forest",
		"jira.structure.folders",
		"jira.structure.rows",
		"jira.structure.values",
		"jira.structure.pull-issues",
		"jira.structure.export",
		"jira.structure.view",
	} {
		t.Run(family, func(t *testing.T) {
			metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
				Family: family, Invocations: 1, Successes: 1, OutputBytes: 42,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || metrics[0].Family != family {
				t.Fatalf("metrics=%+v", metrics)
			}
		})
	}
}

func TestEvidenceReadCapabilityFamiliesNormalize(t *testing.T) {
	for _, family := range []string{
		"jira.issue.history",
		"confluence.page.meta",
		"confluence.page.history",
		"confluence.page.view",
		"confluence.attachment.list",
	} {
		t.Run(family, func(t *testing.T) {
			metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
				Family: family, Invocations: 1, Successes: 1, OutputBytes: 42,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || metrics[0].Family != family {
				t.Fatalf("metrics=%+v", metrics)
			}
		})
	}
}

func TestRemainingReadCapabilityFamiliesClassifyAndNormalize(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"jira", "field-options", "--project", "DEMO", "--type", "Task", "--field", "priority"}, "jira.field-options"},
		{[]string{"jira", "link-types"}, "jira.link-types"},
		{[]string{"jira", "me"}, "jira.me"},
		{[]string{"jira", "sprint", "current", "--board", "42"}, "jira.sprint.current"},
		{[]string{"jira", "sprint", "get", "7"}, "jira.sprint.get"},
		{[]string{"jira", "sprint", "issues", "7", "--limit", "5"}, "jira.sprint.issues"},
		{[]string{"jira", "sprint", "list", "--board", "42"}, "jira.sprint.list"},
		{[]string{"jira", "transitions", "--key", "DEMO-1"}, "jira.transitions"},
		{[]string{"jira", "user", "get", "example"}, "jira.user.get"},
		{[]string{"jira", "user", "search", "Example", "--limit", "5"}, "jira.user.search"},
		{[]string{"jira", "issue", "attachment", "get", "DEMO-1", "--id", "7"}, "jira.issue.attachment.get"},
		{[]string{"jira", "issue", "attachment", "list", "DEMO-1"}, "jira.issue.attachment.list"},
		{[]string{"jira", "issue", "check", "DEMO-1"}, "jira.issue.check"},
		{[]string{"jira", "issue", "children", "DEMO-1"}, "jira.issue.children"},
		{[]string{"jira", "issue", "comment", "list", "DEMO-1"}, "jira.issue.comment.list"},
		{[]string{"jira", "issue", "get", "DEMO-1"}, "jira.issue.get"},
		{[]string{"jira", "issue", "images", "DEMO-1", "--into", "."}, "jira.issue.images"},
		{[]string{"jira", "issue", "link", "list", "DEMO-1"}, "jira.issue.link.list"},
		{[]string{"jira", "issue", "tree", "--jql", "project = DEMO"}, "jira.issue.tree"},
		{[]string{"jira", "issue", "view", "DEMO-1"}, "jira.issue.view"},
		{[]string{"jira", "issue", "watchers", "list", "DEMO-1"}, "jira.issue.watchers.list"},
		{[]string{"conf", "attachment", "get", "--id", "123", "--name", "file.bin"}, "confluence.attachment.get"},
		{[]string{"conf", "comment", "list", "--id", "123"}, "confluence.comment.list"},
		{[]string{"conf", "me"}, "confluence.me"},
		{[]string{"conf", "page", "get", "--id", "123"}, "confluence.page.get"},
		{[]string{"conf", "page", "labels", "list", "--id", "123"}, "confluence.page.labels.list"},
		{[]string{"conf", "page", "list", "--space", "DEMO"}, "confluence.page.list"},
		{[]string{"conf", "space", "tree", "DEMO"}, "confluence.space.tree"},
	} {
		t.Run(test.want, func(t *testing.T) {
			family, ok := CapabilityFamilyForCLI(test.args)
			if !ok || family != test.want {
				t.Fatalf("family=%q ok=%t want=%q", family, ok, test.want)
			}
			metrics, err := normalizeCapabilityFamilies([]CapabilityFamilyMetric{{
				Family: family, Invocations: 1, Successes: 1, OutputBytes: 42,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(metrics) != 1 || metrics[0].Family != test.want {
				t.Fatalf("metrics=%+v", metrics)
			}
		})
	}
}

func TestRemainingReadCapabilityFamiliesDoNotClassifyWriteSiblings(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "sprint", "add", "7", "DEMO-1"},
		{"jira", "sprint", "remove", "DEMO-1"},
		{"jira", "issue", "attachment", "upload", "DEMO-1", "file.bin"},
		{"jira", "issue", "comment", "add", "DEMO-1", "--from-file", "body.txt"},
		{"jira", "issue", "comment", "delete", "DEMO-1", "7"},
		{"jira", "issue", "link", "add", "DEMO-1", "--to", "DEMO-2"},
		{"jira", "issue", "link", "delete", "7"},
		{"jira", "issue", "watchers", "add", "DEMO-1", "example"},
		{"jira", "issue", "watchers", "remove", "DEMO-1", "example"},
		{"conf", "attachment", "upload", "--id", "123", "file.bin"},
		{"conf", "attachment", "delete", "--id", "123", "--attachment", "7"},
		{"conf", "comment", "add", "--id", "123", "--from-file", "body.csf"},
		{"conf", "page", "labels", "add", "--id", "123", "example"},
		{"conf", "page", "labels", "remove", "--id", "123", "example"},
	} {
		if family, ok := CapabilityFamilyForCLI(args); ok {
			t.Fatalf("write sibling classified as %q for %q", family, args)
		}
	}
}
