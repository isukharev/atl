package agenteval

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPinnedCapabilityCatalogIsStrictAndImmutable(t *testing.T) {
	catalog, err := DecodeCapabilityCatalog(bytes.NewReader(pinnedCapabilityCatalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPinnedCapabilityCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != CapabilityCatalogSchemaVersion || len(catalog.Capabilities) != CapabilityCatalogItemCount {
		t.Fatalf("catalog=%+v", catalog)
	}

	first := mustPinnedCapabilityCatalog(t)
	first.Capabilities[0].ID = "mutated"
	first.Capabilities[0].OutputModes[0] = "mutated"
	second := mustPinnedCapabilityCatalog(t)
	if second.Capabilities[0].ID == "mutated" || second.Capabilities[0].OutputModes[0] == "mutated" {
		t.Fatal("caller mutation changed the pinned capability catalog")
	}
}

func TestDecodeCapabilityCatalogFailsClosed(t *testing.T) {
	valid := mustPinnedCapabilityCatalog(t)
	validJSON := slices.Clone(pinnedCapabilityCatalogJSON)

	mutations := map[string][]byte{
		"unknown member":        bytes.Replace(validJSON, []byte(`"schema_version": 1,`), []byte(`"unknown": true, "schema_version": 1,`), 1),
		"missing member":        deleteCapabilityCatalogMember(t, validJSON, "routing", "stop"),
		"duplicate JSON member": bytes.Replace(validJSON, []byte(`"schema_version": 1,`), []byte(`"schema_version": 1, "schema_version": 1,`), 1),
		"trailing value":        append(slices.Clone(validJSON), []byte(` {}`)...),
	}

	duplicate := cloneCapabilityCatalog(valid)
	duplicate.Capabilities[1].ID = duplicate.Capabilities[0].ID
	mutations["duplicate capability"] = marshalCapabilityCatalog(t, duplicate)

	reordered := cloneCapabilityCatalog(valid)
	reordered.Capabilities[0], reordered.Capabilities[1] = reordered.Capabilities[1], reordered.Capabilities[0]
	mutations["reordered capability"] = marshalCapabilityCatalog(t, reordered)

	short := cloneCapabilityCatalog(valid)
	short.Capabilities = short.Capabilities[:len(short.Capabilities)-1]
	short.Selection.Count = len(short.Capabilities)
	mutations["short cardinality"] = marshalCapabilityCatalog(t, short)

	wrongCount := cloneCapabilityCatalog(valid)
	wrongCount.Selection.Count--
	mutations["selection cardinality"] = marshalCapabilityCatalog(t, wrongCount)

	malformed := cloneCapabilityCatalog(valid)
	malformed.Capabilities[0].Service = "other"
	mutations["malformed service"] = marshalCapabilityCatalog(t, malformed)

	missingBoolean := deleteCapabilityItemMember(t, validJSON, 0, "cli_only")
	mutations["missing false boolean"] = missingBoolean
	mutations["missing effect profile"] = deleteCapabilityItemMember(t, validJSON, 0, "effect_profile")

	reorderedModes := cloneCapabilityCatalog(valid)
	reorderedModes.Capabilities[0].OutputModes = []string{"text", "json"}
	mutations["reordered output modes"] = marshalCapabilityCatalog(t, reorderedModes)

	for name, data := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCapabilityCatalog(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid capability catalog passed")
			}
		})
	}

	oversized := bytes.Repeat([]byte(" "), maxCapabilityCatalogBytes+1)
	if _, err := DecodeCapabilityCatalog(bytes.NewReader(oversized)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized capability catalog: %v", err)
	}
}

