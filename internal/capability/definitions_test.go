package capability

import "testing"

func TestDefinitionsReturnsDefensiveCopy(t *testing.T) {
	first := Definitions()
	if len(first) != 49 {
		t.Fatalf("definitions=%d want=49", len(first))
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
	if mapped != 30 || cliOnly != 19 {
		t.Fatalf("mapped=%d cli_only=%d want=30/19", mapped, cliOnly)
	}
	if mappedMutating != 0 {
		t.Fatalf("mapped mutating definitions=%d want=0", mappedMutating)
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
		const scope = "Jira-only stable-source schema-v2 graph with structural traversal, qualified sources, fixed request/response bounds, and an encoded-result bound; no Confluence resolution or Development source."
		if definition.MCPScope != scope {
			t.Fatalf("graph MCP scope=%q want=%q", definition.MCPScope, scope)
		}
	}
	if graphCount != 1 {
		t.Fatalf("jira.issue.graph definitions=%d want=1", graphCount)
	}
}
