package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraGraphMaxNodes       = 2_048
	jiraGraphMaxEdges       = 4_096
	jiraGraphMaxEvidence    = 4_096
	jiraGraphMaxSourceBytes = 1 << 20
)

var jiraGraphSourceOrder = []string{
	"issue_fields",
	"issue_links",
	"hierarchy",
	"attachments",
	"issue_properties",
	"comments",
	"worklogs",
	"remote_links",
}

func jiraGraphSourceKinds(includeDevelopment bool) []string {
	kinds := append([]string(nil), jiraGraphSourceOrder...)
	if includeDevelopment {
		kinds = append(kinds, "development")
	}
	return kinds
}

// JiraIssueGraphBounds records the requested limits and reconciled usage.
type JiraIssueGraphBounds struct {
	RequestedDepth     int  `json:"requested_depth"`
	MaxNodes           int  `json:"max_nodes"`
	MaxEdges           int  `json:"max_edges"`
	MaxEvidence        int  `json:"max_evidence"`
	MaxSourceBytes     int  `json:"max_source_bytes"`
	ExpandedNodes      int  `json:"expanded_node_count"`
	FollowedNodes      int  `json:"followed_node_count"`
	AttemptedNodes     int  `json:"attempted_node_count,omitempty"`
	MaxRequests        int  `json:"max_requests,omitempty"`
	RequestsUsed       int  `json:"requests_used,omitempty"`
	MaxResponseBytes   int  `json:"max_response_bytes,omitempty"`
	ResponseBytesUsed  int  `json:"response_bytes_used,omitempty"`
	MaxSources         int  `json:"max_sources,omitempty"`
	MaxFrontier        int  `json:"max_frontier,omitempty"`
	FrontierCount      int  `json:"frontier_count,omitempty"`
	FrontierTruncated  bool `json:"frontier_truncated,omitempty"`
	IncludeDevelopment bool `json:"include_development,omitempty"`
}

// JiraIssueGraphSummary mechanically reconciles the final graph projection.
type JiraIssueGraphSummary struct {
	NodeCount                 int            `json:"node_count"`
	EdgeCount                 int            `json:"edge_count"`
	EvidenceCount             int            `json:"evidence_count"`
	SourceCount               int            `json:"source_count"`
	IncompleteSourceCount     int            `json:"incomplete_source_count"`
	SourceStatusCounts        map[string]int `json:"source_status_counts"`
	NodeCountMatchesNodes     bool           `json:"node_count_matches_nodes"`
	EdgeCountMatchesEdges     bool           `json:"edge_count_matches_edges"`
	EvidenceCountMatchesEdges bool           `json:"evidence_count_matches_edges"`
	SourceCountMatchesSources bool           `json:"source_count_matches_sources"`
	SourceStatusCountsMatch   bool           `json:"source_status_count_matches_sources"`
	IncompleteCountMatches    bool           `json:"incomplete_source_count_matches_sources"`
	ExpandedCountMatchesNodes bool           `json:"expanded_count_matches_nodes"`
	CompleteMatchesSources    bool           `json:"complete_matches_sources"`
}

// JiraIssueGraphResult is the authoritative transient bounded graph.
type JiraIssueGraphResult struct {
	SchemaVersion int                          `json:"schema_version"`
	RootID        string                       `json:"root_id"`
	Complete      bool                         `json:"complete"`
	Truncated     bool                         `json:"truncated,omitempty"`
	Bounds        JiraIssueGraphBounds         `json:"bounds"`
	Summary       JiraIssueGraphSummary        `json:"summary"`
	Nodes         []domain.ArtifactGraphNode   `json:"nodes"`
	Edges         []domain.ArtifactGraphEdge   `json:"edges"`
	Sources       []domain.ArtifactGraphSource `json:"sources"`
	Frontier      []JiraIssueGraphFrontierItem `json:"frontier,omitempty"`
	Warnings      []string                     `json:"warnings,omitempty"`
}

type jiraGraphBuilder struct {
	result        *JiraIssueGraphResult
	nodes         map[string]domain.ArtifactGraphNode
	edges         map[string]domain.ArtifactGraphEdge
	sources       map[string]*domain.ArtifactGraphSource
	evidenceCount int
	sourceKinds   []string
}

