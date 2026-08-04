package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// JiraIssueGraphViewSchemaVersion is the released MCP graph schema.
	JiraIssueGraphViewSchemaVersion = 2

	jiraGraphWireMaxBytes         = 1 << 20
	jiraGraphWireMaxSourceBytes   = 1 << 20
	jiraGraphWireMaxResponseBytes = 16 << 20
	jiraGraphWireMaxEvidence      = 500
)

var (
	jiraGraphWireKey            = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}-[1-9][0-9]*$`)
	jiraGraphWirePositiveID     = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
	jiraGraphWireHex            = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jiraGraphWireSHA            = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	jiraGraphWireIID            = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	jiraGraphWireProjectSegment = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	jiraGraphWireSafeSourceID   = regexp.MustCompile(`^[A-Za-z0-9._:/~-]{1,256}$`)
	jiraGraphWireSafePointer    = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+)(?:/[A-Za-z0-9._~-]+)*$`)
)

// JiraIssueGraphView is the evaluator-owned released jira_issue_graph wire.
// It intentionally excludes product-private and narrative fields.
type JiraIssueGraphView struct {
	SchemaVersion int                      `json:"schema_version"`
	RootID        string                   `json:"root_id"`
	Complete      bool                     `json:"complete"`
	Truncated     bool                     `json:"truncated"`
	Bounds        JiraIssueGraphBounds     `json:"bounds"`
	Summary       JiraIssueGraphSummary    `json:"summary"`
	Nodes         []JiraIssueGraphNode     `json:"nodes"`
	Edges         []JiraIssueGraphEdge     `json:"edges"`
	Sources       []JiraIssueGraphSource   `json:"sources"`
	Frontier      []JiraIssueGraphFrontier `json:"frontier"`
	Warnings      []string                 `json:"warnings"`
}

type JiraIssueGraphBounds struct {
	RequestedDepth     int  `json:"requested_depth"`
	IncludeDevelopment bool `json:"include_development,omitempty"`
	MaxNodes           int  `json:"max_nodes"`
	MaxEdges           int  `json:"max_edges"`
	MaxEvidence        int  `json:"max_evidence"`
	MaxSourceBytes     int  `json:"max_source_bytes"`
	ExpandedNodes      int  `json:"expanded_node_count"`
	FollowedNodes      int  `json:"followed_node_count"`
	AttemptedNodes     int  `json:"attempted_node_count"`
	MaxRequests        int  `json:"max_requests"`
	RequestsUsed       int  `json:"requests_used"`
	MaxResponseBytes   int  `json:"max_response_bytes"`
	ResponseBytesUsed  int  `json:"response_bytes_used"`
	MaxSources         int  `json:"max_sources"`
	MaxFrontier        int  `json:"max_frontier"`
	FrontierCount      int  `json:"frontier_count"`
	FrontierTruncated  bool `json:"frontier_truncated"`
}

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

type JiraIssueGraphNode struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Service    string             `json:"service"`
	ExternalID string             `json:"external_id,omitempty"`
	URL        string             `json:"url,omitempty"`
	State      string             `json:"state"`
	Expanded   bool               `json:"expanded"`
	Depth      int                `json:"depth"`
	Stability  string             `json:"stability"`
	SCM        *JiraIssueGraphSCM `json:"scm,omitempty"`
}

type JiraIssueGraphSCM struct {
	Host              string `json:"host"`
	ProjectPath       string `json:"project_path"`
	CommitSHA         string `json:"commit_sha,omitempty"`
	BranchName        string `json:"branch_name,omitempty"`
	MergeRequestIID   string `json:"merge_request_iid,omitempty"`
	MergeRequestState string `json:"merge_request_state,omitempty"`
}

type JiraIssueGraphEvidence struct {
	Collector    string `json:"collector"`
	SourceNodeID string `json:"source_node_id,omitempty"`
	SourceKind   string `json:"source_kind"`
	SourceID     string `json:"source_id,omitempty"`
	JSONPointer  string `json:"json_pointer,omitempty"`
	Extraction   string `json:"extraction"`
}

type JiraIssueGraphEdge struct {
	ID           string                   `json:"id"`
	From         string                   `json:"from"`
	To           string                   `json:"to"`
	Kind         string                   `json:"kind"`
	RelationType string                   `json:"relation_type,omitempty"`
	Relation     string                   `json:"relation,omitempty"`
	Direction    string                   `json:"direction"`
	Current      bool                     `json:"current"`
	Confidence   string                   `json:"confidence"`
	Stability    string                   `json:"stability"`
	Evidence     []JiraIssueGraphEvidence `json:"evidence"`
}

