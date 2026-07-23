package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/agenteval"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestServerAdvertisesOnlyTypedReadOnlyTools(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()

	initialized := client.InitializeResult()
	if initialized == nil || initialized.Instructions != Instructions || initialized.ServerInfo.Name != "atl" {
		t.Fatalf("initialize=%+v", initialized)
	}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"confluence_page_outline", "confluence_page_resolve", "confluence_page_section", "confluence_search",
		"confluence_table_extract", "confluence_table_summary",
		"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_search",
	}
	got := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %s annotations=%+v", tool.Name, tool.Annotations)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("tool %s destructive annotation=%+v", tool.Name, tool.Annotations)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %s open-world annotation=%+v", tool.Name, tool.Annotations)
		}
		input, ok := tool.InputSchema.(map[string]any)
		if !ok || input["type"] != "object" {
			t.Errorf("tool %s input schema=%#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "jira_epic_digest" && !schemaRequired(input, "include") {
			t.Errorf("tool %s must require an explicit include: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "confluence_search" && !schemaRequired(input, "cql") {
			t.Errorf("tool %s must require explicit cql: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "confluence_page_section" {
			properties, _ := input["properties"].(map[string]any)
			heading, _ := properties["heading"].(map[string]any)
			description, _ := heading["description"].(string)
			if !strings.Contains(description, "without a Markdown # prefix") {
				t.Errorf("tool %s heading guidance is ambiguous: %#v", tool.Name, heading)
			}
		}
		if tool.Name == "confluence_table_extract" && (!schemaRequired(input, "reference") || !schemaRequired(input, "table")) {
			t.Errorf("tool %s must require reference and selected table: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "confluence_table_extract" {
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !bytes.Contains(encoded, []byte("whitespace-normalized plain text")) ||
				!bytes.Contains(encoded, []byte("formatting-preserving Markdown")) {
				t.Errorf("tool %s output schema does not distinguish text and markdown: %s", tool.Name, encoded)
			}
		}
		if tool.Name == "confluence_table_summary" && !schemaRequired(input, "reference") {
			t.Errorf("tool %s must require reference: %#v", tool.Name, tool.InputSchema)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", tool.Name)
		}
		if path, ok := booleanPropertySchema(tool.OutputSchema, "outputSchema"); ok {
			t.Errorf("tool %s exposes client-incompatible boolean property schema at %s", tool.Name, path)
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tools=%v want=%v", got, want)
	}
}

func booleanPropertySchema(value any, path string) (string, bool) {
	switch current := value.(type) {
	case map[string]any:
		if properties, ok := current["properties"].(map[string]any); ok {
			for name, property := range properties {
				if _, ok := property.(bool); ok {
					return path + ".properties." + name, true
				}
				if found, ok := booleanPropertySchema(property, path+".properties."+name); ok {
					return found, true
				}
			}
		}
		for keyword, child := range current {
			if keyword == "properties" {
				continue
			}
			if found, ok := booleanPropertySchema(child, path+"."+keyword); ok {
				return found, true
			}
		}
	case []any:
		for index, child := range current {
			if found, ok := booleanPropertySchema(child, fmt.Sprintf("%s[%d]", path, index)); ok {
				return found, true
			}
		}
	}
	return "", false
}

func schemaRequired(schema map[string]any, name string) bool {
	required, _ := schema["required"].([]any)
	for _, value := range required {
		if value == name {
			return true
		}
	}
	return false
}

func TestSyntheticPortfolioThroughMCPUsesExactGETOnlyRoute(t *testing.T) {
	fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-quarter-portfolio", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := agenteval.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := agenteval.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for name, value := range backend.Environment() {
		t.Setenv(name, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_NO_UPDATE", "1")

	client, closeSessions := connectTestClient(t, New("test", ProductionDependencies("test")))
	defer closeSessions()
	fields := callToolOK(t, client, "jira_fields", map[string]any{})
	fieldContent, ok := fields.StructuredContent.(map[string]any)
	if !ok || fieldContent["schema_version"] != float64(1) || fieldContent["complete"] != true {
		t.Fatalf("field catalog=%#v", fields.StructuredContent)
	}
	callToolOK(t, client, "jira_board_view", map[string]any{
		"board_id": 5, "scope": "board", "limit": 50,
		"columns": []string{"key", "summary", "status", "issuetype", "updated", "customfield_11001", "customfield_11002", "customfield_11003"},
	})
	for _, key := range []string{"PROJ-10", "PROJ-20", "PROJ-30"} {
		callToolOK(t, client, "jira_epic_digest", map[string]any{
			"key": key, "quarter": "2026-Q2", "include": []string{"identity", "status-field", "history"}, "status_field": "customfield_11002", "projection": "compact",
		})
	}
	for _, pageID := range []string{"9001", "9002", "9003"} {
		result := callToolOK(t, client, "confluence_page_section", map[string]any{
			"reference": "/wiki/pages/viewpage.action?pageId=" + pageID, "heading": "Results", "max_bytes": 32768,
		})
		content, ok := result.StructuredContent.(map[string]any)
		if !ok || content["id"] != pageID || content["complete"] != true {
			t.Fatalf("section %s content=%#v", pageID, result.StructuredContent)
		}
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 15 || len(methods) != 1 || unexpected != 0 || duplicates != 2 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticClippedDigestExpandsOnlyExactField(t *testing.T) {
	fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-clipped-field-evidence", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := agenteval.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := agenteval.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for name, value := range backend.Environment() {
		t.Setenv(name, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_NO_UPDATE", "1")

	client, closeSessions := connectTestClient(t, New("test", ProductionDependencies("test")))
	defer closeSessions()
	catalog := callToolOK(t, client, "jira_fields", map[string]any{"name_like": "Delivery Notes"})
	catalogContent, ok := catalog.StructuredContent.(map[string]any)
	if !ok || catalogContent["complete"] != true || catalogContent["count"] != float64(1) {
		t.Fatalf("field catalog=%#v", catalog.StructuredContent)
	}
	digest := callToolOK(t, client, "jira_epic_digest", map[string]any{
		"key": "PROJ-1", "include": []string{"identity", "status-field"},
		"status_field": "customfield_10001", "projection": "compact",
	})
	digestContent, ok := digest.StructuredContent.(map[string]any)
	projection, projectionOK := digestContent["projection"].(map[string]any)
	clipped, clippedOK := projection["clipped"].([]any)
	if !ok || !projectionOK || !clippedOK || len(clipped) != 1 || clipped[0] != "status_field.value" {
		t.Fatalf("compact digest projection=%#v", projection)
	}

	field := callToolOK(t, client, "jira_issue_field_get", map[string]any{
		"key": "PROJ-1", "field": "customfield_10001", "max_bytes": 8192,
	})
	fieldContent, ok := field.StructuredContent.(map[string]any)
	value, _ := fieldContent["value"].(string)
	if !ok || fieldContent["complete"] != true || !strings.Contains(value, "DECISION=proceed") || len(value) <= 3<<10 {
		t.Fatalf("field expansion complete=%v value-bytes=%d", fieldContent["complete"], len(value))
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 4 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticTopicDiscoveryThroughMCPUsesExactGETOnlyRoute(t *testing.T) {
	fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", "cross-service-topic-discovery", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := agenteval.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := agenteval.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for name, value := range backend.Environment() {
		t.Setenv(name, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_NO_UPDATE", "1")

	client, closeSessions := connectTestClient(t, New("test", ProductionDependencies("test")))
	defer closeSessions()
	conf := callToolOK(t, client, "confluence_search", map[string]any{"cql": `siteSearch ~ "Orchid retry worker"`, "limit": 10})
	confContent, ok := conf.StructuredContent.(map[string]any)
	if !ok || confContent["complete"] != true || confContent["count"] != float64(3) {
		t.Fatalf("confluence search=%#v", conf.StructuredContent)
	}
	jira := callToolOK(t, client, "jira_issue_search", map[string]any{
		"jql":     `text ~ "Orchid retry worker" ORDER BY updated DESC`,
		"columns": []string{"key", "summary", "status", "updated"}, "limit": 10,
	})
	jiraContent, ok := jira.StructuredContent.(map[string]any)
	page, pageOK := jiraContent["page"].(map[string]any)
	if !ok || !pageOK || page["complete"] != true {
		t.Fatalf("jira search=%#v", jira.StructuredContent)
	}
	callToolOK(t, client, "confluence_page_outline", map[string]any{"reference": "8101"})
	section := callToolOK(t, client, "confluence_page_section", map[string]any{"reference": "8101", "heading": "Decision"})
	sectionContent, ok := section.StructuredContent.(map[string]any)
	markdown, _ := sectionContent["markdown"].(string)
	if !ok || sectionContent["complete"] != true || !strings.Contains(markdown, "25 percent") {
		t.Fatalf("section=%#v", section.StructuredContent)
	}
	field := callToolOK(t, client, "jira_issue_field_get", map[string]any{"key": "OPS-42", "field": "Description"})
	fieldContent, ok := field.StructuredContent.(map[string]any)
	value, _ := fieldContent["value"].(string)
	if !ok || fieldContent["complete"] != true || !strings.Contains(value, "Capacity test pending") {
		t.Fatalf("field=%#v", field.StructuredContent)
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 6 || len(methods) != 1 || unexpected != 0 || duplicates != 1 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestToolInputsMapToBoundedApplicationCalls(t *testing.T) {
	j := &recordingJiraReader{}
	c := &recordingConfluenceReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira:       func() (JiraReader, error) { return j, nil },
		Confluence: func() (ConfluenceReader, error) { return c, nil },
	}))
	defer closeSessions()
	custom := true
	callToolOK(t, client, "jira_fields", map[string]any{"name_like": "Outcome", "custom": custom})
	callToolOK(t, client, "jira_issue_search", map[string]any{
		"jql": "project=PROJ", "columns": []string{"key", "status"}, "view": "compact", "cursor": "next",
	})
	callToolOK(t, client, "jira_issue_field_get", map[string]any{
		"key": "PROJ-1", "field": "Delivery Notes", "max_bytes": 4096,
	})
	digest := callToolOK(t, client, "jira_epic_digest", map[string]any{
		"key": "PROJ-1", "quarter": "2026-Q2", "include": []string{"identity", "history"},
		"status_field": "customfield_1", "dod_field": "customfield_2", "epic_field": "customfield_3",
		"projection": "compact",
	})
	digestContent, ok := digest.StructuredContent.(map[string]any)
	projection, projectionOK := digestContent["projection"].(map[string]any)
	if !ok || !projectionOK || projection["name"] != "compact" {
		t.Fatalf("digest content=%#v", digest.StructuredContent)
	}
	callToolOK(t, client, "jira_board_view", map[string]any{
		"board_id": 7, "scope": "backlog", "columns": []string{"key"}, "view": "compact", "jql": "labels=x",
	})
	callToolOK(t, client, "confluence_search", map[string]any{"cql": "space=DOCS", "cursor": "25"})
	callToolOK(t, client, "confluence_page_resolve", map[string]any{"reference": "/x/Abc"})
	callToolOK(t, client, "confluence_page_outline", map[string]any{"reference": "42"})
	callToolOK(t, client, "confluence_page_section", map[string]any{
		"reference": "42", "heading": "Results", "occurrence": 2,
	})
	summary := callToolOK(t, client, "confluence_table_summary", map[string]any{"reference": "42", "table": 2})
	summaryContent, ok := summary.StructuredContent.(map[string]any)
	if !ok || summaryContent["selection_reconciled"] != true {
		t.Fatalf("table summary=%#v", summary.StructuredContent)
	}
	extract := callToolOK(t, client, "confluence_table_extract", map[string]any{"reference": "42", "table": 2, "max_bytes": 4096})
	extractContent, ok := extract.StructuredContent.(map[string]any)
	if !ok || extractContent["selected_table"] != float64(2) {
		t.Fatalf("table extract=%#v", extract.StructuredContent)
	}

	if j.fieldOpts.Custom != "true" || j.fieldOpts.NameLike != "Outcome" {
		t.Fatalf("field opts=%+v", j.fieldOpts)
	}
	if j.searchJQL != "project=PROJ" || j.searchLimit != 50 || j.searchCursor != "next" || j.searchView != "compact" || strings.Join(j.searchColumns, ",") != "key,status" {
		t.Fatalf("search jql=%q columns=%v view=%q limit=%d cursor=%q", j.searchJQL, j.searchColumns, j.searchView, j.searchLimit, j.searchCursor)
	}
	if j.fieldEvidenceKey != "PROJ-1" || j.fieldEvidenceOpts.Selector != "Delivery Notes" || j.fieldEvidenceOpts.MaxBytes != 4096 {
		t.Fatalf("field evidence key=%q opts=%+v", j.fieldEvidenceKey, j.fieldEvidenceOpts)
	}
	if j.digestKey != "PROJ-1" || j.digestOpts.Quarter != "2026-Q2" || j.digestOpts.StatusField != "customfield_1" || j.digestOpts.DoDField != "customfield_2" || j.digestOpts.EpicField != "customfield_3" || j.digestOpts.ChildLimit != 1000 || j.digestOpts.CommentLimit != 50 || j.digestOpts.HistoryLimit != 500 {
		t.Fatalf("digest key=%q opts=%+v", j.digestKey, j.digestOpts)
	}
	if j.boardID != 7 || j.boardOpts.Scope != "backlog" || j.boardOpts.Limit != 200 || j.boardOpts.JQL != "labels=x" {
		t.Fatalf("board id=%d opts=%+v", j.boardID, j.boardOpts)
	}
	if c.resolveReference != "/x/Abc" || c.outlineReference != "42" || c.sectionReference != "42" || c.sectionOpts.Heading != "Results" || c.sectionOpts.Occurrence != 2 || c.sectionOpts.MaxBytes != 32<<10 {
		t.Fatalf("confluence=%+v", c)
	}
	if c.searchCQL != "space=DOCS" || c.searchLimit != 25 || c.searchCursor != "25" {
		t.Fatalf("confluence search cql=%q limit=%d cursor=%q", c.searchCQL, c.searchLimit, c.searchCursor)
	}
	if c.tableSummaryReference != "42" || c.tableSummaryIndex != 2 || c.tableExtractReference != "42" || c.tableExtractIndex != 2 {
		t.Fatalf("confluence table calls=%+v", c)
	}
}

func TestToolErrorsExposeStableClassification(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_fields", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("error content=%q: %v", text.Text, err)
	}
	if got.Kind != "configuration_error" || got.Remediation != "complete_configuration" || strings.Contains(strings.ToLower(got.Message), "token") {
		t.Fatalf("classified error=%+v", got)
	}
}

func TestToolErrorsDoNotExposeBackendPathOrBody(t *testing.T) {
	err := classified(&httpx.APIError{
		Status: 400, Method: "GET",
		Path: "/rest/api/2/search?jql=project%3DPRIVATE",
		Body: "query project=PRIVATE was rejected",
	})
	var got toolError
	if !errors.As(err, &got) {
		t.Fatalf("error=%T %v", err, err)
	}
	encoded := got.Error()
	if got.Kind != "api_error" || got.Message != "backend returned HTTP 400" || strings.Contains(encoded, "PRIVATE") || strings.Contains(encoded, "/rest/") {
		t.Fatalf("classified error=%s", encoded)
	}
	transport := classified(&httpx.TransportError{Method: "GET", Category: "dns"})
	got = toolError{}
	if !errors.As(transport, &got) || got.Kind != "transport_error" || got.Message != "backend transport failed (dns)" {
		t.Fatalf("transport error=%v", transport)
	}
}

func TestToolBoundsFailBeforeBackendResolution(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "jira_issue_search", args: map[string]any{"jql": "project=PROJ", "limit": 1001}},
		{name: "jira_issue_field_get", args: map[string]any{"key": "PROJ-1", "field": "Delivery Notes", "max_bytes": 128}},
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "limit": 1001}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"comments"}, "comment_limit": 51}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"confluence"}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "projection": "brief"}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "limit": 101}},
		{name: "confluence_page_section", args: map[string]any{"reference": "1", "heading": "Results", "max_bytes": 1048577}},
		{name: "confluence_table_summary", args: map[string]any{"reference": "1", "table": -1}},
		{name: "confluence_table_summary", args: map[string]any{"reference": "1", "max_bytes": 1023}},
		{name: "confluence_table_extract", args: map[string]any{"reference": "1", "table": 0}},
		{name: "confluence_table_extract", args: map[string]any{"reference": "1", "table": 1, "max_bytes": 1048577}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("result=%+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("error content=%q: %v", text.Text, err)
			}
			if got.Kind != "usage_error" || got.Remediation != "fix_request" {
				t.Fatalf("classified error=%+v", got)
			}
		})
	}
}

