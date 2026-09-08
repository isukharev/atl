package agenteval

import (
	"encoding/json"
	"fmt"
	"slices"
)

const JiraIssueGraphSourceSelectionSchemaVersion = 1

var jiraGraphWireAllSourceKinds = []string{
	"issue_fields",
	"issue_links",
	"hierarchy",
	"attachments",
	"issue_properties",
	"comments",
	"worklogs",
	"remote_links",
	"development",
}

// JiraIssueGraphSourceSelection is emitted only when callers explicitly select
// graph sources. It records both the normalized partition and the one-request
// Jira issue projection needed by those collectors.
type JiraIssueGraphSourceSelection struct {
	SchemaVersion int                                   `json:"schema_version"`
	Selected      []string                              `json:"selected"`
	Omitted       []string                              `json:"omitted"`
	Snapshot      JiraIssueGraphSourceSelectionSnapshot `json:"snapshot"`
}

type JiraIssueGraphSourceSelectionSnapshot struct {
	Fields                 []string `json:"fields"`
	Properties             bool     `json:"properties"`
	SupportingFieldsReason string   `json:"supporting_fields_reason,omitempty"`
}

func validateJiraGraphSourceSelectionMembers(raw json.RawMessage) error {
	selection, err := jiraGraphWireObject(raw, "graph.source_selection")
	if err != nil {
		return err
	}
	if err := jiraGraphWireMembers(selection, "graph.source_selection",
		[]string{"schema_version", "selected", "omitted", "snapshot"}, nil); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(selection["selected"], "graph.source_selection.selected", nil); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(selection["omitted"], "graph.source_selection.omitted", nil); err != nil {
		return err
	}
	snapshot, err := jiraGraphWireObject(selection["snapshot"], "graph.source_selection.snapshot")
	if err != nil {
		return err
	}
	if err := jiraGraphWireMembers(snapshot, "graph.source_selection.snapshot",
		[]string{"fields", "properties"}, []string{"supporting_fields_reason"}); err != nil {
		return err
	}
	return jiraGraphWireArrayMembers(snapshot["fields"], "graph.source_selection.snapshot.fields", nil)
}

func validateJiraGraphSourceSelection(selection *JiraIssueGraphSourceSelection, includeDevelopment bool) ([]string, error) {
	if selection == nil {
		return jiraGraphWireSourceKinds(includeDevelopment), nil
	}
	if selection.SchemaVersion != JiraIssueGraphSourceSelectionSchemaVersion ||
		selection.Selected == nil || len(selection.Selected) == 0 || selection.Omitted == nil {
		return nil, fmt.Errorf("source selection schema or arrays are invalid")
	}

	selectedSet, err := jiraGraphWireOrderedSourceSet(selection.Selected)
	if err != nil {
		return nil, fmt.Errorf("selected source partition is invalid: %w", err)
	}
	omittedSet, err := jiraGraphWireOrderedSourceSet(selection.Omitted)
	if err != nil {
		return nil, fmt.Errorf("omitted source partition is invalid: %w", err)
	}
	for _, kind := range jiraGraphWireAllSourceKinds {
		if selectedSet[kind] == omittedSet[kind] {
			return nil, fmt.Errorf("source selection does not partition the source catalog")
		}
	}
	if selectedSet["development"] != includeDevelopment {
		return nil, fmt.Errorf("development source selection does not match the bound")
	}

	wantFields := []string{"summary"}
	if selectedSet["issue_fields"] || selectedSet["hierarchy"] {
		wantFields = []string{"*all"}
	} else {
		if selectedSet["issue_links"] {
			wantFields = append(wantFields, "issuelinks")
		}
		if selectedSet["attachments"] {
			wantFields = append(wantFields, "attachment")
		}
	}
	wantReason := ""
	if selectedSet["hierarchy"] && !selectedSet["issue_fields"] {
		wantReason = "hierarchy_discovery"
	}
	if selection.Snapshot.Fields == nil || !slices.Equal(selection.Snapshot.Fields, wantFields) ||
		selection.Snapshot.Properties != selectedSet["issue_properties"] ||
		selection.Snapshot.SupportingFieldsReason != wantReason {
		return nil, fmt.Errorf("source selection snapshot projection is invalid")
	}
	return slices.Clone(selection.Selected), nil
}

func jiraGraphWireOrderedSourceSet(kinds []string) (map[string]bool, error) {
	set := make(map[string]bool, len(kinds))
	previousRank := -1
	for _, kind := range kinds {
		rank := slices.Index(jiraGraphWireAllSourceKinds, kind)
		if rank < 0 || rank <= previousRank {
			return nil, fmt.Errorf("unknown, duplicate, or unordered source %q", kind)
		}
		set[kind] = true
		previousRank = rank
	}
	return set, nil
}

func jiraGraphWireSourceKinds(includeDevelopment bool) []string {
	limit := len(jiraGraphWireAllSourceKinds) - 1
	if includeDevelopment {
		limit++
	}
	return slices.Clone(jiraGraphWireAllSourceKinds[:limit])
}

func validateJiraGraphSelectedSourceInventory(
	nodes []JiraIssueGraphNode,
	sourcesByNode map[string]map[string]bool,
	selected []string,
) error {
	for _, node := range nodes {
		inventory := sourcesByNode[node.ID]
		hasInventory := false
		for _, kind := range selected {
			hasInventory = hasInventory || inventory[kind]
		}
		if !node.Expanded && !hasInventory {
			continue
		}
		for _, kind := range selected {
			if !inventory[kind] {
				return fmt.Errorf("attempted jira node source inventory is incomplete")
			}
		}
	}
	return nil
}
