package grading_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/grading"
)

const (
	inputSHA       = "1111111111111111111111111111111111111111111111111111111111111111"
	environmentSHA = "2222222222222222222222222222222222222222222222222222222222222222"
	fileSHA        = "a948904f2f0f479b8f8197694b30184b0d2ed1c1cd2a1ec0fb85d299a192a447"
)

func TestGradingContractV1IsClosedCanonicalAndImmutable(t *testing.T) {
	contract := builtinContract(t)
	data, err := grading.EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := grading.DecodeContract(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	decoded.Capabilities[0].Support = grading.SupportUnknown
	again := builtinContract(t)
	if again.Capabilities[0].Support != grading.SupportSupported {
		t.Fatal("built-in contract shared mutable capability storage")
	}
	if len(again.Capabilities) != len(grading.CheckKinds()) || len(again.Modes) != len(grading.Modes()) {
		t.Fatalf("contract is not complete: %+v", again)
	}

	for name, mutation := range map[string][]byte{
		"future":       bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":      bytes.Replace(data, []byte(`,"grader_id"`), []byte(`,"extra":true,"grader_id"`), 1),
		"duplicate":    bytes.Replace(data, []byte(`,"grader_id"`), []byte(`,"schema_version":1,"grader_id"`), 1),
		"trailing":     append(append([]byte{}, data...), []byte("{}\n")...),
		"noncanonical": append([]byte(" "), data...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := grading.DecodeContract(bytes.NewReader(mutation)); err == nil {
				t.Fatal("accepted invalid wire")
			}
		})
	}

	drift := contract
	drift.Modes = slices.Clone(contract.Modes)
	drift.Modes[0].Network = true
	if err := grading.ValidateContract(drift); err == nil {
		t.Fatal("in-process mode acquired network authority")
	}
	drift = contract
	drift.Capabilities = slices.Clone(contract.Capabilities)
	drift.Capabilities[0].Support = grading.SupportUnknown
	if err := grading.ValidateContract(drift); err == nil {
		t.Fatal("built-in capability drift passed")
	}

	bounded := contract
	bounded.GraderID = "bounded-grader"
	bounded.Limits.MaxChecks = 1
	_, manyChecks := deterministicFixture(t)
	manyChecks.ContractSHA256, _ = grading.ContractSHA256(bounded)
	if _, err := grading.Admit(bounded, manyChecks); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("contract max_checks was not enforced: %v", err)
	}
	bounded.Limits.MaxChecks = grading.MaxChecks
	bounded.Limits.MaxScriptInstructions = 1
	backendSHA, _ := grading.ReferenceBackendSHA256()
	zero := int64(0)
	_, script := deterministicOneCheckFixture(t)
	script.ContractSHA256, _ = grading.ContractSHA256(bounded)
	script.Mode = grading.ModeScriptDSL
	script.ExecutionBackendSHA256 = backendSHA
	script.Script = []grading.ScriptInstruction{{Operation: grading.ScriptCommandExitEquals, EvidenceID: "proof", Integer: &zero},
		{Operation: grading.ScriptEmit, CheckID: "mechanical"}}
	if _, err := grading.Admit(bounded, script); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("contract script limit was not enforced: %v", err)
	}
	bounded.Limits.MaxScriptInstructions = grading.MaxScriptInstructions
	bounded.Limits.MaxCitationsPerCheck = 1
	judge := judgeFixture(t, bounded)
	judge.Checks[0].Qualitative.EvidenceIDs = []string{"proof", "unused"}
	if _, err := grading.Admit(bounded, judge); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("contract citation limit was not enforced: %v", err)
	}
}

