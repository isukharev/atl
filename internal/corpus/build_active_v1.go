package corpus

import (
	"bytes"
	"encoding/json"
	"time"
)

const (
	// BuildActiveSchemaV1 is the legacy active-record schema. It remains
	// readable so an interrupted build can migrate without resetting guards.
	BuildActiveSchemaV1 = 1
	// BuildActiveSchemaV2 adds durable generation-wide and per-service
	// attachment-body byte usage.
	BuildActiveSchemaV2 = 2
)

type BuildAttemptStatus string

const (
	BuildAttemptActive    BuildAttemptStatus = "active"
	BuildAttemptCompleted BuildAttemptStatus = "completed"
	BuildAttemptFailed    BuildAttemptStatus = "failed"
)

// BuildServiceState is the content-free durable progress for one selected
// backend. AttachmentBodyBytes is a monotonic successful-stream high-water;
// a validated snapshot may contain fewer bytes when a later publication step
// failed. ScopeDigest appears after principal qualification; ReceiptDigest
// appears only after exact mirror reconciliation and receipt persistence.
type BuildServiceState struct {
	Service             Service      `json:"service"`
	SelectorDigest      string       `json:"selector_digest"`
	ScopeDigest         string       `json:"scope_digest,omitempty"`
	StartedAt           string       `json:"started_at,omitempty"`
	Usage               CaptureUsage `json:"usage"`
	AttachmentBodyBytes int64        `json:"attachment_body_bytes"`
	ReceiptDigest       string       `json:"receipt_digest,omitempty"`
}

// BuildActive is the canonical crash-recovery record for one retained attempt.
// The absolute deadline, cumulative physical read usage, and successful
// attachment-stream high-water prevent resume from resetting aggregate guards.
// It contains no raw selector, origin, principal, object identity, local path,
// title, or body.
type BuildActive struct {
	SchemaVersion       int                 `json:"schema_version"`
	AttemptID           string              `json:"attempt_id"`
	Status              BuildAttemptStatus  `json:"status"`
	OptionsDigest       string              `json:"options_digest"`
	Services            []BuildServiceState `json:"services"`
	StartedAt           string              `json:"started_at"`
	Deadline            string              `json:"deadline"`
	MaxAttempts         int                 `json:"max_attempts"`
	MaxResponseBytes    int64               `json:"max_response_bytes"`
	Usage               CaptureUsage        `json:"usage"`
	AttachmentBodyBytes int64               `json:"attachment_body_bytes"`
	RemoteInFlight      bool                `json:"remote_in_flight"`
	RemoteService       Service             `json:"remote_service,omitempty"`
	GenerationDigest    string              `json:"generation_digest,omitempty"`
}

// CanonicalBuildActive returns strict current-schema active-record bytes.
func CanonicalBuildActive(active BuildActive, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if active.SchemaVersion != BuildActiveSchemaV2 {
		return nil, reject(ReasonSchema)
	}
	if err := validateBuildActive(active); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(active)
	if err != nil {
		return nil, err
	}
	if len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseBuildActive accepts exact canonical current or legacy bytes and returns
// defensive service state storage. Callers must migrate schema v1 before
// writing; CanonicalBuildActive never emits the legacy shape.
func ParseBuildActive(data []byte, limits Limits) (BuildActive, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return BuildActive{}, err
	}
	if len(data) == 0 || len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return BuildActive{}, reject(ReasonBounds)
	}
	var envelope map[string]json.RawMessage
	if err := decodeStrictObject(data, &envelope); err != nil {
		return BuildActive{}, err
	}
	rawVersion, present := envelope["schema_version"]
	if !present {
		return BuildActive{}, reject(ReasonSchema)
	}
	var version int
	if err := json.Unmarshal(rawVersion, &version); err != nil {
		return BuildActive{}, reject(ReasonSchema)
	}
	var active BuildActive
	var canonical []byte
	switch version {
	case BuildActiveSchemaV1:
		var legacy buildActiveV1
		if err := decodeStrictObject(data, &legacy); err != nil {
			return BuildActive{}, err
		}
		active = buildActiveFromV1(legacy)
		if err := validateBuildActive(active); err != nil {
			return BuildActive{}, err
		}
		canonical, err = marshalCanonical(legacy)
	case BuildActiveSchemaV2:
		if err := decodeStrictObject(data, &active); err != nil {
			return BuildActive{}, err
		}
		canonical, err = CanonicalBuildActive(active, limits)
	default:
		return BuildActive{}, reject(ReasonSchema)
	}
	if err != nil {
		return BuildActive{}, err
	}
	if !bytes.Equal(data, canonical) {
		return BuildActive{}, reject(ReasonFormat)
	}
	active.Services = append([]BuildServiceState(nil), active.Services...)
	return active, nil
}

