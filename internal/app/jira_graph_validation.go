package app

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func validateJiraGraphV2Result(result *JiraIssueGraphResult) error {
	invalid := func(detail string) error {
		return fmt.Errorf("%w: Jira graph v2 %s", domain.ErrCheckFailed, detail)
	}
	if result == nil {
		return invalid("result is nil")
	}
	activeSourceKinds := jiraGraphSourceKinds(result.Bounds.IncludeDevelopment)
	if result.SchemaVersion != jiraIssueGraphSchemaVersionV2 ||
		result.Bounds.RequestedDepth < 0 || result.Bounds.RequestedDepth > jiraGraphMaxDepth ||
		result.Bounds.MaxNodes < 1 || result.Bounds.MaxNodes > jiraGraphMaxNodes ||
		result.Bounds.MaxEdges < 1 || result.Bounds.MaxEdges > jiraGraphMaxEdges ||
		result.Bounds.MaxEvidence < 1 || result.Bounds.MaxEvidence > jiraGraphMaxEvidence ||
		result.Bounds.MaxRequests < 1 || result.Bounds.MaxRequests > jiraGraphMaxRequests ||
		result.Bounds.MaxResponseBytes < 1 || result.Bounds.MaxResponseBytes > jiraGraphMaxResponseBytes ||
		result.Bounds.MaxSourceBytes != jiraGraphMaxSourceBytes ||
		result.Bounds.MaxSources != result.Bounds.MaxNodes*len(activeSourceKinds)+1 ||
		result.Bounds.MaxFrontier != result.Bounds.MaxNodes {
		return invalid("bounds are invalid")
	}
	if len(result.Nodes) > result.Bounds.MaxNodes || len(result.Edges) > result.Bounds.MaxEdges ||
		result.Summary.EvidenceCount > result.Bounds.MaxEvidence ||
		len(result.Sources) > result.Bounds.MaxSources || len(result.Frontier) > result.Bounds.MaxFrontier ||
		result.Bounds.FrontierCount != len(result.Frontier) ||
		result.Bounds.RequestsUsed < 0 || result.Bounds.RequestsUsed > result.Bounds.MaxRequests ||
		result.Bounds.ResponseBytesUsed < 0 || result.Bounds.ResponseBytesUsed > result.Bounds.MaxResponseBytes ||
		result.Bounds.AttemptedNodes < 0 || result.Bounds.AttemptedNodes > result.Bounds.MaxNodes ||
		result.Bounds.ExpandedNodes < 0 || result.Bounds.ExpandedNodes > result.Bounds.AttemptedNodes ||
		(result.Bounds.FrontierTruncated && len(result.Frontier) != result.Bounds.MaxFrontier) {
		return invalid("usage exceeds a hard bound")
	}
	nodes := make(map[string]domain.ArtifactGraphNode, len(result.Nodes))
	expanded := 0
	for index, node := range result.Nodes {
		if node.ID == "" || node.Depth < 0 || node.Depth > result.Bounds.RequestedDepth+1 ||
			!oneOf(node.Kind, "jira_issue", "confluence_page", "attachment", "url",
				"gitlab_project", "gitlab_commit", "gitlab_branch", "gitlab_merge_request") ||
			!oneOf(node.Service, "jira", "confluence", "external", "gitlab") ||
			!oneOf(string(node.State), "resolved", "stub", "unresolved", "forbidden", "missing") ||
			!oneOf(string(node.Stability), "public_api", "experimental_api", "heuristic") ||
			(index > 0 && graphV2NodeSortKey(result.Nodes[index-1]) >= graphV2NodeSortKey(node)) {
			return invalid("node inventory is invalid or unordered")
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			return invalid("node inventory contains a duplicate")
		}
		if node.Kind == "jira_issue" {
			key := strings.TrimPrefix(node.ID, "jira:issue:")
			if node.Service != "jira" || node.ID != "jira:issue:"+key ||
				!jiraGraphExactKey(key) || node.ExternalID != key {
				return invalid("Jira node identity is not canonical")
			}
		} else if node.Kind == "confluence_page" {
			id := strings.TrimPrefix(node.ID, "confluence:page:")
			if node.Service != "confluence" || node.ID != "confluence:page:"+id ||
				!graphNumericIDPattern.MatchString(id) || node.ExternalID != id {
				return invalid("Confluence node identity is not canonical")
			}
		} else if node.Kind == "attachment" {
			id := strings.TrimPrefix(node.ID, "jira:attachment:")
			if node.Service != "jira" || node.ID != "jira:attachment:"+id ||
				!graphNumericIDPattern.MatchString(id) || node.ExternalID != id {
				return invalid("attachment node identity is not canonical")
			}
		} else if strings.HasPrefix(node.Kind, "gitlab_") {
			if !result.Bounds.IncludeDevelopment || !validateJiraDevelopmentGraphNode(node) {
				return invalid("GitLab node identity is not canonical")
			}
		} else if node.Service != "external" ||
			(!strings.HasPrefix(node.ID, "url:") && !strings.HasPrefix(node.ID, "candidate:url:")) {
			return invalid("URL node service is invalid")
		}
		if !strings.HasPrefix(node.Kind, "gitlab_") && node.SCM != nil {
			return invalid("non-GitLab node contains SCM coordinates")
		}
		if node.Expanded {
			if node.Kind != "jira_issue" || node.State != domain.ArtifactNodeResolved || node.Depth > result.Bounds.RequestedDepth {
				return invalid("expanded node is invalid")
			}
			expanded++
		}
		nodes[node.ID] = node
	}
	root, ok := nodes[result.RootID]
	if !ok || root.Depth != 0 || root.Kind != "jira_issue" ||
		(root.Expanded && root.State != domain.ArtifactNodeResolved) ||
		(!root.Expanded && root.State != domain.ArtifactNodeUnresolved) {
		return invalid("root is invalid")
	}
	rootBudgetLimited := !root.Expanded
	evidenceCount := 0
	developmentArtifactEdges := map[string]int{}
	developmentEdgesBySource := map[string]int{}
	developmentTargetEdges := map[string]int{}
	developmentTargetDepth := map[string]int{}
	developmentArtifactProjects := map[string]bool{}
	developmentProjectEdges := map[string]bool{}
	for index, edge := range result.Edges {
		if edge.ID != graphEdgeID(edge) {
			return invalid("edge inventory has an invalid identity")
		}
		if edge.From == edge.To {
			return invalid("edge inventory contains a self-reference")
		}
		if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
			return invalid("edge inventory has an unknown endpoint")
		}
		if len(edge.Evidence) == 0 ||
			strings.HasPrefix(nodes[edge.From].Kind, "gitlab_") ||
			!edge.Current ||
			!oneOf(edge.Kind, "jira_link", "parent_of", "child_of", "epic_of", "attached", "mentions", "remote_link",
				"development_project", "development_commit", "development_branch", "development_merge_request") ||
			!oneOf(edge.Direction, "inward", "outward", "outbound") ||
			!oneOf(edge.Confidence, "exact", "high", "candidate") ||
			!oneOf(string(edge.Stability), "public_api", "experimental_api", "heuristic") {
			return invalid("edge inventory is invalid")
		}
		if index > 0 && graphEdgeSortKey(result.Edges[index-1]) >= graphEdgeSortKey(edge) {
			return invalid("edge inventory is unordered")
		}
		developmentEdge := strings.HasPrefix(edge.Kind, "development_")
		if strings.HasPrefix(nodes[edge.To].Kind, "gitlab_") != developmentEdge {
			return invalid("Development target boundary is invalid")
		}
		if developmentEdge && (!result.Bounds.IncludeDevelopment || edge.Stability != domain.ArtifactStabilityExperimentalAPI ||
			edge.Direction != "outbound" || edge.Confidence != "exact" || nodes[edge.From].Kind != "jira_issue" ||
			nodes[edge.To].Kind != "gitlab_"+strings.TrimPrefix(edge.Kind, "development_") ||
			nodes[edge.To].Depth > nodes[edge.From].Depth+1 || len(edge.Evidence) != 1 ||
			edge.RelationType != "" || edge.Relation != "") {
			return invalid("Development edge is invalid")
		}
		if developmentEdge {
			developmentEdgesBySource[edge.From]++
			developmentTargetEdges[edge.To]++
			candidateDepth := nodes[edge.From].Depth + 1
			if current, found := developmentTargetDepth[edge.To]; !found || candidateDepth < current {
				developmentTargetDepth[edge.To] = candidateDepth
			}
			if edge.Kind != "development_project" {
				developmentArtifactEdges[edge.From]++
				scm := nodes[edge.To].SCM
				developmentArtifactProjects[edge.From+"\x00"+scm.Host+"\x00"+scm.ProjectPath] = true
			} else {
				scm := nodes[edge.To].SCM
				developmentProjectEdges[edge.From+"\x00"+scm.Host+"\x00"+scm.ProjectPath] = true
			}
		}
		for evidenceIndex, evidence := range edge.Evidence {
			if evidence.SourceNodeID != edge.From || !oneOf(evidence.Collector, activeSourceKinds...) ||
				!oneOf(evidence.SourceKind, "field", "property", "comment", "worklog", "remote_link", "development_detail") ||
				!oneOf(evidence.Extraction, "structured", "absolute_url", "jira_key", "confluence_page_id", "service_url") ||
				(evidenceIndex > 0 && graphEvidenceKey(edge.Evidence[evidenceIndex-1]) >= graphEvidenceKey(evidence)) {
				return invalid("evidence provenance is invalid or unordered")
			}
			validDevelopmentEvidence := evidence.Collector == "development" && evidence.SourceKind == "development_detail" &&
				evidence.Extraction == "structured" && evidence.JSONPointer == "" && jiraDevelopmentSourceID.MatchString(evidence.SourceID) &&
				evidence.SourceID == jiraDevelopmentEvidenceSourceID(edge.Kind, nodes[edge.To].SCM)
			if developmentEdge != validDevelopmentEvidence ||
				(!developmentEdge && (evidence.Collector == "development" || evidence.SourceKind == "development_detail")) {
				return invalid("Development evidence provenance is invalid")
			}
			evidenceCount++
		}
	}
	for _, node := range result.Nodes {
		if strings.HasPrefix(node.Kind, "gitlab_") &&
			(developmentTargetEdges[node.ID] == 0 || node.Depth != developmentTargetDepth[node.ID]) {
			return invalid("GitLab node has no valid Development edge depth")
		}
	}
	for _, edge := range result.Edges {
		if edge.Kind == "development_project" {
			scm := nodes[edge.To].SCM
			if !developmentArtifactProjects[edge.From+"\x00"+scm.Host+"\x00"+scm.ProjectPath] {
				return invalid("Development project has no artifact identity")
			}
		}
	}
	for identity := range developmentArtifactProjects {
		if !developmentProjectEdges[identity] {
			return invalid("Development artifact has no project edge")
		}
	}
	rank := map[string]int{}
	for index, kind := range activeSourceKinds {
		rank[kind] = index
	}
	rank["confluence_metadata"] = len(activeSourceKinds)
	statusCounts := map[string]int{"complete": 0, "empty": 0, "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0}
	incomplete := 0
	truncated := false
	sourcesByNode := map[string]map[string]bool{}
	for index, source := range result.Sources {
		node, nodeOK := nodes[source.NodeID]
		kindRank, kindOK := rank[source.Kind]
		if !nodeOK || source.NodeDepth == nil || *source.NodeDepth != node.Depth || !kindOK || !source.Requested ||
			source.Count < 0 || source.Complete != (source.Status == domain.ArtifactSourceComplete || source.Status == domain.ArtifactSourceEmpty) {
			return invalid("source inventory is invalid")
		}
		if !oneOf(string(source.Status), "complete", "empty", "partial", "forbidden", "unsupported", "skipped") {
			return invalid("source status is invalid")
		}
		if index > 0 && !graphV2SourceLess(result.Sources[index-1], source, rank) {
			return invalid("source inventory is unordered")
		}
		if source.Status == domain.ArtifactSourcePartial && !domain.ValidArtifactPartialReason(source.PartialReason) {
			return invalid("partial source reason is invalid")
		}
		if source.Status == domain.ArtifactSourcePartial && source.Truncated !=
			oneOf(source.PartialReason, domain.ArtifactPartialInspectionLimit, domain.ArtifactPartialOutputLimit,
				domain.ArtifactPartialRequestLimit, domain.ArtifactPartialByteLimit) {
			return invalid("partial source truncation is invalid")
		}
		if source.Status == domain.ArtifactSourceSkipped && source.PartialReason != domain.ArtifactPartialDependencyUnavailable {
			return invalid("skipped source reason is invalid")
		}
		if source.Status != domain.ArtifactSourcePartial && source.Status != domain.ArtifactSourceSkipped && source.PartialReason != "" {
			return invalid("qualified source contains a partial reason")
		}
		if source.Status != domain.ArtifactSourcePartial && source.Truncated {
			return invalid("non-partial source is truncated")
		}
		if source.Stability != jiraGraphSourceStability(source.Kind) {
			return invalid("source stability is invalid")
		}
		if source.Kind == "development" && source.Complete && source.Count != developmentArtifactEdges[source.NodeID] {
			return invalid("Development source count is invalid")
		}
		if source.Kind == "development" && source.Complete &&
			(source.Status == domain.ArtifactSourceEmpty) != (source.Count == 0) {
			return invalid("Development source status does not match count")
		}
		if source.Kind == "development" && !source.Complete && developmentEdgesBySource[source.NodeID] != 0 {
			return invalid("incomplete Development source contains facts")
		}
		if source.Kind == "confluence_metadata" && source.NodeID != result.RootID {
			return invalid("Confluence metadata source is not rooted")
		}
		if source.Kind != "confluence_metadata" && node.Kind != "jira_issue" {
			return invalid("Jira collector is attached to a non-Jira node")
		}
		if sourcesByNode[source.NodeID] == nil {
			sourcesByNode[source.NodeID] = map[string]bool{}
		}
		if sourcesByNode[source.NodeID][source.Kind] {
			return invalid("source inventory contains a duplicate")
		}
		sourcesByNode[source.NodeID][source.Kind] = true
		statusCounts[string(source.Status)]++
		if !source.Complete {
			incomplete++
		}
		truncated = truncated || source.Truncated
		_ = kindRank
	}
	for _, node := range result.Nodes {
		kinds := sourcesByNode[node.ID]
		hasJiraInventory := false
		for _, kind := range activeSourceKinds {
			hasJiraInventory = hasJiraInventory || kinds[kind]
		}
		if !node.Expanded && !hasJiraInventory {
			continue
		}
		for _, kind := range activeSourceKinds {
			if !kinds[kind] {
				return invalid("attempted Jira node source inventory is incomplete")
			}
		}
	}
	if kinds := sourcesByNode[result.RootID]; kinds["confluence_metadata"] {
		confluenceCount := 0
		for _, node := range result.Nodes {
			if node.Kind == "confluence_page" {
				confluenceCount++
			}
		}
		for _, source := range result.Sources {
			if source.Kind == "confluence_metadata" && source.Count != confluenceCount {
				return invalid("Confluence metadata count is invalid")
			}
		}
	}
	for index, item := range result.Frontier {
		if item.NodeID == "" || item.Depth < 0 || item.Depth > result.Bounds.RequestedDepth+1 ||
			!jiraGraphV2FrontierID(item.NodeID) ||
			!oneOf(item.Reason, domain.ArtifactPartialOutputLimit, domain.ArtifactPartialRequestLimit, domain.ArtifactPartialByteLimit) ||
			(index > 0 && !graphV2FrontierLess(result.Frontier[index-1], item)) {
			return invalid("frontier is invalid or unordered")
		}
	}
	truncated = truncated || len(result.Frontier) > 0 || result.Bounds.FrontierTruncated
	if (len(result.Frontier) > 0 || result.Bounds.FrontierTruncated) && !result.Truncated {
		return invalid("frontier truncation is not reported")
	}
	if rootBudgetLimited {
		if len(result.Nodes) != 1 || len(result.Edges) != 0 || expanded != 0 ||
			len(result.Frontier) != 1 || result.Frontier[0].NodeID != result.RootID ||
			result.Frontier[0].Depth != 0 ||
			!oneOf(result.Frontier[0].Reason, domain.ArtifactPartialRequestLimit, domain.ArtifactPartialByteLimit) ||
			(len(result.Sources) != len(activeSourceKinds) &&
				len(result.Sources) != len(activeSourceKinds)+1) {
			return invalid("root budget qualification is invalid")
		}
		for _, source := range result.Sources {
			if source.NodeID != result.RootID ||
				(!oneOf(source.Kind, activeSourceKinds...) && source.Kind != "confluence_metadata") ||
				source.Status != domain.ArtifactSourcePartial || !source.Truncated ||
				source.PartialReason != result.Frontier[0].Reason {
				return invalid("root budget source qualification is invalid")
			}
		}
	} else if expanded < 1 || result.Bounds.AttemptedNodes < 1 {
		return invalid("expanded root accounting is invalid")
	}
	if result.Bounds.ExpandedNodes != expanded || result.Bounds.AttemptedNodes < expanded ||
		result.Bounds.FollowedNodes != max(0, result.Bounds.AttemptedNodes-1) ||
		result.Summary.NodeCount != len(result.Nodes) || result.Summary.EdgeCount != len(result.Edges) ||
		result.Summary.EvidenceCount != evidenceCount || result.Summary.SourceCount != len(result.Sources) ||
		result.Summary.IncompleteSourceCount != incomplete || !equalStringIntMap(result.Summary.SourceStatusCounts, statusCounts) ||
		result.Complete != (incomplete == 0) || result.Truncated != truncated ||
		(incomplete == 0 && len(result.Warnings) != 0) ||
		(incomplete > 0 && (len(result.Warnings) != 1 || result.Warnings[0] != "one or more requested graph sources are incomplete")) ||
		!result.Summary.NodeCountMatchesNodes ||
		!result.Summary.EdgeCountMatchesEdges || !result.Summary.EvidenceCountMatchesEdges ||
		!result.Summary.SourceCountMatchesSources || !result.Summary.SourceStatusCountsMatch ||
		!result.Summary.IncompleteCountMatches || !result.Summary.ExpandedCountMatchesNodes ||
		!result.Summary.CompleteMatchesSources {
		return invalid("summary reconciliation failed")
	}
	return nil
}
