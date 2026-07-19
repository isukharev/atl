package agenteval

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestDeriveEvidenceAttemptState(t *testing.T) {
	tests := []struct {
		name   string
		counts EvidenceAttemptCounts
		want   EvidenceAttemptState
	}{
		{name: "none", want: EvidenceAttemptStateNone},
		{name: "unavailable", counts: EvidenceAttemptCounts{Attempts: 2, UnavailableIntents: 2}, want: EvidenceAttemptStateUnavailable},
		{name: "blocked", counts: EvidenceAttemptCounts{Attempts: 2, Denied: 1, UnavailableIntents: 1}, want: EvidenceAttemptStateBlocked},
		{name: "failed", counts: EvidenceAttemptCounts{Attempts: 2, Admitted: 1, Failed: 1, Denied: 1}, want: EvidenceAttemptStateFailed},
		{name: "partial failed", counts: EvidenceAttemptCounts{Attempts: 2, Admitted: 2, Succeeded: 1, Failed: 1}, want: EvidenceAttemptStatePartial},
		{name: "partial denied", counts: EvidenceAttemptCounts{Attempts: 2, Admitted: 1, Succeeded: 1, Denied: 1}, want: EvidenceAttemptStatePartial},
		{name: "partial unavailable", counts: EvidenceAttemptCounts{Attempts: 2, Admitted: 1, Succeeded: 1, UnavailableIntents: 1}, want: EvidenceAttemptStatePartial},
		{name: "succeeded", counts: EvidenceAttemptCounts{Attempts: 3, Admitted: 3, Succeeded: 3}, want: EvidenceAttemptStateSucceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveEvidenceAttemptState(tt.counts)
			if err != nil {
				t.Fatalf("DeriveEvidenceAttemptState() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DeriveEvidenceAttemptState() = %q, want %q", got, tt.want)
			}
			telemetry, err := NewEvidenceAttemptTelemetry(true, tt.counts)
			if err != nil {
				t.Fatalf("NewEvidenceAttemptTelemetry() error = %v", err)
			}
			if telemetry.State != tt.want || telemetry.Counts() != tt.counts {
				t.Fatalf("telemetry = %#v, want state %q and counts %#v", telemetry, tt.want, tt.counts)
			}
			if err := telemetry.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestEvidenceAttemptTelemetryMissingCoverageClaimsNothing(t *testing.T) {
	telemetry, err := NewEvidenceAttemptTelemetry(false, EvidenceAttemptCounts{})
	if err != nil {
		t.Fatalf("NewEvidenceAttemptTelemetry() error = %v", err)
	}
	if telemetry != (EvidenceAttemptTelemetry{}) {
		t.Fatalf("uncovered telemetry = %#v, want zero value", telemetry)
	}
	if err := telemetry.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if _, err := NewEvidenceAttemptTelemetry(false, EvidenceAttemptCounts{Attempts: 1, Denied: 1}); err == nil {
		t.Fatal("NewEvidenceAttemptTelemetry() accepted uncovered counters")
	}
	for _, invalid := range []EvidenceAttemptTelemetry{
		{State: EvidenceAttemptStateNone},
		{Attempts: 1, UnavailableIntents: 1},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate() accepted uncovered claim %#v", invalid)
		}
	}
}

func TestEvidenceAttemptCountsRejectImpossibleAndBoundViolations(t *testing.T) {
	tests := []struct {
		name   string
		counts EvidenceAttemptCounts
	}{
		{name: "negative attempts", counts: EvidenceAttemptCounts{Attempts: -1}},
		{name: "negative admitted", counts: EvidenceAttemptCounts{Admitted: -1}},
		{name: "negative succeeded", counts: EvidenceAttemptCounts{Succeeded: -1}},
		{name: "negative failed", counts: EvidenceAttemptCounts{Failed: -1}},
		{name: "negative denied", counts: EvidenceAttemptCounts{Denied: -1}},
		{name: "negative unavailable", counts: EvidenceAttemptCounts{UnavailableIntents: -1}},
		{name: "attempt overflow", counts: EvidenceAttemptCounts{Attempts: math.MaxInt}},
		{name: "admitted overflow", counts: EvidenceAttemptCounts{Admitted: maxEvidenceAttemptCount + 1}},
		{name: "succeeded overflow", counts: EvidenceAttemptCounts{Succeeded: maxEvidenceAttemptCount + 1}},
		{name: "failed overflow", counts: EvidenceAttemptCounts{Failed: maxEvidenceAttemptCount + 1}},
		{name: "denied overflow", counts: EvidenceAttemptCounts{Denied: maxEvidenceAttemptCount + 1}},
		{name: "unavailable overflow", counts: EvidenceAttemptCounts{UnavailableIntents: maxEvidenceAttemptCount + 1}},
		{name: "admitted outcome mismatch", counts: EvidenceAttemptCounts{Attempts: 1, Admitted: 1}},
		{name: "attempt classification mismatch", counts: EvidenceAttemptCounts{Attempts: 2, Admitted: 1, Succeeded: 1}},
		{name: "outcomes without admission", counts: EvidenceAttemptCounts{Attempts: 1, Succeeded: 1, UnavailableIntents: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DeriveEvidenceAttemptState(tt.counts); err == nil {
				t.Fatal("DeriveEvidenceAttemptState() accepted invalid counts")
			}
			if _, err := NewEvidenceAttemptTelemetry(true, tt.counts); err == nil {
				t.Fatal("NewEvidenceAttemptTelemetry() accepted invalid counts")
			}
		})
	}
}

func TestEvidenceAttemptTelemetryRejectsNonDerivedState(t *testing.T) {
	valid, err := NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{Attempts: 1, Admitted: 1, Succeeded: 1})
	if err != nil {
		t.Fatalf("NewEvidenceAttemptTelemetry() error = %v", err)
	}
	for _, state := range []EvidenceAttemptState{
		"",
		EvidenceAttemptStateNone,
		EvidenceAttemptStateUnavailable,
		EvidenceAttemptStateBlocked,
		EvidenceAttemptStateFailed,
		EvidenceAttemptStatePartial,
		"future-state",
	} {
		candidate := valid
		candidate.State = state
		if err := candidate.Validate(); err == nil {
			t.Fatalf("Validate() accepted state %q for successful counters", state)
		}
	}
}

func TestEvidenceAttemptTelemetryCanonicalPrivacySafeJSON(t *testing.T) {
	telemetry, err := NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{
		Attempts: 4, Admitted: 2, Succeeded: 1, Failed: 1, Denied: 1, UnavailableIntents: 1,
	})
	if err != nil {
		t.Fatalf("NewEvidenceAttemptTelemetry() error = %v", err)
	}
	encoded, err := json.Marshal(telemetry)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const want = `{"coverage":true,"state":"partial","attempts":4,"admitted":2,"succeeded":1,"failed":1,"denied":1,"unavailable_intents":1}`
	if string(encoded) != want {
		t.Fatalf("JSON = %s, want %s", encoded, want)
	}
	for _, forbidden := range []string{"command", "argument", "path", "url", "error", "value"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("privacy-safe JSON unexpectedly contains %q: %s", forbidden, encoded)
		}
	}

	uncovered, err := json.Marshal(EvidenceAttemptTelemetry{})
	if err != nil {
		t.Fatalf("json.Marshal(zero) error = %v", err)
	}
	if string(uncovered) != `{"coverage":false}` {
		t.Fatalf("uncovered JSON = %s", uncovered)
	}
	none, err := json.Marshal(EvidenceAttemptTelemetry{Coverage: true, State: EvidenceAttemptStateNone})
	if err != nil {
		t.Fatalf("json.Marshal(none) error = %v", err)
	}
	if string(none) != `{"coverage":true,"state":"none"}` {
		t.Fatalf("none JSON = %s", none)
	}
}