func validateBuildActive(active BuildActive) error {
	if active.SchemaVersion != BuildActiveSchemaV1 && active.SchemaVersion != BuildActiveSchemaV2 {
		return reject(ReasonSchema)
	}
	if active.SchemaVersion == BuildActiveSchemaV1 && active.AttachmentBodyBytes != 0 {
		return reject(ReasonSchema)
	}
	if err := validGenerationID(active.AttemptID); err != nil {
		return err
	}
	if active.Status != BuildAttemptActive && active.Status != BuildAttemptCompleted && active.Status != BuildAttemptFailed {
		return reject(ReasonType)
	}
	if !isLowerSHA256(active.OptionsDigest) {
		return reject(ReasonDigest)
	}
	started, err := parseCanonicalCaptureTime(active.StartedAt)
	if err != nil {
		return err
	}
	deadline, err := parseCanonicalCaptureTime(active.Deadline)
	if err != nil || !deadline.After(started) {
		return reject(ReasonLineage)
	}
	if active.MaxAttempts <= 0 || active.MaxAttempts > maxCaptureAttempts ||
		active.MaxResponseBytes <= 0 || active.MaxResponseBytes > maxCaptureResponseBytes ||
		active.Usage.Attempts < 0 || active.Usage.Attempts > active.MaxAttempts ||
		active.Usage.ResponseBytes < 0 || active.Usage.ResponseBytes > active.MaxResponseBytes ||
		active.AttachmentBodyBytes < 0 || active.AttachmentBodyBytes > maxCaptureResponseBytes {
		return reject(ReasonBounds)
	}
	if len(active.Services) == 0 || len(active.Services) > 2 {
		return reject(ReasonMembership)
	}
	var serviceAttempts int
	var serviceBytes int64
	var serviceAttachmentBytes int64
	for index, state := range active.Services {
		if !validQualificationService(state.Service) {
			return reject(ReasonType)
		}
		if !isLowerSHA256(state.SelectorDigest) {
			return reject(ReasonDigest)
		}
		if index > 0 && active.Services[index-1].Service >= state.Service {
			return reject(ReasonMembership)
		}
		if state.ScopeDigest != "" && !isLowerSHA256(state.ScopeDigest) ||
			state.ReceiptDigest != "" && !isLowerSHA256(state.ReceiptDigest) ||
			state.ReceiptDigest != "" && (state.ScopeDigest == "" || state.StartedAt == "") {
			return reject(ReasonDigest)
		}
		if state.StartedAt != "" {
			captureStarted, err := parseCanonicalCaptureTime(state.StartedAt)
			if err != nil || captureStarted.Before(started) || !captureStarted.Before(deadline) {
				return reject(ReasonLineage)
			}
		} else if state.ScopeDigest != "" || state.Usage.Attempts != 0 || state.Usage.ResponseBytes != 0 ||
			state.AttachmentBodyBytes != 0 || state.ReceiptDigest != "" {
			return reject(ReasonLineage)
		}
		if state.Usage.Attempts < 0 || state.Usage.ResponseBytes < 0 ||
			state.Usage.Attempts > active.MaxAttempts-serviceAttempts ||
			state.Usage.ResponseBytes > active.MaxResponseBytes-serviceBytes ||
			state.AttachmentBodyBytes < 0 || state.AttachmentBodyBytes > active.AttachmentBodyBytes-serviceAttachmentBytes {
			return reject(ReasonBounds)
		}
		if active.SchemaVersion == BuildActiveSchemaV1 && state.AttachmentBodyBytes != 0 {
			return reject(ReasonSchema)
		}
		serviceAttempts += state.Usage.Attempts
		serviceBytes += state.Usage.ResponseBytes
		serviceAttachmentBytes += state.AttachmentBodyBytes
		if active.Status == BuildAttemptCompleted && (state.ScopeDigest == "" || state.ReceiptDigest == "") {
			return reject(ReasonMembership)
		}
	}
	if serviceAttempts > active.Usage.Attempts || serviceBytes > active.Usage.ResponseBytes {
		return reject(ReasonLineage)
	}
	if active.RemoteInFlight {
		if active.Status != BuildAttemptActive || !buildActiveHasService(active, active.RemoteService) {
			return reject(ReasonMembership)
		}
		for _, state := range active.Services {
			if state.Service == active.RemoteService && state.StartedAt == "" {
				return reject(ReasonLineage)
			}
		}
	} else if active.RemoteService != "" {
		return reject(ReasonMembership)
	}
	if active.Status == BuildAttemptCompleted {
		if !isLowerSHA256(active.GenerationDigest) {
			return reject(ReasonDigest)
		}
	} else if active.GenerationDigest != "" {
		return reject(ReasonMembership)
	}
	return nil
}

