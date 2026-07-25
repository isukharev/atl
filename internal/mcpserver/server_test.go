package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/agenteval"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
	"github.com/isukharev/atl/internal/mirror"
)

func TestServerAdvertisesOnlyTypedReadOnlyTools(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()

	initialized := client.InitializeResult()
	if initialized == nil || initialized.Instructions != Instructions || initialized.ServerInfo.Name != "atl" {
		t.Fatalf("initialize=%+v", initialized)
	}
	if !strings.Contains(initialized.Instructions, "columns (preferred), fields, or projection") {
		t.Fatalf("initialize instructions do not disambiguate Jira search field selection: %q", initialized.Instructions)
	}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"confluence_attachment_list", "confluence_mirror_snapshot",
		"confluence_page_outline", "confluence_page_resolve", "confluence_page_section", "confluence_search",
		"confluence_table_extract", "confluence_table_summary",
		"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_history",
		"jira_issue_search", "jira_mirror_snapshot", "jira_structure_get", "jira_structure_view",
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
		if tool.Name == "confluence_page_section" {
			properties, _ := input["properties"].(map[string]any)
			heading, _ := properties["heading"].(map[string]any)
			description, _ := heading["description"].(string)
			if !strings.Contains(description, "without a Markdown # prefix") {
				t.Errorf("tool %s heading guidance is ambiguous: %#v", tool.Name, heading)
			}
		}
		// Both bounded page reads can return a partial result, so each advertises
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
		if tool.Name == "confluence_table_summary" && !schemaRequired(input, "reference") {
			t.Errorf("tool %s must require reference: %#v", tool.Name, tool.InputSchema)
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
			output, _ := tool.OutputSchema.(map[string]any)
			for _, required := range []string{"schema_version", "structure", "projection", "rows", "row_count", "issue_count", "complete", "inaccessible_rows", "warnings"} {
				if !schemaRequired(output, required) {
					t.Errorf("tool %s output must require %s: %#v", tool.Name, required, tool.OutputSchema)
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
	inventory, err := agenteval.ValidateBenchmarkCorpus(filepath.Join("..", "..", "benchmarks", "agent-eval"))
	if err != nil {
		t.Fatal(err)
	}
	covered := make([]string, len(inventory.MCPTools))
	for index, tool := range inventory.MCPTools {
		covered[index] = tool.Tool
	}
	// Every advertised read-only tool must carry exact model-in-the-loop
	// benchmark coverage, and a benchmark that names an unregistered tool must
	// fail: the two sets are compared for equality with no exceptions.
	if !slices.Equal(covered, want) {
		t.Fatalf("advertised tools lack exact benchmark coverage: covered=%v want=%v", covered, want)
	}
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
		if err == nil || result != nil || strings.Contains(err.Error(), root) {
			t.Fatalf("model-controlled mirror arguments were not rejected safely: args=%v result=%+v err=%v", args, result, err)
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
		"structure_id": 9, "fields": []string{"key", "summary"}, "folder_id": "folder-a", "max_rows": 10, "max_bytes": 4096,
	})
	viewContent, ok := view.StructuredContent.(map[string]any)
	if !ok || viewContent["row_count"] != float64(2) || viewContent["complete"] != true {
		t.Fatalf("Structure view=%#v", view.StructuredContent)
	}
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
		j.structureOpts.FolderID != "folder-a" || strings.Join(j.structureOpts.Attributes, ",") != "key,summary" {
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
	if err == nil {
		t.Fatalf("unknown input succeeded: %+v", result)
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
	if !errors.As(safeUsage, &got) || got.Message != "usage error: max_rows must be at least 1" {
		t.Fatalf("safe usage guidance was not preserved: %v", safeUsage)
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
	fixture := agenteval.MockFixture{
		SchemaVersion: agenteval.MockFixtureSchemaVersion,
		JiraContext:   "/jira", ConfluenceContext: "/wiki",
		Routes: []agenteval.MockRoute{{
			Method: "GET", Path: "/jira/rest/api/2/search",
			QueryEquals: map[string]string{
				"jql":     "project = PROJ ORDER BY key",
				"startAt": "0", "maxResults": "250", "fields": "summary,status",
			},
			Status: 200,
			Body:   json.RawMessage(`{"startAt":0,"maxResults":250,"total":0,"issues":[]}`),
		}},
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
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"comments"}, "comment_limit": 51}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"confluence"}}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "projection": "brief"}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "max_bytes": 1023}},
		{name: "jira_epic_digest", args: map[string]any{"key": "PROJ-1", "include": []string{"identity"}, "max_bytes": 1048577}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "limit": 101}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "max_bytes": 1023}},
		{name: "confluence_search", args: map[string]any{"cql": "space=DOCS", "max_bytes": 1048577}},
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
	fixture := agenteval.MockFixture{
		SchemaVersion: agenteval.MockFixtureSchemaVersion,
		JiraContext:   "/jira", ConfluenceContext: "/wiki",
		Routes: []agenteval.MockRoute{{
			Method: http.MethodGet, Path: "/wiki/rest/api/search",
			QueryEquals: map[string]string{"cql": `siteSearch ~ "bounded topic"`},
			Status:      http.StatusOK, Body: body,
		}},
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
	for _, mode := range []string{"row-count", "selection", "wrong-root", "second-root", "wrong-path", "projection", "completeness"} {
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
		{name: "extract summary", tool: "confluence_table_extract", args: map[string]any{"reference": "42", "table": 1}, mode: "extract-summary"},
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

// attachmentInventoryClient wires one recording reader into a live session.
func attachmentInventoryClient(t *testing.T, reader *recordingConfluenceReader) *mcp.ClientSession {
	t.Helper()
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	return client
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
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "confluence_attachment_list", Arguments: map[string]any{"reference": "42"},
	}); err == nil {
		t.Fatal("an omitted expected_page_version must be rejected by the input schema")
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
				ID: "42", Count: 1000, Total: 1001, Complete: false, Truncated: true,
				PartialReason: "heading_limit", OriginalBytes: 90_000, EmittedBytes: 89_000,
				Headings: []app.ConfluenceOutlineEntry{},
			}},
		},
		{
			name: "section max bytes", tool: "confluence_page_section", reason: "max_bytes",
			args: map[string]any{"reference": "42", "heading": "Overview", "max_bytes": 4096},
			reader: &recordingConfluenceReader{sectionResult: &app.ConfluencePageSectionResult{
				ID: "42", Heading: "Overview", Occurrence: 1, Path: []string{"Overview"},
				Markdown: "# Overview\n\n[... truncated by atl ...]\n", Complete: false, Truncated: true,
				PartialReason: "max_bytes", OriginalBytes: 14_000, EmittedBytes: 4_000,
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
			ID: "42", Count: 1, Total: 1, Complete: true, OriginalBytes: 64, EmittedBytes: 64,
			Headings: []app.ConfluenceOutlineEntry{{Index: 1, Level: 1, Title: "Overview", Path: []string{"Overview"}, Occurrence: 1}},
		},
		sectionResult: &app.ConfluencePageSectionResult{
			ID: "42", Heading: "Overview", Occurrence: 1, Path: []string{"Overview"},
			Markdown: "# Overview\n", Complete: true, OriginalBytes: 11, EmittedBytes: 11,
		},
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	defer closeSessions()
	for tool, args := range map[string]map[string]any{
		"confluence_page_outline": {"reference": "42"},
		"confluence_page_section": {"reference": "42", "heading": "Overview"},
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
			args:    map[string]any{"reference": "42", "heading": heading},
			message: "Confluence heading selection is ambiguous; available occurrence count is 3, so select an occurrence from 1 to 3",
		},
		{
			name: "out of range", kind: "not_found", err: stale, requested: 5, available: 2,
			args:    map[string]any{"reference": "42", "heading": heading, "occurrence": 5},
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
				Arguments: map[string]any{"reference": "42", "heading": "Heading " + marker},
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
	searchJQL, searchView, searchCursor string
	searchColumns                       []string
	searchLimit                         int
	historyKey                          string
	historyOpts                         app.JiraHistoryOpts
	historyErr                          error
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
	return &app.StructureSnapshot{
		SchemaVersion: 1, Structure: app.StructureSnapshotMetadata{ID: id, Name: "Synthetic Structure"},
		Projection: app.StructureProjection{Kind: "jira-fields-v1", Source: "explicit", Attributes: append([]string(nil), opts.Attributes...)},
		Rows:       rows,
		RowCount:   len(rows), IssueCount: 1, Complete: true, InaccessibleRows: []int64{}, Selection: selection, Warnings: []string{},
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
	}
	return result, err
}

type recordingConfluenceReader struct {
	searchCQL, searchCursor                              string
	searchLimit                                          int
	searchText                                           string
	resolveReference, outlineReference, sectionReference string
	sectionOpts                                          app.ConfluencePageSectionOpts
	tableSummaryReference, tableExtractReference         string
	tableSummaryIndex, tableExtractIndex                 int
	tableText                                            string
	tableErr                                             error
	sectionErr                                           error
	outlineResult                                        *app.ConfluencePageOutlineResult
	sectionResult                                        *app.ConfluencePageSectionResult
	attachmentReference                                  string
	attachmentOpts                                       app.ConfluenceAttachmentInventoryOpts
	attachmentResult                                     *app.ConfluenceAttachmentInventoryResult
	attachmentErr                                        error
	attachmentCalls                                      int
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

func (r *recordingConfluenceReader) PageOutline(_ context.Context, reference string) (*app.ConfluencePageOutlineResult, error) {
	r.outlineReference = reference
	if r.outlineResult != nil {
		return r.outlineResult, nil
	}
	return &app.ConfluencePageOutlineResult{Headings: []app.ConfluenceOutlineEntry{}}, nil
}

func (r *recordingConfluenceReader) PageSection(_ context.Context, reference string, opts app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error) {
	r.sectionReference, r.sectionOpts = reference, opts
	if r.sectionErr != nil {
		return nil, r.sectionErr
	}
	if r.sectionResult != nil {
		return r.sectionResult, nil
	}
	return &app.ConfluencePageSectionResult{Path: []string{}}, nil
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
	case "extract-summary":
		result.Tables[0].Summary.ExpandedCellCount++
	}
	return result, err
}

func (r *recordingConfluenceReader) ExtractTables(_ context.Context, reference string, table int) (*app.ConfluenceTableExtract, error) {
	r.tableExtractReference, r.tableExtractIndex = reference, table
	if r.tableErr != nil {
		return nil, r.tableErr
	}
	result := &app.ConfluenceTableExtract{PageID: "42", TableCount: 2, Table: table, Tables: []app.ConfluenceTable{{Index: table,
		RowCount: 1, ColumnCount: 1, Rows: []app.ConfluenceTableRow{{Index: 1,
			Cells: []app.ConfluenceTableCell{{Row: 1, Column: 1, Text: r.tableText}}}}}}}
	summary := app.SummarizeConfluenceTables(result)
	result.Tables[0].Summary = summary.Tables[0]
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

func (r *cancellingJiraReader) HistoryFiltered(ctx context.Context, _ string, _ app.JiraHistoryOpts) (*app.JiraHistoryResult, error) {
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

func (*cancellingConfluenceReader) AttachmentInventory(context.Context, string, app.ConfluenceAttachmentInventoryOpts) (*app.ConfluenceAttachmentInventoryResult, error) {
	panic("unexpected call")
}