func TestDeterministicGradersCoverMechanicalFamiliesAndFailClosed(t *testing.T) {
	contract, plan := deterministicFixture(t)
	admitted, err := grading.Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	evidence := mechanicalEvidence()
	prepared, err := grading.PrepareEvidence(context.Background(), admitted, evidence)
	if err != nil {
		t.Fatalf("prepare: %v: %v", err, errors.Unwrap(err))
	}
	defer prepared.Destroy()
	evidence.Files[0].Data[0] = 'X'
	receipt, err := grading.EvaluateDeterministic(context.Background(), admitted, prepared)
	if err != nil {
		t.Fatalf("evaluate: %v: %v", err, errors.Unwrap(err))
	}
	if receipt.Status != grading.ReceiptComplete || len(receipt.Decisions) != 14 {
		t.Fatalf("receipt=%+v", receipt)
	}
	for _, decision := range receipt.Decisions {
		if decision.Presence != grading.PresenceObserved || !decision.Passed || len(decision.Citations) != 1 ||
			len(decision.Citations[0].SHA256) != 64 {
			t.Fatalf("decision=%+v", decision)
		}
	}
	toolDrift := mechanicalEvidence()
	toolDrift.Sequences[1].Values = []string{"read", "inspect", "write"}
	toolEvidence, err := grading.PrepareEvidence(context.Background(), admitted, toolDrift)
	if err != nil {
		t.Fatal(err)
	}
	defer toolEvidence.Destroy()
	toolReceipt, err := grading.EvaluateDeterministic(context.Background(), admitted, toolEvidence)
	if err != nil || decisionByID(t, toolReceipt, "09-tool-sequence").Passed || !decisionByID(t, toolReceipt, "10-action-sequence").Passed {
		t.Fatalf("sequence authority drift receipt=%+v err=%v", toolReceipt, err)
	}
	fractional := mechanicalEvidence()
	fractional.Files[1].Data = []byte(`{"count":9999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999.5}`)
	fractionalEvidence, err := grading.PrepareEvidence(context.Background(), admitted, fractional)
	if err != nil {
		t.Fatal(err)
	}
	defer fractionalEvidence.Destroy()
	fractionalReceipt, err := grading.EvaluateDeterministic(context.Background(), admitted, fractionalEvidence)
	if err != nil || decisionByID(t, fractionalReceipt, "05-json-schema").Passed {
		t.Fatalf("large fractional JSON value passed as integer: receipt=%+v err=%v", fractionalReceipt, err)
	}
	planData, err := grading.EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	assertClosedWire(t, "plan", planData, func(data []byte) error {
		_, err := grading.DecodePlan(bytes.NewReader(data))
		return err
	})
	receiptData, err := grading.EncodeReceipt(plan, receipt)
	if err != nil {
		t.Fatal(err)
	}
	assertClosedWire(t, "receipt", receiptData, func(data []byte) error {
		_, err := grading.DecodeReceipt(bytes.NewReader(data), plan)
		return err
	})
	forged := receipt
	forged.Decisions = slices.Clone(receipt.Decisions)
	forged.Decisions[0].Citations = slices.Clone(receipt.Decisions[0].Citations)
	forged.Decisions[0].Citations[0].SHA256 = strings.Repeat("f", 64)
	if err := grading.ValidateReceipt(plan, forged); err == nil {
		t.Fatal("receipt accepted a citation not bound to its evidence digest")
	}

	missing := mechanicalEvidence()
	missing.Files = missing.Files[1:]
	preparedMissing, err := grading.PrepareEvidence(context.Background(), admitted, missing)
	if err != nil {
		t.Fatal(err)
	}
	defer preparedMissing.Destroy()
	partial, err := grading.EvaluateDeterministic(context.Background(), admitted, preparedMissing)
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionByID(t, partial, "01-file-exists")
	if decision.Presence != grading.PresenceUnknown || decision.Passed || len(decision.Citations) != 0 || partial.Status != grading.ReceiptIncomplete {
		t.Fatalf("missing evidence passed: %+v", decision)
	}

	changed := mechanicalEvidence()
	changed.InputProjectionSHA256 = strings.Repeat("f", 64)
	if _, err := grading.PrepareEvidence(context.Background(), admitted, changed); !errors.Is(err, grading.ErrEvidence) {
		t.Fatalf("identity drift err=%v", err)
	}
	prepared.Destroy()
	if _, err := grading.EvaluateDeterministic(context.Background(), admitted, prepared); !errors.Is(err, grading.ErrEvidence) {
		t.Fatalf("destroyed evidence err=%v", err)
	}
	tightPlan := plan
	tightPlan.Limits.MaxOutputBytes = 1
	tight, err := grading.Admit(contract, tightPlan)
	if err != nil {
		t.Fatal(err)
	}
	tightEvidence, err := grading.PrepareEvidence(context.Background(), tight, mechanicalEvidence())
	if err != nil {
		t.Fatal(err)
	}
	defer tightEvidence.Destroy()
	if _, err := grading.EvaluateDeterministic(context.Background(), tight, tightEvidence); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("receipt byte bound err=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := grading.PrepareEvidence(canceled, admitted, mechanicalEvidence()); !errors.Is(err, grading.ErrInterrupted) {
		t.Fatalf("canceled prepare err=%v", err)
	}
	over := mechanicalEvidence()
	over.Counters = make([]grading.CounterEvidence, grading.MaxEvidenceItems+1)
	for index := range over.Counters {
		over.Counters[index] = grading.CounterEvidence{ID: "counter-" + strings.Repeat("x", 5) + string(rune('a'+index%26)),
			Visibility: grading.VisibilityPublic}
	}
	if _, err := grading.PrepareEvidence(context.Background(), admitted, over); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("item bound err=%v", err)
	}
	nested := mechanicalEvidence()
	for index := 0; index < 4; index++ {
		nested.Sequences = append(nested.Sequences, grading.SequenceEvidence{ID: "zz-nested-" + string(rune('a'+index)),
			Visibility: grading.VisibilityPublic, Values: make([]string, grading.MaxSequenceItems)})
	}
	if _, err := grading.PrepareEvidence(context.Background(), admitted, nested); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("nested item bound err=%v", err)
	}
	deep := plan
	deep.Checks = slices.Clone(plan.Checks)
	deep.Checks[3] = plan.Checks[3]
	deep.Checks[3].JSONValue = &grading.JSONValueRule{EvidenceID: "json", Pointer: "/count",
		Expected: json.RawMessage(strings.Repeat("[", grading.MaxJSONDepth+1) + "0" + strings.Repeat("]", grading.MaxJSONDepth+1))}
	if err := grading.ValidatePlan(deep); err == nil {
		t.Fatal("deep embedded JSON passed")
	}
}

