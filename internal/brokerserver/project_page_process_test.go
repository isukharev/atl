//go:build !windows

package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/brokercontract"
)

type projectPageProcessField struct {
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Null    bool   `json:"null"`
	Value   string `json:"value"`
}

type projectPageProcessIssue struct {
	ID         string                    `json:"id"`
	Key        string                    `json:"key"`
	ProjectID  string                    `json:"project_id"`
	ProjectKey string                    `json:"project_key"`
	Updated    string                    `json:"updated"`
	Fields     []projectPageProcessField `json:"fields"`
}

type projectPageProcessPage struct {
	StartAt             int    `json:"start_at"`
	MaxResults          int    `json:"max_results"`
	Total               int    `json:"total"`
	Count               int    `json:"count"`
	NextCursor          string `json:"next_cursor"`
	NextCursorPresent   bool   `json:"next_cursor_present"`
	CoordinateExhausted bool   `json:"coordinate_exhausted"`
	SelectionComplete   bool   `json:"selection_complete"`
	PartialReason       string `json:"partial_reason"`
}

type projectPageProcessResult struct {
	SchemaVersion      int                       `json:"schema_version"`
	ArgumentsSHA256    string                    `json:"arguments_sha256"`
	ConsistencyProfile string                    `json:"consistency_profile"`
	ProjectID          string                    `json:"project_id"`
	ProjectKey         string                    `json:"project_key"`
	Issues             []projectPageProcessIssue `json:"issues"`
	Page               projectPageProcessPage    `json:"page"`
	Complete           bool                      `json:"complete"`
}

