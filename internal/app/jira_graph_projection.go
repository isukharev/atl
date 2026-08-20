package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const jiraIssueGraphCompactSchemaVersion = 1

func markJiraGraphSourceMalformed(source *domain.ArtifactGraphSource) {
	if source == nil {
		return
	}
	source.Status, source.Complete, source.Truncated, source.PartialReason =
		domain.ArtifactSourcePartial, false, false, domain.ArtifactPartialMalformed
}

// Closed Jira graph projection and selector names shared by transport
// frontends. Selectors reduce an already collected graph; they never change
// graph collection or its bounds.
const (
	JiraIssueGraphProjectionFull    = "full"
	JiraIssueGraphProjectionCompact = "compact"

	JiraIssueGraphSelectorURLs = "urls"
	JiraIssueGraphSelectorSCM  = "scm"
	JiraIssueGraphSelectorNone = "none"
)

var jiraIssueGraphFactClasses = []string{
	JiraIssueGraphSelectorURLs,
	JiraIssueGraphSelectorSCM,
}

// JiraIssueGraphProjectionOptions is the normalized closed projection
// selection shared by CLI and typed transports. Selectors accepts repeated or
// comma-separated raw input; NormalizeJiraIssueGraphProjection canonicalizes
// it before use. The none sentinel remains explicit so qualification-only
// compact output cannot be confused with an omitted selector list.
type JiraIssueGraphProjectionOptions struct {
	Projection string
	Selectors  []string
}

// JiraIssueGraphCompactProjection records which closed fact classes were
// intentionally selected and omitted.
type JiraIssueGraphCompactProjection struct {
	Name     string   `json:"name"`
	Selected []string `json:"selected"`
	Omitted  []string `json:"omitted"`
}

// JiraIssueGraphCompactFact is one content-minimized selected fact. URL facts
// carry only the already canonical safe URL, while opaque URL identities keep
// an empty URL. SCM facts copy only the closed coordinates and deliberately
// omit Development web URLs. SourceNodeIDs is content-free provenance.
type JiraIssueGraphCompactFact struct {
	Class         string                           `json:"class"`
	NodeID        string                           `json:"node_id"`
	Kind          string                           `json:"kind"`
	URL           string                           `json:"url,omitempty"`
	SCM           *domain.ArtifactGraphSCMIdentity `json:"scm,omitempty"`
	State         domain.ArtifactGraphNodeState    `json:"state"`
	Depth         int                              `json:"depth"`
	Stability     domain.ArtifactGraphStability    `json:"stability"`
	SourceNodeIDs []string                         `json:"source_node_ids"`
}

// JiraIssueGraphCompactCollectedSummary preserves the full graph's validated
// collection accounting without retaining its node, edge, or evidence arrays.
type JiraIssueGraphCompactCollectedSummary struct {
	NodeCount             int            `json:"node_count"`
	EdgeCount             int            `json:"edge_count"`
	EvidenceCount         int            `json:"evidence_count"`
	SourceCount           int            `json:"source_count"`
	IncompleteSourceCount int            `json:"incomplete_source_count"`
	SourceStatusCounts    map[string]int `json:"source_status_counts"`
}

// JiraIssueGraphCompactProjectedSummary reconciles facts and retained source
// qualifications in the compact result.
type JiraIssueGraphCompactProjectedSummary struct {
	FactCount             int            `json:"fact_count"`
	SourceCount           int            `json:"source_count"`
	URLCount              int            `json:"url_count"`
	SCMCount              int            `json:"scm_count"`
	IncompleteSourceCount int            `json:"incomplete_source_count"`
	SourceStatusCounts    map[string]int `json:"source_status_counts"`
}

