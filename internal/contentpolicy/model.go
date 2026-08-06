// Package contentpolicy evaluates local, content-scoped write policies.
// It deliberately depends only on the transport-neutral domain package.
package contentpolicy

import "github.com/isukharev/atl/internal/domain"

const SchemaVersion = 1

type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

type Policy struct {
	SchemaVersion int
	Rules         []Rule
	Backend       BackendBinding
}

type BackendBinding struct {
	JiraSHA256       string
	ConfluenceSHA256 string
}

type Rule struct {
	ID       string
	Effect   Effect
	Verbs    domain.WriteVerbSet
	Resource Selector
}

// Selector is a conjunction. Each populated slice is an exact any-of matcher;
// wildcard and pattern matching are intentionally unsupported.
type Selector struct {
	Services []string
	Kinds    []string
	Projects []string
	Keys     []string
	Spaces   []string
	IDs      []string
	Under    []string
}

type Decision struct {
	Allowed   bool
	Reason    DenialReason
	RuleID    string
	Layer     string
	Target    int
	Verb      domain.WriteVerb
	Attribute string
}

type Layer struct {
	Source string
	Digest string
	Policy Policy
}

type Resolved struct {
	Layers   []Layer
	Warnings []Warning
}

type Warning struct {
	Source  string
	RuleID  string
	Message string
}