func TestSelectedProjectPageCLIAndMCPProcessOracle(t *testing.T) {
	// Build before creating any expiring authentication, discovery, decision,
	// or backend fixtures. The positive paths are unconditional: the integrated
	// head must enable the registry and wire both public consumers.
	binary := buildSelectedATLBinary(t, "")

	t.Run("CLI first and fresh second page", func(t *testing.T) {
		fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{})
		first := runProjectPageProcessCLI(t, binary, fixture.environment,
			"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "0")
		firstPage := requireProjectPageProcessSuccess(t, first)
		assertProjectPageProcessFirstPage(t, firstPage)
		fixture.assertCounts(projectPageProcessCountSnapshot{
			brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1,
			admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1,
		})

		// Deterministically refill the Host's bounded data admission tokens;
		// paging freshness is asserted by the doubled route/phase counters.
		fixture.advanceHostClock(time.Second)
		second := runProjectPageProcessCLI(t, binary, fixture.environment,
			"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "2")
		secondPage := requireProjectPageProcessSuccess(t, second)
		if len(secondPage.Issues) != 1 || secondPage.Issues[0].ID != "100" || secondPage.Page.StartAt != 2 || secondPage.Page.Count != 1 ||
			!secondPage.Page.CoordinateExhausted || secondPage.Page.SelectionComplete || secondPage.Page.NextCursorPresent || secondPage.Page.NextCursor != "" || secondPage.Page.PartialReason != "" || !secondPage.Complete {
			t.Fatalf("second page=%+v", secondPage)
		}
		fixture.assertCounts(projectPageProcessCountSnapshot{
			brokerNegotiate: 2, brokerDiscovery: 2, brokerExecute: 2, authentication: 6, discovery: 2,
			admission: 2, qualification: 2, operation: 2, jiraProject: 2, jiraIdentity: 2, jiraBusiness: 2,
		})
		fixture.assertNoViolations()
	})

	t.Run("MCP structured and text projections", func(t *testing.T) {
		fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{})
		result, stderr := runProjectPageProcessMCP(t, binary, fixture.environment, map[string]any{
			"project_key": "PROJ", "fields": []string{"summary", "description"}, "limit": 2, "cursor": "0", "max_bytes": 65536,
		})
		if result == nil || result.IsError || result.StructuredContent == nil || stderr != "" {
			fixture.assertNoViolations()
			encoded, encodeErr := json.Marshal(result)
			t.Fatalf("MCP result=%s encode_error=%v counts=%+v stderr=%s", encoded, encodeErr, fixture.counters.snapshot(), stderr)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		page := decodeProjectPageProcessResult(t, encoded)
		assertProjectPageProcessFirstPage(t, page)
		if len(result.Content) != 1 {
			t.Fatalf("unexpected MCP content count: %d", len(result.Content))
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || len(text.Text) >= 65536 {
			t.Fatal("missing or oversized MCP text projection")
		}
		textPage := decodeProjectPageProcessResult(t, []byte(text.Text))
		if !reflect.DeepEqual(textPage, page) {
			t.Fatal("MCP structured and user-visible text projections differ")
		}
		fixture.assertCounts(projectPageProcessCountSnapshot{
			brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1,
			admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1,
		})
		fixture.assertNoViolations()
	})

	t.Run("forbidden sibling denies complete page", func(t *testing.T) {
		fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{denyForbiddenSibling: true, jiraMode: "forbidden"})
		result := runProjectPageProcessCLI(t, binary, fixture.environment,
			"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "0")
		requireProjectPageProcessFailure(t, result)
		fixture.assertCounts(projectPageProcessCountSnapshot{
			brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1,
			admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1,
		})
		fixture.assertNoViolations()
	})

	t.Run("invalid public field stops before all IO", func(t *testing.T) {
		fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{})
		result := runProjectPageProcessCLI(t, binary, fixture.environment,
			"jira", "issue", "project-page", "--project", "PROJ", "--fields", "status", "--limit", "2", "--cursor", "0")
		requireProjectPageProcessFailure(t, result)
		fixture.assertCounts(projectPageProcessCountSnapshot{})
		fixture.assertNoViolations()
	})

	for _, test := range []struct {
		name        string
		missingPath string
		want        projectPageProcessCountSnapshot
	}{
		{name: "missing discovery", missingPath: "/v3/discovery", want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, authentication: 2, discovery: 1}},
		{name: "missing admission", missingPath: "/v2/authorize/project-page/admission", want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1}},
		{name: "missing qualification", missingPath: "/v2/authorize/project-page/qualification", want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1}},
		{name: "missing final", missingPath: "/v2/authorize/project-page/operation", want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{missingAuthorityPath: test.missingPath})
			result := runProjectPageProcessCLI(t, binary, fixture.environment,
				"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "0")
			requireProjectPageProcessFailure(t, result)
			fixture.assertCounts(test.want)
			fixture.assertNoViolations()
		})
	}

	for _, test := range []struct {
		name    string
		options projectPageProcessOptions
		want    projectPageProcessCountSnapshot
	}{
		{name: "extra backend field", options: projectPageProcessOptions{jiraMode: "extra_field"}, want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1}},
		{name: "backend order drift", options: projectPageProcessOptions{jiraMode: "order_drift"}, want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1}},
		{name: "authority revision drift", options: projectPageProcessOptions{finalRevisionDrift: true}, want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageProcessFixture(t, test.options)
			result := runProjectPageProcessCLI(t, binary, fixture.environment,
				"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "0")
			requireProjectPageProcessFailure(t, result)
			fixture.assertCounts(test.want)
			fixture.assertNoViolations()
		})
	}

	for _, test := range []struct {
		name    string
		options projectPageProcessOptions
		want    projectPageProcessCountSnapshot
	}{
		{name: "credential replaced after discovery", options: projectPageProcessOptions{replaceAfterDiscovery: true}, want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, authentication: 2, discovery: 1}},
		{name: "credential replaced after buffered response", options: projectPageProcessOptions{replaceAfterBuffer: true}, want: projectPageProcessCountSnapshot{brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1, admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageProcessFixture(t, test.options)
			result := runProjectPageProcessCLI(t, binary, fixture.environment,
				"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", "0")
			requireProjectPageProcessFailure(t, result)
			fixture.assertCounts(test.want)
			fixture.assertNoViolations()
		})
	}

	for _, test := range []struct {
		name          string
		mode          string
		cursor        string
		wantCount     int
		wantReason    string
		wantExhausted bool
	}{
		{name: "stalled", mode: "stalled", cursor: "0", wantReason: brokercontract.BrokerProjectPagePartialPaginationStalledV2},
		{name: "offset limit", mode: "offset_limit", cursor: "1000000", wantCount: 1, wantReason: brokercontract.BrokerProjectPagePartialOffsetLimitV2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageProcessFixture(t, projectPageProcessOptions{jiraMode: test.mode})
			result := runProjectPageProcessCLI(t, binary, fixture.environment,
				"jira", "issue", "project-page", "--project", "PROJ", "--fields", "summary,description", "--limit", "2", "--cursor", test.cursor)
			page := requireProjectPageProcessSuccess(t, result)
			if page.Page.Count != test.wantCount || page.Page.PartialReason != test.wantReason || page.Page.CoordinateExhausted != test.wantExhausted || page.Page.SelectionComplete || page.Page.NextCursorPresent || page.Page.NextCursor != "" || !page.Complete {
				t.Fatalf("page=%+v", page)
			}
			fixture.assertCounts(projectPageProcessCountSnapshot{
				brokerNegotiate: 1, brokerDiscovery: 1, brokerExecute: 1, authentication: 3, discovery: 1,
				admission: 1, qualification: 1, operation: 1, jiraProject: 1, jiraIdentity: 1, jiraBusiness: 1,
			})
			fixture.assertNoViolations()
		})
	}
}