func TestVerifyPinnedCapabilityCatalogChecksEveryWireField(t *testing.T) {
	base := mustPinnedCapabilityCatalog(t)
	mutations := map[string]func(*CapabilityCatalog){
		"routing":   func(c *CapabilityCatalog) { c.Routing.Stop += "." },
		"selection": func(c *CapabilityCatalog) { c.Selection.Count-- },
		"id":        func(c *CapabilityCatalog) { c.Capabilities[0].ID += ".changed" },
		"task":      func(c *CapabilityCatalog) { c.Capabilities[0].TaskClass += ".changed" },
		"service":   func(c *CapabilityCatalog) { c.Capabilities[0].Service = "jira" },
		"role":      func(c *CapabilityCatalog) { c.Capabilities[0].Role += ".changed" },
		"priority":  func(c *CapabilityCatalog) { c.Capabilities[0].Priority++ },
		"summary":   func(c *CapabilityCatalog) { c.Capabilities[0].Summary += "." },
		"command": func(c *CapabilityCatalog) {
			c.Capabilities[0].Command += " changed"
			c.Capabilities[0].CLICommand += " changed"
		},
		"mcp tool":       func(c *CapabilityCatalog) { c.Capabilities[0].MCPTool += "_changed" },
		"mcp scope":      func(c *CapabilityCatalog) { c.Capabilities[0].MCPScope += "." },
		"cli only":       func(c *CapabilityCatalog) { c.Capabilities[2].CLIOnly = false },
		"access":         func(c *CapabilityCatalog) { c.Capabilities[2].Access = "mutating" },
		"effect profile": func(c *CapabilityCatalog) { c.Capabilities[0].EffectProfile += "-changed" },
		"output modes":   func(c *CapabilityCatalog) { c.Capabilities[0].OutputModes = []string{"json"} },
		"evidence":       func(c *CapabilityCatalog) { c.Capabilities[0].Evidence += ".changed" },
		"completeness":   func(c *CapabilityCatalog) { c.Capabilities[0].Completeness += ".changed" },
		"skill":          func(c *CapabilityCatalog) { c.Capabilities[0].Skill += ".changed" },
		"reference":      func(c *CapabilityCatalog) { c.Capabilities[0].Reference += ".changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			catalog := cloneCapabilityCatalog(base)
			mutate(&catalog)
			if reflect.DeepEqual(catalog, base) || VerifyPinnedCapabilityCatalog(catalog) == nil {
				t.Fatal("wire-field drift matched the pinned catalog")
			}
		})
	}
}

func TestVerifyPinnedCapabilityCatalogNamesFirstDifferingField(t *testing.T) {
	catalog := mustPinnedCapabilityCatalog(t)
	catalog.Capabilities[0].Summary += ".changed"
	err := VerifyPinnedCapabilityCatalog(catalog)
	if err == nil || !strings.Contains(err.Error(), "$.capabilities[0].summary") {
		t.Fatalf("catalog difference error=%v, want first field path", err)
	}
}

func TestCapabilityCatalogMinimalProjectionPreservesProfiles(t *testing.T) {
	catalog := mustPinnedCapabilityCatalog(t)
	want := map[string][]string{
		"jira": {
			"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_graph",
			"jira_issue_history", "jira_issue_refs", "jira_issue_search", "jira_mirror_snapshot", "jira_structure_get", "jira_structure_view",
		},
		"confluence": {
			"confluence_attachment_list", "confluence_comment_list", "confluence_comment_thread", "confluence_mirror_snapshot", "confluence_page_meta", "confluence_page_outline",
			"confluence_page_resolve", "confluence_page_section", "confluence_page_sections", "confluence_search", "confluence_table_extract", "confluence_table_summary",
		},
		"offline": {"confluence_mirror_snapshot", "jira_mirror_snapshot"},
	}
	for profile, expected := range want {
		tools, ok := catalog.mcpToolsForProfile(profile)
		actual := make([]string, 0, len(tools))
		for tool := range tools {
			actual = append(actual, tool)
		}
		slices.Sort(actual)
		if !ok || !slices.Equal(actual, expected) {
			t.Fatalf("%s tools=%v want=%v", profile, actual, expected)
		}
	}
	for _, profile := range []string{"default", "all"} {
		if _, ok := catalog.mcpToolsForProfile(profile); ok {
			t.Fatalf("non-explicit profile %q passed", profile)
		}
	}
}

func marshalCapabilityCatalog(t *testing.T, catalog CapabilityCatalog) []byte {
	t.Helper()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func deleteCapabilityCatalogMember(t *testing.T, data []byte, object, member string) []byte {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(document[object], &nested); err != nil {
		t.Fatal(err)
	}
	delete(nested, member)
	document[object] = mustMarshalCapabilityCatalogValue(t, nested)
	return mustMarshalCapabilityCatalogValue(t, document)
}

func deleteCapabilityItemMember(t *testing.T, data []byte, index int, member string) []byte {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(document["capabilities"], &items); err != nil {
		t.Fatal(err)
	}
	delete(items[index], member)
	document["capabilities"] = mustMarshalCapabilityCatalogValue(t, items)
	return mustMarshalCapabilityCatalogValue(t, document)
}

func mustMarshalCapabilityCatalogValue(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
