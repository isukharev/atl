package domain

import (
	"context"
	"encoding/json"
)

// JiraInverseReferenceTargetKind identifies the bounded external artifact
// namespace an inverse Jira search may inspect. Target values never carry a
// title, snippet, or backend URL.
type JiraInverseReferenceTargetKind string

const (
	JiraInverseReferenceTargetConfluencePage JiraInverseReferenceTargetKind = "confluence_page"
	JiraInverseReferenceTargetGitLabProject  JiraInverseReferenceTargetKind = "gitlab_project"
)

// ValidJiraInverseReferenceTargetKind reports whether kind is one of the
// fixed inverse-reference target namespaces.
func ValidJiraInverseReferenceTargetKind(kind JiraInverseReferenceTargetKind) bool {
	switch kind {
	case JiraInverseReferenceTargetConfluencePage, JiraInverseReferenceTargetGitLabProject:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceTarget is the exact content-free target identity to find
// in Jira sources. Value is a backend identity, such as a Confluence content
// id or a canonical GitLab project path; it is never a display label.
type JiraInverseReferenceTarget struct {
	Kind  JiraInverseReferenceTargetKind
	Value string
}

// JiraInverseReferenceMode is the command's bounded search policy. Exhaustive
// allows the app to prove terminal coverage; fast permits its documented early
// stop policy without changing any adapter's pagination coordinates.
type JiraInverseReferenceMode string

const (
	JiraInverseReferenceModeExhaustive JiraInverseReferenceMode = "exhaustive"
	JiraInverseReferenceModeFast       JiraInverseReferenceMode = "fast"
)

// ValidJiraInverseReferenceMode reports whether mode belongs to the fixed
// inverse-reference policy set.
func ValidJiraInverseReferenceMode(mode JiraInverseReferenceMode) bool {
	switch mode {
	case JiraInverseReferenceModeExhaustive, JiraInverseReferenceModeFast:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceSource identifies one bounded Jira location inspected
// locally after candidate selection. It is not an arbitrary backend field or
// endpoint name.
type JiraInverseReferenceSource string

const (
	JiraInverseReferenceSourceDescription JiraInverseReferenceSource = "description"
	JiraInverseReferenceSourceFields      JiraInverseReferenceSource = "fields"
	JiraInverseReferenceSourceComments    JiraInverseReferenceSource = "comments"
	JiraInverseReferenceSourceDevelopment JiraInverseReferenceSource = "development"
	JiraInverseReferenceSourceProperties  JiraInverseReferenceSource = "properties"
	JiraInverseReferenceSourceRemoteLinks JiraInverseReferenceSource = "remote_links"
	JiraInverseReferenceSourceWorklogs    JiraInverseReferenceSource = "worklogs"
)

// ValidJiraInverseReferenceSource reports whether source belongs to the fixed
// inspected-source set.
func ValidJiraInverseReferenceSource(source JiraInverseReferenceSource) bool {
	switch source {
	case JiraInverseReferenceSourceDescription, JiraInverseReferenceSourceFields, JiraInverseReferenceSourceComments,
		JiraInverseReferenceSourceDevelopment, JiraInverseReferenceSourceProperties,
		JiraInverseReferenceSourceRemoteLinks, JiraInverseReferenceSourceWorklogs:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceOrder specifies the one deterministic candidate order.
// Exhaustive search repeats this same order for both terminal passes so a set
// mismatch detects drift without claiming snapshot isolation.
type JiraInverseReferenceOrder string

const (
	JiraInverseReferenceOrderAscending JiraInverseReferenceOrder = "ascending"
)

// ValidJiraInverseReferenceOrder reports whether order is a supported
// deterministic candidate ordering.
func ValidJiraInverseReferenceOrder(order JiraInverseReferenceOrder) bool {
	switch order {
	case JiraInverseReferenceOrderAscending:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceIssueIdentity is the minimal Jira identity retained by
// candidate pages and content-free match results.
type JiraInverseReferenceIssueIdentity struct {
	ID  string
	Key string
}

// JiraInverseReferenceSelection describes exactly one page of one ordered
// candidate pass. JQL is the caller-qualified predicate, including its stable
// ORDER BY clause; app code owns safe composition and rejects a conflicting
// ordering before this port is called. Adapters execute it but never return it
// in a result or error. StartAt and MaxResults are Jira pagination coordinates;
// the caller never infers them from a cursor or an accumulated issue count.
// Sources tells the selector which configured source classes justify this
// bounded candidate selection, while matching remains app-local.
type JiraInverseReferenceSelection struct {
	Target     JiraInverseReferenceTarget
	Mode       JiraInverseReferenceMode
	Sources    []JiraInverseReferenceSource
	JQL        string
	Order      JiraInverseReferenceOrder
	StartAt    int
	MaxResults int
}

// JiraInverseReferencePage preserves Jira's raw pagination coordinates. A
// caller can prove that each repeated ascending pass reached a terminal
// response only from StartAt, MaxResults, Total, and the returned identities.
// It intentionally carries neither summaries nor source text.
type JiraInverseReferencePage struct {
	StartAt    int
	MaxResults int
	Total      int
	Issues     []JiraInverseReferenceIssueIdentity
}

// JiraInverseReferenceSelector is the narrow Jira candidate selector for an
// inverse-reference search. Implementations consume the shared command budget
// carried by ctx with WithReadBudget and make one physical request per call;
// app orchestration supplies WithSingleAttempt and owns terminal-pass proof.
type JiraInverseReferenceSelector interface {
	SelectInverseReferencePage(ctx context.Context, selection JiraInverseReferenceSelection) (JiraInverseReferencePage, error)
}

// JiraInverseReferenceSnapshotRequest asks for exactly the field ids needed
// for local matching on one candidate. IncludeProperties is opt-in because
// property expansion may expose a separate source class. FieldIDs is an exact
// bounded set, not a request for Jira's global or all-fields projection.
type JiraInverseReferenceSnapshotRequest struct {
	Issue             JiraInverseReferenceIssueIdentity
	FieldIDs          []string
	IncludeProperties bool
}

// JiraInverseReferenceFieldSnapshot distinguishes an absent requested field
// from an explicit JSON null. Present is false only when Jira omitted FieldID;
// a present null has Present true and Value containing the JSON token "null".
// Value is raw bounded JSON for app-local matching and must not be emitted as a
// search result.
type JiraInverseReferenceFieldSnapshot struct {
	FieldID string
	Present bool
	Value   json.RawMessage
}

// JiraInverseReferencePropertySnapshot is one opt-in raw bounded property
// value retained only for local matching. Property keys and values are not
// exposed through the content-free result DTOs.
type JiraInverseReferencePropertySnapshot struct {
	Key   string
	Value json.RawMessage
}

// JiraInverseReferenceSnapshot contains only the exact candidate identity and
// requested raw source structures needed for local matching. Field entries
// must retain every requested FieldID so missing is never hidden by a map
// lookup. Properties is nil when IncludeProperties was false.
type JiraInverseReferenceSnapshot struct {
	Issue      JiraInverseReferenceIssueIdentity
	Fields     []JiraInverseReferenceFieldSnapshot
	Properties []JiraInverseReferencePropertySnapshot
}

// JiraInverseReferenceSnapshotReader reads one purpose-specific candidate
// snapshot. Implementations consume the shared ReadBudget in ctx and make one
// physical request per call; comments, development, and remote links retain
// their existing narrow readers rather than widening this snapshot.
type JiraInverseReferenceSnapshotReader interface {
	ReadInverseReferenceSnapshot(ctx context.Context, request JiraInverseReferenceSnapshotRequest) (JiraInverseReferenceSnapshot, error)
}

// JiraInverseReferenceMatchStatus is the content-free local match conclusion
// for one candidate. Indeterminate means at least one requested source could
// not supply decisive evidence; it is distinct from NotMatched.
type JiraInverseReferenceMatchStatus string

const (
	JiraInverseReferenceMatched       JiraInverseReferenceMatchStatus = "matched"
	JiraInverseReferenceNotMatched    JiraInverseReferenceMatchStatus = "not_matched"
	JiraInverseReferenceIndeterminate JiraInverseReferenceMatchStatus = "indeterminate"
)

// ValidJiraInverseReferenceMatchStatus reports whether status belongs to the
// fixed local-match vocabulary.
func ValidJiraInverseReferenceMatchStatus(status JiraInverseReferenceMatchStatus) bool {
	switch status {
	case JiraInverseReferenceMatched, JiraInverseReferenceNotMatched, JiraInverseReferenceIndeterminate:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceSourceStatus is the content-free evidence state for one
// inspected source.
type JiraInverseReferenceSourceStatus string

const (
	JiraInverseReferenceSourceComplete    JiraInverseReferenceSourceStatus = "complete"
	JiraInverseReferenceSourceEmpty       JiraInverseReferenceSourceStatus = "empty"
	JiraInverseReferenceSourcePartial     JiraInverseReferenceSourceStatus = "partial"
	JiraInverseReferenceSourceForbidden   JiraInverseReferenceSourceStatus = "forbidden"
	JiraInverseReferenceSourceUnsupported JiraInverseReferenceSourceStatus = "unsupported"
	JiraInverseReferenceSourceSkipped     JiraInverseReferenceSourceStatus = "skipped"
)

// ValidJiraInverseReferenceSourceStatus reports whether status belongs to the
// fixed source-evidence vocabulary.
func ValidJiraInverseReferenceSourceStatus(status JiraInverseReferenceSourceStatus) bool {
	switch status {
	case JiraInverseReferenceSourceComplete, JiraInverseReferenceSourceEmpty,
		JiraInverseReferenceSourcePartial, JiraInverseReferenceSourceForbidden,
		JiraInverseReferenceSourceUnsupported, JiraInverseReferenceSourceSkipped:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceReason is a static, content-free explanation for a
// non-complete source outcome. It never carries a backend error, query, title,
// snippet, username, URL, or raw source value.
type JiraInverseReferenceReason string

const (
	JiraInverseReferenceReasonRequestFailed JiraInverseReferenceReason = "request_failed"
	JiraInverseReferenceReasonRequestLimit  JiraInverseReferenceReason = "request_limit"
	JiraInverseReferenceReasonByteLimit     JiraInverseReferenceReason = "byte_limit"
	JiraInverseReferenceReasonMalformed     JiraInverseReferenceReason = "malformed_response"
	JiraInverseReferenceReasonFieldMissing  JiraInverseReferenceReason = "field_missing"
	JiraInverseReferenceReasonNotPermitted  JiraInverseReferenceReason = "not_permitted"
	JiraInverseReferenceReasonNotSupported  JiraInverseReferenceReason = "not_supported"
	JiraInverseReferenceReasonModeFast      JiraInverseReferenceReason = "mode_fast"
)

// ValidJiraInverseReferenceReason reports whether reason belongs to the fixed
// non-content source-outcome vocabulary.
func ValidJiraInverseReferenceReason(reason JiraInverseReferenceReason) bool {
	switch reason {
	case JiraInverseReferenceReasonRequestFailed, JiraInverseReferenceReasonRequestLimit,
		JiraInverseReferenceReasonByteLimit, JiraInverseReferenceReasonMalformed,
		JiraInverseReferenceReasonFieldMissing, JiraInverseReferenceReasonNotPermitted,
		JiraInverseReferenceReasonNotSupported, JiraInverseReferenceReasonModeFast:
		return true
	default:
		return false
	}
}

// JiraInverseReferenceSourceOutcome is one content-free local source
// conclusion. Reason is empty for Complete and Empty outcomes, or a member of
// the closed JiraInverseReferenceReason set otherwise.
type JiraInverseReferenceSourceOutcome struct {
	Source JiraInverseReferenceSource
	Status JiraInverseReferenceSourceStatus
	Reason JiraInverseReferenceReason
}

// JiraInverseReferenceMatch is the content-free output projection for one
// candidate. It deliberately retains no Jira summary, source bytes, remote
// link title, comment text, property key, or user identity.
type JiraInverseReferenceMatch struct {
	Issue   JiraInverseReferenceIssueIdentity
	Status  JiraInverseReferenceMatchStatus
	Sources []JiraInverseReferenceSourceOutcome
}
