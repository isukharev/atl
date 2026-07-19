package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxEvidenceAttemptCount = 1_000_000

// EvidenceAttemptState is the closed, transport-neutral outcome of attempts to
// obtain task evidence. It intentionally describes only aggregate control-flow
// outcomes: it cannot retain commands, arguments, paths, URLs, errors, or
// backend values.
type EvidenceAttemptState string

const (
	EvidenceAttemptStateNone        EvidenceAttemptState = "none"
	EvidenceAttemptStateUnavailable EvidenceAttemptState = "unavailable"
	EvidenceAttemptStateBlocked     EvidenceAttemptState = "blocked"
	EvidenceAttemptStateFailed      EvidenceAttemptState = "failed"
	EvidenceAttemptStatePartial     EvidenceAttemptState = "partial"
	EvidenceAttemptStateSucceeded   EvidenceAttemptState = "succeeded"
)

// EvidenceAttemptCounts records aggregate evidence intents and outcomes.
//
// Attempts is the total number of client intents. Every intent is classified
// exactly once as admitted, denied by a guard, or unavailable because no
// usable interface was exposed. Every admitted attempt is classified exactly
// once as succeeded or failed.
type EvidenceAttemptCounts struct {
	Attempts           int
	Admitted           int
	Succeeded          int
	Failed             int
	Denied             int
	UnavailableIntents int
}

// EvidenceAttemptTelemetry is a compact, privacy-safe measurement. When
// Coverage is false, State and every counter must be absent/zero so missing
// instrumentation cannot be mistaken for observed inactivity.
type EvidenceAttemptTelemetry struct {
	Coverage           bool                 `json:"coverage"`
	State              EvidenceAttemptState `json:"state,omitempty"`
	Attempts           int                  `json:"attempts,omitempty"`
	Admitted           int                  `json:"admitted,omitempty"`
	Succeeded          int                  `json:"succeeded,omitempty"`
	Failed             int                  `json:"failed,omitempty"`
	Denied             int                  `json:"denied,omitempty"`
	UnavailableIntents int                  `json:"unavailable_intents,omitempty"`
}

// EvidenceOutcomeReport is the model's bounded, content-free account of why it
// did or did not obtain task evidence. It is deliberately separate from
// EvidenceAttemptTelemetry: the report can explain a zero-call answer, but it
// can never create or override audit-derived attempts.
type EvidenceOutcomeReport struct {
	Coverage bool                 `json:"coverage"`
	State    EvidenceAttemptState `json:"state,omitempty"`
}

func (r EvidenceOutcomeReport) Validate() error {
	if !r.Coverage {
		if r.State != "" {
			return fmt.Errorf("uncovered evidence outcome must not claim state")
		}
		return nil
	}
	if !validEvidenceAttemptState(r.State) {
		return fmt.Errorf("invalid evidence outcome state")
	}
	return nil
}

// ConsistentWithAudit rejects self-reports that contradict observed control
// flow. The sole asymmetric case is an audit-observed zero-call result: the
// model may report either an intentional non-attempt or an unavailable route.
// That unavailable claim remains self-report only; the audit state stays none.
func (r EvidenceOutcomeReport) ConsistentWithAudit(audit EvidenceAttemptTelemetry) bool {
	if r.Validate() != nil || !r.Coverage || audit.Validate() != nil || !audit.Coverage {
		return false
	}
	if audit.State == EvidenceAttemptStateNone {
		return r.State == EvidenceAttemptStateNone || r.State == EvidenceAttemptStateUnavailable
	}
	return r.State == audit.State
}

// ParseEvidenceOutcomeReport reads the common model response member:
// {"evidence_outcome":{"state":"..."}}. Absence is represented as
// uncovered rather than guessed. Unknown envelope members are rejected so the
// response remains bounded and mechanically comparable across transports.
func ParseEvidenceOutcomeReport(data []byte) (EvidenceOutcomeReport, error) {
	var outer struct {
		EvidenceOutcome json.RawMessage `json:"evidence_outcome"`
	}
	if err := json.Unmarshal(data, &outer); err != nil {
		return EvidenceOutcomeReport{}, fmt.Errorf("decode evidence outcome: %w", err)
	}
	if len(outer.EvidenceOutcome) == 0 || bytes.Equal(bytes.TrimSpace(outer.EvidenceOutcome), []byte("null")) {
		return EvidenceOutcomeReport{}, nil
	}
	var envelope struct {
		State EvidenceAttemptState `json:"state"`
	}
	decoder := json.NewDecoder(bytes.NewReader(outer.EvidenceOutcome))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return EvidenceOutcomeReport{}, fmt.Errorf("decode bounded evidence outcome")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return EvidenceOutcomeReport{}, fmt.Errorf("decode bounded evidence outcome")
	}
	report := EvidenceOutcomeReport{Coverage: true, State: envelope.State}
	if err := report.Validate(); err != nil {
		return EvidenceOutcomeReport{}, err
	}
	return report, nil
}