type JiraIssueGraphSource struct {
	NodeID        string `json:"node_id"`
	NodeDepth     int    `json:"node_depth"`
	Kind          string `json:"kind"`
	Requested     bool   `json:"requested"`
	Status        string `json:"status"`
	Complete      bool   `json:"complete"`
	Count         int    `json:"count"`
	Truncated     bool   `json:"truncated"`
	PartialReason string `json:"partial_reason,omitempty"`
	Stability     string `json:"stability"`
}

type JiraIssueGraphFrontier struct {
	NodeID string `json:"node_id"`
	Depth  int    `json:"depth"`
	Reason string `json:"reason"`
}

// DecodeJiraIssueGraphView strictly decodes and independently reconciles the
// released schema-v2 graph projection.
func DecodeJiraIssueGraphView(r io.Reader) (JiraIssueGraphView, error) {
	limited := &io.LimitedReader{R: r, N: jiraGraphWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return JiraIssueGraphView{}, fmt.Errorf("read jira issue graph wire: %w", err)
	}
	if limited.N <= 0 {
		return JiraIssueGraphView{}, fmt.Errorf("jira issue graph wire exceeds %d bytes", jiraGraphWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return JiraIssueGraphView{}, fmt.Errorf("decode jira issue graph wire: %w", err)
	}
	if err := validateJiraGraphWireMembers(data); err != nil {
		return JiraIssueGraphView{}, fmt.Errorf("decode jira issue graph wire: %w", err)
	}
	var view JiraIssueGraphView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return JiraIssueGraphView{}, fmt.Errorf("decode jira issue graph wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return JiraIssueGraphView{}, fmt.Errorf("validate jira issue graph view: %w", err)
	}
	return view, nil
}

func validateJiraGraphWireMembers(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("graph must be an object")
	}
	warnings, warningsPresent := root["warnings"]
	delete(root, "warnings")
	if err := jiraGraphWireMembers(root, "graph", []string{
		"schema_version", "root_id", "complete", "truncated", "bounds", "summary",
		"nodes", "edges", "sources", "frontier",
	}, nil); err != nil {
		return err
	}
	if !warningsPresent {
		return fmt.Errorf("graph.warnings is required")
	}
	root["warnings"] = warnings
	bounds, err := jiraGraphWireObject(root["bounds"], "graph.bounds")
	if err != nil {
		return err
	}
	if err := jiraGraphWireMembers(bounds, "graph.bounds", []string{
		"requested_depth", "max_nodes", "max_edges", "max_evidence", "max_source_bytes",
		"expanded_node_count", "followed_node_count", "attempted_node_count", "max_requests",
		"requests_used", "max_response_bytes", "response_bytes_used", "max_sources",
		"max_frontier", "frontier_count", "frontier_truncated",
	}, []string{"include_development"}); err != nil {
		return err
	}
	summary, err := jiraGraphWireObject(root["summary"], "graph.summary")
	if err != nil {
		return err
	}
	if err := jiraGraphWireMembers(summary, "graph.summary", []string{
		"node_count", "edge_count", "evidence_count", "source_count", "incomplete_source_count",
		"source_status_counts", "node_count_matches_nodes", "edge_count_matches_edges",
		"evidence_count_matches_edges", "source_count_matches_sources",
		"source_status_count_matches_sources", "incomplete_source_count_matches_sources",
		"expanded_count_matches_nodes", "complete_matches_sources",
	}, nil); err != nil {
		return err
	}
	statusCounts, err := jiraGraphWireObject(summary["source_status_counts"], "graph.summary.source_status_counts")
	if err != nil {
		return err
	}
	if err := jiraGraphWireMembers(statusCounts, "graph.summary.source_status_counts",
		[]string{"complete", "empty", "partial", "forbidden", "unsupported", "skipped"}, nil); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(root["nodes"], "graph.nodes", validateJiraGraphNodeMembers); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(root["edges"], "graph.edges", validateJiraGraphEdgeMembers); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(root["sources"], "graph.sources", validateJiraGraphSourceMembers); err != nil {
		return err
	}
	if err := jiraGraphWireArrayMembers(root["frontier"], "graph.frontier", func(object map[string]json.RawMessage, owner string) error {
		return jiraGraphWireMembers(object, owner, []string{"node_id", "depth", "reason"}, nil)
	}); err != nil {
		return err
	}
	// The released product projection currently encodes the no-warning state as
	// JSON null because its copied source slice remains nil. Preserve that exact
	// wire compatibility while treating null and [] identically in validation.
	if jiraGraphWireNull(root["warnings"]) {
		return nil
	}
	return jiraGraphWireArrayMembers(root["warnings"], "graph.warnings", nil)
}

