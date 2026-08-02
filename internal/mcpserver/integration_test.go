package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestIntegrationProductionReadOnlyFixtures(t *testing.T) {
	if os.Getenv("ATL_INTEGRATION") == "" {
		t.Skip("set ATL_INTEGRATION=1 to run live integration tests")
	}

	t.Run("jira fields", testIntegrationMCPJiraFields)
	t.Run("jira issue references", testIntegrationMCPJiraIssueRefs)
	t.Run("jira issue development graph", testIntegrationMCPJiraIssueDevelopmentGraph)
	t.Run("jira structure", testIntegrationMCPJiraStructure)
	t.Run("confluence page metadata", testIntegrationMCPConfluencePageMetadata)
	t.Run("confluence comments", testIntegrationMCPConfluenceComments)
	t.Run("confluence tables", testIntegrationMCPConfluenceTables)
}

func testIntegrationMCPJiraIssueDevelopmentGraph(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("ATL_TEST_JIRA_GRAPH_KEY"))
	if key == "" {
		t.Skip("set ATL_TEST_JIRA_GRAPH_KEY to run the live Jira Development graph MCP test")
	}
	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	stable := callIntegrationMCPTool[JiraIssueGraphOutput](
		t, client, "jira_issue_graph", map[string]any{"key": key, "depth": 0, "max_bytes": 1 << 20},
	)
	if stable.Bounds.IncludeDevelopment || stable.Bounds.RequestsUsed != 4 || len(stable.Sources) != 8 {
		t.Fatal("live default Jira graph request profile changed")
	}
	for _, node := range stable.Nodes {
		if node.SCM != nil || strings.HasPrefix(node.Kind, "gitlab_") {
			t.Fatal("live default Jira graph exposed Development evidence")
		}
	}

	development := callIntegrationMCPTool[JiraIssueGraphOutput](
		t, client, "jira_issue_graph", map[string]any{
			"key": key, "depth": 0, "include_development": true, "max_bytes": 1 << 20,
		},
	)
	if !development.Bounds.IncludeDevelopment || development.Bounds.RequestsUsed <= stable.Bounds.RequestsUsed || len(development.Sources) != 9 {
		t.Fatal("live opt-in Jira graph request profile did not reconcile")
	}
	developmentComplete := false
	for _, source := range development.Sources {
		if source.Kind == "development" {
			developmentComplete = source.Complete && source.Stability == domain.ArtifactStabilityExperimentalAPI
		}
	}
	if !developmentComplete {
		t.Fatal("live Development source was not complete and experimental")
	}
	developmentNodes := 0
	for _, node := range development.Nodes {
		if !strings.HasPrefix(node.Kind, "gitlab_") {
			continue
		}
		developmentNodes++
		if node.SCM == nil || node.URL != "" || node.Expanded || node.State != domain.ArtifactNodeStub ||
			node.Stability != domain.ArtifactStabilityExperimentalAPI || !safeJiraIssueGraphProject(node.SCM.Host, node.SCM.ProjectPath) {
			t.Fatal("live Development node crossed the closed MCP projection")
		}
	}
	if developmentNodes == 0 {
		t.Fatal("live opt-in Jira graph returned no Development identities")
	}
}

func testIntegrationMCPJiraFields(t *testing.T) {
	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	result := callIntegrationMCPTool[app.JiraFieldCatalogResult](
		t, client, "jira_fields", map[string]any{"summary_only": true, "max_bytes": 1024},
	)
	if result.SchemaVersion != 1 || result.Projection != "summary" || len(result.Fields) != 0 ||
		result.Fields == nil || result.Count != result.CustomCount+result.SystemCount || result.Total < result.Count {
		t.Fatal("live Jira field catalog summary did not reconcile")
	}
}

func testIntegrationMCPJiraIssueRefs(t *testing.T) {
	project := strings.TrimSpace(os.Getenv("ATL_TEST_JIRA_PROJECT"))
	if project == "" {
		t.Skip("set ATL_TEST_JIRA_PROJECT to run the live Jira reference MCP test")
	}
	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	result := callIntegrationMCPTool[app.JiraIssueRefsView](
		t, client, "jira_issue_refs", map[string]any{
			"jql":       "project = " + project + " ORDER BY key ASC",
			"limit":     1,
			"max_bytes": 1 << 20,
		},
	)
	if result.SchemaVersion != 1 || result.Count != len(result.Issues) ||
		result.Summary.IssueCount != result.Count ||
		!result.Summary.CountMatchesIssues ||
		!result.Summary.SelectionCountMatchesIssues ||
		!result.Summary.ReferenceCountMatchesKinds ||
		!result.Summary.IssueSummariesReconciled ||
		!result.Summary.CompleteMatchesInputs ||
		!result.Summary.TruncatedMatchesInputs {
		t.Fatal("live Jira reference summary did not reconcile")
	}
	for _, issue := range result.Issues {
		if strings.TrimSpace(issue.Key) == "" ||
			!issue.ReferenceSummary.ReferenceCountMatchesKinds ||
			!issue.ReferenceSummary.CompleteMatchesSources ||
			!issue.ReferenceSummary.TruncatedMatchesSources {
			t.Fatal("live Jira per-issue reference summary did not reconcile")
		}
	}
}

