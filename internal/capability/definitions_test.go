package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestDefinitionsReturnsDefensiveCopy(t *testing.T) {
	first := Definitions()
	if len(first) != 69 {
		t.Fatalf("definitions=%d want=69", len(first))
	}
	want := first[0]
	first[0] = Definition{ID: "changed"}

	second := Definitions()
	if second[0] != want {
		t.Fatalf("Definitions shared backing storage: got=%+v want=%+v", second[0], want)
	}
}

func TestDefinitionsCanonicalMetadataDigestIsStable(t *testing.T) {
	encoded, err := json.Marshal(Definitions())
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.Sum256(encoded)
	const want = "5c15e676be9a6c9223e43973699c8b3f0a064de3c9d81d749d347f10e0d81adf"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("definition metadata digest=%x", got)
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
	if mapped != 33 || cliOnly != 36 {
		t.Fatalf("mapped=%d cli_only=%d want=33/36", mapped, cliOnly)
	}
	if mappedMutating != 0 {
		t.Fatalf("mapped mutating definitions=%d want=0", mappedMutating)
	}
}

func TestJiraQualifiedWorkflowRoutesHaveExactMetadata(t *testing.T) {
	want := []Definition{
		{ID: "jira.batch.issue.fields", TaskClass: "jira/batch-analysis", Service: "jira", Role: "primary", Priority: 10, CLICommand: "jira issue field batch", Evidence: "qualified", Completeness: "reconciled", Skill: "jira", Reference: "reference/batch-read.md"},
		{ID: "jira.batch.issue.export", TaskClass: "jira/batch-analysis", Service: "jira", Role: "expand", Priority: 20, CLICommand: "jira export", Evidence: "snapshot", Completeness: "explicit", Skill: "jira", Reference: "reference/batch-read.md"},
		{ID: "jira.issue.create-metadata", TaskClass: "jira/create", Service: "jira", Role: "discover", Priority: 10, CLICommand: "jira issue create-metadata", Evidence: "qualified", Completeness: "explicit", Skill: "jira", Reference: "reference/fields.md"},
		{ID: "jira.issue.create.preview", TaskClass: "jira/create", Service: "jira", Role: "preview", Priority: 20, CLICommand: "jira issue create preview", Evidence: "hash-bound", Completeness: "explicit", Skill: "jira", Reference: "reference/fields.md"},
		{ID: "jira.issue.create", TaskClass: "jira/create", Service: "jira", Role: "write", Priority: 30, CLICommand: "jira issue create", Evidence: "hash-bound", Completeness: "reconciled", Skill: "jira", Reference: "reference/fields.md"},
		{ID: "jira.issue.link.add.preview", TaskClass: "jira/link", Service: "jira", Role: "preview", Priority: 10, CLICommand: "jira issue link add preview", Evidence: "hash-bound", Completeness: "explicit", Skill: "jira", Reference: "reference/editing.md"},
		{ID: "jira.issue.link.add", TaskClass: "jira/link", Service: "jira", Role: "write", Priority: 20, CLICommand: "jira issue link add", Evidence: "hash-bound", Completeness: "reconciled", Skill: "jira", Reference: "reference/editing.md"},
		{ID: "jira.issue.link.delete.preview", TaskClass: "jira/link", Service: "jira", Role: "preview", Priority: 30, CLICommand: "jira issue link delete preview", Evidence: "hash-bound", Completeness: "explicit", Skill: "jira", Reference: "reference/editing.md"},
		{ID: "jira.issue.link.delete", TaskClass: "jira/link", Service: "jira", Role: "write", Priority: 40, CLICommand: "jira issue link delete", Evidence: "hash-bound", Completeness: "reconciled", Skill: "jira", Reference: "reference/editing.md"},
		{ID: "jira.issue.plan.preview", TaskClass: "jira/batch-edit", Service: "jira", Role: "review", Priority: 10, CLICommand: "jira issue plan preview", Evidence: "hash-bound", Completeness: "per-row", Skill: "jira", Reference: "reference/extended-capabilities.md"},
		{ID: "jira.issue.plan.apply", TaskClass: "jira/batch-edit", Service: "jira", Role: "write", Priority: 20, CLICommand: "jira issue plan apply", Evidence: "hash-bound", Completeness: "per-row", Skill: "jira", Reference: "reference/extended-capabilities.md"},
	}
	byID := make(map[string]Definition)
	for _, definition := range Definitions() {
		byID[definition.ID] = definition
	}
	for _, expected := range want {
		got, ok := byID[expected.ID]
		if !ok {
			t.Errorf("missing capability %q", expected.ID)
			continue
		}
		got.Summary = ""
		if got != expected {
			t.Errorf("%s=%+v want=%+v", expected.ID, got, expected)
		}
		if got.MCPTool != "" || got.MCPScope != "" {
			t.Errorf("%s unexpectedly maps to MCP", expected.ID)
		}
	}
}

