package agenteval

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
)

const (
	CapabilityCatalogSchemaVersion = 1
	CapabilityCatalogItemCount     = 58
	maxCapabilityCatalogBytes      = 1 << 20
)

const (
	capabilityRoutingMatch         = "exact"
	capabilityRoutingReferenceLoad = "invoke capability.skill, then open capability.reference relative to that skill; do not search the filesystem"
	capabilityRoutingStop          = "stop expanding the route when sufficient complete evidence is available"
)

//go:embed testdata/capability-catalog.v1.json
var pinnedCapabilityCatalogJSON []byte

// CapabilityCatalog is the evaluator-owned schema-v1 projection of
// `atl capabilities -o json`. It is a wire contract, not product routing logic.
type CapabilityCatalog struct {
	SchemaVersion int                        `json:"schema_version"`
	Routing       CapabilityCatalogRouting   `json:"routing"`
	Selection     CapabilityCatalogSelection `json:"selection"`
	Capabilities  []CapabilityCatalogItem    `json:"capabilities"`
}

type CapabilityCatalogRouting struct {
	Match         string `json:"match"`
	ReferenceLoad string `json:"reference_load"`
	Stop          string `json:"stop"`
}

type CapabilityCatalogSelection struct {
	Count int `json:"count"`
}

type CapabilityCatalogItem struct {
	ID           string   `json:"id"`
	TaskClass    string   `json:"task_class"`
	Service      string   `json:"service"`
	Role         string   `json:"role"`
	Priority     int      `json:"priority"`
	Summary      string   `json:"summary"`
	Command      string   `json:"command"`
	CLICommand   string   `json:"cli_command"`
	MCPTool      string   `json:"mcp_tool,omitempty"`
	MCPScope     string   `json:"mcp_scope,omitempty"`
	CLIOnly      bool     `json:"cli_only"`
	Access       string   `json:"access"`
	OutputModes  []string `json:"output_modes"`
	Evidence     string   `json:"evidence"`
	Completeness string   `json:"completeness"`
	Skill        string   `json:"skill"`
	Reference    string   `json:"reference"`
}

var (
	pinnedCapabilityCatalogOnce sync.Once
	pinnedCapabilityCatalog     CapabilityCatalog
	pinnedCapabilityCatalogErr  error
)

