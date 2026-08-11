// Package lifecycle owns the neutral, append-only attempt state contract.
package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
)

const (
	LedgerSchema    = "agent-eval/attempt-ledger"
	PlanSchema      = "agent-eval/attempt-plan"
	EventSchema     = "agent-eval/attempt-event"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxHeaderBytes = 16 << 10
	MaxPlanBytes   = 64 << 10
	MaxEventBytes  = 64 << 10
	MaxAttempts    = 4096
	MaxEvents      = 256
)

var ErrContract = errors.New("attempt_ledger_contract_invalid")

type State string

const (
	StateCanceled     State = "canceled"
	StateCommitted    State = "committed"
	StateFailed       State = "failed"
	StatePlanned      State = "planned"
	StatePolicyDenied State = "policy_denied"
	StateRunning      State = "running"
	StateSpawning     State = "spawning"
	StateSucceeded    State = "succeeded"
	StateTimedOut     State = "timed_out"
	StateUnknown      State = "unknown"
	StateUnsupported  State = "unsupported"
)

type Proof string

const (
	ProofCompleteLedger           Proof = "complete_ledger"
	ProofDefinitiveSpawnFailure   Proof = "definitive_spawn_failure"
	ProofDurableCancel            Proof = "durable_cancel"
	ProofDurableCapabilityRefusal Proof = "durable_capability_refusal"
	ProofDurableCommit            Proof = "durable_commit"
	ProofDurableDeadline          Proof = "durable_deadline"
	ProofDurablePolicyRefusal     Proof = "durable_policy_refusal"
	ProofDurableProcessIdentity   Proof = "durable_process_identity"
	ProofDurableSpawnIntent       Proof = "durable_spawn_intent"
	ProofImmutablePlan            Proof = "immutable_plan"
	ProofIncompleteTerminal       Proof = "incomplete_terminal_evidence"
	ProofNoCommit                 Proof = "no_commit"
	ProofNonExecution             Proof = "non_execution_proof"
	ProofTerminalReceipt          Proof = "terminal_receipt"
	ProofTermination              Proof = "termination_proof"
)

const (
	PrivacyPublic           = "public"
	PrivacyContentMinimized = "content_minimized"
	PrivacyOwnerPrivate     = "owner_private"

	MetricUnknown  = "unknown"
	MetricObserved = "observed"
)

type transitionKey struct {
	from State
	to   State
}

var transitionProofSets = map[transitionKey][][]Proof{
	{StateCommitted, StateCanceled}:   {{ProofDurableCancel, ProofNonExecution}},
	{StateCommitted, StateFailed}:     {{ProofDefinitiveSpawnFailure, ProofNonExecution}},
	{StateCommitted, StateSpawning}:   {{ProofDurableSpawnIntent}},
	{StateCommitted, StateTimedOut}:   {{ProofDurableDeadline, ProofNonExecution}},
	{StateCommitted, StateUnknown}:    {{ProofIncompleteTerminal}},
	{StatePlanned, StateCanceled}:     {{ProofCompleteLedger, ProofDurableCancel, ProofNoCommit}},
	{StatePlanned, StateCommitted}:    {{ProofDurableCommit}},
	{StatePlanned, StatePolicyDenied}: {{ProofCompleteLedger, ProofDurablePolicyRefusal, ProofNoCommit}},
	{StatePlanned, StateTimedOut}:     {{ProofCompleteLedger, ProofDurableDeadline, ProofNoCommit}},
	{StatePlanned, StateUnknown}:      {{ProofIncompleteTerminal}},
	{StatePlanned, StateUnsupported}:  {{ProofCompleteLedger, ProofDurableCapabilityRefusal, ProofNoCommit}},
	{StateRunning, StateCanceled}:     {{ProofDurableCancel, ProofTermination}},
	{StateRunning, StateFailed}:       {{ProofTerminalReceipt, ProofTermination}},
	{StateRunning, StateSucceeded}:    {{ProofTerminalReceipt, ProofTermination}},
	{StateRunning, StateTimedOut}:     {{ProofDurableDeadline, ProofTermination}},
	{StateRunning, StateUnknown}:      {{ProofIncompleteTerminal}},
	{StateSpawning, StateCanceled}:    {{ProofDurableCancel, ProofNonExecution}, {ProofDurableCancel, ProofTermination}},
	{StateSpawning, StateFailed}:      {{ProofDefinitiveSpawnFailure, ProofNonExecution}, {ProofTerminalReceipt, ProofTermination}},
	{StateSpawning, StateRunning}:     {{ProofDurableProcessIdentity}},
	{StateSpawning, StateSucceeded}:   {{ProofTerminalReceipt, ProofTermination}},
	{StateSpawning, StateTimedOut}:    {{ProofDurableDeadline, ProofNonExecution}, {ProofDurableDeadline, ProofTermination}},
	{StateSpawning, StateUnknown}:     {{ProofIncompleteTerminal}},
}

