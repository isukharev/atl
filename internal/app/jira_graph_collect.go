package app

import (
	"context"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func (b *jiraGraphBuilder) collectSnapshotText(snapshot *domain.QualifiedIssueSnapshot, jiraBase, confluenceBase string) {
	if b.sources["issue_fields"] != nil {
		b.collectSnapshotFields(snapshot, jiraBase, confluenceBase)
	}
	if b.sources["issue_properties"] != nil {
		b.collectSnapshotProperties(snapshot, jiraBase, confluenceBase)
	}
}

func (b *jiraGraphBuilder) collectSnapshotFields(snapshot *domain.QualifiedIssueSnapshot, jiraBase, confluenceBase string) {
	fieldsSource := b.sources["issue_fields"]
	fieldsSource.Count = len(snapshot.Fields)
	fieldsBudget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	fieldIDs := graphSortedSourceKeys(snapshot.Fields, fieldsBudget, graphWalkMaxFields)
	type fieldInspection struct {
		id        string
		allowBare bool
	}
	inspections := make([]fieldInspection, 0, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		if graphSkippedPathKeys[strings.ToLower(fieldID)] {
			continue
		}
		switch fieldID {
		case "issuelinks", "parent", "subtasks", "attachment", "comment", "worklog":
			continue
		}
		if !graphValueMayContainReferences(snapshot.Fields[fieldID]) {
			continue
		}
		schema, schemaPresent := snapshot.Schema[fieldID]
		if !schemaPresent {
			b.markMalformed(fieldsSource)
			continue
		}
		custom := graphCustomFieldIDPattern.MatchString(fieldID)
		schema, schemaValid := graphNormalizeFieldSchema(schema, custom)
		if !schemaValid {
			b.markMalformed(fieldsSource)
			continue
		}
		knownSystem := graphKnownSystemFieldID(fieldID) || schema.System != "" && strings.EqualFold(schema.System, fieldID)
		if custom && schema.System != "" || knownSystem && schema.Custom != "" {
			b.markMalformed(fieldsSource)
			continue
		}
		if !custom && !knownSystem {
			b.markMalformed(fieldsSource)
		}
		if knownSystem && schema.System != "" && !strings.EqualFold(schema.System, fieldID) {
			b.markMalformed(fieldsSource)
			continue
		}
		if graphSchemaIsIdentity(schema) {
			continue
		}
		name, namePresent := snapshot.Names[fieldID]
		allowBare := false
		if custom || knownSystem {
			allowBare = graphFieldAllowsBareReferences(fieldID, name, schema)
		}
		if custom && (!namePresent || strings.TrimSpace(name) == "") {
			b.markMalformed(fieldsSource)
			allowBare = false
		}
		inspections = append(inspections, fieldInspection{id: fieldID, allowBare: allowBare})
	}
	for _, inspection := range inspections {
		if fieldsBudget.Clipped {
			break
		}
		fieldID := inspection.id
		safeFieldID := graphSafeFieldToken(fieldID)
		walkGraphValue(snapshot.Fields[fieldID], "/fields/"+escapeJSONPointer(safeFieldID), inspection.allowBare, fieldsBudget,
			func(value any, pointer string, bare bool) {
				b.addValueReferences(value, pointer, "issue_fields", "field", safeFieldID, bare, jiraBase, confluenceBase, fieldsSource)
			})
	}
	if fieldsBudget.Clipped {
		b.markInspectionLimit(fieldsSource)
	}
	b.completeSource(fieldsSource)
}

func (b *jiraGraphBuilder) collectSnapshotProperties(snapshot *domain.QualifiedIssueSnapshot, jiraBase, confluenceBase string) {
	propertiesSource := b.sources["issue_properties"]
	propertiesSource.Count = len(snapshot.Properties)
	propertiesBudget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	propertyKeys := graphSortedSourceKeys(snapshot.Properties, propertiesBudget, graphWalkMaxObject)
	for _, property := range propertyKeys {
		if propertiesBudget.Clipped {
			break
		}
		safeProperty := "opaque-" + graphHash(property)
		walkGraphValue(snapshot.Properties[property], "/properties/"+escapeJSONPointer(safeProperty), true, propertiesBudget,
			func(value any, pointer string, bare bool) {
				b.addValueReferences(value, pointer, "issue_properties", "property", safeProperty, bare, jiraBase, confluenceBase, propertiesSource)
			})
	}
	if propertiesBudget.Clipped {
		b.markInspectionLimit(propertiesSource)
	}
	b.completeSource(propertiesSource)
}

func (s *JiraService) collectJiraGraphV2Node(ctx context.Context, snapshot *domain.QualifiedIssueSnapshot, nodeID string, depth int, sourceKinds []string) (*jiraGraphBuilder, error) {
	temp := newJiraGraphBuilderWithSources(nodeID, sourceKinds)
	temp.addNode(domain.ArtifactGraphNode{
		ID: nodeID, Kind: "jira_issue", Service: "jira", ExternalID: strings.ToUpper(snapshot.Key),
		Label: graphBoundedLabel(snapshot.Issue.Summary), State: domain.ArtifactNodeResolved,
		Expanded: true, Depth: depth, Stability: domain.ArtifactStabilityPublicAPI,
	}, nil)
	if temp.sources["issue_links"] != nil {
		temp.collectIssueLinks(snapshot)
	}
	if temp.sources["hierarchy"] != nil {
		temp.collectHierarchy(snapshot)
	}
	if temp.sources["attachments"] != nil {
		temp.collectAttachments(snapshot)
	}
	temp.collectSnapshotText(snapshot, s.baseURL, jiraGraphConfluenceBase(s))
	if temp.sources["comments"] != nil {
		if err := temp.collectComments(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
			return nil, err
		}
	}
	if temp.sources["worklogs"] != nil {
		if err := temp.collectWorklogs(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
			return nil, err
		}
	}
	if temp.sources["remote_links"] != nil {
		if err := temp.collectRemoteLinks(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
			return nil, err
		}
	}
	if temp.sources["development"] != nil {
		if err := temp.collectDevelopment(ctx, s.tr, snapshot.ID); err != nil {
			return nil, err
		}
	}
	for id, node := range temp.nodes {
		if id != nodeID {
			node.Depth = depth + 1
			temp.nodes[id] = node
		}
	}
	for kind, source := range temp.sources {
		d := depth
		source.NodeID = nodeID
		source.NodeDepth = &d
		temp.sources[kind] = source
	}
	for id, edge := range temp.edges {
		for index := range edge.Evidence {
			edge.Evidence[index].SourceNodeID = nodeID
		}
		temp.edges[id] = edge
	}
	return temp, nil
}