// JiraIssueGraphCompactSummary proves that collected and projected counts
// reconcile with their corresponding inventories.
type JiraIssueGraphCompactSummary struct {
	Collected                          JiraIssueGraphCompactCollectedSummary `json:"collected"`
	Projected                          JiraIssueGraphCompactProjectedSummary `json:"projected"`
	CollectedCountsMatchFull           bool                                  `json:"collected_counts_match_full"`
	ProjectedFactCountMatchesFacts     bool                                  `json:"projected_fact_count_matches_facts"`
	FactClassCountsMatchFacts          bool                                  `json:"fact_class_counts_match_facts"`
	ProjectedSourceCountMatchesSources bool                                  `json:"projected_source_count_matches_sources"`
	SourceStatusCountsMatchSources     bool                                  `json:"source_status_counts_match_sources"`
	IncompleteCountMatchesSources      bool                                  `json:"incomplete_source_count_matches_sources"`
}

// JiraIssueGraphCompactResult is the schema-v1 qualified projection of an
// already collected and validated schema-v2 Jira graph.
type JiraIssueGraphCompactResult struct {
	SchemaVersion int                             `json:"schema_version"`
	Projection    JiraIssueGraphCompactProjection `json:"projection"`
	RootID        string                          `json:"root_id"`
	Complete      bool                            `json:"complete"`
	Truncated     bool                            `json:"truncated,omitempty"`
	Bounds        JiraIssueGraphBounds            `json:"bounds"`
	Summary       JiraIssueGraphCompactSummary    `json:"summary"`
	Facts         []JiraIssueGraphCompactFact     `json:"facts"`
	Sources       []domain.ArtifactGraphSource    `json:"sources"`
	Frontier      []JiraIssueGraphFrontierItem    `json:"frontier,omitempty"`
	Warnings      []string                        `json:"warnings,omitempty"`
}

// NormalizeJiraIssueGraphProjection validates and canonicalizes the closed
// full|compact projection contract. Raw selectors may be repeated and/or
// comma-separated. Compact defaults to URLs and, when Development collection
// is enabled, SCM. Full output does not accept selectors.
func NormalizeJiraIssueGraphProjection(projection string, selectors []string, includeDevelopment bool) (JiraIssueGraphProjectionOptions, error) {
	projection = strings.ToLower(strings.TrimSpace(projection))
	if projection == "" {
		projection = JiraIssueGraphProjectionFull
	}
	if projection != JiraIssueGraphProjectionFull && projection != JiraIssueGraphProjectionCompact {
		return JiraIssueGraphProjectionOptions{}, fmt.Errorf("%w: --projection must be full or compact", domain.ErrUsage)
	}

	parsed, specified, err := normalizeJiraIssueGraphSelectors(selectors)
	if err != nil {
		return JiraIssueGraphProjectionOptions{}, err
	}
	if projection == JiraIssueGraphProjectionFull {
		if specified {
			return JiraIssueGraphProjectionOptions{}, fmt.Errorf("%w: --select requires --projection compact", domain.ErrUsage)
		}
		return JiraIssueGraphProjectionOptions{Projection: projection, Selectors: []string{}}, nil
	}
	if !specified {
		parsed = []string{JiraIssueGraphSelectorURLs}
		if includeDevelopment {
			parsed = append(parsed, JiraIssueGraphSelectorSCM)
		}
	}
	if containsJiraIssueGraphSelector(parsed, JiraIssueGraphSelectorNone) && len(parsed) != 1 {
		return JiraIssueGraphProjectionOptions{}, fmt.Errorf("%w: --select none cannot be combined with urls or scm", domain.ErrUsage)
	}
	if containsJiraIssueGraphSelector(parsed, JiraIssueGraphSelectorSCM) && !includeDevelopment {
		return JiraIssueGraphProjectionOptions{}, fmt.Errorf("%w: --select scm requires --include-development", domain.ErrUsage)
	}
	return JiraIssueGraphProjectionOptions{Projection: projection, Selectors: parsed}, nil
}

