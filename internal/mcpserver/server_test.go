package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/testbackend"
)

func TestServerAdvertisesOnlyTypedReadOnlyTools(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()

	initialized := client.InitializeResult()
	if initialized == nil || initialized.Instructions != Instructions || initialized.ServerInfo.Name != "atl" {
		t.Fatalf("initialize=%+v", initialized)
	}
	if initialized.ProtocolVersion != "2025-11-25" {
		t.Fatalf("protocol version=%q want 2025-11-25", initialized.ProtocolVersion)
	}
	if !strings.Contains(initialized.Instructions, "columns (preferred), fields, or projection") {
		t.Fatalf("initialize instructions do not disambiguate Jira search field selection: %q", initialized.Instructions)
	}
	for _, guidance := range []string{
		"confluence_page_meta", "body-free page identity", "restricted, unrestricted, or unknown",
		"omits labels, ancestors, URLs, principals, and page content",
	} {
		if !strings.Contains(initialized.Instructions, guidance) {
			t.Fatalf("initialize instructions omit page-metadata guidance %q: %q", guidance, initialized.Instructions)
		}
	}
	for _, guidance := range []string{"jira_issue_refs", "raw reference URLs and issue narrative are deliberately omitted"} {
		if !strings.Contains(initialized.Instructions, guidance) {
			t.Fatalf("initialize instructions omit reference-summary guidance %q: %q", guidance, initialized.Instructions)
		}
	}
	// The section gate is conditional, so the server-level guidance has to state
	// all three cases; an agent that reads only "pass a version" would either
	// bind an externally fixed selection it cannot bind or skip the binding that
	// makes an outline-derived occurrence attributable.
	for _, guidance := range []string{
		"confluence_page_outline result", "first section result's version", "explicitly ungated read that reconciles nothing",
	} {
		if !strings.Contains(initialized.Instructions, guidance) {
			t.Fatalf("initialize instructions omit section-gate guidance %q: %q", guidance, initialized.Instructions)
		}
	}
	for _, guidance := range []string{
		"table index came from confluence_table_summary", "expected_page_version",
		"externally fixed index",
	} {
		if !strings.Contains(initialized.Instructions, guidance) {
			t.Fatalf("initialize instructions omit table-gate guidance %q: %q", guidance, initialized.Instructions)
		}
	}
	for _, guidance := range []string{
		"forest_version.signature", "expected_forest_signature",
		"explicitly ungated selection", "non-bindable",
		"folder labels are separately timed",
	} {
		if !strings.Contains(initialized.Instructions, guidance) {
			t.Fatalf("initialize instructions omit Structure forest-gate guidance %q: %q", guidance, initialized.Instructions)
		}
	}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"confluence_attachment_list", "confluence_mirror_snapshot",
		"confluence_page_meta", "confluence_page_outline", "confluence_page_resolve", "confluence_page_section", "confluence_page_sections", "confluence_search",
		"confluence_table_extract", "confluence_table_summary",
		"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_graph",
		"jira_issue_history", "jira_issue_refs", "jira_issue_search", "jira_mirror_snapshot", "jira_structure_get", "jira_structure_view",
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
		if tool.Name == "confluence_search" {
			properties, _ := input["properties"].(map[string]any)
			maxBytes, _ := properties["max_bytes"].(map[string]any)
			description, _ := maxBytes["description"].(string)
			if !schemaRequired(input, "cql") ||
				!strings.Contains(description, "1024 to 1048576") ||
				!strings.Contains(description, "default 131072") {
				t.Errorf("tool %s must require CQL and advertise its byte bound: %#v", tool.Name, tool.InputSchema)
			}
		}
		if tool.Name == "jira_issue_search" {
			properties, _ := input["properties"].(map[string]any)
			columns, _ := properties["columns"].(map[string]any)
			fields, _ := properties["fields"].(map[string]any)
			projection, projectionExists := properties["projection"].(map[string]any)
			columnsDescription, _ := columns["description"].(string)
			fieldsDescription, _ := fields["description"].(string)
			projectionDescription, _ := projection["description"].(string)
			if !strings.Contains(tool.Description, "`columns` (preferred), `fields`, or `projection`") ||
				!strings.Contains(columnsDescription, "columns, fields, or projection") ||
				!strings.Contains(fieldsDescription, "columns, fields, or projection") ||
				!projectionExists || !strings.Contains(projectionDescription, "compatibility alias for columns") {
				t.Errorf("tool %s field selection guidance is ambiguous: description=%q columns=%#v fields=%#v projection=%#v",
					tool.Name, tool.Description, columns, fields, projection)
			}
		}
		if tool.Name == "jira_issue_history" {
			properties, _ := input["properties"].(map[string]any)
			for _, forbidden := range []string{"history", "projection", "summary_only", "raw", "limit"} {
				if _, exists := properties[forbidden]; exists {
					t.Errorf("tool %s must not expose a %s selector: %#v", tool.Name, forbidden, tool.InputSchema)
				}
			}
			for _, expected := range []string{"key", "fields", "since", "until", "max_bytes"} {
				if _, exists := properties[expected]; !exists {
					t.Errorf("tool %s input must expose %s: %#v", tool.Name, expected, tool.InputSchema)
				}
			}
			if !schemaRequired(input, "key") {
				t.Errorf("tool %s must require key: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			outputProperties, _ := output["properties"].(map[string]any)
			if _, exists := outputProperties["history"]; exists {
				t.Errorf("tool %s output must not carry raw history rows: %#v", tool.Name, tool.OutputSchema)
			}
			for _, required := range []string{"key", "complete", "source", "total", "fetched", "count", "filters", "summary"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
		}
		if tool.Name == "jira_issue_refs" {
			properties, _ := input["properties"].(map[string]any)
			for _, forbidden := range []string{"refs", "urls", "include_refs", "raw", "projection", "summary_only", "cursor"} {
				if _, exists := properties[forbidden]; exists {
					t.Errorf("tool %s must not expose a %s selector: %#v", tool.Name, forbidden, tool.InputSchema)
				}
			}
			for _, expected := range []string{"key", "jql", "fields", "limit", "max_bytes"} {
				if _, exists := properties[expected]; !exists {
					t.Errorf("tool %s input must expose %s: %#v", tool.Name, expected, tool.InputSchema)
				}
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "count", "complete", "selection", "summary", "issues"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{`"refs"`, `"url"`, `"jql"`, `"type":{"type":"string"`, `"summary":{"type":"string"`} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Errorf("tool %s output schema advertises %s: %s", tool.Name, forbidden, encoded)
				}
			}
			for _, guidance := range []string{"Raw reference URLs", "issue summaries", "use the CLI"} {
				if !strings.Contains(tool.Description, guidance) {
					t.Errorf("tool %s description omits %q: %q", tool.Name, guidance, tool.Description)
				}
			}
		}
		if tool.Name == "confluence_page_section" {
			properties, _ := input["properties"].(map[string]any)
			heading, _ := properties["heading"].(map[string]any)
			description, _ := heading["description"].(string)
			if !strings.Contains(description, "without a Markdown # prefix") {
				t.Errorf("tool %s heading guidance is ambiguous: %#v", tool.Name, heading)
			}
			// The gate is conditional on where the selection came from, so the field
			// is advertised but never demanded: a caller whose heading and
			// occurrence were fixed externally has no version to copy. It is only
			// useful when the agent knows which integer to copy and from where, so
			// both the tool and the field say so.
			expected, _ := properties["expected_page_version"].(map[string]any)
			if expected == nil {
				t.Errorf("tool %s must advertise expected_page_version: %#v", tool.Name, tool.InputSchema)
			}
			if schemaRequired(input, "expected_page_version") {
				t.Errorf("tool %s must not require expected_page_version: %#v", tool.Name, tool.InputSchema)
			}
			expectedDescription, _ := expected["description"].(string)
			if !strings.Contains(expectedDescription, "confluence_page_outline") ||
				!strings.Contains(expectedDescription, "exact") ||
				!strings.Contains(expectedDescription, "omit") {
				t.Errorf("tool %s expected_page_version guidance is ambiguous: %#v", tool.Name, expected)
			}
			// The description has to carry all three provenance rules: outline-derived
			// selection, recovery re-read, and what an omitted gate does and does not
			// mean.
			for _, guidance := range []string{
				"confluence_page_outline", "expected_page_version", "re-reading", "page_version_gated:false",
			} {
				if !strings.Contains(tool.Description, guidance) {
					t.Errorf("tool %s description omits %q: %q", tool.Name, guidance, tool.Description)
				}
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "version", "page_version_gated"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
		}
		if tool.Name == "confluence_page_sections" {
			properties, _ := input["properties"].(map[string]any)
			selectors, _ := properties["selectors"].(map[string]any)
			description, _ := selectors["description"].(string)
			if !schemaRequired(input, "reference") || !schemaRequired(input, "selectors") ||
				!strings.Contains(description, "one to 32") || !strings.Contains(description, "repeated selectors") {
				t.Errorf("tool %s selector schema is not bounded and ordered: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{
				"schema_version", "id", "version", "page_version_gated", "requested_count", "returned_count",
				"reconciled", "complete", "original_bytes", "emitted_bytes", "max_bytes", "sections",
			} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			outputProperties, _ := output["properties"].(map[string]any)
			sectionsSchema, _ := outputProperties["sections"].(map[string]any)
			sectionItems, _ := sectionsSchema["items"].(map[string]any)
			sectionProperties, _ := sectionItems["properties"].(map[string]any)
			if !schemaRequired(sectionItems, "complete") || !schemaRequired(sectionItems, "markdown") {
				t.Errorf("tool %s section entries must require completeness and content: %#v", tool.Name, sectionsSchema)
			}
			if _, exists := sectionProperties["partial_reason"]; !exists || schemaRequired(sectionItems, "partial_reason") {
				t.Errorf("tool %s section partial_reason must exist and stay optional: %#v", tool.Name, sectionsSchema)
			}
		}
		if tool.Name == "confluence_page_outline" {
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "version"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
		}
		if tool.Name == "confluence_page_meta" {
			if !schemaRequired(input, "reference") {
				t.Errorf("tool %s must require reference: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "id", "title", "space", "version", "restriction_state"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			outputProperties, _ := output["properties"].(map[string]any)
			for _, forbidden := range []string{"url", "labels", "ancestors", "restrictions", "principals", "body"} {
				if _, exists := outputProperties[forbidden]; exists {
					t.Errorf("tool %s output must not expose %s: %#v", tool.Name, forbidden, tool.OutputSchema)
				}
			}
			for _, guidance := range []string{
				"body-free", "restricted, unrestricted, or unknown", "omits labels, ancestors, URLs",
			} {
				if !strings.Contains(tool.Description, guidance) {
					t.Errorf("tool %s description omits %q: %q", tool.Name, guidance, tool.Description)
				}
			}
		}
		// The singular bounded page reads can return a top-level partial result, so each advertises
		// an optional machine-readable reason next to its completeness flag.
		if tool.Name == "confluence_page_outline" || tool.Name == "confluence_page_section" {
			output, _ := tool.OutputSchema.(map[string]any)
			outputProperties, _ := output["properties"].(map[string]any)
			if _, exists := outputProperties["partial_reason"]; !exists {
				t.Errorf("tool %s output must advertise partial_reason: %#v", tool.Name, tool.OutputSchema)
			}
			if schemaRequired(output, "partial_reason") {
				t.Errorf("tool %s partial_reason must stay optional: %#v", tool.Name, tool.OutputSchema)
			}
			if !schemaRequired(output, "complete") {
				t.Errorf("tool %s output must require complete: %#v", tool.Name, tool.OutputSchema)
			}
		}
		if tool.Name == "confluence_attachment_list" {
			properties, _ := input["properties"].(map[string]any)
			maxBytes, _ := properties["max_bytes"].(map[string]any)
			description, _ := maxBytes["description"].(string)
			if !schemaRequired(input, "reference") || !schemaRequired(input, "expected_page_version") {
				t.Errorf("tool %s must require reference and expected_page_version: %#v", tool.Name, tool.InputSchema)
			}
			if !strings.Contains(description, "1024 to 1048576") || !strings.Contains(description, "default 131072") {
				t.Errorf("tool %s must advertise its byte bound: %#v", tool.Name, tool.InputSchema)
			}
			// No selector may reach an attachment's bytes, download path, or comment.
			for _, forbidden := range []string{"download", "content", "filename", "attachment_id", "comment", "version"} {
				if _, exists := properties[forbidden]; exists {
					t.Errorf("tool %s must not expose a %s selector: %#v", tool.Name, forbidden, tool.InputSchema)
				}
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "page_id", "page_version", "count", "complete", "attachments"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			if schemaRequired(output, "partial_reason") {
				t.Errorf("tool %s partial_reason must stay optional: %#v", tool.Name, tool.OutputSchema)
			}
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{`"comment"`, `"download`, `"url"`, `"page_title"`, `"body"`} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Errorf("tool %s output schema advertises %s: %s", tool.Name, forbidden, encoded)
				}
			}
			if !strings.Contains(tool.Description, "untrusted evidence") {
				t.Errorf("tool %s must mark attachment titles untrusted: %q", tool.Name, tool.Description)
			}
		}
		if tool.Name == "confluence_table_extract" && (!schemaRequired(input, "reference") || !schemaRequired(input, "table")) {
			t.Errorf("tool %s must require reference and selected table: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "confluence_table_extract" || tool.Name == "confluence_table_summary" {
			properties, _ := input["properties"].(map[string]any)
			expected, _ := properties["expected_page_version"].(map[string]any)
			expectedDescription, _ := expected["description"].(string)
			if expected == nil || schemaRequired(input, "expected_page_version") ||
				!strings.Contains(expectedDescription, "exact positive") ||
				!strings.Contains(expectedDescription, "omit") {
				t.Errorf("tool %s must advertise a provenance-conditional expected_page_version: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "version", "page_version_gated", "returned_table_count", "selection_reconciled"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
		}
		if tool.Name == "confluence_table_extract" {
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !bytes.Contains(encoded, []byte("whitespace-normalized plain text")) ||
				!bytes.Contains(encoded, []byte("formatting-preserving Markdown")) ||
				!bytes.Contains(encoded, []byte(`"summary"`)) ||
				!bytes.Contains(encoded, []byte(`"cell_count_reconciled"`)) {
				t.Errorf("tool %s output schema lacks cell or summary semantics: %s", tool.Name, encoded)
			}
		}
		if tool.Name == "confluence_table_summary" {
			if !schemaRequired(input, "reference") {
				t.Errorf("tool %s must require reference: %#v", tool.Name, tool.InputSchema)
			}
			for _, guidance := range []string{"exact page version", "confluence_table_extract.expected_page_version", "page_version_gated:false"} {
				if !strings.Contains(tool.Description, guidance) {
					t.Errorf("tool %s description omits %q: %q", tool.Name, guidance, tool.Description)
				}
			}
		}
		if (tool.Name == "jira_structure_get" || tool.Name == "jira_structure_view") && !schemaRequired(input, "structure_id") {
			t.Errorf("tool %s must require structure_id: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Name == "jira_structure_get" {
			properties, _ := input["properties"].(map[string]any)
			structureID, _ := properties["structure_id"].(map[string]any)
			alternatives, _ := structureID["oneOf"].([]any)
			if len(alternatives) != 2 ||
				!schemaAlternative(alternatives, "integer", "", float64(1)) ||
				!schemaAlternative(alternatives, "string", `^[1-9][0-9]{0,18}$`, nil) ||
				input["additionalProperties"] != false {
				t.Errorf("tool %s must accept only a positive integer or canonical decimal string: %#v", tool.Name, tool.InputSchema)
			}
		}
		if tool.Name == "jira_mirror_snapshot" || tool.Name == "confluence_mirror_snapshot" {
			properties, _ := input["properties"].(map[string]any)
			if len(properties) != 0 || schemaRequired(input, "path") || schemaRequired(input, "remote") {
				t.Errorf("tool %s must accept no model-controlled input: %#v", tool.Name, tool.InputSchema)
			}
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", tool.Name)
		}
		if tool.Name == "jira_structure_get" {
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "id", "name", "read_only"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
		}
		if tool.Name == "jira_structure_view" {
			properties, _ := input["properties"].(map[string]any)
			expectedSignature, _ := properties["expected_forest_signature"].(map[string]any)
			expectedVersion, _ := properties["expected_forest_version"].(map[string]any)
			signatureDescription, _ := expectedSignature["description"].(string)
			versionDescription, _ := expectedVersion["description"].(string)
			if expectedSignature == nil || expectedVersion == nil ||
				schemaRequired(input, "expected_forest_signature") || schemaRequired(input, "expected_forest_version") ||
				!strings.Contains(signatureDescription, "forest_version.signature") ||
				!strings.Contains(versionDescription, "forest_version.version") {
				t.Errorf("tool %s must advertise the optional exact forest-version pair: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "structure", "forest_version", "forest_version_gated", "projection", "rows", "row_count", "issue_count", "complete", "inaccessible_rows", "warnings"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			for _, guidance := range []string{"forest_version.signature", "expected_forest_signature", "non-bindable", "separately timed"} {
				if !strings.Contains(tool.Description, guidance) {
					t.Errorf("tool %s description omits %q: %q", tool.Name, guidance, tool.Description)
				}
			}
		}
		if tool.Name == "jira_mirror_snapshot" || tool.Name == "confluence_mirror_snapshot" {
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "service", "remote_requested", "complete", "reconciled", "local", "native", "render", "remote"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			serviceFields := []string{"validation"}
			if tool.Name == "jira_mirror_snapshot" {
				serviceFields = []string{"snapshot", "pending"}
			}
			for _, required := range serviceFields {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
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

func TestSDKSchemaValidationErrorsAreRedactedBeforeBackendConstruction(t *testing.T) {
	var jiraConstructed, confluenceConstructed, mirrorConstructed int
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			jiraConstructed++
			return nil, errors.New("unexpected Jira construction")
		},
		Confluence: func() (ConfluenceReader, error) {
			confluenceConstructed++
			return nil, errors.New("unexpected Confluence construction")
		},
		MirrorRoot: func() (string, error) {
			mirrorConstructed++
			return "", errors.New("unexpected mirror construction")
		},
	}))
	defer closeSessions()

	const callerValue = "CALLER_VALUE_MUST_BE_REDACTED"
	wantSchemaError := toolError{
		Kind:        "usage_error",
		Remediation: "fix_request",
		Message:     "MCP tool arguments do not match the declared schema",
		Recovery:    diagnostic.Recover(domain.ErrUsage, diagnostic.OperationRead),
	}.Error()
	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "missing required field", tool: "jira_issue_search", args: map[string]any{}},
		{name: "unknown property", tool: "jira_fields", args: map[string]any{"caller_secret_property": callerValue}},
		{name: "wrong type", tool: "jira_issue_search", args: map[string]any{"jql": "project = DEMO", "limit": callerValue}},
		{name: "SDK unmarshal range", tool: "jira_issue_search", args: map[string]any{"jql": "project = DEMO", "limit": 1e100}},
		{name: "custom schema range", tool: "jira_structure_get", args: map[string]any{"structure_id": 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
				t.Fatalf("result=%+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content type=%T", result.Content[0])
			}
			if text.Text != wantSchemaError {
				t.Fatalf("text=%q want %q", text.Text, wantSchemaError)
			}
			for _, leaked := range []string{callerValue, "caller_secret_property", `validating "arguments":`, "additional properties", "cannot unmarshal"} {
				if strings.Contains(text.Text, leaked) {
					t.Fatalf("redacted result contains %q: %q", leaked, text.Text)
				}
			}
			if jiraConstructed != 0 || confluenceConstructed != 0 || mirrorConstructed != 0 {
				t.Fatalf("backend constructed: jira=%d confluence=%d mirror=%d", jiraConstructed, confluenceConstructed, mirrorConstructed)
			}
		})
	}
}

func TestSchemaValidHandlerToolErrorIsUnchanged(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_table_extract",
		Arguments: map[string]any{
			"reference": "42",
			"table":     confluenceTableMaxIndex + 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type=%T", result.Content[0])
	}
	want := toolError{
		Kind:        "usage_error",
		Remediation: "fix_request",
		Message:     "invalid Confluence table request",
		Recovery:    diagnostic.Recover(domain.ErrUsage, diagnostic.OperationConfluenceTableRead),
	}.Error()
	if text.Text != want {
		t.Fatalf("semantic error=%q want %q", text.Text, want)
	}
}

func TestProtocolErrorsBypassSchemaValidationMiddleware(t *testing.T) {
	t.Run("unknown tool", func(t *testing.T) {
		client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
		defer closeSessions()

		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "not_a_registered_tool", Arguments: map[string]any{}})
		if result != nil || err == nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("error=%T %v", err, err)
		}
	})

	t.Run("malformed outer request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		serverTransport, peerTransport := mcp.NewInMemoryTransports()
		serverSession, err := New("test", Dependencies{}).Connect(ctx, serverTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer serverSession.Close()
		peer, err := peerTransport.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()

		write := func(raw string) {
			t.Helper()
			message, err := jsonrpc.DecodeMessage([]byte(raw))
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if err := peer.Write(ctx, message); err != nil {
				t.Fatalf("write request: %v", err)
			}
		}
		readResponse := func() *jsonrpc.Response {
			t.Helper()
			message, err := peer.Read(ctx)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			response, ok := message.(*jsonrpc.Response)
			if !ok {
				t.Fatalf("response type=%T", message)
			}
			return response
		}

		write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw-test","version":"1"}}}`)
		if response := readResponse(); response.Error != nil {
			t.Fatalf("initialize error: %v", response.Error)
		}
		write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":7,"arguments":{}}}`)
		response := readResponse()
		var rpcErr *jsonrpc.Error
		if !errors.As(response.Error, &rpcErr) {
			t.Fatalf("error=%T %v", response.Error, response.Error)
		}
	})
}

func TestMirrorSnapshotToolsAreOfflineContentFreeAndPathless(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			return nil, fmt.Errorf("Jira backend must not be resolved")
		},
		Confluence: func() (ConfluenceReader, error) {
			return nil, fmt.Errorf("Confluence backend must not be resolved")
		},
		MirrorRoot: func() (string, error) { return root, nil },
	}))
	defer closeSessions()

	for _, test := range []struct {
		tool, service string
	}{
		{tool: "jira_mirror_snapshot", service: "jira"},
		{tool: "confluence_mirror_snapshot", service: "confluence"},
	} {
		result := callToolOK(t, client, test.tool, map[string]any{})
		content, ok := result.StructuredContent.(map[string]any)
		if !ok || content["service"] != test.service || content["remote_requested"] != false || content["complete"] != true || content["reconciled"] != true {
			t.Fatalf("%s content=%#v", test.tool, result.StructuredContent)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(root)) || bytes.Contains(encoded, []byte("backend must not be resolved")) {
			t.Fatalf("%s leaked local or backend detail: %s", test.tool, encoded)
		}
	}
}

func TestMirrorSnapshotReturnsReconciledHealthWhenLocalCheckFails(t *testing.T) {
	root := t.TempDir()
	privateID := "SYNTHETIC-PRIVATE-ID"
	privateBody := "SYNTHETIC-PRIVATE-BODY"
	m := mirror.New(root)
	if err := m.Write(filepath.Join(root, "SPACE"), "private-title", &domain.Resource{
		ID: privateID, Title: "private-title", SpaceKey: "SPACE", Version: 1, Body: []byte("<p>" + privateBody + "</p>"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", privateID+".csf"), []byte("<p>other</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) {
			return nil, fmt.Errorf("Confluence backend must not be resolved")
		},
		MirrorRoot: func() (string, error) { return root, nil },
	}))
	defer closeSessions()
	result := callToolOK(t, client, "confluence_mirror_snapshot", map[string]any{})
	content, ok := result.StructuredContent.(map[string]any)
	native, nativeOK := content["native"].(map[string]any)
	if !ok || !nativeOK || content["complete"] != false || content["reconciled"] != true || native["baseline_mismatch"] != float64(1) {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, privateID, privateBody, "private-title", "backend must not be resolved"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestMirrorSnapshotRootFailsClosedWithoutPathDisclosure(t *testing.T) {
	privateRoot := filepath.Join(t.TempDir(), "PRIVATE-MIRROR-NAME")
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		MirrorRoot: func() (string, error) { return privateRoot, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_mirror_snapshot", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(privateRoot)) || bytes.Contains(encoded, []byte("PRIVATE-MIRROR-NAME")) {
		t.Fatalf("mirror root error leaked configuration: %s", encoded)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "configuration_error" || got.Remediation != "complete_configuration" {
		t.Fatalf("classified error=%+v decode=%v", got, err)
	}
}

func TestMirrorSnapshotToolsRejectModelSuppliedProperties(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		MirrorRoot: func() (string, error) { return root, nil },
	}))
	defer closeSessions()
	for _, args := range []map[string]any{{"path": root}, {"remote": true}} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_mirror_snapshot", Arguments: args})
		if err != nil || result == nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
			t.Fatalf("model-controlled mirror arguments were not rejected safely: args=%v result=%+v err=%v", args, result, err)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || text.Text != sdkSchemaValidationToolError.Error() || strings.Contains(text.Text, root) {
			t.Fatalf("model-controlled mirror arguments were not redacted: args=%v content=%+v", args, result.Content)
		}
	}
}

