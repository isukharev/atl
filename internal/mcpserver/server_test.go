package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
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
		"confluence_page_outline", "confluence_page_resolve", "confluence_page_section",
		"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_search",
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
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", tool.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tools=%v want=%v", got, want)
	}
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
	callToolOK(t, client, "jira_fields", map[string]any{})
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
	callToolOK(t, client, "confluence_page_resolve", map[string]any{"reference": "/x/Abc"})
	callToolOK(t, client, "confluence_page_outline", map[string]any{"reference": "42"})
	callToolOK(t, client, "confluence_page_section", map[string]any{
		"reference": "42", "heading": "Results", "occurrence": 2,
	})

	if j.fieldOpts.Custom != "true" || j.fieldOpts.NameLike != "Outcome" {
		t.Fatalf("field opts=%+v", j.fieldOpts)
	}
	if j.searchJQL != "project=PROJ" || j.searchLimit != 50 || j.searchCursor != "next" || j.searchView != "compact" || strings.Join(j.searchColumns, ",") != "key,status" {
		t.Fatalf("search jql=%q columns=%v view=%q limit=%d cursor=%q", j.searchJQL, j.searchColumns, j.searchView, j.searchLimit, j.searchCursor)
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
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "limit": 1001}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"comments"}, "comment_limit": 51}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"confluence"}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "projection": "brief"}},
		{name: "confluence_page_section", args: map[string]any{"reference": "1", "heading": "Results", "max_bytes": 1048577}},
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

type recordingJiraReader struct {
	fieldOpts                           app.JiraFieldCatalogOpts
	searchJQL, searchView, searchCursor string
	searchColumns                       []string
	searchLimit                         int
	digestKey                           string
	digestOpts                          app.JiraEpicDigestOpts
	boardID                             int
	boardOpts                           app.BoardSnapshotOpts
}

func (r *recordingJiraReader) FieldCatalog(_ context.Context, opts app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	r.fieldOpts = opts
	return &app.JiraFieldCatalogResult{Fields: []domain.FieldDef{}}, nil
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
	resolveReference, outlineReference, sectionReference string
	sectionOpts                                          app.ConfluencePageSectionOpts
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

func (r *cancellingJiraReader) FieldCatalog(ctx context.Context, _ app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
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
