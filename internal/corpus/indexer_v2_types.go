package corpus

const (
	IndexerSchemaV2        = 2
	IndexerReceiptSchemaV2 = 2
)

// ArtifactBodyStatus is the closed outcome of an explicitly bounded native
// attachment-body policy. Inventory metadata remains available independently
// of whether bytes were requested or accepted.
type ArtifactBodyStatus string

const (
	ArtifactBodyCaptured     ArtifactBodyStatus = "captured"
	ArtifactBodyExcluded     ArtifactBodyStatus = "excluded"
	ArtifactBodyForbidden    ArtifactBodyStatus = "forbidden"
	ArtifactBodyFailed       ArtifactBodyStatus = "failed"
	ArtifactBodyNotRequested ArtifactBodyStatus = "not_requested"
)

// ArtifactBodyReason is deliberately categorical and content-free.
type ArtifactBodyReason string

const (
	ArtifactReasonMediaTypeExcluded ArtifactBodyReason = "media_type_excluded"
	ArtifactReasonCountLimit        ArtifactBodyReason = "count_limit"
	ArtifactReasonItemLimit         ArtifactBodyReason = "item_limit"
	ArtifactReasonAggregateLimit    ArtifactBodyReason = "aggregate_limit"
	ArtifactReasonForbidden         ArtifactBodyReason = "forbidden"
	ArtifactReasonFailed            ArtifactBodyReason = "failed"
	ArtifactReasonSizeMismatch      ArtifactBodyReason = "size_mismatch"
)

// ArtifactSourceLineage binds an artifact record to the exact qualified
// attachment inventory and parent bytes from which it was projected.
type ArtifactSourceLineage struct {
	InventoryPath        string `json:"inventory_path"`
	InventorySHA256      string `json:"inventory_sha256"`
	ParentNativeSHA256   string `json:"parent_native_sha256"`
	ParentMetadataSHA256 string `json:"parent_metadata_sha256"`
}

// IndexerArtifact joins one stable attachment document to its stable parent.
// Captured binary bytes stay in a distinct contained store member; JSONL and
// Markdown never inline them.
type IndexerArtifact struct {
	SchemaVersion int                   `json:"schema_version"`
	DocumentID    string                `json:"document_id"`
	Service       Service               `json:"service"`
	ParentID      string                `json:"parent_id"`
	MediaType     string                `json:"media_type,omitempty"`
	DeclaredSize  int64                 `json:"declared_size"`
	Status        ArtifactBodyStatus    `json:"status"`
	Reason        ArtifactBodyReason    `json:"reason,omitempty"`
	Path          string                `json:"path,omitempty"`
	Size          int64                 `json:"size"`
	SHA256        string                `json:"sha256,omitempty"`
	Source        ArtifactSourceLineage `json:"source"`
}

// ArtifactMember is the content-minimal inventory used to prove that every
// captured record has exactly one matching sealed asset member.
type ArtifactMember struct {
	DocumentID string
	Path       string
	Size       int64
	SHA256     string
}

type ProjectionCountsV2 struct {
	Documents     int   `json:"documents"`
	Edges         int   `json:"edges"`
	MarkdownFiles int   `json:"markdown_files"`
	MarkdownBytes int64 `json:"markdown_bytes"`
	Artifacts     int   `json:"artifacts"`
	ArtifactBytes int64 `json:"artifact_bytes"`
}

// IndexerReceiptV2 is additive to the byte-stable v1 document, edge, Markdown,
// and receipt contracts. It binds their exact digests plus the new artifact
// reference inventory.
type IndexerReceiptV2 struct {
	SchemaVersion    int                    `json:"schema_version"`
	ProjectionSchema int                    `json:"projection_schema"`
	Readiness        ProjectionReadiness    `json:"readiness"`
	Qualifications   []IndexerQualification `json:"qualifications"`
	Counts           ProjectionCountsV2     `json:"counts"`
	DocumentsDigest  string                 `json:"documents_digest"`
	EdgesDigest      string                 `json:"edges_digest"`
	MarkdownDigest   string                 `json:"markdown_digest"`
	ArtifactsDigest  string                 `json:"artifacts_digest"`
	ProjectionDigest string                 `json:"projection_digest"`
}
