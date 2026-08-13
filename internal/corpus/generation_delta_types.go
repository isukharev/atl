package corpus

const (
	GenerationDeltaSchemaV1        = 1
	GenerationDiffArtifactSchemaV1 = 1
)

// GenerationDeltaState classifies one stable document identity across two
// compatible, qualified generations. It describes index membership only; it
// never claims that a backend object was physically deleted or restored.
type GenerationDeltaState string

const (
	GenerationDeltaAdded      GenerationDeltaState = "added"
	GenerationDeltaRetained   GenerationDeltaState = "retained"
	GenerationDeltaChanged    GenerationDeltaState = "changed"
	GenerationDeltaTombstoned GenerationDeltaState = "tombstoned"
)

// GenerationDeltaReason is closed so a consumer cannot mistake an index
// retirement for stronger backend-deletion evidence.
type GenerationDeltaReason string

const GenerationDeltaAbsentQualified GenerationDeltaReason = "absent_from_qualified_generation"

// GenerationDeltaBinding is the content-free compatibility boundary for one
// service. Scope binds backend origin and principal; selector and options bind
// the exact membership request and evidence policy.
type GenerationDeltaBinding struct {
	Service        Service `json:"service"`
	ReceiptSchema  int     `json:"receipt_schema"`
	ScopeDigest    string  `json:"scope_digest"`
	SelectorDigest string  `json:"selector_digest"`
	OptionsDigest  string  `json:"options_digest"`
}

// GenerationDeltaRecord is private identity-bearing evidence. Document
// digests hash canonical document records, so mutable key/title/path changes
// retain one stable identity and are classified as changed rather than removed.
type GenerationDeltaRecord struct {
	ID                string                `json:"id"`
	Service           Service               `json:"service"`
	Kind              ObjectKind            `json:"kind"`
	State             GenerationDeltaState  `json:"state"`
	PredecessorDigest string                `json:"predecessor_digest,omitempty"`
	SuccessorDigest   string                `json:"successor_digest,omitempty"`
	Reason            GenerationDeltaReason `json:"reason,omitempty"`
}

type GenerationDeltaCounts struct {
	Added      int `json:"added"`
	Retained   int `json:"retained"`
	Changed    int `json:"changed"`
	Tombstoned int `json:"tombstoned"`
}

// GenerationDelta is one canonical private member sealed into the successor.
// The final successor generation digest cannot appear here because these bytes
// participate in that digest. The public diff result and optional private
// artifact add the final digest after re-verifying both sealed generations.
type GenerationDelta struct {
	SchemaVersion               int                      `json:"schema_version"`
	PredecessorGenerationID     string                   `json:"predecessor_generation_id"`
	PredecessorGenerationDigest string                   `json:"predecessor_generation_digest"`
	PredecessorProjectionDigest string                   `json:"predecessor_projection_digest"`
	SuccessorProjectionDigest   string                   `json:"successor_projection_digest"`
	Bindings                    []GenerationDeltaBinding `json:"bindings"`
	Records                     []GenerationDeltaRecord  `json:"records"`
	Counts                      GenerationDeltaCounts    `json:"counts"`
}

// GenerationDiffArtifact is the explicit owner-private identity artifact. It
// adds the final successor seal while retaining the exact sealed delta digest.
type GenerationDiffArtifact struct {
	SchemaVersion               int                      `json:"schema_version"`
	PredecessorGenerationDigest string                   `json:"predecessor_generation_digest"`
	SuccessorGenerationDigest   string                   `json:"successor_generation_digest"`
	TombstoneDigest             string                   `json:"tombstone_digest"`
	Reason                      GenerationDeltaReason    `json:"reason"`
	Bindings                    []GenerationDeltaBinding `json:"bindings"`
	Records                     []GenerationDeltaRecord  `json:"records"`
	Counts                      GenerationDeltaCounts    `json:"counts"`
}