var terminalStates = map[State]bool{
	StateCanceled: true, StateFailed: true, StatePolicyDenied: true, StateSucceeded: true,
	StateTimedOut: true, StateUnknown: true, StateUnsupported: true,
}

type LedgerHeader struct {
	Schema          string `json:"schema"`
	SchemaVersion   int    `json:"schema_version"`
	ContractVersion string `json:"contract_version"`
	LedgerID        string `json:"ledger_id"`
	HeaderSHA256    string `json:"header_sha256"`
}

type Identity struct {
	ExperimentSHA256  string `json:"experiment_sha256"`
	TaskSHA256        string `json:"task_sha256"`
	SkillSHA256       string `json:"skill_sha256"`
	AgentSHA256       string `json:"agent_sha256"`
	ModelSHA256       string `json:"model_sha256"`
	EnvironmentSHA256 string `json:"environment_sha256"`
	GraderSHA256      string `json:"grader_sha256"`
	BudgetsSHA256     string `json:"budgets_sha256"`
	AdapterSHA256     string `json:"adapter_sha256"`
	AuthoritySHA256   string `json:"authority_sha256"`
}

type Binding struct {
	Privacy  string   `json:"privacy"`
	Identity Identity `json:"identity"`
}

type Plan struct {
	Schema               string  `json:"schema"`
	SchemaVersion        int     `json:"schema_version"`
	LedgerID             string  `json:"ledger_id"`
	AttemptID            string  `json:"attempt_id"`
	Ordinal              uint32  `json:"ordinal"`
	PredecessorAttemptID string  `json:"predecessor_attempt_id,omitempty"`
	ReconciliationSHA256 string  `json:"reconciliation_sha256,omitempty"`
	Binding              Binding `json:"binding"`
	BindingSHA256        string  `json:"binding_sha256"`
	PlanSHA256           string  `json:"plan_sha256"`
}

type Metric struct {
	State string  `json:"state"`
	Value *uint64 `json:"value,omitempty"`
}

type Usage struct {
	EstimatedCostMicroUSD Metric `json:"estimated_cost_microusd"`
	InputTokens           Metric `json:"input_tokens"`
	OutputTokens          Metric `json:"output_tokens"`
}

type Evidence struct {
	ProcessIdentitySHA256 string `json:"process_identity_sha256,omitempty"`
	ReceiptSHA256         string `json:"receipt_sha256,omitempty"`
	ErrorClass            string `json:"error_class,omitempty"`
	Usage                 Usage  `json:"usage"`
}