func validateJiraGraphNodeMembers(node map[string]json.RawMessage, owner string) error {
	if err := jiraGraphWireMembers(node, owner,
		[]string{"id", "kind", "service", "state", "expanded", "depth", "stability"},
		[]string{"external_id", "url", "scm"}); err != nil {
		return err
	}
	if raw, ok := node["scm"]; ok {
		scm, err := jiraGraphWireObject(raw, owner+".scm")
		if err != nil {
			return err
		}
		return jiraGraphWireMembers(scm, owner+".scm", []string{"host", "project_path"},
			[]string{"commit_sha", "branch_name", "merge_request_iid", "merge_request_state"})
	}
	return nil
}

func validateJiraGraphEdgeMembers(edge map[string]json.RawMessage, owner string) error {
	if err := jiraGraphWireMembers(edge, owner,
		[]string{"id", "from", "to", "kind", "direction", "current", "confidence", "stability", "evidence"},
		[]string{"relation_type", "relation"}); err != nil {
		return err
	}
	return jiraGraphWireArrayMembers(edge["evidence"], owner+".evidence", func(evidence map[string]json.RawMessage, evidenceOwner string) error {
		return jiraGraphWireMembers(evidence, evidenceOwner,
			[]string{"collector", "source_kind", "extraction"},
			[]string{"source_node_id", "source_id", "json_pointer"})
	})
}

func validateJiraGraphSourceMembers(source map[string]json.RawMessage, owner string) error {
	return jiraGraphWireMembers(source, owner,
		[]string{"node_id", "node_depth", "kind", "requested", "status", "complete", "count", "truncated", "stability"},
		[]string{"partial_reason"})
}

func jiraGraphWireMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		raw, ok := object[name]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
		if jiraGraphWireNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
		if raw, ok := object[name]; ok && jiraGraphWireNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func jiraGraphWireObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraGraphWireArrayMembers(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	for index, value := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		if validate == nil {
			if jiraGraphWireNull(value) {
				return fmt.Errorf("%s must not be null", itemOwner)
			}
			continue
		}
		object, err := jiraGraphWireObject(value, itemOwner)
		if err != nil {
			return err
		}
		if err := validate(object, itemOwner); err != nil {
			return err
		}
	}
	return nil
}

func jiraGraphWireNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (view JiraIssueGraphView) validate() error {
	bounds := view.Bounds
	sourceKinds := jiraGraphWireSourceKinds(bounds.IncludeDevelopment)
	if view.SchemaVersion != JiraIssueGraphViewSchemaVersion || !jiraGraphWireJiraID(view.RootID) {
		return fmt.Errorf("schema version or root is invalid")
	}
	if bounds.RequestedDepth < 0 || bounds.RequestedDepth > 2 || bounds.MaxNodes < 1 || bounds.MaxNodes > 100 ||
		bounds.MaxEdges < 1 || bounds.MaxEdges > 500 || bounds.MaxEvidence != jiraGraphWireMaxEvidence ||
		bounds.MaxSourceBytes != jiraGraphWireMaxSourceBytes || bounds.MaxRequests < 1 || bounds.MaxRequests > 100 ||
		bounds.MaxResponseBytes != jiraGraphWireMaxResponseBytes || bounds.MaxSources != bounds.MaxNodes*len(sourceKinds)+1 ||
		bounds.MaxFrontier != bounds.MaxNodes {
		return fmt.Errorf("bounds are invalid")
	}
	if len(view.Nodes) > bounds.MaxNodes || len(view.Edges) > bounds.MaxEdges || len(view.Sources) > bounds.MaxSources ||
		len(view.Frontier) > bounds.MaxFrontier || bounds.FrontierCount != len(view.Frontier) ||
		bounds.RequestsUsed < 0 || bounds.RequestsUsed > bounds.MaxRequests ||
		bounds.ResponseBytesUsed < 0 || bounds.ResponseBytesUsed > bounds.MaxResponseBytes ||
		bounds.AttemptedNodes < 0 || bounds.AttemptedNodes > bounds.MaxNodes ||
		bounds.ExpandedNodes < 0 || bounds.ExpandedNodes > bounds.AttemptedNodes ||
		bounds.FollowedNodes != max(0, bounds.AttemptedNodes-1) ||
		(bounds.FrontierTruncated && len(view.Frontier) != bounds.MaxFrontier) {
		return fmt.Errorf("usage exceeds a hard bound")
	}

	nodes := make(map[string]JiraIssueGraphNode, len(view.Nodes))
	expanded := 0
	for index, node := range view.Nodes {
		if err := validateJiraGraphWireNode(node, bounds); err != nil {
			return fmt.Errorf("node %d: %w", index, err)
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("node inventory contains a duplicate")
		}
		if index > 0 && jiraGraphWireNodeSortKey(view.Nodes[index-1]) >= jiraGraphWireNodeSortKey(node) {
			return fmt.Errorf("node inventory is unordered")
		}
		if node.Expanded {
			expanded++
		}
		nodes[node.ID] = node
	}
	root, ok := nodes[view.RootID]
	if !ok || root.Depth != 0 || root.Kind != "jira_issue" ||
		(root.Expanded && root.State != "resolved") || (!root.Expanded && root.State != "unresolved") {
		return fmt.Errorf("root node is invalid")
	}

	evidenceCount := 0
	developmentEdges := map[string]int{}
	developmentArtifacts := map[string]int{}
	developmentTargetEdges := map[string]int{}
	developmentTargetDepth := map[string]int{}
	developmentProjects := map[string]bool{}
	developmentProjectEdges := map[string]bool{}
	for index, edge := range view.Edges {
		if err := validateJiraGraphWireEdge(edge, nodes, bounds.IncludeDevelopment); err != nil {
			return fmt.Errorf("edge %d: %w", index, err)
		}
		if index > 0 && jiraGraphWireEdgeSortKey(view.Edges[index-1]) >= jiraGraphWireEdgeSortKey(edge) {
			return fmt.Errorf("edge inventory is unordered")
		}
		evidenceCount += len(edge.Evidence)
		if strings.HasPrefix(edge.Kind, "development_") {
			developmentEdges[edge.From]++
			developmentTargetEdges[edge.To]++
			candidateDepth := nodes[edge.From].Depth + 1
			if current, found := developmentTargetDepth[edge.To]; !found || candidateDepth < current {
				developmentTargetDepth[edge.To] = candidateDepth
			}
			if edge.Kind != "development_project" {
				developmentArtifacts[edge.From]++
				scm := nodes[edge.To].SCM
				developmentProjects[edge.From+"\x00"+scm.Host+"\x00"+scm.ProjectPath] = true
			} else {
				scm := nodes[edge.To].SCM
				developmentProjectEdges[edge.From+"\x00"+scm.Host+"\x00"+scm.ProjectPath] = true
			}
		}
	}
	for _, node := range view.Nodes {
		if strings.HasPrefix(node.Kind, "gitlab_") &&
			(developmentTargetEdges[node.ID] == 0 || node.Depth != developmentTargetDepth[node.ID]) {
			return fmt.Errorf("gitlab node has no valid development edge depth")
		}
	}
	for identity := range developmentProjectEdges {
		if !developmentProjects[identity] {
			return fmt.Errorf("development project has no artifact identity")
		}
	}
	for identity := range developmentProjects {
		if !developmentProjectEdges[identity] {
			return fmt.Errorf("development artifact has no project edge")
		}
	}

	statusCounts := map[string]int{"complete": 0, "empty": 0, "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0}
	sourcesByNode := map[string]map[string]bool{}
	sourceRank := make(map[string]int, len(sourceKinds))
	for index, kind := range sourceKinds {
		sourceRank[kind] = index
	}
	incomplete := 0
	truncated := false
	for index, source := range view.Sources {
		if err := validateJiraGraphWireSource(source, nodes, sourceKinds, developmentEdges, developmentArtifacts); err != nil {
			return fmt.Errorf("source %d: %w", index, err)
		}
		if sourcesByNode[source.NodeID] == nil {
			sourcesByNode[source.NodeID] = map[string]bool{}
		}
		if sourcesByNode[source.NodeID][source.Kind] {
			return fmt.Errorf("source inventory contains a duplicate")
		}
		if index > 0 && !jiraGraphWireSourceLess(view.Sources[index-1], source, sourceRank) {
			return fmt.Errorf("source inventory is unordered")
		}
		sourcesByNode[source.NodeID][source.Kind] = true
		statusCounts[source.Status]++
		if !source.Complete {
			incomplete++
		}
		truncated = truncated || source.Truncated
	}
	for _, node := range view.Nodes {
		inventory := sourcesByNode[node.ID]
		hasInventory := false
		for _, kind := range sourceKinds {
			hasInventory = hasInventory || inventory[kind]
		}
		if !node.Expanded && !hasInventory {
			continue
		}
		for _, kind := range sourceKinds {
			if !inventory[kind] {
				return fmt.Errorf("attempted jira node source inventory is incomplete")
			}
		}
	}

	for index, item := range view.Frontier {
		if item.Depth < 0 || item.Depth > bounds.RequestedDepth+1 || !jiraGraphWireFrontierID(item.NodeID) ||
			!jiraGraphWireOneOf(item.Reason, "output_limit", "request_limit", "byte_limit") {
			return fmt.Errorf("frontier item %d is invalid", index)
		}
		if node, exists := nodes[item.NodeID]; exists && node.Depth != item.Depth {
			return fmt.Errorf("frontier item %d depth does not match its node", index)
		}
		if index > 0 && jiraGraphWireFrontierSortKey(view.Frontier[index-1]) >= jiraGraphWireFrontierSortKey(item) {
			return fmt.Errorf("frontier is unordered")
		}
	}
	truncated = truncated || len(view.Frontier) > 0 || bounds.FrontierTruncated
	if !root.Expanded {
		if len(view.Nodes) != 1 || len(view.Edges) != 0 || expanded != 0 || len(view.Frontier) != 1 ||
			view.Frontier[0].NodeID != view.RootID || view.Frontier[0].Depth != 0 ||
			!jiraGraphWireOneOf(view.Frontier[0].Reason, "request_limit", "byte_limit") {
			return fmt.Errorf("root budget qualification is invalid")
		}
	} else if expanded < 1 || bounds.AttemptedNodes < 1 {
		return fmt.Errorf("expanded root accounting is invalid")
	}

	summary := view.Summary
	if summary.NodeCount != len(view.Nodes) || summary.EdgeCount != len(view.Edges) ||
		summary.EvidenceCount != evidenceCount || summary.EvidenceCount > bounds.MaxEvidence ||
		summary.SourceCount != len(view.Sources) || summary.IncompleteSourceCount != incomplete ||
		!jiraGraphWireEqualCounts(summary.SourceStatusCounts, statusCounts) || bounds.ExpandedNodes != expanded ||
		view.Complete != (incomplete == 0) || view.Truncated != truncated ||
		!summary.NodeCountMatchesNodes || !summary.EdgeCountMatchesEdges || !summary.EvidenceCountMatchesEdges ||
		!summary.SourceCountMatchesSources || !summary.SourceStatusCountsMatch || !summary.IncompleteCountMatches ||
		!summary.ExpandedCountMatchesNodes || !summary.CompleteMatchesSources {
		return fmt.Errorf("summary reconciliation failed")
	}
	if incomplete == 0 && len(view.Warnings) != 0 || incomplete > 0 &&
		(len(view.Warnings) != 1 || view.Warnings[0] != "one or more requested graph sources are incomplete") {
		return fmt.Errorf("warnings do not match completeness")
	}
	return nil
}

func validateJiraGraphWireNode(node JiraIssueGraphNode, bounds JiraIssueGraphBounds) error {
	if node.ID == "" || node.Depth < 0 || node.Depth > bounds.RequestedDepth+1 ||
		!jiraGraphWireOneOf(node.State, "resolved", "stub", "unresolved", "forbidden", "missing") ||
		!jiraGraphWireOneOf(node.Stability, "public_api", "experimental_api", "heuristic") {
		return fmt.Errorf("identity, depth, state, or stability is invalid")
	}
	if node.Expanded && (node.Kind != "jira_issue" || node.State != "resolved" || node.Depth > bounds.RequestedDepth) {
		return fmt.Errorf("expanded node is invalid")
	}
	switch node.Kind {
	case "jira_issue":
		key := strings.TrimPrefix(node.ID, "jira:issue:")
		if node.ID != "jira:issue:"+key || !jiraGraphWireKey.MatchString(key) || node.Service != "jira" || node.ExternalID != key || node.SCM != nil {
			return fmt.Errorf("jira identity is invalid")
		}
	case "confluence_page":
		id := strings.TrimPrefix(node.ID, "confluence:page:")
		if node.ID != "confluence:page:"+id || !jiraGraphWirePositiveID.MatchString(id) || node.Service != "confluence" || node.ExternalID != id || node.SCM != nil {
			return fmt.Errorf("confluence identity is invalid")
		}
	case "attachment":
		id := strings.TrimPrefix(node.ID, "jira:attachment:")
		if node.ID != "jira:attachment:"+id || !jiraGraphWirePositiveID.MatchString(id) || node.Service != "jira" || node.ExternalID != id || node.SCM != nil {
			return fmt.Errorf("attachment identity is invalid")
		}
	case "url":
		if node.Service != "external" || (!strings.HasPrefix(node.ID, "url:") && !strings.HasPrefix(node.ID, "candidate:url:")) || node.ExternalID != "" || node.SCM != nil {
			return fmt.Errorf("url identity is invalid")
		}
	default:
		if !strings.HasPrefix(node.Kind, "gitlab_") || !bounds.IncludeDevelopment || !validateJiraGraphWireSCMNode(node) {
			return fmt.Errorf("node kind is invalid")
		}
		return nil
	}
	if !jiraGraphWireSafeURL(node.URL) {
		return fmt.Errorf("url is invalid")
	}
	return nil
}

func validateJiraGraphWireSCMNode(node JiraIssueGraphNode) bool {
	if node.SCM == nil || node.Service != "gitlab" || node.ExternalID != "" || node.URL != "" || node.State != "stub" ||
		node.Expanded || node.Depth < 1 || node.Stability != "experimental_api" || !jiraGraphWireProject(node.SCM.Host, node.SCM.ProjectPath) {
		return false
	}
	scm := node.SCM
	projectHash := jiraGraphWireHash("https://" + scm.Host + "\x00" + scm.ProjectPath)
	switch node.Kind {
	case "gitlab_project":
		return node.ID == "gitlab:project:"+projectHash && scm.CommitSHA == "" && scm.BranchName == "" && scm.MergeRequestIID == "" && scm.MergeRequestState == ""
	case "gitlab_commit":
		return node.ID == "gitlab:commit:"+projectHash+":"+scm.CommitSHA && jiraGraphWireSHA.MatchString(scm.CommitSHA) && scm.BranchName == "" && scm.MergeRequestIID == "" && scm.MergeRequestState == ""
	case "gitlab_branch":
		return node.ID == "gitlab:branch:"+projectHash+":"+jiraGraphWireHash(scm.BranchName) && scm.CommitSHA == "" && jiraGraphWireBranch(scm.BranchName) && scm.MergeRequestIID == "" && scm.MergeRequestState == ""
	case "gitlab_merge_request":
		return node.ID == "gitlab:merge_request:"+projectHash+":"+scm.MergeRequestIID && scm.CommitSHA == "" && scm.BranchName == "" && jiraGraphWireIID.MatchString(scm.MergeRequestIID) && jiraGraphWireOneOf(scm.MergeRequestState, "open", "merged", "closed", "unknown")
	}
	return false
}

func validateJiraGraphWireEdge(edge JiraIssueGraphEdge, nodes map[string]JiraIssueGraphNode, includeDevelopment bool) error {
	from, fromOK := nodes[edge.From]
	to, toOK := nodes[edge.To]
	if !fromOK || !toOK || edge.From == edge.To || !jiraGraphWireHex.MatchString(strings.TrimPrefix(edge.ID, "edge:")) ||
		edge.ID != jiraGraphWireEdgeID(edge) || len(edge.Evidence) == 0 || strings.HasPrefix(from.Kind, "gitlab_") || !edge.Current ||
		!jiraGraphWireOneOf(edge.Direction, "inward", "outward", "outbound") ||
		!jiraGraphWireOneOf(edge.Confidence, "exact", "high", "candidate") ||
		!jiraGraphWireOneOf(edge.Stability, "public_api", "experimental_api", "heuristic") ||
		!jiraGraphWireSafeText(edge.RelationType) || !jiraGraphWireSafeText(edge.Relation) {
		return fmt.Errorf("identity, endpoints, or qualification is invalid")
	}
	development := strings.HasPrefix(edge.Kind, "development_")
	if !jiraGraphWireOneOf(edge.Kind, "jira_link", "parent_of", "child_of", "epic_of", "attached", "mentions", "remote_link",
		"development_project", "development_commit", "development_branch", "development_merge_request") ||
		strings.HasPrefix(to.Kind, "gitlab_") != development {
		return fmt.Errorf("kind or target boundary is invalid")
	}
	if development && (!includeDevelopment || edge.Stability != "experimental_api" || edge.Direction != "outbound" ||
		edge.Confidence != "exact" || from.Kind != "jira_issue" || to.Kind != "gitlab_"+strings.TrimPrefix(edge.Kind, "development_") ||
		to.Depth > from.Depth+1 || len(edge.Evidence) != 1 || edge.RelationType != "" || edge.Relation != "") {
		return fmt.Errorf("development edge is invalid")
	}
	seen := map[string]bool{}
	previousIdentity := ""
	for index, evidence := range edge.Evidence {
		identity := strings.Join([]string{evidence.Collector, evidence.SourceKind, evidence.SourceID, evidence.JSONPointer, evidence.Extraction}, "\x00")
		if seen[identity] || index > 0 && previousIdentity >= identity || evidence.SourceNodeID != edge.From ||
			!jiraGraphWireOneOf(evidence.SourceKind, "field", "property", "comment", "worklog", "remote_link", "development_detail") ||
			!jiraGraphWireOneOf(evidence.Extraction, "structured", "absolute_url", "jira_key", "confluence_page_id", "service_url") ||
			!jiraGraphWireSafeEvidenceCoordinate(evidence) {
			return fmt.Errorf("evidence provenance is invalid")
		}
		seen[identity] = true
		previousIdentity = identity
		validDevelopment := evidence.Collector == "development" && evidence.SourceKind == "development_detail" &&
			evidence.Extraction == "structured" && evidence.JSONPointer == "" && jiraGraphWireHex.MatchString(evidence.SourceID) &&
			evidence.SourceID == jiraGraphWireDevelopmentEvidenceID(edge.Kind, to.SCM)
		if development != validDevelopment || !development && (evidence.Collector == "development" || evidence.SourceKind == "development_detail") {
			return fmt.Errorf("development evidence provenance is invalid")
		}
	}
	return nil
}

func validateJiraGraphWireSource(source JiraIssueGraphSource, nodes map[string]JiraIssueGraphNode, sourceKinds []string, developmentEdges, developmentArtifacts map[string]int) error {
	node, ok := nodes[source.NodeID]
	if !ok || source.NodeDepth != node.Depth || !jiraGraphWireOneOf(source.Kind, sourceKinds...) || !source.Requested || source.Count < 0 ||
		!jiraGraphWireOneOf(source.Status, "complete", "empty", "partial", "forbidden", "unsupported", "skipped") ||
		source.Complete != (source.Status == "complete" || source.Status == "empty") {
		return fmt.Errorf("identity, status, or count is invalid")
	}
	if source.Complete && (source.Status == "empty") != (source.Count == 0) {
		return fmt.Errorf("complete status does not match count")
	}
	partialReason := jiraGraphWireOneOf(source.PartialReason, "inspection_limit", "output_limit", "request_failed", "request_limit", "byte_limit", "dependency_unavailable", "malformed_response", "policy")
	if source.Status == "partial" && !partialReason || source.Status == "skipped" && source.PartialReason != "dependency_unavailable" ||
		source.Status != "partial" && source.Status != "skipped" && source.PartialReason != "" {
		return fmt.Errorf("partial reason is invalid")
	}
	wantTruncated := source.Status == "partial" && jiraGraphWireOneOf(source.PartialReason, "inspection_limit", "output_limit", "request_limit", "byte_limit")
	if source.Truncated != wantTruncated {
		return fmt.Errorf("truncation is invalid")
	}
	wantStability := "public_api"
	if source.Kind == "issue_properties" || source.Kind == "development" {
		wantStability = "experimental_api"
	}
	if source.Stability != wantStability || node.Kind != "jira_issue" {
		return fmt.Errorf("stability or owner is invalid")
	}
	if source.Kind == "development" {
		if source.Complete && source.Count != developmentArtifacts[source.NodeID] || !source.Complete && developmentEdges[source.NodeID] != 0 {
			return fmt.Errorf("development count is invalid")
		}
	}
	return nil
}

func jiraGraphWireSourceKinds(includeDevelopment bool) []string {
	kinds := []string{"issue_fields", "issue_links", "hierarchy", "attachments", "issue_properties", "comments", "worklogs", "remote_links"}
	if includeDevelopment {
		kinds = append(kinds, "development")
	}
	return kinds
}

func jiraGraphWireJiraID(id string) bool {
	key := strings.TrimPrefix(id, "jira:issue:")
	return id == "jira:issue:"+key && jiraGraphWireKey.MatchString(key)
}

func jiraGraphWireFrontierID(id string) bool {
	if jiraGraphWireJiraID(id) {
		return true
	}
	pageID := strings.TrimPrefix(id, "confluence:page:")
	return id == "confluence:page:"+pageID && jiraGraphWirePositiveID.MatchString(pageID)
}

func jiraGraphWireSafeEvidenceCoordinate(e JiraIssueGraphEvidence) bool {
	if e.SourceID != "" && (!jiraGraphWireSafeSourceID.MatchString(e.SourceID) || jiraGraphWireEvidenceSensitive(e.SourceID)) {
		return false
	}
	return e.JSONPointer == "" || len(e.JSONPointer) <= 1<<10 && jiraGraphWireSafePointer.MatchString(e.JSONPointer) && !jiraGraphWireEvidenceSensitive(e.JSONPointer)
}

func jiraGraphWireSafeText(value string) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && utf8.RuneCountInString(value) <= 160
}

func jiraGraphWireSafeURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 2<<10 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !jiraGraphWireOneOf(parsed.Scheme, "http", "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" ||
		(parsed.RawQuery != "" && parsed.RawQuery != "redacted=redacted") {
		return false
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if jiraGraphWireURLSensitive(segment) || len(segment) >= 24 {
			return false
		}
	}
	return true
}

func jiraGraphWireURLSensitive(value string) bool {
	if strings.Contains(value, "@") {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"access_token", "apikey", "api_key", "auth", "credential", "jwt", "password", "passwd", "secret", "session", "signature", "ticket", "token"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func jiraGraphWireEvidenceSensitive(value string) bool {
	return jiraGraphWireURLSensitive(value) || strings.Contains(strings.ToLower(value), "private")
}

func jiraGraphWireProject(host, projectPath string) bool {
	if host == "" || host != strings.ToLower(host) || len(projectPath) == 0 || len(projectPath) > 2<<10 || !utf8.ValidString(host) || !utf8.ValidString(projectPath) || len("https://"+host+"/"+projectPath) > 2<<10 {
		return false
	}
	parsed, err := url.Parse("https://" + host + "/")
	if err != nil || parsed.Scheme != "https" || parsed.Host != host || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || number == 443 || port != strconv.Itoa(number) {
			return false
		}
	}
	parts := strings.Split(projectPath, "/")
	if len(parts) < 2 || len(parts) > 32 || strings.HasSuffix(parts[len(parts)-1], ".git") {
		return false
	}
	for _, part := range parts {
		if !jiraGraphWireProjectSegment.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func jiraGraphWireBranch(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current == 0 || unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func jiraGraphWireDevelopmentEvidenceID(kind string, scm *JiraIssueGraphSCM) string {
	if scm == nil {
		return ""
	}
	return jiraGraphWireHash(strings.Join([]string{kind, scm.Host, scm.ProjectPath, scm.CommitSHA, scm.BranchName, scm.MergeRequestIID}, "\x00"))
}

func jiraGraphWireEdgeID(edge JiraIssueGraphEdge) string {
	return "edge:" + jiraGraphWireHash(strings.Join([]string{"atl-jira-graph-edge-v1", edge.From, edge.To, edge.Kind, edge.RelationType, edge.Relation, edge.Direction}, "\x00"))
}

func jiraGraphWireHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func jiraGraphWireNodeSortKey(node JiraIssueGraphNode) string {
	return fmt.Sprintf("%08d\x00%s\x00%s", node.Depth, node.Kind, node.ID)
}

func jiraGraphWireEdgeSortKey(edge JiraIssueGraphEdge) string {
	return strings.Join([]string{edge.From, edge.Kind, edge.RelationType, edge.Relation, edge.To, edge.Direction, edge.ID}, "\x00")
}

func jiraGraphWireFrontierSortKey(item JiraIssueGraphFrontier) string {
	return fmt.Sprintf("%08d\x00%s\x00%s", item.Depth, item.NodeID, item.Reason)
}

func jiraGraphWireSourceLess(left, right JiraIssueGraphSource, rank map[string]int) bool {
	if left.NodeDepth != right.NodeDepth {
		return left.NodeDepth < right.NodeDepth
	}
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	return rank[left.Kind] < rank[right.Kind]
}

func jiraGraphWireEqualCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if count < 0 || right[key] != count {
			return false
		}
	}
	return true
}

func jiraGraphWireOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
