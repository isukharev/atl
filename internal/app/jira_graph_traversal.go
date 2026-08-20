package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraIssueGraphSchemaVersionV2 = 2
	jiraGraphDefaultNodes         = 100
	jiraGraphDefaultEdges         = 500
	jiraGraphDefaultEvidence      = 500
	jiraGraphDefaultRequests      = 100
	jiraGraphMaxRequests          = 128
	jiraGraphDefaultResponseBytes = 16 << 20
	jiraGraphMaxResponseBytes     = 64 << 20
	jiraGraphMaxDepth             = 3
)

// Exported graph option bounds let transport frontends reject explicitly
// supplied zero/out-of-range values before constructing backend services.
const (
	JiraIssueGraphMaxDepth             = jiraGraphMaxDepth
	JiraIssueGraphDefaultMaxNodes      = jiraGraphDefaultNodes
	JiraIssueGraphMaxNodes             = jiraGraphMaxNodes
	JiraIssueGraphDefaultMaxEdges      = jiraGraphDefaultEdges
	JiraIssueGraphMaxEdges             = jiraGraphMaxEdges
	JiraIssueGraphDefaultMaxEvidence   = jiraGraphDefaultEvidence
	JiraIssueGraphMaxEvidence          = jiraGraphMaxEvidence
	JiraIssueGraphDefaultMaxRequests   = jiraGraphDefaultRequests
	JiraIssueGraphMaxRequests          = jiraGraphMaxRequests
	JiraIssueGraphDefaultResponseBytes = jiraGraphDefaultResponseBytes
	JiraIssueGraphMaxResponseBytes     = jiraGraphMaxResponseBytes
)

// JiraIssueGraphOptions configures the schema-v2 bounded traversal contract.
// A zero limit selects its documented default. Transport request and response
// byte enforcement is performed by the caller's read-budget context.
type JiraIssueGraphOptions struct {
	Depth              int  `json:"depth"`
	MaxNodes           int  `json:"max_nodes,omitempty"`
	MaxEdges           int  `json:"max_edges,omitempty"`
	MaxEvidence        int  `json:"max_evidence,omitempty"`
	MaxRequests        int  `json:"max_requests,omitempty"`
	MaxResponseBytes   int  `json:"max_response_bytes,omitempty"`
	ResolveConfluence  bool `json:"resolve_confluence,omitempty"`
	IncludeDevelopment bool `json:"include_development,omitempty"`
}

// JiraIssueGraphFrontierItem records a canonical Jira node that was eligible
// for traversal but could not be attempted because a hard bound was reached.
type JiraIssueGraphFrontierItem struct {
	NodeID string `json:"node_id"`
	Depth  int    `json:"depth"`
	Reason string `json:"reason"`
}

type jiraGraphV2Builder struct {
	result                      *JiraIssueGraphResult
	nodes                       map[string]domain.ArtifactGraphNode
	edges                       map[string]domain.ArtifactGraphEdge
	sources                     map[string]domain.ArtifactGraphSource
	evidenceCount               int
	maxNodes                    int
	maxEdges                    int
	maxEvidence                 int
	frontierSeen                map[string]bool
	aliases                     map[string]string
	requestConfluenceResolution bool
	sourceKinds                 []string
}

type jiraGraphQueueItem struct {
	ID    string
	Key   string
	Depth int
}