func newJiraGraphBuilderWithSources(rootID string, includeDevelopment bool) *jiraGraphBuilder {
	sourceKinds := jiraGraphSourceKinds(includeDevelopment)
	result := &JiraIssueGraphResult{
		RootID: rootID,
	}
	builder := &jiraGraphBuilder{
		result: result, nodes: map[string]domain.ArtifactGraphNode{},
		edges:       map[string]domain.ArtifactGraphEdge{},
		sources:     map[string]*domain.ArtifactGraphSource{},
		sourceKinds: sourceKinds,
	}
	for _, kind := range sourceKinds {
		builder.sources[kind] = &domain.ArtifactGraphSource{
			NodeID: rootID, Kind: kind, Requested: true,
			Status: domain.ArtifactSourceEmpty, Complete: true,
			Stability: jiraGraphSourceStability(kind),
		}
	}
	return builder
}

func jiraGraphSourceStability(kind string) domain.ArtifactGraphStability {
	if kind == "issue_properties" || kind == "development" {
		return domain.ArtifactStabilityExperimentalAPI
	}
	return domain.ArtifactStabilityPublicAPI
}

func (b *jiraGraphBuilder) collectIssueLinks(snapshot *domain.QualifiedIssueSnapshot) {
	source := b.sources["issue_links"]
	raw, present := snapshot.Fields["issuelinks"]
	if !present || raw == nil {
		b.markMalformed(source)
		return
	}
	rows, ok := raw.([]any)
	if !ok {
		b.markMalformed(source)
		return
	}
	source.Count = len(rows)
	seenIDs := map[string]bool{}
	for index, rawRow := range rows {
		row, rowOK := rawRow.(map[string]any)
		linkID, idOK := graphStrictPositiveID(row["id"])
		typeObject, typeOK := row["type"].(map[string]any)
		typeName, nameOK := graphStrictString(typeObject["name"])
		inward, hasInward := row["inwardIssue"].(map[string]any)
		outward, hasOutward := row["outwardIssue"].(map[string]any)
		if !rowOK || !idOK || seenIDs[linkID] || !typeOK || !nameOK || hasInward == hasOutward {
			b.markMalformed(source)
			continue
		}
		seenIDs[linkID] = true
		direction := "inward"
		targetObject := inward
		if hasOutward {
			direction = "outward"
			targetObject = outward
		}
		targetID, targetIDOK := graphStrictPositiveID(targetObject["id"])
		targetKey, targetKeyOK := graphStrictString(targetObject["key"])
		relation, relationOK := graphStrictString(typeObject[direction])
		if !targetIDOK || targetID == snapshot.ID || !targetKeyOK ||
			!jiraGraphExactKey(targetKey) || !relationOK {
			b.markMalformed(source)
			continue
		}
		target := domain.ArtifactGraphNode{
			ID: "jira:issue:" + strings.ToUpper(targetKey), Kind: "jira_issue", Service: "jira",
			ExternalID: strings.ToUpper(targetKey), State: domain.ArtifactNodeStub, Depth: 1,
			Stability: domain.ArtifactStabilityPublicAPI,
		}
		evidence := domain.ArtifactGraphEvidence{
			Collector: "issue_links", SourceKind: "field", SourceID: linkID,
			JSONPointer: fmt.Sprintf("/fields/issuelinks/%d", index), Extraction: "structured",
		}
		if !b.addNode(target, source) {
			continue
		}
		b.addEdge(domain.ArtifactGraphEdge{
			From: b.result.RootID, To: target.ID, Kind: "jira_link",
			RelationType: graphBoundedLabel(typeName), Relation: graphBoundedLabel(relation),
			Direction: direction, Current: true, Confidence: "exact",
			Stability: domain.ArtifactStabilityPublicAPI, Evidence: []domain.ArtifactGraphEvidence{evidence},
		}, source)
	}
	b.completeSource(source)
}