func TestConfluenceAttachmentDiscoveryIsOneMappedPrimaryRoute(t *testing.T) {
	const scope = "Caller-bounded live Server/Data Center attachment-metadata prefix with closed complete, partial, or failed qualification and query-bound continuation; no bytes, comments, paths, or URLs."
	var matches []Definition
	for _, definition := range Definitions() {
		if definition.TaskClass == "confluence/attachment-discovery" || definition.ID == "confluence.attachment.search" {
			matches = append(matches, definition)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("attachment-discovery routes=%d want=1: %+v", len(matches), matches)
	}
	got := matches[0]
	if got.ID != "confluence.attachment.search" || got.Service != "confluence" || got.Role != "primary" ||
		got.Priority != 10 || got.CLICommand != "conf attachment search" || got.MCPTool != "confluence_attachment_search" ||
		got.MCPScope != scope || got.Evidence != "qualified" || got.Completeness != "explicit" ||
		got.Skill != "confluence" || got.Reference != "reference/tables-attachments.md" {
		t.Fatalf("attachment-discovery route=%+v", got)
	}
}

func TestConfluenceSpaceHierarchyIsOneCLIOnlyPrimaryRoute(t *testing.T) {
	var matches []Definition
	for _, definition := range Definitions() {
		if definition.TaskClass == "confluence/space-hierarchy" || definition.ID == "confluence.space.tree" {
			matches = append(matches, definition)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("space-hierarchy routes=%d want=1: %+v", len(matches), matches)
	}
	got := matches[0]
	if got.ID != "confluence.space.tree" || got.Service != "confluence" || got.Role != "primary" ||
		got.Priority != 10 || got.CLICommand != "conf space tree" || got.MCPTool != "" || got.MCPScope != "" ||
		got.Evidence != "qualified" || got.Completeness != "explicit" || got.Skill != "confluence" ||
		got.Reference != "reference/commands.md" {
		t.Fatalf("space-hierarchy route=%+v", got)
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

func TestJiraInverseReferenceIsOneCLIOnlyRoute(t *testing.T) {
	count := 0
	for _, definition := range Definitions() {
		if definition.ID == "knowledge.jira.inverse-reference" {
			t.Fatal("inverse-reference search must not have a knowledge alias")
		}
		if definition.ID != "jira.issue.reference.search" {
			continue
		}
		count++
		if definition.CLICommand != "jira issue reference search" || definition.MCPTool != "" || definition.MCPScope != "" {
			t.Fatalf("inverse-reference transports=%+v", definition)
		}
		if definition.Service != "jira" || definition.TaskClass != "jira/inverse-reference" ||
			definition.Role != "primary" || definition.Priority != 10 || definition.Evidence != "qualified" ||
			definition.Completeness != "per-source-and-selection" || definition.Skill != "jira" ||
			definition.Reference != "reference/commands.md" {
			t.Fatalf("inverse-reference route=%+v", definition)
		}
	}
	if count != 1 {
		t.Fatalf("jira.issue.reference.search definitions=%d want=1", count)
	}
}