func testIntegrationMCPJiraStructure(t *testing.T) {
	rawID := strings.TrimSpace(os.Getenv("ATL_TEST_JIRA_STRUCTURE_ID"))
	if rawID == "" {
		t.Skip("set ATL_TEST_JIRA_STRUCTURE_ID to run the live Structure MCP test")
	}
	structureID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || structureID <= 0 {
		t.Fatal("ATL_TEST_JIRA_STRUCTURE_ID must be a positive integer")
	}
	fields := integrationMCPFields(os.Getenv("ATL_TEST_JIRA_STRUCTURE_FIELDS"))
	args := map[string]any{
		"structure_id": structureID,
		"fields":       fields,
		"max_rows":     1000,
		"max_bytes":    1 << 20,
	}
	// ATL_TEST_JIRA_STRUCTURE_ROOT selects an issue root in the CLI workflow;
	// it is not a stored-folder path and must not be remapped to MCP folder_path.
	folderRow := strings.TrimSpace(os.Getenv("ATL_TEST_JIRA_STRUCTURE_FOLDER_ROW"))
	selectionKind := ""
	if folderRow != "" {
		row, parseErr := strconv.ParseInt(folderRow, 10, 64)
		if parseErr != nil || row <= 0 {
			t.Fatal("ATL_TEST_JIRA_STRUCTURE_FOLDER_ROW must be a positive integer")
		}
		args["folder_row"] = row
		selectionKind = "folder-row"
	}

	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	metadata := callIntegrationMCPTool[app.StructureMetadataResult](
		t, client, "jira_structure_get", map[string]any{"structure_id": structureID},
	)
	if metadata.SchemaVersion != 1 || metadata.ID != structureID || strings.TrimSpace(metadata.Name) == "" {
		t.Fatal("live Structure metadata did not reconcile")
	}

	view := callIntegrationMCPTool[app.StructureSnapshot](t, client, "jira_structure_view", args)
	if view.SchemaVersion != 1 || view.Structure.ID != structureID ||
		view.RowCount == 0 || view.RowCount != len(view.Rows) ||
		view.IssueCount < 0 || !slices.Equal(view.Projection.Attributes, fields) {
		t.Fatal("live Structure view did not reconcile")
	}
	inaccessible := make(map[int64]struct{}, len(view.InaccessibleRows))
	for _, rowID := range view.InaccessibleRows {
		if _, duplicate := inaccessible[rowID]; duplicate {
			t.Fatal("live Structure inaccessible rows did not reconcile")
		}
		inaccessible[rowID] = struct{}{}
	}
	matchedInaccessible := 0
	for _, row := range view.Rows {
		_, listed := inaccessible[row.RowID]
		if listed == row.Accessible {
			t.Fatal("live Structure row accessibility did not reconcile")
		}
		if listed {
			matchedInaccessible++
		}
	}
	if matchedInaccessible != len(inaccessible) {
		t.Fatal("live Structure inaccessible rows did not reconcile")
	}
	if (view.Complete && len(inaccessible) != 0) ||
		(!view.Complete && len(inaccessible) == 0 && len(view.Warnings) == 0) {
		t.Fatal("live Structure completeness did not reconcile")
	}
	if selectionKind != "" && (view.Selection == nil || view.Selection.Kind != selectionKind) {
		t.Fatal("live Structure selection did not reconcile")
	}
}

func testIntegrationMCPConfluencePageMetadata(t *testing.T) {
	pageID := strings.TrimSpace(os.Getenv("ATL_TEST_CONFLUENCE_TABLE_PAGE_ID"))
	if pageID == "" {
		t.Skip("set ATL_TEST_CONFLUENCE_TABLE_PAGE_ID to run the live page metadata MCP test")
	}
	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	result := callIntegrationMCPTool[app.ConfluencePageMetadataResult](
		t, client, "confluence_page_meta", map[string]any{"reference": pageID},
	)
	if result.SchemaVersion != app.ConfluencePageMetadataSchemaVersion ||
		result.ID != pageID || strings.TrimSpace(result.Title) == "" ||
		strings.TrimSpace(result.Space) == "" || result.Version < 1 {
		t.Fatal("live Confluence page metadata did not reconcile")
	}
	switch result.RestrictionState {
	case app.ConfluenceRestrictionUnknown, app.ConfluenceRestrictionRestricted, app.ConfluenceRestrictionUnrestricted:
	default:
		t.Fatal("live Confluence restriction state did not reconcile")
	}
}