// IssueGraphWithOptions builds the bounded schema-v2 graph.
func (s *JiraService) IssueGraphWithOptions(ctx context.Context, key string, opts JiraIssueGraphOptions) (*JiraIssueGraphResult, error) {
	limits, err := NormalizeJiraIssueGraphOptions(opts)
	if err != nil {
		return nil, err
	}
	// Preserve the established CLI/application compatibility that accepts
	// surrounding whitespace. Typed transports can call the exported validator
	// before service construction when they require an exact canonical token.
	key = strings.TrimSpace(key)
	if err := ValidateJiraIssueGraphKey(key); err != nil {
		return nil, err
	}
	reader, ok := s.tr.(domain.QualifiedIssueSnapshotReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira graph snapshot capability is unavailable", domain.ErrCheckFailed)
	}

	readBudget, budgetErr := domain.NewReadBudget(limits.MaxRequests, int64(limits.MaxResponseBytes))
	if budgetErr != nil {
		return nil, fmt.Errorf("%w: graph read budget is invalid", domain.ErrCheckFailed)
	}
	ctx = domain.WithSingleAttempt(domain.WithReadBudget(ctx, readBudget))
	b := newJiraGraphV2Builder(strings.ToUpper(key), limits)
	queue := []jiraGraphQueueItem{{ID: b.result.RootID, Key: strings.ToUpper(key), Depth: 0}}
	attempted := map[string]bool{}
	for len(queue) > 0 {
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].Depth != queue[j].Depth {
				return queue[i].Depth < queue[j].Depth
			}
			return queue[i].ID < queue[j].ID
		})
		item := queue[0]
		queue = queue[1:]
		item.ID = b.canonicalNodeID(item.ID)
		item.Key = strings.TrimPrefix(item.ID, "jira:issue:")
		if attempted[item.ID] {
			continue
		}
		attempted[item.ID] = true

		snapshot, readErr := reader.ReadIssueSnapshot(ctx, item.Key)
		if !errors.Is(readErr, domain.ErrReadAttemptBudgetExhausted) {
			b.result.Bounds.AttemptedNodes++
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if item.Depth == 0 {
				if isJiraGraphBudgetError(readErr) {
					b.qualifyBudgetLimitedRoot(item, readErr)
					usage := readBudget.Usage()
					b.result.Bounds.RequestsUsed = usage.Attempts
					b.result.Bounds.ResponseBytesUsed = int(usage.ResponseBytes)
					return b.finish()
				}
				return nil, readErr
			}
			b.qualifyFailedNode(item, readErr)
			if isJiraGraphBudgetError(readErr) {
				b.addFrontier(item, jiraGraphBudgetReason(readErr))
				for _, pending := range queue {
					b.addFrontier(pending, jiraGraphBudgetReason(readErr))
				}
				break
			}
			continue
		}
		canonicalKey, identityErr := validateJiraGraphV2Snapshot(snapshot, item.Key)
		if identityErr != nil {
			if item.Depth == 0 {
				return nil, identityErr
			}
			b.qualifyFailedNode(item, identityErr)
			continue
		}
		canonicalID := b.canonicalNodeID("jira:issue:" + canonicalKey)
		canonicalKey = strings.TrimPrefix(canonicalID, "jira:issue:")
		if canonicalID != item.ID {
			canonicalAlreadyAttempted := attempted[canonicalID]
			b.reconcileMovedNode(item.ID, canonicalID, item.Depth)
			item.ID, item.Key = canonicalID, canonicalKey
			attempted[canonicalID] = true
			if canonicalAlreadyAttempted {
				continue
			}
		}

		projection, collectErr := s.collectJiraGraphV2Node(ctx, snapshot, item.ID, item.Depth, limits.IncludeDevelopment)
		if collectErr != nil {
			return nil, collectErr
		}
		b.mergeProjection(projection, item.ID, item.Depth)
		if reason := projectionBudgetReason(projection); reason != "" {
			for _, candidate := range b.followableJiraNodes(item.ID, item.Depth+1, attempted) {
				b.addFrontier(candidate, reason)
			}
			for _, pending := range queue {
				b.addFrontier(pending, reason)
			}
			break
		}

		if item.Depth >= limits.Depth {
			continue
		}
		next := b.followableJiraNodes(item.ID, item.Depth+1, attempted)
		queue = append(queue, next...)
	}
	if limits.ResolveConfluence {
		b.resolveConfluence(ctx, s)
	}
	usage := readBudget.Usage()
	b.result.Bounds.RequestsUsed = usage.Attempts
	b.result.Bounds.ResponseBytesUsed = int(usage.ResponseBytes)
	return b.finish()
}

// ValidateJiraIssueGraphKey validates the exact canonical seed identity shared
// by the CLI and typed transports. Whitespace and lowercase aliases are refused
// so a caller can reconcile the returned root without transport-specific
// normalization.
func ValidateJiraIssueGraphKey(key string) error {
	if key != strings.TrimSpace(key) || !jiraGraphExactKey(key) {
		return fmt.Errorf("%w: issue key must use the canonical PROJECT-123 form", domain.ErrUsage)
	}
	return nil
}

func NormalizeJiraIssueGraphOptions(opts JiraIssueGraphOptions) (JiraIssueGraphOptions, error) {
	if opts.Depth < 0 || opts.Depth > jiraGraphMaxDepth {
		return opts, fmt.Errorf("%w: graph depth must be between 0 and %d", domain.ErrUsage, jiraGraphMaxDepth)
	}
	set := func(value, defaultValue, maximum int, name string) (int, error) {
		if value == 0 {
			return defaultValue, nil
		}
		if value < 0 || value > maximum {
			return 0, fmt.Errorf("%w: graph %s must be between 1 and %d", domain.ErrUsage, name, maximum)
		}
		return value, nil
	}
	var err error
	if opts.MaxNodes, err = set(opts.MaxNodes, jiraGraphDefaultNodes, jiraGraphMaxNodes, "max nodes"); err != nil {
		return opts, err
	}
	if opts.MaxEdges, err = set(opts.MaxEdges, jiraGraphDefaultEdges, jiraGraphMaxEdges, "max edges"); err != nil {
		return opts, err
	}
	if opts.MaxEvidence, err = set(opts.MaxEvidence, jiraGraphDefaultEvidence, jiraGraphMaxEvidence, "max evidence"); err != nil {
		return opts, err
	}
	if opts.MaxRequests, err = set(opts.MaxRequests, jiraGraphDefaultRequests, jiraGraphMaxRequests, "max requests"); err != nil {
		return opts, err
	}
	if opts.MaxResponseBytes, err = set(opts.MaxResponseBytes, jiraGraphDefaultResponseBytes, jiraGraphMaxResponseBytes, "max response bytes"); err != nil {
		return opts, err
	}
	return opts, nil
}