func (b *jiraGraphBuilder) collectHierarchy(snapshot *domain.QualifiedIssueSnapshot) {
	source := b.sources["hierarchy"]
	count := 0
	seen := map[string]bool{}
	addTarget := func(key, kind, pointer string) {
		count++
		if !jiraGraphExactKey(key) {
			b.markMalformed(source)
			return
		}
		identity := kind + "\x00" + strings.ToUpper(key)
		if seen[identity] {
			b.markMalformed(source)
			return
		}
		seen[identity] = true
		target := domain.ArtifactGraphNode{
			ID: "jira:issue:" + strings.ToUpper(key), Kind: "jira_issue", Service: "jira",
			ExternalID: strings.ToUpper(key), State: domain.ArtifactNodeStub, Depth: 1,
			Stability: domain.ArtifactStabilityPublicAPI,
		}
		if !b.addNode(target, source) {
			return
		}
		b.addEdge(domain.ArtifactGraphEdge{
			From: b.result.RootID, To: target.ID, Kind: kind, Direction: "outbound",
			Current: true, Confidence: "exact", Stability: domain.ArtifactStabilityPublicAPI,
			Evidence: []domain.ArtifactGraphEvidence{{
				Collector: "hierarchy", SourceKind: "field", SourceID: strings.TrimPrefix(pointer, "/fields/"),
				JSONPointer: pointer, Extraction: "structured",
			}},
		}, source)
	}
	parentRaw, parentPresent := snapshot.Fields["parent"]
	switch {
	case !parentPresent || parentRaw == nil:
	default:
		parent, ok := parentRaw.(map[string]any)
		parentID, idOK := graphStrictPositiveID(parent["id"])
		parentKey, keyOK := graphStrictString(parent["key"])
		if !ok || !idOK || parentID == snapshot.ID || !keyOK {
			count++
			b.markMalformed(source)
		} else {
			addTarget(parentKey, "child_of", "/fields/parent")
		}
	}
	subtasksRaw, subtasksPresent := snapshot.Fields["subtasks"]
	if !subtasksPresent || subtasksRaw == nil {
		// Jira omits unset optional fields from an otherwise qualified
		// fields=*all snapshot. Omission therefore proves no relation.
	} else if subtasks, ok := subtasksRaw.([]any); ok {
		for index, raw := range subtasks {
			child, rowOK := raw.(map[string]any)
			childID, idOK := graphStrictPositiveID(child["id"])
			childKey, keyOK := graphStrictString(child["key"])
			if !rowOK || !idOK || childID == snapshot.ID || !keyOK {
				count++
				b.markMalformed(source)
				continue
			}
			addTarget(childKey, "parent_of", fmt.Sprintf("/fields/subtasks/%d", index))
		}
	} else {
		count++
		b.markMalformed(source)
	}
	for fieldID, schema := range snapshot.Schema {
		name := strings.ToLower(strings.TrimSpace(snapshot.Names[fieldID]))
		custom := strings.ToLower(schema.Custom)
		if name != "epic link" && !strings.Contains(custom, "epic-link") {
			continue
		}
		if raw, present := snapshot.Fields[fieldID]; present && raw != nil {
			epic, ok := graphStrictString(raw)
			if !ok {
				count++
				b.markMalformed(source)
				continue
			}
			addTarget(epic, "epic_of", "/fields/"+escapeJSONPointer(graphSafeFieldToken(fieldID)))
		}
	}
	source.Count = count
	b.completeSource(source)
}

func (b *jiraGraphBuilder) collectAttachments(snapshot *domain.QualifiedIssueSnapshot) {
	source := b.sources["attachments"]
	raw, present := snapshot.Fields["attachment"]
	if !present || raw == nil {
		b.markMalformed(source)
		return
	}
	rows, ok := raw.([]any)
	if !ok {
		b.markMalformed(source)
		return
	}
	source.Count = len(rows)
	seenIDs := map[string]bool{}
	for index, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		id, idOK := graphStrictPositiveID(row["id"])
		filename, filenameOK := graphStrictString(row["filename"])
		if !ok || !idOK || seenIDs[id] || !filenameOK {
			b.markMalformed(source)
			continue
		}
		seenIDs[id] = true
		target := domain.ArtifactGraphNode{
			ID: "jira:attachment:" + id, Kind: "attachment", Service: "jira",
			ExternalID: id, Label: graphBoundedLabel(filename),
			State: domain.ArtifactNodeResolved, Depth: 1, Stability: domain.ArtifactStabilityPublicAPI,
		}
		if !b.addNode(target, source) {
			continue
		}
		b.addEdge(domain.ArtifactGraphEdge{
			From: b.result.RootID, To: target.ID, Kind: "attached", Direction: "outbound",
			Current: true, Confidence: "exact", Stability: domain.ArtifactStabilityPublicAPI,
			Evidence: []domain.ArtifactGraphEvidence{{
				Collector: "attachments", SourceKind: "field", SourceID: "attachment",
				JSONPointer: fmt.Sprintf("/fields/attachment/%d", index), Extraction: "structured",
			}},
		}, source)
	}
	b.completeSource(source)
}

