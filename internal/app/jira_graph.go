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
	jiraIssueGraphSchemaVersion = 1
	jiraGraphMaxNodes           = 2_048
	jiraGraphMaxEdges           = 4_096
	jiraGraphMaxEvidence        = 4_096
	jiraGraphMaxSourceBytes     = 1 << 20
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

// JiraIssueGraphBounds records the fixed direct-only contract. There is no
// traversal flag in schema v1; discovered nodes remain unexpanded stubs.
type JiraIssueGraphBounds struct {
	RequestedDepth    int  `json:"requested_depth"`
	MaxNodes          int  `json:"max_nodes"`
	MaxEdges          int  `json:"max_edges"`
	MaxEvidence       int  `json:"max_evidence"`
	MaxSourceBytes    int  `json:"max_source_bytes"`
	ExpandedNodes     int  `json:"expanded_node_count"`
	FollowedNodes     int  `json:"followed_node_count"`
	AttemptedNodes    int  `json:"attempted_node_count,omitempty"`
	MaxRequests       int  `json:"max_requests,omitempty"`
	RequestsUsed      int  `json:"requests_used,omitempty"`
	MaxResponseBytes  int  `json:"max_response_bytes,omitempty"`
	ResponseBytesUsed int  `json:"response_bytes_used,omitempty"`
	MaxSources        int  `json:"max_sources,omitempty"`
	MaxFrontier       int  `json:"max_frontier,omitempty"`
	FrontierCount     int  `json:"frontier_count,omitempty"`
	FrontierTruncated bool `json:"frontier_truncated,omitempty"`
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

// JiraIssueGraphResult is the authoritative transient schema-v1 graph.
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
}