func normalizeJiraIssueGraphSelectors(raw []string) ([]string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	seen := map[string]bool{}
	for _, entry := range raw {
		parts := strings.Split(entry, ",")
		for _, part := range parts {
			selector := strings.ToLower(strings.TrimSpace(part))
			if selector == "" || (selector != JiraIssueGraphSelectorURLs &&
				selector != JiraIssueGraphSelectorSCM && selector != JiraIssueGraphSelectorNone) {
				return nil, true, fmt.Errorf("%w: --select must contain only urls, scm, or none", domain.ErrUsage)
			}
			seen[selector] = true
		}
	}
	selectors := make([]string, 0, len(seen))
	for _, selector := range append(append([]string(nil), jiraIssueGraphFactClasses...), JiraIssueGraphSelectorNone) {
		if seen[selector] {
			selectors = append(selectors, selector)
		}
	}
	return selectors, true, nil
}

func containsJiraIssueGraphSelector(selectors []string, selected string) bool {
	for _, selector := range selectors {
		if selector == selected {
			return true
		}
	}
	return false
}

// ProjectJiraIssueGraphCompact copies selected facts from a fully collected
// graph. Validation is deliberately the first operation: malformed complete-
// looking input is rejected rather than repaired, filtered, or interpreted.
func ProjectJiraIssueGraphCompact(full *JiraIssueGraphResult, opts JiraIssueGraphProjectionOptions) (*JiraIssueGraphCompactResult, error) {
	if err := ValidateJiraIssueGraphResult(full); err != nil {
		return nil, err
	}
	normalized, err := NormalizeJiraIssueGraphProjection(opts.Projection, opts.Selectors, full.Bounds.IncludeDevelopment)
	if err != nil {
		return nil, err
	}
	if normalized.Projection != JiraIssueGraphProjectionCompact {
		return nil, fmt.Errorf("%w: compact projector requires --projection compact", domain.ErrUsage)
	}

	selected := make(map[string]bool, len(normalized.Selectors))
	for _, selector := range normalized.Selectors {
		if selector != JiraIssueGraphSelectorNone {
			selected[selector] = true
		}
	}
	projection := JiraIssueGraphCompactProjection{
		Name: JiraIssueGraphProjectionCompact, Selected: []string{}, Omitted: []string{},
	}
	for _, class := range jiraIssueGraphFactClasses {
		if selected[class] {
			projection.Selected = append(projection.Selected, class)
		} else {
			projection.Omitted = append(projection.Omitted, class)
		}
	}

	provenance := jiraIssueGraphCompactProvenance(full)
	facts := make([]JiraIssueGraphCompactFact, 0)
	for _, node := range full.Nodes {
		var fact JiraIssueGraphCompactFact
		var project bool
		switch {
		case selected[JiraIssueGraphSelectorURLs] && node.Kind == "url":
			fact, err = jiraIssueGraphCompactURLFact(node, provenance[node.ID])
			project = true
		case selected[JiraIssueGraphSelectorSCM] && strings.HasPrefix(node.Kind, "gitlab_"):
			fact, err = jiraIssueGraphCompactSCMFact(node, provenance[node.ID])
			project = true
		}
		if err != nil {
			return nil, err
		}
		if project {
			facts = append(facts, fact)
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		left, right := jiraIssueGraphCompactFactSortKey(facts[i]), jiraIssueGraphCompactFactSortKey(facts[j])
		return left < right
	})

	sources := make([]domain.ArtifactGraphSource, 0)
	for _, source := range full.Sources {
		if source.Complete && (!selected[JiraIssueGraphSelectorSCM] || source.Kind != "development") {
			continue
		}
		sources = append(sources, cloneJiraIssueGraphSource(source))
	}

	result := &JiraIssueGraphCompactResult{
		SchemaVersion: jiraIssueGraphCompactSchemaVersion,
		Projection:    projection,
		RootID:        full.RootID,
		Complete:      full.Complete,
		Truncated:     full.Truncated,
		Bounds:        full.Bounds,
		Facts:         facts,
		Sources:       sources,
		Frontier:      append([]JiraIssueGraphFrontierItem(nil), full.Frontier...),
		Warnings:      append([]string(nil), full.Warnings...),
	}
	result.Summary = jiraIssueGraphCompactSummary(full, result)
	return result, nil
}