func TestMirrorRootRejectsSymlinkMarker(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".atl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := mirrorRoot(Dependencies{MirrorRoot: func() (string, error) { return root, nil }})
	if !errors.Is(err, domain.ErrConfig) || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), outside) {
		t.Fatalf("err=%v", err)
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

func schemaAlternative(alternatives []any, schemaType, pattern string, minimum any) bool {
	for _, alternative := range alternatives {
		schema, _ := alternative.(map[string]any)
		if schema["type"] != schemaType {
			continue
		}
		if pattern != "" && schema["pattern"] != pattern {
			continue
		}
		if minimum != nil && schema["minimum"] != minimum {
			continue
		}
		return true
	}
	return false
}

func TestSyntheticPortfolioThroughMCPUsesExactGETOnlyRoute(t *testing.T) {
	fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-quarter-portfolio", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	board := callToolOK(t, client, "jira_board_view", map[string]any{
		"board_id": 5, "scope": "board", "limit": 50,
		"columns":    []string{"key", "summary", "status", "issuetype", "updated", "customfield_11001", "customfield_11002", "customfield_11003"},
		"epic_field": "customfield_11001", "done_statuses": []string{"Done"},
	})
	boardContent, ok := board.StructuredContent.(map[string]any)
	rollup, rollupOK := boardContent["epic_rollup"].(map[string]any)
	epics, epicsOK := rollup["epics"].([]any)
	if !ok || !rollupOK || !epicsOK || rollup["complete"] != true || len(epics) != 3 {
		t.Fatalf("board rollup=%#v", board.StructuredContent)
	}
	first, firstOK := epics[0].(map[string]any)
	second, secondOK := epics[1].(map[string]any)
	third, thirdOK := epics[2].(map[string]any)
	if !firstOK || !secondOK || !thirdOK ||
		first["key"] != "PROJ-10" || first["child_count"] != float64(2) || first["done_child_count"] != float64(2) ||
		second["key"] != "PROJ-20" || second["latest_child_updated"] != "2026-06-20T10:00:00.000+0000" ||
		third["key"] != "PROJ-30" || third["latest_child_updated"] != "2026-06-27T10:00:00.000+0000" {
		t.Fatalf("epics=%#v", epics)
	}
	for _, key := range []string{"PROJ-10", "PROJ-20", "PROJ-30"} {
		callToolOK(t, client, "jira_epic_digest", map[string]any{
			"key": key, "quarter": "2026-Q2", "include": []string{"identity", "status-field", "history"}, "status_field": "customfield_11002", "projection": "compact",
		})
	}
	// These headings are fixed by the route and no earlier call exposes a page
	// version, so the exact ordered benchmark route reads them explicitly
	// ungated rather than inventing a binding from test-only fixture data.
	for _, pageID := range []string{"9001", "9002", "9003"} {
		result := callToolOK(t, client, "confluence_page_section", map[string]any{
			"reference": "/wiki/pages/viewpage.action?pageId=" + pageID, "heading": "Results",
			"max_bytes": 32768,
		})
		content, ok := result.StructuredContent.(map[string]any)
		if !ok || content["id"] != pageID || content["complete"] != true ||
			content["page_version_gated"] != false {
			t.Fatalf("section %s content=%#v", pageID, result.StructuredContent)
		}
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 15 || len(methods) != 1 || unexpected != 0 || duplicates != 2 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticJiraReferenceSummaryThroughMCPUsesExactClosedRoute(t *testing.T) {
	tests := []struct {
		name, directory string
		args            map[string]any
		count, refs     int
		complete        bool
		truncated       bool
		gets            int
	}{
		{
			name: "key primary", directory: "jira-reference-summary-mcp",
			args: map[string]any{
				"key": "RF-42", "fields": []string{"customfield_20001"}, "max_bytes": 32768,
			},
			count: 1, refs: 5, complete: true, gets: 2,
		},
		{
			name: "bounded JQL holdout", directory: "jira-reference-summary-mcp-holdout",
			args: map[string]any{
				"jql": "project=RF", "limit": 2, "max_bytes": 32768,
			},
			count: 2, refs: 4, complete: false, truncated: true, gets: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			result := callToolOK(t, client, "jira_issue_refs", test.args)
			content, ok := result.StructuredContent.(map[string]any)
			summary, summaryOK := content["summary"].(map[string]any)
			issues, issuesOK := content["issues"].([]any)
			if !ok || !summaryOK || !issuesOK ||
				content["schema_version"] != float64(1) ||
				content["count"] != float64(test.count) ||
				content["complete"] != test.complete ||
				summary["reference_count"] != float64(test.refs) ||
				len(issues) != test.count {
				t.Fatalf("reference summary=%#v", result.StructuredContent)
			}
			if _, exists := content["jql"]; exists {
				t.Fatalf("reference summary echoed JQL: %#v", content)
			}
			gotTruncated, _ := content["truncated"].(bool)
			if gotTruncated != test.truncated {
				t.Fatalf("truncated=%t want=%t content=%#v", gotTruncated, test.truncated, content)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{`"refs"`, `"url"`, "https://"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("reference summary leaked %q: %s", forbidden, encoded)
				}
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods["GET"] != test.gets || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestSyntheticPaginatedBoardThroughMCPReconcilesMembership(t *testing.T) {
	for _, test := range []struct {
		name, directory, query   string
		boardID, limit, requests int
		keys                     []string
		inBoard, inBacklog       []bool
		boardPositions           []any
		backlogPositions         []any
		columns                  []string
		mapped                   []bool
		complete                 bool
	}{
		{
			name: "primary", directory: "jira-board-pagination-mcp",
			boardID: 21, query: "labels = readiness ORDER BY Rank ASC", limit: 100, requests: 5,
			keys:             []string{"RIVER-9", "RIVER-8", "RIVER-7", "RIVER-6", "RIVER-5", "RIVER-4"},
			inBoard:          []bool{true, true, true, true, false, false},
			inBacklog:        []bool{false, true, false, false, true, true},
			boardPositions:   []any{0, 1, 2, 3, nil, nil},
			backlogPositions: []any{nil, 0, nil, nil, 1, 2},
			columns:          []string{"Active", "Ready", "Done", "Unmapped", "Active", "Ready"},
			mapped:           []bool{true, true, true, false, true, true},
			complete:         true,
		},
		{
			name: "holdout", directory: "jira-board-pagination-mcp-holdout",
			boardID: 34, query: "labels = launch ORDER BY Rank ASC", limit: 75, requests: 4,
			keys:             []string{"COMET-12", "COMET-10", "COMET-8", "COMET-6", "COMET-2"},
			inBoard:          []bool{true, true, true, false, false},
			inBacklog:        []bool{true, true, false, true, true},
			boardPositions:   []any{0, 1, 2, nil, nil},
			backlogPositions: []any{0, 1, nil, 2, 3},
			columns:          []string{"Unmapped", "Work", "Closed", "Queue", "Queue"},
			mapped:           []bool{false, true, true, true, true},
			complete:         true,
		},
		{
			name: "incomplete-primary", directory: "jira-board-incomplete-mcp",
			boardID: 41, query: "labels = bounded ORDER BY Rank ASC", limit: 2, requests: 3,
			keys:             []string{"DELTA-4", "DELTA-3", "DELTA-2"},
			inBoard:          []bool{true, true, false},
			inBacklog:        []bool{false, true, true},
			boardPositions:   []any{0, 1, nil},
			backlogPositions: []any{nil, 0, 1},
			columns:          []string{"Work", "Queue", "Unmapped"},
			mapped:           []bool{true, true, false},
			complete:         false,
		},
		{
			name: "incomplete-holdout", directory: "jira-board-incomplete-mcp-holdout",
			boardID: 52, query: "labels = capped ORDER BY Rank ASC", limit: 3, requests: 3,
			keys:             []string{"EMBER-9", "EMBER-8", "EMBER-7", "EMBER-6"},
			inBoard:          []bool{true, true, true, false},
			inBacklog:        []bool{false, true, true, true},
			boardPositions:   []any{0, 1, 2, nil},
			backlogPositions: []any{nil, 0, 1, 2},
			columns:          []string{"Active", "Ready", "Done", "Unmapped"},
			mapped:           []bool{true, true, true, false},
			complete:         false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			result := callToolOK(t, client, "jira_board_view", map[string]any{
				"board_id": test.boardID, "scope": "all",
				"columns": []string{"key", "summary", "status"},
				"jql":     test.query, "limit": test.limit, "max_bytes": 131072,
			})
			content, ok := result.StructuredContent.(map[string]any)
			rows, rowsOK := content["rows"].([]any)
			board, boardOK := content["board"].(map[string]any)
			if !ok || !rowsOK || !boardOK ||
				board["id"] != float64(test.boardID) ||
				content["scope"] != "all" ||
				content["complete"] != test.complete ||
				content["truncated"] != !test.complete ||
				content["backlog_fetched"] != true ||
				content["row_count"] != float64(len(test.keys)) ||
				len(rows) != len(test.keys) {
				t.Fatalf("board content=%#v", result.StructuredContent)
			}
			for index, raw := range rows {
				row, rowOK := raw.(map[string]any)
				if !rowOK ||
					row["key"] != test.keys[index] ||
					row["position"] != float64(index) ||
					row["in_board"] != test.inBoard[index] ||
					row["in_backlog"] != test.inBacklog[index] ||
					row["column"] != test.columns[index] ||
					row["column_mapped"] != test.mapped[index] {
					t.Fatalf("row %d=%#v", index, raw)
				}
				for _, position := range []struct {
					name     string
					expected any
				}{
					{name: "board_position", expected: test.boardPositions[index]},
					{name: "backlog_position", expected: test.backlogPositions[index]},
				} {
					actual, present := row[position.name]
					if position.expected == nil {
						if present {
							t.Fatalf("row %d unexpectedly has %s=%#v", index, position.name, actual)
						}
					} else if !present || actual != float64(position.expected.(int)) {
						t.Fatalf("row %d %s=%#v want=%v", index, position.name, actual, position.expected)
					}
				}
			}
			methods, unexpected, duplicates := backend.Summary()
			if len(methods) != 1 || methods["GET"] != test.requests ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestSyntheticStructureQualificationThroughMCPProjectsMetadataAndObservedRoute(t *testing.T) {
	for _, test := range []struct {
		name        string
		directory   string
		structureID int
		folderPath  string
		folderID    string
		rootRow     int
		structure   string
		readOnly    bool
		rowCount    int
		complete    bool
		canaries    []string
	}{
		{
			name: "primary", directory: "jira-structure-qualification-mcp",
			structureID: 93, folderPath: "Plans / Current", folderID: "current",
			rootRow: 510, structure: "Synthetic release train", readOnly: true,
			rowCount: 6, complete: false,
			canaries: []string{"OWNER-CANARY-PRIMARY", "PERMISSION-CANARY-PRIMARY", "VIEW-CANARY-PRIMARY"},
		},
		{
			name: "holdout", directory: "jira-structure-qualification-mcp-holdout",
			structureID: 94, folderPath: "Capacity / Week 28", folderID: "week-28",
			rootRow: 710, structure: "Synthetic capacity plan", readOnly: false,
			rowCount: 7, complete: true,
			canaries: []string{"OWNER-CANARY-HOLDOUT", "PERMISSION-CANARY-HOLDOUT", "VIEW-CANARY-HOLDOUT"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			metadataResult := callToolOK(t, client, "jira_structure_get", map[string]any{"structure_id": test.structureID})
			metadata, ok := metadataResult.StructuredContent.(map[string]any)
			if !ok || len(metadata) != 4 ||
				metadata["schema_version"] != float64(1) ||
				metadata["id"] != float64(test.structureID) ||
				metadata["name"] != test.structure ||
				metadata["read_only"] != test.readOnly {
				t.Fatalf("compact metadata=%#v", metadataResult.StructuredContent)
			}
			for _, forbidden := range []string{"owner", "permissions", "views", "forest"} {
				if _, exists := metadata[forbidden]; exists {
					t.Fatalf("compact metadata exposed %q: %#v", forbidden, metadata)
				}
			}

			viewResult := callToolOK(t, client, "jira_structure_view", map[string]any{
				"structure_id": test.structureID,
				"fields":       []string{"key", "summary", "status"},
				"folder_path":  test.folderPath,
				"max_rows":     50,
				"max_bytes":    65536,
			})
			view, ok := viewResult.StructuredContent.(map[string]any)
			viewMetadata, metadataOK := view["structure"].(map[string]any)
			selection, selectionOK := view["selection"].(map[string]any)
			path, pathOK := selection["path"].([]any)
			if !ok || !metadataOK || !selectionOK || !pathOK ||
				view["row_count"] != float64(test.rowCount) || view["complete"] != test.complete ||
				view["forest_version_gated"] != false ||
				viewMetadata["id"] != metadata["id"] ||
				viewMetadata["name"] != metadata["name"] ||
				viewMetadata["read_only"] != metadata["read_only"] ||
				selection["kind"] != "folder-path" ||
				selection["folder_id"] != test.folderID ||
				selection["row_id"] != float64(test.rootRow) ||
				len(path) != 2 || fmt.Sprint(path[0])+" / "+fmt.Sprint(path[1]) != test.folderPath {
				t.Fatalf("metadata/view qualification mismatch: metadata=%#v view=%#v", metadata, view)
			}

			observed, err := json.Marshal([]any{metadataResult.StructuredContent, viewResult.StructuredContent})
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range test.canaries {
				if bytes.Contains(observed, []byte(canary)) {
					t.Fatalf("MCP projection leaked fixture canary %q: %s", canary, observed)
				}
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods["GET"] != 4 || methods["POST"] != 1 || len(methods) != 2 ||
				unexpected != 0 || duplicates != 1 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestSyntheticClippedDigestExpandsOnlyExactField(t *testing.T) {
	fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-clipped-field-evidence", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
	closeErr := fixtureFile.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	outline := callToolOK(t, client, "confluence_page_outline", map[string]any{"reference": "8101"})
	outlineContent, outlineOK := outline.StructuredContent.(map[string]any)
	outlineVersion, versionOK := outlineContent["version"].(float64)
	if !outlineOK || !versionOK || outlineVersion < 1 {
		t.Fatalf("outline=%#v", outline.StructuredContent)
	}
	// The section copies the version the outline just reported, which is the
	// exact binding the tool description instructs an agent to perform.
	section := callToolOK(t, client, "confluence_page_section", map[string]any{
		"reference": "8101", "heading": "Decision", "expected_page_version": int(outlineVersion),
	})
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

func TestSyntheticPartialAuthorizationThroughMCPStopsAtForbiddenSection(t *testing.T) {
	tests := []struct {
		name, directory, jiraQuery, confluenceQuery, jiraKey, jiraStatus string
		pageID, pageTitle, heading, marker                               string
	}{
		{
			name: "primary", directory: "cross-service-partial-authorization-mcp",
			jiraQuery:       `text ~ "Orchid migration readiness" ORDER BY updated DESC`,
			confluenceQuery: `siteSearch ~ "Orchid migration readiness"`,
			jiraKey:         "OPS-217", jiraStatus: "In Review", pageID: "9301",
			pageTitle: "Orchid migration readiness record",
			heading:   "Current decision", marker: "FORBIDDEN_FIXTURE_MARKER",
		},
		{
			name: "holdout", directory: "cross-service-partial-authorization-mcp-holdout",
			jiraQuery:       `text ~ "Cobalt failover readiness" ORDER BY updated DESC`,
			confluenceQuery: `siteSearch ~ "Cobalt failover readiness"`,
			jiraKey:         "SRE-328", jiraStatus: "Blocked", pageID: "9402",
			pageTitle: "Cobalt failover readiness record",
			heading:   "Outcome", marker: "HOLDOUT_FORBIDDEN_MARKER",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join(
				"..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json",
			))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			jiraResult := callToolOK(t, client, "jira_issue_search", map[string]any{
				"jql": test.jiraQuery, "columns": []string{"key", "summary", "status", "updated"},
				"limit": 10, "max_bytes": 131072,
			})
			jiraContent, ok := jiraResult.StructuredContent.(map[string]any)
			jiraPage, pageOK := jiraContent["page"].(map[string]any)
			jiraRows, rowsOK := jiraContent["rows"].([]any)
			var jiraRow map[string]any
			if len(jiraRows) == 1 {
				jiraRow, _ = jiraRows[0].(map[string]any)
			}
			jiraValues, _ := jiraRow["values"].(map[string]any)
			if !ok || !pageOK || jiraPage["complete"] != true || !rowsOK ||
				jiraRow == nil || jiraRow["key"] != test.jiraKey ||
				jiraValues["status"] != test.jiraStatus {
				t.Fatalf("jira discovery=%#v", jiraResult.StructuredContent)
			}
			confluenceResult := callToolOK(t, client, "confluence_search", map[string]any{
				"cql": test.confluenceQuery, "limit": 10, "max_bytes": 131072,
			})
			confluenceContent, ok := confluenceResult.StructuredContent.(map[string]any)
			confluenceResults, resultsOK := confluenceContent["results"].([]any)
			var confluencePage map[string]any
			if len(confluenceResults) == 1 {
				confluencePage, _ = confluenceResults[0].(map[string]any)
			}
			if !ok || confluenceContent["complete"] != true || !resultsOK ||
				confluencePage == nil || confluencePage["id"] != test.pageID ||
				confluencePage["title"] != test.pageTitle {
				t.Fatalf("confluence discovery=%#v", confluenceResult.StructuredContent)
			}
			// Search selected the page, but this test fixes the heading and
			// occurrence externally; no outline-derived selection is being
			// reconciled, so the benchmark route remains explicitly ungated.
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_section",
				Arguments: map[string]any{
					"reference": test.pageID, "heading": test.heading,
					"occurrence": 1, "max_bytes": 32768,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil ||
				bytes.Contains(encoded, []byte(test.marker)) {
				t.Fatalf("forbidden result leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("error content=%q: %v", text.Text, err)
			}
			if got.Kind != "forbidden" || got.Remediation != "request_access" ||
				got.Message != "Confluence page section access is forbidden" {
				t.Fatalf("classified error=%+v", got)
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods[http.MethodGet] != 3 || len(methods) != 1 ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestSyntheticStaleCandidateThroughMCPStopsAtNotFoundSection(t *testing.T) {
	tests := []struct {
		name, directory, query, pageID, pageTitle, heading, marker string
	}{
		{"primary", "confluence-stale-not-found-mcp", `siteSearch ~ "Quartz retention decision"`,
			"9501", "Quartz retention decision record", "Current decision", "NOT_FOUND_FIXTURE_MARKER"},
		{"holdout", "confluence-stale-not-found-mcp-holdout", `siteSearch ~ "Saffron failover approval"`,
			"9602", "Saffron failover approval record", "Approval", "HOLDOUT_NOT_FOUND_MARKER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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

			search := callToolOK(t, client, "confluence_search", map[string]any{
				"cql": test.query, "limit": 10, "max_bytes": 131072,
			})
			content, ok := search.StructuredContent.(map[string]any)
			results, resultsOK := content["results"].([]any)
			var page map[string]any
			if len(results) == 1 {
				page, _ = results[0].(map[string]any)
			}
			if !ok || content["complete"] != true || !resultsOK || page == nil ||
				page["id"] != test.pageID || page["title"] != test.pageTitle {
				t.Fatalf("search=%#v", search.StructuredContent)
			}
			// The task fixes the heading and occurrence rather than choosing
			// them from an outline, so this negative route does not invent a
			// structural binding from the search result.
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_section",
				Arguments: map[string]any{
					"reference": test.pageID, "heading": test.heading,
					"occurrence": 1, "max_bytes": 32768,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(test.marker)) {
				t.Fatalf("not-found result leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("error content=%q: %v", text.Text, err)
			}
			if got.Kind != "not_found" || got.Remediation != "verify_identifier_or_access" ||
				got.Message != "Confluence page, section, or heading was not found" {
				t.Fatalf("classified error=%+v", got)
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods[http.MethodGet] != 2 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
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
	callToolOK(t, client, "jira_fields", map[string]any{"name_like": "Outcome", "custom": custom, "summary_only": true})
	callToolOK(t, client, "jira_issue_search", map[string]any{
		"jql": "project=PROJ", "fields": []string{"key", "status"}, "view": "compact", "cursor": "next",
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
		"board_id": 7, "scope": "backlog", "columns": []string{"key", "updated", "customfield_3"},
		"view": "compact", "jql": "labels=x", "epic_field": "customfield_3", "done_statuses": []string{"Done", "Closed"},
	})
	metadata := callToolOK(t, client, "jira_structure_get", map[string]any{"structure_id": 9})
	metadataContent, ok := metadata.StructuredContent.(map[string]any)
	if !ok || metadataContent["schema_version"] != float64(1) || metadataContent["id"] != float64(9) ||
		metadataContent["name"] != "Synthetic Structure" || metadataContent["read_only"] != false || metadataContent["owner"] != nil {
		t.Fatalf("Structure metadata=%#v", metadata.StructuredContent)
	}
	view := callToolOK(t, client, "jira_structure_view", map[string]any{
		"structure_id": 9, "fields": []string{"key", "summary"}, "folder_id": "folder-a",
		"expected_forest_signature": 55, "expected_forest_version": 7,
		"max_rows": 10, "max_bytes": 4096,
	})
	viewContent, ok := view.StructuredContent.(map[string]any)
	if !ok || viewContent["row_count"] != float64(2) || viewContent["complete"] != true ||
		viewContent["forest_version_gated"] != true {
		t.Fatalf("Structure view=%#v", view.StructuredContent)
	}
	callToolOK(t, client, "confluence_search", map[string]any{"cql": "space=DOCS", "cursor": "25"})
	callToolOK(t, client, "confluence_page_resolve", map[string]any{"reference": "/x/Abc"})
	callToolOK(t, client, "confluence_page_outline", map[string]any{"reference": "42"})
	callToolOK(t, client, "confluence_page_section", map[string]any{
		"reference": "42", "heading": "Results", "occurrence": 2, "expected_page_version": 3,
	})
	summary := callToolOK(t, client, "confluence_table_summary", map[string]any{"reference": "42", "table": 2})
	summaryContent, ok := summary.StructuredContent.(map[string]any)
	if !ok || summaryContent["schema_version"] != float64(app.ConfluenceTableSchemaVersion) ||
		summaryContent["cell_contract"] != app.ConfluenceTableCellContract || summaryContent["version"] != float64(3) ||
		summaryContent["page_version_gated"] != false || summaryContent["selection_reconciled"] != true {
		t.Fatalf("table summary=%#v", summary.StructuredContent)
	}
	extract := callToolOK(t, client, "confluence_table_extract", map[string]any{
		"reference": "42", "table": 2, "expected_page_version": 3, "max_bytes": 4096,
	})
	extractContent, ok := extract.StructuredContent.(map[string]any)
	if !ok || extractContent["schema_version"] != float64(app.ConfluenceTableSchemaVersion) ||
		extractContent["cell_contract"] != app.ConfluenceTableCellContract || extractContent["version"] != float64(3) ||
		extractContent["page_version_gated"] != true || extractContent["selected_table"] != float64(2) ||
		extractContent["returned_table_count"] != float64(1) || extractContent["selection_reconciled"] != true {
		t.Fatalf("table extract=%#v", extract.StructuredContent)
	}
	extractTables, ok := extractContent["tables"].([]any)
	if !ok || len(extractTables) != 1 {
		t.Fatalf("table extract tables=%#v", extractContent["tables"])
	}
	extractTable, ok := extractTables[0].(map[string]any)
	if !ok {
		t.Fatalf("table extract record=%#v", extractTables[0])
	}
	extractSummary, ok := extractTable["summary"].(map[string]any)
	if !ok || extractSummary["index"] != float64(2) || extractSummary["cell_count_reconciled"] != true {
		t.Fatalf("table extract summary=%#v", extractTable["summary"])
	}

	if j.fieldOpts.Custom != "true" || j.fieldOpts.NameLike != "Outcome" || !j.fieldOpts.SummaryOnly {
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
	if j.boardID != 7 || j.boardOpts.Scope != "backlog" || j.boardOpts.Limit != 200 || j.boardOpts.JQL != "labels=x" ||
		j.boardOpts.EpicField != "customfield_3" || strings.Join(j.boardOpts.DoneStatuses, ",") != "Done,Closed" {
		t.Fatalf("board id=%d opts=%+v", j.boardID, j.boardOpts)
	}
	if j.structureID != 9 || j.structureViewID != 9 || j.structureOpts.MaxRows != 10 ||
		j.structureOpts.MaxScanRows != jiraStructureViewMaxMaxRows || j.structureOpts.BatchSize != 100 ||
		j.structureOpts.FolderID != "folder-a" || strings.Join(j.structureOpts.Attributes, ",") != "key,summary" ||
		j.structureOpts.ExpectedForestVersion == nil ||
		*j.structureOpts.ExpectedForestVersion != (domain.StructureVersion{Signature: 55, Version: 7}) {
		t.Fatalf("Structure calls=%+v", j)
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

func TestJiraIssueSearchRejectsUnknownInputBeforeBackend(t *testing.T) {
	jira := &recordingJiraReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return jira, nil },
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jira_issue_search",
		Arguments: map[string]any{
			"jql":             "project=PROJ",
			"projection_mode": []string{"key", "status"},
		},
	})
	if err != nil || result == nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("unknown input was not rejected as a tool error: result=%+v err=%v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != sdkSchemaValidationToolError.Error() || strings.Contains(text.Text, "projection_mode") {
		t.Fatalf("unknown input was not redacted: content=%+v", result.Content)
	}
	if jira.searchJQL != "" || jira.searchColumns != nil {
		t.Fatalf("unknown input reached backend: jql=%q columns=%v", jira.searchJQL, jira.searchColumns)
	}
}

func TestJiraIssueSearchProjectionAliasesTreatEmptyArraysAsOmitted(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{name: "columns", args: map[string]any{"columns": []string{"key"}}, want: []string{"key"}},
		{name: "fields", args: map[string]any{"fields": []string{"status"}}, want: []string{"status"}},
		{name: "projection", args: map[string]any{"projection": []string{"assignee"}}, want: []string{"assignee"}},
		{name: "empty columns", args: map[string]any{"columns": []string{}, "fields": []string{"status"}}, want: []string{"status"}},
		{name: "empty columns with projection", args: map[string]any{"columns": []string{}, "projection": []string{"assignee"}}, want: []string{"assignee"}},
		{name: "empty fields", args: map[string]any{"columns": []string{"key"}, "fields": []string{}}, want: []string{"key"}},
		{name: "empty fields with projection", args: map[string]any{"fields": []string{}, "projection": []string{"assignee"}}, want: []string{"assignee"}},
		{name: "empty projection with columns", args: map[string]any{"projection": []string{}, "columns": []string{"key"}}, want: []string{"key"}},
		{name: "empty projection with fields", args: map[string]any{"projection": []string{}, "fields": []string{"status"}}, want: []string{"status"}},
		{name: "all empty", args: map[string]any{"columns": []string{}, "fields": []string{}, "projection": []string{}}, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingJiraReader{}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			test.args["jql"] = "project=PROJ"
			callToolOK(t, client, "jira_issue_search", test.args)
			if !slices.Equal(reader.searchColumns, test.want) {
				t.Fatalf("columns=%v want %v", reader.searchColumns, test.want)
			}
		})
	}

}

func TestJiraIssueHistoryForwardsExactOptionsAndReturnsSummaryOnly(t *testing.T) {
	reader := &recordingJiraReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_history", map[string]any{
		"key": "PROJ-1", "fields": []string{"customfield_10001", "status"},
		"since": "2026-03-01", "until": "2026-03-31", "max_bytes": 4096,
	})

	if reader.historyKey != "PROJ-1" || reader.historyOpts.Since != "2026-03-01" || reader.historyOpts.Until != "2026-03-31" ||
		!slices.Equal(reader.historyOpts.Fields, []string{"customfield_10001", "status"}) {
		t.Fatalf("history key=%q opts=%+v", reader.historyKey, reader.historyOpts)
	}

	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	if _, exists := content["history"]; exists {
		t.Fatalf("summary projection exposed raw history rows: %#v", content)
	}
	summary, summaryOK := content["summary"].(map[string]any)
	if !summaryOK || summary["history_count"] != float64(1) || summary["count_matches_history"] != true {
		t.Fatalf("summary=%#v", content["summary"])
	}
	if content["key"] != "PROJ-1" || content["complete"] != true || content["source"] != "paginated" ||
		content["total"] != float64(2) || content["fetched"] != float64(2) || content["count"] != float64(1) {
		t.Fatalf("provenance=%#v", content)
	}
	filters, filtersOK := content["filters"].(map[string]any)
	if !filtersOK || filters["since"] != "2026-03-01" || filters["until"] != "2026-03-31" {
		t.Fatalf("filters=%#v", content["filters"])
	}
	changes, changesOK := content["last_changes"].([]any)
	if !changesOK || len(changes) != 1 {
		t.Fatalf("last_changes=%#v", content["last_changes"])
	}
	change, changeOK := changes[0].(map[string]any)
	if !changeOK || change["field_id"] != "customfield_10001" || change["history_id"] != "101" {
		t.Fatalf("last change=%#v", changes[0])
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(historyRawRowMarker)) {
		t.Fatalf("MCP result leaked a raw changelog row: %s", encoded)
	}
}

func TestJiraIssueHistoryOmitsLastChangesWithoutSelectedFields(t *testing.T) {
	reader := &recordingJiraReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_history", map[string]any{"key": "PROJ-1"})
	if reader.historyOpts.Fields != nil || reader.historyOpts.Since != "" || reader.historyOpts.Until != "" {
		t.Fatalf("unselected history opts=%+v", reader.historyOpts)
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	if _, exists := content["history"]; exists {
		t.Fatalf("summary projection exposed raw history rows: %#v", content)
	}
	if _, exists := content["last_changes"]; exists {
		t.Fatalf("last_changes present without an explicit field selection: %#v", content)
	}
}

func TestJiraIssueRefsForwardsExactOptionsAndReturnsClosedSummary(t *testing.T) {
	reader := &recordingJiraReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_refs", map[string]any{
		"key": "PROJ-1", "fields": []string{"customfield_10001"}, "max_bytes": 4096,
	})
	if reader.refsOpts.Key != "PROJ-1" || reader.refsOpts.JQL != "" || reader.refsOpts.Limit != 0 ||
		!slices.Equal(reader.refsOpts.Fields, []string{"customfield_10001"}) {
		t.Fatalf("refs opts=%+v", reader.refsOpts)
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok || content["schema_version"] != float64(1) || content["count"] != float64(1) ||
		content["complete"] != true {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"https://private.invalid", "narrative must not cross MCP", `"refs"`, `"jql"`, `"type":"Story"`} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("reference summary leaked %q: %s", forbidden, encoded)
		}
	}
	issues, issuesOK := content["issues"].([]any)
	if !issuesOK || len(issues) != 1 {
		t.Fatalf("issues=%#v", content["issues"])
	}
	issue, issueOK := issues[0].(map[string]any)
	if !issueOK || issue["key"] != "PROJ-1" || issue["complete"] != true {
		t.Fatalf("issue=%#v", issues[0])
	}
	for _, required := range []string{"sources", "reference_summary"} {
		if _, exists := issue[required]; !exists {
			t.Fatalf("issue omits %s: %#v", required, issue)
		}
	}
}

func TestJiraIssueRefsAcceptsCanonicalizedOrMovedIssueKey(t *testing.T) {
	reader := &recordingJiraReader{
		refsResult: validJiraIssueRefsResult(app.JiraIssueRefsOpts{Key: "NEW-9"}),
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_refs", map[string]any{"key": "old-5"})
	content, ok := result.StructuredContent.(map[string]any)
	issues, issuesOK := content["issues"].([]any)
	if !ok || !issuesOK || len(issues) != 1 {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	issue, issueOK := issues[0].(map[string]any)
	if !issueOK || issue["key"] != "NEW-9" || reader.refsOpts.Key != "old-5" {
		t.Fatalf("issue=%#v opts=%+v", issues[0], reader.refsOpts)
	}
}

func TestJiraIssueRefsRejectsUnreconciledSummary(t *testing.T) {
	tests := []struct {
		name   string
		opts   app.JiraIssueRefsOpts
		mutate func(*app.JiraIssueRefsResult)
	}{
		{name: "top-level count", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Summary.IssueCount++
		}},
		{name: "per-issue kind count", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Issues[0].ReferenceSummary.ReferenceKindCounts["link"] = 1
		}},
		{name: "unknown reference kind", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Issues[0].ReferenceSummary.ReferenceKindCounts["private.example"] = 0
			result.Summary.ReferenceKindCounts["private.example"] = 0
		}},
		{name: "source completeness", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			source := result.Issues[0].Sources["comments"]
			source.Complete = false
			source.Warning = app.JiraIssueRefsWarningCommentsPartial
			result.Issues[0].Sources["comments"] = source
		}},
		{name: "negative source count", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			source := result.Issues[0].Sources["description"]
			source.Count = -1
			result.Issues[0].Sources["description"] = source
		}},
		{name: "unrequested source name", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			source := result.Issues[0].Sources["description"]
			delete(result.Issues[0].Sources, "description")
			result.Issues[0].Sources["field.customfield_99999"] = source
			delete(result.Issues[0].ReferenceSummary.SourceValueCounts, "description")
			result.Issues[0].ReferenceSummary.SourceValueCounts["field.customfield_99999"] = 0
			delete(result.Summary.SourceValueCounts, "description")
			result.Summary.SourceValueCounts["field.customfield_99999"] = 0
		}},
		{name: "unrecognized source warning", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			source := result.Issues[0].Sources["comments"]
			source.Complete = false
			source.Warning = "private backend detail"
			result.Issues[0].Sources["comments"] = source
		}},
		{name: "unrecognized warning", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Warnings = []string{"private backend detail"}
		}},
		{name: "blank issue key", opts: app.JiraIssueRefsOpts{Key: "PROJ-1"}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Issues[0].Key = " "
		}},
		{name: "JQL selection mode", opts: app.JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Selection.Mode = "key"
		}},
		{name: "JQL selection limit", opts: app.JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Selection.Limit = 2
		}},
		{name: "JQL count exceeds bound", opts: app.JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1}, mutate: func(result *app.JiraIssueRefsResult) {
			second := result.Issues[0]
			second.Key = "PROJ-2"
			result.Issues = append(result.Issues, second)
			result.Count = 2
			result.Selection.Count = 2
			result.Summary.IssueCount = 2
			result.Summary.CompleteIssueCount = 2
			result.Summary.SourceCount = 4
			result.Summary.CompleteSourceCount = 4
		}},
		{name: "duplicate JQL issue key", opts: app.JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 2}, mutate: func(result *app.JiraIssueRefsResult) {
			result.Issues = append(result.Issues, result.Issues[0])
			result.Count = 2
			result.Selection.Count = 2
			result.Summary.IssueCount = 2
			result.Summary.CompleteIssueCount = 2
			result.Summary.SourceCount = 4
			result.Summary.CompleteSourceCount = 4
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			full := validJiraIssueRefsResult(test.opts)
			test.mutate(full)
			reader := &recordingJiraReader{refsResult: full}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()

			arguments := map[string]any{"key": test.opts.Key}
			if test.opts.JQL != "" {
				arguments = map[string]any{"jql": test.opts.JQL, "limit": test.opts.Limit}
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_issue_refs", Arguments: arguments,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil {
				t.Fatalf("result=%+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "check_failed" || got.Message != "Jira issue reference summary failed validation" {
				t.Fatalf("classified error=%+v decode=%v", got, err)
			}
		})
	}
}

func TestJiraIssueRefsSentinelsKeepStaticClassification(t *testing.T) {
	tests := []struct {
		name, kind, remediation, message string
		err                              error
	}{
		{name: "not found", kind: "not_found", remediation: "verify_identifier_or_access", message: "Jira issue reference source was not found", err: fmt.Errorf("%w: private issue marker", domain.ErrNotFound)},
		{name: "forbidden", kind: "forbidden", remediation: "request_access", message: "Jira issue reference summary access is forbidden", err: fmt.Errorf("%w: private reference marker", domain.ErrForbidden)},
		{name: "transport", kind: "transport_error", remediation: "inspect_network_before_retry", message: "Jira issue reference summary transport failed", err: &httpx.TransportError{Method: "GET", Category: "private backend marker"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingJiraReader{refsErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_issue_refs", Arguments: map[string]any{"key": "PROJ-1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !result.IsError || !ok {
				t.Fatalf("result=%+v", result)
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Remediation != test.remediation || got.Message != test.message ||
				strings.Contains(got.Message, "private") {
				t.Fatalf("classified error=%+v decode=%v", got, err)
			}
		})
	}
}

func TestJiraIssueHistorySentinelsKeepStableClassification(t *testing.T) {
	tests := []struct {
		name, kind, remediation, message string
		err                              error
	}{
		{name: "not found", kind: "not_found", remediation: "verify_identifier_or_access", message: "Jira issue history was not found", err: fmt.Errorf("%w: private issue marker", domain.ErrNotFound)},
		{name: "forbidden", kind: "forbidden", remediation: "request_access", message: "Jira issue history access is forbidden", err: fmt.Errorf("%w: private changelog marker", domain.ErrForbidden)},
		{name: "check failed", kind: "check_failed", remediation: "review_failed_check", message: "Jira issue history summary failed validation", err: fmt.Errorf("%w: private history id and timestamp", domain.ErrCheckFailed)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingJiraReader{historyErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()

			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_issue_history", Arguments: map[string]any{"key": "PROJ-1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil {
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
			if got.Kind != test.kind || got.Remediation != test.remediation || got.Message != test.message ||
				strings.Contains(got.Message, "private") {
				t.Fatalf("classified error=%+v", got)
			}
		})
	}
}

func TestJiraIssueHistoryCancellationPropagatesToApplicationContext(t *testing.T) {
	reader := &cancellingJiraReader{started: make(chan struct{}), canceled: make(chan struct{})}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "jira_issue_history", Arguments: map[string]any{"key": "PROJ-1"}})
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("history tool handler did not start")
	}
	cancel()
	select {
	case <-reader.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("history application context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("history client call did not return after cancellation")
	}
}

func TestJiraIssueRefsCancellationPropagatesToApplicationContext(t *testing.T) {
	reader := &cancellingJiraReader{started: make(chan struct{}), canceled: make(chan struct{})}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "jira_issue_refs", Arguments: map[string]any{"key": "PROJ-1"}})
	}()
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("reference-summary tool handler did not start")
	}
	cancel()
	select {
	case <-reader.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("reference-summary application context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reference-summary client call did not return after cancellation")
	}
}

func TestJiraStructureViewSupportsFullAndExactFolderSelections(t *testing.T) {
	for _, test := range []struct {
		name string
		args map[string]any
		kind string
	}{
		{name: "full", args: map[string]any{}, kind: ""},
		{name: "folder id", args: map[string]any{"folder_id": "folder-a"}, kind: "folder-id"},
		{name: "folder row", args: map[string]any{"folder_row": 10}, kind: "folder-row"},
		{name: "folder path", args: map[string]any{"folder_path": "Plans/Quarter"}, kind: "folder-path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingJiraReader{}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			args := map[string]any{"structure_id": 9, "fields": []string{"key"}}
			for key, value := range test.args {
				args[key] = value
			}
			result := callToolOK(t, client, "jira_structure_view", args)
			content, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("content=%#v", result.StructuredContent)
			}
			if content["forest_version_gated"] != false {
				t.Fatalf("forest version claims=%#v", content)
			}
			selection, selected := content["selection"].(map[string]any)
			if test.kind == "" && selected || test.kind != "" && (!selected || selection["kind"] != test.kind) {
				t.Fatalf("selection=%#v want kind %q", content["selection"], test.kind)
			}
		})
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

func TestConfluenceSearchRateLimitIsClassifiedAfterBoundedReadRetries(t *testing.T) {
	tests := []struct {
		name, directory, query, bodyMarker string
	}{
		{"primary", "confluence-rate-limit-mcp", `siteSearch ~ "Amber quota decision"`, "PRIMARY_RATE_LIMIT_MARKER"},
		{"holdout", "confluence-rate-limit-mcp-holdout", `siteSearch ~ "Indigo recovery approval"`, "HOLDOUT_RATE_LIMIT_MARKER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_search",
				Arguments: map[string]any{
					"cql": test.query, "limit": 10, "max_bytes": 131072,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(test.bodyMarker)) {
				t.Fatalf("rate-limit result leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("error content=%q: %v", text.Text, err)
			}
			if got.Kind != "rate_limited" || got.Remediation != "wait_before_retry" ||
				got.Message != "backend returned HTTP 429" {
				t.Fatalf("classified error=%+v", got)
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods[http.MethodGet] != 4 || len(methods) != 1 || unexpected != 0 || duplicates != 3 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestConfluenceOutputLimitUsesBenchmarkFixturesWithoutLeakingContent(t *testing.T) {
	tests := []struct {
		name, directory, query, bodyMarker string
	}{
		{"primary", "confluence-output-limit-mcp", `siteSearch ~ "Silver retention decision"`, "PRIMARY_OUTPUT_LIMIT_PAYLOAD"},
		{"holdout", "confluence-output-limit-mcp-holdout", `siteSearch ~ "Coral failover approval"`, "HOLDOUT_OUTPUT_LIMIT_PAYLOAD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_search",
				Arguments: map[string]any{
					"cql": test.query, "limit": 10, "max_bytes": 1024,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(test.bodyMarker)) {
				t.Fatalf("output-limit result leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("error content=%q: %v", text.Text, err)
			}
			if got.Kind != "output_limit_exceeded" || got.Remediation != "narrow_or_raise_bound" ||
				!strings.Contains(got.Message, "exceeds max_bytes") {
				t.Fatalf("classified error=%+v", got)
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods[http.MethodGet] != 1 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
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
	rateLimited := classified(&httpx.APIError{
		Status: http.StatusTooManyRequests, Method: "GET",
		Path: "/rest/api/search?cql=PRIVATE",
		Body: "rate limit for PRIVATE backend",
	})
	got = toolError{}
	if !errors.As(rateLimited, &got) {
		t.Fatalf("rate-limit error=%T %v", rateLimited, rateLimited)
	}
	encoded = got.Error()
	if got.Kind != "rate_limited" || got.Remediation != "wait_before_retry" ||
		got.Message != "backend returned HTTP 429" || strings.Contains(encoded, "PRIVATE") || strings.Contains(encoded, "/rest/") {
		t.Fatalf("classified rate-limit error=%s", encoded)
	}
	transport := classified(&httpx.TransportError{Method: "GET", Category: "dns"})
	got = toolError{}
	if !errors.As(transport, &got) || got.Kind != "transport_error" || got.Message != "backend transport failed (dns)" {
		t.Fatalf("transport error=%v", transport)
	}
}

func TestToolErrorsRedactSecureURLConfigurationDetails(t *testing.T) {
	const privateHost = "configured-backend.private.example"
	secureErr := config.CheckSecureURL("http://" + privateHost)
	if secureErr == nil {
		t.Fatal("insecure backend URL passed validation")
	}
	err := classified(fmt.Errorf("%w: %w", domain.ErrUsage, secureErr))
	var got toolError
	if !errors.As(err, &got) {
		t.Fatalf("error=%T %v", err, err)
	}
	encoded := got.Error()
	if got.Kind != "usage_error" || got.Remediation != "fix_request" ||
		got.Message != "backend URL is not approved for authenticated reads" ||
		strings.Contains(encoded, privateHost) || strings.Contains(encoded, "http") {
		t.Fatalf("classified secure URL error=%s", encoded)
	}

	safeUsage := classified(fmt.Errorf("%w: max_rows must be at least 1", domain.ErrUsage))
	got = toolError{}
	if !errors.As(safeUsage, &got) || got.Message != "tool request failed" {
		t.Fatalf("untyped usage detail was not redacted: %v", safeUsage)
	}
}

func TestToolClassifiersRedactRequestPolicyDetails(t *testing.T) {
	const (
		configuredHost = "configured-policy.invalid"
		foreignHost    = "foreign-policy.invalid"
		foreignPath    = "/private-attachment-path"
		downgradePath  = "/private-downgrade-path"
		requestPath    = "/private-request-path"
	)
	directClient := httpx.New("https://"+configuredHost, "synthetic-token", "test")
	_, directErr := directClient.Do(context.Background(), http.MethodGet, "https://"+foreignHost+foreignPath, nil, nil)
	if directErr == nil || !strings.Contains(directErr.Error(), foreignHost) {
		t.Fatalf("direct policy error = %v, want valid foreign-host refusal", directErr)
	}
	_, downgradeErr := directClient.Do(context.Background(), http.MethodGet, "http://"+configuredHost+downgradePath, nil, nil)
	if downgradeErr == nil || !strings.Contains(downgradeErr.Error(), configuredHost) {
		t.Fatalf("downgrade policy error = %v, want valid scheme-downgrade refusal", downgradeErr)
	}
	for name, policyErr := range map[string]error{"direct": directErr, "downgrade": downgradeErr} {
		var transportErr *httpx.TransportError
		var apiErr *httpx.APIError
		if errors.As(policyErr, &transportErr) || errors.As(policyErr, &apiErr) {
			t.Fatalf("%s policy error reached transport: %T %v", name, policyErr, policyErr)
		}
	}

	foreignHit := make(chan struct{}, 1)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignHit <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer foreign.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", foreign.URL+foreignPath)
		w.WriteHeader(http.StatusFound)
	}))
	defer backend.Close()

	redirectClient := httpx.New(backend.URL, "synthetic-token", "test")
	_, redirectErr := redirectClient.Do(context.Background(), http.MethodGet, requestPath, nil, nil)
	if redirectErr == nil || !strings.Contains(redirectErr.Error(), requestPath) {
		t.Fatalf("redirect policy error = %v, want valid cross-host redirect refusal", redirectErr)
	}
	select {
	case <-foreignHit:
		t.Fatal("cross-host redirect target was contacted")
	default:
	}

	classifiers := map[string]func(error) error{
		"generic":              classified,
		"outline":              classifiedOutlineRead,
		"page_metadata":        classifiedConfluencePageMetadataRead,
		"jira_history":         classifiedJiraHistoryRead,
		"jira_issue_refs":      classifiedJiraIssueRefsRead,
		"table":                classifiedTableRead,
		"section":              classifiedSectionRead,
		"attachment_inventory": classifiedAttachmentInventoryRead,
		"structure":            classifiedStructureRead,
		"mirror":               classifiedMirrorRead,
	}
	policyErrors := map[string]error{
		"direct":    directErr,
		"downgrade": downgradeErr,
		"redirect":  redirectErr,
	}
	for policyName, policyErr := range policyErrors {
		for classifierName, classify := range classifiers {
			t.Run(policyName+"/"+classifierName, func(t *testing.T) {
				classifiedErr := classify(policyErr)
				var got toolError
				if !errors.As(classifiedErr, &got) {
					t.Fatalf("error=%T %v", classifiedErr, classifiedErr)
				}
				encoded := got.Error()
				for _, privateDetail := range []string{
					configuredHost, foreignHost, foreignPath, downgradePath, requestPath, "http://", "https://",
				} {
					if strings.Contains(encoded, privateDetail) {
						t.Fatalf("classified request-policy error leaked %q: %s", privateDetail, encoded)
					}
				}
			})
		}
	}
}

func TestProductionDependenciesRedactSecureBackendURLs(t *testing.T) {
	const privateHost = "configured-backend.private.example"
	t.Setenv("ATL_ALLOW_INSECURE", "")
	t.Setenv("ATL_CONFLUENCE_URL", "http://"+privateHost)
	t.Setenv("ATL_JIRA_URL", "http://"+privateHost)

	deps := ProductionDependencies("test")
	for name, resolve := range map[string]func() error{
		"confluence": func() error { _, err := deps.Confluence(); return err },
		"jira":       func() error { _, err := deps.Jira(); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := classified(resolve())
			var got toolError
			if !errors.As(err, &got) {
				t.Fatalf("error=%T %v", err, err)
			}
			encoded := got.Error()
			if got.Kind != "usage_error" || got.Remediation != "fix_request" ||
				got.Message != "backend URL is not approved for authenticated reads" ||
				strings.Contains(encoded, privateHost) || strings.Contains(encoded, "http") {
				t.Fatalf("production dependency error=%s", encoded)
			}
		})
	}
}

func TestProductionJiraIssueSearchHonorsPageSizeAboveOneHundred(t *testing.T) {
	fixture := testbackend.MockFixture{
		SchemaVersion: testbackend.MockFixtureSchemaVersion,
		JiraContext:   "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{{
			Method: "GET", Path: "/jira/rest/api/2/search",
			QueryEquals: map[string]string{
				"jql":     "project = PROJ ORDER BY key",
				"startAt": "0", "maxResults": "250", "fields": "summary,status",
			},
			Status: 200,
			Body:   json.RawMessage(`{"startAt":0,"maxResults":250,"total":0,"issues":[]}`),
		}},
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	result := callToolOK(t, client, "jira_issue_search", map[string]any{
		"jql": "project = PROJ ORDER BY key", "columns": []string{"key", "summary", "status"}, "limit": 250,
	})
	content, ok := result.StructuredContent.(map[string]any)
	page, pageOK := content["page"].(map[string]any)
	if !ok || !pageOK || page["count"] != float64(0) || page["complete"] != true || page["truncated"] != false {
		t.Fatalf("issue search=%#v", result.StructuredContent)
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 1 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestProductionJiraIssueSearchKeepsStalledPaginationIncomplete(t *testing.T) {
	fixture := testbackend.MockFixture{
		SchemaVersion: testbackend.MockFixtureSchemaVersion,
		JiraContext:   "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{{
			Method: "GET", Path: "/jira/rest/api/2/search",
			QueryEquals: map[string]string{
				"jql": "project = PROJ", "startAt": "0", "maxResults": "50", "fields": "summary,status",
			},
			Status: 200,
			Body:   json.RawMessage(`{"startAt":0,"maxResults":50,"total":3,"issues":[]}`),
		}},
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	result := callToolOK(t, client, "jira_issue_search", map[string]any{
		"jql": "project = PROJ", "columns": []string{"key", "summary", "status"},
	})
	content, ok := result.StructuredContent.(map[string]any)
	page, pageOK := content["page"].(map[string]any)
	if !ok || !pageOK || page["count"] != float64(0) || page["complete"] != false ||
		page["truncated"] != true || page["partial_reason"] != domain.IssueSearchPartialPaginationStalled ||
		page["next_cursor"] != nil {
		t.Fatalf("issue search=%#v", result.StructuredContent)
	}
}

func TestSyntheticPaginatedJiraSearchThroughMCPReachesTerminalPage(t *testing.T) {
	tests := []struct {
		name, directory, query string
		limit                  int
		cursors                []string
		keys                   [][]string
	}{
		{
			name: "primary", directory: "jira-paginated-search-mcp",
			query: "project = NOVA AND labels = readiness ORDER BY updated DESC, key ASC",
			limit: 250, cursors: []string{"", "2", "4"},
			keys: [][]string{{"NOVA-6", "NOVA-5"}, {"NOVA-4", "NOVA-3"}, {"NOVA-2", "NOVA-1"}},
		},
		{
			name: "holdout", directory: "jira-paginated-search-mcp-holdout",
			query: "project = ORBIT AND labels = launch ORDER BY priority DESC, key ASC",
			limit: 125, cursors: []string{"", "3"},
			keys: [][]string{{"ORBIT-15", "ORBIT-11", "ORBIT-9"}, {"ORBIT-7", "ORBIT-2"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtureFile, err := os.Open(filepath.Join(
				"..", "..", "benchmarks", "agent-eval", test.directory, "fixture.json",
			))
			if err != nil {
				t.Fatal(err)
			}
			fixture, decodeErr := testbackend.DecodeMockFixture(fixtureFile)
			closeErr := fixtureFile.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
			}
			backend, err := testbackend.StartMockBackend(fixture)
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
			for pageIndex, cursor := range test.cursors {
				arguments := map[string]any{
					"jql": test.query, "columns": []string{"key", "summary", "status", "updated"},
					"limit": test.limit, "max_bytes": 65536,
				}
				if cursor != "" {
					arguments["cursor"] = cursor
				}
				result := callToolOK(t, client, "jira_issue_search", arguments)
				content, contentOK := result.StructuredContent.(map[string]any)
				source, sourceOK := content["source"].(map[string]any)
				selection, selectionOK := content["selection"].(map[string]any)
				projection, projectionOK := content["projection"].(map[string]any)
				page, pageOK := content["page"].(map[string]any)
				rows, rowsOK := content["rows"].([]any)
				if !contentOK || !sourceOK || !selectionOK || !projectionOK || !pageOK || !rowsOK ||
					source["kind"] != "jql" || selection["jql"] != test.query ||
					projection["ordering"] != "jql-order" ||
					page["count"] != float64(len(test.keys[pageIndex])) ||
					len(rows) != len(test.keys[pageIndex]) {
					t.Fatalf("page %d content=%#v", pageIndex, result.StructuredContent)
				}
				terminal := pageIndex == len(test.cursors)-1
				if page["complete"] != terminal || page["truncated"] == terminal {
					t.Fatalf("page %d completeness=%#v", pageIndex, page)
				}
				if terminal {
					if page["next_cursor"] != nil {
						t.Fatalf("terminal page next_cursor=%#v", page["next_cursor"])
					}
				} else if page["next_cursor"] != test.cursors[pageIndex+1] {
					t.Fatalf("page %d next_cursor=%#v want=%q", pageIndex, page["next_cursor"], test.cursors[pageIndex+1])
				}
				for rowIndex, rawRow := range rows {
					row, ok := rawRow.(map[string]any)
					if !ok || row["key"] != test.keys[pageIndex][rowIndex] ||
						row["position"] != float64(rowIndex) {
						t.Fatalf("page %d row %d=%#v", pageIndex, rowIndex, rawRow)
					}
				}
			}
			methods, unexpected, duplicates := backend.Summary()
			if methods["GET"] != len(test.cursors) || len(methods) != 1 ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func TestJiraStructureGetAcceptsIntegerAndCanonicalDecimalString(t *testing.T) {
	for _, value := range []any{int64(9), "9"} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			reader := &recordingJiraReader{}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()

			result := callToolOK(t, client, "jira_structure_get", map[string]any{"structure_id": value})
			content, ok := result.StructuredContent.(map[string]any)
			if !ok || content["id"] != float64(9) || reader.structureID != 9 {
				t.Fatalf("value=%#v content=%#v called_with=%d", value, result.StructuredContent, reader.structureID)
			}
		})
	}
}

func TestParseStructureIDInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "integer", raw: "9", want: 9},
		{name: "integer whitespace", raw: " \n9\t", want: 9},
		{name: "string", raw: `"9"`, want: 9},
		{name: "maximum", raw: `"9223372036854775807"`, want: 9223372036854775807},
		{name: "empty", raw: ""},
		{name: "null", raw: "null"},
		{name: "boolean", raw: "true"},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-9"},
		{name: "fraction", raw: "9.0"},
		{name: "exponent", raw: "9e0"},
		{name: "empty string", raw: `""`},
		{name: "zero string", raw: `"0"`},
		{name: "leading zero string", raw: `"09"`},
		{name: "signed string", raw: `"+9"`},
		{name: "whitespace string", raw: `" 9 "`},
		{name: "fraction string", raw: `"9.0"`},
		{name: "overflow string", raw: `"9223372036854775808"`},
		{name: "object", raw: `{}`},
		{name: "array", raw: `[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseStructureIDInput(json.RawMessage(test.raw))
			if test.want > 0 {
				if err != nil || got != test.want {
					t.Fatalf("got=%d err=%v want=%d", got, err, test.want)
				}
				return
			}
			if !errors.Is(err, domain.ErrUsage) || got != 0 {
				t.Fatalf("got=%d err=%v", got, err)
			}
		})
	}

}

func TestJiraStructureGetRejectsInvalidIDsBeforeBackendResolution(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing", args: map[string]any{}},
		{name: "unknown property", args: map[string]any{"structure_id": 9, "extra": true}},
		{name: "null", args: map[string]any{"structure_id": nil}},
		{name: "boolean", args: map[string]any{"structure_id": true}},
		{name: "zero", args: map[string]any{"structure_id": 0}},
		{name: "negative", args: map[string]any{"structure_id": -9}},
		{name: "fraction", args: map[string]any{"structure_id": 9.5}},
		{name: "empty string", args: map[string]any{"structure_id": ""}},
		{name: "leading zero string", args: map[string]any{"structure_id": "09"}},
		{name: "signed string", args: map[string]any{"structure_id": "+9"}},
		{name: "whitespace string", args: map[string]any{"structure_id": " 9 "}},
		{name: "overflow string", args: map[string]any{"structure_id": "9223372036854775808"}},
		{name: "object", args: map[string]any{"structure_id": map[string]any{}}},
		{name: "array", args: map[string]any{"structure_id": []any{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := false
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) {
					resolved = true
					return &recordingJiraReader{}, nil
				},
			}))
			defer closeSessions()

			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_structure_get", Arguments: test.args,
			})
			if err == nil && (result == nil || !result.IsError) {
				t.Fatalf("invalid input succeeded: result=%+v", result)
			}
			if resolved {
				t.Fatal("Jira backend was resolved for invalid input")
			}
		})
	}
}

func TestToolBoundsFailBeforeBackendResolution(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "jira_fields", args: map[string]any{"max_bytes": 1023}},
		{name: "jira_fields", args: map[string]any{"max_bytes": 1048577}},
		{name: "jira_issue_search", args: map[string]any{"jql": "project=PROJ", "limit": 1001}},
		{name: "jira_issue_search", args: map[string]any{"jql": "project=PROJ", "max_bytes": 1023}},
		{name: "jira_issue_search", args: map[string]any{"jql": "project=PROJ", "max_bytes": 1048577}},
		{name: "jira_issue_search", args: map[string]any{
			"jql": "project=PROJ", "columns": []string{"key"}, "fields": []string{"status"},
		}},
		{name: "jira_issue_search", args: map[string]any{
			"jql": "project=PROJ", "columns": []string{"key"}, "projection": []string{"status"},
		}},
		{name: "jira_issue_search", args: map[string]any{
			"jql": "project=PROJ", "fields": []string{"key"}, "projection": []string{"status"},
		}},
		{name: "jira_issue_search", args: map[string]any{
			"jql": "project=PROJ", "columns": []string{"key"}, "fields": []string{"summary"}, "projection": []string{"status"},
		}},
		{name: "jira_issue_field_get", args: map[string]any{"key": "PROJ-1", "field": "Delivery Notes", "max_bytes": 128}},
		{name: "jira_issue_history", args: map[string]any{"key": "   "}},
		{name: "jira_issue_history", args: map[string]any{"key": "PROJ-1", "max_bytes": 1023}},
		{name: "jira_issue_history", args: map[string]any{"key": "PROJ-1", "max_bytes": 1048577}},
		{name: "jira_issue_refs", args: map[string]any{}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "jql": "project=PROJ"}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "limit": 1}},
		{name: "jira_issue_refs", args: map[string]any{"jql": "project=PROJ"}},
		{name: "jira_issue_refs", args: map[string]any{"jql": "project=PROJ", "limit": 26}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "fields": []string{"Delivery Notes"}}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "fields": []string{"customfield_1", "customfield_1"}}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "fields": []string{
			"a", "b", "c", "d", "e", "f", "g", "h", "i",
		}}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "max_bytes": 1023}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "max_bytes": 1048577}},
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "limit": 1001}},
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "max_bytes": 1023}},
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "max_bytes": 1048577}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "max_rows": 1001}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "max_bytes": 1023}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "folder_id": "a", "folder_row": 2}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "fields": []string{"key", "key"}}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "fields": []string{strings.Repeat("x", jiraStructureFieldIDMaxBytes+1)}}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "folder_path": strings.Repeat("x", jiraStructureFolderPathMaxBytes+1)}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "folder_path": "Plans//Quarter"}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "expected_forest_signature": 55}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "expected_forest_version": 7}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "expected_forest_signature": 0, "expected_forest_version": 0}},
		{name: "jira_structure_view", args: map[string]any{"structure_id": 1, "expected_forest_signature": 55, "expected_forest_version": -1}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"comments"}, "comment_limit": 51}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"confluence"}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "projection": "brief"}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "max_bytes": 1023}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "max_bytes": 1048577}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "limit": 101}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "max_bytes": 1023}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "max_bytes": 1048577}},
		{name: "confluence_page_section", args: map[string]any{"reference": "1", "heading": "Results", "expected_page_version": 3, "max_bytes": 1048577}},
		{name: "confluence_page_section", args: map[string]any{"reference": "1", "heading": "Results", "expected_page_version": -1}},
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

func TestJiraEvidenceOutputBoundsFailWithoutLeakingContent(t *testing.T) {
	const privateMarker = "PRIVATE-JIRA-EVIDENCE-MARKER"
	reader := &oversizedJiraReader{
		recordingJiraReader: &recordingJiraReader{},
		payload:             privateMarker + strings.Repeat("x", 4<<10),
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "jira_fields", args: map[string]any{"max_bytes": 1024}},
		{name: "jira_issue_search", args: map[string]any{"jql": "project=PROJ", "max_bytes": 1024}},
		{name: "jira_issue_history", args: map[string]any{
			"key": "PROJ-1", "fields": []string{"Delivery Notes"}, "max_bytes": 1024,
		}},
		{name: "jira_issue_refs", args: map[string]any{"key": "PROJ-1", "max_bytes": 1024}},
		{name: "jira_epic_digest", args: map[string]any{
			"key": "PROJ-1", "include": []string{"identity"}, "projection": "full", "max_bytes": 1024,
		}},
		{name: "jira_board_view", args: map[string]any{"board_id": 1, "max_bytes": 1024}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(privateMarker)) {
				t.Fatalf("oversize result leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "output_limit_exceeded" || got.Remediation != "narrow_or_raise_bound" ||
				!strings.Contains(got.Message, "exceeds max_bytes") {
				t.Fatalf("classified error=%+v decode=%v", got, err)
			}
		})
	}

	summary := callToolOK(t, client, "jira_fields", map[string]any{"summary_only": true, "max_bytes": 1024})
	content, ok := summary.StructuredContent.(map[string]any)
	fields, fieldsOK := content["fields"].([]any)
	if !ok || !fieldsOK || content["projection"] != "summary" ||
		content["count"] != float64(1) || content["custom_count"] != float64(1) || len(fields) != 0 {
		t.Fatalf("compact field summary=%#v", summary.StructuredContent)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(privateMarker)) {
		t.Fatalf("compact field summary leaked definition: %s", encoded)
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "output_limit_exceeded" || got.Remediation != "narrow_or_raise_bound" ||
		got.Message != "Confluence table result exceeds the selected output bound" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestConfluenceSearchOutputBoundFailsWithoutLeakingContent(t *testing.T) {
	const privateMarker = "PRIVATE-CONFLUENCE-SEARCH-MARKER"
	reader := &recordingConfluenceReader{searchText: privateMarker + strings.Repeat("x", 4<<10)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_search",
		Arguments: map[string]any{
			"cql": "siteSearch ~ \"bounded topic\" ", "limit": 10, "max_bytes": 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertConfluenceSearchOversizeError(t, result, privateMarker)
}

func TestProductionConfluenceSearchOutputBoundFailsWithoutLeakingContent(t *testing.T) {
	const privateMarker = "PRIVATE-CONFLUENCE-SEARCH-PRODUCTION-MARKER"
	body, err := json.Marshal(map[string]any{
		"results": []any{map[string]any{
			"content": map[string]any{
				"id": "42", "type": "page", "title": "Bounded search result",
				"space":   map[string]any{"key": "DOC"},
				"version": map[string]any{"number": 1, "when": "2026-07-24T00:00:00.000Z"},
			},
			"excerpt": privateMarker + strings.Repeat("x", 4<<10),
		}},
		"size": 1, "totalCount": 1, "_links": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := testbackend.MockFixture{
		SchemaVersion: testbackend.MockFixtureSchemaVersion,
		JiraContext:   "/jira", ConfluenceContext: "/wiki",
		Routes: []testbackend.MockRoute{{
			Method: http.MethodGet, Path: "/wiki/rest/api/search",
			QueryEquals: map[string]string{"cql": `siteSearch ~ "bounded topic"`},
			Status:      http.StatusOK, Body: body,
		}},
	}
	backend, err := testbackend.StartMockBackend(fixture)
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
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_search",
		Arguments: map[string]any{
			"cql": `siteSearch ~ "bounded topic"`, "limit": 10, "max_bytes": 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertConfluenceSearchOversizeError(t, result, privateMarker)
	methods, unexpected, duplicates := backend.Summary()
	if methods[http.MethodGet] != 1 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func assertConfluenceSearchOversizeError(t *testing.T, result *mcp.CallToolResult, privateMarker string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(privateMarker)) {
		t.Fatalf("oversize result leaked or succeeded: %s", encoded)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "output_limit_exceeded" || got.Remediation != "narrow_or_raise_bound" ||
		!strings.Contains(got.Message, "exceeds max_bytes") {
		t.Fatalf("classified error=%+v decode=%v", got, err)
	}
}

func TestJiraStructureOutputBoundFailsWithoutLeakingContent(t *testing.T) {
	reader := &recordingJiraReader{structureText: "PRIVATE-MARKER-" + strings.Repeat("x", 4<<10)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_structure_view", Arguments: map[string]any{
		"structure_id": 9, "fields": []string{"summary"}, "max_bytes": 1024,
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "output_limit_exceeded" || got.Remediation != "narrow_or_raise_bound" ||
		got.Message != "Jira Structure result exceeds the selected output bound" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestJiraStructureMetadataBoundFailsWithoutLeakingContent(t *testing.T) {
	reader := &recordingJiraReader{structureName: "PRIVATE-MARKER-" + strings.Repeat("x", jiraStructureMetadataMaxBytes)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_structure_get", Arguments: map[string]any{"structure_id": 9}})
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "check_failed" || got.Remediation != "review_failed_check" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestJiraStructureViewRejectsUnreconciledApplicationResults(t *testing.T) {
	for _, mode := range []string{
		"row-count", "selection", "wrong-root", "second-root", "wrong-path", "projection", "completeness",
		"forest-gated",
	} {
		t.Run(mode, func(t *testing.T) {
			reader := &invalidStructureReader{recordingJiraReader: &recordingJiraReader{}, mode: mode}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			args := map[string]any{"structure_id": 9, "folder_id": "folder-a"}
			if mode == "wrong-path" {
				delete(args, "folder_id")
				args["folder_path"] = "Plans/Quarter"
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_structure_view", Arguments: args})
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

func TestJiraStructureViewPreservesUngatedZeroForestVersion(t *testing.T) {
	reader := &invalidStructureReader{recordingJiraReader: &recordingJiraReader{}, mode: "forest-version"}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jira_structure_view", Arguments: map[string]any{
			"structure_id": 9, "folder_id": "folder-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	content, ok := result.StructuredContent.(map[string]any)
	version, versionOK := content["forest_version"].(map[string]any)
	if result.IsError || !ok || !versionOK ||
		version["signature"] != float64(0) || version["version"] != float64(0) ||
		content["forest_version_gated"] != false {
		t.Fatalf("result=%+v", result)
	}
}

func TestJiraStructureViewRejectsBoundResultForWrongForestVersion(t *testing.T) {
	reader := &invalidStructureReader{recordingJiraReader: &recordingJiraReader{}, mode: "wrong-expected-forest"}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jira_structure_view", Arguments: map[string]any{
			"structure_id": 9, "expected_forest_signature": 55, "expected_forest_version": 7,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("result=%+v", result)
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
		{name: "summary schema", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-schema"},
		{name: "summary cell contract", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-cell-contract"},
		{name: "summary version", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-version"},
		{name: "summary gate", tool: "confluence_table_summary", args: map[string]any{"reference": "42"}, mode: "summary-gate"},
		{name: "bound summary ungated", tool: "confluence_table_summary", args: map[string]any{"reference": "42", "expected_page_version": 7}, mode: "summary-bound-ungated"},
		{name: "bound summary wrong version", tool: "confluence_table_summary", args: map[string]any{"reference": "42", "expected_page_version": 7}, mode: "summary-bound-wrong-version"},
		{name: "extract selection", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract"},
		{name: "extract returned count", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-returned-count"},
		{name: "extract reconciliation", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-reconciliation"},
		{name: "extract dimensions", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-dimensions"},
		{name: "extract summary", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-summary"},
		{name: "extract schema", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-schema"},
		{name: "extract cell contract", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-cell-contract"},
		{name: "extract version", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-version"},
		{name: "extract gate", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-gate"},
		{name: "bound extract ungated", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1, "expected_page_version": 7}, mode: "extract-bound-ungated"},
		{name: "bound extract wrong version", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1, "expected_page_version": 7}, mode: "extract-bound-wrong-version"},
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

func TestConfluenceTableSelectionErrorIsDistinctAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-TABLE-SELECTION-SECRET"
	body := `<table><tbody><tr><td><a href="https://backend.invalid/` + marker + `">` + marker + `</a></td></tr></tbody></table>` +
		`<table><tbody><tr><td>` + marker + `</td></tr></tbody></table>`
	_, selectionErr := app.ExtractTablesFromCSF("PAGE-"+marker, "Title "+marker, []byte(body), 7)
	var typed *app.ConfluenceTableSelectionError
	if !errors.As(selectionErr, &typed) || typed.Requested != 7 || typed.Available != 2 {
		t.Fatalf("test fixture must produce an out-of-range selection error: %v", selectionErr)
	}
	for _, tool := range []string{"confluence_table_summary", "confluence_table_extract"} {
		t.Run(tool, func(t *testing.T) {
			reader := &recordingConfluenceReader{tableErr: selectionErr}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: tool, Arguments: map[string]any{"reference": "42", "table": 7},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "not_found" || got.Remediation != "summarize_then_select_table" ||
				got.Message != "selected Confluence table index 7 is out of range; available table count is 2" {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "PAGE-", "Title ", "https://", "backend.invalid"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("selection error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestConfluenceTableVersionGateIsForwardedAndMismatchIsContentFree(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()

	callToolOK(t, client, "confluence_table_summary", map[string]any{
		"reference": "42", "expected_page_version": 7,
	})
	callToolOK(t, client, "confluence_table_extract", map[string]any{
		"reference": "42", "table": 1, "expected_page_version": 7,
	})
	if reader.tableSummaryOpts.ExpectedPageVersion != 7 || reader.tableExtractOpts.ExpectedPageVersion != 7 {
		t.Fatalf("summary opts=%+v extract opts=%+v", reader.tableSummaryOpts, reader.tableExtractOpts)
	}

	const marker = "SYNTHETIC-TABLE-VERSION-SECRET"
	reader.tableErr = fmt.Errorf("wrapped %s: %w", marker, &app.ConfluencePageVersionMismatchError{Expected: 7, Current: 8})
	for _, tool := range []string{"confluence_table_summary", "confluence_table_extract"} {
		args := map[string]any{"reference": "42", "expected_page_version": 7}
		if tool == "confluence_table_extract" {
			args["table"] = 1
		}
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
			t.Fatalf("%s result=%+v", tool, result)
		}
		text, _ := result.Content[0].(*mcp.TextContent)
		var got toolError
		if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
			got.Kind != "check_failed" ||
			got.Remediation != "reread_table_summary_then_retry_expected_version" ||
			got.Message != "expected Confluence page version 7 does not match the current page version 8" {
			t.Fatalf("%s error=%+v decode=%v", tool, got, err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(marker)) {
			t.Fatalf("%s leaked wrapped content: %s", tool, encoded)
		}
	}
}

func TestConfluenceTableVersionGateRejectsNegativeBeforeReader(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()
	for _, tool := range []string{"confluence_table_summary", "confluence_table_extract"} {
		args := map[string]any{"reference": "42", "expected_page_version": -1}
		if tool == "confluence_table_extract" {
			args["table"] = 1
		}
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("%s accepted a negative version: %+v", tool, result)
		}
	}
	if reader.tableSummaryReference != "" || reader.tableExtractReference != "" {
		t.Fatalf("invalid versions reached reader: summary=%q extract=%q", reader.tableSummaryReference, reader.tableExtractReference)
	}
}

// attachmentInventoryClient wires one recording reader into a live session.
func attachmentInventoryClient(t *testing.T, reader *recordingConfluenceReader) *mcp.ClientSession {
	t.Helper()
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	return client
}

func pageMetadataClient(t *testing.T, reader *recordingConfluenceReader) *mcp.ClientSession {
	t.Helper()
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	return client
}

func TestConfluencePageMetadataForwardsReferenceAndPreservesTriState(t *testing.T) {
	for _, test := range []struct {
		name, state string
	}{
		{name: "unknown", state: app.ConfluenceRestrictionUnknown},
		{name: "restricted", state: app.ConfluenceRestrictionRestricted},
		{name: "unrestricted", state: app.ConfluenceRestrictionUnrestricted},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{metadataResult: &app.ConfluencePageMetadataResult{
				SchemaVersion: app.ConfluencePageMetadataSchemaVersion,
				ID:            "42", Title: "Synthetic page", Space: "DOCS", Version: 7,
				Updated: "2026-07-25T12:00:00.000Z", RestrictionState: test.state,
			}}
			result := callToolOK(t, pageMetadataClient(t, reader), "confluence_page_meta", map[string]any{
				"reference": "/wiki/pages/viewpage.action?pageId=42",
			})
			if reader.metadataCalls != 1 || reader.metadataReference != "/wiki/pages/viewpage.action?pageId=42" {
				t.Fatalf("metadata calls=%d reference=%q", reader.metadataCalls, reader.metadataReference)
			}
			content, ok := result.StructuredContent.(map[string]any)
			if !ok || len(content) != 7 ||
				content["schema_version"] != float64(app.ConfluencePageMetadataSchemaVersion) ||
				content["id"] != "42" || content["title"] != "Synthetic page" ||
				content["space"] != "DOCS" || content["version"] != float64(7) ||
				content["updated"] != "2026-07-25T12:00:00.000Z" ||
				content["restriction_state"] != test.state {
				t.Fatalf("metadata=%#v", result.StructuredContent)
			}
			for _, forbidden := range []string{"url", "labels", "ancestors", "restrictions", "principals", "body"} {
				if _, exists := content[forbidden]; exists {
					t.Fatalf("metadata exposed %q: %#v", forbidden, content)
				}
			}
		})
	}
}

func TestConfluencePageMetadataRejectsEmptyReferenceBeforeReaderConstruction(t *testing.T) {
	constructed := false
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) {
			constructed = true
			return &recordingConfluenceReader{}, nil
		},
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_page_meta", Arguments: map[string]any{"reference": " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if constructed || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("constructed=%t result=%+v", constructed, result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "usage_error" || got.Message != "invalid Confluence page metadata request" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestConfluencePageMetadataRejectsUnknownInputBeforeReaderConstruction(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_page_meta",
		Arguments: map[string]any{
			"reference": "42",
			"include":   []string{"labels"},
		},
	})
	if err != nil || result == nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("unknown input was not rejected as a tool error: result=%+v err=%v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != sdkSchemaValidationToolError.Error() || strings.Contains(text.Text, "include") {
		t.Fatalf("unknown input was not redacted: content=%+v", result.Content)
	}
	if reader.metadataCalls != 0 || reader.metadataReference != "" {
		t.Fatalf("unknown input reached reader: calls=%d reference=%q", reader.metadataCalls, reader.metadataReference)
	}
}

func TestConfluencePageMetadataRejectsUnreconciledApplicationResults(t *testing.T) {
	valid := func() *app.ConfluencePageMetadataResult {
		return &app.ConfluencePageMetadataResult{
			SchemaVersion: app.ConfluencePageMetadataSchemaVersion,
			ID:            "42", Title: "Synthetic page", Space: "DOCS", Version: 7,
			RestrictionState: app.ConfluenceRestrictionUnknown,
		}
	}
	tests := []struct {
		name   string
		mutate func(*app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult
	}{
		{name: "nil", mutate: func(*app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult { return nil }},
		{name: "schema", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.SchemaVersion++
			return result
		}},
		{name: "id", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.ID = ""
			return result
		}},
		{name: "title", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.Title = ""
			return result
		}},
		{name: "space", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.Space = ""
			return result
		}},
		{name: "version", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.Version = 0
			return result
		}},
		{name: "restriction state", mutate: func(result *app.ConfluencePageMetadataResult) *app.ConfluencePageMetadataResult {
			result.RestrictionState = "missing"
			return result
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{
				metadataResult: test.mutate(valid()), metadataResultSet: true,
			}
			result, err := pageMetadataClient(t, reader).CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_meta", Arguments: map[string]any{"reference": "42"},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "check_failed" || got.Message != "Confluence page metadata failed validation" {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
		})
	}
}

func TestConfluencePageMetadataOutputBoundFailsWithoutLeakingContent(t *testing.T) {
	const marker = "SYNTHETIC-PAGE-METADATA-PRIVATE-MARKER"
	reader := &recordingConfluenceReader{metadataResult: &app.ConfluencePageMetadataResult{
		SchemaVersion: app.ConfluencePageMetadataSchemaVersion,
		ID:            "42", Title: marker + strings.Repeat("x", confluencePageMetadataMaxBytes),
		Space: "DOCS", Version: 7, RestrictionState: app.ConfluenceRestrictionRestricted,
	}}
	result, err := pageMetadataClient(t, reader).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_page_meta", Arguments: map[string]any{"reference": "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(marker)) ||
		len(result.Content) != 1 {
		t.Fatalf("oversize metadata leaked or succeeded: %s", encoded)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "output_limit_exceeded" ||
		got.Remediation != "use_cli_conf_page_meta" ||
		got.Message != "Confluence page metadata exceeds its output bound" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestConfluencePageMetadataErrorsAreStaticAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-PAGE-METADATA-BACKEND-SECRET"
	for _, test := range []struct {
		name, kind, message string
		err                 error
	}{
		{name: "config", kind: "configuration_error", err: fmt.Errorf("%w: %s", domain.ErrConfig, marker), message: "Confluence page metadata service is not configured"},
		{name: "auth", kind: "authentication_failed", err: fmt.Errorf("%w: %s", domain.ErrAuth, marker), message: "Confluence page metadata authentication failed"},
		{name: "forbidden", kind: "forbidden", err: fmt.Errorf("%w: %s", domain.ErrForbidden, marker), message: "Confluence page metadata access is forbidden"},
		{name: "not found", kind: "not_found", err: fmt.Errorf("%w: %s", domain.ErrNotFound, marker), message: "Confluence page was not found"},
		{name: "check", kind: "check_failed", err: fmt.Errorf("%w: %s", domain.ErrCheckFailed, marker), message: "Confluence page metadata failed validation"},
		{name: "api", kind: "api_error", err: &httpx.APIError{
			Status: 500, Method: "GET", Path: "/rest/api/content/" + marker, Body: marker,
		}, message: "Confluence page metadata API request failed"},
		{name: "rate limited", kind: "rate_limited", err: &httpx.APIError{
			Status: http.StatusTooManyRequests, Method: "GET",
			Path: "/rest/api/content/" + marker, Body: marker,
		}, message: "Confluence page metadata rate limit was exhausted"},
		{name: "transport", kind: "transport_error", err: &httpx.TransportError{
			Method: "GET", Category: marker,
		}, message: "Confluence page metadata transport failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{metadataErr: test.err}
			result, err := pageMetadataClient(t, reader).CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_meta", Arguments: map[string]any{"reference": "42"},
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !result.IsError || result.StructuredContent != nil || bytes.Contains(encoded, []byte(marker)) ||
				len(result.Content) != 1 {
				t.Fatalf("error leaked or succeeded: %s", encoded)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content=%T", result.Content[0])
			}
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
		})
	}
}

func TestConfluenceAttachmentListForwardsExactInput(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client := attachmentInventoryClient(t, reader)
	result := callToolOK(t, client, "confluence_attachment_list", map[string]any{
		"reference": "/pages/viewpage.action?pageId=42", "expected_page_version": 7,
	})
	if reader.attachmentReference != "/pages/viewpage.action?pageId=42" || reader.attachmentOpts.ExpectedPageVersion != 7 {
		t.Fatalf("reference=%q opts=%+v", reader.attachmentReference, reader.attachmentOpts)
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok || content["schema_version"] != float64(1) || content["page_id"] != "42" ||
		content["page_version"] != float64(7) || content["count"] != float64(0) || content["complete"] != true {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	attachments, ok := content["attachments"].([]any)
	if !ok || len(attachments) != 0 {
		t.Fatalf("an exhausted empty inventory must be an empty array: %#v", result.StructuredContent)
	}
	if _, exists := content["partial_reason"]; exists {
		t.Fatalf("a complete inventory must omit partial_reason: %#v", result.StructuredContent)
	}
}

// The projection is the privacy boundary: the source record carries an author
// comment and a download path, and neither may cross it.
func TestConfluenceAttachmentListOutputIsMetadataOnly(t *testing.T) {
	const marker = "SYNTHETIC-ATTACHMENT-SECRET"
	reader := &recordingConfluenceReader{attachmentResult: &app.ConfluenceAttachmentInventoryResult{
		SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: 1, Complete: true,
		Attachments: []domain.Attachment{{
			ID: "att1", Title: "diagram.png", MediaType: "image/png", FileSize: 4096, Version: 3,
			Comment: "comment " + marker, DownPath: "/download/attachments/42/" + marker + ".png",
		}},
	}}
	client := attachmentInventoryClient(t, reader)
	result := callToolOK(t, client, "confluence_attachment_list", map[string]any{
		"reference": "42", "expected_page_version": 7,
	})
	content, _ := result.StructuredContent.(map[string]any)
	attachments, ok := content["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
	attachment, _ := attachments[0].(map[string]any)
	if attachment["id"] != "att1" || attachment["title"] != "diagram.png" || attachment["media_type"] != "image/png" ||
		attachment["file_size"] != float64(4096) || attachment["version"] != float64(3) {
		t.Fatalf("attachment=%#v", attachment)
	}
	for _, forbidden := range []string{"comment", "down_path", "downPath", "download"} {
		if _, exists := attachment[forbidden]; exists {
			t.Fatalf("attachment projection exposed %q: %#v", forbidden, attachment)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{marker, "/download/", "comment"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("attachment inventory leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestConfluenceAttachmentListSurfacesStaticPartialReason(t *testing.T) {
	for _, reason := range []string{"page_limit", "item_limit", "pagination_stalled", "legacy_unqualified"} {
		t.Run(reason, func(t *testing.T) {
			reader := &recordingConfluenceReader{attachmentResult: &app.ConfluenceAttachmentInventoryResult{
				SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: 1, PartialReason: reason,
				Attachments: []domain.Attachment{{ID: "att1", Title: "one.png"}},
			}}
			client := attachmentInventoryClient(t, reader)
			result := callToolOK(t, client, "confluence_attachment_list", map[string]any{
				"reference": "42", "expected_page_version": 7,
			})
			content, _ := result.StructuredContent.(map[string]any)
			if content["complete"] != false || content["partial_reason"] != reason {
				t.Fatalf("content=%#v", result.StructuredContent)
			}
		})
	}
}

// The tool refuses an unbound read outright, before any backend call. Omitting
// the version entirely is rejected by the strict input schema; a present but
// non-positive version is rejected by the handler.
func TestConfluenceAttachmentListRequiresPositiveExpectedVersion(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client := attachmentInventoryClient(t, reader)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_attachment_list", Arguments: map[string]any{"reference": "42"},
	})
	if err != nil || result == nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("omitted expected_page_version was not rejected as a tool error: result=%+v err=%v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != sdkSchemaValidationToolError.Error() || strings.Contains(text.Text, "expected_page_version") {
		t.Fatalf("omitted expected_page_version was not redacted: content=%+v", result.Content)
	}
	for _, args := range []map[string]any{
		{"reference": "42", "expected_page_version": 0},
		{"reference": "42", "expected_page_version": -3},
		{"reference": "   ", "expected_page_version": 7},
	} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "confluence_attachment_list", Arguments: args,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || result.StructuredContent != nil {
			t.Fatalf("args=%#v result=%+v", args, result)
		}
		text, _ := result.Content[0].(*mcp.TextContent)
		var got toolError
		if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "usage_error" ||
			got.Message != "invalid Confluence attachment inventory request" {
			t.Fatalf("args=%#v error=%+v decode=%v", args, got, err)
		}
	}
	if reader.attachmentCalls != 0 {
		t.Fatalf("an unbound request reached the backend %d times", reader.attachmentCalls)
	}
}

func TestConfluenceAttachmentListRejectsOutOfRangeByteBounds(t *testing.T) {
	for _, maxBytes := range []int{1023, 1048577, -1} {
		reader := &recordingConfluenceReader{}
		client := attachmentInventoryClient(t, reader)
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "confluence_attachment_list",
			Arguments: map[string]any{"reference": "42", "expected_page_version": 7, "max_bytes": maxBytes},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || reader.attachmentCalls != 0 {
			t.Fatalf("max_bytes=%d result=%+v calls=%d", maxBytes, result, reader.attachmentCalls)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content=%T", result.Content[0])
		}
		var got toolError
		if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "usage_error" ||
			got.Message != "invalid Confluence attachment inventory request" {
			t.Fatalf("error=%+v decode=%v", got, err)
		}
	}
}

// An oversize inventory is refused, never clipped: a silently shortened
// attachment list is exactly the false-absence evidence this tool prevents.
func TestConfluenceAttachmentListRefusesOversizeInventoryWithoutClipping(t *testing.T) {
	attachments := make([]domain.Attachment, 0, 200)
	for i := 0; i < 200; i++ {
		attachments = append(attachments, domain.Attachment{
			ID: fmt.Sprintf("att%03d", i), Title: fmt.Sprintf("attachment-%03d.png", i),
			MediaType: "image/png", FileSize: int64(i), Version: 1,
		})
	}
	reader := &recordingConfluenceReader{attachmentResult: &app.ConfluenceAttachmentInventoryResult{
		SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: len(attachments), Complete: true,
		Attachments: attachments,
	}}
	client := attachmentInventoryClient(t, reader)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "confluence_attachment_list",
		Arguments: map[string]any{"reference": "42", "expected_page_version": 7, "max_bytes": 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("result=%+v", result)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "output_limit_exceeded" ||
		got.Remediation != "raise_bound_or_use_cli_attachment_list" ||
		got.Message != "Confluence attachment inventory exceeds the selected output bound" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("attachment-000.png")) {
		t.Fatalf("a rejected inventory leaked its rows: %s", encoded)
	}

	// The same inventory is returned whole once the bound admits it.
	ok := callToolOK(t, client, "confluence_attachment_list", map[string]any{
		"reference": "42", "expected_page_version": 7, "max_bytes": 128 << 10,
	})
	content, _ := ok.StructuredContent.(map[string]any)
	rows, _ := content["attachments"].([]any)
	if len(rows) != len(attachments) || content["count"] != float64(len(attachments)) {
		t.Fatalf("bounded inventory was clipped: %d rows", len(rows))
	}
}

// A moved page is reported with two integers and nothing else.
func TestConfluenceAttachmentListVersionMismatchIsContentFree(t *testing.T) {
	reader := &recordingConfluenceReader{
		attachmentErr: &app.ConfluencePageVersionMismatchError{Expected: 7, Current: 9},
	}
	client := attachmentInventoryClient(t, reader)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "confluence_attachment_list",
		Arguments: map[string]any{"reference": "42", "expected_page_version": 7},
	})
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" ||
		got.Remediation != "reread_page_then_retry_expected_version" ||
		got.Message != "expected Confluence page version 7 does not match the current page version 9" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

// Every non-mismatch failure is a static sentence, including backend and
// transport failures, so no backend diagnostic or page text can cross.
func TestConfluenceAttachmentListErrorsAreStaticAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-ATTACHMENT-BACKEND-SECRET"
	for _, test := range []struct {
		name, kind, message string
		err                 error
	}{
		{
			name: "not found", kind: "not_found",
			err: fmt.Errorf("%w: page %s is gone", domain.ErrNotFound, marker), message: "Confluence page was not found",
		},
		{
			name: "forbidden", kind: "forbidden",
			err: fmt.Errorf("%w: %s", domain.ErrForbidden, marker), message: "Confluence attachment inventory access is forbidden",
		},
		{
			name: "auth", kind: "authentication_failed",
			err: fmt.Errorf("%w: %s", domain.ErrAuth, marker), message: "Confluence attachment inventory authentication failed",
		},
		{
			name: "config", kind: "configuration_error",
			err: fmt.Errorf("%w: %s", domain.ErrConfig, marker), message: "Confluence attachment inventory service is not configured",
		},
		{
			name: "check failed", kind: "check_failed",
			err: fmt.Errorf("%w: %s", domain.ErrCheckFailed, marker), message: "Confluence attachment inventory failed validation",
		},
		{
			name: "backend", kind: "api_error",
			err:     &httpx.APIError{Status: 500, Method: "GET", Path: "/rest/api/content/" + marker, Body: marker},
			message: "Confluence attachment inventory read failed",
		},
		{
			name: "transport", kind: "transport_error",
			err: &httpx.TransportError{Category: marker}, message: "Confluence attachment inventory read failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{attachmentErr: test.err}
			client := attachmentInventoryClient(t, reader)
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "confluence_attachment_list",
				Arguments: map[string]any{"reference": "42", "expected_page_version": 7},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil {
				t.Fatalf("result=%+v", result)
			}
			text, _ := result.Content[0].(*mcp.TextContent)
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != test.kind || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(marker)) {
				t.Fatalf("%s leaked backend text: %s", test.name, encoded)
			}
		})
	}
}

// Unreconciled evidence is refused before it becomes a client result.
func TestConfluenceAttachmentListRejectsUnreconciledInventory(t *testing.T) {
	for name, inventory := range map[string]*app.ConfluenceAttachmentInventoryResult{
		"absent":            nil,
		"wrong schema":      {SchemaVersion: 2, PageID: "42", PageVersion: 7, Complete: true, Attachments: []domain.Attachment{}},
		"empty page id":     {SchemaVersion: 1, PageID: " ", PageVersion: 7, Complete: true, Attachments: []domain.Attachment{}},
		"other version":     {SchemaVersion: 1, PageID: "42", PageVersion: 9, Complete: true, Attachments: []domain.Attachment{}},
		"nil collection":    {SchemaVersion: 1, PageID: "42", PageVersion: 7, Complete: true},
		"count mismatch":    {SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: 2, Complete: true, Attachments: []domain.Attachment{{ID: "att1"}}},
		"complete + reason": {SchemaVersion: 1, PageID: "42", PageVersion: 7, Complete: true, PartialReason: "page_limit", Attachments: []domain.Attachment{}},
		"partial no reason": {SchemaVersion: 1, PageID: "42", PageVersion: 7, Attachments: []domain.Attachment{}},
		"unknown reason":    {SchemaVersion: 1, PageID: "42", PageVersion: 7, PartialReason: "backend said so", Attachments: []domain.Attachment{}},
		"duplicate ids": {SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: 2, Complete: true,
			Attachments: []domain.Attachment{{ID: "att1"}, {ID: "att1"}}},
		"negative size": {SchemaVersion: 1, PageID: "42", PageVersion: 7, Count: 1, Complete: true,
			Attachments: []domain.Attachment{{ID: "att1", FileSize: -1}}},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &recordingConfluenceReader{attachmentResult: inventory}
			if inventory == nil {
				reader.attachmentResult = &app.ConfluenceAttachmentInventoryResult{}
			}
			client := attachmentInventoryClient(t, reader)
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "confluence_attachment_list",
				Arguments: map[string]any{"reference": "42", "expected_page_version": 7},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil {
				t.Fatalf("%s produced a result: %+v", name, result)
			}
			text, _ := result.Content[0].(*mcp.TextContent)
			var got toolError
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" ||
				got.Message != "Confluence attachment inventory failed validation" {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
		})
	}
}

// The production wiring must expose the tool through the real application
// service, not only through a test double.
func TestConfluenceAttachmentListUsesProductionDependencies(t *testing.T) {
	var _ ConfluenceReader = (*app.ConfluenceService)(nil)
	client, closeSessions := connectTestClient(t, New("test", ProductionDependencies("test")))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "confluence_attachment_list",
		Arguments: map[string]any{"reference": "42", "expected_page_version": 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("an unconfigured backend must fail closed: %+v", result)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	var got toolError
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "configuration_error" ||
		got.Message != "Confluence attachment inventory service is not configured" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func reconciledConfluenceOutlineEntries(count int) []app.ConfluenceOutlineEntry {
	entries := make([]app.ConfluenceOutlineEntry, count)
	for i := range entries {
		title := fmt.Sprintf("Heading %d", i+1)
		entries[i] = app.ConfluenceOutlineEntry{
			Index: i + 1, Level: 2, Title: title, Path: []string{title}, Occurrence: 1,
		}
	}
	return entries
}

func TestConfluenceOutlineAndSectionPartialReadsCarryStaticReasons(t *testing.T) {
	for _, test := range []struct {
		name, tool, reason string
		args               map[string]any
		reader             *recordingConfluenceReader
	}{
		{
			name: "outline heading limit", tool: "confluence_page_outline", reason: "heading_limit",
			args: map[string]any{"reference": "42"},
			reader: &recordingConfluenceReader{outlineResult: &app.ConfluencePageOutlineResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
				Count: 1000, Total: 1001, Complete: false, Truncated: true,
				PartialReason: "heading_limit", OriginalBytes: 90_000, EmittedBytes: 89_000,
				Headings: reconciledConfluenceOutlineEntries(1000),
			}},
		},
		{
			name: "section max bytes", tool: "confluence_page_section", reason: "max_bytes",
			args: map[string]any{"reference": "42", "heading": "Overview", "expected_page_version": 3, "max_bytes": 4096},
			reader: &recordingConfluenceReader{sectionResult: &app.ConfluencePageSectionResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
				Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
				Markdown: "# Overview\n\n[... truncated by atl ...]\n", Complete: false, Truncated: true,
				PartialReason: "max_bytes", OriginalBytes: 14_000, EmittedBytes: 39,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return test.reader, nil },
			}))
			defer closeSessions()
			result := callToolOK(t, client, test.tool, test.args)
			content, ok := result.StructuredContent.(map[string]any)
			if !ok || content["complete"] != false || content["truncated"] != true || content["partial_reason"] != test.reason {
				t.Fatalf("%s content=%#v", test.tool, result.StructuredContent)
			}
			// original_bytes stays the exact bound a client needs to decide
			// whether one re-read can complete the same evidence.
			if content["original_bytes"] != float64(test.reader.partialOriginalBytes()) {
				t.Fatalf("%s lost its original byte bound: %#v", test.tool, result.StructuredContent)
			}
		})
	}
}

func TestConfluenceOutlineAndSectionCompleteReadsOmitPartialReason(t *testing.T) {
	reader := &recordingConfluenceReader{
		outlineResult: &app.ConfluencePageOutlineResult{
			SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
			Count: 1, Total: 1, Complete: true, OriginalBytes: 64, EmittedBytes: 64,
			Headings: []app.ConfluenceOutlineEntry{{Index: 1, Level: 1, Title: "Overview", Path: []string{"Overview"}, Occurrence: 1}},
		},
		sectionResult: &app.ConfluencePageSectionResult{
			SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
			Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
			Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
		},
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()
	for tool, args := range map[string]map[string]any{
		"confluence_page_outline": {"reference": "42"},
		"confluence_page_section": {"reference": "42", "heading": "Overview", "expected_page_version": 3},
	} {
		result := callToolOK(t, client, tool, args)
		content, ok := result.StructuredContent.(map[string]any)
		if !ok || content["complete"] != true {
			t.Fatalf("%s content=%#v", tool, result.StructuredContent)
		}
		if _, exists := content["partial_reason"]; exists {
			t.Fatalf("%s complete read must omit partial_reason: %#v", tool, result.StructuredContent)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte("partial_reason")) {
			t.Fatalf("%s emitted partial_reason on a complete read: %s", tool, encoded)
		}
	}
}

func TestConfluenceSectionSelectionErrorsAreDistinctAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-SECTION-SELECTION-SECRET"
	heading := "Heading " + marker
	// Both fixtures reproduce exactly what the application layer returns: the
	// typed selection error wrapped in the heading-bearing human message.
	ambiguous := fmt.Errorf("%w: Confluence heading %q occurs %d times; pass --occurrence 1..%d",
		&app.ConfluenceSectionSelectionError{Available: 3}, heading, 3, 3)
	stale := fmt.Errorf("%w: Confluence heading %q has %d occurrence(s), not %d",
		&app.ConfluenceSectionSelectionError{Requested: 5, Available: 2}, heading, 2, 5)
	for _, test := range []struct {
		name, kind, message  string
		args                 map[string]any
		err                  error
		requested, available int
	}{
		{
			name: "ambiguous", kind: "check_failed", err: ambiguous, requested: 0, available: 3,
			args:    map[string]any{"reference": "42", "heading": heading, "expected_page_version": 3},
			message: "Confluence heading selection is ambiguous; available occurrence count is 3, so select an occurrence from 1 to 3",
		},
		{
			name: "out of range", kind: "not_found", err: stale, requested: 5, available: 2,
			args:    map[string]any{"reference": "42", "heading": heading, "occurrence": 5, "expected_page_version": 3},
			message: "selected Confluence heading occurrence 5 is out of range; available occurrence count is 2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var typed *app.ConfluenceSectionSelectionError
			if !errors.As(test.err, &typed) || typed.Requested != test.requested || typed.Available != test.available {
				t.Fatalf("test fixture must carry a typed selection error: %v", test.err)
			}
			reader := &recordingConfluenceReader{sectionErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_section", Arguments: test.args,
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Remediation != "outline_then_select_section" || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "Heading ", "--occurrence", "occurrence(s)"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("selection error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

// structureSelectionFixture mirrors what the application layer hands the Structure
// classifier: the sentinel that drives classification, the typed selection error
// that carries the safe exported counts, and the identifier-bearing diagnostic the
// app keeps unexported. The typed error contributes no bytes of its own here (its
// diagnostic field is unexported), so the wire message is exactly `detail`.
func structureSelectionFixture(sentinel error, typed *app.StructureFolderSelectionError, detail string) error {
	return fmt.Errorf("%w: %s%w", sentinel, detail, typed)
}

func TestJiraStructureFolderSelectionErrorsAreDistinctAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-STRUCTURE-SELECTION-SECRET"
	for _, test := range []struct {
		name, kind, message string
		args                map[string]any
		err                 error
		reason              app.StructureFolderSelectionReason
		matches, available  int
	}{
		{
			name: "stale selector", kind: "not_found",
			reason: app.StructureFolderSelectionNotFound, matches: 0, available: 4,
			args: map[string]any{"structure_id": 9, "folder_id": "folder-" + marker},
			err: structureSelectionFixture(domain.ErrNotFound,
				&app.StructureFolderSelectionError{Reason: app.StructureFolderSelectionNotFound, Available: 4},
				"exact Structure folder was not found"),
			message: "selected Jira Structure folder was not found; available stored-folder count is 4",
		},
		{
			name: "ambiguous selector", kind: "check_failed",
			reason: app.StructureFolderSelectionAmbiguous, matches: 2, available: 4,
			args: map[string]any{"structure_id": 9, "folder_id": "folder-" + marker},
			err: structureSelectionFixture(domain.ErrCheckFailed,
				&app.StructureFolderSelectionError{Reason: app.StructureFolderSelectionAmbiguous, Matches: 2, Available: 4},
				"exact Structure folder selector is ambiguous: folder="+marker+" row=10, folder="+marker+" row=20"),
			message: "Jira Structure folder selector is ambiguous; matching stored-folder count is 2 and available stored-folder count is 4",
		},
		{
			name: "incomplete labels", kind: "check_failed",
			reason: app.StructureFolderSelectionLabelsIncomplete, matches: 0, available: 4,
			args: map[string]any{"structure_id": 9, "folder_path": "Plans/" + marker},
			err: structureSelectionFixture(domain.ErrCheckFailed,
				&app.StructureFolderSelectionError{Reason: app.StructureFolderSelectionLabelsIncomplete, Available: 4},
				"exact folder path cannot be validated because folder labels are incomplete; use --folder-id or --folder-row"),
			message: "Jira Structure folder path cannot be validated because folder labels are incomplete; available stored-folder count is 4",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var typed *app.StructureFolderSelectionError
			if !errors.As(test.err, &typed) || typed.Reason != test.reason ||
				typed.Matches != test.matches || typed.Available != test.available {
				t.Fatalf("test fixture must carry a typed selection error: %v", test.err)
			}
			reader := &failingStructureReader{recordingJiraReader: &recordingJiraReader{}, err: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_structure_view", Arguments: test.args,
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Remediation != "view_then_select_subtree" || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "folder-", "folder=", "row=", "Plans/", "--folder-id", "--folder-row"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("selection error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestJiraStructureOtherErrorsStayStaticAndUnremediated(t *testing.T) {
	const marker = "SYNTHETIC-STRUCTURE-PROSE-SECRET"
	for _, test := range []struct {
		name, kind, remediation, message string
		err                              error
	}{
		{
			name: "missing structure", kind: "not_found", remediation: "verify_identifier_or_access",
			message: "Jira Structure or subtree was not found",
			err:     fmt.Errorf("%w: Structure %s was not found", domain.ErrNotFound, "id-"+marker),
		},
		{
			name: "unrelated check failure", kind: "check_failed", remediation: "review_failed_check",
			message: "Jira Structure result failed validation",
			err: fmt.Errorf("%w: selected Structure folder row disappeared from the forest snapshot: %v",
				domain.ErrCheckFailed, errors.New("backend detail "+marker)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var typed *app.StructureFolderSelectionError
			if errors.As(test.err, &typed) {
				t.Fatalf("fixture must stay untyped: %#v", typed)
			}
			reader := &failingStructureReader{recordingJiraReader: &recordingJiraReader{}, err: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "jira_structure_view", Arguments: map[string]any{"structure_id": 9, "folder_id": "folder-a"},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Remediation != test.remediation || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "stored-folder count"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("generic error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestJiraStructureForestVersionMismatchIsPairOnlyAndRecoverable(t *testing.T) {
	const marker = "SYNTHETIC-STRUCTURE-VERSION-PROSE-SECRET"
	reader := &failingStructureReader{
		recordingJiraReader: &recordingJiraReader{},
		err: fmt.Errorf("wrapped %s: %w", marker, &app.StructureForestVersionMismatchError{
			Expected: domain.StructureVersion{Signature: -55, Version: 7},
			Current:  domain.StructureVersion{Signature: 66, Version: 8},
		}),
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "jira_structure_view", Arguments: map[string]any{
			"structure_id": 9, "expected_forest_signature": -55, "expected_forest_version": 7,
		},
	})
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "check_failed" ||
		got.Remediation != "reread_structure_view_then_retry_expected_forest_version" ||
		got.Message != "expected Jira Structure forest signature -55 version 7 does not match current signature 66 version 8" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(marker)) || bytes.Contains(encoded, []byte("wrapped")) {
		t.Fatalf("version error leaked backend prose: %s", encoded)
	}
}

func TestConfluenceSectionOtherErrorsStayStaticAndUnremediated(t *testing.T) {
	const marker = "SYNTHETIC-SECTION-PROSE-SECRET"
	for _, test := range []struct {
		name, kind, remediation, message string
		err                              error
	}{
		{
			name: "missing heading", kind: "not_found", remediation: "verify_identifier_or_access",
			message: "Confluence page, section, or heading was not found",
			err:     fmt.Errorf("%w: Confluence heading %q was not found", domain.ErrNotFound, "Heading "+marker),
		},
		{
			name: "structural check failure", kind: "check_failed", remediation: "review_failed_check",
			message: "Confluence page section result failed validation",
			err: fmt.Errorf("%w: page %s CSF cannot be inspected structurally: %v",
				domain.ErrCheckFailed, "PAGE-"+marker, errors.New("backend detail "+marker)),
		},
		{
			name: "configuration failure", kind: "configuration_error", remediation: "complete_configuration",
			message: "Confluence page section service is not configured",
			err:     fmt.Errorf("%w: backend %s is not configured", domain.ErrConfig, "https://backend.invalid/"+marker),
		},
		{
			name: "unexpected failure", kind: "unexpected_error", remediation: "inspect_error",
			message: "Confluence page section read failed",
			err:     errors.New("unexpected backend detail " + marker),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{sectionErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "confluence_page_section",
				Arguments: map[string]any{"reference": "42", "heading": "Heading " + marker, "expected_page_version": 3},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Remediation != test.remediation || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "PAGE-", "https://", "backend.invalid"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("page-section error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestConfluenceOutlineErrorsAreStaticAndContentFree(t *testing.T) {
	const marker = "SYNTHETIC-OUTLINE-ERROR-SECRET"
	for _, test := range []struct {
		name, kind, message string
		err                 error
	}{
		{
			name: "structural check failure", kind: "check_failed",
			message: "Confluence page outline result failed validation",
			err: fmt.Errorf("%w: page %s CSF cannot be inspected structurally: XML element <%s> is malformed",
				domain.ErrCheckFailed, "PAGE-"+marker, marker),
		},
		{
			name: "configuration failure", kind: "configuration_error",
			message: "Confluence page outline service is not configured",
			err:     fmt.Errorf("%w: backend https://backend.invalid/%s is not configured", domain.ErrConfig, marker),
		},
		{
			name: "not found", kind: "not_found",
			message: "Confluence page was not found",
			err:     fmt.Errorf("%w: page %s does not exist", domain.ErrNotFound, marker),
		},
		{
			name: "authentication failure", kind: "authentication_failed",
			message: "Confluence page outline authentication failed",
			err:     fmt.Errorf("%w: credential %s was rejected", domain.ErrAuth, marker),
		},
		{
			name: "forbidden", kind: "forbidden",
			message: "Confluence page outline access is forbidden",
			err:     fmt.Errorf("%w: page %s is restricted", domain.ErrForbidden, marker),
		},
		{
			name: "backend failure", kind: "api_error",
			message: "backend returned HTTP 500",
			err:     &httpx.APIError{Status: 500, Method: "GET", Path: "/" + marker, Body: marker},
		},
		{
			name: "transport failure", kind: "transport_error",
			message: "backend transport failed (connection-refused)",
			err:     &httpx.TransportError{Method: marker, Category: "connection-refused"},
		},
		{
			name: "unexpected failure", kind: "unexpected_error",
			message: "Confluence page outline read failed",
			err:     errors.New(marker),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{outlineErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_outline", Arguments: map[string]any{"reference": "42"},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != test.kind || got.Message != test.message {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "PAGE-", "backend.invalid", "XML element"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("outline error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

// TestConfluenceSectionRejectsNegativeExpectedPageVersionBeforeAnyBackendWork
// pins the one value the MCP surface refuses outright. A negative bound cannot
// name a revision, so it is a malformed request rather than a disabled gate, and
// the handler rejects it before a Confluence reader is ever constructed.
func TestConfluenceSectionRejectsNegativeExpectedPageVersionBeforeAnyBackendWork(t *testing.T) {
	reader := &recordingConfluenceReader{}
	constructed := 0
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) {
			constructed++
			return reader, nil
		},
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "confluence_page_section",
		Arguments: map[string]any{"reference": "42", "heading": "Overview", "expected_page_version": -3},
	})
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
		got.Kind != "usage_error" || got.Remediation != "fix_request" ||
		got.Message != "invalid Confluence page section request" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
	if constructed != 0 || reader.sectionReference != "" {
		t.Fatalf("malformed section request reached the backend: constructed=%d reference=%q", constructed, reader.sectionReference)
	}
}

// TestConfluenceSectionWithoutExpectedPageVersionReadsUngated pins the other
// half of the conditional contract: a selection that was not derived from an
// earlier read is a valid request, and the result says so instead of implying a
// binding nobody established. Omitting the field and sending zero are the same
// request, and both reach the backend ungated.
func TestConfluenceSectionWithoutExpectedPageVersionReadsUngated(t *testing.T) {
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "omitted", args: map[string]any{"reference": "42", "heading": "Overview"}},
		{name: "zero", args: map[string]any{"reference": "42", "heading": "Overview", "expected_page_version": 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result := callToolOK(t, client, "confluence_page_section", test.args)
			content, ok := result.StructuredContent.(map[string]any)
			if !ok || content["page_version_gated"] != false {
				t.Fatalf("ungated section content=%#v", result.StructuredContent)
			}
			if reader.sectionOpts.ExpectedPageVersion != 0 {
				t.Fatalf("ungated request reached the application as %+v", reader.sectionOpts)
			}
		})
	}
}

func TestConfluenceSectionVersionMismatchClassificationIsIntegerOnly(t *testing.T) {
	const marker = "SYNTHETIC-SECTION-VERSION-SECRET"
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "bare typed error", err: &app.ConfluencePageVersionMismatchError{Expected: 5, Current: 6}},
		{
			name: "wrapped in a content-bearing message",
			err: fmt.Errorf("%w: page %q at https://backend.invalid/x",
				&app.ConfluencePageVersionMismatchError{Expected: 5, Current: 6}, marker),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingConfluenceReader{sectionErr: test.err}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_section",
				Arguments: map[string]any{
					"reference": "42", "heading": "Heading " + marker, "occurrence": 2, "expected_page_version": 5,
				},
			})
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
			// The remediation names the outline deliberately: the new revision may
			// have renumbered the very occurrence this call selected, so retrying
			// the same selection against the new version is not the recovery.
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "check_failed" ||
				got.Remediation != "reread_outline_then_retry_expected_version" ||
				got.Message != "expected Confluence page version 5 does not match the current page version 6" {
				t.Fatalf("error=%+v decode=%v", got, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{marker, "https://", "backend.invalid"} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("version-mismatch error leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestConfluencePageSectionsPreservesOrderedDuplicateSelectorsInOneCall(t *testing.T) {
	reader := &recordingConfluenceReader{}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "confluence_page_sections", map[string]any{
		"reference": "42", "expected_page_version": 3, "max_bytes": 4096,
		"selectors": []any{
			map[string]any{"heading": "Results", "occurrence": 2},
			map[string]any{"heading": "Overview"},
			map[string]any{"heading": "Results", "occurrence": 2},
		},
	})
	content, ok := result.StructuredContent.(map[string]any)
	if !ok || content["requested_count"] != float64(3) || content["returned_count"] != float64(3) ||
		content["reconciled"] != true || content["complete"] != true || content["page_version_gated"] != true {
		t.Fatalf("sections=%#v", result.StructuredContent)
	}
	if reader.sectionsCalls != 1 || reader.sectionsReference != "42" ||
		reader.sectionsOpts.ExpectedPageVersion != 3 || reader.sectionsOpts.MaxBytes != 4096 {
		t.Fatalf("reader calls=%d reference=%q opts=%+v", reader.sectionsCalls, reader.sectionsReference, reader.sectionsOpts)
	}
	want := []app.ConfluencePageSectionSelector{
		{Heading: "Results", Occurrence: 2},
		{Heading: "Overview"},
		{Heading: "Results", Occurrence: 2},
	}
	if !slices.Equal(reader.sectionsOpts.Selectors, want) {
		t.Fatalf("selectors=%+v want %+v", reader.sectionsOpts.Selectors, want)
	}
}

func TestConfluencePageSectionsDefaultsAggregateBound(t *testing.T) {
	selectors, maxBytes, err := validatedConfluenceSectionsInput(ConfluenceSectionsInput{
		Reference: "42", Selectors: []ConfluenceSectionSelectorInput{{Heading: "Overview"}},
	})
	if err != nil || maxBytes != confluencePageSectionsDefaultMaxBytes ||
		!slices.Equal(selectors, []app.ConfluencePageSectionSelector{{Heading: "Overview"}}) {
		t.Fatalf("selectors=%+v max_bytes=%d err=%v", selectors, maxBytes, err)
	}
}

func TestConfluencePageSectionsRejectsMalformedSelectionBeforeReaderConstruction(t *testing.T) {
	tooMany := make([]any, confluencePageSectionsMaxSelectors+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"heading": fmt.Sprintf("Heading %d", i)}
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "blank reference", args: map[string]any{"reference": " ", "selectors": []any{map[string]any{"heading": "A"}}}},
		{name: "empty selectors", args: map[string]any{"reference": "42", "selectors": []any{}}},
		{name: "too many selectors", args: map[string]any{"reference": "42", "selectors": tooMany}},
		{name: "blank heading", args: map[string]any{"reference": "42", "selectors": []any{map[string]any{"heading": " \t"}}}},
		{name: "negative occurrence", args: map[string]any{"reference": "42", "selectors": []any{map[string]any{"heading": "A", "occurrence": -1}}}},
		{name: "negative version", args: map[string]any{"reference": "42", "expected_page_version": -1, "selectors": []any{map[string]any{"heading": "A"}}}},
		{name: "negative bytes", args: map[string]any{"reference": "42", "max_bytes": -1, "selectors": []any{map[string]any{"heading": "A"}}}},
		{name: "excess bytes", args: map[string]any{"reference": "42", "max_bytes": 1048577, "selectors": []any{map[string]any{"heading": "A"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			constructed := 0
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) {
					constructed++
					return &recordingConfluenceReader{}, nil
				},
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "confluence_page_sections", Arguments: test.args,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || constructed != 0 {
				t.Fatalf("result=%+v constructed=%d", result, constructed)
			}
		})
	}
}

func TestConfluencePageSectionsVersionMismatchUsesSectionRecovery(t *testing.T) {
	reader := &recordingConfluenceReader{sectionsErr: fmt.Errorf("wrapped private prose: %w",
		&app.ConfluencePageVersionMismatchError{Expected: 5, Current: 6})}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_page_sections", Arguments: map[string]any{
			"reference": "42", "expected_page_version": 5,
			"selectors": []any{map[string]any{"heading": "Overview"}},
		},
	})
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" ||
		got.Remediation != "reread_outline_then_retry_expected_version" ||
		got.Message != "expected Confluence page version 5 does not match the current page version 6" {
		t.Fatalf("error=%+v decode=%v", got, err)
	}
}

func TestConfluencePageSectionsValidatorFailsClosed(t *testing.T) {
	input := ConfluenceSectionsInput{
		ExpectedPageVersion: 3, MaxBytes: 64,
		Selectors: []ConfluenceSectionSelectorInput{{Heading: "First"}, {Heading: "Second", Occurrence: 2}},
	}
	valid := func() *app.ConfluencePageSectionsResult {
		return &app.ConfluencePageSectionsResult{
			SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
			RequestedCount: 2, ReturnedCount: 2, Reconciled: true, Complete: true, MaxBytes: 64,
			OriginalBytes: 6, EmittedBytes: 6,
			Sections: []app.ConfluencePageSectionEntry{
				{Heading: "First", Level: 1, Path: []string{"First"}, Occurrence: 1, Markdown: "one", Complete: true, OriginalBytes: 3, EmittedBytes: 3},
				{Heading: "Second", Level: 2, Path: []string{"Root", "Second"}, Occurrence: 2, Markdown: "two", Complete: true, OriginalBytes: 3, EmittedBytes: 3},
			},
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*app.ConfluencePageSectionsResult)
	}{
		{name: "nil result"},
		{name: "schema", mutate: func(r *app.ConfluencePageSectionsResult) { r.SchemaVersion++ }},
		{name: "identity", mutate: func(r *app.ConfluencePageSectionsResult) { r.ID = " " }},
		{name: "identity invalid utf8", mutate: func(r *app.ConfluencePageSectionsResult) { r.ID = "\xff" }},
		{name: "page title invalid utf8", mutate: func(r *app.ConfluencePageSectionsResult) { r.PageTitle = "\xff" }},
		{name: "space invalid utf8", mutate: func(r *app.ConfluencePageSectionsResult) { r.Space = "\xff" }},
		{name: "version", mutate: func(r *app.ConfluencePageSectionsResult) { r.Version = 0 }},
		{name: "gate", mutate: func(r *app.ConfluencePageSectionsResult) { r.PageVersionGated = false }},
		{name: "requested count", mutate: func(r *app.ConfluencePageSectionsResult) { r.RequestedCount++ }},
		{name: "returned count", mutate: func(r *app.ConfluencePageSectionsResult) { r.ReturnedCount-- }},
		{name: "unreconciled", mutate: func(r *app.ConfluencePageSectionsResult) { r.Reconciled = false }},
		{name: "wrong bound", mutate: func(r *app.ConfluencePageSectionsResult) { r.MaxBytes++ }},
		{name: "wrong order", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Heading = "Second"
			r.Sections[0].Path = []string{"Second"}
		}},
		{name: "path", mutate: func(r *app.ConfluencePageSectionsResult) { r.Sections[0].Path = []string{"Other"} }},
		{name: "blank ancestor", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[1].Path = []string{" ", "Second"}
		}},
		{name: "invalid utf8 ancestor", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[1].Path = []string{"\xff", "Second"}
		}},
		{name: "path deeper than heading level", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Path = []string{"Root", "First"}
		}},
		{name: "occurrence", mutate: func(r *app.ConfluencePageSectionsResult) { r.Sections[1].Occurrence = 1 }},
		{name: "invalid utf8", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Markdown = "\xff\xfe"
			r.Sections[0].OriginalBytes = 2
			r.Sections[0].EmittedBytes = 2
			r.OriginalBytes = 5
			r.EmittedBytes = 5
		}},
		{name: "unknown partial reason", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Complete = false
			r.Sections[0].Truncated = true
			r.Sections[0].PartialReason = "heading_limit"
			r.Sections[0].OriginalBytes = 4
			r.OriginalBytes = 7
			r.Complete = false
			r.Truncated = true
		}},
		{name: "section bytes", mutate: func(r *app.ConfluencePageSectionsResult) { r.Sections[0].EmittedBytes++ }},
		{name: "aggregate bytes", mutate: func(r *app.ConfluencePageSectionsResult) { r.EmittedBytes++ }},
		{name: "impossible allocator share", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Markdown = strings.Repeat("a", 40)
			r.Sections[0].OriginalBytes = 40
			r.Sections[0].EmittedBytes = 40
			r.Sections[1].Markdown = strings.Repeat("b", 24)
			r.Sections[1].OriginalBytes = 24
			r.Sections[1].EmittedBytes = 24
			r.OriginalBytes = 64
			r.EmittedBytes = 64
		}},
		{name: "impossible max bytes partial", mutate: func(r *app.ConfluencePageSectionsResult) {
			r.Sections[0].Markdown = ""
			r.Sections[0].Complete = false
			r.Sections[0].Truncated = true
			r.Sections[0].PartialReason = "max_bytes"
			r.Sections[0].OriginalBytes = 10
			r.Sections[0].EmittedBytes = 0
			r.OriginalBytes = 13
			r.EmittedBytes = 3
			r.Complete = false
			r.Truncated = true
		}},
		{name: "aggregate complete", mutate: func(r *app.ConfluencePageSectionsResult) { r.Complete = false }},
		{name: "aggregate truncated", mutate: func(r *app.ConfluencePageSectionsResult) { r.Truncated = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var candidate *app.ConfluencePageSectionsResult
			if test.mutate != nil {
				candidate = valid()
				test.mutate(candidate)
			}
			if err := validateConfluenceSectionsResult(candidate, input, 64); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v want check failed", err)
			}
		})
	}
	if err := validateConfluenceSectionsResult(valid(), input, 64); err != nil {
		t.Fatalf("valid result: %v", err)
	}
}

func TestConfluencePageSectionsValidatorIsWiredIntoTool(t *testing.T) {
	reader := &recordingConfluenceReader{sectionsResult: &app.ConfluencePageSectionsResult{
		SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
		RequestedCount: 2, ReturnedCount: 1, Reconciled: true, Complete: true,
		MaxBytes: confluencePageSectionsDefaultMaxBytes,
		Sections: []app.ConfluencePageSectionEntry{{
			Heading: "Overview", Level: 2, Path: []string{"Overview"}, Occurrence: 1, Complete: true,
		}},
	}}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_page_sections", Arguments: map[string]any{
			"reference": "42", "expected_page_version": 3,
			"selectors": []any{map[string]any{"heading": "Overview"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil || reader.sectionsCalls != 1 {
		t.Fatalf("result=%+v calls=%d", result, reader.sectionsCalls)
	}
}

func TestConfluencePageSectionsEncodedMetadataHasIndependentBound(t *testing.T) {
	out := &app.ConfluencePageSectionsResult{
		SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
		RequestedCount: 1, ReturnedCount: 1, Reconciled: true, Complete: true, MaxBytes: 1,
		Sections: []app.ConfluencePageSectionEntry{{
			Heading: "Overview", Level: 2, Path: []string{strings.Repeat("x", confluencePageSectionsMaxMaxBytes+confluencePageSectionsResultOverhead), "Overview"},
			Occurrence: 1, Complete: true,
		}},
	}
	input := ConfluenceSectionsInput{Selectors: []ConfluenceSectionSelectorInput{{Heading: "Overview"}}, MaxBytes: 1}
	if err := validateConfluenceSectionsResult(out, input, 1); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v want pre-encoding check failure", err)
	}
}

func TestConfluenceStructuralResultValidatorsFailClosed(t *testing.T) {
	outline := func(mutate func(*app.ConfluencePageOutlineResult)) *app.ConfluencePageOutlineResult {
		result := &app.ConfluencePageOutlineResult{
			SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
			Count: 1, Total: 1, Complete: true, OriginalBytes: 64, EmittedBytes: 64,
			Headings: []app.ConfluenceOutlineEntry{{Index: 1, Level: 1, Title: "Overview", Path: []string{"Overview"}, Occurrence: 1}},
		}
		mutate(result)
		return result
	}
	section := func(mutate func(*app.ConfluencePageSectionResult)) *app.ConfluencePageSectionResult {
		result := &app.ConfluencePageSectionResult{
			SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
			Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
			Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
		}
		mutate(result)
		return result
	}
	// A page with no headings at all is legitimate evidence of absence and must
	// survive validation; only self-inconsistent shapes are refused.
	if err := validateConfluenceOutlineResult(outline(func(r *app.ConfluencePageOutlineResult) {
		r.Count, r.Total, r.OriginalBytes, r.EmittedBytes = 0, 0, 0, 0
		r.Headings = []app.ConfluenceOutlineEntry{}
	})); err != nil {
		t.Fatalf("empty outline rejected: %v", err)
	}
	if err := validateConfluenceOutlineResult(outline(func(*app.ConfluencePageOutlineResult) {})); err != nil {
		t.Fatalf("reconciled outline rejected: %v", err)
	}
	boundSectionInput := ConfluenceSectionInput{
		Heading: "Overview", Occurrence: 1, ExpectedPageVersion: 3,
	}
	if err := validateConfluenceSectionResult(section(func(*app.ConfluencePageSectionResult) {}), boundSectionInput); err != nil {
		t.Fatalf("reconciled section rejected: %v", err)
	}
	// The gate is conditional, so the validator has to accept both honest states
	// and reject only the contradictory ones. An ungated read is legitimate
	// evidence about whatever revision it was served from, so any positive
	// returned version is accepted when no version was requested.
	for _, version := range []int{1, 3, 97} {
		if err := validateConfluenceSectionResult(section(func(r *app.ConfluencePageSectionResult) {
			r.PageVersionGated, r.Version = false, version
		}), ConfluenceSectionInput{Heading: "Overview", Occurrence: 1}); err != nil {
			t.Fatalf("honest ungated section at version %d rejected: %v", version, err)
		}
	}
	for _, test := range []struct {
		name  string
		input ConfluenceSectionInput
	}{
		{
			name:  "returned heading differs from request",
			input: ConfluenceSectionInput{Heading: "Other", Occurrence: 1, ExpectedPageVersion: 3},
		},
		{
			name:  "returned occurrence differs from request",
			input: ConfluenceSectionInput{Heading: "Overview", Occurrence: 2, ExpectedPageVersion: 3},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateConfluenceSectionResult(
				section(func(*app.ConfluencePageSectionResult) {}), test.input,
			); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v want check failed", err)
			}
		})
	}
	if err := validateConfluenceSectionResult(
		section(func(r *app.ConfluencePageSectionResult) { r.Occurrence = 2 }),
		ConfluenceSectionInput{Heading: "Overview", ExpectedPageVersion: 3},
	); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("omitted occurrence accepted occurrence 2: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*app.ConfluencePageOutlineResult)
	}{
		{name: "nil result", mutate: nil},
		{name: "unknown schema", mutate: func(r *app.ConfluencePageOutlineResult) { r.SchemaVersion = 2 }},
		{name: "empty id", mutate: func(r *app.ConfluencePageOutlineResult) { r.ID = "  " }},
		{name: "no version", mutate: func(r *app.ConfluencePageOutlineResult) { r.Version = 0 }},
		{name: "absent headings", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings = nil }},
		{name: "count disagrees with length", mutate: func(r *app.ConfluencePageOutlineResult) { r.Count = 2 }},
		{name: "total below count", mutate: func(r *app.ConfluencePageOutlineResult) { r.Total = 0 }},
		{name: "emitted exceeds original", mutate: func(r *app.ConfluencePageOutlineResult) { r.EmittedBytes = 65 }},
		{name: "nonsequential heading index", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Index = 2 }},
		{name: "impossible heading level", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Level = 7 }},
		{name: "empty heading title", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Title = " " }},
		{name: "absent heading path", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Path = nil }},
		{name: "path does not end in heading", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Path = []string{"Other"} }},
		{name: "zero heading occurrence", mutate: func(r *app.ConfluencePageOutlineResult) { r.Headings[0].Occurrence = 0 }},
		{name: "complete with a reason", mutate: func(r *app.ConfluencePageOutlineResult) { r.PartialReason = "byte_limit" }},
		{
			name: "partial without a reason",
			mutate: func(r *app.ConfluencePageOutlineResult) {
				r.Complete, r.Truncated, r.Total = false, true, 2
			},
		},
		{
			name:   "truncated flag contradicts completeness",
			mutate: func(r *app.ConfluencePageOutlineResult) { r.Truncated = true },
		},
		{
			name: "unknown partial reason",
			mutate: func(r *app.ConfluencePageOutlineResult) {
				r.Complete, r.Truncated, r.PartialReason, r.Total = false, true, "max_bytes", 2
			},
		},
		{
			name:   "complete but withheld a heading",
			mutate: func(r *app.ConfluencePageOutlineResult) { r.Total = 2 },
		},
	} {
		t.Run("outline "+test.name, func(t *testing.T) {
			var candidate *app.ConfluencePageOutlineResult
			if test.mutate != nil {
				candidate = outline(test.mutate)
			}
			if err := validateConfluenceOutlineResult(candidate); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v want check failed", err)
			}
		})
	}

	for _, test := range []struct {
		name     string
		expected int
		mutate   func(*app.ConfluencePageSectionResult)
	}{
		{name: "nil result", expected: 3},
		{name: "unknown schema", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.SchemaVersion = 2 }},
		{name: "empty id", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.ID = "  " }},
		{name: "no version", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Version = 0 }},
		// Every contradictory combination of the requested gate and the claimed
		// one. The honest pairs — bound/gated at the same version, and
		// unbound/ungated — are asserted to pass above.
		{name: "bound request answered ungated", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.PageVersionGated = false }},
		{name: "bound request gated at another version", expected: 4, mutate: func(*app.ConfluencePageSectionResult) {}},
		{name: "unbound request answered with a gate claim", expected: 0, mutate: func(*app.ConfluencePageSectionResult) {}},
		{
			name: "impossible negative requirement claimed as gated", expected: -1,
			mutate: func(*app.ConfluencePageSectionResult) {},
		},
		{
			name: "impossible negative requirement reported ungated", expected: -1,
			mutate: func(r *app.ConfluencePageSectionResult) { r.PageVersionGated = false },
		},
		{name: "absent path", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Path = nil }},
		{name: "empty heading", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Heading = " " }},
		{name: "path does not end in heading", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Path = []string{"Other"} }},
		{name: "impossible level", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Level = 7 }},
		{name: "zero occurrence", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.Occurrence = 0 }},
		{name: "complete with a reason", expected: 3, mutate: func(r *app.ConfluencePageSectionResult) { r.PartialReason = "max_bytes" }},
		{
			name: "partial without a reason", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) {
				r.Complete, r.Truncated, r.OriginalBytes = false, true, 40
			},
		},
		{
			name: "unknown partial reason", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) {
				r.Complete, r.Truncated, r.PartialReason, r.OriginalBytes = false, true, "heading_limit", 40
			},
		},
		{
			name: "emitted bytes disagree with the body", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) { r.EmittedBytes = 12 },
		},
		{
			name: "original bytes below emitted bytes", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) {
				r.Complete, r.Truncated, r.PartialReason, r.OriginalBytes = false, true, "max_bytes", 10
			},
		},
		{
			name: "invalid utf-8 body", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) {
				r.Markdown, r.OriginalBytes, r.EmittedBytes = "\xff\xfe", 2, 2
			},
		},
		{
			name: "complete but withheld bytes", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) { r.OriginalBytes = 40 },
		},
		{
			name: "partial that withheld nothing", expected: 3,
			mutate: func(r *app.ConfluencePageSectionResult) {
				r.Complete, r.Truncated, r.PartialReason = false, true, "max_bytes"
			},
		},
	} {
		t.Run("section "+test.name, func(t *testing.T) {
			var candidate *app.ConfluencePageSectionResult
			if test.mutate != nil {
				candidate = section(test.mutate)
			}
			input := ConfluenceSectionInput{
				Heading: "Overview", Occurrence: 1, ExpectedPageVersion: test.expected,
			}
			if err := validateConfluenceSectionResult(candidate, input); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v want check failed", err)
			}
		})
	}
}

// TestConfluenceStructuralValidatorsAreWiredIntoBothTools proves the validators
// are not dead code: a reader that returns an unreconciled result fails the call
// instead of handing the client evidence it cannot trust.
func TestConfluenceStructuralValidatorsAreWiredIntoBothTools(t *testing.T) {
	for _, test := range []struct {
		name, tool string
		reader     *recordingConfluenceReader
		args       map[string]any
	}{
		{
			name: "outline", tool: "confluence_page_outline",
			args: map[string]any{"reference": "42"},
			reader: &recordingConfluenceReader{outlineResult: &app.ConfluencePageOutlineResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
				Count: 1, Total: 1, Complete: true, Headings: nil,
			}},
		},
		{
			name: "bound section answered ungated", tool: "confluence_page_section",
			args: map[string]any{"reference": "42", "heading": "Overview", "expected_page_version": 3},
			reader: &recordingConfluenceReader{sectionResult: &app.ConfluencePageSectionResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: false,
				Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
				Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
			}},
		},
		{
			// The conditional gate cuts both ways: an unbound request that comes
			// back claiming a binding would hand the client authority it never
			// asked for, which is exactly what page_version_gated must not mean.
			name: "unbound section answered gated", tool: "confluence_page_section",
			args: map[string]any{"reference": "42", "heading": "Overview"},
			reader: &recordingConfluenceReader{sectionResult: &app.ConfluencePageSectionResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
				Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
				Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
			}},
		},
		{
			name: "section answered for another occurrence", tool: "confluence_page_section",
			args: map[string]any{
				"reference": "42", "heading": "Overview", "occurrence": 2, "expected_page_version": 3,
			},
			reader: &recordingConfluenceReader{sectionResult: &app.ConfluencePageSectionResult{
				SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3, PageVersionGated: true,
				Heading: "Overview", Level: 1, Occurrence: 1, Path: []string{"Overview"},
				Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return test.reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
				t.Fatalf("unreconciled %s result was returned: %+v", test.tool, result)
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

func TestConfluenceTableGenericNotFoundStaysUnchanged(t *testing.T) {
	notFound := fmt.Errorf("%w: page SYNTHETIC-MISSING-PAGE-SECRET", domain.ErrNotFound)
	for _, tool := range []string{"confluence_table_summary", "confluence_table_extract"} {
		t.Run(tool, func(t *testing.T) {
			reader := &recordingConfluenceReader{tableErr: notFound}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: tool, Arguments: map[string]any{"reference": "42", "table": 7},
			})
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
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
				got.Kind != "not_found" || got.Remediation != "verify_identifier_or_access" ||
				got.Message != "Confluence page or table was not found" {
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
	graphKey                            string
	graphOpts                           app.JiraIssueGraphOptions
	graphResult                         *app.JiraIssueGraphResult
	graphErr                            error
	searchJQL, searchView, searchCursor string
	searchColumns                       []string
	searchLimit                         int
	historyKey                          string
	historyOpts                         app.JiraHistoryOpts
	historyErr                          error
	refsOpts                            app.JiraIssueRefsOpts
	refsResult                          *app.JiraIssueRefsResult
	refsErr                             error
	digestKey                           string
	digestOpts                          app.JiraEpicDigestOpts
	boardID                             int
	boardOpts                           app.BoardSnapshotOpts
	structureID, structureViewID        int64
	structureOpts                       app.StructureSnapshotOpts
	structureText                       string
	structureName                       string
}

type invalidStructureReader struct {
	*recordingJiraReader
	mode string
}

type failingStructureReader struct {
	*recordingJiraReader
	err error
}

func (r *failingStructureReader) StructureSnapshot(_ context.Context, _ int64, _ app.StructureSnapshotOpts) (*app.StructureSnapshot, error) {
	return nil, r.err
}

type oversizedJiraReader struct {
	*recordingJiraReader
	payload string
}

func (r *oversizedJiraReader) FieldCatalog(_ context.Context, opts app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	if opts.SummaryOnly {
		return &app.JiraFieldCatalogResult{
			SchemaVersion: 1, Projection: "summary", Source: "test", Complete: true,
			Total: 1, Count: 1, CustomCount: 1, Fields: []domain.FieldDef{},
		}, nil
	}
	return &app.JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: "full", Source: "test", Complete: true,
		Fields: []domain.FieldDef{{ID: "customfield_1", Name: r.payload, Custom: true}},
	}, nil
}

func (r *oversizedJiraReader) SearchIssueListView(_ context.Context, _ string, _ []string, _ string, _ int, _ string) (*app.IssueList, error) {
	return &app.IssueList{
		SchemaVersion: 1,
		Source:        app.IssueListSource{Kind: "jql"},
		Selection:     map[string]any{},
		Projection:    app.IssueListProjection{Columns: []string{"key", "summary"}, Fields: []string{"summary"}, Ordering: "backend"},
		Rows:          []app.IssueListRow{{Key: "PROJ-1", Position: 1, Values: map[string]any{"summary": r.payload}}},
		Page:          app.IssueListPage{Count: 1, Complete: true},
	}, nil
}

func (r *oversizedJiraReader) HistoryFiltered(_ context.Context, key string, opts app.JiraHistoryOpts) (*app.JiraHistoryResult, error) {
	r.historyKey, r.historyOpts = key, opts
	return &app.JiraHistoryResult{
		Key: key, Complete: true, Source: "paginated", Total: 1, Fetched: 1, Count: 1,
		History: []domain.ChangelogEntry{{ID: "101", Items: []domain.ChangelogItem{{Field: "Delivery Notes", To: historyRawRowMarker}}}},
		Summary: app.JiraHistorySummary{HistoryCount: 1, Fields: []app.JiraHistoryFieldSummary{}},
		LastChanges: []app.JiraFieldLastChange{{
			FieldID: "customfield_10001", Field: "Delivery Notes", Created: "2026-03-08T10:00:00Z",
			HistoryID: "101", To: r.payload,
		}},
	}, nil
}

func (r *oversizedJiraReader) IssueRefs(_ context.Context, opts app.JiraIssueRefsOpts) (*app.JiraIssueRefsResult, error) {
	return validJiraIssueRefsResult(opts), nil
}

func (r *oversizedJiraReader) EpicDigest(_ context.Context, _ string, _ app.JiraEpicDigestOpts) (*app.JiraEpicDigestResult, error) {
	return &app.JiraEpicDigestResult{
		SchemaVersion: 1, Includes: []string{"identity"},
		Sources: map[string]app.JiraDigestSource{"identity": {Complete: true, Count: 1}},
		Epic:    app.JiraDigestIdentity{Key: "PROJ-1", Summary: r.payload},
	}, nil
}

func (r *oversizedJiraReader) BoardSnapshot(_ context.Context, _ int, _ app.BoardSnapshotOpts) (*app.BoardSnapshot, error) {
	return &app.BoardSnapshot{
		SchemaVersion: 1, Board: &domain.BoardConfiguration{Columns: []domain.BoardColumn{}},
		Projection: app.BoardProjection{Columns: []string{"key", "summary"}, Fields: []string{"summary"}, Ordering: "backend"},
		Rows:       []app.BoardSnapshotRow{{Key: "PROJ-1", Position: 1, Values: map[string]any{"summary": r.payload}}},
		RowCount:   1, Complete: true,
	}, nil
}

func (r *recordingJiraReader) IssueFieldEvidence(_ context.Context, key string, opts app.JiraIssueFieldEvidenceOpts) (*app.JiraIssueFieldEvidenceResult, error) {
	r.fieldEvidenceKey, r.fieldEvidenceOpts = key, opts
	return &app.JiraIssueFieldEvidenceResult{
		SchemaVersion: 1, Issue: app.JiraIssueFieldEvidenceIssue{Key: key, Updated: "2026-01-01T00:00:00Z"},
		Field:      app.JiraIssueFieldEvidenceField{ID: "customfield_1", Name: "Delivery Notes", Present: true, ValueType: "string"},
		Projection: "compact", MaxValueBytes: opts.MaxBytes, OriginalValueBytes: 7, EmittedValueBytes: 7, Complete: true, Value: "value",
	}, nil
}

func (r *recordingJiraReader) IssueGraphWithOptions(_ context.Context, key string, opts app.JiraIssueGraphOptions) (*app.JiraIssueGraphResult, error) {
	r.graphKey, r.graphOpts = key, opts
	return r.graphResult, r.graphErr
}

func (r *recordingJiraReader) FieldCatalog(_ context.Context, opts app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	r.fieldOpts = opts
	return &app.JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: "full", Source: "test", Complete: true, Fields: []domain.FieldDef{},
	}, nil
}

// historyRawRowMarker only ever appears inside the raw History array the
// summary projection must drop, so any test that finds it in an MCP payload has
// found a raw-changelog leak.
const historyRawRowMarker = "RAW-HISTORY-ROW-MARKER"

func (r *recordingJiraReader) HistoryFiltered(_ context.Context, key string, opts app.JiraHistoryOpts) (*app.JiraHistoryResult, error) {
	r.historyKey, r.historyOpts = key, opts
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	result := &app.JiraHistoryResult{
		Key: key, Complete: true, Source: "paginated", Total: 2, Fetched: 2, Count: 1,
		Filters: app.JiraHistoryFilters{Since: opts.Since, Until: opts.Until},
		History: []domain.ChangelogEntry{{
			ID: "101", Author: "synthetic", Created: "2026-03-08T10:00:00Z",
			Items: []domain.ChangelogItem{{Field: "Delivery Notes", From: historyRawRowMarker, To: historyRawRowMarker}},
		}},
		Summary: app.JiraHistorySummary{
			HistoryCount: 1, HistoryIDNonemptyCount: 1, HistoryIDsUnique: true, HistoryNonemptyIDsUnique: true,
			CountMatchesHistory: true, Fields: []app.JiraHistoryFieldSummary{},
		},
	}
	if len(opts.Fields) > 0 {
		result.LastChanges = []app.JiraFieldLastChange{{
			FieldID: "customfield_10001", Field: "Delivery Notes",
			Created: "2026-03-08T10:00:00Z", HistoryID: "101", To: "current",
		}}
	}
	return result, nil
}

func (r *recordingJiraReader) IssueRefs(_ context.Context, opts app.JiraIssueRefsOpts) (*app.JiraIssueRefsResult, error) {
	r.refsOpts = opts
	if r.refsErr != nil {
		return nil, r.refsErr
	}
	if r.refsResult != nil {
		return r.refsResult, nil
	}
	return validJiraIssueRefsResult(opts), nil
}

func validJiraIssueRefsResult(opts app.JiraIssueRefsOpts) *app.JiraIssueRefsResult {
	selection := app.JiraIssueRefsSelection{Mode: "jql", Limit: opts.Limit, Complete: true}
	key := "PROJ-1"
	if opts.Key != "" {
		key = opts.Key
		selection = app.JiraIssueRefsSelection{Mode: "key", Count: 1, Complete: true}
	} else {
		selection.Count = 1
	}
	sources := map[string]app.JiraIssueRefsSource{
		"comments":    {Complete: true, Count: 0},
		"description": {Complete: true, Count: 0},
	}
	issues := []app.JiraIssueRefs{{
		Key: key, Summary: "narrative must not cross MCP", Type: "Story", Complete: true, Sources: sources,
		ReferenceSummary: app.JiraIssueReferenceSummary{
			ReferenceKindCounts: map[string]int{}, SourceCount: 2,
			SourceValueCounts:   map[string]int{"comments": 0, "description": 0},
			CompleteSourceCount: 2, ReferenceCountMatchesKinds: true,
			CompleteMatchesSources: true, TruncatedMatchesSources: true,
		},
		Refs: []app.PlanningRef{{URL: "https://private.invalid/must-not-cross", Kind: "link"}},
	}}
	result := &app.JiraIssueRefsResult{
		Key: opts.Key, JQL: opts.JQL, Count: len(issues), Complete: true,
		Selection: selection, Issues: issues,
	}
	result.Summary = app.JiraIssueRefsSummary{
		IssueCount: len(issues), CompleteIssueCount: len(issues),
		ReferenceKindCounts: map[string]int{}, SourceCount: len(issues) * 2,
		SourceValueCounts:   map[string]int{"comments": 0, "description": 0},
		CompleteSourceCount: len(issues) * 2,
		CountMatchesIssues:  true, SelectionCountMatchesIssues: true,
		ReferenceCountMatchesKinds: true, IssueSummariesReconciled: true,
		CompleteMatchesInputs: true, TruncatedMatchesInputs: true,
	}
	return result
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

func (r *recordingJiraReader) Structure(_ context.Context, id int64) (*domain.Structure, error) {
	r.structureID = id
	name := r.structureName
	if name == "" {
		name = "Synthetic Structure"
	}
	return &domain.Structure{ID: id, Name: name, Owner: map[string]any{"private": "must-not-project"}}, nil
}

func (r *recordingJiraReader) StructureSnapshot(_ context.Context, id int64, opts app.StructureSnapshotOpts) (*app.StructureSnapshot, error) {
	r.structureViewID, r.structureOpts = id, opts
	issueValues := make(map[string]any, len(opts.Attributes))
	for _, field := range opts.Attributes {
		issueValues[field] = nil
	}
	if r.structureText != "" {
		issueValues[opts.Attributes[0]] = r.structureText
	}
	var selection *app.StructureSelection
	switch {
	case opts.FolderID != "":
		selection = &app.StructureSelection{Kind: "folder-id", FolderID: opts.FolderID, RowID: 10, Path: []string{"Synthetic"}}
	case opts.FolderRow != 0:
		selection = &app.StructureSelection{Kind: "folder-row", FolderID: "folder-a", RowID: opts.FolderRow, Path: []string{"Synthetic"}}
	case opts.FolderPath != "":
		selection = &app.StructureSelection{Kind: "folder-path", FolderID: "folder-a", RowID: 10, Path: strings.Split(opts.FolderPath, "/")}
	}
	rows := []app.StructureSnapshotRow{{RowID: 10, ItemType: "issue", ItemID: "10001", Accessible: true, Values: issueValues}}
	if selection != nil {
		folderValues := make(map[string]any, len(opts.Attributes))
		for _, field := range opts.Attributes {
			folderValues[field] = nil
		}
		zero, one := 0, 1
		rows = []app.StructureSnapshotRow{
			{RowID: selection.RowID, ItemType: "folder", ItemID: selection.FolderID, Accessible: true, RelativeDepth: &zero, Values: folderValues},
			{RowID: selection.RowID + 1, Depth: 1, ParentRowID: selection.RowID, ItemType: "issue", ItemID: "10001", Accessible: true, RelativeDepth: &one, Values: issueValues},
		}
	}
	forestVersion := domain.StructureVersion{Signature: 55, Version: 7}
	if opts.ExpectedForestVersion != nil {
		forestVersion = *opts.ExpectedForestVersion
	}
	return &app.StructureSnapshot{
		SchemaVersion: 1, Structure: app.StructureSnapshotMetadata{ID: id, Name: "Synthetic Structure"},
		ForestVersion:      forestVersion,
		ForestVersionGated: opts.ExpectedForestVersion != nil,
		Projection:         app.StructureProjection{Kind: "jira-fields-v1", Source: "explicit", Attributes: append([]string(nil), opts.Attributes...)},
		Rows:               rows,
		RowCount:           len(rows), IssueCount: 1, Complete: true, InaccessibleRows: []int64{}, Selection: selection, Warnings: []string{},
	}, nil
}

func (r *invalidStructureReader) StructureSnapshot(ctx context.Context, id int64, opts app.StructureSnapshotOpts) (*app.StructureSnapshot, error) {
	result, err := r.recordingJiraReader.StructureSnapshot(ctx, id, opts)
	switch r.mode {
	case "row-count":
		result.RowCount++
	case "selection":
		result.Selection = nil
	case "wrong-root":
		result.Rows[0].ItemID = "another-folder"
	case "second-root":
		zero := 0
		result.Rows[1].RelativeDepth = &zero
	case "wrong-path":
		result.Selection.Path = []string{"Another", "Path"}
	case "projection":
		delete(result.Rows[0].Values, opts.Attributes[0])
	case "completeness":
		result.Complete = false
	case "forest-version":
		result.ForestVersion = domain.StructureVersion{}
	case "forest-gated":
		result.ForestVersionGated = !result.ForestVersionGated
	case "wrong-expected-forest":
		result.ForestVersion.Signature++
	}
	return result, err
}

type recordingConfluenceReader struct {
	searchCQL, searchCursor                      string
	searchLimit                                  int
	searchText                                   string
	resolveReference, metadataReference          string
	outlineReference, sectionReference           string
	metadataResult                               *app.ConfluencePageMetadataResult
	metadataResultSet                            bool
	metadataErr                                  error
	metadataCalls                                int
	sectionOpts                                  app.ConfluencePageSectionOpts
	sectionsReference                            string
	sectionsOpts                                 app.ConfluencePageSectionsOpts
	sectionsCalls                                int
	tableSummaryReference, tableExtractReference string
	tableSummaryIndex, tableExtractIndex         int
	tableSummaryOpts, tableExtractOpts           app.ConfluenceTableReadOpts
	tableText                                    string
	tableErr                                     error
	sectionErr                                   error
	sectionsErr                                  error
	outlineErr                                   error
	outlineResult                                *app.ConfluencePageOutlineResult
	sectionResult                                *app.ConfluencePageSectionResult
	sectionsResult                               *app.ConfluencePageSectionsResult
	attachmentReference                          string
	attachmentOpts                               app.ConfluenceAttachmentInventoryOpts
	attachmentResult                             *app.ConfluenceAttachmentInventoryResult
	attachmentErr                                error
	attachmentCalls                              int
}

// partialOriginalBytes reports the configured original byte bound of whichever
// bounded page read the stub is standing in for.
func (r *recordingConfluenceReader) partialOriginalBytes() int {
	if r.outlineResult != nil {
		return r.outlineResult.OriginalBytes
	}
	if r.sectionResult != nil {
		return r.sectionResult.OriginalBytes
	}
	return 0
}

type invalidConfluenceTableReader struct {
	*recordingConfluenceReader
	mode string
}

func (r *recordingConfluenceReader) SearchQualified(_ context.Context, cql string, limit int, cursor string) (*app.ConfluenceSearchResult, error) {
	r.searchCQL, r.searchLimit, r.searchCursor = cql, limit, cursor
	results := []domain.PageRef{}
	if r.searchText != "" {
		results = append(results, domain.PageRef{ID: "42", Title: r.searchText, Excerpt: r.searchText})
	}
	return &app.ConfluenceSearchResult{
		SchemaVersion: 1, Query: cql, Results: results, Count: len(results), Complete: true,
	}, nil
}

func (r *recordingConfluenceReader) ResolvePageReference(_ context.Context, reference string) (*app.ConfluencePageResolution, error) {
	r.resolveReference = reference
	return &app.ConfluencePageResolution{}, nil
}

func (r *recordingConfluenceReader) PageMetadata(_ context.Context, reference string) (*app.ConfluencePageMetadataResult, error) {
	r.metadataReference = reference
	r.metadataCalls++
	if r.metadataErr != nil {
		return nil, r.metadataErr
	}
	if r.metadataResultSet || r.metadataResult != nil {
		return r.metadataResult, nil
	}
	return &app.ConfluencePageMetadataResult{
		SchemaVersion: app.ConfluencePageMetadataSchemaVersion,
		ID:            "42", Title: "Synthetic page", Space: "DOCS", Version: 3,
		RestrictionState: app.ConfluenceRestrictionUnknown,
	}, nil
}

func (r *recordingConfluenceReader) PageOutline(_ context.Context, reference string) (*app.ConfluencePageOutlineResult, error) {
	r.outlineReference = reference
	if r.outlineErr != nil {
		return nil, r.outlineErr
	}
	if r.outlineResult != nil {
		return r.outlineResult, nil
	}
	// The default stands in for a legitimately heading-free page: reconciled
	// provenance and completeness, and an empty — not absent — heading slice.
	return &app.ConfluencePageOutlineResult{
		SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: 3,
		Complete: true, Headings: []app.ConfluenceOutlineEntry{},
	}, nil
}

func (r *recordingConfluenceReader) PageSection(_ context.Context, reference string, opts app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error) {
	r.sectionReference, r.sectionOpts = reference, opts
	if r.sectionErr != nil {
		return nil, r.sectionErr
	}
	if r.sectionResult != nil {
		return r.sectionResult, nil
	}
	occurrence := opts.Occurrence
	if occurrence == 0 {
		occurrence = 1
	}
	// The stub echoes the requested gate the way the service does, so the
	// transport-side validator is exercised against a reconciled result. An
	// ungated read still reports the revision it was served from — that is the
	// difference the gate flag records.
	version := opts.ExpectedPageVersion
	if version == 0 {
		version = 3
	}
	return &app.ConfluencePageSectionResult{
		SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42",
		Version: version, PageVersionGated: opts.ExpectedPageVersion > 0,
		Heading: opts.Heading, Level: 2, Path: []string{opts.Heading}, Occurrence: occurrence,
		Complete: true,
	}, nil
}

func (r *recordingConfluenceReader) PageSections(_ context.Context, reference string, opts app.ConfluencePageSectionsOpts) (*app.ConfluencePageSectionsResult, error) {
	r.sectionsReference, r.sectionsOpts = reference, opts
	r.sectionsCalls++
	if r.sectionsErr != nil {
		return nil, r.sectionsErr
	}
	if r.sectionsResult != nil {
		return r.sectionsResult, nil
	}
	version := opts.ExpectedPageVersion
	if version == 0 {
		version = 3
	}
	sections := make([]app.ConfluencePageSectionEntry, 0, len(opts.Selectors))
	for _, selector := range opts.Selectors {
		occurrence := selector.Occurrence
		if occurrence == 0 {
			occurrence = 1
		}
		sections = append(sections, app.ConfluencePageSectionEntry{
			Heading: selector.Heading, Level: 2, Path: []string{selector.Heading}, Occurrence: occurrence,
			Complete: true,
		})
	}
	return &app.ConfluencePageSectionsResult{
		SchemaVersion: app.ConfluenceStructuralSchemaVersion, ID: "42", Version: version,
		PageVersionGated: opts.ExpectedPageVersion > 0,
		RequestedCount:   len(opts.Selectors), ReturnedCount: len(sections), Reconciled: true,
		Complete: true, MaxBytes: opts.MaxBytes, Sections: sections,
	}, nil
}

func (r *recordingConfluenceReader) AttachmentInventory(_ context.Context, reference string, opts app.ConfluenceAttachmentInventoryOpts) (*app.ConfluenceAttachmentInventoryResult, error) {
	r.attachmentReference, r.attachmentOpts = reference, opts
	r.attachmentCalls++
	if r.attachmentErr != nil {
		return nil, r.attachmentErr
	}
	if r.attachmentResult != nil {
		return r.attachmentResult, nil
	}
	return &app.ConfluenceAttachmentInventoryResult{
		SchemaVersion: 1, PageID: "42", PageVersion: opts.ExpectedPageVersion,
		Complete: true, Attachments: []domain.Attachment{},
	}, nil
}

func (r *recordingConfluenceReader) SummarizeTablesWithOptions(_ context.Context, reference string, table int, opts app.ConfluenceTableReadOpts) (*app.ConfluenceTableSummary, error) {
	r.tableSummaryReference, r.tableSummaryIndex, r.tableSummaryOpts = reference, table, opts
	if r.tableErr != nil {
		return nil, r.tableErr
	}
	version := opts.ExpectedPageVersion
	if version == 0 {
		version = 3
	}
	tables := []app.ConfluenceTableSummaryRecord{{Index: table, RowCount: 1, ColumnCount: 1, Rectangular: true,
		ExpandedCellCount: 1, OriginCellCount: 1, CellCountReconciled: true}}
	if table == 0 {
		tables = []app.ConfluenceTableSummaryRecord{{Index: 1, RowCount: 1, ColumnCount: 1, Rectangular: true,
			ExpandedCellCount: 1, OriginCellCount: 1, CellCountReconciled: true},
			{Index: 2, RowCount: 1, ColumnCount: 1, Rectangular: true,
				ExpandedCellCount: 1, OriginCellCount: 1, CellCountReconciled: true}}
	}
	return &app.ConfluenceTableSummary{SchemaVersion: app.ConfluenceTableSchemaVersion, CellContract: app.ConfluenceTableCellContract, PageID: "42",
		Version: version, PageVersionGated: opts.ExpectedPageVersion > 0, TableCount: 2, Table: table, ReturnedTableCount: len(tables),
		SelectionReconciled: true, Tables: tables}, nil
}

func (r *invalidConfluenceTableReader) SummarizeTablesWithOptions(ctx context.Context, reference string, table int, opts app.ConfluenceTableReadOpts) (*app.ConfluenceTableSummary, error) {
	result, err := r.recordingConfluenceReader.SummarizeTablesWithOptions(ctx, reference, table, opts)
	switch r.mode {
	case "summary-selection":
		result.SelectionReconciled = false
	case "summary-rectangular":
		result.Tables[0].Rectangular = false
	case "summary-cell-count":
		result.Tables[0].CellCountReconciled = false
	case "summary-schema":
		result.SchemaVersion++
	case "summary-cell-contract":
		result.CellContract = ""
	case "summary-version":
		result.Version = 0
	case "summary-gate":
		result.PageVersionGated = true
	case "summary-bound-ungated":
		result.PageVersionGated = false
	case "summary-bound-wrong-version":
		result.Version++
	}
	return result, err
}

func (r *invalidConfluenceTableReader) ExtractTablesWithOptions(ctx context.Context, reference string, table int, opts app.ConfluenceTableReadOpts) (*app.ConfluenceTableExtract, error) {
	result, err := r.recordingConfluenceReader.ExtractTablesWithOptions(ctx, reference, table, opts)
	switch r.mode {
	case "extract":
		result.Tables = append(result.Tables, result.Tables[0])
	case "extract-returned-count":
		result.ReturnedTableCount++
	case "extract-reconciliation":
		result.SelectionReconciled = false
	case "extract-dimensions":
		result.Tables[0].RowCount++
	case "extract-summary":
		result.Tables[0].Summary.ExpandedCellCount++
	case "extract-schema":
		result.SchemaVersion++
	case "extract-cell-contract":
		result.CellContract = ""
	case "extract-version":
		result.Version = 0
	case "extract-gate":
		result.PageVersionGated = true
	case "extract-bound-ungated":
		result.PageVersionGated = false
	case "extract-bound-wrong-version":
		result.Version++
	}
	return result, err
}

func (r *recordingConfluenceReader) ExtractTablesWithOptions(_ context.Context, reference string, table int, opts app.ConfluenceTableReadOpts) (*app.ConfluenceTableExtract, error) {
	r.tableExtractReference, r.tableExtractIndex, r.tableExtractOpts = reference, table, opts
	if r.tableErr != nil {
		return nil, r.tableErr
	}
	version := opts.ExpectedPageVersion
	if version == 0 {
		version = 3
	}
	result := &app.ConfluenceTableExtract{SchemaVersion: app.ConfluenceTableSchemaVersion, CellContract: app.ConfluenceTableCellContract, PageID: "42",
		Version: version, PageVersionGated: opts.ExpectedPageVersion > 0, TableCount: 2, Table: table,
		ReturnedTableCount: 1, SelectionReconciled: true, Tables: []app.ConfluenceTable{{Index: table,
			RowCount: 1, ColumnCount: 1, Rows: []app.ConfluenceTableRow{{Index: 1,
				Cells: []app.ConfluenceTableCell{{Row: 1, Column: 1, Text: r.tableText}}}}}}}
	summary := app.SummarizeConfluenceTables(result)
	record := summary.Tables[0]
	record.CellCountReconciled = true
	result.Tables[0].Summary = record
	return result, nil
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

func (r *cancellingJiraReader) IssueGraphWithOptions(ctx context.Context, _ string, _ app.JiraIssueGraphOptions) (*app.JiraIssueGraphResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (r *cancellingJiraReader) HistoryFiltered(ctx context.Context, _ string, _ app.JiraHistoryOpts) (*app.JiraHistoryResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return nil, ctx.Err()
}

func (r *cancellingJiraReader) IssueRefs(ctx context.Context, _ app.JiraIssueRefsOpts) (*app.JiraIssueRefsResult, error) {
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

func (*cancellingJiraReader) Structure(context.Context, int64) (*domain.Structure, error) {
	panic("unexpected call")
}

func (*cancellingJiraReader) StructureSnapshot(context.Context, int64, app.StructureSnapshotOpts) (*app.StructureSnapshot, error) {
	panic("unexpected call")
}

func (r *cancellingConfluenceReader) SummarizeTablesWithOptions(ctx context.Context, _ string, _ int, _ app.ConfluenceTableReadOpts) (*app.ConfluenceTableSummary, error) {
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

func (*cancellingConfluenceReader) PageMetadata(context.Context, string) (*app.ConfluencePageMetadataResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) PageOutline(context.Context, string) (*app.ConfluencePageOutlineResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) PageSection(context.Context, string, app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) PageSections(context.Context, string, app.ConfluencePageSectionsOpts) (*app.ConfluencePageSectionsResult, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) ExtractTablesWithOptions(context.Context, string, int, app.ConfluenceTableReadOpts) (*app.ConfluenceTableExtract, error) {
	panic("unexpected call")
}

func (*cancellingConfluenceReader) AttachmentInventory(context.Context, string, app.ConfluenceAttachmentInventoryOpts) (*app.ConfluenceAttachmentInventoryResult, error) {
	panic("unexpected call")
}

// The bounded byte resolvers and the bounded JSON output helpers repeat one
// shape across tools, so the tests below pin what a consolidation must not
// change: the exact default, minimum, and maximum each resolver applies, the
// order in which the range check and the minimum check report, the exact
// message text, which helpers refuse a nil result and which encode it as
// `null`, and which oversize errors carry domain.ErrOutputLimit.
func TestBoundedByteResolversPinDefaultsMinimaAndMaxima(t *testing.T) {
	resolvers := []struct {
		name                   string
		resolve                func(int) (int, error)
		defaultValue, min, max int
	}{
		{
			name:         "confluence_table_summary",
			resolve:      func(value int) (int, error) { return boundedTableBytes(value, confluenceTableSummaryDefaultMaxBytes) },
			defaultValue: confluenceTableSummaryDefaultMaxBytes, min: confluenceTableMinMaxBytes, max: confluenceTableMaxMaxBytes,
		},
		{
			name:         "confluence_table_extract",
			resolve:      func(value int) (int, error) { return boundedTableBytes(value, confluenceTableExtractDefaultMaxBytes) },
			defaultValue: confluenceTableExtractDefaultMaxBytes, min: confluenceTableMinMaxBytes, max: confluenceTableMaxMaxBytes,
		},
		{
			name:         "confluence_search",
			resolve:      boundedConfluenceSearchBytes,
			defaultValue: confluenceSearchDefaultMaxBytes, min: confluenceSearchMinMaxBytes, max: confluenceSearchMaxMaxBytes,
		},
		{
			name:         "confluence_attachment_list",
			resolve:      boundedConfluenceAttachmentBytes,
			defaultValue: confluenceAttachmentDefaultMaxBytes, min: confluenceAttachmentMinMaxBytes, max: confluenceAttachmentMaxMaxBytes,
		},
		{
			name:         "jira_evidence",
			resolve:      boundedJiraEvidenceBytes,
			defaultValue: jiraEvidenceDefaultMaxBytes, min: jiraEvidenceMinMaxBytes, max: jiraEvidenceMaxMaxBytes,
		},
		{
			name: "jira_structure_view",
			resolve: func(value int) (int, error) {
				_, _, maxBytes, _, err := validatedStructureViewInput(JiraStructureViewInput{StructureID: 1, MaxBytes: value})
				return maxBytes, err
			},
			defaultValue: jiraStructureViewDefaultMaxBytes, min: jiraStructureViewMinMaxBytes, max: jiraStructureViewMaxMaxBytes,
		},
	}
	for _, resolver := range resolvers {
		t.Run(resolver.name, func(t *testing.T) {
			belowMinimum := fmt.Sprintf("max_bytes must be at least %d", resolver.min)
			outOfRange := fmt.Sprintf("max_bytes must be between 1 and %d", resolver.max)
			cases := []struct {
				name    string
				value   int
				want    int
				wantErr string
			}{
				{name: "zero_resolves_the_default", value: 0, want: resolver.defaultValue},
				// One pins the below-minimum message; the negative case below
				// proves the range error wins when both checks could reject.
				{name: "one", value: 1, wantErr: belowMinimum},
				{name: "minimum_minus_one", value: resolver.min - 1, wantErr: belowMinimum},
				{name: "minimum", value: resolver.min, want: resolver.min},
				{name: "maximum", value: resolver.max, want: resolver.max},
				{name: "maximum_plus_one", value: resolver.max + 1, wantErr: outOfRange},
				{name: "negative", value: -1, wantErr: outOfRange},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					got, err := resolver.resolve(test.value)
					if test.wantErr == "" {
						if err != nil || got != test.want {
							t.Fatalf("resolve(%d)=%d err=%v want %d", test.value, got, err, test.want)
						}
						return
					}
					if got != 0 || !errors.Is(err, domain.ErrUsage) ||
						err.Error() != domain.ErrUsage.Error()+": "+test.wantErr {
						t.Fatalf("resolve(%d)=%d err=%v want usage error %q", test.value, got, err, test.wantErr)
					}
				})
			}
		})
	}
}

// jira_issue_field_get resolves its own byte bound inline against the
// application-layer constants, so it is pinned through the tool surface.
func TestJiraIssueFieldGetByteBoundIsResolvedBeforeTheBackend(t *testing.T) {
	cases := []struct {
		name           string
		value          int
		want           int
		wantUsageError bool
	}{
		{name: "zero_resolves_the_default", value: 0, want: app.JiraIssueFieldEvidenceDefaultMaxBytes},
		{name: "one", value: 1, wantUsageError: true},
		{name: "minimum_minus_one", value: app.JiraIssueFieldEvidenceMinMaxBytes - 1, wantUsageError: true},
		{name: "minimum", value: app.JiraIssueFieldEvidenceMinMaxBytes, want: app.JiraIssueFieldEvidenceMinMaxBytes},
		{name: "maximum", value: app.JiraIssueFieldEvidenceMaxMaxBytes, want: app.JiraIssueFieldEvidenceMaxMaxBytes},
		{name: "maximum_plus_one", value: app.JiraIssueFieldEvidenceMaxMaxBytes + 1, wantUsageError: true},
		{name: "negative", value: -1, wantUsageError: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingJiraReader{}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()
			args := map[string]any{"key": "PROJ-1", "field": "customfield_1", "max_bytes": test.value}
			if test.wantUsageError {
				result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_issue_field_get", Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				text, ok := result.Content[0].(*mcp.TextContent)
				if !result.IsError || len(result.Content) != 1 || !ok {
					t.Fatalf("result=%+v", result)
				}
				var got toolError
				if err := json.Unmarshal([]byte(text.Text), &got); err != nil ||
					got.Kind != "usage_error" || got.Remediation != "fix_request" {
					t.Fatalf("error=%+v decode=%v", got, err)
				}
				if reader.fieldEvidenceKey != "" {
					t.Fatalf("backend reached with key=%q", reader.fieldEvidenceKey)
				}
				return
			}
			callToolOK(t, client, "jira_issue_field_get", args)
			if reader.fieldEvidenceOpts.MaxBytes != test.want {
				t.Fatalf("forwarded max_bytes=%d want %d", reader.fieldEvidenceOpts.MaxBytes, test.want)
			}
		})
	}
}

func TestBoundedOutputHelpersRefuseUnavailableResults(t *testing.T) {
	cases := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "attachment_inventory_nil",
			call: func() error { return boundedAttachmentInventoryOutput(nil, confluenceAttachmentMinMaxBytes) },
			want: "Confluence attachment inventory is unavailable",
		},
		{
			name: "confluence_search_nil",
			call: func() error { return boundedConfluenceSearchOutput(nil, confluenceSearchMinMaxBytes) },
			want: "Confluence search result is unavailable",
		},
		{
			name: "structure_nil",
			call: func() error { return boundedStructureOutput(nil, jiraStructureViewMinMaxBytes) },
			want: "Structure result is unavailable",
		},
		{
			name: "jira_evidence_nil_interface",
			call: func() error { return boundedJiraEvidenceOutput(nil, jiraEvidenceMinMaxBytes) },
			want: "Jira evidence result is unavailable",
		},
		{
			name: "jira_evidence_typed_nil_pointer",
			call: func() error {
				return boundedJiraEvidenceOutput((*app.JiraHistorySummaryResult)(nil), jiraEvidenceMinMaxBytes)
			},
			want: "Jira evidence result is unavailable",
		},
		{
			name: "table_nil_interface",
			call: func() error { return boundedTableOutput(nil, confluenceTableMinMaxBytes) },
			want: "table result is unavailable",
		},
		{
			name: "table_typed_nil_pointer",
			call: func() error {
				return boundedTableOutput((*app.ConfluenceTableSummary)(nil), confluenceTableMinMaxBytes)
			},
			want: "table result is unavailable",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrOutputLimit) ||
				err.Error() != domain.ErrCheckFailed.Error()+": "+test.want {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// Only a nil pointer is an unavailable result. A nil slice or map is real
// evidence about an empty collection, so it stays on the encoding path.
func TestBoundedOutputHelpersEncodeNonPointerNilsAsEvidence(t *testing.T) {
	if err := boundedJiraEvidenceOutput([]string(nil), jiraEvidenceMinMaxBytes); err != nil {
		t.Fatalf("nil slice err=%v", err)
	}
	if err := boundedTableOutput(map[string]string(nil), confluenceTableMinMaxBytes); err != nil {
		t.Fatalf("nil map err=%v", err)
	}
}

func TestBoundedOutputHelpersRefuseUnencodableResults(t *testing.T) {
	cases := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "jira_evidence",
			call: func() error { return boundedJiraEvidenceOutput(make(chan int), jiraEvidenceMinMaxBytes) },
			want: "encode Jira evidence result",
		},
		{
			name: "table",
			call: func() error { return boundedTableOutput(make(chan int), confluenceTableMinMaxBytes) },
			want: "encode table result",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrOutputLimit) ||
				err.Error() != domain.ErrCheckFailed.Error()+": "+test.want {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestBoundedOutputHelpersEnforceExactByteBounds(t *testing.T) {
	inventory := &app.ConfluenceAttachmentInventoryView{
		SchemaVersion: 1, PageID: "1", PageVersion: 2, Complete: true,
		Attachments: []app.ConfluenceAttachmentView{},
	}
	search := &app.ConfluenceSearchResult{SchemaVersion: 1, Query: "space=DOCS", Results: []domain.PageRef{}}
	snapshot := &app.StructureSnapshot{SchemaVersion: 1}
	cases := []struct {
		name  string
		value any
		at    func(int) error
		want  string
	}{
		{
			name: "attachment_inventory", value: inventory,
			at:   func(maxBytes int) error { return boundedAttachmentInventoryOutput(inventory, maxBytes) },
			want: "Confluence attachment inventory exceeds max_bytes; raise the bound",
		},
		{
			name: "confluence_search", value: search,
			at:   func(maxBytes int) error { return boundedConfluenceSearchOutput(search, maxBytes) },
			want: "Confluence search result exceeds max_bytes; narrow CQL or lower the row limit before raising the bound",
		},
		{
			name: "structure", value: snapshot,
			at:   func(maxBytes int) error { return boundedStructureOutput(snapshot, maxBytes) },
			want: "Structure result exceeds max_bytes; select an exact subtree or raise the bound",
		},
		{
			name: "jira_evidence", value: "evidence",
			at:   func(maxBytes int) error { return boundedJiraEvidenceOutput("evidence", maxBytes) },
			want: "Jira evidence result exceeds max_bytes; narrow the selection or raise the bound",
		},
		{
			name: "table", value: "table",
			at:   func(maxBytes int) error { return boundedTableOutput("table", maxBytes) },
			want: "table result exceeds max_bytes; select one table or raise the bound",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			exact := len(encoded)
			for _, maxBytes := range []int{exact, exact + 1} {
				if err := test.at(maxBytes); err != nil {
					t.Fatalf("max_bytes=%d (encoded %d) err=%v", maxBytes, exact, err)
				}
			}
			for _, maxBytes := range []int{0, exact - 1} {
				err := test.at(maxBytes)
				if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrOutputLimit) ||
					err.Error() != domain.ErrCheckFailed.Error()+": "+domain.ErrOutputLimit.Error()+": "+test.want {
					t.Fatalf("max_bytes=%d (encoded %d) err=%v", maxBytes, exact, err)
				}
			}
		})
	}
}

// The two fixed-bound helpers guard no nil: they run only on results a
// validator already reconciled, and they must keep encoding a nil pointer as
// `null` rather than growing an availability check. Structure metadata is also
// the one oversize error that deliberately does not carry
// domain.ErrOutputLimit.
func TestFixedBoundOutputHelpersPinNilAndOversizeBehavior(t *testing.T) {
	t.Run("confluence_page_metadata", func(t *testing.T) {
		if err := boundedConfluencePageMetadataOutput(nil); err != nil {
			t.Fatalf("nil err=%v", err)
		}
		metadata := &app.ConfluencePageMetadataResult{
			SchemaVersion: app.ConfluencePageMetadataSchemaVersion, ID: "1", Space: "DOCS",
			Version: 1, RestrictionState: app.ConfluenceRestrictionUnrestricted,
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		metadata.Title = strings.Repeat("x", confluencePageMetadataMaxBytes-len(encoded))
		if err := boundedConfluencePageMetadataOutput(metadata); err != nil {
			t.Fatalf("exact bound err=%v", err)
		}
		metadata.Title += "x"
		err = boundedConfluencePageMetadataOutput(metadata)
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrOutputLimit) ||
			err.Error() != domain.ErrCheckFailed.Error()+": "+domain.ErrOutputLimit.Error()+
				": Confluence page metadata exceeds its output bound" {
			t.Fatalf("oversize err=%v", err)
		}
	})
	t.Run("structure_metadata", func(t *testing.T) {
		if err := boundedStructureMetadataOutput(nil); err != nil {
			t.Fatalf("nil err=%v", err)
		}
		metadata := &app.StructureMetadataResult{SchemaVersion: 1, ID: 9}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		metadata.Name = strings.Repeat("x", jiraStructureMetadataMaxBytes-len(encoded))
		if err := boundedStructureMetadataOutput(metadata); err != nil {
			t.Fatalf("exact bound err=%v", err)
		}
		metadata.Name += "x"
		err = boundedStructureMetadataOutput(metadata)
		if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrOutputLimit) ||
			err.Error() != domain.ErrCheckFailed.Error()+": Structure metadata exceeds the output bound" {
			t.Fatalf("oversize err=%v", err)
		}
	})
}

// classifierCategoryFixture pins one diagnostic category together with an error
// fixture that carries a private marker, so the classifier matrix doubles as
// redaction coverage for every category.
type classifierCategoryFixture struct {
	kind        string
	remediation string
	err         error
}

const classifierMatrixMarker = "SYNTHETIC-CLASSIFIER-MATRIX-SECRET"

func classifierCategoryFixtures() []classifierCategoryFixture {
	const marker = classifierMatrixMarker
	apiError := func(status int) error {
		return fmt.Errorf("read %s: %w", marker, &httpx.APIError{
			Status: status, Method: http.MethodGet,
			Path: "/private/" + marker, Body: "body " + marker,
		})
	}
	return []classifierCategoryFixture{
		{"usage_error", "fix_request", fmt.Errorf("%w: bad request %s", domain.ErrUsage, marker)},
		{"configuration_error", "complete_configuration", fmt.Errorf("%w: missing config %s", domain.ErrConfig, marker)},
		{"authentication_failed", "reauthenticate", fmt.Errorf("%w: token rejected %s", domain.ErrAuth, marker)},
		{"forbidden", "request_access", fmt.Errorf("%w: denied %s", domain.ErrForbidden, marker)},
		{"not_found", "verify_identifier_or_access", fmt.Errorf("%w: missing %s", domain.ErrNotFound, marker)},
		{"version_conflict", "refresh_and_reapply", fmt.Errorf("%w: stale %s", domain.ErrVersionConflict, marker)},
		{"check_failed", "review_failed_check", fmt.Errorf("%w: unreconciled %s", domain.ErrCheckFailed, marker)},
		{"output_limit_exceeded", "narrow_or_raise_bound", fmt.Errorf("%w: too big %s", domain.ErrOutputLimit, marker)},
		{"rate_limited", "wait_before_retry", apiError(http.StatusTooManyRequests)},
		{"api_error", "inspect_backend_error", apiError(http.StatusServiceUnavailable)},
		{"transport_error", "inspect_network_before_retry", fmt.Errorf("read %s: %w", marker, &httpx.TransportError{Method: http.MethodGet, Category: "timeout"})},
		{"unexpected_error", "inspect_error", errors.New("unexpected " + marker)},
	}
}

// classifierExpectation is the exact tool error one classifier must produce for
// one category. An empty remediation means the category's own remediation,
// which classifierCategoryFixtures pins against diagnostic.Classify.
type classifierExpectation struct {
	message     string
	remediation string
}

// TestToolErrorClassifierMatrixIsExact pins every classifier/category pair:
// the message, any remediation override, and the fact that no fixture prose or
// backend request detail survives classification. Entries are positional and
// follow classifierCategoryFixtures order.
func TestToolErrorClassifierMatrixIsExact(t *testing.T) {
	categories := classifierCategoryFixtures()
	for _, category := range categories {
		kind, remediation := diagnostic.Classify(category.err)
		if kind != category.kind || remediation != category.remediation {
			t.Fatalf("fixture %q classifies as %q/%q", category.kind, kind, remediation)
		}
	}

	matrix := []struct {
		name     string
		classify func(error) error
		want     []classifierExpectation
	}{
		{
			name: "generic", classify: classified,
			want: []classifierExpectation{
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool request failed"},
				{message: "tool result exceeds max_bytes"},
				{message: "backend returned HTTP 429"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "tool request failed"},
			},
		},
		{
			name: "outline", classify: classifiedOutlineRead,
			want: []classifierExpectation{
				{message: "invalid Confluence page outline request"},
				{message: "Confluence page outline service is not configured"},
				{message: "Confluence page outline authentication failed"},
				{message: "Confluence page outline access is forbidden"},
				{message: "Confluence page was not found"},
				{message: "Confluence page outline read failed"},
				{message: "Confluence page outline result failed validation"},
				{message: "Confluence page outline result exceeds its output bound"},
				{message: "Confluence page outline read failed"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "Confluence page outline read failed"},
			},
		},
		{
			name: "page_metadata", classify: classifiedConfluencePageMetadataRead,
			want: []classifierExpectation{
				{message: "invalid Confluence page metadata request"},
				{message: "Confluence page metadata service is not configured"},
				{message: "Confluence page metadata authentication failed"},
				{message: "Confluence page metadata access is forbidden"},
				{message: "Confluence page was not found"},
				{message: "Confluence page metadata read failed"},
				{message: "Confluence page metadata failed validation"},
				{message: "Confluence page metadata exceeds its output bound", remediation: "use_cli_conf_page_meta"},
				{message: "Confluence page metadata rate limit was exhausted"},
				{message: "Confluence page metadata API request failed"},
				{message: "Confluence page metadata transport failed"},
				{message: "Confluence page metadata read failed"},
			},
		},
		{
			name: "jira_history", classify: classifiedJiraHistoryRead,
			want: []classifierExpectation{
				{message: "invalid Jira issue history request"},
				{message: "Jira issue history service is not configured"},
				{message: "Jira issue history authentication failed"},
				{message: "Jira issue history access is forbidden"},
				{message: "Jira issue history was not found"},
				{message: "Jira issue history read failed"},
				{message: "Jira issue history summary failed validation"},
				{message: "Jira issue history result exceeds max_bytes"},
				{message: "Jira issue history read failed"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "Jira issue history read failed"},
			},
		},
		{
			name: "jira_issue_refs", classify: classifiedJiraIssueRefsRead,
			want: []classifierExpectation{
				{message: "invalid Jira issue reference summary request"},
				{message: "Jira issue reference summary service is not configured"},
				{message: "Jira issue reference summary authentication failed"},
				{message: "Jira issue reference summary access is forbidden"},
				{message: "Jira issue reference source was not found"},
				{message: "Jira issue reference summary read failed"},
				{message: "Jira issue reference summary failed validation"},
				{message: "Jira issue reference summary exceeds max_bytes"},
				{message: "Jira issue reference summary rate limit was exhausted"},
				{message: "Jira issue reference summary API request failed"},
				{message: "Jira issue reference summary transport failed"},
				{message: "Jira issue reference summary read failed"},
			},
		},
		{
			name: "table", classify: classifiedTableRead,
			want: []classifierExpectation{
				{message: "invalid Confluence table request"},
				{message: "Confluence table service is not configured"},
				{message: "Confluence table authentication failed"},
				{message: "Confluence table access is forbidden"},
				{message: "Confluence page or table was not found"},
				{message: "Confluence table read failed"},
				{message: "Confluence table result failed validation"},
				{message: "Confluence table result exceeds the selected output bound"},
				{message: "Confluence table read failed"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "Confluence table read failed"},
			},
		},
		{
			name: "section", classify: classifiedSectionRead,
			want: []classifierExpectation{
				{message: "invalid Confluence page section request"},
				{message: "Confluence page section service is not configured"},
				{message: "Confluence page section authentication failed"},
				{message: "Confluence page section access is forbidden"},
				{message: "Confluence page, section, or heading was not found"},
				{message: "Confluence page section read failed"},
				{message: "Confluence page section result failed validation"},
				{message: "Confluence page section result exceeds the selected output bound"},
				{message: "Confluence page section read failed"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "Confluence page section read failed"},
			},
		},
		{
			name: "attachment_inventory", classify: classifiedAttachmentInventoryRead,
			want: []classifierExpectation{
				{message: "invalid Confluence attachment inventory request"},
				{message: "Confluence attachment inventory service is not configured"},
				{message: "Confluence attachment inventory authentication failed"},
				{message: "Confluence attachment inventory access is forbidden"},
				{message: "Confluence page was not found"},
				{message: "Confluence attachment inventory read failed"},
				{message: "Confluence attachment inventory failed validation"},
				{message: "Confluence attachment inventory exceeds the selected output bound", remediation: "raise_bound_or_use_cli_attachment_list"},
				{message: "Confluence attachment inventory read failed"},
				{message: "Confluence attachment inventory read failed"},
				{message: "Confluence attachment inventory read failed"},
				{message: "Confluence attachment inventory read failed"},
			},
		},
		{
			name: "structure", classify: classifiedStructureRead,
			want: []classifierExpectation{
				{message: "invalid Jira Structure request"},
				{message: "Jira Structure service is not configured"},
				{message: "Jira Structure authentication failed"},
				{message: "Jira Structure access is forbidden"},
				{message: "Jira Structure or subtree was not found"},
				{message: "Jira Structure read failed"},
				{message: "Jira Structure result failed validation"},
				{message: "Jira Structure result exceeds the selected output bound"},
				{message: "Jira Structure read failed"},
				{message: "backend returned HTTP 503"},
				{message: "backend transport failed (timeout)"},
				{message: "Jira Structure read failed"},
			},
		},
		{
			name: "mirror", classify: classifiedMirrorRead,
			want: []classifierExpectation{
				{message: "local mirror snapshot failed"},
				{message: "local mirror root is not configured or is invalid"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot could not be completed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
				{message: "local mirror snapshot failed"},
			},
		},
	}
	if len(matrix) != 10 {
		t.Fatalf("classifier matrix has %d rows, want 10 current families", len(matrix))
	}

	for _, entry := range matrix {
		if len(entry.want) != len(categories) {
			t.Fatalf("%s expectation count=%d, want %d", entry.name, len(entry.want), len(categories))
		}
		for i, category := range categories {
			t.Run(entry.name+"/"+category.kind, func(t *testing.T) {
				if entry.classify(nil) != nil {
					t.Fatal("nil error must classify as nil")
				}
				want := entry.want[i]
				wantRemediation := category.remediation
				if want.remediation != "" {
					wantRemediation = want.remediation
				}
				var got toolError
				if !errors.As(entry.classify(category.err), &got) {
					t.Fatalf("classified error is not a toolError")
				}
				if got.Kind != category.kind || got.Remediation != wantRemediation || got.Message != want.message {
					t.Fatalf("got %+v, want kind=%q remediation=%q message=%q",
						got, category.kind, wantRemediation, want.message)
				}
				if !diagnostic.ValidateRecovery(got.Recovery) {
					t.Fatalf("invalid recovery: %+v", got.Recovery)
				}
				for _, forbidden := range []string{
					classifierMatrixMarker, "/private/", "body ",
				} {
					if strings.Contains(got.Error(), forbidden) {
						t.Fatalf("classified error leaked %q: %s", forbidden, got.Error())
					}
				}
			})
		}
	}
}

func TestToolErrorPoliciesAlwaysHaveClientMessages(t *testing.T) {
	policies := []struct {
		name   string
		policy toolErrorPolicy
	}{
		{name: "generic", policy: genericToolPolicy},
		{name: "outline", policy: confluenceOutlineReadPolicy},
		{name: "page_metadata", policy: confluencePageMetadataReadPolicy},
		{name: "jira_history", policy: jiraHistoryReadPolicy},
		{name: "jira_issue_refs", policy: jiraIssueRefsReadPolicy},
		{name: "table", policy: confluenceTableReadPolicy},
		{name: "section", policy: confluenceSectionReadPolicy},
		{name: "attachment_inventory", policy: confluenceAttachmentInventoryReadPolicy},
		{name: "structure", policy: jiraStructureReadPolicy},
		{name: "mirror", policy: mirrorReadPolicy},
	}
	valid := func(rule toolErrorRule) bool { return rule.message != "" || rule.safeMessage }
	for _, entry := range policies {
		t.Run(entry.name, func(t *testing.T) {
			if !valid(entry.policy.fallback) {
				t.Fatal("fallback has no static or redacted client message")
			}
			for kind, rule := range entry.policy.kinds {
				if !valid(rule) {
					t.Fatalf("category %q has no static or redacted client message", kind)
				}
			}
		})
	}
}

// TestToolErrorClassifierCompositePrecedence pins the two deliberately opposite
// composite orderings: a section version mismatch outranks a section selection
// error, while a Structure folder selection error outranks a forest version
// mismatch. errors.Join makes both typed errors reachable at once.
func TestToolErrorClassifierCompositePrecedence(t *testing.T) {
	const marker = "SYNTHETIC-COMPOSITE-PRECEDENCE-SECRET"
	for _, test := range []struct {
		name, kind, remediation, message string
		classify                         func(error) error
		err                              error
	}{
		{
			name:     "section version mismatch outranks selection",
			classify: classifiedSectionRead,
			err: fmt.Errorf("%s: %w", marker, errors.Join(
				&app.ConfluenceSectionSelectionError{Requested: 0, Available: 3},
				&app.ConfluencePageVersionMismatchError{Expected: 4, Current: 5},
			)),
			kind:        "check_failed",
			remediation: "reread_outline_then_retry_expected_version",
			message:     "expected Confluence page version 4 does not match the current page version 5",
		},
		{
			name:     "section selection alone stays recoverable",
			classify: classifiedSectionRead,
			err: fmt.Errorf("%s: %w", marker,
				&app.ConfluenceSectionSelectionError{Requested: 0, Available: 3}),
			kind:        "check_failed",
			remediation: "outline_then_select_section",
			message:     "Confluence heading selection is ambiguous; available occurrence count is 3, so select an occurrence from 1 to 3",
		},
		{
			name:     "structure folder selection outranks forest version mismatch",
			classify: classifiedStructureRead,
			err: fmt.Errorf("%s: %w", marker, errors.Join(
				&app.StructureForestVersionMismatchError{
					Expected: domain.StructureVersion{Signature: -55, Version: 7},
					Current:  domain.StructureVersion{Signature: 66, Version: 8},
				},
				&app.StructureFolderSelectionError{
					Reason: app.StructureFolderSelectionAmbiguous, Matches: 2, Available: 4,
				},
			)),
			kind:        "check_failed",
			remediation: "view_then_select_subtree",
			message:     "Jira Structure folder selector is ambiguous; matching stored-folder count is 2 and available stored-folder count is 4",
		},
		{
			name:     "structure forest version mismatch alone stays recoverable",
			classify: classifiedStructureRead,
			err: fmt.Errorf("%s: %w", marker, &app.StructureForestVersionMismatchError{
				Expected: domain.StructureVersion{Signature: -55, Version: 7},
				Current:  domain.StructureVersion{Signature: 66, Version: 8},
			}),
			kind:        "check_failed",
			remediation: "reread_structure_view_then_retry_expected_forest_version",
			message:     "expected Jira Structure forest signature -55 version 7 does not match current signature 66 version 8",
		},
		{
			name:     "table version mismatch keeps its own remediation",
			classify: classifiedTableRead,
			err: fmt.Errorf("%s: %w", marker,
				&app.ConfluencePageVersionMismatchError{Expected: 2, Current: 9}),
			kind:        "check_failed",
			remediation: "reread_table_summary_then_retry_expected_version",
			message:     "expected Confluence page version 2 does not match the current page version 9",
		},
		{
			name:     "attachment inventory version mismatch keeps its own remediation",
			classify: classifiedAttachmentInventoryRead,
			err: fmt.Errorf("%s: %w", marker,
				&app.ConfluencePageVersionMismatchError{Expected: 2, Current: 9}),
			kind:        "check_failed",
			remediation: "reread_page_then_retry_expected_version",
			message:     "expected Confluence page version 2 does not match the current page version 9",
		},
		{
			name:     "table selection is out of range",
			classify: classifiedTableRead,
			err: fmt.Errorf("%s: %w", marker,
				&app.ConfluenceTableSelectionError{Requested: 7, Available: 3}),
			kind:        "not_found",
			remediation: "summarize_then_select_table",
			message:     "selected Confluence table index 7 is out of range; available table count is 3",
		},
		{
			name:     "section occurrence is out of range",
			classify: classifiedSectionRead,
			err: fmt.Errorf("%s: %w", marker,
				&app.ConfluenceSectionSelectionError{Requested: 7, Available: 3}),
			kind:        "not_found",
			remediation: "outline_then_select_section",
			message:     "selected Confluence heading occurrence 7 is out of range; available occurrence count is 3",
		},
		{
			name:     "structure folder selector is stale",
			classify: classifiedStructureRead,
			err: structureSelectionFixture(domain.ErrNotFound,
				&app.StructureFolderSelectionError{Reason: app.StructureFolderSelectionNotFound, Available: 4},
				"folder "+marker+" was not found"),
			kind:        "not_found",
			remediation: "view_then_select_subtree",
			message:     "selected Jira Structure folder was not found; available stored-folder count is 4",
		},
		{
			name:     "structure folder labels are incomplete",
			classify: classifiedStructureRead,
			err: structureSelectionFixture(domain.ErrCheckFailed,
				&app.StructureFolderSelectionError{Reason: app.StructureFolderSelectionLabelsIncomplete, Available: 4},
				"folder path "+marker+" cannot be validated"),
			kind:        "check_failed",
			remediation: "view_then_select_subtree",
			message:     "Jira Structure folder path cannot be validated because folder labels are incomplete; available stored-folder count is 4",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got toolError
			if !errors.As(test.classify(test.err), &got) {
				t.Fatalf("classified error is not a toolError")
			}
			if got.Kind != test.kind || got.Remediation != test.remediation || got.Message != test.message {
				t.Fatalf("got %+v, want kind=%q remediation=%q message=%q",
					got, test.kind, test.remediation, test.message)
			}
			if !diagnostic.ValidateRecovery(got.Recovery) || got.Recovery.RetrySafe {
				t.Fatalf("invalid changed-argument recovery: %+v", got.Recovery)
			}
			if strings.Contains(got.Error(), marker) {
				t.Fatalf("composite classification leaked backend prose: %s", got.Error())
			}
		})
	}
}

// TestToolErrorClassifiersIgnoreUntypedSelectionProse keeps the recoverable
// remediations bound to typed application errors: prose that merely reads like
// a selection or version failure must stay on the static path.
func TestToolErrorClassifiersIgnoreUntypedSelectionProse(t *testing.T) {
	for _, test := range []struct {
		name, remediation, message string
		classify                   func(error) error
		err                        error
	}{
		{
			name: "section", classify: classifiedSectionRead,
			err:         fmt.Errorf("%w: heading occurrence 7 is out of range; expected version 4 got 5", domain.ErrCheckFailed),
			remediation: "review_failed_check",
			message:     "Confluence page section result failed validation",
		},
		{
			name: "structure", classify: classifiedStructureRead,
			err:         fmt.Errorf("%w: folder selector is ambiguous; forest signature -55 version 7", domain.ErrCheckFailed),
			remediation: "review_failed_check",
			message:     "Jira Structure result failed validation",
		},
		{
			name: "table", classify: classifiedTableRead,
			err:         fmt.Errorf("%w: table index 7 is out of range", domain.ErrNotFound),
			remediation: "verify_identifier_or_access",
			message:     "Confluence page or table was not found",
		},
		{
			name: "attachment_inventory", classify: classifiedAttachmentInventoryRead,
			err:         fmt.Errorf("%w: expected page version 2 does not match 9", domain.ErrCheckFailed),
			remediation: "review_failed_check",
			message:     "Confluence attachment inventory failed validation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got toolError
			if !errors.As(test.classify(test.err), &got) {
				t.Fatalf("classified error is not a toolError")
			}
			if got.Remediation != test.remediation || got.Message != test.message {
				t.Fatalf("got %+v, want remediation=%q message=%q", got, test.remediation, test.message)
			}
		})
	}
}