func TestConfluenceTableOutputBoundFailsWithoutLeakingContent(t *testing.T) {
	reader := &recordingConfluenceReader{tableText: "PRIVATE-MARKER-" + strings.Repeat("x", 4<<10)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "confluence_table_extract", Arguments: map[string]any{
		"reference": "42", "table": 1, "max_bytes": 1024,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || strings.Contains(text.Text, "PRIVATE-MARKER") {
		t.Fatalf("error content=%#v", result.Content)
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestConfluenceTableToolsRejectUnreconciledApplicationResults(t *testing.T) {
	for _, test := range []struct {
		name, tool, mode string
		args             map[string]any
	}{
		{name: "summary selection", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-selection"},
		{name: "summary rectangular", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-rectangular"},
		{name: "summary cell count", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-cell-count"},
		{name: "extract selection", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract"},
		{name: "extract dimensions", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-dimensions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &invalidConfluenceTableReader{recordingConfluenceReader: &recordingConfluenceReader{}, mode: test.mode}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
				t.Fatalf("result=%+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
		})
	}
}

func TestConfluenceTableErrorsNeverExposeParserContent(t *testing.T) {
	marker := "SYNTHETIC-SECRET-ENTITY"
	_, parserErr := app.ExtractTablesFromCSF("42", "Synthetic", []byte("<table><tr><td>&"+marker+";</td></tr></table>"), 1)
	if parserErr == nil || !strings.Contains(parserErr.Error(), marker) {
		t.Fatalf("test fixture must produce a content-bearing parser error: %v", parserErr)
	}
	for _, tool := range []string{"confluence_table_summary", "confluence_table_extract"} {
		t.Run(tool, func(t *testing.T) {
			reader := &recordingConfluenceReader{tableErr: parserErr}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			args := map[string]any{"reference": "42"}
			if tool == "confluence_table_extract" {
				args["table"] = 1
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(marker)) {
				t.Fatalf("result leaked parser content: %s", encoded)
			}
		})
	}
}

func TestToolCancellationPropagatesToApplicationContext(t *testing.T) {
	reader := &cancellingJiraReader{started: make(chan struct{}), canceled: make(chan struct{})}
	server := New("test", Dependencies{Jira: func() (JiraReader, error) { return reader, nil }})
	client, closeSessions := connectTestClient(t, server)
	defer closeSessions()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "jira_fields", Arguments: map[string]any{}})
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool handler did not start")
	}
	cancel()
	select {
	case <-reader.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("application context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client call did not return after cancellation")
	}
}

func TestConfluenceTableCancellationPropagatesToApplicationContext(t *testing.T) {
	reader := &cancellingConfluenceReader{started: make(chan struct{}), canceled: make(chan struct{})}
	server := New("test", Dependencies{Confluence: func() (ConfluenceReader, error) { return reader, nil }})
	client, closeSessions := connectTestClient(t, server)
	defer closeSessions()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "confluence_table_summary", Arguments: map[string]any{"reference": "42"}})
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("table tool handler did not start")
	}
	cancel()
	select {
	case <-reader.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("table application context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("table client call did not return after cancellation")
	}
}

func connectTestClient(t *testing.T, server *mcp.Server) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "atl-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		cancel()
	}
}

func callToolOK(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s: %+v", name, result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatalf("%s has no structured output", name)
	}
	return result
}