type Event struct {
	Schema         string   `json:"schema"`
	SchemaVersion  int      `json:"schema_version"`
	LedgerID       string   `json:"ledger_id"`
	AttemptID      string   `json:"attempt_id"`
	PlanSHA256     string   `json:"plan_sha256"`
	Sequence       uint32   `json:"sequence"`
	PreviousSHA256 string   `json:"previous_sha256,omitempty"`
	From           State    `json:"from"`
	To             State    `json:"to"`
	Proofs         []Proof  `json:"proofs"`
	Evidence       Evidence `json:"evidence"`
	EventSHA256    string   `json:"event_sha256"`
}

type Projection struct {
	State         State
	Terminal      bool
	Sequence      uint32
	LastSHA256    string
	Usage         Usage
	ProcessSHA256 string
	ReceiptSHA256 string
}

func NewHeader(random io.Reader) (LedgerHeader, error) {
	if random == nil {
		return LedgerHeader{}, contractError("random")
	}
	var nonce [32]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		return LedgerHeader{}, contractError("random", err)
	}
	header := LedgerHeader{Schema: LedgerSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion}
	header.LedgerID = hashDomain("ledger-id", nonce[:])
	digest, err := digestHeader(header)
	if err != nil {
		return LedgerHeader{}, err
	}
	header.HeaderSHA256 = digest
	return header, nil
}

func NewPlan(header LedgerHeader, ordinal uint32, binding Binding) (Plan, error) {
	return newPlan(header, ordinal, "", "", binding)
}

func NewReconciledPlan(header LedgerHeader, ordinal uint32, predecessorAttemptID, reconciliationSHA256 string, binding Binding) (Plan, error) {
	if !validSHA256(predecessorAttemptID) || !validSHA256(reconciliationSHA256) {
		return Plan{}, contractError("reconciliation")
	}
	return newPlan(header, ordinal, predecessorAttemptID, reconciliationSHA256, binding)
}

func newPlan(header LedgerHeader, ordinal uint32, predecessorAttemptID, reconciliationSHA256 string, binding Binding) (Plan, error) {
	if err := ValidateHeader(header); err != nil || ordinal == 0 || ordinal > MaxAttempts || validateBinding(binding) != nil {
		return Plan{}, contractError("plan")
	}
	bindingDigest, err := digestBinding(binding)
	if err != nil {
		return Plan{}, err
	}
	attemptMaterial := []byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", header.LedgerID, ordinal, bindingDigest, predecessorAttemptID, reconciliationSHA256))
	plan := Plan{Schema: PlanSchema, SchemaVersion: SchemaVersion, LedgerID: header.LedgerID,
		AttemptID: hashDomain("attempt-id", attemptMaterial), Ordinal: ordinal, PredecessorAttemptID: predecessorAttemptID,
		ReconciliationSHA256: reconciliationSHA256, Binding: binding, BindingSHA256: bindingDigest}
	planDigest, err := digestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanSHA256 = planDigest
	return plan, nil
}

func UnknownUsage() Usage {
	return Usage{EstimatedCostMicroUSD: Metric{State: MetricUnknown}, InputTokens: Metric{State: MetricUnknown}, OutputTokens: Metric{State: MetricUnknown}}
}

func ObservedMetric(value uint64) Metric {
	return Metric{State: MetricObserved, Value: &value}
}

func ValidateHeader(header LedgerHeader) error {
	if header.Schema != LedgerSchema || header.SchemaVersion != SchemaVersion || header.ContractVersion != ContractVersion ||
		!validSHA256(header.LedgerID) || !validSHA256(header.HeaderSHA256) {
		return contractError("header")
	}
	digest, err := digestHeader(header)
	if err != nil || digest != header.HeaderSHA256 {
		return contractError("header_digest", err)
	}
	return nil
}