func DecodeCapabilityCatalog(r io.Reader) (CapabilityCatalog, error) {
	limited := &io.LimitedReader{R: r, N: maxCapabilityCatalogBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return CapabilityCatalog{}, fmt.Errorf("read capability catalog: %w", err)
	}
	if limited.N <= 0 {
		return CapabilityCatalog{}, fmt.Errorf("capability catalog exceeds %d bytes", maxCapabilityCatalogBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return CapabilityCatalog{}, fmt.Errorf("decode capability catalog: %w", err)
	}
	if err := validateCapabilityCatalogMemberSets(data); err != nil {
		return CapabilityCatalog{}, fmt.Errorf("decode capability catalog: %w", err)
	}

	var catalog CapabilityCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return CapabilityCatalog{}, fmt.Errorf("decode capability catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CapabilityCatalog{}, fmt.Errorf("capability catalog contains trailing JSON data")
	}
	if err := catalog.validate(); err != nil {
		return CapabilityCatalog{}, err
	}
	return catalog, nil
}

// PinnedCapabilityCatalog returns a deep copy so callers cannot mutate the
// evaluator's released schema-v1 compatibility artifact.
func PinnedCapabilityCatalog() (CapabilityCatalog, error) {
	pinnedCapabilityCatalogOnce.Do(func() {
		pinnedCapabilityCatalog, pinnedCapabilityCatalogErr = DecodeCapabilityCatalog(bytes.NewReader(pinnedCapabilityCatalogJSON))
	})
	if pinnedCapabilityCatalogErr != nil {
		return CapabilityCatalog{}, fmt.Errorf("decode pinned capability catalog: %w", pinnedCapabilityCatalogErr)
	}
	return cloneCapabilityCatalog(pinnedCapabilityCatalog), nil
}

// VerifyPinnedCapabilityCatalog requires semantic equality with the released
// evaluator-owned artifact after strict wire decoding.
func VerifyPinnedCapabilityCatalog(catalog CapabilityCatalog) error {
	pinned, err := PinnedCapabilityCatalog()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(catalog, pinned) {
		return fmt.Errorf("capability catalog differs from the pinned schema-v1 artifact at %s", firstCapabilityCatalogDifference(pinned, catalog))
	}
	return nil
}

func firstCapabilityCatalogDifference(want, got CapabilityCatalog) string {
	wantData, _ := json.Marshal(want)
	gotData, _ := json.Marshal(got)
	var wantValue, gotValue any
	_ = json.Unmarshal(wantData, &wantValue)
	_ = json.Unmarshal(gotData, &gotValue)
	return firstJSONDifference("$", wantValue, gotValue)
}

func firstJSONDifference(path string, want, got any) string {
	if reflect.DeepEqual(want, got) {
		return path
	}
	switch wantValue := want.(type) {
	case map[string]any:
		gotValue, ok := got.(map[string]any)
		if !ok {
			return path
		}
		keys := make([]string, 0, len(wantValue))
		for key := range wantValue {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			other, exists := gotValue[key]
			if !exists || !reflect.DeepEqual(wantValue[key], other) {
				return firstJSONDifference(path+"."+key, wantValue[key], other)
			}
		}
	case []any:
		gotValue, ok := got.([]any)
		if !ok || len(wantValue) != len(gotValue) {
			return path
		}
		for index := range wantValue {
			if !reflect.DeepEqual(wantValue[index], gotValue[index]) {
				return firstJSONDifference(fmt.Sprintf("%s[%d]", path, index), wantValue[index], gotValue[index])
			}
		}
	}
	return path
}

func (c CapabilityCatalog) mcpToolsForProfile(profile string) (map[string]bool, bool) {
	if profile != "jira" && profile != "confluence" && profile != "offline" {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, item := range c.Capabilities {
		if item.MCPTool == "" {
			continue
		}
		if profile == "offline" {
			if item.ID == "jira.mirror.snapshot" || item.ID == "confluence.mirror.snapshot" {
				allowed[item.MCPTool] = true
			}
			continue
		}
		if item.Service == profile {
			allowed[item.MCPTool] = true
		}
	}
	return allowed, true
}

func (c CapabilityCatalog) validate() error {
	if c.SchemaVersion != CapabilityCatalogSchemaVersion {
		return fmt.Errorf("unsupported capability catalog schema_version %d", c.SchemaVersion)
	}
	if c.Routing.Match != capabilityRoutingMatch || c.Routing.ReferenceLoad != capabilityRoutingReferenceLoad || c.Routing.Stop != capabilityRoutingStop {
		return fmt.Errorf("capability catalog routing contract is incomplete or unsupported")
	}
	if c.Selection.Count != CapabilityCatalogItemCount || len(c.Capabilities) != CapabilityCatalogItemCount || c.Selection.Count != len(c.Capabilities) {
		return fmt.Errorf("capability catalog cardinality=%d/%d, want %d", c.Selection.Count, len(c.Capabilities), CapabilityCatalogItemCount)
	}

	seenIDs := make(map[string]struct{}, len(c.Capabilities))
	for index, item := range c.Capabilities {
		if err := item.validate(); err != nil {
			return fmt.Errorf("capability[%d]: %w", index, err)
		}
		if _, exists := seenIDs[item.ID]; exists {
			return fmt.Errorf("capability[%d]: duplicate id %q", index, item.ID)
		}
		seenIDs[item.ID] = struct{}{}
		if index > 0 && !capabilityCatalogItemLess(c.Capabilities[index-1], item) {
			return fmt.Errorf("capability[%d]: catalog order is not task_class, priority, id", index)
		}
	}
	return nil
}

func validateCapabilityCatalogMemberSets(data []byte) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	if err := requireExactJSONMembers(document, "catalog", []string{"schema_version", "routing", "selection", "capabilities"}); err != nil {
		return err
	}
	var routing map[string]json.RawMessage
	if err := json.Unmarshal(document["routing"], &routing); err != nil {
		return fmt.Errorf("routing: %w", err)
	}
	if err := requireExactJSONMembers(routing, "routing", []string{"match", "reference_load", "stop"}); err != nil {
		return err
	}
	var selection map[string]json.RawMessage
	if err := json.Unmarshal(document["selection"], &selection); err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	if err := requireExactJSONMembers(selection, "selection", []string{"count"}); err != nil {
		return err
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(document["capabilities"], &items); err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	baseMembers := []string{
		"id", "task_class", "service", "role", "priority", "summary", "command", "cli_command",
		"cli_only", "access", "output_modes", "evidence", "completeness", "skill", "reference",
	}
	for index, item := range items {
		members := baseMembers
		var cliOnly bool
		if err := json.Unmarshal(item["cli_only"], &cliOnly); err != nil {
			return fmt.Errorf("capability[%d] cli_only: %w", index, err)
		}
		if !cliOnly {
			members = append(slices.Clone(baseMembers), "mcp_tool", "mcp_scope")
		}
		if err := requireExactJSONMembers(item, fmt.Sprintf("capability[%d]", index), members); err != nil {
			return err
		}
	}
	return nil
}

func requireExactJSONMembers(document map[string]json.RawMessage, owner string, members []string) error {
	expected := make(map[string]struct{}, len(members))
	for _, member := range members {
		expected[member] = struct{}{}
		if _, ok := document[member]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, member)
		}
	}
	for member := range document {
		if _, ok := expected[member]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, member)
		}
	}
	return nil
}

