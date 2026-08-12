package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestServerAdvertisesOnlyTypedReadOnlyTools(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()

	initialized := client.InitializeResult()
	if initialized == nil || initialized.Instructions != Instructions || initialized.ServerInfo.Name != "atl" {
		t.Fatalf("initialize=%+v", initialized)
	}
	if initialized.ProtocolVersion != modernMCPProtocolVersion {
		t.Fatalf("protocol version=%q want %s", initialized.ProtocolVersion, modernMCPProtocolVersion)
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
		"confluence_attachment_list", "confluence_comment_list", "confluence_comment_thread", "confluence_mirror_snapshot",
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
		if tool.Name == "confluence_comment_list" || tool.Name == "confluence_comment_thread" {
			properties, _ := input["properties"].(map[string]any)
			if !schemaRequired(input, "page_id") || schemaRequired(input, "expected_page_version") {
				t.Errorf("tool %s must require page_id and keep its provenance gate optional: %#v", tool.Name, tool.InputSchema)
			}
			if _, exists := properties["max_comment_pages"]; exists {
				t.Errorf("tool %s must keep its request cap server-controlled: %#v", tool.Name, tool.InputSchema)
			}
			for _, expected := range []string{"max_items", "max_bytes"} {
				if _, exists := properties[expected]; !exists {
					t.Errorf("tool %s input must expose %s: %#v", tool.Name, expected, tool.InputSchema)
				}
			}
			if tool.Name == "confluence_comment_thread" && !schemaRequired(input, "comment_id") {
				t.Errorf("tool %s must require one exact comment_id: %#v", tool.Name, tool.InputSchema)
			}
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "page_id", "page_version", "page_version_gated", "query", "bounds", "complete", "comments_complete", "threads_complete", "anchors_complete", "count", "root_count", "partial_reasons", "capabilities", "comments", "diagnostics"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
				}
			}
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{`"body_storage"`, `"original_selection"`, `"observed_selection"`, `"url"`, `"email"`, `"page_title"`} {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Errorf("tool %s output schema advertises %s: %s", tool.Name, forbidden, encoded)
				}
			}
			bodyToken := `"body_text"`
			if tool.Name == "confluence_comment_list" && bytes.Contains(encoded, []byte(bodyToken)) {
				t.Errorf("tool %s must be body-free: %s", tool.Name, encoded)
			}
			if tool.Name == "confluence_comment_thread" && !bytes.Contains(encoded, []byte(bodyToken)) {
				t.Errorf("tool %s must expose only plain body_text: %s", tool.Name, encoded)
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
