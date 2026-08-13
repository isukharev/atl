package corpus

const (
	IndexerSchemaV1          = 1
	IndexerReceiptSchemaV1   = 1
	IndexerDocumentsStableID = "indexer-v1-documents"
)

// ObjectKind is the closed logical object namespace used to derive stable
// indexer identities. Human keys, titles, and mirror paths are deliberately
// absent from that identity preimage. Backend identity uses the mirror's
// tagged sha256: origin digest rather than a raw URL.
type ObjectKind string

const (
	ObjectPage       ObjectKind = "page"
	ObjectIssue      ObjectKind = "issue"
	ObjectComment    ObjectKind = "comment"
	ObjectAttachment ObjectKind = "attachment"
)

// Visibility records only visibility proven by the captured source evidence.
// Unknown must never be interpreted as unrestricted.
type Visibility string

const (
	VisibilityRestricted   Visibility = "restricted"
	VisibilityUnrestricted Visibility = "unrestricted"
	VisibilityUnknown      Visibility = "unknown"
)

// RenderStatus distinguishes an empty body from a failed or unsupported
// projection. Only rendered and empty documents may name a Markdown member.
type RenderStatus string

const (
	RenderRendered    RenderStatus = "rendered"
	RenderEmpty       RenderStatus = "empty"
	RenderFailed      RenderStatus = "failed"
	RenderUnsupported RenderStatus = "unsupported"
)

// EvidenceKind is the closed set of independently qualified document facets.
type EvidenceKind string

const (
	EvidenceMetadata    EvidenceKind = "metadata"
	EvidenceBody        EvidenceKind = "body"
	EvidenceHierarchy   EvidenceKind = "hierarchy"
	EvidenceRelations   EvidenceKind = "relations"
	EvidenceComments    EvidenceKind = "comments"
	EvidenceAttachments EvidenceKind = "attachments"
	EvidenceVisibility  EvidenceKind = "visibility"
)

// EvidenceStatus prevents an omitted or unreadable source from being
// represented as a qualified empty collection.
type EvidenceStatus string

const (
	EvidenceComplete     EvidenceStatus = "complete"
	EvidencePartial      EvidenceStatus = "partial"
	EvidenceForbidden    EvidenceStatus = "forbidden"
	EvidenceUnavailable  EvidenceStatus = "unavailable"
	EvidenceNotRequested EvidenceStatus = "not_requested"
	EvidenceUnsupported  EvidenceStatus = "unsupported"
)

// EvidenceReason is intentionally categorical and content-free.
type EvidenceReason string

const (
	EvidenceTruncated         EvidenceReason = "truncated"
	EvidenceMissing           EvidenceReason = "missing"
	EvidenceCorrupt           EvidenceReason = "corrupt"
	EvidenceRestrictedReason  EvidenceReason = "restricted"
	EvidenceLegacyUnqualified EvidenceReason = "legacy_unqualified"
	EvidenceRenderFailed      EvidenceReason = "render_failed"
	EvidenceUnresolved        EvidenceReason = "unresolved"
	EvidenceUnsupportedReason EvidenceReason = "unsupported"
)

// Evidence reports one facet's status. ObservedCount is exact when CountExact
// is true and a lower bound for partial evidence; exact zero remains distinct
// from unknown.
type Evidence struct {
	Kind          EvidenceKind     `json:"kind"`
	Status        EvidenceStatus   `json:"status"`
	Reasons       []EvidenceReason `json:"reasons"`
	ObservedCount int              `json:"observed_count"`
	CountExact    bool             `json:"count_exact"`
}

// SourceLineage binds a projected document to exact pristine mirror inputs.
// Path is mirror-relative and NativeSHA256 hashes the captured baseline, never
// an ambient working file. MetadataSHA256 binds the correlated metadata bytes.
type SourceLineage struct {
	Path           string `json:"path"`
	NativeSHA256   string `json:"native_sha256"`
	MetadataSHA256 string `json:"metadata_sha256"`
}

// IndexerDocument is one canonical indexer-v1 logical document. Text contains
// the clean logical body once; presentation metadata and auxiliary evidence do
// not repeat staging-view compatibility sections.
type IndexerDocument struct {
	SchemaVersion  int           `json:"schema_version"`
	ID             string        `json:"id"`
	Service        Service       `json:"service"`
	Kind           ObjectKind    `json:"kind"`
	Key            string        `json:"key,omitempty"`
	Title          string        `json:"title,omitempty"`
	Container      string        `json:"container,omitempty"`
	Version        string        `json:"version,omitempty"`
	Updated        string        `json:"updated,omitempty"`
	Labels         []string      `json:"labels"`
	Source         SourceLineage `json:"source"`
	Text           string        `json:"text"`
	BodySHA256     string        `json:"body_sha256"`
	RenderStatus   RenderStatus  `json:"render_status"`
	MarkdownPath   string        `json:"markdown_path,omitempty"`
	MarkdownSHA256 string        `json:"markdown_sha256,omitempty"`
	Visibility     Visibility    `json:"visibility"`
	Evidence       []Evidence    `json:"evidence"`
}

