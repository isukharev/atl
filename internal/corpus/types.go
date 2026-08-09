package corpus

import "errors"

const (
	ManifestSchemaV1 = 1
	ReceiptSchemaV1  = 1
	PointerSchemaV1  = 1
)

// Service is a backend category recorded in a sealed generation.
type Service string

const (
	ServiceJira       Service = "jira"
	ServiceConfluence Service = "confluence"
)

// Role describes how a member participates in a generation.
type Role string

const (
	RoleNative    Role = "native"
	RoleMetadata  Role = "metadata"
	RoleDocument  Role = "document"
	RoleEdges     Role = "edges"
	RoleAsset     Role = "asset"
	RoleTombstone Role = "tombstone"
)

// BuildState records whether the generator executable matched its declared
// source state without exposing build-host details.
type BuildState string

const (
	BuildStateClean    BuildState = "clean"
	BuildStateModified BuildState = "modified"
	BuildStateUnknown  BuildState = "unknown"
)

// Limits bounds all attacker- or caller-controlled generation metadata.
// Zero-valued fields are replaced by DefaultLimits.
type Limits struct {
	MaxMembers       int
	MaxMemberBytes   int64
	MaxTotalBytes    int64
	MaxManifestBytes int64
	MaxPathBytes     int
	MaxPathDepth     int
}

// DefaultLimits returns the production safety bounds for a generation.
func DefaultLimits() Limits {
	return Limits{
		MaxMembers:       100_000,
		MaxMemberBytes:   1 << 30,
		MaxTotalBytes:    64 << 30,
		MaxManifestBytes: 64 << 20,
		MaxPathBytes:     4096,
		MaxPathDepth:     64,
	}
}

// MemberSpec identifies a member before its bytes and file metadata have been
// measured by the store.
type MemberSpec struct {
	Service  Service
	StableID string
	Role     Role
	Path     string
}

// Member is one exact regular-file member of a sealed generation.
type Member struct {
	Service  Service `json:"service"`
	StableID string  `json:"stable_id"`
	Role     Role    `json:"role"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Mode     uint32  `json:"mode"`
	SHA256   string  `json:"sha256"`
}

// Qualification binds one service category to its content-free source and
// projection receipts.
type Qualification struct {
	Service          Service `json:"service"`
	ReceiptSchema    int     `json:"receipt_schema"`
	ScopeDigest      string  `json:"scope_digest"`
	SelectorDigest   string  `json:"selector_digest"`
	ProjectionDigest string  `json:"projection_digest"`
	ReceiptDigest    string  `json:"receipt_digest"`
}

// Totals is the bounded aggregate size of a generation.
type Totals struct {
	Members int   `json:"members"`
	Bytes   int64 `json:"bytes"`
}

// Manifest is the complete, exact inventory of a generation.
type Manifest struct {
	SchemaVersion     int             `json:"schema_version"`
	ProjectionSchema  int             `json:"projection_schema"`
	GeneratorVersion  string          `json:"generator_version"`
	BuildState        BuildState      `json:"build_state"`
	PredecessorDigest string          `json:"predecessor_digest,omitempty"`
	Qualifications    []Qualification `json:"qualifications"`
	TombstoneDigest   string          `json:"tombstone_digest,omitempty"`
	Members           []Member        `json:"members"`
	Totals            Totals          `json:"totals"`
}

// Receipt is the content-free sealed-generation envelope. It deliberately
// excludes member paths and stable identities.
type Receipt struct {
	SchemaVersion     int             `json:"schema_version"`
	ManifestSchema    int             `json:"manifest_schema"`
	ProjectionSchema  int             `json:"projection_schema"`
	GeneratorVersion  string          `json:"generator_version"`
	BuildState        BuildState      `json:"build_state"`
	PredecessorDigest string          `json:"predecessor_digest,omitempty"`
	Qualifications    []Qualification `json:"qualifications"`
	TombstoneDigest   string          `json:"tombstone_digest,omitempty"`
	Totals            Totals          `json:"totals"`
	ManifestDigest    string          `json:"manifest_digest"`
	InventoryDigest   string          `json:"inventory_digest"`
	GenerationDigest  string          `json:"generation_digest"`
}

// Pointer is the atomic, content-free consumer selection record.
type Pointer struct {
	SchemaVersion    int    `json:"schema_version"`
	GenerationID     string `json:"generation_id"`
	GenerationDigest string `json:"generation_digest"`
}

// Summary contains only aggregate and categorical generation information.
type Summary struct {
	GenerationDigest string     `json:"generation_digest"`
	ManifestSchema   int        `json:"manifest_schema"`
	ReceiptSchema    int        `json:"receipt_schema"`
	ProjectionSchema int        `json:"projection_schema"`
	GeneratorVersion string     `json:"generator_version"`
	BuildState       BuildState `json:"build_state"`
	Services         []Service  `json:"services"`
	Totals           Totals     `json:"totals"`
}

const integrityErrorText = "corpus integrity check failed"

// ErrIntegrity is the stable sentinel for rejected generation state.
var ErrIntegrity error = errors.New(integrityErrorText)

// Reason is a closed, content-free integrity failure classification.
type Reason string

const (
	ReasonFormat     Reason = "format"
	ReasonSchema     Reason = "schema"
	ReasonBounds     Reason = "bounds"
	ReasonPath       Reason = "path"
	ReasonType       Reason = "type"
	ReasonMode       Reason = "mode"
	ReasonMembership Reason = "membership"
	ReasonDigest     Reason = "digest"
	ReasonLineage    Reason = "lineage"
	ReasonConcurrent Reason = "concurrent"
	ReasonIO         Reason = "io"
)

type integrityError struct {
	reason Reason
}

func (e integrityError) Error() string {
	return integrityErrorText + ": " + string(e.reason)
}

func (e integrityError) Unwrap() error { return ErrIntegrity }

func (e integrityError) Reason() Reason { return e.reason }

func reject(reason Reason) error {
	switch reason {
	case ReasonFormat, ReasonSchema, ReasonBounds, ReasonPath, ReasonType,
		ReasonMode, ReasonMembership, ReasonDigest, ReasonLineage,
		ReasonConcurrent, ReasonIO:
		return integrityError{reason: reason}
	default:
		return integrityError{reason: ReasonFormat}
	}
}