type cancellingJiraReader struct {
	started  chan struct{}
	canceled chan struct{}
}

type cancellingConfluenceReader struct {
	started  chan struct{}
	canceled chan struct{}
}

type recordingJiraReader struct {
	fieldOpts                           app.JiraFieldCatalogOpts
	fieldEvidenceKey                    string
	fieldEvidenceOpts                   app.JiraIssueFieldEvidenceOpts
	searchJQL, searchView, searchCursor string
	searchColumns                       []string
	searchLimit                         int
	digestKey                           string
	digestOpts                          app.JiraEpicDigestOpts
	boardID                             int
	boardOpts                           app.BoardSnapshotOpts
}

func (r *recordingJiraReader) IssueFieldEvidence(_ context.Context, key string, opts app.JiraIssueFieldEvidenceOpts) (*app.JiraIssueFieldEvidenceResult, error) {
	r.fieldEvidenceKey, r.fieldEvidenceOpts = key, opts
	return &app.JiraIssueFieldEvidenceResult{
		SchemaVersion: 1, Issue: app.JiraIssueFieldEvidenceIssue{Key: key, Updated: "2026-01-01T00:00:00Z"},
		Field:      app.JiraIssueFieldEvidenceField{ID: "customfield_1", Name: "Delivery Notes", Present: true, ValueType: "string"},
		Projection: "compact", MaxValueBytes: opts.MaxBytes, OriginalValueBytes: 7, EmittedValueBytes: 7, Complete: true, Value: "value",
	}, nil
}