func jiraIssueGraphCompactProvenance(full *JiraIssueGraphResult) map[string][]string {
	byNode := make(map[string]map[string]bool)
	for _, edge := range full.Edges {
		for _, evidence := range edge.Evidence {
			if evidence.SourceNodeID == "" {
				continue
			}
			if byNode[edge.To] == nil {
				byNode[edge.To] = map[string]bool{}
			}
			byNode[edge.To][evidence.SourceNodeID] = true
		}
	}
	out := make(map[string][]string, len(byNode))
	for nodeID, sourceSet := range byNode {
		sourceIDs := make([]string, 0, len(sourceSet))
		for sourceID := range sourceSet {
			sourceIDs = append(sourceIDs, sourceID)
		}
		sort.Strings(sourceIDs)
		out[nodeID] = sourceIDs
	}
	return out
}

func jiraIssueGraphCompactURLFact(node domain.ArtifactGraphNode, sourceNodeIDs []string) (JiraIssueGraphCompactFact, error) {
	invalid := func() (JiraIssueGraphCompactFact, error) {
		return JiraIssueGraphCompactFact{}, fmt.Errorf("%w: Jira graph compact URL fact is not canonical", domain.ErrCheckFailed)
	}
	if len(sourceNodeIDs) == 0 {
		return invalid()
	}
	switch {
	case strings.HasPrefix(node.ID, "url:"):
		reference, ok := normalizeGraphURL(node.URL, "", "")
		if !ok || node.URL == "" || reference.Node.Kind != "url" ||
			reference.Node.ID != node.ID || reference.Node.URL != node.URL {
			return invalid()
		}
	case strings.HasPrefix(node.ID, "candidate:url:"):
		if node.URL != "" || node.State != domain.ArtifactNodeUnresolved ||
			!jiraIssueGraphCompactHashID(node.ID, "candidate:url:") {
			return invalid()
		}
	default:
		return invalid()
	}
	return JiraIssueGraphCompactFact{
		Class: JiraIssueGraphSelectorURLs, NodeID: node.ID, Kind: node.Kind,
		URL: node.URL, State: node.State, Depth: node.Depth, Stability: node.Stability,
		SourceNodeIDs: append([]string(nil), sourceNodeIDs...),
	}, nil
}

func jiraIssueGraphCompactSCMFact(node domain.ArtifactGraphNode, sourceNodeIDs []string) (JiraIssueGraphCompactFact, error) {
	if len(sourceNodeIDs) == 0 || node.SCM == nil || !validateJiraDevelopmentGraphNode(node) {
		return JiraIssueGraphCompactFact{}, fmt.Errorf("%w: Jira graph compact SCM fact is not canonical", domain.ErrCheckFailed)
	}
	scm := &domain.ArtifactGraphSCMIdentity{ //nolint:staticcheck // Closed projection: copy every allowed coordinate explicitly.
		Host: node.SCM.Host, ProjectPath: node.SCM.ProjectPath,
		CommitSHA: node.SCM.CommitSHA, BranchName: node.SCM.BranchName,
		MergeRequestIID: node.SCM.MergeRequestIID, MergeRequestState: node.SCM.MergeRequestState,
	}
	return JiraIssueGraphCompactFact{
		Class: JiraIssueGraphSelectorSCM, NodeID: node.ID, Kind: node.Kind,
		SCM: scm, State: node.State, Depth: node.Depth, Stability: node.Stability,
		SourceNodeIDs: append([]string(nil), sourceNodeIDs...),
	}, nil
}