func (f *projectPageProcessFixture) assertCounts(want projectPageProcessCountSnapshot) {
	f.t.Helper()
	got := f.counters.snapshot()
	if !reflect.DeepEqual(got, want) {
		f.t.Fatalf("counts=%+v want=%+v", got, want)
	}
}

func requireProjectPageProcessSuccess(t *testing.T, result selectedCacheCLIResult) projectPageProcessResult {
	t.Helper()
	if result.exitCode != 0 || result.stderr != "" || result.stdout == "" {
		t.Fatalf("selected CLI exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	return decodeProjectPageProcessResult(t, []byte(result.stdout))
}

func decodeProjectPageProcessResult(t *testing.T, body []byte) projectPageProcessResult {
	t.Helper()
	var result projectPageProcessResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode project-page result: %v body=%s", err, body)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("project-page result has trailing data: %v body=%s", err, body)
	}
	return result
}

func assertProjectPageProcessFirstPage(t *testing.T, result projectPageProcessResult) {
	t.Helper()
	if result.SchemaVersion != 2 || len(result.ArgumentsSHA256) != 64 || result.ConsistencyProfile != "identity_snapshot_v1" || result.ProjectID != "100" || result.ProjectKey != "PROJ" ||
		len(result.Issues) != 2 || result.Issues[0].ID != "20" || result.Issues[1].ID != "3" || result.Issues[0].Key != "PROJ-20" ||
		result.Page.StartAt != 0 || result.Page.MaxResults != 2 || result.Page.Total != 3 || result.Page.Count != 2 || result.Page.NextCursor != "2" || !result.Page.NextCursorPresent ||
		result.Page.CoordinateExhausted || result.Page.SelectionComplete || result.Page.PartialReason != "" || !result.Complete {
		t.Fatalf("first page=%+v", result)
	}
	for index, id := range []string{"20", "3"} {
		wantFields := []projectPageProcessField{
			{Field: "description", Present: true, Value: "Native *wiki* body " + id},
			{Field: "summary", Present: true, Value: "Synthetic summary " + id},
		}
		if !reflect.DeepEqual(result.Issues[index].Fields, wantFields) {
			t.Fatalf("issue %s fields=%+v want=%+v", id, result.Issues[index].Fields, wantFields)
		}
	}
}

func requireProjectPageProcessFailure(t *testing.T, result selectedCacheCLIResult) {
	t.Helper()
	if result.exitCode == 0 || result.stdout != "" || result.stderr == "" {
		t.Fatalf("expected selected CLI failure: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	combined := result.stdout + result.stderr
	for _, private := range []string{
		projectPageProcessRemoteCanary, projectPageProcessWorkloadCredential, projectPageProcessReplacementCredential,
		projectPageProcessAuthorityCredential, projectPageProcessJiraCredential, "Native *wiki* body",
	} {
		if strings.Contains(combined, private) {
			t.Fatalf("selected CLI failure disclosed %q: %s", private, combined)
		}
	}
}

func runProjectPageProcessMCP(t *testing.T, binary string, environment []string, arguments map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--service", "jira")
	command.Env = environment
	command.WaitDelay = 2 * time.Second
	var stderr selectedCacheCLIOutput
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "project-page-process-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect selected MCP process: %v stderr=%s", err, stderr.String())
	}
	result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "jira_project_issue_page", Arguments: arguments})
	closeErr := session.Close()
	if callErr != nil || closeErr != nil || ctx.Err() != nil || stderr.exceeded {
		t.Fatalf("selected MCP call=%v close=%v context=%v exceeded=%t stderr=%s", callErr, closeErr, ctx.Err(), stderr.exceeded, stderr.String())
	}
	return result, stderr.String()
}
