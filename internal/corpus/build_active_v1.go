package corpus

import (
	"bytes"
	"time"
)

const BuildActiveSchemaV1 = 1

type BuildAttemptStatus string

const (
	BuildAttemptActive    BuildAttemptStatus = "active"
	BuildAttemptCompleted BuildAttemptStatus = "completed"
	BuildAttemptFailed    BuildAttemptStatus = "failed"
)

// BuildServiceState is the content-free durable progress for one selected
// backend. ScopeDigest appears after principal qualification; ReceiptDigest
// appears only after exact mirror reconciliation and receipt persistence.
type BuildServiceState struct {
	Service        Service      `json:"service"`
	SelectorDigest string       `json:"selector_digest"`
	ScopeDigest    string       `json:"scope_digest,omitempty"`
	StartedAt      string       `json:"started_at,omitempty"`
	Usage          CaptureUsage `json:"usage"`
	ReceiptDigest  string       `json:"receipt_digest,omitempty"`
}

// BuildActive is the canonical crash-recovery record for one retained attempt.
// The absolute deadline and cumulative physical usage prevent resume from
// resetting aggregate guards. It contains no raw selector, origin, principal,
// object identity, local path, title, or body.
type BuildActive struct {
	SchemaVersion    int                 `json:"schema_version"`
	AttemptID        string              `json:"attempt_id"`
	Status           BuildAttemptStatus  `json:"status"`
	OptionsDigest    string              `json:"options_digest"`
	Services         []BuildServiceState `json:"services"`
	StartedAt        string              `json:"started_at"`
	Deadline         string              `json:"deadline"`
	MaxAttempts      int                 `json:"max_attempts"`
	MaxResponseBytes int64               `json:"max_response_bytes"`
	Usage            CaptureUsage        `json:"usage"`
	RemoteInFlight   bool                `json:"remote_in_flight"`
	RemoteService    Service             `json:"remote_service,omitempty"`
	GenerationDigest string              `json:"generation_digest,omitempty"`
}

// CanonicalBuildActive returns strict schema-v1 active-record bytes.
func CanonicalBuildActive(active BuildActive, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateBuildActive(active, limits); err != nil {
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

// ParseBuildActive accepts only exact canonical bytes and returns defensive
// service state storage.
func ParseBuildActive(data []byte, limits Limits) (BuildActive, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return BuildActive{}, err
	}
	if len(data) == 0 || len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return BuildActive{}, reject(ReasonBounds)
	}
	var active BuildActive
	if err := decodeStrictObject(data, &active); err != nil {
		return BuildActive{}, err
	}
	canonical, err := CanonicalBuildActive(active, limits)
	if err != nil {
		return BuildActive{}, err
	}
	if !bytes.Equal(data, canonical) {
		return BuildActive{}, reject(ReasonFormat)
	}
	active.Services = append([]BuildServiceState(nil), active.Services...)
	return active, nil
}

func validateBuildActive(active BuildActive, limits Limits) error {
	if active.SchemaVersion != BuildActiveSchemaV1 {
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
		active.Usage.ResponseBytes < 0 || active.Usage.ResponseBytes > active.MaxResponseBytes {
		return reject(ReasonBounds)
	}
	if active.Services == nil || len(active.Services) == 0 || len(active.Services) > 2 {
		return reject(ReasonMembership)
	}
	var serviceAttempts int
	var serviceBytes int64
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
		} else if state.ScopeDigest != "" || state.Usage.Attempts != 0 || state.Usage.ResponseBytes != 0 || state.ReceiptDigest != "" {
			return reject(ReasonLineage)
		}
		if state.Usage.Attempts < 0 || state.Usage.ResponseBytes < 0 ||
			state.Usage.Attempts > active.MaxAttempts-serviceAttempts ||
			state.Usage.ResponseBytes > active.MaxResponseBytes-serviceBytes {
			return reject(ReasonBounds)
		}
		serviceAttempts += state.Usage.Attempts
		serviceBytes += state.Usage.ResponseBytes
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