func (b *jiraGraphBuilder) collectSnapshotText(snapshot *domain.QualifiedIssueSnapshot, jiraBase, confluenceBase string) {
	fieldsSource := b.sources["issue_fields"]
	propertiesSource := b.sources["issue_properties"]
	fieldsSource.Count = len(snapshot.Fields)
	propertiesSource.Count = len(snapshot.Properties)
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

func (b *jiraGraphBuilder) collectComments(ctx context.Context, tracker domain.Tracker, key, jiraBase, confluenceBase string) error {
	source := b.sources["comments"]
	comments, err := tracker.ListComments(ctx, key)
	if err != nil {
		return b.qualifyAuxiliaryError(ctx, source, err, false)
	}
	if comments == nil {
		b.markMalformed(source)
		return nil
	}
	source.Count = len(comments)
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	seenIDs := map[string]bool{}
	for _, comment := range comments {
		if !graphNumericIDPattern.MatchString(comment.ID) || seenIDs[comment.ID] {
			b.markMalformed(source)
			continue
		}
		seenIDs[comment.ID] = true
		pointer := "/comments/" + escapeJSONPointer(comment.ID) + "/body"
		walkGraphValue(comment.Body, pointer, true, budget, func(value any, pointer string, bare bool) {
			b.addValueReferences(value, pointer, "comments", "comment", comment.ID, bare, jiraBase, confluenceBase, source)
		})
	}
	if budget.Clipped {
		b.markInspectionLimit(source)
	}
	b.completeSource(source)
	return nil
}

func (b *jiraGraphBuilder) collectWorklogs(ctx context.Context, tracker domain.Tracker, key, jiraBase, confluenceBase string) error {
	source := b.sources["worklogs"]
	reader, ok := tracker.(domain.IssueWorklogReader)
	if !ok {
		source.Status = domain.ArtifactSourceUnsupported
		source.Complete = false
		return nil
	}
	inventory, err := reader.ListIssueWorklogs(ctx, key)
	if err != nil {
		return b.qualifyAuxiliaryError(ctx, source, err, false)
	}
	if inventory == nil || !inventory.Complete || inventory.Total != len(inventory.Worklogs) {
		b.markMalformed(source)
		return nil
	}
	source.Count = inventory.Total
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	seenIDs := map[string]bool{}
	for _, worklog := range inventory.Worklogs {
		if !graphNumericIDPattern.MatchString(worklog.ID) || seenIDs[worklog.ID] {
			b.markMalformed(source)
			continue
		}
		seenIDs[worklog.ID] = true
		pointer := "/worklogs/" + escapeJSONPointer(worklog.ID) + "/comment"
		walkGraphValue(worklog.Comment, pointer, true, budget, func(value any, pointer string, bare bool) {
			b.addValueReferences(value, pointer, "worklogs", "worklog", worklog.ID, bare, jiraBase, confluenceBase, source)
		})
	}
	if budget.Clipped {
		b.markInspectionLimit(source)
	}
	b.completeSource(source)
	return nil
}

func (b *jiraGraphBuilder) collectRemoteLinks(ctx context.Context, tracker domain.Tracker, key, jiraBase, confluenceBase string) error {
	source := b.sources["remote_links"]
	reader, ok := tracker.(domain.JiraRemoteLinkReader)
	if !ok {
		source.Status = domain.ArtifactSourceUnsupported
		source.Complete = false
		return nil
	}
	inventory, err := reader.ReadIssueRemoteLinks(ctx, key)
	if err != nil {
		return b.qualifyAuxiliaryError(ctx, source, err, true)
	}
	if inventory.Total < 0 || inventory.Unsupported < 0 ||
		inventory.Total != len(inventory.Links)+inventory.Unsupported {
		b.markMalformed(source)
		return nil
	}
	source.Count = inventory.Total
	seenIDs := map[string]bool{}
	for index, link := range inventory.Links {
		if !graphNumericIDPattern.MatchString(link.ID) || seenIDs[link.ID] {
			b.markMalformed(source)
			continue
		}
		seenIDs[link.ID] = true
		reference, ok := normalizeGraphURL(link.ObjectURL, jiraBase, confluenceBase)
		if !ok {
			b.markMalformed(source)
			continue
		}
		reference.Node.Label = graphBoundedLabel(link.ObjectTitle)
		if !b.addNode(reference.Node, source) {
			continue
		}
		b.addEdge(domain.ArtifactGraphEdge{
			From: b.result.RootID, To: reference.Node.ID, Kind: "remote_link",
			Relation: graphBoundedLabel(link.Relationship), Direction: "outbound",
			Current: true, Confidence: "exact", Stability: domain.ArtifactStabilityPublicAPI,
			Evidence: []domain.ArtifactGraphEvidence{{
				Collector: "remote_links", SourceKind: "remote_link", SourceID: link.ID,
				JSONPointer: fmt.Sprintf("/remote_links/%d", index), Extraction: "structured",
			}},
		}, source)
	}
	if inventory.Unsupported > 0 {
		b.markMalformed(source)
	}
	b.completeSource(source)
	return nil
}

func (b *jiraGraphBuilder) addValueReferences(value any, pointer, collector, sourceKind, sourceID string, allowBare bool, jiraBase, confluenceBase string, source *domain.ArtifactGraphSource) {
	if strings.EqualFold(graphPointerLeaf(pointer), "pageId") {
		id, ok := graphStrictPositiveID(value)
		if !ok {
			b.markMalformed(source)
			return
		}
		node := domain.ArtifactGraphNode{
			ID: "confluence:page:" + id, Kind: "confluence_page", Service: "confluence",
			ExternalID: id, State: domain.ArtifactNodeStub, Depth: 1,
			Stability: domain.ArtifactStabilityHeuristic,
		}
		if b.addNode(node, source) {
			b.addEdge(domain.ArtifactGraphEdge{
				From: b.result.RootID, To: node.ID, Kind: "mentions", Direction: "outbound",
				Current: true, Confidence: "high", Stability: domain.ArtifactStabilityHeuristic,
				Evidence: []domain.ArtifactGraphEvidence{{
					Collector: collector, SourceKind: sourceKind, SourceID: sourceID,
					JSONPointer: pointer, Extraction: "confluence_page_id",
				}},
			}, source)
		}
	}
	text, ok := value.(string)
	if !ok {
		return
	}
	b.addTextReferences(text, pointer, collector, sourceKind, sourceID, allowBare, jiraBase, confluenceBase, source)
}

func (b *jiraGraphBuilder) addTextReferences(text, pointer, collector, sourceKind, sourceID string, allowBare bool, jiraBase, confluenceBase string, source *domain.ArtifactGraphSource) {
	for _, reference := range extractGraphReferences(text, jiraBase, confluenceBase, allowBare) {
		if !b.addNode(reference.Node, source) {
			continue
		}
		b.addEdge(domain.ArtifactGraphEdge{
			From: b.result.RootID, To: reference.Node.ID, Kind: "mentions", Direction: "outbound",
			Current: true, Confidence: reference.Confidence, Stability: domain.ArtifactStabilityHeuristic,
			Evidence: []domain.ArtifactGraphEvidence{{
				Collector: collector, SourceKind: sourceKind, SourceID: sourceID,
				JSONPointer: pointer, Extraction: reference.Extraction,
			}},
		}, source)
	}
}

func graphSortedSourceKeys(values map[string]any, budget *graphExtractBudget, maxKeys int) []string {
	if len(values) > maxKeys || !budget.consume(2+len(values)) {
		budget.Clipped = true
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !budget.consume(len(key) + 3) {
			return nil
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func graphSchemaIsIdentity(schema domain.IssueFieldSchema) bool {
	for _, value := range []string{schema.Type, schema.Items} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "user", "users", "group", "groups":
			return true
		}
	}
	return false
}

func graphValueMayContainReferences(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case nil, json.Number, float64, bool:
		return false
	default:
		return true
	}
}

func graphNormalizeFieldSchema(schema domain.IssueFieldSchema, allowTopLevelAny bool) (domain.IssueFieldSchema, bool) {
	schema.Type = strings.ToLower(strings.TrimSpace(schema.Type))
	schema.Items = strings.ToLower(strings.TrimSpace(schema.Items))
	schema.System = strings.TrimSpace(schema.System)
	schema.Custom = strings.TrimSpace(schema.Custom)
	if schema.Type == "any" {
		if !allowTopLevelAny || schema.Items != "" {
			return domain.IssueFieldSchema{}, false
		}
		return schema, true
	}
	if !graphKnownSchemaType(schema.Type) {
		return domain.IssueFieldSchema{}, false
	}
	if schema.Type == "array" {
		if schema.Items == "" || schema.Items == "array" || !graphKnownSchemaType(schema.Items) {
			return domain.IssueFieldSchema{}, false
		}
	} else if schema.Items != "" {
		return domain.IssueFieldSchema{}, false
	}
	return schema, true
}

func graphKnownSchemaType(value string) bool {
	switch value {
	case "array", "string", "number", "integer", "boolean", "date", "datetime",
		"option", "option-with-child", "component", "version", "user", "users",
		"group", "groups", "project", "issuetype", "priority", "resolution",
		"status", "securitylevel", "progress", "votes", "watches", "timetracking",
		"attachment", "issuelink", "comment", "worklog":
		return true
	default:
		return false
	}
}

func graphKnownSystemFieldID(fieldID string) bool {
	switch strings.ToLower(fieldID) {
	case "summary", "description", "environment", "labels", "status", "issuetype",
		"project", "priority", "resolution", "components", "fixversions", "versions",
		"created", "updated", "duedate", "resolutiondate", "lastviewed", "security",
		"progress", "aggregateprogress", "votes", "watches", "timetracking",
		"timeestimate", "timeoriginalestimate", "timespent", "aggregatetimeestimate",
		"aggregatetimeoriginalestimate", "aggregatetimespent", "workratio":
		return true
	default:
		return false
	}
}

func graphStrictPositiveID(value any) (string, bool) {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case json.Number:
		text = typed.String()
	default:
		return "", false
	}
	if !graphNumericIDPattern.MatchString(text) {
		return "", false
	}
	return text, true
}