func ValidatePlan(plan Plan) error {
	if plan.Schema != PlanSchema || plan.SchemaVersion != SchemaVersion || !validSHA256(plan.LedgerID) ||
		!validSHA256(plan.AttemptID) || plan.Ordinal == 0 || plan.Ordinal > MaxAttempts || validateBinding(plan.Binding) != nil ||
		((plan.PredecessorAttemptID == "") != (plan.ReconciliationSHA256 == "")) ||
		(plan.PredecessorAttemptID != "" && (!validSHA256(plan.PredecessorAttemptID) || !validSHA256(plan.ReconciliationSHA256))) ||
		!validSHA256(plan.BindingSHA256) || !validSHA256(plan.PlanSHA256) {
		return contractError("plan")
	}
	bindingDigest, err := digestBinding(plan.Binding)
	if err != nil || bindingDigest != plan.BindingSHA256 {
		return contractError("binding_digest", err)
	}
	wantAttempt := hashDomain("attempt-id", []byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", plan.LedgerID, plan.Ordinal,
		bindingDigest, plan.PredecessorAttemptID, plan.ReconciliationSHA256)))
	if plan.AttemptID != wantAttempt {
		return contractError("attempt_id")
	}
	planDigest, err := digestPlan(plan)
	if err != nil || planDigest != plan.PlanSHA256 {
		return contractError("plan_digest", err)
	}
	return nil
}

func IsTerminal(state State) bool { return terminalStates[state] }

func States() []State {
	return []State{StateCanceled, StateCommitted, StateFailed, StatePlanned, StatePolicyDenied, StateRunning,
		StateSpawning, StateSucceeded, StateTimedOut, StateUnknown, StateUnsupported}
}

func Proofs() []Proof {
	return []Proof{ProofCompleteLedger, ProofDefinitiveSpawnFailure, ProofDurableCancel, ProofDurableCapabilityRefusal,
		ProofDurableCommit, ProofDurableDeadline, ProofDurablePolicyRefusal, ProofDurableProcessIdentity,
		ProofDurableSpawnIntent, ProofImmutablePlan, ProofIncompleteTerminal, ProofNoCommit, ProofNonExecution,
		ProofTerminalReceipt, ProofTermination}
}

func AllowedProofSets(from, to State) [][]Proof {
	sets := transitionProofSets[transitionKey{from: from, to: to}]
	copySets := make([][]Proof, len(sets))
	for index := range sets {
		copySets[index] = append([]Proof(nil), sets[index]...)
	}
	return copySets
}

func validTransitionProofs(from, to State, proofs []Proof) bool {
	sets := transitionProofSets[transitionKey{from: from, to: to}]
	for _, set := range sets {
		if equalProofs(set, proofs) {
			return true
		}
	}
	return false
}

func validateBinding(binding Binding) error {
	if binding.Privacy != PrivacyPublic && binding.Privacy != PrivacyContentMinimized && binding.Privacy != PrivacyOwnerPrivate {
		return contractError("privacy")
	}
	digests := []string{binding.Identity.ExperimentSHA256, binding.Identity.TaskSHA256, binding.Identity.SkillSHA256,
		binding.Identity.AgentSHA256, binding.Identity.ModelSHA256, binding.Identity.EnvironmentSHA256,
		binding.Identity.GraderSHA256, binding.Identity.BudgetsSHA256, binding.Identity.AdapterSHA256,
		binding.Identity.AuthoritySHA256}
	for _, digest := range digests {
		if !validSHA256(digest) {
			return contractError("identity")
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashDomain(domain string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/" + domain + "/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func equalProofs(left, right []Proof) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedUniqueProofs(proofs []Proof) bool {
	return sort.SliceIsSorted(proofs, func(i, j int) bool { return proofs[i] < proofs[j] }) &&
		!hasDuplicateProof(proofs)
}

func hasDuplicateProof(proofs []Proof) bool {
	for index := 1; index < len(proofs); index++ {
		if proofs[index] == proofs[index-1] {
			return true
		}
	}
	return false
}

func contractError(code string, causes ...error) error {
	err := fmt.Errorf("%w: %s", ErrContract, code)
	for _, cause := range causes {
		if cause != nil {
			return errors.Join(err, cause)
		}
	}
	return err
}