func (r *recordingJiraReader) FieldCatalog(_ context.Context, opts app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	r.fieldOpts = opts
	return &app.JiraFieldCatalogResult{SchemaVersion: 1, Source: "test", Complete: true, Fields: []domain.FieldDef{}}, nil
}

func (r *recordingJiraReader) SearchIssueListView(_ context.Context, jql string, columns []string, view string, limit int, cursor string) (*app.IssueList, error) {
	r.searchJQL, r.searchColumns, r.searchView, r.searchLimit, r.searchCursor = jql, columns, view, limit, cursor
	return app.NewIssueList(app.IssueListSource{Kind: "jql"}, map[string]any{}, []string{"key"}, []string{}, "backend", []domain.Issue{}, nil, ""), nil
}

func (r *recordingJiraReader) EpicDigest(_ context.Context, key string, opts app.JiraEpicDigestOpts) (*app.JiraEpicDigestResult, error) {
	r.digestKey, r.digestOpts = key, opts
	return &app.JiraEpicDigestResult{Includes: []string{}, Sources: map[string]app.JiraDigestSource{}, Staleness: app.JiraDigestStaleness{Reasons: []string{}}}, nil
}

func (r *recordingJiraReader) BoardSnapshot(_ context.Context, id int, opts app.BoardSnapshotOpts) (*app.BoardSnapshot, error) {
	r.boardID, r.boardOpts = id, opts
	return &app.BoardSnapshot{
		Board:      &domain.BoardConfiguration{Columns: []domain.BoardColumn{}},
		Projection: app.BoardProjection{Columns: []string{}, Fields: []string{}}, Rows: []app.BoardSnapshotRow{},
	}, nil
}