func graphStrictString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" || text != strings.TrimSpace(text) {
		return "", false
	}
	return text, true
}

func (b *jiraGraphBuilder) addNode(node domain.ArtifactGraphNode, source *domain.ArtifactGraphSource) bool {
	if existing, ok := b.nodes[node.ID]; ok {
		if existing.Label == "" && node.Label != "" {
			existing.Label = node.Label
		}
		if existing.URL == "" && node.URL != "" {
			existing.URL = node.URL
		}
		if existing.SCM == nil && node.SCM != nil {
			scm := *node.SCM
			existing.SCM = &scm
		}
		if node.State == domain.ArtifactNodeResolved || (node.State == domain.ArtifactNodeStub && existing.State == domain.ArtifactNodeUnresolved) {
			existing.State = node.State
			existing.Stability = node.Stability
		}
		b.nodes[node.ID] = existing
		return true
	}
	if len(b.nodes) >= jiraGraphMaxNodes {
		b.markOutputLimit(source)
		return false
	}
	b.nodes[node.ID] = node
	return true
}

func (b *jiraGraphBuilder) addEdge(edge domain.ArtifactGraphEdge, source *domain.ArtifactGraphSource) bool {
	edge.ID = graphEdgeID(edge)
	if existing, ok := b.edges[edge.ID]; ok {
		seen := map[string]bool{}
		for _, evidence := range existing.Evidence {
			seen[graphEvidenceKey(evidence)] = true
		}
		for _, evidence := range edge.Evidence {
			if seen[graphEvidenceKey(evidence)] {
				continue
			}
			if b.evidenceCount >= jiraGraphMaxEvidence {
				b.markOutputLimit(source)
				return false
			}
			existing.Evidence = append(existing.Evidence, evidence)
			b.evidenceCount++
		}
		b.edges[edge.ID] = existing
		return true
	}
	if len(b.edges) >= jiraGraphMaxEdges || b.evidenceCount+len(edge.Evidence) > jiraGraphMaxEvidence {
		b.markOutputLimit(source)
		return false
	}
	b.edges[edge.ID] = edge
	b.evidenceCount += len(edge.Evidence)
	return true
}

