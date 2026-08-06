package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
)

const jiraPortfolioCapabilityCount = 7

var jiraPortfolioCapabilityIDs = []string{
	"jira.board.list",
	"jira.board.view",
	"jira.structure.get",
	"jira.structure.folders",
	"jira.structure.view",
	"jira.portfolio.epic.digest",
	"jira.portfolio.confluence.section",
}

// JiraPortfolioCapabilityCatalog is the evaluator-owned projection of the
// exact released `capabilities --task jira/portfolio` response. The full
// capability item stays closed so routing drift cannot hide behind a minimal
// selection-only assertion.
type JiraPortfolioCapabilityCatalog struct {
	SchemaVersion int                              `json:"schema_version"`
	Routing       CapabilityCatalogRouting         `json:"routing"`
	Selection     JiraPortfolioCapabilitySelection `json:"selection"`
	Capabilities  []CapabilityCatalogItem          `json:"capabilities"`
}

type JiraPortfolioCapabilitySelection struct {
	Task  string `json:"task"`
	Count int    `json:"count"`
}

func DecodeJiraPortfolioCapabilityCatalog(r io.Reader) (JiraPortfolioCapabilityCatalog, error) {
	var catalog JiraPortfolioCapabilityCatalog
	if err := decodeJiraWorkflowWire(
		r,
		maxCapabilityCatalogBytes,
		"Jira portfolio capability catalog",
		&catalog,
		validateJiraPortfolioCapabilityMembers,
	); err != nil {
		return JiraPortfolioCapabilityCatalog{}, err
	}
	if err := catalog.validate(); err != nil {
		return JiraPortfolioCapabilityCatalog{}, fmt.Errorf("validate Jira portfolio capability catalog: %w", err)
	}
	return catalog, nil
}

func validateJiraPortfolioCapabilityMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira portfolio capability catalog")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "Jira portfolio capability catalog", []string{
		"schema_version", "routing", "selection", "capabilities",
	}, nil); err != nil {
		return err
	}
	routing, err := jiraWorkflowNestedObject(root["routing"], "Jira portfolio capability catalog.routing")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(routing, "Jira portfolio capability catalog.routing", []string{
		"match", "reference_load", "stop",
	}, nil); err != nil {
		return err
	}
	selection, err := jiraWorkflowNestedObject(root["selection"], "Jira portfolio capability catalog.selection")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(selection, "Jira portfolio capability catalog.selection", []string{"task", "count"}, nil); err != nil {
		return err
	}
	return jiraWorkflowArray(root["capabilities"], "Jira portfolio capability catalog.capabilities", validateJiraPortfolioCapabilityItemMembers)
}

func validateJiraPortfolioCapabilityItemMembers(item map[string]json.RawMessage, owner string) error {
	base := []string{
		"id", "task_class", "service", "role", "priority", "summary", "command", "cli_command",
		"cli_only", "access", "output_modes", "evidence", "completeness", "skill", "reference",
	}
	var cliOnly bool
	if raw, ok := item["cli_only"]; !ok || jiraWorkflowNull(raw) || json.Unmarshal(raw, &cliOnly) != nil {
		return fmt.Errorf("%s.cli_only must be a non-null boolean", owner)
	}
	required := base
	if !cliOnly {
		required = append(slices.Clone(base), "mcp_tool", "mcp_scope")
	}
	if err := jiraWorkflowMembers(item, owner, required, nil); err != nil {
		return err
	}
	return jiraWorkflowArray(item["output_modes"], owner+".output_modes", nil)
}

func (catalog JiraPortfolioCapabilityCatalog) validate() error {
	if catalog.SchemaVersion != CapabilityCatalogSchemaVersion {
		return fmt.Errorf("schema_version must be %d", CapabilityCatalogSchemaVersion)
	}
	if catalog.Routing.Match != capabilityRoutingMatch ||
		catalog.Routing.ReferenceLoad != capabilityRoutingReferenceLoad ||
		catalog.Routing.Stop != capabilityRoutingStop {
		return fmt.Errorf("routing contract is incomplete or unsupported")
	}
	if catalog.Selection.Task != "jira/portfolio" ||
		catalog.Selection.Count != jiraPortfolioCapabilityCount ||
		len(catalog.Capabilities) != jiraPortfolioCapabilityCount {
		return fmt.Errorf("selection is not the exact Jira portfolio capability set")
	}
	seen := make(map[string]struct{}, len(catalog.Capabilities))
	for index, item := range catalog.Capabilities {
		if err := item.validate(); err != nil {
			return fmt.Errorf("capabilities[%d]: %w", index, err)
		}
		if item.TaskClass != catalog.Selection.Task || item.ID != jiraPortfolioCapabilityIDs[index] {
			return fmt.Errorf("capabilities[%d] identity or ordering drifted", index)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("capabilities[%d].id duplicates an earlier capability", index)
		}
		seen[item.ID] = struct{}{}
	}
	pinned, err := PinnedCapabilityCatalog()
	if err != nil {
		return err
	}
	expected := make([]CapabilityCatalogItem, 0, jiraPortfolioCapabilityCount)
	for _, item := range pinned.Capabilities {
		if item.TaskClass == catalog.Selection.Task {
			expected = append(expected, item)
		}
	}
	if !reflect.DeepEqual(catalog.Capabilities, expected) {
		return fmt.Errorf("capability entries differ from the pinned released catalog")
	}
	return nil
}