// IssueGraph builds one direct graph. Only the seed is fetched as an issue;
// discovered Jira, Confluence, and external targets are never followed.
func (s *JiraService) IssueGraph(ctx context.Context, key string) (*JiraIssueGraphResult, error) {
	key = strings.TrimSpace(key)
	if !jiraGraphExactKey(key) {
		return nil, fmt.Errorf("%w: issue key must use the canonical PROJECT-123 form", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.QualifiedIssueSnapshotReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira graph snapshot capability is unavailable", domain.ErrCheckFailed)
	}
	snapshot, err := reader.ReadIssueSnapshot(ctx, key)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.ID == "" || !jiraGraphExactKey(snapshot.Key) ||
		!strings.EqualFold(snapshot.RequestedKey, key) ||
		!strings.EqualFold(snapshot.Key, key) ||
		snapshot.Issue.ID != snapshot.ID ||
		!strings.EqualFold(snapshot.Issue.Key, snapshot.Key) ||
		snapshot.Fields == nil || snapshot.Names == nil ||
		snapshot.Schema == nil || snapshot.Properties == nil {
		return nil, fmt.Errorf("%w: Jira graph snapshot has no usable identity", domain.ErrCheckFailed)
	}

	rootID := "jira:issue:" + strings.ToUpper(snapshot.Key)
	builder := newJiraGraphBuilder(rootID)
	builder.addNode(domain.ArtifactGraphNode{
		ID: rootID, Kind: "jira_issue", Service: "jira",
		ExternalID: strings.ToUpper(snapshot.Key),
		Label:      graphBoundedLabel(snapshot.Issue.Summary),
		State:      domain.ArtifactNodeResolved,
		Expanded:   true,
		Depth:      0,
		Stability:  domain.ArtifactStabilityPublicAPI,
	}, nil)

	builder.collectIssueLinks(snapshot)
	builder.collectHierarchy(snapshot)
	builder.collectAttachments(snapshot)
	builder.collectSnapshotText(snapshot, s.baseURL, jiraGraphConfluenceBase(s))
	if err := builder.collectComments(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	if err := builder.collectWorklogs(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	if err := builder.collectRemoteLinks(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	return builder.finish()
}

func newJiraGraphBuilder(rootID string) *jiraGraphBuilder {
	result := &JiraIssueGraphResult{
		SchemaVersion: jiraIssueGraphSchemaVersion,
		RootID:        rootID,
		Bounds: JiraIssueGraphBounds{
			RequestedDepth: 0, MaxNodes: jiraGraphMaxNodes, MaxEdges: jiraGraphMaxEdges,
			MaxEvidence: jiraGraphMaxEvidence, MaxSourceBytes: jiraGraphMaxSourceBytes,
		},
		Nodes: []domain.ArtifactGraphNode{}, Edges: []domain.ArtifactGraphEdge{},
		Sources: []domain.ArtifactGraphSource{},
	}
	builder := &jiraGraphBuilder{
		result: result, nodes: map[string]domain.ArtifactGraphNode{},
		edges:   map[string]domain.ArtifactGraphEdge{},
		sources: map[string]*domain.ArtifactGraphSource{},
	}
	for _, kind := range jiraGraphSourceOrder {
		builder.sources[kind] = &domain.ArtifactGraphSource{
			NodeID: rootID, Kind: kind, Requested: true,
			Status: domain.ArtifactSourceEmpty, Complete: true,
			Stability: domain.ArtifactStabilityPublicAPI,
		}
	}
	return builder
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
	for _, fieldID := range fieldIDs {
		if fieldsBudget.Clipped {
			break
		}
		if graphSkippedPathKeys[strings.ToLower(fieldID)] {
			continue
		}
		switch fieldID {
		case "issuelinks", "parent", "subtasks", "attachment", "comment", "worklog":
			continue
		}
		schema := snapshot.Schema[fieldID]
		if graphSchemaIsIdentity(schema) {
			continue
		}
		allowBare := graphFieldAllowsBareReferences(fieldID, snapshot.Names[fieldID], schema)
		safeFieldID := graphSafeFieldToken(fieldID)
		walkGraphValue(snapshot.Fields[fieldID], "/fields/"+escapeJSONPointer(safeFieldID), allowBare, fieldsBudget,
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

func (b *jiraGraphBuilder) finish() (*JiraIssueGraphResult, error) {
	builderExpanded := 0
	for _, node := range b.nodes {
		if node.Expanded {
			builderExpanded++
		}
	}
	builderIncomplete := 0
	builderComplete := true
	for _, source := range b.sources {
		if !source.Complete {
			builderComplete = false
			builderIncomplete++
		}
	}
	b.result.Bounds.ExpandedNodes = builderExpanded
	b.result.Bounds.FollowedNodes = 0
	b.result.Complete = builderComplete

	for _, node := range b.nodes {
		b.result.Nodes = append(b.result.Nodes, node)
	}
	sort.Slice(b.result.Nodes, func(i, j int) bool {
		if b.result.Nodes[i].Depth != b.result.Nodes[j].Depth {
			return b.result.Nodes[i].Depth < b.result.Nodes[j].Depth
		}
		if b.result.Nodes[i].Kind != b.result.Nodes[j].Kind {
			return b.result.Nodes[i].Kind < b.result.Nodes[j].Kind
		}
		return b.result.Nodes[i].ID < b.result.Nodes[j].ID
	})
	for _, edge := range b.edges {
		sort.Slice(edge.Evidence, func(i, j int) bool {
			return graphEvidenceKey(edge.Evidence[i]) < graphEvidenceKey(edge.Evidence[j])
		})
		b.result.Edges = append(b.result.Edges, edge)
	}
	sort.Slice(b.result.Edges, func(i, j int) bool {
		left, right := b.result.Edges[i], b.result.Edges[j]
		return graphEdgeSortKey(left) < graphEdgeSortKey(right)
	})
	for _, kind := range jiraGraphSourceOrder {
		source, ok := b.sources[kind]
		if !ok || source == nil {
			return nil, fmt.Errorf("%w: Jira graph source inventory is incomplete", domain.ErrCheckFailed)
		}
		b.result.Sources = append(b.result.Sources, *source)
	}

	summary := JiraIssueGraphSummary{
		NodeCount: len(b.nodes), EdgeCount: len(b.edges), EvidenceCount: b.evidenceCount,
		SourceCount: len(b.sources), IncompleteSourceCount: builderIncomplete,
		SourceStatusCounts: map[string]int{
			"complete": 0, "empty": 0, "partial": 0,
			"forbidden": 0, "unsupported": 0, "skipped": 0,
		},
	}
	finalExpanded := 0
	finalEvidence := 0
	for _, node := range b.result.Nodes {
		if node.Expanded {
			finalExpanded++
		}
	}
	for _, edge := range b.result.Edges {
		finalEvidence += len(edge.Evidence)
	}
	finalIncomplete := 0
	finalComplete := true
	for _, source := range b.result.Sources {
		summary.SourceStatusCounts[string(source.Status)]++
		if !source.Complete {
			finalComplete = false
			finalIncomplete++
		}
		if source.Truncated {
			b.result.Truncated = true
		}
	}
	statusTotal := 0
	for _, count := range summary.SourceStatusCounts {
		statusTotal += count
	}
	summary.NodeCountMatchesNodes = summary.NodeCount == len(b.result.Nodes)
	summary.EdgeCountMatchesEdges = summary.EdgeCount == len(b.result.Edges)
	summary.EvidenceCountMatchesEdges = summary.EvidenceCount == finalEvidence
	summary.SourceCountMatchesSources = summary.SourceCount == len(b.result.Sources) &&
		summary.SourceCount == len(jiraGraphSourceOrder)
	summary.SourceStatusCountsMatch = statusTotal == summary.SourceCount
	summary.IncompleteCountMatches = summary.IncompleteSourceCount == finalIncomplete
	summary.ExpandedCountMatchesNodes = b.result.Bounds.ExpandedNodes == finalExpanded &&
		finalExpanded == 1 && b.result.Bounds.FollowedNodes == 0
	summary.CompleteMatchesSources = b.result.Complete == finalComplete
	b.result.Summary = summary
	if summary.IncompleteSourceCount > 0 {
		b.result.Warnings = []string{"one or more requested graph sources are incomplete"}
	}
	if !summary.NodeCountMatchesNodes || !summary.EdgeCountMatchesEdges ||
		!summary.EvidenceCountMatchesEdges || !summary.SourceCountMatchesSources ||
		!summary.SourceStatusCountsMatch || !summary.IncompleteCountMatches ||
		!summary.ExpandedCountMatchesNodes || !summary.CompleteMatchesSources {
		return nil, fmt.Errorf("%w: Jira graph reconciliation failed", domain.ErrCheckFailed)
	}
	if err := validateJiraGraphResult(b.result); err != nil {
		return nil, err
	}
	return b.result, nil
}

func graphFieldAllowsBareReferences(fieldID, name string, schema domain.IssueFieldSchema) bool {
	switch strings.ToLower(fieldID) {
	case "summary", "description", "environment":
		return true
	case "labels", "status", "issuetype", "project", "priority", "resolution",
		"assignee", "reporter", "creator", "components", "fixversions", "versions":
		return false
	}
	if strings.EqualFold(schema.Type, "string") && strings.HasPrefix(strings.ToLower(fieldID), "customfield_") {
		return true
	}
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "description") || strings.Contains(lowerName, "notes") ||
		strings.Contains(lowerName, "details") || strings.Contains(lowerName, "documentation")
}

func jiraGraphExactKey(key string) bool {
	return len(graphJiraKeyPattern.FindStringSubmatch(key)) == 2 &&
		graphJiraKeyPattern.FindStringSubmatch(key)[0] == key
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

func validateJiraGraphResult(result *JiraIssueGraphResult) error {
	if result == nil || result.SchemaVersion != jiraIssueGraphSchemaVersion {
		return fmt.Errorf("%w: Jira graph schema is invalid", domain.ErrCheckFailed)
	}
	nodes := make(map[string]bool, len(result.Nodes))
	expanded := 0
	for _, node := range result.Nodes {
		if node.ID == "" || nodes[node.ID] || !oneOf(node.Kind, "jira_issue", "confluence_page", "attachment", "url") ||
			!oneOf(node.Service, "jira", "confluence", "external") ||
			!oneOf(string(node.State), "resolved", "stub", "unresolved", "forbidden", "missing") ||
			!oneOf(string(node.Stability), "public_api", "experimental_api", "heuristic") ||
			node.Depth < 0 || node.Depth > 1 {
			return fmt.Errorf("%w: Jira graph node contract is invalid", domain.ErrCheckFailed)
		}
		nodes[node.ID] = true
		if node.Expanded {
			expanded++
		}
	}
	if !nodes[result.RootID] || expanded != 1 {
		return fmt.Errorf("%w: Jira graph root contract is invalid", domain.ErrCheckFailed)
	}
	evidenceCount := 0
	for _, edge := range result.Edges {
		if edge.ID != graphEdgeID(edge) || !nodes[edge.From] || !nodes[edge.To] ||
			!oneOf(edge.Kind, "jira_link", "parent_of", "child_of", "epic_of", "attached", "mentions", "remote_link") ||
			!oneOf(edge.Direction, "inward", "outward", "outbound") ||
			!oneOf(edge.Confidence, "exact", "high", "candidate") ||
			!oneOf(string(edge.Stability), "public_api", "experimental_api", "heuristic") ||
			len(edge.Evidence) == 0 {
			return fmt.Errorf("%w: Jira graph edge contract is invalid", domain.ErrCheckFailed)
		}
		for _, evidence := range edge.Evidence {
			if !oneOf(evidence.Collector, jiraGraphSourceOrder...) ||
				!oneOf(evidence.SourceKind, "field", "property", "comment", "worklog", "remote_link") ||
				!oneOf(evidence.Extraction, "structured", "absolute_url", "jira_key", "confluence_page_id", "service_url") {
				return fmt.Errorf("%w: Jira graph evidence contract is invalid", domain.ErrCheckFailed)
			}
			evidenceCount++
		}
	}
	statusCounts := map[string]int{
		"complete": 0, "empty": 0, "partial": 0,
		"forbidden": 0, "unsupported": 0, "skipped": 0,
	}
	incomplete := 0
	for index, source := range result.Sources {
		if index >= len(jiraGraphSourceOrder) || source.Kind != jiraGraphSourceOrder[index] ||
			source.NodeID != result.RootID || !source.Requested ||
			!oneOf(string(source.Status), "complete", "empty", "partial", "forbidden", "unsupported", "skipped") ||
			source.Stability != domain.ArtifactStabilityPublicAPI ||
			source.Count < 0 || source.Complete != (source.Status == domain.ArtifactSourceComplete || source.Status == domain.ArtifactSourceEmpty) {
			return fmt.Errorf("%w: Jira graph source contract is invalid", domain.ErrCheckFailed)
		}
		switch source.Status {
		case domain.ArtifactSourcePartial:
			if !domain.ValidArtifactPartialReason(source.PartialReason) ||
				source.Truncated != (source.PartialReason == domain.ArtifactPartialInspectionLimit ||
					source.PartialReason == domain.ArtifactPartialOutputLimit ||
					source.PartialReason == domain.ArtifactPartialRequestLimit ||
					source.PartialReason == domain.ArtifactPartialByteLimit) {
				return fmt.Errorf("%w: Jira graph partial source contract is invalid", domain.ErrCheckFailed)
			}
		case domain.ArtifactSourceSkipped:
			if source.PartialReason != domain.ArtifactPartialPolicy || source.Truncated {
				return fmt.Errorf("%w: Jira graph skipped source contract is invalid", domain.ErrCheckFailed)
			}
		default:
			if source.PartialReason != "" || source.Truncated {
				return fmt.Errorf("%w: Jira graph qualified source contract is invalid", domain.ErrCheckFailed)
			}
		}
		statusCounts[string(source.Status)]++
		if !source.Complete {
			incomplete++
		}
	}
	if len(result.Sources) != len(jiraGraphSourceOrder) ||
		result.Summary.NodeCount != len(result.Nodes) ||
		result.Summary.EdgeCount != len(result.Edges) ||
		result.Summary.EvidenceCount != evidenceCount ||
		result.Summary.SourceCount != len(result.Sources) ||
		result.Summary.IncompleteSourceCount != incomplete ||
		!equalStringIntMap(result.Summary.SourceStatusCounts, statusCounts) ||
		!result.Summary.NodeCountMatchesNodes || !result.Summary.EdgeCountMatchesEdges ||
		!result.Summary.EvidenceCountMatchesEdges || !result.Summary.SourceCountMatchesSources ||
		!result.Summary.SourceStatusCountsMatch || !result.Summary.IncompleteCountMatches ||
		!result.Summary.ExpandedCountMatchesNodes || !result.Summary.CompleteMatchesSources {
		return fmt.Errorf("%w: Jira graph summary contract is invalid", domain.ErrCheckFailed)
	}
	return nil
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
	if result.SchemaVersion >= jiraIssueGraphSchemaVersionV2 {
		fmt.Fprintf(&out, "- Transport: `%d/%d` attempts; `%d/%d` buffered response bytes\n",
			result.Bounds.RequestsUsed, result.Bounds.MaxRequests,
			result.Bounds.ResponseBytesUsed, result.Bounds.MaxResponseBytes)
	}
	fmt.Fprintf(&out, "- Nodes: `%d`; edges: `%d`; evidence: `%d`; sources: `%d`\n\n",
		result.Summary.NodeCount, result.Summary.EdgeCount, result.Summary.EvidenceCount, result.Summary.SourceCount)

	sourceRows := make([][]string, 0, len(result.Sources))
	for _, source := range result.Sources {
		row := []string{
			source.Kind, string(source.Status), fmt.Sprint(source.Complete),
			fmt.Sprint(source.Count), fmt.Sprint(source.Truncated),
			string(source.Stability), source.PartialReason,
		}
		if result.SchemaVersion >= jiraIssueGraphSchemaVersionV2 {
			depth := ""
			if source.NodeDepth != nil {
				depth = fmt.Sprint(*source.NodeDepth)
			}
			row = append([]string{source.NodeID, depth}, row...)
		}
		sourceRows = append(sourceRows, row)
	}
	out.WriteString("## Sources\n\n")
	sourceHeader := []string{"Source", "Status", "Complete", "Count", "Truncated", "Stability", "Reason"}
	if result.SchemaVersion >= jiraIssueGraphSchemaVersionV2 {
		sourceHeader = append([]string{"Node", "Depth"}, sourceHeader...)
	}
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
	nodeRows := make([][]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodeRows = append(nodeRows, []string{
			node.ID, node.Kind, string(node.State), fmt.Sprint(node.Depth),
			fmt.Sprint(node.Expanded), node.Label,
		})
	}
	out.WriteString(MarkdownTable([]string{"ID", "Kind", "State", "Depth", "Expanded", "Label"}, nodeRows))
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