func TestTypedScriptRequiresReferenceBackendAndCannotAcquireAmbientAuthority(t *testing.T) {
	contract := builtinContract(t)
	contractSHA, _ := grading.ContractSHA256(contract)
	backend := referenceBackend(t)
	backendSHA, _ := executionbackend.ContractSHA256(backend)
	zero := int64(0)
	one := uint64(1)
	plan := grading.Plan{Schema: grading.PlanSchema, SchemaVersion: 1, ContractVersion: grading.ContractVersion, ContractSHA256: contractSHA,
		Mode: grading.ModeScriptDSL, InputProjectionSHA256: inputSHA, EnvironmentSHA256: environmentSHA, ExecutionBackendSHA256: backendSHA,
		Checks: []grading.Check{
			{ID: "file-ready", Kind: grading.CheckFileExists, Visibility: grading.VisibilityHidden,
				FileExists: &grading.FileExistsRule{EvidenceID: "hidden-file", Expected: true}},
			{ID: "operation-clean", Kind: grading.CheckCommandExit, Visibility: grading.VisibilityPublic,
				CommandExit: &grading.CommandExitRule{EvidenceID: "command", Expected: 0}},
		},
		Script: []grading.ScriptInstruction{
			{Operation: grading.ScriptFileExists, EvidenceID: "hidden-file"},
			{Operation: grading.ScriptEmit, CheckID: "file-ready"},
			{Operation: grading.ScriptCommandExitEquals, EvidenceID: "command", Integer: &zero},
			{Operation: grading.ScriptEventCountMinimum, EvidenceID: "events", Unsigned: &one},
			{Operation: grading.ScriptAnd},
			{Operation: grading.ScriptEmit, CheckID: "operation-clean"},
		},
		Limits: grading.PlanLimits{DeadlineMillis: 1000, MaxInputBytes: grading.MaxEvidenceBytes, MaxOutputBytes: grading.MaxReceiptBytes}}
	admitted, err := grading.Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := grading.PrepareEvidence(context.Background(), admitted, grading.EvidenceSet{InputProjectionSHA256: inputSHA,
		Files:    []grading.FileEvidence{{ID: "hidden-file", Visibility: grading.VisibilityHidden, Present: true, Mode: 0o600, Data: []byte("secret")}},
		Commands: []grading.CommandEvidence{{ID: "command", Visibility: grading.VisibilityPublic}}, Trees: []grading.TreeEvidence{},
		Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{{ID: "events", Visibility: grading.VisibilityPublic, Value: 1}}})
	if err != nil {
		t.Fatalf("prepare script: %v: %v", err, errors.Unwrap(err))
	}
	defer prepared.Destroy()
	receipt, err := grading.EvaluateScript(context.Background(), admitted, backend, prepared)
	if err != nil || receipt.Status != grading.ReceiptComplete || !receipt.Decisions[0].Passed || !receipt.Decisions[1].Passed {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	local, err := executionbackend.LocalProcessContract(strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("prepare judge: %v: %v", err, errors.Unwrap(err))
	}
	if _, err := grading.EvaluateScript(context.Background(), admitted, local, prepared); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("local process admitted: %v", err)
	}
	drift := plan
	drift.ExecutionBackendSHA256 = strings.Repeat("f", 64)
	if _, err := grading.Admit(contract, drift); !errors.Is(err, grading.ErrPolicy) {
		t.Fatalf("foreign backend admitted: %v", err)
	}
}