func (b *jiraGraphBuilder) qualifyAuxiliaryError(ctx context.Context, source *domain.ArtifactGraphSource, err error, notFoundUnsupported bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	source.Complete = false
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		source.Status = domain.ArtifactSourcePartial
		source.Truncated = true
		source.PartialReason = domain.ArtifactPartialRequestLimit
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		source.Status = domain.ArtifactSourcePartial
		source.Truncated = true
		source.PartialReason = domain.ArtifactPartialByteLimit
	case errors.Is(err, domain.ErrOutputLimit):
		source.Status = domain.ArtifactSourcePartial
		source.Truncated = true
		source.PartialReason = domain.ArtifactPartialOutputLimit
	case errors.Is(err, domain.ErrAuth), errors.Is(err, domain.ErrForbidden):
		source.Status = domain.ArtifactSourceForbidden
	case notFoundUnsupported && errors.Is(err, domain.ErrNotFound):
		source.Status = domain.ArtifactSourceUnsupported
	case errors.Is(err, domain.ErrCheckFailed):
		source.Status = domain.ArtifactSourcePartial
		source.PartialReason = domain.ArtifactPartialMalformed
	default:
		source.Status = domain.ArtifactSourcePartial
		source.PartialReason = domain.ArtifactPartialRequestFailed
	}
	return nil
}