// validateEvidenceOutcomeResponseSchema proves that an activation-study
// response schema requires the same minimal closed envelope on every transport.
func validateEvidenceOutcomeResponseSchema(data []byte) error {
	var root struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &root); err != nil || !containsString(root.Required, "evidence_outcome") {
		return fmt.Errorf("response schema must require evidence_outcome")
	}
	raw, ok := root.Properties["evidence_outcome"]
	if !ok {
		return fmt.Errorf("response schema must define evidence_outcome")
	}
	var envelope struct {
		Type                 string                     `json:"type"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "object" || envelope.AdditionalProperties == nil || *envelope.AdditionalProperties ||
		len(envelope.Required) != 1 || envelope.Required[0] != "state" || len(envelope.Properties) != 1 {
		return fmt.Errorf("evidence_outcome schema must be a closed object requiring state")
	}
	var state struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}
	if json.Unmarshal(envelope.Properties["state"], &state) != nil || state.Type != "string" || len(state.Enum) != 6 {
		return fmt.Errorf("evidence_outcome state schema is invalid")
	}
	seen := make(map[EvidenceAttemptState]bool, len(state.Enum))
	for _, value := range state.Enum {
		candidate := EvidenceAttemptState(value)
		if !validEvidenceAttemptState(candidate) || seen[candidate] {
			return fmt.Errorf("evidence_outcome state schema is invalid")
		}
		seen[candidate] = true
	}
	return nil
}

// NewEvidenceAttemptTelemetry constructs validated telemetry from generic
// aggregate counts. Uncovered telemetry accepts only zero counts; it never
// silently discards observations supplied by a caller.
func NewEvidenceAttemptTelemetry(coverage bool, counts EvidenceAttemptCounts) (EvidenceAttemptTelemetry, error) {
	if !coverage {
		if counts != (EvidenceAttemptCounts{}) {
			return EvidenceAttemptTelemetry{}, fmt.Errorf("uncovered evidence attempts must not contain counters")
		}
		return EvidenceAttemptTelemetry{}, nil
	}

	state, err := DeriveEvidenceAttemptState(counts)
	if err != nil {
		return EvidenceAttemptTelemetry{}, err
	}
	telemetry := EvidenceAttemptTelemetry{
		Coverage:           true,
		State:              state,
		Attempts:           counts.Attempts,
		Admitted:           counts.Admitted,
		Succeeded:          counts.Succeeded,
		Failed:             counts.Failed,
		Denied:             counts.Denied,
		UnavailableIntents: counts.UnavailableIntents,
	}
	if err := telemetry.Validate(); err != nil {
		return EvidenceAttemptTelemetry{}, err
	}
	return telemetry, nil
}

// DeriveEvidenceAttemptState validates counts and returns their unique state.
// Guard denials take precedence over unavailability when no attempt was
// admitted. Once an attempt was admitted, the state describes its outcomes:
// no success is failed, mixed success is partial, and only complete success is
// succeeded.
func DeriveEvidenceAttemptState(counts EvidenceAttemptCounts) (EvidenceAttemptState, error) {
	if err := validateEvidenceAttemptCounts(counts); err != nil {
		return "", err
	}
	if counts.Attempts == 0 {
		return EvidenceAttemptStateNone, nil
	}
	if counts.Admitted == 0 {
		if counts.Denied > 0 {
			return EvidenceAttemptStateBlocked, nil
		}
		return EvidenceAttemptStateUnavailable, nil
	}
	if counts.Succeeded == 0 {
		return EvidenceAttemptStateFailed, nil
	}
	if counts.Failed > 0 || counts.Denied > 0 || counts.UnavailableIntents > 0 {
		return EvidenceAttemptStatePartial, nil
	}
	return EvidenceAttemptStateSucceeded, nil
}

// Validate rejects claimed states that do not exactly match the counters.
func (t EvidenceAttemptTelemetry) Validate() error {
	counts := t.Counts()
	if !t.Coverage {
		if t.State != "" || counts != (EvidenceAttemptCounts{}) {
			return fmt.Errorf("uncovered evidence attempts must not claim state or counters")
		}
		return nil
	}

	state, err := DeriveEvidenceAttemptState(counts)
	if err != nil {
		return err
	}
	if t.State != state {
		return fmt.Errorf("evidence attempt state does not match counters")
	}
	return nil
}

// Counts returns the transport-neutral aggregate counts represented by t.
func (t EvidenceAttemptTelemetry) Counts() EvidenceAttemptCounts {
	return EvidenceAttemptCounts{
		Attempts:           t.Attempts,
		Admitted:           t.Admitted,
		Succeeded:          t.Succeeded,
		Failed:             t.Failed,
		Denied:             t.Denied,
		UnavailableIntents: t.UnavailableIntents,
	}
}

func validateEvidenceAttemptCounts(counts EvidenceAttemptCounts) error {
	values := [...]int{
		counts.Attempts,
		counts.Admitted,
		counts.Succeeded,
		counts.Failed,
		counts.Denied,
		counts.UnavailableIntents,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("evidence attempt counters must be nonnegative")
		}
		if value > maxEvidenceAttemptCount {
			return fmt.Errorf("evidence attempt counter exceeds maximum")
		}
	}
	if counts.Admitted != counts.Succeeded+counts.Failed {
		return fmt.Errorf("admitted evidence attempts must equal succeeded plus failed")
	}
	if counts.Attempts != counts.Admitted+counts.Denied+counts.UnavailableIntents {
		return fmt.Errorf("evidence attempts must equal admitted plus denied plus unavailable intents")
	}
	return nil
}

func validEvidenceAttemptState(state EvidenceAttemptState) bool {
	switch state {
	case EvidenceAttemptStateNone, EvidenceAttemptStateUnavailable, EvidenceAttemptStateBlocked,
		EvidenceAttemptStateFailed, EvidenceAttemptStatePartial, EvidenceAttemptStateSucceeded:
		return true
	default:
		return false
	}
}
