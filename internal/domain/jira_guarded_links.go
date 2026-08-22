package domain

import "context"

// JiraLinkTypeMetadata is the immutable Jira issue-link type identity used by
// guarded link proposals. Exact strings are retained for review and hashing.
type JiraLinkTypeMetadata struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// JiraStrictIssueLink is one structurally qualified endpoint-local link row.
// Role is the current endpoint's immutable role (inward or outward); Other is
// the reciprocal endpoint embedded by Jira.
type JiraStrictIssueLink struct {
	ID       string               `json:"id"`
	Type     JiraLinkTypeMetadata `json:"type"`
	Role     string               `json:"role"`
	OtherID  string               `json:"other_id"`
	OtherKey string               `json:"other_key"`
}

// JiraStrictLinkEndpoint is one complete, non-paginated issuelinks snapshot.
// Complete qualifies only the documented direct issue response.
type JiraStrictLinkEndpoint struct {
	ID       string                `json:"id"`
	Key      string                `json:"key"`
	Project  string                `json:"project"`
	Links    []JiraStrictIssueLink `json:"links"`
	Complete bool                  `json:"complete"`
}

// JiraStrictLinkCatalog is the complete non-paginated type catalog.
type JiraStrictLinkCatalog struct {
	Types    []JiraLinkTypeMetadata `json:"types"`
	Complete bool                   `json:"complete"`
}

// JiraGuardedLinkWrite identifies the already-qualified mutation. Endpoint
// IDs are transport identity only; authorization uses the exact keys/projects.
type JiraGuardedLinkWrite struct {
	TypeID  string
	Outward JiraStrictLinkEndpoint
	Inward  JiraStrictLinkEndpoint
	LinkID  string
}

// JiraGuardedLinkPort is intentionally separate from Tracker so legacy link,
// plan, suggest, list, link-types, capability, and MCP contracts remain exact.
type JiraGuardedLinkPort interface {
	ReadStrictLinkTypes(context.Context) (JiraStrictLinkCatalog, error)
	ReadStrictLinkEndpoint(context.Context, string) (JiraStrictLinkEndpoint, error)
	AddGuardedLink(context.Context, JiraGuardedLinkWrite) error
	DeleteGuardedLink(context.Context, JiraGuardedLinkWrite) error
}