// buildActiveV1 is the frozen legacy wire shape. Keeping it distinct prevents
// the v2 fields from becoming optional and silently resetting on old bytes.
type buildActiveV1 struct {
	SchemaVersion    int                   `json:"schema_version"`
	AttemptID        string                `json:"attempt_id"`
	Status           BuildAttemptStatus    `json:"status"`
	OptionsDigest    string                `json:"options_digest"`
	Services         []buildServiceStateV1 `json:"services"`
	StartedAt        string                `json:"started_at"`
	Deadline         string                `json:"deadline"`
	MaxAttempts      int                   `json:"max_attempts"`
	MaxResponseBytes int64                 `json:"max_response_bytes"`
	Usage            CaptureUsage          `json:"usage"`
	RemoteInFlight   bool                  `json:"remote_in_flight"`
	RemoteService    Service               `json:"remote_service,omitempty"`
	GenerationDigest string                `json:"generation_digest,omitempty"`
}

type buildServiceStateV1 struct {
	Service        Service      `json:"service"`
	SelectorDigest string       `json:"selector_digest"`
	ScopeDigest    string       `json:"scope_digest,omitempty"`
	StartedAt      string       `json:"started_at,omitempty"`
	Usage          CaptureUsage `json:"usage"`
	ReceiptDigest  string       `json:"receipt_digest,omitempty"`
}

func buildActiveFromV1(legacy buildActiveV1) BuildActive {
	services := make([]BuildServiceState, len(legacy.Services))
	for index, state := range legacy.Services {
		services[index] = BuildServiceState{
			Service: state.Service, SelectorDigest: state.SelectorDigest, ScopeDigest: state.ScopeDigest,
			StartedAt: state.StartedAt, Usage: state.Usage, ReceiptDigest: state.ReceiptDigest,
		}
	}
	return BuildActive{
		SchemaVersion: legacy.SchemaVersion, AttemptID: legacy.AttemptID, Status: legacy.Status,
		OptionsDigest: legacy.OptionsDigest, Services: services, StartedAt: legacy.StartedAt,
		Deadline: legacy.Deadline, MaxAttempts: legacy.MaxAttempts, MaxResponseBytes: legacy.MaxResponseBytes,
		Usage: legacy.Usage, RemoteInFlight: legacy.RemoteInFlight, RemoteService: legacy.RemoteService,
		GenerationDigest: legacy.GenerationDigest,
	}
}

func buildActiveV1Projection(active BuildActive) buildActiveV1 {
	services := make([]buildServiceStateV1, len(active.Services))
	for index, state := range active.Services {
		services[index] = buildServiceStateV1{
			Service: state.Service, SelectorDigest: state.SelectorDigest, ScopeDigest: state.ScopeDigest,
			StartedAt: state.StartedAt, Usage: state.Usage, ReceiptDigest: state.ReceiptDigest,
		}
	}
	return buildActiveV1{
		SchemaVersion: BuildActiveSchemaV1, AttemptID: active.AttemptID, Status: active.Status,
		OptionsDigest: active.OptionsDigest, Services: services, StartedAt: active.StartedAt,
		Deadline: active.Deadline, MaxAttempts: active.MaxAttempts, MaxResponseBytes: active.MaxResponseBytes,
		Usage: active.Usage, RemoteInFlight: active.RemoteInFlight, RemoteService: active.RemoteService,
		GenerationDigest: active.GenerationDigest,
	}
}

func buildActiveHasService(active BuildActive, service Service) bool {
	if !validQualificationService(service) {
		return false
	}
	for _, state := range active.Services {
		if state.Service == service {
			return true
		}
	}
	return false
}

// NewBuildActiveTime converts a caller-owned time to the exact durable form.
func NewBuildActiveTime(value time.Time) string { return canonicalCaptureTime(value) }