func (b *jiraGraphBuilder) markMalformed(source *domain.ArtifactGraphSource) {
	if source == nil {
		return
	}
	source.Status = domain.ArtifactSourcePartial
	source.Complete = false
	source.PartialReason = domain.ArtifactPartialMalformed
}

func (b *jiraGraphBuilder) markInspectionLimit(source *domain.ArtifactGraphSource) {
	if source == nil {
		return
	}
	source.Status = domain.ArtifactSourcePartial
	source.Complete = false
	source.Truncated = true
	source.PartialReason = domain.ArtifactPartialInspectionLimit
}

func (b *jiraGraphBuilder) markOutputLimit(source *domain.ArtifactGraphSource) {
	if source == nil {
		return
	}
	source.Status = domain.ArtifactSourcePartial
	source.Complete = false
	source.Truncated = true
	source.PartialReason = domain.ArtifactPartialOutputLimit
}

func (b *jiraGraphBuilder) completeSource(source *domain.ArtifactGraphSource) {
	if source == nil || !source.Complete {
		return
	}
	if source.Count == 0 {
		source.Status = domain.ArtifactSourceEmpty
	} else {
		source.Status = domain.ArtifactSourceComplete
	}
}

func graphFieldAllowsBareReferences(fieldID, name string, schema domain.IssueFieldSchema) bool {
	narrativeShape := schema.Type == "string" || schema.Type == "array" && schema.Items == "string"
	if !narrativeShape {
		return false
	}
	switch strings.ToLower(fieldID) {
	case "summary", "description", "environment":
		return true
	case "labels", "status", "issuetype", "project", "priority", "resolution",
		"assignee", "reporter", "creator", "components", "fixversions", "versions":
		return false
	}
	if !graphCustomFieldIDPattern.MatchString(fieldID) {
		return false
	}
	if schema.Type == "string" {
		return true
	}
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "description") || strings.Contains(lowerName, "notes") ||
		strings.Contains(lowerName, "details") || strings.Contains(lowerName, "documentation")
}

func jiraGraphExactKey(key string) bool {
	span := graphJiraKeyPattern.FindStringIndex(key)
	return span != nil && span[0] == 0 && span[1] == len(key)
}

func jiraGraphConfluenceBase(service *JiraService) string {
	if service == nil || service.cfg == nil {
		return ""
	}
	return service.cfg.ConfluenceURL
}

func graphEdgeID(edge domain.ArtifactGraphEdge) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"atl-jira-graph-edge-v1", edge.From, edge.To, edge.Kind,
		edge.RelationType, edge.Relation, edge.Direction,
	}, "\x00")))
	return "edge:" + hex.EncodeToString(sum[:])
}

func graphEvidenceKey(evidence domain.ArtifactGraphEvidence) string {
	return strings.Join([]string{
		evidence.Collector, evidence.SourceKind, evidence.SourceID,
		evidence.JSONPointer, evidence.Extraction,
	}, "\x00")
}

