package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func FuzzDecodeHeaderCanonical(f *testing.F) {
	header, _ := lifecycleFuzzHeaderPlan(f)
	canonical := lifecycleFuzzEncodeHeader(f, header)
	addLifecycleCodecSeeds(f, canonical, MaxHeaderBytes, "header_sha256")

	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeHeader(data)
		if err != nil {
			assertLifecycleFuzzContractError(t, err)
			return
		}
		if err := ValidateHeader(decoded); err != nil {
			t.Fatalf("decoded header did not validate: %v", err)
		}
		encoded, err := EncodeHeader(decoded)
		if err != nil {
			t.Fatalf("encode accepted header: %v", err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("accepted header was not canonical")
		}
		roundTrip, err := DecodeHeader(encoded)
		if err != nil || roundTrip != decoded {
			t.Fatalf("header second decode: decoded=%+v err=%v", roundTrip, err)
		}
		second, err := EncodeHeader(roundTrip)
		if err != nil || !bytes.Equal(second, encoded) {
			t.Fatalf("header encoding was not idempotent: err=%v", err)
		}
	})
}

func FuzzDecodePlanCanonical(f *testing.F) {
	header, plan := lifecycleFuzzHeaderPlan(f)
	canonical := lifecycleFuzzEncodePlan(f, plan)
	addLifecycleCodecSeeds(f, canonical, MaxPlanBytes, "plan_sha256")

	reconciled, err := NewReconciledPlan(header, 2, strings.Repeat("b", 64), strings.Repeat("c", 64), plan.Binding)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(lifecycleFuzzEncodePlan(f, reconciled))

	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodePlan(data)
		if err != nil {
			assertLifecycleFuzzContractError(t, err)
			return
		}
		if err := ValidatePlan(decoded); err != nil {
			t.Fatalf("decoded plan did not validate: %v", err)
		}
		encoded, err := EncodePlan(decoded)
		if err != nil {
			t.Fatalf("encode accepted plan: %v", err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("accepted plan was not canonical")
		}
		roundTrip, err := DecodePlan(encoded)
		if err != nil || !reflect.DeepEqual(roundTrip, decoded) {
			t.Fatalf("plan second decode: decoded=%+v err=%v", roundTrip, err)
		}
		second, err := EncodePlan(roundTrip)
		if err != nil || !bytes.Equal(second, encoded) {
			t.Fatalf("plan encoding was not idempotent: err=%v", err)
		}
	})
}

func FuzzDecodeEventCanonical(f *testing.F) {
	_, plan := lifecycleFuzzHeaderPlan(f)
	events := lifecycleFuzzEvents(f, plan)
	for _, event := range events {
		f.Add(lifecycleFuzzEncodeEvent(f, event))
	}
	canonical := lifecycleFuzzEncodeEvent(f, events[0])
	addLifecycleCodecSeeds(f, canonical, MaxEventBytes, "event_sha256")
	invalidEvidence := bytes.Replace(canonical, []byte(`"proofs":["durable_commit"]`), []byte(`"proofs":[]`), 1)
	f.Add(invalidEvidence)

	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeEvent(data)
		if err != nil {
			assertLifecycleFuzzContractError(t, err)
			return
		}
		if err := ValidateEvent(decoded); err != nil {
			t.Fatalf("decoded event did not validate: %v", err)
		}
		encoded, err := EncodeEvent(decoded)
		if err != nil {
			t.Fatalf("encode accepted event: %v", err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("accepted event was not canonical")
		}
		roundTrip, err := DecodeEvent(encoded)
		if err != nil || !reflect.DeepEqual(roundTrip, decoded) {
			t.Fatalf("event second decode: decoded=%+v err=%v", roundTrip, err)
		}
		second, err := EncodeEvent(roundTrip)
		if err != nil || !bytes.Equal(second, encoded) {
			t.Fatalf("event encoding was not idempotent: err=%v", err)
		}
	})
}

