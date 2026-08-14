// Package atif owns a bounded, owner-private projection into the pinned ATIF
// v1.7 text-and-tool-call subset. It has no provider, network, credential,
// process, public-report, or upload authority.
package atif

import (
	"encoding/json"
	"errors"
)

const (
	ATIFSchemaVersion = "ATIF-v1.7"
	BindingSchema     = "agent-eval/atif-owner-private"
	BindingVersion    = 1

	MaxDocumentBytes = 1 << 20
	MaxSteps         = 1024
	MaxToolCalls     = 4096
	MaxResults       = 4096
	MaxTextBytes     = 64 << 10
	MaxIdentifier    = 256
	MaxArgumentBytes = 64 << 10
	MaxJSONDepth     = 64
)

var ErrInvalid = errors.New("atif_projection_invalid")

// ErrorCode is a closed, content-free failure classification.
type ErrorCode string

const (
	ErrorInvalidEventSet    ErrorCode = "invalid_atif_event_set"
	ErrorInvalidEvent       ErrorCode = "invalid_atif_event"
	ErrorInvalidToolCall    ErrorCode = "invalid_atif_tool_call"
	ErrorInvalidObservation ErrorCode = "invalid_atif_observation"
	ErrorInvalidProjection  ErrorCode = "invalid_atif_projection"
	ErrorInvalidBinding     ErrorCode = "invalid_atif_binding"
	ErrorInvalidWire        ErrorCode = "invalid_atif_wire"
	ErrorInvalidDestination ErrorCode = "invalid_atif_destination"
	ErrorExportFailed       ErrorCode = "atif_export_failed"
	ErrorLimitExceeded      ErrorCode = "atif_limit_exceeded"
)

// Error is deliberately independent of source paths, provider values, and
// parser messages so owner-private failures cannot become public diagnostics.
type Error struct{ code ErrorCode }

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return ErrInvalid }
func (e *Error) Code() ErrorCode { return e.code }

func fail(code ErrorCode) error { return &Error{code: code} }

// CodeOf extracts a stable ATIF failure classification.
func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

// Privacy is intentionally closed; this leaf has no public/default mode.
type Privacy string

const PrivacyOwnerPrivate Privacy = "owner_private"

// Role is the ATIF v1.7 source vocabulary used by this subset.
type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
	RoleAgent  Role = "agent"
)

// State is the fixed evaluator lifecycle state carried in the closed step
// extra object. It is not inferred from message text or tool results.
type State string

const (
	StateStarted   State = "started"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
	StateSkipped   State = "skipped"
)

// Producer identifies the normalized event producer. It is duplicated in the
// ATIF agent object and in the owner-private binding so both identities remain
// bound without relying on an external registry.
type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolCall is the closed ATIF v1.7 tool-call subset. Arguments must be a
// canonical JSON object; arbitrary ATIF extra metadata is not admitted.
type ToolCall struct {
	ToolCallID   string          `json:"tool_call_id"`
	FunctionName string          `json:"function_name"`
	Arguments    json.RawMessage `json:"arguments"`
}

// ObservationResult is text-only in this subset. Images, paths, URLs,
// subagent references, and arbitrary extra metadata are intentionally absent.
type ObservationResult struct {
	SourceCallID string `json:"source_call_id,omitempty"`
	Content      string `json:"content"`
}

// Event is the normalized source event accepted by Project. Every field is
// projected into the corresponding ATIF step or its fixed state binding.
type Event struct {
	StepID    uint32              `json:"step_id"`
	Timestamp string              `json:"timestamp,omitempty"`
	Role      Role                `json:"role"`
	State     State               `json:"state"`
	Message   string              `json:"message"`
	ToolCalls []ToolCall          `json:"tool_calls,omitempty"`
	Results   []ObservationResult `json:"results,omitempty"`
}

// EventSet is a complete normalized source snapshot. DeclaredEvents must
// equal len(Events); partial or inferred event sets are rejected.
type EventSet struct {
	Producer       Producer `json:"producer"`
	ModelName      string   `json:"model_name,omitempty"`
	DeclaredEvents uint32   `json:"declared_events"`
	Events         []Event  `json:"events"`
	SourceSHA256   string   `json:"source_sha256,omitempty"`
}

// Coverage binds the declared normalized event count to the emitted ATIF
// steps. Complete is fixed true for every accepted projection.
type Coverage struct {
	DeclaredEvents uint32 `json:"declared_events"`
	ProjectedSteps uint32 `json:"projected_steps"`
	Complete       bool   `json:"complete"`
}

// Binding is a closed owner-private ATIF extension carried in the ATIF root
// extra object. It has no arbitrary extension map.
type Binding struct {
	Schema           string   `json:"schema"`
	Version          int      `json:"version"`
	SourceSHA256     string   `json:"source_sha256"`
	ProjectionSHA256 string   `json:"projection_sha256"`
	Producer         Producer `json:"producer"`
	Privacy          Privacy  `json:"privacy"`
	Coverage         Coverage `json:"coverage"`
}

type StepExtra struct {
	State State `json:"state"`
}

// Step is the pinned, closed ATIF v1.7 step subset. Timestamp, multimodal
// content, reasoning, metrics, continuation metadata, and arbitrary extras
// are deliberately not part of this package's wire contract.
type Step struct {
	StepID      uint32       `json:"step_id"`
	Timestamp   string       `json:"timestamp,omitempty"`
	Source      Role         `json:"source"`
	Message     string       `json:"message"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	Observation *Observation `json:"observation,omitempty"`
	Extra       StepExtra    `json:"extra"`
}

type Observation struct {
	Results []ObservationResult `json:"results"`
}

// Document is the ATIF v1.7 JSON document emitted by Encode.
type Document struct {
	SchemaVersion string  `json:"schema_version"`
	Agent         Agent   `json:"agent"`
	Steps         []Step  `json:"steps"`
	Extra         Binding `json:"extra"`
}

type Agent struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ModelName string `json:"model_name,omitempty"`
}

// Projection keeps the validated ATIF document and its externally useful
// content addresses. Only Document is serialized; digest fields are repeated
// from Document.Extra and are checked for drift by Validate.
type Projection struct {
	Document         Document
	SourceSHA256     string
	ProjectionSHA256 string
}