func graphEdgeSortKey(edge domain.ArtifactGraphEdge) string {
	return strings.Join([]string{
		edge.From, edge.Kind, edge.RelationType, edge.Relation,
		edge.To, edge.Direction, edge.ID,
	}, "\x00")
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func equalStringIntMap(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// JiraIssueGraphMarkdown renders the same qualified facts as compact Markdown.
// Dynamic cells pass through the shared table escaper.
func JiraIssueGraphMarkdown(result *JiraIssueGraphResult) string {
	if result == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Jira work-artifact graph\n\n")
	fmt.Fprintf(&out, "- Root: `%s`\n", result.RootID)
	fmt.Fprintf(&out, "- Complete: `%t`\n", result.Complete)
	fmt.Fprintf(&out, "- Depth: `%d` (expanded `%d`, followed `%d`)\n", result.Bounds.RequestedDepth, result.Bounds.ExpandedNodes, result.Bounds.FollowedNodes)
	fmt.Fprintf(&out, "- Transport: `%d/%d` attempts; `%d/%d` buffered response bytes\n",
		result.Bounds.RequestsUsed, result.Bounds.MaxRequests,
		result.Bounds.ResponseBytesUsed, result.Bounds.MaxResponseBytes)
	fmt.Fprintf(&out, "- Nodes: `%d`; edges: `%d`; evidence: `%d`; sources: `%d`\n\n",
		result.Summary.NodeCount, result.Summary.EdgeCount, result.Summary.EvidenceCount, result.Summary.SourceCount)

	sourceRows := make([][]string, 0, len(result.Sources))
	for _, source := range result.Sources {
		row := []string{
			source.Kind, string(source.Status), fmt.Sprint(source.Complete),
			fmt.Sprint(source.Count), fmt.Sprint(source.Truncated),
			string(source.Stability), source.PartialReason,
		}
		depth := ""
		if source.NodeDepth != nil {
			depth = fmt.Sprint(*source.NodeDepth)
		}
		row = append([]string{source.NodeID, depth}, row...)
		sourceRows = append(sourceRows, row)
	}
	out.WriteString("## Sources\n\n")
	sourceHeader := []string{"Source", "Status", "Complete", "Count", "Truncated", "Stability", "Reason"}
	sourceHeader = append([]string{"Node", "Depth"}, sourceHeader...)
	out.WriteString(MarkdownTable(sourceHeader, sourceRows))
	if len(result.Frontier) > 0 {
		frontierRows := make([][]string, 0, len(result.Frontier))
		for _, item := range result.Frontier {
			frontierRows = append(frontierRows, []string{item.NodeID, fmt.Sprint(item.Depth), item.Reason})
		}
		out.WriteString("\n## Frontier\n\n")
		out.WriteString(MarkdownTable([]string{"Node", "Depth", "Reason"}, frontierRows))
	}
	out.WriteString("\n## Nodes\n\n")
	includeSCM := false
	for _, node := range result.Nodes {
		if node.SCM != nil {
			includeSCM = true
			break
		}
	}
	nodeRows := make([][]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		row := []string{
			node.ID, node.Kind, string(node.State), fmt.Sprint(node.Depth),
			fmt.Sprint(node.Expanded), node.Label, node.URL,
		}
		if includeSCM {
			host, project, selector, artifactState := "", "", "", ""
			if node.SCM != nil {
				host, project = node.SCM.Host, node.SCM.ProjectPath
				switch {
				case node.SCM.CommitSHA != "":
					selector = "commit:" + node.SCM.CommitSHA
				case node.SCM.BranchName != "":
					selector = "branch:" + node.SCM.BranchName
				case node.SCM.MergeRequestIID != "":
					selector = "merge_request:" + node.SCM.MergeRequestIID
					artifactState = node.SCM.MergeRequestState
				case node.Kind == "gitlab_project":
					selector = "project"
				}
			}
			row = append(row, host, project, selector, artifactState)
		}
		nodeRows = append(nodeRows, row)
	}
	nodeHeader := []string{"ID", "Kind", "State", "Depth", "Expanded", "Label", "URL"}
	if includeSCM {
		nodeHeader = append(nodeHeader, "Host", "Project", "Selector", "Artifact State")
	}
	out.WriteString(MarkdownTable(nodeHeader, nodeRows))
	out.WriteString("\n## Edges\n\n")
	edgeRows := make([][]string, 0, len(result.Edges))
	for _, edge := range result.Edges {
		edgeRows = append(edgeRows, []string{
			edge.From, edge.Kind, edge.RelationType, edge.Relation, edge.To,
			edge.Direction, edge.Confidence, fmt.Sprint(len(edge.Evidence)),
		})
	}
	out.WriteString(MarkdownTable(
		[]string{"From", "Kind", "Type", "Relation", "To", "Direction", "Confidence", "Evidence"},
		edgeRows,
	))
	return strings.TrimRight(out.String(), "\n")
}
