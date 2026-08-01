package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/capability"
)

func TestCapabilitiesResourceIsFixedStaticAndDependencyFree(t *testing.T) {
	var dependencyCalls atomic.Int32
	unexpected := func() error {
		dependencyCalls.Add(1)
		return errors.New("dependency must not be read")
	}
	deps := Dependencies{
		Jira:       func() (JiraReader, error) { return nil, unexpected() },
		Confluence: func() (ConfluenceReader, error) { return nil, unexpected() },
		MirrorRoot: func() (string, error) { return "", unexpected() },
	}

	for _, profile := range []ServiceProfile{ServiceDefault, ServiceJira, ServiceConfluence, ServiceOffline} {
		client, closeSessions := connectTestClient(t, NewForService("test", deps, profile))
		listed, err := client.ListResources(context.Background(), nil)
		if err != nil {
			closeSessions()
			t.Fatal(err)
		}
		if len(listed.Resources) != 1 {
			closeSessions()
			t.Fatalf("profile %q resources=%+v", profile, listed.Resources)
		}
		resource := listed.Resources[0]
		if resource.URI != CapabilitiesResourceURI || resource.MIMEType != capabilitiesResourceMIMEType {
			closeSessions()
			t.Fatalf("profile %q resource=%+v", profile, resource)
		}
		result, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: CapabilitiesResourceURI})
		closeSessions()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Contents) != 1 || result.Contents[0].URI != CapabilitiesResourceURI ||
			result.Contents[0].MIMEType != capabilitiesResourceMIMEType || len(result.Contents[0].Blob) != 0 {
			t.Fatalf("profile %q contents=%+v", profile, result.Contents)
		}
		assertCapabilitiesResourceJSON(t, result.Contents[0].Text)
	}
	if dependencyCalls.Load() != 0 {
		t.Fatalf("capability resource read %d dependencies", dependencyCalls.Load())
	}
}

func assertCapabilitiesResourceJSON(t *testing.T, text string) {
	t.Helper()
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &topLevel); err != nil {
		t.Fatal(err)
	}
	topLevelKeys := make([]string, 0, len(topLevel))
	for key := range topLevel {
		topLevelKeys = append(topLevelKeys, key)
	}
	sort.Strings(topLevelKeys)
	if want := []string{"capabilities", "schema_version"}; !reflect.DeepEqual(topLevelKeys, want) {
		t.Fatalf("resource keys=%v want=%v", topLevelKeys, want)
	}
	var document struct {
		SchemaVersion int                      `json:"schema_version"`
		Capabilities  []map[string]interface{} `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		t.Fatal(err)
	}
	definitions := capability.Definitions()
	if document.SchemaVersion != capabilitiesResourceSchema || len(document.Capabilities) != len(definitions) {
		t.Fatalf("resource schema=%d capabilities=%d", document.SchemaVersion, len(document.Capabilities))
	}
	wantBaseKeys := []string{"cli_command", "cli_only", "id", "priority", "role", "service", "task_class"}
	for i, entry := range document.Capabilities {
		definition := definitions[i]
		keys := make([]string, 0, len(entry))
		for key := range entry {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		wantKeys := append([]string(nil), wantBaseKeys...)
		if definition.MCPTool != "" {
			wantKeys = append(wantKeys, "mcp_scope", "mcp_tool")
			sort.Strings(wantKeys)
		}
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Fatalf("capability %q keys=%v want=%v", definition.ID, keys, wantKeys)
		}
		if entry["id"] != definition.ID || entry["task_class"] != definition.TaskClass ||
			entry["service"] != definition.Service || entry["role"] != definition.Role ||
			entry["priority"] != float64(definition.Priority) || entry["cli_command"] != definition.CLICommand ||
			entry["mcp_tool"] != nil && entry["mcp_tool"] != definition.MCPTool ||
			entry["mcp_scope"] != nil && entry["mcp_scope"] != definition.MCPScope ||
			entry["cli_only"] != (definition.MCPTool == "") {
			t.Fatalf("capability %q resource entry=%+v", definition.ID, entry)
		}
	}
}

func TestCapabilityMappingsReconcileWithRegisteredToolInventory(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		registered[tool.Name] = true
	}
	covered := make(map[string]bool, len(registered))
	for _, definition := range capability.Definitions() {
		if definition.MCPTool == "" {
			continue
		}
		if !registered[definition.MCPTool] {
			t.Errorf("capability %q maps unregistered MCP tool %q", definition.ID, definition.MCPTool)
		}
		covered[definition.MCPTool] = true
	}
	for name := range registered {
		if !covered[name] {
			t.Errorf("registered MCP tool %q has no curated capability mapping", name)
		}
	}
	if len(registered) != 23 || len(covered) != len(registered) {
		t.Fatalf("registered=%d covered=%d want=23/23", len(registered), len(covered))
	}
}

func TestCapabilitiesResourceKeepsCommentRoutesClosedAndSchemaV1(t *testing.T) {
	resource := staticCapabilitiesResource()
	if resource.SchemaVersion != 1 {
		t.Fatalf("schema_version=%d want=1", resource.SchemaVersion)
	}
	var got []capabilityResourceEntry
	for _, entry := range resource.Capabilities {
		if entry.TaskClass == "confluence/comments" {
			got = append(got, entry)
		}
	}
	if len(got) != 6 {
		t.Fatalf("comment routes=%d want=6: %+v", len(got), got)
	}
	if got[0].ID != "confluence.comment.list" || got[0].MCPTool != "confluence_comment_list" || got[0].CLIOnly ||
		got[1].ID != "confluence.comment.thread" || got[1].MCPTool != "confluence_comment_thread" || got[1].CLIOnly ||
		got[2].ID != "confluence.comment.preview" || !got[2].CLIOnly || got[2].MCPTool != "" ||
		got[3].ID != "confluence.comment.add" || !got[3].CLIOnly || got[3].MCPTool != "" ||
		got[4].ID != "confluence.comment.mutation.preview" || !got[4].CLIOnly || got[4].MCPTool != "" ||
		got[5].ID != "confluence.comment.mutation.apply" || !got[5].CLIOnly || got[5].MCPTool != "" {
		t.Fatalf("comment routes are not closed: %+v", got)
	}
}