func (i CapabilityCatalogItem) validate() error {
	stringsToValidate := []struct {
		name  string
		value string
	}{
		{"id", i.ID}, {"task_class", i.TaskClass}, {"service", i.Service}, {"role", i.Role},
		{"summary", i.Summary}, {"command", i.Command}, {"cli_command", i.CLICommand},
		{"access", i.Access}, {"evidence", i.Evidence}, {"completeness", i.Completeness},
		{"skill", i.Skill}, {"reference", i.Reference},
	}
	for _, field := range stringsToValidate {
		if field.value == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%s must be non-empty and whitespace-normalized", field.name)
		}
	}
	if i.Service != "jira" && i.Service != "confluence" {
		return fmt.Errorf("service %q is unsupported", i.Service)
	}
	if i.Priority <= 0 {
		return fmt.Errorf("priority must be positive")
	}
	if i.Command != i.CLICommand {
		return fmt.Errorf("command and cli_command differ")
	}
	if i.Access != "read-only" && i.Access != "mutating" {
		return fmt.Errorf("access %q is unsupported", i.Access)
	}
	if i.CLIOnly != (i.MCPTool == "") {
		return fmt.Errorf("cli_only and mcp_tool disagree")
	}
	if (i.MCPTool == "") != (i.MCPScope == "") {
		return fmt.Errorf("mcp_tool and mcp_scope presence disagree")
	}
	if i.MCPTool != "" && !mcpToolNameRE.MatchString(i.MCPTool) {
		return fmt.Errorf("mcp_tool %q is malformed", i.MCPTool)
	}
	if !validCapabilityOutputModes(i.OutputModes) {
		return fmt.Errorf("output_modes %v are malformed or reordered", i.OutputModes)
	}
	return nil
}

func capabilityCatalogItemLess(left, right CapabilityCatalogItem) bool {
	if left.TaskClass != right.TaskClass {
		return left.TaskClass < right.TaskClass
	}
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	return left.ID < right.ID
}

func validCapabilityOutputModes(modes []string) bool {
	for _, allowed := range [][]string{{"json"}, {"json", "text"}, {"json", "text", "id"}} {
		if slices.Equal(modes, allowed) {
			return true
		}
	}
	return false
}

func cloneCapabilityCatalog(catalog CapabilityCatalog) CapabilityCatalog {
	clone := catalog
	clone.Capabilities = slices.Clone(catalog.Capabilities)
	for index := range clone.Capabilities {
		clone.Capabilities[index].OutputModes = slices.Clone(catalog.Capabilities[index].OutputModes)
	}
	return clone
}
