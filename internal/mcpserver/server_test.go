package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		"confluence_mirror_snapshot",
		"confluence_page_outline", "confluence_page_resolve", "confluence_page_section", "confluence_search",
		"confluence_table_extract", "confluence_table_summary",
		"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_search",
		"jira_mirror_snapshot", "jira_structure_get", "jira_structure_view",
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
		"board_id": 7, "scope": "backlog", "columns": []string{"key"}, "view": "compact", "jql": "labels=x",
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
				got.Kind != "check_failed" || got.Remediation != "review_failed_check" ||
				!strings.Contains(got.Message, "exceeds max_bytes") {
				t.Fatalf("classified error=%+v decode=%v", got, err)
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
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil || got.Kind != "check_failed" {
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
	structureID, structureViewID        int64
	structureOpts                       app.StructureSnapshotOpts
	structureText                       string
	structureName                       string
}

type invalidStructureReader struct {
	*recordingJiraReader
	mode string
}

type oversizedJiraReader struct {
	*recordingJiraReader
	payload string
}

func (r *oversizedJiraReader) FieldCatalog(_ context.Context, _ app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error) {
	return &app.JiraFieldCatalogResult{
		SchemaVersion: 1, Source: "test", Complete: true,
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