type recordingConfluenceReader struct {
	searchCQL, searchCursor                              string
	searchLimit                                          int
	resolveReference, outlineReference, sectionReference string
	sectionOpts                                          app.ConfluencePageSectionOpts
	tableSummaryReference, tableExtractReference         string
	tableSummaryIndex, tableExtractIndex                 int
	tableText                                            string
	tableErr                                             error
}

type invalidConfluenceTableReader struct {
	*recordingConfluenceReader
	mode string
}

func (r *recordingConfluenceReader) SearchQualified(_ context.Context, cql string, limit int, cursor string) (*app.ConfluenceSearchResult, error) {
	r.searchCQL, r.searchLimit, r.searchCursor = cql, limit, cursor
	return &app.ConfluenceSearchResult{SchemaVersion: 1, Results: []domain.PageRef{}, Complete: true}, nil
}

func (r *recordingConfluenceReader) ResolvePageReference(_ context.Context, reference string) (*app.ConfluencePageResolution, error) {
	r.resolveReference = reference
	return &app.ConfluencePageResolution{}, nil
}

func (r *recordingConfluenceReader) PageOutline(_ context.Context, reference string) (*app.ConfluencePageOutlineResult, error) {
	r.outlineReference = reference
	return &app.ConfluencePageOutlineResult{Headings: []app.ConfluenceOutlineEntry{}}, nil
}

