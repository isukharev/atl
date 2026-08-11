package lifecycle

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAttemptStateMachineIsClosedAndAbsorbing(t *testing.T) {
	allowed := 0
	for _, from := range States() {
		for _, to := range States() {
			sets := AllowedProofSets(from, to)
			if len(sets) == 0 {
				continue
			}
			allowed++
			if IsTerminal(from) {
				t.Fatalf("terminal state %s has outgoing transition to %s", from, to)
			}
			for _, set := range sets {
				if !sortedUniqueProofs(set) || !validTransitionProofs(from, to, set) {
					t.Fatalf("invalid proof set for %s -> %s: %v", from, to, set)
				}
			}
		}
	}
	if allowed != 22 {
		t.Fatalf("allowed transition pair count = %d, want 22", allowed)
	}
	if len(States()) != 11 || len(Proofs()) != 15 {
		t.Fatalf("closed vocabulary drift: states=%d proofs=%d", len(States()), len(Proofs()))
	}
}

func TestAttemptPlanAndEventChainAreCanonicalAndBound(t *testing.T) {
	header, plan := testHeaderPlan(t)
	headerBytes, err := EncodeHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodeHeader(headerBytes); err != nil || decoded != header {
		t.Fatalf("header round trip: decoded=%+v err=%v", decoded, err)
	}
	planBytes, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodePlan(planBytes); err != nil || decoded.PlanSHA256 != plan.PlanSHA256 {
		t.Fatalf("plan round trip: decoded=%+v err=%v", decoded, err)
	}

	projection, err := InitialProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]Event, 0, 4)
	appendEvent := func(to State, proofs []Proof, evidence Evidence) {
		t.Helper()
		event, eventErr := NewEvent(plan, projection, to, proofs, evidence)
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		encoded, encodeErr := EncodeEvent(event)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		decoded, decodeErr := DecodeEvent(encoded)
		if decodeErr != nil || decoded.EventSHA256 != event.EventSHA256 {
			t.Fatalf("event round trip: decoded=%+v err=%v", decoded, decodeErr)
		}
		projection, eventErr = Apply(plan, projection, decoded)
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		events = append(events, decoded)
	}
	appendEvent(StateCommitted, []Proof{ProofDurableCommit}, testEvidence(ErrorNone))
	appendEvent(StateSpawning, []Proof{ProofDurableSpawnIntent}, testEvidence(ErrorNone))
	running := testEvidence(ErrorNone)
	running.ProcessIdentitySHA256 = strings.Repeat("b", 64)
	running.Usage.InputTokens = ObservedMetric(7)
	appendEvent(StateRunning, []Proof{ProofDurableProcessIdentity}, running)
	terminal := testEvidence(ErrorNone)
	terminal.ReceiptSHA256 = strings.Repeat("c", 64)
	terminal.Usage.InputTokens = ObservedMetric(9)
	terminal.Usage.EstimatedCostMicroUSD = ObservedMetric(12)
	appendEvent(StateSucceeded, []Proof{ProofTerminalReceipt, ProofTermination}, terminal)
	if !projection.Terminal || projection.State != StateSucceeded || projection.Usage.InputTokens.Value == nil ||
		*projection.Usage.InputTokens.Value != 9 || projection.Usage.EstimatedCostMicroUSD.Value == nil ||
		*projection.Usage.EstimatedCostMicroUSD.Value != 12 {
		t.Fatalf("unexpected projection: %+v", projection)
	}
	if got, projectErr := Project(plan, events); projectErr != nil || got != projection {
		t.Fatalf("project: got=%+v err=%v", got, projectErr)
	}
	if _, err := NewEvent(plan, projection, StateUnknown, []Proof{ProofIncompleteTerminal}, testEvidence(ErrorInternal)); !errors.Is(err, ErrContract) {
		t.Fatalf("terminal replay accepted: %v", err)
	}
}