func testIntegrationMCPConfluenceComments(t *testing.T) {
	pageID := strings.TrimSpace(os.Getenv("ATL_TEST_PAGE_ID"))
	if pageID == "" {
		pageID = strings.TrimSpace(os.Getenv("ATL_TEST_CONFLUENCE_TABLE_PAGE_ID"))
	}
	if pageID == "" {
		t.Skip("set ATL_TEST_PAGE_ID or ATL_TEST_CONFLUENCE_TABLE_PAGE_ID to run the live comment MCP test")
	}
	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	metadata := callIntegrationMCPTool[app.ConfluencePageMetadataResult](
		t, client, "confluence_page_meta", map[string]any{"reference": pageID},
	)
	list := callIntegrationMCPTool[app.ConfluenceCommentListView](
		t, client, "confluence_comment_list", map[string]any{
			"page_id": pageID, "expected_page_version": metadata.Version,
			"max_items": 1000, "max_bytes": 1 << 20,
		},
	)
	if err := app.ValidateConfluenceCommentListView(&list); err != nil ||
		list.PageID != pageID || list.PageVersion != metadata.Version || !list.PageVersionGated ||
		list.Bounds.MaxCommentPages != 32 || list.Bounds.MaxItems != 1000 || list.Bounds.MaxBytes != 1<<20 {
		t.Fatal("live Confluence comment list did not reconcile")
	}
	if len(list.Comments) == 0 {
		if !list.Complete {
			t.Fatal("live empty Confluence comment list was not complete")
		}
		return
	}

	thread := callIntegrationMCPTool[app.ConfluenceCommentThreadView](
		t, client, "confluence_comment_thread", map[string]any{
			"page_id": pageID, "comment_id": list.Comments[0].ID,
			"expected_page_version": list.PageVersion,
			"max_items":             1000, "max_bytes": 1 << 20,
		},
	)
	if err := app.ValidateConfluenceCommentThreadView(&thread); err != nil ||
		thread.PageID != pageID || thread.PageVersion != list.PageVersion || !thread.PageVersionGated ||
		thread.Query.CommentID != list.Comments[0].ID || len(thread.Comments) == 0 {
		t.Fatal("live Confluence comment thread did not reconcile")
	}
}

func testIntegrationMCPConfluenceTables(t *testing.T) {
	pageID := strings.TrimSpace(os.Getenv("ATL_TEST_CONFLUENCE_TABLE_PAGE_ID"))
	if pageID == "" {
		t.Skip("set ATL_TEST_CONFLUENCE_TABLE_PAGE_ID to run the live table MCP test")
	}
	minTables := 1
	if raw := strings.TrimSpace(os.Getenv("ATL_TEST_CONFLUENCE_TABLE_MIN_COUNT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			t.Fatal("ATL_TEST_CONFLUENCE_TABLE_MIN_COUNT must be a positive integer")
		}
		minTables = parsed
	}

	client, closeSessions := connectIntegrationMCPClient(t)
	defer closeSessions()

	summary := callIntegrationMCPTool[app.ConfluenceTableSummary](
		t, client, "confluence_table_summary",
		map[string]any{"reference": pageID, "max_bytes": 1 << 20},
	)
	if summary.Table != 0 || summary.TableCount < minTables ||
		summary.ReturnedTableCount != summary.TableCount ||
		len(summary.Tables) != summary.TableCount || !summary.SelectionReconciled {
		t.Fatal("live table summary did not reconcile")
	}
	for _, table := range summary.Tables {
		if table.Index < 1 || table.RowCount < 1 || table.ColumnCount < 1 ||
			!table.Rectangular || !table.CellCountReconciled {
			t.Fatal("live table summary shape did not reconcile")
		}
	}

	selected := minTables
	extract := callIntegrationMCPTool[app.ConfluenceTableExtract](
		t, client, "confluence_table_extract",
		map[string]any{"reference": pageID, "table": selected, "max_bytes": 1 << 20},
	)
	if extract.Table != selected || extract.TableCount < selected || len(extract.Tables) != 1 ||
		extract.Tables[0].Index != selected ||
		extract.Tables[0].RowCount != len(extract.Tables[0].Rows) ||
		extract.Tables[0].Summary != summary.Tables[selected-1] {
		t.Fatal("live selected table did not reconcile")
	}
	for _, row := range extract.Tables[0].Rows {
		if len(row.Cells) != extract.Tables[0].ColumnCount {
			t.Fatal("live selected table is not rectangular")
		}
	}
}

func integrationMCPFields(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"key", "summary", "status"}
	}
	parts := strings.Split(raw, ",")
	fields := make([]string, 0, len(parts))
	for _, part := range parts {
		if field := strings.TrimSpace(part); field != "" {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return []string{"key", "summary", "status"}
	}
	return fields
}

func connectIntegrationMCPClient(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("integration-test", ProductionDependencies("integration-test")).Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal("connect live MCP server")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "atl-integration-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal("connect live MCP client")
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		cancel()
	}
}

func callIntegrationMCPTool[T any](
	t *testing.T, client *mcp.ClientSession, name string, args map[string]any,
) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("%s live read failed", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s live result encoding failed", name)
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("%s live result shape failed", name)
	}
	return decoded
}