func (r *recordingConfluenceReader) PageSection(_ context.Context, reference string, opts app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error) {
	r.sectionReference, r.sectionOpts = reference, opts
	return &app.ConfluencePageSectionResult{Path: []string{}}, nil
}

func (r *recordingConfluenceReader) SummarizeTables(_ context.Context, reference string, table int) (*app.ConfluenceTableSummary, error) {
	r.tableSummaryReference, r.tableSummaryIndex = reference, table
	if r.tableErr != nil {
		return nil, r.tableErr
	}
	tables := []app.ConfluenceTableSummaryRecord{{Index: table, RowCount: 1, ColumnCount: 1, Rectangular: true, CellCountReconciled: true}}
	if table == 0 {
		tables = []app.ConfluenceTableSummaryRecord{{Index: 1, RowCount: 1, ColumnCount: 1, Rectangular: true, CellCountReconciled: true},
			{Index: 2, RowCount: 1, ColumnCount: 1, Rectangular: true, CellCountReconciled: true}}
	}
	return &app.ConfluenceTableSummary{PageID: "42", TableCount: 2, Table: table, ReturnedTableCount: len(tables),
		SelectionReconciled: true, Tables: tables}, nil
}

func (r *invalidConfluenceTableReader) SummarizeTables(ctx context.Context, reference string, table int) (*app.ConfluenceTableSummary, error) {
	result, err := r.recordingConfluenceReader.SummarizeTables(ctx, reference, table)
	switch r.mode {
	case "summary-selection":
		result.SelectionReconciled = false
	case "summary-rectangular":
		result.Tables[0].Rectangular = false
	case "summary-cell-count":
		result.Tables[0].CellCountReconciled = false
	}
	return result, err
}

