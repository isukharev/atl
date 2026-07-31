package domain

import "context"

// ArtifactGraphNodeState is the closed resolution state for one graph node.
type ArtifactGraphNodeState string

const (
	ArtifactNodeResolved   ArtifactGraphNodeState = "resolved"
	ArtifactNodeStub       ArtifactGraphNodeState = "stub"
	ArtifactNodeUnresolved ArtifactGraphNodeState = "unresolved"
	ArtifactNodeForbidden  ArtifactGraphNodeState = "forbidden"
	ArtifactNodeMissing    ArtifactGraphNodeState = "missing"
)

// ArtifactGraphStability qualifies the contract that produced a graph fact.
type ArtifactGraphStability string

const (
	ArtifactStabilityPublicAPI       ArtifactGraphStability = "public_api"
	ArtifactStabilityExperimentalAPI ArtifactGraphStability = "experimental_api"
	ArtifactStabilityHeuristic       ArtifactGraphStability = "heuristic"
)

// ArtifactGraphSourceStatus is the closed completeness result for a collector.
type ArtifactGraphSourceStatus string

const (
	ArtifactSourceComplete    ArtifactGraphSourceStatus = "complete"
	ArtifactSourceEmpty       ArtifactGraphSourceStatus = "empty"
	ArtifactSourcePartial     ArtifactGraphSourceStatus = "partial"
	ArtifactSourceForbidden   ArtifactGraphSourceStatus = "forbidden"
	ArtifactSourceUnsupported ArtifactGraphSourceStatus = "unsupported"
	ArtifactSourceSkipped     ArtifactGraphSourceStatus = "skipped"
)

// ArtifactGraphNode is one content-minimized work artifact. ExternalID and URL
// are omitted when an untrusted candidate cannot be projected safely.
type ArtifactGraphNode struct {
	ID         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Service    string                 `json:"service"`
	ExternalID string                 `json:"external_id,omitempty"`
	URL        string                 `json:"url,omitempty"`
	Label      string                 `json:"label,omitempty"`
	State      ArtifactGraphNodeState `json:"state"`
	Expanded   bool                   `json:"expanded"`
	Depth      int                    `json:"depth"`
	Stability  ArtifactGraphStability `json:"stability"`
}

// ArtifactGraphEvidence explains why an edge exists without copying source
// narrative, identities, or backend errors into the graph.
type ArtifactGraphEvidence struct {
	Collector    string `json:"collector"`
	SourceNodeID string `json:"source_node_id,omitempty"`
	SourceKind   string `json:"source_kind"`
	SourceID     string `json:"source_id,omitempty"`
	JSONPointer  string `json:"json_pointer,omitempty"`
	Extraction   string `json:"extraction"`
}

// ArtifactGraphEdge is one directed semantic relation. ID is derived from the
// semantic tuple; duplicate observations merge their evidence.
type ArtifactGraphEdge struct {
	ID           string                  `json:"id"`
	From         string                  `json:"from"`
	To           string                  `json:"to"`
	Kind         string                  `json:"kind"`
	RelationType string                  `json:"relation_type,omitempty"`
	Relation     string                  `json:"relation,omitempty"`
	Direction    string                  `json:"direction"`
	Current      bool                    `json:"current"`
	Confidence   string                  `json:"confidence"`
	Stability    ArtifactGraphStability  `json:"stability"`
	Evidence     []ArtifactGraphEvidence `json:"evidence"`
}

const (
	ArtifactPartialInspectionLimit       = "inspection_limit"
	ArtifactPartialOutputLimit           = "output_limit"
	ArtifactPartialRequestFailed         = "request_failed"
	ArtifactPartialRequestLimit          = "request_limit"
	ArtifactPartialByteLimit             = "byte_limit"
	ArtifactPartialDependencyUnavailable = "dependency_unavailable"
	ArtifactPartialMalformed             = "malformed_response"
	ArtifactPartialPolicy                = "policy"
)

// ValidArtifactPartialReason reports whether a source reason belongs to the
// static content-free graph vocabulary.
func ValidArtifactPartialReason(reason string) bool {
	switch reason {
	case ArtifactPartialInspectionLimit, ArtifactPartialOutputLimit,
		ArtifactPartialRequestFailed, ArtifactPartialRequestLimit,
		ArtifactPartialByteLimit, ArtifactPartialDependencyUnavailable,
		ArtifactPartialMalformed,
		ArtifactPartialPolicy:
		return true
	}
	return false
}

// ArtifactGraphSource qualifies one requested collector for one expanded node.
type ArtifactGraphSource struct {
	NodeID        string                    `json:"node_id"`
	NodeDepth     *int                      `json:"node_depth,omitempty"`
	Kind          string                    `json:"kind"`
	Requested     bool                      `json:"requested"`
	Status        ArtifactGraphSourceStatus `json:"status"`
	Complete      bool                      `json:"complete"`
	Count         int                       `json:"count"`
	Truncated     bool                      `json:"truncated,omitempty"`
	PartialReason string                    `json:"partial_reason,omitempty"`
	Stability     ArtifactGraphStability    `json:"stability"`
}

// IssueFieldSchema is the bounded Jira field metadata needed to distinguish
// structured fields and narrative strings in one exact issue snapshot.
type IssueFieldSchema struct {
	Type   string `json:"type,omitempty"`
	Items  string `json:"items,omitempty"`
	System string `json:"system,omitempty"`
	Custom string `json:"custom,omitempty"`
}

// QualifiedIssueSnapshot is the permission-relative result of one Jira issue
// request with fields/properties/names/schema expanded together.
type QualifiedIssueSnapshot struct {
	RequestedKey string
	ID           string
	Key          string
	Issue        Issue
	Fields       map[string]any
	Names        map[string]string
	Schema       map[string]IssueFieldSchema
	Properties   map[string]any
}

// QualifiedIssueSnapshotReader is the narrow read-only Jira capability used by
// graph construction. Implementations perform one issue request.
type QualifiedIssueSnapshotReader interface {
	ReadIssueSnapshot(ctx context.Context, key string) (*QualifiedIssueSnapshot, error)
}

// JiraRemoteLink is the supported Jira remote-link projection. It deliberately
// omits user records and preserves only bounded graph identity/label metadata.
type JiraRemoteLink struct {
	ID           string
	Relationship string
	ObjectURL    string
	ObjectTitle  string
}

// JiraRemoteLinkInventory distinguishes an empty complete list from a response
// containing malformed or unsafe rows. Unsupported rows are never projected.
type JiraRemoteLinkInventory struct {
	Links       []JiraRemoteLink
	Total       int
	Unsupported int
}

// JiraRemoteLinkReader is the narrow supported remote-link read capability.
type JiraRemoteLinkReader interface {
	ReadIssueRemoteLinks(ctx context.Context, key string) (JiraRemoteLinkInventory, error)
}