func TestEvidenceAttemptTelemetryControlRemovedWouldMisclassify(t *testing.T) {
	// A raw caller-provided state could label a guard denial as successful. The
	// validation control must reject that contradiction so aggregate benchmark
	// reports cannot turn blocked evidence retrieval into measured success.
	contradiction := EvidenceAttemptTelemetry{
		Coverage: true,
		State:    EvidenceAttemptStateSucceeded,
		Attempts: 1,
		Denied:   1,
	}
	if derived, err := DeriveEvidenceAttemptState(contradiction.Counts()); err != nil || derived != EvidenceAttemptStateBlocked {
		t.Fatalf("derived state = %q, err = %v; want blocked", derived, err)
	}
	if err := contradiction.Validate(); err == nil {
		t.Fatal("Validate() accepted a caller-claimed successful state for a denied attempt")
	}
}

func TestEvidenceOutcomeReportIsBoundedAndAuditChecked(t *testing.T) {
	tests := []struct {
		name       string
		final      string
		audit      EvidenceAttemptCounts
		covered    bool
		consistent bool
		wantErr    bool
	}{
		{name: "absent", final: `{"answer":"ok"}`},
		{name: "no attempt", final: `{"evidence_outcome":{"state":"none"}}`, covered: true, consistent: true},
		{name: "unavailable self report after zero calls", final: `{"evidence_outcome":{"state":"unavailable"}}`, covered: true, consistent: true},
		{name: "observed success", final: `{"evidence_outcome":{"state":"succeeded"}}`, audit: EvidenceAttemptCounts{Attempts: 1, Admitted: 1, Succeeded: 1}, covered: true, consistent: true},
		{name: "fabricated success", final: `{"evidence_outcome":{"state":"succeeded"}}`, covered: true},
		{name: "unknown state", final: `{"evidence_outcome":{"state":"maybe"}}`, wantErr: true},
		{name: "unknown envelope member", final: `{"evidence_outcome":{"state":"none","detail":"private"}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := ParseEvidenceOutcomeReport([]byte(tt.final))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseEvidenceOutcomeReport() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if report.Coverage != tt.covered {
				t.Fatalf("coverage = %v, want %v", report.Coverage, tt.covered)
			}
			audit, err := NewEvidenceAttemptTelemetry(true, tt.audit)
			if err != nil {
				t.Fatal(err)
			}
			if got := report.ConsistentWithAudit(audit); got != tt.consistent {
				t.Fatalf("ConsistentWithAudit() = %v, want %v", got, tt.consistent)
			}
		})
	}
}