func TestAttemptUsageIsPresenceAwareAndMonotonic(t *testing.T) {
	_, plan := testHeaderPlan(t)
	projection, _ := InitialProjection(plan)
	committed, err := NewEvent(plan, projection, StateCommitted, []Proof{ProofDurableCommit}, testEvidence(ErrorNone))
	if err != nil {
		t.Fatal(err)
	}
	projection, _ = Apply(plan, projection, committed)
	spawn, err := NewEvent(plan, projection, StateSpawning, []Proof{ProofDurableSpawnIntent}, Evidence{
		ErrorClass: ErrorNone,
		Usage: Usage{EstimatedCostMicroUSD: ObservedMetric(0),
			InputTokens: Metric{State: MetricUnknown}, OutputTokens: Metric{State: MetricUnknown}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEvent(spawn)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"estimated_cost_microusd":{"state":"observed"}`)) ||
		!bytes.Contains(encoded, []byte(`"estimated_cost_microusd":{"state":"observed","value":0}`)) ||
		bytes.Contains(encoded, []byte(`"input_tokens":{"state":"unknown","value"`)) {
		t.Fatalf("presence-aware metric wire drift: %s", encoded)
	}
	projection, _ = Apply(plan, projection, spawn)
	regressed := testEvidence(ErrorInternal)
	regressed.Usage.EstimatedCostMicroUSD = ObservedMetric(0)
	regressed.Usage.InputTokens = Metric{State: MetricUnknown}
	if _, err := NewEvent(plan, projection, StateUnknown, []Proof{ProofIncompleteTerminal}, regressed); err != nil {
		t.Fatalf("observed zero lower bound was not preserved: %v", err)
	}
	bad := regressed
	badValue := uint64(1)
	bad.Usage.EstimatedCostMicroUSD = Metric{State: MetricUnknown, Value: &badValue}
	if _, err := NewEvent(plan, projection, StateUnknown, []Proof{ProofIncompleteTerminal}, bad); !errors.Is(err, ErrContract) {
		t.Fatalf("unknown nonzero metric accepted: %v", err)
	}
}

func TestAttemptWireRejectsDuplicateUnknownNoncanonicalFutureAndOversize(t *testing.T) {
	header, _ := testHeaderPlan(t)
	valid, err := EncodeHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"duplicate":    bytes.Replace(valid, []byte(`"schema":`), []byte(`"schema":"agent-eval/attempt-ledger","schema":`), 1),
		"unknown":      bytes.Replace(valid, []byte(`{"schema":`), []byte(`{"extra":1,"schema":`), 1),
		"future":       bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"noncanonical": append([]byte(" "), valid...),
		"trailing":     append(append([]byte(nil), valid...), '\n'),
		"oversize":     append(bytes.Repeat([]byte{'x'}, MaxHeaderBytes), '\n'),
	}
	for name, data := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHeader(data); !errors.Is(err, ErrContract) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func TestAttemptEventDecoderEnforcesTransitionEvidenceWithoutPriorState(t *testing.T) {
	_, plan := testHeaderPlan(t)
	projection, err := InitialProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := NewEvent(plan, projection, StateCommitted, []Proof{ProofDurableCommit}, testEvidence(ErrorNone))
	if err != nil {
		t.Fatal(err)
	}
	projection, _ = Apply(plan, projection, committed)
	spawning, err := NewEvent(plan, projection, StateSpawning, []Proof{ProofDurableSpawnIntent}, testEvidence(ErrorNone))
	if err != nil {
		t.Fatal(err)
	}
	projection, _ = Apply(plan, projection, spawning)
	running := testEvidence(ErrorNone)
	running.ProcessIdentitySHA256 = strings.Repeat("b", 64)
	event, err := NewEvent(plan, projection, StateRunning, []Proof{ProofDurableProcessIdentity}, running)
	if err != nil {
		t.Fatal(err)
	}
	event.Evidence.ProcessIdentitySHA256 = ""
	digest, err := digestEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	event.EventSHA256 = digest
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEvent(append(data, '\n')); !errors.Is(err, ErrContract) {
		t.Fatalf("running event without process identity decoded: %v", err)
	}
}

func TestAttemptAllocationIsLedgerOwnedAndCollisionResistant(t *testing.T) {
	header, first := testHeaderPlan(t)
	second, err := NewPlan(header, 2, first.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if first.AttemptID == second.AttemptID || first.Ordinal != 1 || second.Ordinal != 2 {
		t.Fatalf("attempt allocation collided: first=%+v second=%+v", first, second)
	}
	otherHeader, err := NewHeader(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewPlan(otherHeader, 1, first.Binding)
	if err != nil || other.AttemptID == first.AttemptID {
		t.Fatalf("ledger identity not bound: other=%+v err=%v", other, err)
	}
}

func testHeaderPlan(t *testing.T) (LedgerHeader, Plan) {
	t.Helper()
	header, err := NewHeader(bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	plan, err := NewPlan(header, 1, Binding{Privacy: PrivacyContentMinimized, Identity: Identity{
		ExperimentSHA256: digest, TaskSHA256: digest, SkillSHA256: digest, AgentSHA256: digest,
		ModelSHA256: digest, EnvironmentSHA256: digest, GraderSHA256: digest, BudgetsSHA256: digest, AdapterSHA256: digest,
		AuthoritySHA256: digest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return header, plan
}

func testEvidence(code string) Evidence {
	return Evidence{ErrorClass: code, Usage: UnknownUsage()}
}

func TestAttemptJSONFieldsRemainClosed(t *testing.T) {
	// This compile-time-like projection keeps all v1 field names visible in one
	// place so a future rename cannot silently evade the strict decoder tests.
	data, err := json.Marshal(Event{})
	if err != nil || !bytes.Contains(data, []byte(`"event_sha256"`)) {
		t.Fatalf("event field projection drift: %s %v", data, err)
	}
}