func lifecycleFuzzHeaderPlan(t testing.TB) (LedgerHeader, Plan) {
	t.Helper()
	header, err := NewHeader(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	plan, err := NewPlan(header, 1, Binding{Privacy: PrivacyContentMinimized, Identity: Identity{
		ExperimentSHA256:  digest,
		TaskSHA256:        digest,
		SkillSHA256:       digest,
		AgentSHA256:       digest,
		ModelSHA256:       digest,
		EnvironmentSHA256: digest,
		GraderSHA256:      digest,
		BudgetsSHA256:     digest,
		AdapterSHA256:     digest,
		AuthoritySHA256:   digest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return header, plan
}

func lifecycleFuzzEvents(t testing.TB, plan Plan) []Event {
	t.Helper()
	unknown := UnknownUsage()
	zero := Usage{
		EstimatedCostMicroUSD: ObservedMetric(0),
		InputTokens:           ObservedMetric(0),
		OutputTokens:          Metric{State: MetricUnknown},
	}

	projection, err := InitialProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	committed := lifecycleFuzzNewEvent(t, plan, projection, StateCommitted, []Proof{ProofDurableCommit}, Evidence{
		ErrorClass: ErrorNone,
		Usage:      zero,
	})
	projection = lifecycleFuzzApply(t, plan, projection, committed)
	spawning := lifecycleFuzzNewEvent(t, plan, projection, StateSpawning, []Proof{ProofDurableSpawnIntent}, Evidence{
		ErrorClass: ErrorNone,
		Usage:      unknown,
	})
	projection = lifecycleFuzzApply(t, plan, projection, spawning)
	runningEvidence := Evidence{
		ProcessIdentitySHA256: strings.Repeat("b", 64),
		ErrorClass:            ErrorNone,
		Usage: Usage{
			EstimatedCostMicroUSD: ObservedMetric(0),
			InputTokens:           ObservedMetric(7),
			OutputTokens:          Metric{State: MetricUnknown},
		},
	}
	running := lifecycleFuzzNewEvent(t, plan, projection, StateRunning, []Proof{ProofDurableProcessIdentity}, runningEvidence)
	projection = lifecycleFuzzApply(t, plan, projection, running)
	terminalEvidence := runningEvidence
	terminalEvidence.ReceiptSHA256 = strings.Repeat("c", 64)
	terminalEvidence.Usage.InputTokens = ObservedMetric(9)
	terminalEvidence.Usage.OutputTokens = ObservedMetric(3)
	succeeded := lifecycleFuzzNewEvent(t, plan, projection, StateSucceeded, []Proof{ProofTerminalReceipt, ProofTermination}, terminalEvidence)

	initial, err := InitialProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	unknownEvent := lifecycleFuzzNewEvent(t, plan, initial, StateUnknown, []Proof{ProofIncompleteTerminal}, Evidence{
		ErrorClass: ErrorInternal,
		Usage:      unknown,
	})
	policyDenied := lifecycleFuzzNewEvent(t, plan, initial, StatePolicyDenied,
		[]Proof{ProofCompleteLedger, ProofDurablePolicyRefusal, ProofNoCommit}, Evidence{
			ErrorClass: ErrorPolicyDenied,
			Usage:      unknown,
		})
	unsupported := lifecycleFuzzNewEvent(t, plan, initial, StateUnsupported,
		[]Proof{ProofCompleteLedger, ProofDurableCapabilityRefusal, ProofNoCommit}, Evidence{
			ErrorClass: ErrorUnsupported,
			Usage:      unknown,
		})

	return []Event{committed, spawning, running, succeeded, unknownEvent, policyDenied, unsupported}
}

func lifecycleFuzzNewEvent(t testing.TB, plan Plan, projection Projection, to State, proofs []Proof, evidence Evidence) Event {
	t.Helper()
	event, err := NewEvent(plan, projection, to, proofs, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func lifecycleFuzzApply(t testing.TB, plan Plan, projection Projection, event Event) Projection {
	t.Helper()
	next, err := Apply(plan, projection, event)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func lifecycleFuzzEncodeHeader(t testing.TB, value LedgerHeader) []byte {
	t.Helper()
	data, err := EncodeHeader(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func lifecycleFuzzEncodePlan(t testing.TB, value Plan) []byte {
	t.Helper()
	data, err := EncodePlan(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func lifecycleFuzzEncodeEvent(t testing.TB, value Event) []byte {
	t.Helper()
	data, err := EncodeEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func addLifecycleCodecSeeds(f *testing.F, canonical []byte, maximum int, digestField string) {
	f.Helper()
	f.Add(canonical)
	f.Add(bytes.Replace(canonical, []byte(`{"schema":`), []byte(`{"schema":"duplicate","schema":`), 1))
	f.Add(bytes.Replace(canonical, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1))
	f.Add(bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1))
	f.Add(append([]byte(" "), canonical...))
	f.Add(bytes.Replace(canonical, []byte(","), []byte(", "), 1))
	f.Add(bytes.Replace(canonical, []byte(","), []byte(",\n"), 1))
	f.Add(lifecycleFuzzReorderMembers(canonical))
	f.Add(append(bytes.TrimSuffix(bytes.Clone(canonical), []byte("\n")), []byte("{}\n")...))
	f.Add(append(bytes.Repeat([]byte{'x'}, maximum), '\n'))
	f.Add(lifecycleFuzzDepthSeed())
	f.Add([]byte{'{', '"', 0xff, '"', ':', '1', '}', '\n'})
	f.Add(lifecycleFuzzCorruptDigest(canonical, digestField))
}

func lifecycleFuzzDepthSeed() []byte {
	return append(append([]byte(`{"unknown":`), bytes.Repeat([]byte{'['}, maxJSONDepth+2)...),
		append([]byte("0"), append(bytes.Repeat([]byte{']'}, maxJSONDepth+2), '\n')...)...)
}

func lifecycleFuzzCorruptDigest(canonical []byte, field string) []byte {
	result := bytes.Clone(canonical)
	prefix := []byte(`"` + field + `":"`)
	index := bytes.Index(result, prefix)
	if index < 0 {
		return append([]byte(" "), canonical...)
	}
	digestOffset := index + len(prefix)
	if result[digestOffset] == 'a' {
		result[digestOffset] = 'b'
	} else {
		result[digestOffset] = 'a'
	}
	return result
}

func lifecycleFuzzReorderMembers(canonical []byte) []byte {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(canonical), &members); err != nil || len(members) < 2 {
		return append([]byte(" "), canonical...)
	}
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var reordered bytes.Buffer
	reordered.WriteByte('{')
	for index, key := range keys {
		if index != 0 {
			reordered.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		reordered.Write(encodedKey)
		reordered.WriteByte(':')
		reordered.Write(members[key])
	}
	reordered.WriteString("}\n")
	return reordered.Bytes()
}

func assertLifecycleFuzzContractError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrContract) {
		t.Fatalf("decoder returned unstable error classification: %v", err)
	}
}