func TestOfflineJudgeIsBlindEvidenceBoundBoundedAndPreservesDisagreement(t *testing.T) {
	contract, deterministicPlan := deterministicOneCheckFixture(t)
	deterministic, err := grading.Admit(contract, deterministicPlan)
	if err != nil {
		t.Fatalf("prepare core: %v: %v", err, errors.Unwrap(err))
	}
	prepared, err := grading.PrepareEvidence(context.Background(), deterministic, grading.EvidenceSet{InputProjectionSHA256: inputSHA,
		Files: []grading.FileEvidence{
			{ID: "proof", Visibility: grading.VisibilityHidden, Present: true, Mode: 0o600, Data: []byte("not-expected")},
			{ID: "unused", Visibility: grading.VisibilityHidden, Present: true, Mode: 0o600, Data: []byte("not-reviewed")},
		},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Destroy()
	deterministicReceipt, err := grading.EvaluateDeterministic(context.Background(), deterministic, prepared)
	if err != nil || deterministicReceipt.Decisions[0].Passed {
		t.Fatalf("deterministic=%+v err=%v cause=%v", deterministicReceipt, err, errors.Unwrap(err))
	}

	judgePlan := judgeFixture(t, contract)
	judge, err := grading.Admit(contract, judgePlan)
	if err != nil {
		t.Fatal(err)
	}
	citation := deterministicReceipt.Decisions[0].Citations[0]
	citation.Visibility = grading.VisibilityHidden
	reviews := []grading.Review{
		reviewFixture("model-b", false, citation, prepared.SHA256(), 30),
		reviewFixture("human-a", true, citation, prepared.SHA256(), 0),
		reviewFixture("model-a", true, citation, prepared.SHA256(), 20),
	}
	receipt, err := grading.AssessReviews(context.Background(), judge, prepared, reviews, &grading.DeterministicComparison{
		Plan: deterministicPlan, Receipt: deterministicReceipt,
		Pairs: []grading.ComparisonPair{{JudgeCheckID: "quality", DeterministicCheckID: "mechanical"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != grading.ReceiptComplete || !receipt.Decisions[0].Passed || len(receipt.Reviewers) != 3 ||
		len(receipt.Disagreements) != 2 || receipt.Usage.InputTokens.Value != 50 {
		t.Fatalf("judge receipt=%+v", receipt)
	}
	twoPlan := deterministicPlan
	twoPlan.Checks = append(slices.Clone(deterministicPlan.Checks), grading.Check{ID: "mechanical-two", Kind: grading.CheckFileSHA256,
		Visibility: grading.VisibilityHidden, FileSHA256: &grading.FileSHA256Rule{EvidenceID: "proof", ExpectedSHA256: fileSHA}})
	two, err := grading.Admit(contract, twoPlan)
	if err != nil {
		t.Fatal(err)
	}
	twoReceipt, err := grading.EvaluateDeterministic(context.Background(), two, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := grading.ValidateReceipt(twoPlan, twoReceipt); err != nil {
		t.Fatalf("two-check receipt invalid: %v: %v", err, errors.Unwrap(err))
	}
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, reviews, &grading.DeterministicComparison{
		Plan: twoPlan, Receipt: twoReceipt, Pairs: []grading.ComparisonPair{
			{JudgeCheckID: "quality", DeterministicCheckID: "mechanical"},
			{JudgeCheckID: "quality", DeterministicCheckID: "mechanical-two"},
		},
	}); err == nil {
		t.Fatal("many-to-one deterministic comparison was accepted")
	}
	drift := reviews
	drift[0] = reviewFixture("model-b", false, citation, strings.Repeat("f", 64), 30)
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, drift, nil); err == nil {
		t.Fatal("unbound reviewer evidence passed")
	}
	drift = slices.Clone(reviews)
	drift[0] = reviews[0]
	drift[0].PromptContractSHA256 = strings.Repeat("f", 64)
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, drift, nil); err == nil {
		t.Fatal("reviewer prompt-contract drift passed")
	}
	foreign := grading.Review{ReviewerID: reviews[0].ReviewerID, RubricSHA256: reviews[0].RubricSHA256,
		PromptContractSHA256:  reviews[0].PromptContractSHA256,
		BlindAssignmentSHA256: reviews[0].BlindAssignmentSHA256, EvidenceProjectionSHA256: reviews[0].EvidenceProjectionSHA256,
		Decisions: []grading.ReviewDecision{{CheckID: "quality", Passed: false, Citations: []grading.Citation{prepared.Citations()[1]}}},
		Usage:     reviews[0].Usage}
	drift = slices.Clone(reviews)
	drift[0] = foreign
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, drift, nil); err == nil {
		t.Fatal("review cited evidence outside the criterion projection")
	}
	over := reviewFixture("model-b", false, citation, prepared.SHA256(), 101)
	drift = slices.Clone(reviews)
	drift[0] = over
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, drift, nil); err == nil {
		t.Fatal("reviewer token budget passed")
	}
	drift = slices.Clone(reviews)
	drift[0] = reviewFixture("model-b", false, citation, prepared.SHA256(), 30)
	drift[0].Usage.DurationMillis.Value = judgePlan.Limits.DeadlineMillis + 1
	if _, err := grading.AssessReviews(context.Background(), judge, prepared, drift, nil); err == nil {
		t.Fatal("reviewer duration exceeded plan deadline")
	}
}

func TestCoreGraderPreservesCoverageAndReceiptAuthority(t *testing.T) {
	contract, plan := deterministicOneCheckFixture(t)
	task := core.Task{ID: "task", Checks: []core.Check{{ID: "mechanical", Weight: 1}}}
	fixture := core.Fixture{ID: "fixture"}
	treatment := core.Treatment{ID: "treatment"}
	identity := core.AttemptIdentity{Plan: "plan", Task: task.ID, Treatment: treatment.ID, Ordinal: 1}
	inputSHA, err := grading.CoreAttemptInputSHA256(identity, task, fixture, treatment)
	if err != nil {
		t.Fatal(err)
	}
	plan.InputProjectionSHA256 = inputSHA
	admitted, err := grading.Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := grading.PrepareEvidence(context.Background(), admitted, grading.EvidenceSet{InputProjectionSHA256: inputSHA,
		Files:    []grading.FileEvidence{{ID: "proof", Visibility: grading.VisibilityHidden, Present: true, Mode: 0o600, Data: []byte("hello world\n")}},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Destroy()
	receipt, err := grading.EvaluateDeterministic(context.Background(), admitted, prepared)
	if err != nil {
		t.Fatalf("evaluate core: %v: %v", err, errors.Unwrap(err))
	}
	grader, err := grading.NewCoreGrader(identity, task, fixture, treatment, plan, receipt)
	if err != nil {
		t.Fatal(err)
	}
	profile := gradingProfile{grader: grader, task: task}
	registry, err := core.NewRegistry(profile)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := core.NewEngine(registry)
	result, err := engine.Run(context.Background(), core.Plan{ID: "plan", Profile: "grading", Task: task,
		Fixture: fixture, Treatment: treatment, Attempts: 1})
	if err != nil || result.Attempts[0].Outcome != core.OutcomeSucceeded || result.Attempts[0].Score.BasisPoints != 10_000 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	copy := grader.Receipt()
	copy.Decisions[0].Passed = false
	if !grader.Receipt().Decisions[0].Passed {
		t.Fatal("receipt accessor returned mutable storage")
	}
	if _, err := engine.Run(context.Background(), core.Plan{ID: "plan", Profile: "grading", Task: task,
		Fixture: fixture, Treatment: treatment, Attempts: 2}); err == nil {
		t.Fatal("one-attempt receipt was replayed for another ordinal")
	}
}

type gradingProfile struct {
	grader core.Grader
	task   core.Task
}

func (p gradingProfile) Descriptor() core.ProfileDescriptor {
	return core.ProfileDescriptor{ID: "grading", Capabilities: []core.Capability{}}
}

func (p gradingProfile) Open(context.Context, core.AdmittedPlan, core.AttemptIdentity) (core.AttemptRuntime, error) {
	return core.AttemptRuntime{Adapter: gradingAdapter{task: p.task}, Backend: passthroughBackend{}, Grader: p.grader}, nil
}

type gradingAdapter struct{ task core.Task }

func (a gradingAdapter) Execute(context.Context, core.AttemptInput) (core.Observation, error) {
	return core.Observation{Checks: []core.CheckObservation{{ID: a.task.Checks[0].ID, Presence: core.PresenceObserved, Passed: false}},
		Resources: []core.ResourceObservation{}, Evidence: []core.EvidenceObservation{}}, nil
}

type passthroughBackend struct{}

func (passthroughBackend) Run(ctx context.Context, input core.AttemptInput, adapter core.AgentAdapter) (core.Observation, error) {
	return adapter.Execute(ctx, input)
}

func builtinContract(t *testing.T) grading.Contract {
	t.Helper()
	contract, err := grading.BuiltinContract()
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func deterministicFixture(t *testing.T) (grading.Contract, grading.Plan) {
	t.Helper()
	contract := builtinContract(t)
	contractSHA, _ := grading.ContractSHA256(contract)
	checks := []grading.Check{
		{ID: "01-file-exists", Kind: grading.CheckFileExists, Visibility: grading.VisibilityPublic,
			FileExists: &grading.FileExistsRule{EvidenceID: "file-a", Expected: true}},
		{ID: "02-file-metadata", Kind: grading.CheckFileMetadata, Visibility: grading.VisibilityPublic,
			FileMetadata: &grading.FileMetadataRule{EvidenceID: "file-a", ExpectedSizeBytes: 12, ExpectedMode: 0o644}},
		{ID: "03-file-sha", Kind: grading.CheckFileSHA256, Visibility: grading.VisibilityPublic,
			FileSHA256: &grading.FileSHA256Rule{EvidenceID: "file-a", ExpectedSHA256: fileSHA}},
		{ID: "04-json-value", Kind: grading.CheckJSONValue, Visibility: grading.VisibilityPublic,
			JSONValue: &grading.JSONValueRule{EvidenceID: "json", Pointer: "/count", Expected: json.RawMessage(`2`)}},
		{ID: "05-json-schema", Kind: grading.CheckJSONSchema, Visibility: grading.VisibilityPublic,
			JSONSchema: &grading.JSONSchemaRule{EvidenceID: "json", Fields: []grading.JSONField{{Pointer: "/count", Type: grading.JSONTypeInteger, Required: true}}}},
		{ID: "06-command-exit", Kind: grading.CheckCommandExit, Visibility: grading.VisibilityPublic,
			CommandExit: &grading.CommandExitRule{EvidenceID: "command", Expected: 0}},
		{ID: "07-command-output", Kind: grading.CheckCommandOutput, Visibility: grading.VisibilityPublic,
			CommandOutput: &grading.CommandOutputRule{EvidenceID: "command", Stream: grading.OutputStdout, ExpectedSHA256: fileSHA}},
		{ID: "08-tree-diff", Kind: grading.CheckTreeDiff, Visibility: grading.VisibilityHidden,
			TreeDiff: &grading.TreeDiffRule{EvidenceID: "tree", Expected: []grading.TreeChangeExpectation{{Path: "answer.txt", Kind: grading.TreeAdded, SHA256: fileSHA}}}},
		{ID: "09-tool-sequence", Kind: grading.CheckToolSequence, Visibility: grading.VisibilityPublic,
			ToolSequence: &grading.SequenceRule{EvidenceID: "tools", Expected: []string{"read", "write"}, MinimumSimilarityBPS: 10_000}},
		{ID: "10-action-sequence", Kind: grading.CheckActionSequence, Visibility: grading.VisibilityPublic,
			ActionSequence: &grading.SequenceRule{EvidenceID: "actions", Expected: []string{"inspect", "verify"}, MinimumSimilarityBPS: 7500}},
		{ID: "11-skill-activation", Kind: grading.CheckSkillActivation, Visibility: grading.VisibilityPublic,
			SkillActivation: &grading.CountRule{EvidenceID: "activation", Minimum: 1, Maximum: 1}},
		{ID: "12-skill-use", Kind: grading.CheckSkillUse, Visibility: grading.VisibilityPublic,
			SkillUse: &grading.CountRule{EvidenceID: "use", Minimum: 2, Maximum: 3}},
		{ID: "13-budget", Kind: grading.CheckBudget, Visibility: grading.VisibilityPublic,
			Budget: &grading.BudgetRule{EvidenceID: "tokens", Minimum: 50, Maximum: 100}},
		{ID: "14-policy", Kind: grading.CheckPolicy, Visibility: grading.VisibilityHidden,
			Policy: &grading.PolicyRule{EvidenceID: "violations", MaximumViolations: 0}},
	}
	plan := grading.Plan{Schema: grading.PlanSchema, SchemaVersion: 1, ContractVersion: grading.ContractVersion, ContractSHA256: contractSHA,
		Mode: grading.ModeDeterministic, InputProjectionSHA256: inputSHA, EnvironmentSHA256: environmentSHA, Checks: checks,
		Limits: grading.PlanLimits{DeadlineMillis: 1000, MaxInputBytes: grading.MaxEvidenceBytes, MaxOutputBytes: grading.MaxReceiptBytes}}
	return contract, plan
}

func mechanicalEvidence() grading.EvidenceSet {
	return grading.EvidenceSet{InputProjectionSHA256: inputSHA,
		Files: []grading.FileEvidence{
			{ID: "file-a", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o644, Data: []byte("hello world\n")},
			{ID: "json", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o644, Data: []byte(`{"count":2}`)},
		},
		Commands: []grading.CommandEvidence{{ID: "command", Visibility: grading.VisibilityPublic, ExitCode: 0, Stdout: []byte("hello world\n"), Stderr: []byte{}}},
		Trees: []grading.TreeEvidence{{ID: "tree", Visibility: grading.VisibilityHidden,
			Changes: []grading.TreeChangeExpectation{{Path: "answer.txt", Kind: grading.TreeAdded, SHA256: fileSHA}}}},
		Sequences: []grading.SequenceEvidence{
			{ID: "actions", Visibility: grading.VisibilityPublic, Values: []string{"inspect", "other", "verify"}},
			{ID: "tools", Visibility: grading.VisibilityPublic, Values: []string{"read", "write"}},
		},
		Counters: []grading.CounterEvidence{
			{ID: "activation", Visibility: grading.VisibilityPublic, Value: 1},
			{ID: "tokens", Visibility: grading.VisibilityPublic, Value: 99},
			{ID: "use", Visibility: grading.VisibilityPublic, Value: 2},
			{ID: "violations", Visibility: grading.VisibilityHidden, Value: 0},
		}}
}

func deterministicOneCheckFixture(t *testing.T) (grading.Contract, grading.Plan) {
	t.Helper()
	contract := builtinContract(t)
	contractSHA, _ := grading.ContractSHA256(contract)
	return contract, grading.Plan{Schema: grading.PlanSchema, SchemaVersion: 1, ContractVersion: grading.ContractVersion,
		ContractSHA256: contractSHA, Mode: grading.ModeDeterministic, InputProjectionSHA256: inputSHA, EnvironmentSHA256: environmentSHA,
		Checks: []grading.Check{{ID: "mechanical", Kind: grading.CheckFileSHA256, Visibility: grading.VisibilityHidden,
			FileSHA256: &grading.FileSHA256Rule{EvidenceID: "proof", ExpectedSHA256: fileSHA}}},
		Limits: grading.PlanLimits{DeadlineMillis: 1000, MaxInputBytes: grading.MaxEvidenceBytes, MaxOutputBytes: grading.MaxReceiptBytes}}
}

func judgeFixture(t *testing.T, contract grading.Contract) grading.Plan {
	t.Helper()
	contractSHA, _ := grading.ContractSHA256(contract)
	return grading.Plan{Schema: grading.PlanSchema, SchemaVersion: 1, ContractVersion: grading.ContractVersion,
		ContractSHA256: contractSHA, Mode: grading.ModeJudgeAssessment, InputProjectionSHA256: inputSHA, EnvironmentSHA256: environmentSHA,
		Checks: []grading.Check{{ID: "quality", Kind: grading.CheckQualitative, Visibility: grading.VisibilityHidden,
			Qualitative: &grading.QualitativeRule{RubricCriterionID: "quality", EvidenceIDs: []string{"proof"}}}},
		Judge: &grading.JudgePolicy{RubricSHA256: strings.Repeat("3", 64), PromptContractSHA256: strings.Repeat("7", 64),
			BlindAssignmentSHA256: strings.Repeat("4", 64), ToolPolicy: "none",
			Reviewers: []grading.Reviewer{
				{ID: "human-a", Kind: grading.ReviewerHuman},
				{ID: "model-a", Kind: grading.ReviewerModel, Model: "model-a", EnvironmentSHA256: strings.Repeat("5", 64),
					MaxInputTokens: 100, MaxOutputTokens: 100, MaxEstimatedCostMicroUSD: 100},
				{ID: "model-b", Kind: grading.ReviewerModel, Model: "model-b", EnvironmentSHA256: strings.Repeat("6", 64),
					MaxInputTokens: 100, MaxOutputTokens: 100, MaxEstimatedCostMicroUSD: 100},
			}},
		Limits: grading.PlanLimits{DeadlineMillis: 1000, MaxInputBytes: grading.MaxEvidenceBytes, MaxOutputBytes: grading.MaxReceiptBytes}}
}

func reviewFixture(id string, passed bool, citation grading.Citation, evidenceSHA string, tokens uint64) grading.Review {
	usage := grading.Usage{InputTokens: grading.MetricPresence{Presence: grading.PresenceObserved, Value: tokens},
		OutputTokens:          grading.MetricPresence{Presence: grading.PresenceObserved, Value: 1},
		EstimatedCostMicroUSD: grading.MetricPresence{Presence: grading.PresenceObserved, Value: 1},
		DurationMillis:        grading.MetricPresence{Presence: grading.PresenceObserved, Value: 1}}
	if strings.HasPrefix(id, "human") {
		usage = grading.Usage{InputTokens: grading.MetricPresence{Presence: grading.PresenceNotApplicable},
			OutputTokens:          grading.MetricPresence{Presence: grading.PresenceNotApplicable},
			EstimatedCostMicroUSD: grading.MetricPresence{Presence: grading.PresenceNotApplicable},
			DurationMillis:        grading.MetricPresence{Presence: grading.PresenceNotApplicable}}
	}
	return grading.Review{ReviewerID: id, RubricSHA256: strings.Repeat("3", 64), PromptContractSHA256: strings.Repeat("7", 64),
		BlindAssignmentSHA256:    strings.Repeat("4", 64),
		EvidenceProjectionSHA256: evidenceSHA, Decisions: []grading.ReviewDecision{{CheckID: "quality", Passed: passed,
			Citations: []grading.Citation{citation}}}, Usage: usage}
}

func referenceBackend(t *testing.T) executionbackend.Contract {
	t.Helper()
	contract, err := executionbackend.ReferenceContract()
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func decisionByID(t *testing.T, receipt grading.Receipt, id string) grading.Decision {
	t.Helper()
	for _, decision := range receipt.Decisions {
		if decision.CheckID == id {
			return decision
		}
	}
	t.Fatalf("missing decision %q", id)
	return grading.Decision{}
}

func assertClosedWire(t *testing.T, family string, data []byte, decode func([]byte) error) {
	t.Helper()
	for name, mutation := range map[string][]byte{
		"future":    bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":   bytes.Replace(data, []byte(`,"schema_version"`), []byte(`,"extra":true,"schema_version"`), 1),
		"duplicate": bytes.Replace(data, []byte(`,"schema_version"`), []byte(`,"schema":"duplicate","schema_version"`), 1),
		"trailing":  append(append([]byte{}, data...), []byte("{}\n")...),
	} {
		t.Run(family+"-"+name, func(t *testing.T) {
			if err := decode(mutation); err == nil {
				t.Fatal("accepted invalid wire")
			}
		})
	}
}