func jiraIssueGraphCompactHashID(id, prefix string) bool {
	digest := strings.TrimPrefix(id, prefix)
	if id == digest || len(digest) != 64 {
		return false
	}
	for _, current := range digest {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func jiraIssueGraphCompactFactSortKey(fact JiraIssueGraphCompactFact) string {
	rank := "1"
	if fact.Class == JiraIssueGraphSelectorURLs {
		rank = "0"
	}
	return rank + "\x00" + fact.Kind + "\x00" + fact.NodeID
}

func cloneJiraIssueGraphSource(source domain.ArtifactGraphSource) domain.ArtifactGraphSource {
	if source.NodeDepth != nil {
		depth := *source.NodeDepth
		source.NodeDepth = &depth
	}
	return source
}

func jiraIssueGraphCompactSummary(full *JiraIssueGraphResult, compact *JiraIssueGraphCompactResult) JiraIssueGraphCompactSummary {
	collected := JiraIssueGraphCompactCollectedSummary{
		NodeCount: full.Summary.NodeCount, EdgeCount: full.Summary.EdgeCount,
		EvidenceCount: full.Summary.EvidenceCount, SourceCount: full.Summary.SourceCount,
		IncompleteSourceCount: full.Summary.IncompleteSourceCount,
		SourceStatusCounts:    cloneJiraIssueGraphStatusCounts(full.Summary.SourceStatusCounts),
	}
	projected := JiraIssueGraphCompactProjectedSummary{
		FactCount: len(compact.Facts), SourceCount: len(compact.Sources),
		SourceStatusCounts: jiraIssueGraphEmptyStatusCounts(),
	}
	for _, fact := range compact.Facts {
		switch fact.Class {
		case JiraIssueGraphSelectorURLs:
			projected.URLCount++
		case JiraIssueGraphSelectorSCM:
			projected.SCMCount++
		}
	}
	for _, source := range compact.Sources {
		projected.SourceStatusCounts[string(source.Status)]++
		if !source.Complete {
			projected.IncompleteSourceCount++
		}
	}
	statusTotal := 0
	for _, count := range projected.SourceStatusCounts {
		statusTotal += count
	}
	actualIncomplete := 0
	for _, source := range compact.Sources {
		if !source.Complete {
			actualIncomplete++
		}
	}
	return JiraIssueGraphCompactSummary{
		Collected: collected,
		Projected: projected,
		CollectedCountsMatchFull: collected.NodeCount == len(full.Nodes) &&
			collected.EdgeCount == len(full.Edges) &&
			collected.EvidenceCount == jiraIssueGraphEvidenceCount(full.Edges) &&
			collected.SourceCount == len(full.Sources) &&
			collected.IncompleteSourceCount == jiraIssueGraphIncompleteSourceCount(full.Sources) &&
			equalStringIntMap(collected.SourceStatusCounts, jiraIssueGraphSourceStatusCounts(full.Sources)),
		ProjectedFactCountMatchesFacts:     projected.FactCount == len(compact.Facts),
		FactClassCountsMatchFacts:          projected.FactCount == projected.URLCount+projected.SCMCount,
		ProjectedSourceCountMatchesSources: projected.SourceCount == len(compact.Sources),
		SourceStatusCountsMatchSources: statusTotal == len(compact.Sources) &&
			equalStringIntMap(projected.SourceStatusCounts, jiraIssueGraphSourceStatusCounts(compact.Sources)),
		IncompleteCountMatchesSources: projected.IncompleteSourceCount == actualIncomplete,
	}
}

func jiraIssueGraphEmptyStatusCounts() map[string]int {
	return map[string]int{
		"complete": 0, "empty": 0, "partial": 0,
		"forbidden": 0, "unsupported": 0, "skipped": 0,
	}
}

func cloneJiraIssueGraphStatusCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for status, count := range in {
		out[status] = count
	}
	return out
}

func jiraIssueGraphSourceStatusCounts(sources []domain.ArtifactGraphSource) map[string]int {
	counts := jiraIssueGraphEmptyStatusCounts()
	for _, source := range sources {
		counts[string(source.Status)]++
	}
	return counts
}

func jiraIssueGraphIncompleteSourceCount(sources []domain.ArtifactGraphSource) int {
	count := 0
	for _, source := range sources {
		if !source.Complete {
			count++
		}
	}
	return count
}

func jiraIssueGraphEvidenceCount(edges []domain.ArtifactGraphEdge) int {
	count := 0
	for _, edge := range edges {
		count += len(edge.Evidence)
	}
	return count
}
