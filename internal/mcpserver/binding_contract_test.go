package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/domain"
)

// TestToolProfileAndConfigClassifierBindings is deliberately independent of
// the capability catalog and registration helpers. It freezes which closed
// service profile owns each tool, which dependency the handler constructs,
// and which client-safe policy classifies a configuration failure.
func TestToolProfileAndConfigClassifierBindings(t *testing.T) {
	type binding struct {
		name          string
		service       ServiceProfile
		offline       bool
		args          map[string]any
		configMessage string
	}
	bindings := []binding{
		{"confluence_attachment_list", ServiceConfluence, false, map[string]any{"reference": "42", "expected_page_version": 1}, "Confluence attachment inventory service is not configured"},
		{"confluence_comment_list", ServiceConfluence, false, map[string]any{"page_id": "42"}, "Confluence comment service is not configured"},
		{"confluence_comment_thread", ServiceConfluence, false, map[string]any{"page_id": "42", "comment_id": "7"}, "Confluence comment service is not configured"},
		{"confluence_mirror_snapshot", ServiceConfluence, true, map[string]any{}, "local mirror root is not configured or is invalid"},
		{"confluence_page_meta", ServiceConfluence, false, map[string]any{"reference": "42"}, "Confluence page metadata service is not configured"},
		{"confluence_page_outline", ServiceConfluence, false, map[string]any{"reference": "42"}, "Confluence page outline service is not configured"},
		{"confluence_page_resolve", ServiceConfluence, false, map[string]any{"reference": "42"}, "tool request failed"},
		{"confluence_page_section", ServiceConfluence, false, map[string]any{"reference": "42", "heading": "Summary"}, "Confluence page section service is not configured"},
		{"confluence_page_sections", ServiceConfluence, false, map[string]any{"reference": "42", "selectors": []any{map[string]any{"heading": "Summary"}}}, "Confluence page section service is not configured"},
		{"confluence_search", ServiceConfluence, false, map[string]any{"cql": "type=page"}, "tool request failed"},
		{"confluence_table_extract", ServiceConfluence, false, map[string]any{"reference": "42", "table": 1}, "Confluence table service is not configured"},
		{"confluence_table_summary", ServiceConfluence, false, map[string]any{"reference": "42"}, "Confluence table service is not configured"},
		{"jira_board_view", ServiceJira, false, map[string]any{"board_id": 1}, "tool request failed"},
		{"jira_epic_digest", ServiceJira, false, map[string]any{"key": "PROJ-1", "include": []any{"identity"}}, "tool request failed"},
		{"jira_fields", ServiceJira, false, map[string]any{}, "tool request failed"},
		{"jira_issue_field_get", ServiceJira, false, map[string]any{"key": "PROJ-1", "field": "summary"}, "tool request failed"},
		{"jira_issue_graph", ServiceJira, false, map[string]any{"key": "PROJ-1"}, "Jira issue graph service is not configured"},
		{"jira_issue_history", ServiceJira, false, map[string]any{"key": "PROJ-1"}, "tool request failed"},
		{"jira_issue_refs", ServiceJira, false, map[string]any{"key": "PROJ-1"}, "Jira issue reference summary service is not configured"},
		{"jira_issue_search", ServiceJira, false, map[string]any{"jql": "project = PROJ"}, "tool request failed"},
		{"jira_mirror_snapshot", ServiceJira, true, map[string]any{}, "local mirror root is not configured or is invalid"},
		{"jira_structure_get", ServiceJira, false, map[string]any{"structure_id": 1}, "Jira Structure service is not configured"},
		{"jira_structure_view", ServiceJira, false, map[string]any{"structure_id": 1}, "Jira Structure service is not configured"},
	}
	if len(bindings) != 23 {
		t.Fatalf("binding rows=%d want=23", len(bindings))
	}

	wantProfile := map[ServiceProfile][]string{
		ServiceDefault:    {},
		ServiceJira:       {},
		ServiceConfluence: {},
		ServiceOffline:    {},
	}
	for _, row := range bindings {
		wantProfile[ServiceDefault] = append(wantProfile[ServiceDefault], row.name)
		wantProfile[row.service] = append(wantProfile[row.service], row.name)
		if row.offline {
			wantProfile[ServiceOffline] = append(wantProfile[ServiceOffline], row.name)
		}
	}
	for profile, want := range wantProfile {
		client, closeSessions := connectTestClient(t, NewForService("test", Dependencies{}, profile))
		listed, err := client.ListTools(context.Background(), nil)
		closeSessions()
		if err != nil {
			t.Fatalf("profile %q list: %v", profile, err)
		}
		got := make([]string, 0, len(listed.Tools))
		for _, tool := range listed.Tools {
			got = append(got, tool.Name)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Fatalf("profile %q tools=%v want=%v", profile, got, want)
		}
	}

	var jiraCalls, confluenceCalls, mirrorCalls int
	configErr := fmt.Errorf("%w: synthetic constructor detail", domain.ErrConfig)
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			jiraCalls++
			return nil, configErr
		},
		Confluence: func() (ConfluenceReader, error) {
			confluenceCalls++
			return nil, configErr
		},
		MirrorRoot: func() (string, error) {
			mirrorCalls++
			return "", configErr
		},
	}))
	defer closeSessions()
	for _, row := range bindings {
		t.Run(row.name, func(t *testing.T) {
			beforeJira, beforeConfluence, beforeMirror := jiraCalls, confluenceCalls, mirrorCalls
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: row.name, Arguments: row.args})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("decode %q: %v", text.Text, err)
			}
			if got.Kind != "configuration_error" || got.Remediation != "complete_configuration" || got.Message != row.configMessage {
				t.Fatalf("error=%+v", got)
			}
			wantJira, wantConfluence, wantMirror := 0, 0, 0
			if row.offline {
				wantMirror = 1
			} else if row.service == ServiceJira {
				wantJira = 1
			} else {
				wantConfluence = 1
			}
			if jiraCalls-beforeJira != wantJira || confluenceCalls-beforeConfluence != wantConfluence || mirrorCalls-beforeMirror != wantMirror {
				t.Fatalf("constructor deltas jira=%d confluence=%d mirror=%d want=%d/%d/%d",
					jiraCalls-beforeJira, confluenceCalls-beforeConfluence, mirrorCalls-beforeMirror,
					wantJira, wantConfluence, wantMirror)
			}
		})
	}
}