// EdgeRelation is a provider-neutral relationship category. Provider-specific
// relationship names remain bounded presentation metadata in RelationName.
type EdgeRelation string

const (
	EdgeParent          EdgeRelation = "parent"
	EdgeContains        EdgeRelation = "contains"
	EdgeReferences      EdgeRelation = "references"
	EdgeJiraIssueLink   EdgeRelation = "jira_issue_link"
	EdgeCommentOwner    EdgeRelation = "comment_owner"
	EdgeCommentReply    EdgeRelation = "comment_reply"
	EdgeAttachmentOwner EdgeRelation = "attachment_owner"
)

type EdgeDirection string

const (
	DirectionOutbound   EdgeDirection = "outbound"
	DirectionInbound    EdgeDirection = "inbound"
	DirectionUndirected EdgeDirection = "undirected"
	DirectionUnknown    EdgeDirection = "unknown"
)

type EdgeConfidence string

const (
	ConfidenceExact      EdgeConfidence = "exact"
	ConfidenceReported   EdgeConfidence = "reported"
	ConfidenceStructural EdgeConfidence = "structural"
)

// Reference preserves an unresolved provider reference without inventing a
// target corpus identity. Value may be a current key, content title, or numeric
// provider ID, but never a raw backend URL.
type Reference struct {
	Service Service    `json:"service"`
	Kind    ObjectKind `json:"kind"`
	Value   string     `json:"value"`
}

// EdgeEvidence locates the bounded structured evidence for one edge without
// carrying a backend URL.
type EdgeEvidence struct {
	Kind     EvidenceKind `json:"kind"`
	Path     string       `json:"path"`
	Fragment string       `json:"fragment,omitempty"`
}

// IndexerEdge names either a qualified in-generation TargetID or exactly one
// unresolved provider reference.
type IndexerEdge struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	SourceID      string         `json:"source_id"`
	Relation      EdgeRelation   `json:"relation"`
	RelationName  string         `json:"relation_name,omitempty"`
	Direction     EdgeDirection  `json:"direction"`
	TargetID      string         `json:"target_id,omitempty"`
	Unresolved    *Reference     `json:"unresolved,omitempty"`
	Confidence    EdgeConfidence `json:"confidence"`
	Evidence      EdgeEvidence   `json:"evidence"`
}

// MarkdownMember is the content-minimal inventory used to bind Markdown files
// into a projection receipt. Paths and identities do not enter the receipt.
type MarkdownMember struct {
	DocumentID string
	Path       string
	Size       int64
	SHA256     string
}

type QualificationState string

const (
	QualificationReady       QualificationState = "ready"
	QualificationPartial     QualificationState = "partial"
	QualificationUnavailable QualificationState = "unavailable"
)

type QualificationBasis string

const (
	QualificationReceipt    QualificationBasis = "receipt"
	QualificationStructural QualificationBasis = "structural"
)

type QualificationReason string

const (
	QualificationLegacyMirror      QualificationReason = "legacy_mirror"
	QualificationIncompletePull    QualificationReason = "incomplete_pull"
	QualificationUnreconciled      QualificationReason = "unreconciled"
	QualificationMissingBinding    QualificationReason = "missing_binding"
	QualificationCorruptEvidence   QualificationReason = "corrupt_evidence"
	QualificationUnsupportedSource QualificationReason = "unsupported_source"
)

// IndexerQualification records how strongly one service snapshot is bound.
// Ready requires a cryptographic source receipt; structural correlation is
// represented explicitly and can be partial only.
type IndexerQualification struct {
	Service             Service               `json:"service"`
	State               QualificationState    `json:"state"`
	Basis               QualificationBasis    `json:"basis"`
	ScopeDigest         string                `json:"scope_digest"`
	SourceReceiptDigest string                `json:"source_receipt_digest,omitempty"`
	Reasons             []QualificationReason `json:"reasons"`
}

type ProjectionReadiness string

const (
	ProjectionReady       ProjectionReadiness = "ready"
	ProjectionPartial     ProjectionReadiness = "partial"
	ProjectionUnavailable ProjectionReadiness = "unavailable"
)

type ProjectionCounts struct {
	Documents     int   `json:"documents"`
	Edges         int   `json:"edges"`
	MarkdownFiles int   `json:"markdown_files"`
	MarkdownBytes int64 `json:"markdown_bytes"`
}

// IndexerReceipt is the deterministic, content-free projection envelope. It
// deliberately excludes generator/build/host/path state and lifecycle lineage;
// those belong to the sealed Store receipt.
type IndexerReceipt struct {
	SchemaVersion    int                    `json:"schema_version"`
	ProjectionSchema int                    `json:"projection_schema"`
	Readiness        ProjectionReadiness    `json:"readiness"`
	Qualifications   []IndexerQualification `json:"qualifications"`
	Counts           ProjectionCounts       `json:"counts"`
	DocumentsDigest  string                 `json:"documents_digest"`
	EdgesDigest      string                 `json:"edges_digest"`
	MarkdownDigest   string                 `json:"markdown_digest"`
	ProjectionDigest string                 `json:"projection_digest"`
}
