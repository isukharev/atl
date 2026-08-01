package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraIssueGraphDefaultMaxNodes    = 50
	jiraIssueGraphMaxMaxNodes        = 100
	jiraIssueGraphDefaultMaxEdges    = 200
	jiraIssueGraphMaxMaxEdges        = 500
	jiraIssueGraphDefaultMaxRequests = 50
	jiraIssueGraphMaxMaxRequests     = 100
	jiraIssueGraphMaxDepth           = 2
	jiraIssueGraphFixedMaxEvidence   = 500
	jiraIssueGraphFixedResponseBytes = 16 << 20
	jiraIssueGraphMaxLabelRunes      = 160
	jiraIssueGraphMaxURLBytes        = 2 << 10
)

var (
	jiraIssueGraphSafeID      = regexp.MustCompile(`^[A-Za-z0-9._:/~-]{1,256}$`)
	jiraIssueGraphSafePointer = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+)(?:/[A-Za-z0-9._~-]+)*$`)
)

// JiraIssueGraphInput is deliberately smaller than the CLI graph surface. It
// exposes only the stable Jira collectors and never enables Confluence reads or
// the deferred Development source.
type JiraIssueGraphInput struct {
	Key         string `json:"key" jsonschema:"exact canonical uppercase Jira issue key; required"`
	Depth       int    `json:"depth,omitempty" jsonschema:"exact structured Jira traversal depth from 0 to 2; default 0"`
	MaxNodes    int    `json:"max_nodes,omitempty" jsonschema:"node bound from 1 to 100; default 50"`
	MaxEdges    int    `json:"max_edges,omitempty" jsonschema:"edge bound from 1 to 500; default 200"`
	MaxRequests int    `json:"max_requests,omitempty" jsonschema:"physical HTTP attempt bound from 1 to 100; default 50"`
	MaxBytes    int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraIssueGraphOutput struct {
	SchemaVersion int                          `json:"schema_version"`
	RootID        string                       `json:"root_id"`
	Complete      bool                         `json:"complete"`
	Truncated     bool                         `json:"truncated"`
	Bounds        JiraIssueGraphBoundsOutput   `json:"bounds"`
	Summary       JiraIssueGraphSummaryOutput  `json:"summary"`
	Nodes         []JiraIssueGraphNodeOutput   `json:"nodes"`
	Edges         []JiraIssueGraphEdgeOutput   `json:"edges"`
	Sources       []JiraIssueGraphSourceOutput `json:"sources"`
	Frontier      []JiraIssueGraphFrontier     `json:"frontier"`
	Warnings      []string                     `json:"warnings"`
}

type JiraIssueGraphBoundsOutput struct {
	RequestedDepth    int  `json:"requested_depth"`
	MaxNodes          int  `json:"max_nodes"`
	MaxEdges          int  `json:"max_edges"`
	MaxEvidence       int  `json:"max_evidence"`
	MaxSourceBytes    int  `json:"max_source_bytes"`
	ExpandedNodes     int  `json:"expanded_node_count"`
	FollowedNodes     int  `json:"followed_node_count"`
	AttemptedNodes    int  `json:"attempted_node_count"`
	MaxRequests       int  `json:"max_requests"`
	RequestsUsed      int  `json:"requests_used"`
	MaxResponseBytes  int  `json:"max_response_bytes"`
	ResponseBytesUsed int  `json:"response_bytes_used"`
	MaxSources        int  `json:"max_sources"`
	MaxFrontier       int  `json:"max_frontier"`
	FrontierCount     int  `json:"frontier_count"`
	FrontierTruncated bool `json:"frontier_truncated"`
}

type JiraIssueGraphSummaryOutput struct {
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

type JiraIssueGraphNodeOutput struct {
	ID         string                        `json:"id"`
	Kind       string                        `json:"kind"`
	Service    string                        `json:"service"`
	ExternalID string                        `json:"external_id,omitempty"`
	URL        string                        `json:"url,omitempty"`
	State      domain.ArtifactGraphNodeState `json:"state"`
	Expanded   bool                          `json:"expanded"`
	Depth      int                           `json:"depth"`
	Stability  domain.ArtifactGraphStability `json:"stability"`
}

type JiraIssueGraphEvidenceOutput struct {
	Collector    string `json:"collector"`
	SourceNodeID string `json:"source_node_id,omitempty"`
	SourceKind   string `json:"source_kind"`
	SourceID     string `json:"source_id,omitempty"`
	JSONPointer  string `json:"json_pointer,omitempty"`
	Extraction   string `json:"extraction"`
}

type JiraIssueGraphEdgeOutput struct {
	ID           string                         `json:"id"`
	From         string                         `json:"from"`
	To           string                         `json:"to"`
	Kind         string                         `json:"kind"`
	RelationType string                         `json:"relation_type,omitempty"`
	Relation     string                         `json:"relation,omitempty"`
	Direction    string                         `json:"direction"`
	Current      bool                           `json:"current"`
	Confidence   string                         `json:"confidence"`
	Stability    domain.ArtifactGraphStability  `json:"stability"`
	Evidence     []JiraIssueGraphEvidenceOutput `json:"evidence"`
}

type JiraIssueGraphSourceOutput struct {
	NodeID        string                           `json:"node_id"`
	NodeDepth     int                              `json:"node_depth"`
	Kind          string                           `json:"kind"`
	Requested     bool                             `json:"requested"`
	Status        domain.ArtifactGraphSourceStatus `json:"status"`
	Complete      bool                             `json:"complete"`
	Count         int                              `json:"count"`
	Truncated     bool                             `json:"truncated"`
	PartialReason string                           `json:"partial_reason,omitempty"`
	Stability     domain.ArtifactGraphStability    `json:"stability"`
}

type JiraIssueGraphFrontier struct {
	NodeID string `json:"node_id"`
	Depth  int    `json:"depth"`
	Reason string `json:"reason"`
}

func registerJiraIssueGraphTool(server *mcp.Server, deps Dependencies) {
	addReadOnlyTool(server, readOnlyTool("jira_issue_graph", "Build a bounded Jira issue graph", "Return one provenance-qualified schema-v2 graph from an exact canonical Jira key. Depth is limited to 0..2 and follows only exact structured Jira relations. The tool uses stable Jira sources only: it performs no Confluence reads, exposes no Development source, and never treats absent Development evidence as zero. External and Confluence nodes are unfetched stubs."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraIssueGraphInput) (*mcp.CallToolResult, *JiraIssueGraphOutput, error) {
			key, opts, maxBytes, err := validatedJiraIssueGraphInput(in)
			if err != nil {
				return nil, nil, classifiedJiraIssueGraphRead(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classifiedJiraIssueGraphRead(err)
			}
			result, err := jira.IssueGraphWithOptions(ctx, key, opts)
			if err != nil {
				return nil, nil, classifiedJiraIssueGraphRead(err)
			}
			out, err := projectJiraIssueGraph(result, key, opts)
			if err == nil {
				err = boundedOutput(out, maxBytes, "encode Jira issue graph result", "Jira issue graph result exceeds max_bytes")
			}
			return nil, out, classifiedJiraIssueGraphRead(err)
		})
}

func validatedJiraIssueGraphInput(in JiraIssueGraphInput) (string, app.JiraIssueGraphOptions, int, error) {
	if err := app.ValidateJiraIssueGraphKey(in.Key); err != nil {
		return "", app.JiraIssueGraphOptions{}, 0, err
	}
	if in.Depth < 0 || in.Depth > jiraIssueGraphMaxDepth {
		return "", app.JiraIssueGraphOptions{}, 0, fmt.Errorf("%w: depth must be between 0 and %d", domain.ErrUsage, jiraIssueGraphMaxDepth)
	}
	maxNodes, err := boundedDefault(in.MaxNodes, jiraIssueGraphDefaultMaxNodes, jiraIssueGraphMaxMaxNodes, "max_nodes")
	if err != nil {
		return "", app.JiraIssueGraphOptions{}, 0, err
	}
	maxEdges, err := boundedDefault(in.MaxEdges, jiraIssueGraphDefaultMaxEdges, jiraIssueGraphMaxMaxEdges, "max_edges")
	if err != nil {
		return "", app.JiraIssueGraphOptions{}, 0, err
	}
	maxRequests, err := boundedDefault(in.MaxRequests, jiraIssueGraphDefaultMaxRequests, jiraIssueGraphMaxMaxRequests, "max_requests")
	if err != nil {
		return "", app.JiraIssueGraphOptions{}, 0, err
	}
	maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
	if err != nil {
		return "", app.JiraIssueGraphOptions{}, 0, err
	}
	opts := app.JiraIssueGraphOptions{
		Depth: in.Depth, MaxNodes: maxNodes, MaxEdges: maxEdges,
		MaxEvidence: jiraIssueGraphFixedMaxEvidence, MaxRequests: maxRequests,
		MaxResponseBytes: jiraIssueGraphFixedResponseBytes, ResolveConfluence: false,
	}
	return in.Key, opts, maxBytes, nil
}

func projectJiraIssueGraph(result *app.JiraIssueGraphResult, key string, opts app.JiraIssueGraphOptions) (*JiraIssueGraphOutput, error) {
	invalid := func() (*JiraIssueGraphOutput, error) {
		return nil, fmt.Errorf("%w: Jira issue graph result is outside the MCP projection", domain.ErrCheckFailed)
	}
	if err := app.ValidateJiraIssueGraphResult(result); err != nil || result.RootID != "jira:issue:"+key {
		return invalid()
	}
	bounds := result.Bounds
	if bounds.RequestedDepth != opts.Depth || bounds.MaxNodes != opts.MaxNodes || bounds.MaxEdges != opts.MaxEdges ||
		bounds.MaxEvidence != jiraIssueGraphFixedMaxEvidence || bounds.MaxRequests != opts.MaxRequests ||
		bounds.MaxResponseBytes != jiraIssueGraphFixedResponseBytes || bounds.MaxSources != opts.MaxNodes*8+1 ||
		bounds.MaxFrontier != opts.MaxNodes {
		return invalid()
	}
	out := &JiraIssueGraphOutput{
		SchemaVersion: result.SchemaVersion, RootID: result.RootID, Complete: result.Complete, Truncated: result.Truncated,
		Bounds: JiraIssueGraphBoundsOutput{
			RequestedDepth: bounds.RequestedDepth, MaxNodes: bounds.MaxNodes, MaxEdges: bounds.MaxEdges,
			MaxEvidence: bounds.MaxEvidence, MaxSourceBytes: bounds.MaxSourceBytes,
			ExpandedNodes: bounds.ExpandedNodes, FollowedNodes: bounds.FollowedNodes, AttemptedNodes: bounds.AttemptedNodes,
			MaxRequests: bounds.MaxRequests, RequestsUsed: bounds.RequestsUsed,
			MaxResponseBytes: bounds.MaxResponseBytes, ResponseBytesUsed: bounds.ResponseBytesUsed,
			MaxSources: bounds.MaxSources, MaxFrontier: bounds.MaxFrontier,
			FrontierCount: bounds.FrontierCount, FrontierTruncated: bounds.FrontierTruncated,
		},
		Summary: JiraIssueGraphSummaryOutput{
			NodeCount: result.Summary.NodeCount, EdgeCount: result.Summary.EdgeCount,
			EvidenceCount: result.Summary.EvidenceCount, SourceCount: result.Summary.SourceCount,
			IncompleteSourceCount:     result.Summary.IncompleteSourceCount,
			SourceStatusCounts:        copyStringIntMap(result.Summary.SourceStatusCounts),
			NodeCountMatchesNodes:     result.Summary.NodeCountMatchesNodes,
			EdgeCountMatchesEdges:     result.Summary.EdgeCountMatchesEdges,
			EvidenceCountMatchesEdges: result.Summary.EvidenceCountMatchesEdges,
			SourceCountMatchesSources: result.Summary.SourceCountMatchesSources,
			SourceStatusCountsMatch:   result.Summary.SourceStatusCountsMatch,
			IncompleteCountMatches:    result.Summary.IncompleteCountMatches,
			ExpandedCountMatchesNodes: result.Summary.ExpandedCountMatchesNodes,
			CompleteMatchesSources:    result.Summary.CompleteMatchesSources,
		},
		Nodes: []JiraIssueGraphNodeOutput{}, Edges: []JiraIssueGraphEdgeOutput{},
		Sources: []JiraIssueGraphSourceOutput{}, Frontier: []JiraIssueGraphFrontier{},
		Warnings: append([]string(nil), result.Warnings...),
	}
	for _, node := range result.Nodes {
		if !oneOfString(node.Kind, "jira_issue", "confluence_page", "attachment", "url") || !safeJiraIssueGraphURL(node.URL) {
			return invalid()
		}
		out.Nodes = append(out.Nodes, JiraIssueGraphNodeOutput{
			ID: node.ID, Kind: node.Kind, Service: node.Service, ExternalID: node.ExternalID,
			URL: node.URL, State: node.State, Expanded: node.Expanded,
			Depth: node.Depth, Stability: node.Stability,
		})
	}
	for _, edge := range result.Edges {
		if !oneOfString(edge.Kind, "jira_link", "parent_of", "child_of", "epic_of", "attached", "mentions", "remote_link") ||
			!safeJiraIssueGraphText(edge.RelationType) || !safeJiraIssueGraphText(edge.Relation) {
			return invalid()
		}
		projected := JiraIssueGraphEdgeOutput{
			ID: edge.ID, From: edge.From, To: edge.To, Kind: edge.Kind,
			RelationType: edge.RelationType, Relation: edge.Relation, Direction: edge.Direction,
			Current: edge.Current, Confidence: edge.Confidence, Stability: edge.Stability,
			Evidence: []JiraIssueGraphEvidenceOutput{},
		}
		for _, evidence := range edge.Evidence {
			if !stableJiraIssueGraphSource(evidence.Collector) || !safeJiraIssueGraphEvidence(evidence) {
				return invalid()
			}
			projected.Evidence = append(projected.Evidence, JiraIssueGraphEvidenceOutput{
				Collector: evidence.Collector, SourceNodeID: evidence.SourceNodeID,
				SourceKind: evidence.SourceKind, SourceID: evidence.SourceID,
				JSONPointer: evidence.JSONPointer, Extraction: evidence.Extraction,
			})
		}
		out.Edges = append(out.Edges, projected)
	}
	for _, source := range result.Sources {
		if source.NodeDepth == nil || !stableJiraIssueGraphSource(source.Kind) {
			return invalid()
		}
		out.Sources = append(out.Sources, JiraIssueGraphSourceOutput{
			NodeID: source.NodeID, NodeDepth: *source.NodeDepth, Kind: source.Kind,
			Requested: source.Requested, Status: source.Status, Complete: source.Complete,
			Count: source.Count, Truncated: source.Truncated, PartialReason: source.PartialReason,
			Stability: source.Stability,
		})
	}
	for _, item := range result.Frontier {
		out.Frontier = append(out.Frontier, JiraIssueGraphFrontier{NodeID: item.NodeID, Depth: item.Depth, Reason: item.Reason})
	}
	return out, nil
}

func stableJiraIssueGraphSource(value string) bool {
	return oneOfString(value, "issue_fields", "issue_links", "hierarchy", "attachments",
		"issue_properties", "comments", "worklogs", "remote_links")
}

func safeJiraIssueGraphEvidence(value domain.ArtifactGraphEvidence) bool {
	if value.SourceID != "" && (!jiraIssueGraphSafeID.MatchString(value.SourceID) || sensitiveJiraIssueGraphEvidenceToken(value.SourceID)) {
		return false
	}
	return value.JSONPointer == "" || len(value.JSONPointer) <= 1<<10 &&
		jiraIssueGraphSafePointer.MatchString(value.JSONPointer) && !sensitiveJiraIssueGraphEvidenceToken(value.JSONPointer)
}

func safeJiraIssueGraphText(value string) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && utf8.RuneCountInString(value) <= jiraIssueGraphMaxLabelRunes
}

func safeJiraIssueGraphURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > jiraIssueGraphMaxURLBytes {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || (parsed.RawQuery != "" && parsed.RawQuery != "redacted=redacted") {
		return false
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if sensitiveJiraIssueGraphToken(segment) || len(segment) >= 24 {
			return false
		}
	}
	return true
}

func sensitiveJiraIssueGraphToken(value string) bool {
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

func sensitiveJiraIssueGraphEvidenceToken(value string) bool {
	return sensitiveJiraIssueGraphToken(value) || strings.Contains(strings.ToLower(value), "private")
}

func copyStringIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func oneOfString(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

var jiraIssueGraphReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Jira issue graph read failed"),
	kinds: map[string]toolErrorRule{
		"usage_error":           staticMessage("invalid Jira issue graph request"),
		"configuration_error":   staticMessage("Jira issue graph service is not configured"),
		"authentication_failed": staticMessage("Jira issue graph authentication failed"),
		"forbidden":             staticMessage("Jira issue graph access is forbidden"),
		"not_found":             staticMessage("Jira issue graph root was not found"),
		"check_failed":          staticMessage("Jira issue graph result failed validation"),
		"output_limit_exceeded": staticMessageWithRemediation("Jira issue graph result exceeds max_bytes", "narrow_graph_or_raise_bound"),
		"rate_limited":          staticMessage("Jira issue graph rate limit was exhausted"),
		"api_error":             staticMessage("Jira issue graph API request failed"),
		"transport_error":       staticMessage("Jira issue graph transport failed"),
	},
}

func classifiedJiraIssueGraphRead(err error) error { return jiraIssueGraphReadPolicy.classify(err) }