func newJiraGraphV2Builder(rootKey string, opts JiraIssueGraphOptions) *jiraGraphV2Builder {
	rootID := "jira:issue:" + rootKey
	sourceKinds := jiraGraphSourceKinds(opts.IncludeDevelopment)
	return &jiraGraphV2Builder{
		result: &JiraIssueGraphResult{
			SchemaVersion: jiraIssueGraphSchemaVersionV2,
			RootID:        rootID,
			Bounds: JiraIssueGraphBounds{
				RequestedDepth: opts.Depth, MaxNodes: opts.MaxNodes, MaxEdges: opts.MaxEdges,
				MaxEvidence: opts.MaxEvidence, MaxSourceBytes: jiraGraphMaxSourceBytes,
				MaxRequests: opts.MaxRequests, MaxResponseBytes: opts.MaxResponseBytes,
				MaxSources: opts.MaxNodes*len(sourceKinds) + 1, MaxFrontier: opts.MaxNodes,
				IncludeDevelopment: opts.IncludeDevelopment,
			},
			Nodes: []domain.ArtifactGraphNode{}, Edges: []domain.ArtifactGraphEdge{},
			Sources: []domain.ArtifactGraphSource{}, Frontier: []JiraIssueGraphFrontierItem{},
		},
		nodes: map[string]domain.ArtifactGraphNode{}, edges: map[string]domain.ArtifactGraphEdge{},
		sources: map[string]domain.ArtifactGraphSource{}, maxNodes: opts.MaxNodes,
		maxEdges: opts.MaxEdges, maxEvidence: opts.MaxEvidence, frontierSeen: map[string]bool{},
		aliases: map[string]string{}, requestConfluenceResolution: opts.ResolveConfluence,
		sourceKinds: sourceKinds,
	}
}

func validateJiraGraphV2Snapshot(snapshot *domain.QualifiedIssueSnapshot, requestedKey string) (string, error) {
	if snapshot == nil || snapshot.ID == "" || !jiraGraphExactKey(snapshot.Key) ||
		!strings.EqualFold(snapshot.RequestedKey, requestedKey) || snapshot.Issue.ID != snapshot.ID ||
		!strings.EqualFold(snapshot.Issue.Key, snapshot.Key) || snapshot.Fields == nil ||
		snapshot.Names == nil || snapshot.Schema == nil || snapshot.Properties == nil {
		return "", fmt.Errorf("%w: Jira graph snapshot has no usable identity", domain.ErrCheckFailed)
	}
	return strings.ToUpper(snapshot.Key), nil
}

