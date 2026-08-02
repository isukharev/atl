package capability

import "testing"

func TestDefinitionsReturnsDefensiveCopy(t *testing.T) {
	first := Definitions()
	if len(first) != 55 {
		t.Fatalf("definitions=%d want=55", len(first))
	}
	want := first[0]
	first[0] = Definition{ID: "changed"}

	second := Definitions()
	if second[0] != want {
		t.Fatalf("Definitions shared backing storage: got=%+v want=%+v", second[0], want)
	}
}

func TestDefinitionsTransportMappings(t *testing.T) {
	mapped, cliOnly, mappedMutating := 0, 0, 0
	for _, definition := range Definitions() {
		if definition.MCPTool == "" {
			cliOnly++
			if definition.MCPScope != "" {
				t.Errorf("%s has MCP scope without tool", definition.ID)
			}
			continue
		}
		mapped++
		if definition.MCPScope == "" {
			t.Errorf("%s has MCP tool without scope", definition.ID)
		}
		if definition.Role == "write" {
			mappedMutating++
		}
	}
	if mapped != 32 || cliOnly != 23 {
		t.Fatalf("mapped=%d cli_only=%d want=32/23", mapped, cliOnly)
	}
	if mappedMutating != 0 {
		t.Fatalf("mapped mutating definitions=%d want=0", mappedMutating)
	}
}

func TestConfluenceCommentsHasSixClosedRoutesAndOnlyReadsMapToMCP(t *testing.T) {
	want := []Definition{
		{ID: "confluence.comment.list", Role: "discover", CLICommand: "conf comment list", MCPTool: "confluence_comment_list"},
		{ID: "confluence.comment.thread", Role: "expand", CLICommand: "conf comment thread", MCPTool: "confluence_comment_thread"},
		{ID: "confluence.comment.preview", Role: "preview", CLICommand: "conf comment preview"},
		{ID: "confluence.comment.add", Role: "write", CLICommand: "conf comment add"},
		{ID: "confluence.comment.mutation.preview", Role: "preview", CLICommand: "conf comment mutation preview"},
		{ID: "confluence.comment.mutation.apply", Role: "write", CLICommand: "conf comment mutation apply"},
	}
	var got []Definition
	for _, definition := range Definitions() {
		if definition.TaskClass == "confluence/comments" {
			got = append(got, definition)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("comment routes=%d want=%d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Service != "confluence" || got[i].Role != want[i].Role ||
			got[i].CLICommand != want[i].CLICommand || got[i].MCPTool != want[i].MCPTool ||
			(got[i].MCPTool != "" && got[i].MCPScope == "") {
			t.Fatalf("comment route[%d]=%+v want=%+v", i, got[i], want[i])
		}
	}
}

func TestJiraIssueGraphHasOneJiraOnlyTypedRoute(t *testing.T) {
	graphCount := 0
	for _, definition := range Definitions() {
		if definition.ID == "knowledge.jira.graph" {
			t.Fatal("graph must not have a second knowledge alias")
		}
		if definition.ID != "jira.issue.graph" {
			continue
		}
		graphCount++
		if definition.CLICommand != "jira issue graph" || definition.MCPTool != "jira_issue_graph" {
			t.Fatalf("graph transports=%+v", definition)
		}
		if definition.Service != "jira" || definition.TaskClass != "jira/graph-evidence" ||
			definition.Completeness != "per-source-and-traversal" {
			t.Fatalf("graph route=%+v", definition)
		}
		const scope = "Jira-only schema-v2 graph with structural traversal, qualified sources, fixed request/response/result bounds, and optional bounded experimental SCM identities; no Confluence or GitLab reads."
		if definition.MCPScope != scope {
			t.Fatalf("graph MCP scope=%q want=%q", definition.MCPScope, scope)
		}
	}
	if graphCount != 1 {
		t.Fatalf("jira.issue.graph definitions=%d want=1", graphCount)
	}
}