func (r *invalidConfluenceTableReader) ExtractTables(ctx context.Context, reference string, table int) (*app.ConfluenceTableExtract, error) {
	result, err := r.recordingConfluenceReader.ExtractTables(ctx, reference, table)
	switch r.mode {
	case "extract":
		result.Tables = append(result.Tables, result.Tables[0])
	case "extract-dimensions":
		result.Tables[0].RowCount++
	}
	return result, err
}

func (r *recordingConfluenceReader) ExtractTables(_ context.Context, reference string, table int) (*app.ConfluenceTableExtract, error) {
	r.tableExtractReference, r.tableExtractIndex = reference, table
	if r.tableErr != nil {
		return nil, r.tableErr
	}
	return &app.ConfluenceTableExtract{PageID: "42", TableCount: 2, Table: table, Tables: []app.ConfluenceTable{{Index: table,
		RowCount: 1, ColumnCount: 1, Rows: []app.ConfluenceTableRow{{Index: 1,
			Cells: []app.ConfluenceTableCell{{Row: 1, Column: 1, Text: r.tableText}}}}}}}, nil
}

func (r *cancellingJiraReader) FieldCatalog(ctx context.Context, _ app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (*cancellingJiraReader) IssueFieldEvidence(context.Context, string, app.JiraIssueFieldEvidenceOpts) (*app.JiraIssueFieldEvidenceResult, error) {
	panic("unexpected call")
}

func (*cancellingJiraReader) SearchIssueListView(context.Context, string, []string, string, int, string) (*app.IssueList, error) {
	panic("unexpected call")
}

func (*cancellingJiraReader) EpicDigest(context.Context, string, app.JiraEpicDigestOpts) (*app.JiraEpicDigestResult, error) {
	panic("unexpected call")
}

func (*cancellingJiraReader) BoardSnapshot(context.Context, int, app.BoardSnapshotOpts) (*app.BoardSnapshot, error) {
	panic("unexpected call")
}

func (r *cancellingConfluenceReader) SummarizeTables(ctx context.Context, _ string, _ int) (*app.ConfluenceTableSummary, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (*cancellingConfluenceReader) SearchQualified(context.Context, string, int, string) (*app.ConfluenceSearchResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) ResolvePageReference(context.Context, string) (*app.ConfluencePageResolution, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) PageOutline(context.Context, string) (*app.ConfluencePageOutlineResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) PageSection(context.Context, string, app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) ExtractTables(context.Context, string, int) (*app.ConfluenceTableExtract, error) {
	panic("unexpected call")
}