func (s *JiraService) collectJiraGraphV2Node(ctx context.Context, snapshot *domain.QualifiedIssueSnapshot, nodeID string, depth int, includeDevelopment bool) (*jiraGraphBuilder, error) {
	temp := newJiraGraphBuilderWithSources(nodeID, includeDevelopment)
	temp.addNode(domain.ArtifactGraphNode{
		ID: nodeID, Kind: "jira_issue", Service: "jira", ExternalID: strings.ToUpper(snapshot.Key),
		Label: graphBoundedLabel(snapshot.Issue.Summary), State: domain.ArtifactNodeResolved,
		Expanded: true, Depth: depth, Stability: domain.ArtifactStabilityPublicAPI,
	}, nil)
	temp.collectIssueLinks(snapshot)
	temp.collectHierarchy(snapshot)
	temp.collectAttachments(snapshot)
	temp.collectSnapshotText(snapshot, s.baseURL, jiraGraphConfluenceBase(s))
	if err := temp.collectComments(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	if err := temp.collectWorklogs(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	if err := temp.collectRemoteLinks(ctx, s.tr, snapshot.Key, s.baseURL, jiraGraphConfluenceBase(s)); err != nil {
		return nil, err
	}
	if includeDevelopment {
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

func (b *jiraGraphV2Builder) mergeProjection(p *jiraGraphBuilder, sourceNodeID string, depth int) {
	root := p.nodes[sourceNodeID]
	b.upsertNode(root)
	edges := make([]domain.ArtifactGraphEdge, 0, len(p.edges))
	for _, edge := range p.edges {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool { return graphEdgeSortKey(edges[i]) < graphEdgeSortKey(edges[j]) })
	developmentEdges := make([]domain.ArtifactGraphEdge, 0)
	for _, edge := range edges {
		if strings.HasPrefix(edge.Kind, "development_") {
			developmentEdges = append(developmentEdges, edge)
			continue
		}
		if b.canonicalNodeID(edge.From) == b.canonicalNodeID(edge.To) {
			qualifyProjectedJiraGraphSelfEdge(p, edge)
			continue
		}
		target := p.nodes[edge.To]
		if admitted, dropped := b.admitEdgeWithTarget(edge, target); !admitted {
			for _, evidence := range dropped {
				source := p.sources[evidence.Collector]
				source.Status, source.Complete, source.Truncated = domain.ArtifactSourcePartial, false, true
				source.PartialReason = domain.ArtifactPartialOutputLimit
				p.sources[source.Kind] = source
			}
			if target.Kind == "jira_issue" && target.State == domain.ArtifactNodeStub {
				b.addFrontier(jiraGraphQueueItem{ID: target.ID, Key: target.ExternalID, Depth: target.Depth}, domain.ArtifactPartialOutputLimit)
			}
		}
	}
	if reason := b.admitDevelopmentProjection(p, developmentEdges); reason != "" {
		source := p.sources["development"]
		source.Status, source.Complete = domain.ArtifactSourcePartial, false
		source.Truncated = reason == domain.ArtifactPartialOutputLimit
		source.PartialReason = reason
		p.sources[source.Kind] = source
	}
	for _, kind := range p.sourceKinds {
		source := *p.sources[kind]
		b.sources[graphV2SourceKey(sourceNodeID, kind)] = source
	}
	if node := b.nodes[sourceNodeID]; true {
		node.Expanded, node.State, node.Depth = true, domain.ArtifactNodeResolved, depth
		b.nodes[sourceNodeID] = node
	}
}

func qualifyProjectedJiraGraphSelfEdge(p *jiraGraphBuilder, edge domain.ArtifactGraphEdge) {
	for _, evidence := range edge.Evidence {
		if evidence.Extraction == "structured" {
			p.markMalformed(p.sources[evidence.Collector])
		}
	}
}

func (b *jiraGraphV2Builder) admitDevelopmentProjection(p *jiraGraphBuilder, edges []domain.ArtifactGraphEdge) string {
	if len(edges) == 0 {
		return ""
	}
	trial := *b
	trial.nodes = make(map[string]domain.ArtifactGraphNode, len(b.nodes))
	for key, node := range b.nodes {
		trial.nodes[key] = node
	}
	trial.edges = make(map[string]domain.ArtifactGraphEdge, len(b.edges))
	for key, edge := range b.edges {
		edge.Evidence = append([]domain.ArtifactGraphEvidence(nil), edge.Evidence...)
		trial.edges[key] = edge
	}
	for _, edge := range edges {
		target := p.nodes[edge.To]
		if existing, found := trial.nodes[target.ID]; found {
			merged, ok := reconcileDevelopmentGraphNode(existing, target)
			if !ok {
				return domain.ArtifactPartialMalformed
			}
			trial.nodes[target.ID] = merged
			target = merged
		}
		if admitted, _ := trial.admitEdgeWithTarget(edge, target); !admitted {
			return domain.ArtifactPartialOutputLimit
		}
	}
	b.nodes, b.edges, b.evidenceCount = trial.nodes, trial.edges, trial.evidenceCount
	return ""
}

func reconcileDevelopmentGraphNode(existing, candidate domain.ArtifactGraphNode) (domain.ArtifactGraphNode, bool) {
	if existing.Kind != candidate.Kind || existing.SCM == nil || candidate.SCM == nil {
		return domain.ArtifactGraphNode{}, false
	}
	left, right := *existing.SCM, *candidate.SCM
	if existing.Kind != "gitlab_merge_request" {
		return existing, left == right
	}
	leftState, rightState := left.MergeRequestState, right.MergeRequestState
	left.MergeRequestState, right.MergeRequestState = "", ""
	if left != right {
		return domain.ArtifactGraphNode{}, false
	}
	mergedState := leftState
	switch {
	case leftState == rightState:
	case leftState == "unknown":
		mergedState = rightState
	case rightState == "unknown":
	default:
		return domain.ArtifactGraphNode{}, false
	}
	scm := *existing.SCM
	scm.MergeRequestState = mergedState
	existing.SCM = &scm
	return existing, true
}

func (b *jiraGraphV2Builder) upsertNode(node domain.ArtifactGraphNode) bool {
	node.ID = b.canonicalNodeID(node.ID)
	if node.Kind == "jira_issue" {
		node.ExternalID = strings.TrimPrefix(node.ID, "jira:issue:")
	}
	if existing, ok := b.nodes[node.ID]; ok {
		if node.Depth < existing.Depth {
			existing.Depth = node.Depth
		}
		if node.Label != "" {
			existing.Label = node.Label
		}
		if node.URL != "" {
			existing.URL = node.URL
		}
		if node.Expanded || node.State == domain.ArtifactNodeResolved ||
			(node.State == domain.ArtifactNodeStub && existing.State == domain.ArtifactNodeUnresolved) {
			existing.State = node.State
			existing.Expanded = node.Expanded
			existing.Stability = node.Stability
		}
		b.nodes[node.ID] = existing
		return true
	}
	if len(b.nodes) >= b.maxNodes {
		return false
	}
	b.nodes[node.ID] = node
	return true
}

func (b *jiraGraphV2Builder) canonicalNodeID(id string) string {
	seen := map[string]bool{}
	for id != "" && b.aliases[id] != "" && !seen[id] {
		seen[id] = true
		id = b.aliases[id]
	}
	return id
}

func (b *jiraGraphV2Builder) admitEdgeWithTarget(edge domain.ArtifactGraphEdge, target domain.ArtifactGraphNode) (bool, []domain.ArtifactGraphEvidence) {
	edge.From = b.canonicalNodeID(edge.From)
	edge.To = b.canonicalNodeID(edge.To)
	target.ID = edge.To
	if target.Kind == "jira_issue" {
		target.ExternalID = strings.TrimPrefix(target.ID, "jira:issue:")
	}
	for index := range edge.Evidence {
		edge.Evidence[index].SourceNodeID = b.canonicalNodeID(edge.Evidence[index].SourceNodeID)
	}
	edge.ID = graphEdgeID(edge)
	existing, edgeExists := b.edges[edge.ID]
	nodeExists := false
	seen := map[string]bool{}
	for _, evidence := range existing.Evidence {
		seen[graphEvidenceKey(evidence)] = true
	}
	newEvidence := make([]domain.ArtifactGraphEvidence, 0, len(edge.Evidence))
	for _, evidence := range edge.Evidence {
		if !seen[graphEvidenceKey(evidence)] {
			newEvidence = append(newEvidence, evidence)
		}
	}
	if _, nodeExists = b.nodes[target.ID]; !nodeExists && len(b.nodes) >= b.maxNodes {
		return false, newEvidence
	}
	if !edgeExists && len(b.edges) >= b.maxEdges {
		return false, newEvidence
	}
	if b.evidenceCount+len(newEvidence) > b.maxEvidence {
		return false, newEvidence
	}
	if !nodeExists {
		b.nodes[target.ID] = target
	} else {
		b.upsertNode(target)
	}
	if edgeExists {
		existing.Evidence = append(existing.Evidence, newEvidence...)
		b.edges[edge.ID] = existing
	} else {
		b.edges[edge.ID] = edge
	}
	b.evidenceCount += len(newEvidence)
	return true, nil
}

func (b *jiraGraphV2Builder) followableJiraNodes(sourceID string, depth int, attempted map[string]bool) []jiraGraphQueueItem {
	ids := map[string]bool{}
	for _, edge := range b.edges {
		if edge.From != sourceID {
			continue
		}
		node, ok := b.nodes[edge.To]
		if ok && jiraGraphTraversableEdge(edge) &&
			node.Kind == "jira_issue" && node.State == domain.ArtifactNodeStub && !attempted[node.ID] {
			ids[node.ID] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	items := make([]jiraGraphQueueItem, 0, len(ordered))
	for _, id := range ordered {
		items = append(items, jiraGraphQueueItem{ID: id, Key: strings.TrimPrefix(id, "jira:issue:"), Depth: depth})
	}
	return items
}

func jiraGraphTraversableEdge(edge domain.ArtifactGraphEdge) bool {
	return oneOf(edge.Kind, "jira_link", "parent_of", "child_of", "epic_of") &&
		edge.Confidence == "exact" && edge.Stability == domain.ArtifactStabilityPublicAPI
}

func (b *jiraGraphV2Builder) qualifyFailedNode(item jiraGraphQueueItem, err error) {
	node := b.nodes[item.ID]
	node.Expanded = false
	node.Depth = item.Depth
	status, reason := domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		reason = domain.ArtifactPartialRequestLimit
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		reason = domain.ArtifactPartialByteLimit
	case errors.Is(err, domain.ErrAuth), errors.Is(err, domain.ErrForbidden):
		node.State, status, reason = domain.ArtifactNodeForbidden, domain.ArtifactSourceForbidden, ""
	case errors.Is(err, domain.ErrNotFound):
		node.State, status, reason = domain.ArtifactNodeMissing, domain.ArtifactSourceUnsupported, ""
	case errors.Is(err, domain.ErrCheckFailed):
		reason = domain.ArtifactPartialMalformed
	}
	b.nodes[item.ID] = node
	for _, kind := range b.sourceKinds {
		d := item.Depth
		b.sources[graphV2SourceKey(item.ID, kind)] = domain.ArtifactGraphSource{
			NodeID: item.ID, NodeDepth: &d, Kind: kind, Requested: true,
			Status: status, Complete: false, Truncated: isJiraGraphBudgetError(err), PartialReason: reason,
			Stability: jiraGraphSourceStability(kind),
		}
	}
}

func (b *jiraGraphV2Builder) qualifyBudgetLimitedRoot(item jiraGraphQueueItem, err error) {
	b.nodes[item.ID] = domain.ArtifactGraphNode{
		ID: item.ID, Kind: "jira_issue", Service: "jira", ExternalID: item.Key,
		State: domain.ArtifactNodeUnresolved, Expanded: false, Depth: 0,
		Stability: domain.ArtifactStabilityPublicAPI,
	}
	reason := jiraGraphBudgetReason(err)
	for _, kind := range b.sourceKinds {
		depth := 0
		b.sources[graphV2SourceKey(item.ID, kind)] = domain.ArtifactGraphSource{
			NodeID: item.ID, NodeDepth: &depth, Kind: kind, Requested: true,
			Status: domain.ArtifactSourcePartial, Complete: false, Truncated: true,
			PartialReason: reason, Stability: jiraGraphSourceStability(kind),
		}
	}
	if b.requestConfluenceResolution {
		depth := 0
		b.sources[graphV2SourceKey(item.ID, "confluence_metadata")] = domain.ArtifactGraphSource{
			NodeID: item.ID, NodeDepth: &depth, Kind: "confluence_metadata", Requested: true,
			Status: domain.ArtifactSourcePartial, Complete: false, Truncated: true,
			PartialReason: reason, Stability: domain.ArtifactStabilityPublicAPI,
		}
	}
	b.addFrontier(item, reason)
}

func isJiraGraphBudgetError(err error) bool {
	return errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || errors.Is(err, domain.ErrReadResponseBudgetExhausted)
}

func jiraGraphBudgetReason(err error) string {
	if errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
		return domain.ArtifactPartialByteLimit
	}
	return domain.ArtifactPartialRequestLimit
}

func projectionBudgetReason(p *jiraGraphBuilder) string {
	for _, kind := range p.sourceKinds {
		source := p.sources[kind]
		if source.PartialReason == domain.ArtifactPartialRequestLimit || source.PartialReason == domain.ArtifactPartialByteLimit {
			return source.PartialReason
		}
	}
	return ""
}

func (b *jiraGraphV2Builder) addFrontier(item jiraGraphQueueItem, reason string) {
	item.ID = b.canonicalNodeID(item.ID)
	if strings.HasPrefix(item.ID, "jira:issue:") {
		item.Key = strings.TrimPrefix(item.ID, "jira:issue:")
	}
	key := item.ID + "\x00" + reason
	if b.frontierSeen[key] {
		return
	}
	b.frontierSeen[key] = true
	if len(b.result.Frontier) >= b.result.Bounds.MaxFrontier {
		b.result.Bounds.FrontierTruncated = true
		b.result.Truncated = true
		return
	}
	b.result.Frontier = append(b.result.Frontier, JiraIssueGraphFrontierItem{NodeID: item.ID, Depth: item.Depth, Reason: reason})
	b.result.Truncated = true
}

func (b *jiraGraphV2Builder) resolveConfluence(ctx context.Context, service *JiraService) {
	ids := make([]string, 0)
	for id, node := range b.nodes {
		if node.Kind == "confluence_page" && node.State == domain.ArtifactNodeStub && strings.HasPrefix(id, "confluence:page:") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	depth := 0
	source := domain.ArtifactGraphSource{
		NodeID: b.result.RootID, NodeDepth: &depth, Kind: "confluence_metadata", Requested: true,
		Status: domain.ArtifactSourceEmpty, Complete: true, Count: len(ids), Stability: domain.ArtifactStabilityPublicAPI,
	}
	if len(ids) == 0 {
		b.sources[graphV2SourceKey(source.NodeID, source.Kind)] = source
		return
	}
	reader, _ := service.confluenceGraphMetadataReader()
	if reader == nil {
		source.Status, source.Complete, source.PartialReason = domain.ArtifactSourceSkipped, false, domain.ArtifactPartialDependencyUnavailable
		b.sources[graphV2SourceKey(source.NodeID, source.Kind)] = source
		return
	}
	for index, nodeID := range ids {
		id := strings.TrimPrefix(nodeID, "confluence:page:")
		metadata, err := reader.ReadGraphPageMetadata(ctx, id)
		if err != nil {
			node := b.nodes[nodeID]
			switch {
			case errors.Is(err, domain.ErrNotFound):
				node.State = domain.ArtifactNodeMissing
			case errors.Is(err, domain.ErrAuth), errors.Is(err, domain.ErrForbidden):
				node.State = domain.ArtifactNodeForbidden
				source.Status, source.Complete, source.PartialReason = domain.ArtifactSourceForbidden, false, ""
			default:
				source.Status, source.Complete = domain.ArtifactSourcePartial, false
				if errors.Is(err, domain.ErrCheckFailed) {
					source.PartialReason = domain.ArtifactPartialMalformed
				} else {
					source.PartialReason = domain.ArtifactPartialRequestFailed
				}
			}
			b.nodes[nodeID] = node
			if isJiraGraphBudgetError(err) {
				source.Status, source.Complete, source.Truncated = domain.ArtifactSourcePartial, false, true
				source.PartialReason = jiraGraphBudgetReason(err)
				for _, remaining := range ids[index:] {
					b.addFrontier(jiraGraphQueueItem{ID: remaining, Depth: b.nodes[remaining].Depth}, source.PartialReason)
				}
				break
			}
			continue
		}
		if metadata.ID != id || !graphNumericIDPattern.MatchString(metadata.ID) || strings.TrimSpace(metadata.Title) == "" {
			source.Status, source.Complete, source.PartialReason = domain.ArtifactSourcePartial, false, domain.ArtifactPartialMalformed
			continue
		}
		node := b.nodes[nodeID]
		node.State, node.Label = domain.ArtifactNodeResolved, graphBoundedLabel(metadata.Title)
		b.nodes[nodeID] = node
	}
	if source.Complete && len(ids) > 0 {
		source.Status = domain.ArtifactSourceComplete
	}
	b.sources[graphV2SourceKey(source.NodeID, source.Kind)] = source
}

func (b *jiraGraphV2Builder) reconcileMovedNode(oldID, newID string, depth int) {
	newID = b.canonicalNodeID(newID)
	if oldID == newID {
		return
	}
	newKey := strings.TrimPrefix(newID, "jira:issue:")
	b.aliases[oldID] = newID
	old, ok := b.nodes[oldID]
	delete(b.nodes, oldID)
	if !ok {
		old = domain.ArtifactGraphNode{
			Kind: "jira_issue", Service: "jira", State: domain.ArtifactNodeStub,
			Stability: domain.ArtifactStabilityPublicAPI,
		}
	}
	old.ID, old.ExternalID, old.Depth = newID, newKey, depth
	b.upsertNode(old)
	rebuilt := make(map[string]domain.ArtifactGraphEdge, len(b.edges))
	for _, edge := range b.edges {
		edge.From = b.canonicalNodeID(edge.From)
		edge.To = b.canonicalNodeID(edge.To)
		for index := range edge.Evidence {
			edge.Evidence[index].SourceNodeID = b.canonicalNodeID(edge.Evidence[index].SourceNodeID)
		}
		if edge.From == edge.To {
			b.qualifyReconciledJiraGraphSelfEdge(edge)
			continue
		}
		edge.ID = graphEdgeID(edge)
		if existing, ok := rebuilt[edge.ID]; ok {
			seen := make(map[string]bool, len(existing.Evidence))
			for _, evidence := range existing.Evidence {
				seen[graphEvidenceKey(evidence)] = true
			}
			for _, evidence := range edge.Evidence {
				if !seen[graphEvidenceKey(evidence)] {
					existing.Evidence = append(existing.Evidence, evidence)
					seen[graphEvidenceKey(evidence)] = true
				}
			}
			rebuilt[edge.ID] = existing
		} else {
			rebuilt[edge.ID] = edge
		}
	}
	b.edges = rebuilt
	rebuiltSources := make(map[string]domain.ArtifactGraphSource, len(b.sources))
	for _, source := range b.sources {
		source.NodeID = b.canonicalNodeID(source.NodeID)
		rebuiltSources[graphV2SourceKey(source.NodeID, source.Kind)] = source
	}
	b.sources = rebuiltSources
	b.frontierSeen = map[string]bool{}
	frontier := b.result.Frontier[:0]
	for _, item := range b.result.Frontier {
		item.NodeID = b.canonicalNodeID(item.NodeID)
		key := item.NodeID + "\x00" + item.Reason
		if b.frontierSeen[key] {
			continue
		}
		b.frontierSeen[key] = true
		frontier = append(frontier, item)
	}
	b.result.Frontier = frontier
	b.evidenceCount = 0
	for _, edge := range b.edges {
		b.evidenceCount += len(edge.Evidence)
	}
	if b.result.RootID == oldID {
		b.result.RootID = newID
	}
}

func (b *jiraGraphV2Builder) qualifyReconciledJiraGraphSelfEdge(edge domain.ArtifactGraphEdge) {
	for _, evidence := range edge.Evidence {
		if evidence.Extraction != "structured" {
			continue
		}
		for key, source := range b.sources {
			if source.Kind != evidence.Collector || b.canonicalNodeID(source.NodeID) != edge.From {
				continue
			}
			markJiraGraphSourceMalformed(&source)
			b.sources[key] = source
		}
	}
}

func graphV2SourceKey(nodeID, kind string) string { return nodeID + "\x00" + kind }

func (b *jiraGraphV2Builder) finish() (*JiraIssueGraphResult, error) {
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
		sort.Slice(edge.Evidence, func(i, j int) bool { return graphEvidenceKey(edge.Evidence[i]) < graphEvidenceKey(edge.Evidence[j]) })
		b.result.Edges = append(b.result.Edges, edge)
	}
	sort.Slice(b.result.Edges, func(i, j int) bool { return graphEdgeSortKey(b.result.Edges[i]) < graphEdgeSortKey(b.result.Edges[j]) })
	for _, source := range b.sources {
		b.result.Sources = append(b.result.Sources, source)
	}
	rank := map[string]int{}
	for index, kind := range b.sourceKinds {
		rank[kind] = index
	}
	rank["confluence_metadata"] = len(b.sourceKinds)
	sort.Slice(b.result.Sources, func(i, j int) bool {
		left, right := b.result.Sources[i], b.result.Sources[j]
		if *left.NodeDepth != *right.NodeDepth {
			return *left.NodeDepth < *right.NodeDepth
		}
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		return rank[left.Kind] < rank[right.Kind]
	})
	sort.Slice(b.result.Frontier, func(i, j int) bool {
		if b.result.Frontier[i].Depth != b.result.Frontier[j].Depth {
			return b.result.Frontier[i].Depth < b.result.Frontier[j].Depth
		}
		if b.result.Frontier[i].NodeID != b.result.Frontier[j].NodeID {
			return b.result.Frontier[i].NodeID < b.result.Frontier[j].NodeID
		}
		return b.result.Frontier[i].Reason < b.result.Frontier[j].Reason
	})
	statusCounts := map[string]int{"complete": 0, "empty": 0, "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0}
	incomplete, expanded := 0, 0
	finalEvidence := 0
	for _, node := range b.result.Nodes {
		if node.Expanded {
			expanded++
		}
	}
	for _, source := range b.result.Sources {
		statusCounts[string(source.Status)]++
		if !source.Complete {
			incomplete++
		}
		if source.Truncated {
			b.result.Truncated = true
		}
	}
	for _, edge := range b.result.Edges {
		finalEvidence += len(edge.Evidence)
	}
	b.result.Bounds.ExpandedNodes = expanded
	if b.result.Bounds.AttemptedNodes > 0 {
		b.result.Bounds.FollowedNodes = b.result.Bounds.AttemptedNodes - 1
	}
	b.result.Bounds.FrontierCount = len(b.result.Frontier)
	b.result.Complete = incomplete == 0
	summary := JiraIssueGraphSummary{
		NodeCount: len(b.result.Nodes), EdgeCount: len(b.result.Edges), EvidenceCount: b.evidenceCount,
		SourceCount: len(b.result.Sources), IncompleteSourceCount: incomplete, SourceStatusCounts: statusCounts,
	}
	summary.NodeCountMatchesNodes = summary.NodeCount == len(b.result.Nodes)
	summary.EdgeCountMatchesEdges = summary.EdgeCount == len(b.result.Edges)
	summary.EvidenceCountMatchesEdges = summary.EvidenceCount == finalEvidence
	summary.SourceCountMatchesSources = summary.SourceCount == len(b.result.Sources)
	statusTotal := 0
	for _, count := range statusCounts {
		statusTotal += count
	}
	summary.SourceStatusCountsMatch = statusTotal == summary.SourceCount
	summary.IncompleteCountMatches = summary.IncompleteSourceCount == incomplete
	summary.ExpandedCountMatchesNodes = b.result.Bounds.ExpandedNodes == expanded
	summary.CompleteMatchesSources = b.result.Complete == (incomplete == 0)
	b.result.Summary = summary
	if incomplete > 0 {
		b.result.Warnings = []string{"one or more requested graph sources are incomplete"}
	}
	if err := validateJiraGraphV2Result(b.result); err != nil {
		return nil, err
	}
	return b.result, nil
}

// ValidateJiraIssueGraphResult exposes the load-bearing schema-v2 reconciliation
// check to typed transports. It never rewrites or repairs an invalid result.
func ValidateJiraIssueGraphResult(result *JiraIssueGraphResult) error {
	return validateJiraGraphV2Result(result)
}

func jiraGraphV2FrontierID(id string) bool {
	if strings.HasPrefix(id, "jira:issue:") {
		key := strings.TrimPrefix(id, "jira:issue:")
		return jiraGraphExactKey(key) && id == "jira:issue:"+key
	}
	if strings.HasPrefix(id, "confluence:page:") {
		pageID := strings.TrimPrefix(id, "confluence:page:")
		return graphNumericIDPattern.MatchString(pageID) && id == "confluence:page:"+pageID
	}
	return false
}

func graphV2NodeSortKey(node domain.ArtifactGraphNode) string {
	return fmt.Sprintf("%08d\x00%s\x00%s", node.Depth, node.Kind, node.ID)
}

func graphV2SourceLess(left, right domain.ArtifactGraphSource, rank map[string]int) bool {
	if *left.NodeDepth != *right.NodeDepth {
		return *left.NodeDepth < *right.NodeDepth
	}
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	return rank[left.Kind] < rank[right.Kind]
}

func graphV2FrontierLess(left, right JiraIssueGraphFrontierItem) bool {
	if left.Depth != right.Depth {
		return left.Depth < right.Depth
	}
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	return left.Reason < right.Reason
}
