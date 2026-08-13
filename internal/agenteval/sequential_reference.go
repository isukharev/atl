package agenteval

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

var (
	ErrSequentialReference            = errors.New("sequential_reference_invalid")
	ErrSequentialReferenceUnsupported = errors.New("sequential_reference_unsupported")
)

// SequentialReferenceTreatment supplies the already-authored execution plan
// and canonical input snapshots for one compiled treatment. The coordinator
// never reconstructs treatments or changes their immutable ordering.
type SequentialReferenceTreatment struct {
	TreatmentID string                          `json:"treatment_id"`
	Plan        ExecutionBackendTrialPlan       `json:"plan"`
	Inputs      ExecutionBackendReferenceInputs `json:"inputs"`
}

// SequentialReferenceBundle contains only the fixed public reference inputs.
// Input archive bytes are consumed in memory and never enter the publication.
type SequentialReferenceBundle struct {
	Schema          string                         `json:"schema"`
	SchemaVersion   int                            `json:"schema_version"`
	ContractVersion string                         `json:"contract_version"`
	ManifestSHA256  string                         `json:"manifest_sha256"`
	GradingPlan     GradingPlan                    `json:"grading_plan"`
	Treatments      []SequentialReferenceTreatment `json:"treatments"`
}

// SequentialReferenceTrialArtifacts is the canonical content-minimized chain
// for one immutable manifest assignment. Raw backend artifacts are excluded.
type SequentialReferenceTrialArtifacts struct {
	AttemptPlan      lifecycle.Plan                `json:"attempt_plan"`
	Observation      *AgentAdapterObservation      `json:"observation,omitempty"`
	ExecutionPlan    ExecutionBackendTrialPlan     `json:"execution_plan"`
	ExecutionReceipt *ExecutionBackendTrialReceipt `json:"execution_receipt,omitempty"`
	GradingPlan      GradingPlan                   `json:"grading_plan"`
	GradeReceipt     *GradeReceipt                 `json:"grade_receipt,omitempty"`
	LifecycleEvent   lifecycle.Event               `json:"lifecycle_event"`
	TrialRecord      ExperimentTrialRecord         `json:"trial_record"`
}

// SequentialReferenceResult preserves manifest order and publication-safe
// identities only. It has no raw prompt, output, archive, path, or diagnostic.
type SequentialReferenceResult struct {
	ManifestSHA256 string                              `json:"manifest_sha256"`
	Trials         []SequentialReferenceTrialArtifacts `json:"trials"`
}

type sequentialReferenceTreatment struct {
	plan     executionbackend.Plan
	admitted executionbackend.AdmittedPlan
	inputs   executionbackend.ReferenceInputs
}

type sequentialReferencePrepared struct {
	manifest        experiment.Manifest
	adapter         agentadapter.Contract
	grader          grading.Contract
	gradingPlan     grading.Plan
	admittedGrading grading.AdmittedPlan
	treatments      map[string]*sequentialReferenceTreatment
}

// CreateSequentialReferenceAttemptStore creates a deterministic ledger for a
// clean publication. The manifest digest supplies the fixed 32-byte ledger
// nonce, so identical declared inputs yield identical attempt identities.
func CreateSequentialReferenceAttemptStore(root string, manifest ExperimentManifest) (*AttemptLedgerStore, error) {
	if err := experiment.ValidateManifest(manifest); err != nil {
		return nil, sequentialReferenceError("manifest", err)
	}
	nonce, err := hex.DecodeString(manifest.ManifestSHA256)
	if err != nil || len(nonce) != 32 {
		return nil, sequentialReferenceError("manifest_identity", err)
	}
	return CreateAttemptLedgerStore(root, bytes.NewReader(nonce))
}

// RunSequentialReference executes the fully pre-admitted roster one attempt at
// a time. No attempt is committed until every plan and input snapshot has been
// cloned, decoded, bounded, and identity-checked.
func RunSequentialReference(ctx context.Context, store *AttemptLedgerStore, manifest ExperimentManifest, bundle SequentialReferenceBundle) (SequentialReferenceResult, error) {
	prepared, err := prepareSequentialReference(ctx, manifest, bundle)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer prepared.destroy()
	return prepared.run(ctx, store, nil)
}

func prepareSequentialReference(ctx context.Context, manifest experiment.Manifest, bundle SequentialReferenceBundle) (*sequentialReferencePrepared, error) {
	if ctx == nil {
		return nil, sequentialReferenceError("context", nil)
	}
	if err := validateSequentialReferenceBundleShape(bundle); err != nil {
		return nil, err
	}
	manifestData, err := experiment.EncodeManifest(manifest)
	if err != nil {
		return nil, sequentialReferenceError("manifest", err)
	}
	ownedManifest, err := experiment.DecodeManifest(bytes.NewReader(manifestData))
	if err != nil || bundle.ManifestSHA256 != ownedManifest.ManifestSHA256 || ownedManifest.Design.Case.SourceKind != experiment.SourceAgentSkills ||
		ownedManifest.Design.CompatibilityProfile != experiment.CompatibilityNone || !ownedManifest.PositionBalanceComplete {
		return nil, sequentialReferenceError("manifest_profile", err)
	}
	wantCapability, err := SequentialReferenceExperimentCapabilityContract()
	if err != nil || !reflect.DeepEqual(ownedManifest.CapabilityContract, wantCapability) {
		return nil, unsupportedSequentialReference("capability_contract", err)
	}
	if !sequentialReferenceAnalysisSupported(ownedManifest.AnalysisPlan) || !sequentialReferenceTreatmentsSupported(ownedManifest.Design.Treatments) {
		return nil, unsupportedSequentialReference("experiment_profile", nil)
	}
	adapter, err := agentadapter.ReferenceContract()
	if err != nil {
		return nil, sequentialReferenceError("adapter_contract", err)
	}
	grader, err := grading.BuiltinContract()
	if err != nil {
		return nil, sequentialReferenceError("grader_contract", err)
	}
	gradingData, err := grading.EncodePlan(bundle.GradingPlan)
	if err != nil {
		return nil, sequentialReferenceError("grading_plan", err)
	}
	gradingPlan, err := grading.DecodePlan(bytes.NewReader(gradingData))
	if err != nil {
		return nil, sequentialReferenceError("grading_plan", err)
	}
	gradingSHA, err := grading.PlanSHA256(gradingPlan)
	if err != nil || gradingSHA != ownedManifest.Design.Case.GradingPlanSHA256 {
		return nil, sequentialReferenceError("grading_binding", err)
	}
	wantGrading, err := NewSequentialReferenceGradingPlan(gradingPlan.InputProjectionSHA256)
	if err != nil || !reflect.DeepEqual(gradingPlan, wantGrading) {
		return nil, unsupportedSequentialReference("grading_profile", err)
	}
	admittedGrading, err := grading.Admit(grader, gradingPlan)
	if err != nil {
		return nil, sequentialReferenceError("grading_admission", err)
	}
	if len(bundle.Treatments) != len(ownedManifest.Treatments) {
		return nil, sequentialReferenceError("treatment_count", nil)
	}
	prepared := &sequentialReferencePrepared{
		manifest: ownedManifest, adapter: adapter, grader: grader, gradingPlan: gradingPlan, admittedGrading: admittedGrading,
		treatments: make(map[string]*sequentialReferenceTreatment, len(bundle.Treatments)),
	}
	failed := true
	defer func() {
		if failed {
			prepared.destroy()
		}
	}()
	backend, err := executionbackend.ReferenceContract()
	if err != nil {
		return nil, sequentialReferenceError("execution_contract", err)
	}
	for index, treatmentInput := range bundle.Treatments {
		if treatmentInput.TreatmentID != ownedManifest.Treatments[index].ID ||
			(index > 0 && bundle.Treatments[index-1].TreatmentID >= treatmentInput.TreatmentID) {
			return nil, sequentialReferenceError("treatment_order", nil)
		}
		planData, encodeErr := executionbackend.EncodePlan(treatmentInput.Plan)
		if encodeErr != nil {
			return nil, sequentialReferenceError("execution_plan", encodeErr)
		}
		plan, decodeErr := executionbackend.DecodePlan(bytes.NewReader(planData))
		if decodeErr != nil {
			return nil, sequentialReferenceError("execution_plan", decodeErr)
		}
		admitted, admitErr := executionbackend.Admit(backend, plan)
		if admitErr != nil {
			if errors.Is(admitErr, executionbackend.ErrUnsupported) {
				return nil, unsupportedSequentialReference("execution_plan", admitErr)
			}
			return nil, sequentialReferenceError("execution_admission", admitErr)
		}
		if admitted.SHA256() != ownedManifest.Treatments[index].ExecutionBindingSHA256 || !sequentialReferenceExecutionPlanSupported(plan) {
			return nil, unsupportedSequentialReference("execution_profile", nil)
		}
		ownedInputs, prepareErr := executionbackend.PrepareReferenceInputs(ctx, admitted, treatmentInput.Inputs)
		if prepareErr != nil {
			return nil, sequentialReferenceError("execution_inputs", prepareErr)
		}
		prepared.treatments[treatmentInput.TreatmentID] = &sequentialReferenceTreatment{plan: plan, admitted: admitted, inputs: ownedInputs}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, sequentialReferenceError("preflight_interrupted", err)
	}
	failed = false
	return prepared, nil
}

func (prepared *sequentialReferencePrepared) run(ctx context.Context, store *AttemptLedgerStore,
	sink func(int, SequentialReferenceTrialArtifacts) error,
) (SequentialReferenceResult, error) {
	if store == nil {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_store", nil)
	}
	wantHeader, err := sequentialReferenceLedgerHeader(prepared.manifest.ManifestSHA256)
	if err != nil || store.Header().HeaderSHA256 != wantHeader.HeaderSHA256 {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_store_identity", err)
	}
	bindings, err := ExperimentAttemptBindings(prepared.manifest)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_bindings", err)
	}
	assignments := sequentialReferenceAssignments(prepared.manifest)
	if len(bindings) != len(assignments) {
		return SequentialReferenceResult{}, sequentialReferenceError("assignment_count", nil)
	}
	for index, assignment := range assignments {
		treatment := prepared.treatments[assignment.TreatmentID]
		if treatment == nil {
			return SequentialReferenceResult{}, sequentialReferenceError("assignment_treatment", nil)
		}
		bindings[index], err = BindExecutionBackendTrial(bindings[index], treatment.plan)
		if err == nil {
			bindings[index], err = BindGradingPlan(bindings[index], prepared.gradingPlan)
		}
		if err != nil {
			return SequentialReferenceResult{}, sequentialReferenceError("attempt_binding", err)
		}
	}
	plans, err := store.EnsureRoster(bindings)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_roster", err)
	}
	result := SequentialReferenceResult{ManifestSHA256: prepared.manifest.ManifestSHA256, Trials: make([]SequentialReferenceTrialArtifacts, 0, len(plans))}
	for index, plan := range plans {
		session, sessionErr := NewDurableAttemptSession(store, plan)
		if sessionErr != nil {
			return result, sequentialReferenceError("attempt_session", sessionErr)
		}
		artifacts, runErr := prepared.runTrial(ctx, session, assignments[index])
		if artifacts.TrialRecord.RecordSHA256 != "" {
			result.Trials = append(result.Trials, artifacts)
			if sink != nil {
				if sinkErr := sink(index, artifacts); sinkErr != nil {
					return result, sequentialReferenceError("artifact_sink", sinkErr)
				}
			}
		}
		if runErr != nil {
			return result, runErr
		}
	}
	return result, nil
}

type sequentialReferenceAssignment struct {
	TrialID     string
	BlockID     string
	TreatmentID string
}

func (prepared *sequentialReferencePrepared) runTrial(ctx context.Context, session *DurableAttemptSession, assignment sequentialReferenceAssignment) (SequentialReferenceTrialArtifacts, error) {
	treatment := prepared.treatments[assignment.TreatmentID]
	artifacts := SequentialReferenceTrialArtifacts{AttemptPlan: session.Plan(), ExecutionPlan: treatment.plan, GradingPlan: prepared.gradingPlan}
	usage := sequentialReferenceLifecycleUsage()
	if err := beginRunAttempt(session); err != nil {
		return artifacts, sequentialReferenceError("attempt_commit", err)
	}
	backendResult, runErr := executionbackend.RunReference(ctx, treatment.admitted, treatment.inputs)
	clearResult := func() {
		for index := range backendResult.Artifacts {
			clear(backendResult.Artifacts[index].Data)
			backendResult.Artifacts[index].Data = nil
		}
	}
	defer clearResult()
	if backendResult.Receipt.Schema != "" {
		if _, err := executionbackend.EncodeReceipt(treatment.plan, backendResult.Receipt); err == nil {
			receipt := backendResult.Receipt
			artifacts.ExecutionReceipt = &receipt
		}
	}
	if runErr != nil {
		state, exclusion, lifecycleErr := terminalizeSequentialReferenceInterruption(ctx, session, runErr, usage)
		if lifecycleErr != nil {
			return artifacts, sequentialReferenceError("attempt_terminal", errors.Join(runErr, lifecycleErr))
		}
		sealed, recordErr := prepared.sealIncompleteTrial(session, assignment, state, exclusion, "")
		if recordErr != nil {
			return artifacts, sequentialReferenceError("trial_record", errors.Join(runErr, recordErr))
		}
		artifacts.TrialRecord, artifacts.LifecycleEvent = sealed.record, sealed.event
		return artifacts, sequentialReferenceError("execution", runErr)
	}
	executionReceiptData, err := executionbackend.EncodeReceipt(treatment.plan, backendResult.Receipt)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("execution_receipt", err))
	}
	manifestTreatment, ok := sequentialReferenceTreatmentByID(prepared.manifest, assignment.TreatmentID)
	if !ok {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("treatment", nil))
	}
	observation, err := agentadapter.NewReferenceObservation(prepared.adapter, session.Plan().AttemptID, manifestTreatment.ExpectedActivation)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("observation", err))
	}
	observationSHA, err := agentadapter.ObservationSHA256(prepared.adapter, observation)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("observation_identity", err))
	}
	artifacts.Observation = &observation
	evidence, err := grading.PrepareEvidence(ctx, prepared.admittedGrading, grading.EvidenceSet{
		InputProjectionSHA256: prepared.gradingPlan.InputProjectionSHA256,
		Files: []grading.FileEvidence{{
			ID: "execution-receipt", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o600, Data: executionReceiptData,
		}},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{},
	})
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, observationSHA, sequentialReferenceError("grading_evidence", err))
	}
	defer evidence.Destroy()
	gradeReceipt, err := grading.EvaluateDeterministic(ctx, prepared.admittedGrading, evidence)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, observationSHA, sequentialReferenceError("grading", err))
	}
	gradeSHA, err := grading.ReceiptSHA256(prepared.gradingPlan, gradeReceipt)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, observationSHA, sequentialReferenceError("grading_receipt", err))
	}
	artifacts.GradeReceipt = &gradeReceipt
	passed := backendResult.Receipt.Verdict == executionbackend.VerdictSucceeded && sequentialReferenceGradePassed(gradeReceipt)
	if passed {
		err = session.Succeed(gradeSHA, usage)
	} else {
		err = session.Fail(gradeSHA, usage)
	}
	if err != nil {
		return artifacts, sequentialReferenceError("attempt_terminal", err)
	}
	sealed, err := prepared.sealSupportedTrial(session, assignment, observationSHA, gradeSHA, passed, observation)
	if err != nil {
		return artifacts, sequentialReferenceError("trial_record", err)
	}
	artifacts.TrialRecord, artifacts.LifecycleEvent = sealed.record, sealed.event
	return artifacts, nil
}

type sealedSequentialReferenceTrial struct {
	record experiment.TrialRecord
	event  lifecycle.Event
}

func (prepared *sequentialReferencePrepared) failPostExecution(session *DurableAttemptSession, assignment sequentialReferenceAssignment,
	artifacts SequentialReferenceTrialArtifacts, observationSHA string, cause error,
) (SequentialReferenceTrialArtifacts, error) {
	if err := session.Unknown(lifecycle.ErrorInternal, sequentialReferenceLifecycleUsage()); err != nil {
		return artifacts, sequentialReferenceError("attempt_unknown", errors.Join(cause, err))
	}
	sealed, err := prepared.sealIncompleteTrial(session, assignment, experiment.LifecycleUnknown, experiment.ExclusionLifecycleUnknown, observationSHA)
	if err != nil {
		return artifacts, sequentialReferenceError("trial_record", errors.Join(cause, err))
	}
	artifacts.TrialRecord, artifacts.LifecycleEvent = sealed.record, sealed.event
	return artifacts, cause
}

func (prepared *sequentialReferencePrepared) sealSupportedTrial(session *DurableAttemptSession, assignment sequentialReferenceAssignment,
	observationSHA, gradeSHA string, passed bool, observation agentadapter.Observation,
) (sealedSequentialReferenceTrial, error) {
	event, err := sequentialReferenceTerminalEvent(session)
	if err != nil {
		return sealedSequentialReferenceTrial{}, err
	}
	state := experiment.LifecycleFailed
	if passed {
		state = experiment.LifecycleSucceeded
	}
	stages, metrics, err := sequentialReferenceObservedProjections(prepared.manifest, observation, passed)
	if err != nil {
		return sealedSequentialReferenceTrial{}, err
	}
	record, err := experiment.SealTrialRecord(prepared.manifest, experiment.TrialRecord{
		TrialID: assignment.TrialID, BlockID: assignment.BlockID, TreatmentID: assignment.TreatmentID,
		AttemptPlanSHA256: session.Plan().PlanSHA256, LifecycleState: state, Eligibility: experiment.EligibilitySupported,
		Exclusion: experiment.ExclusionNone, AgentObservationSHA256: observationSHA, GradeReceiptSHA256: gradeSHA,
		LifecycleEventSHA256: event.EventSHA256, Stages: stages, Metrics: metrics,
	})
	return sealedSequentialReferenceTrial{record: record, event: event}, err
}

func sequentialReferenceObservedProjections(manifest experiment.Manifest, observation agentadapter.Observation,
	passed bool,
) ([]experiment.StageObservation, []experiment.MetricObservation, error) {
	if !observation.Coverage || len(observation.Events) != 2 || observation.Events[0].Start == nil {
		return nil, nil, sequentialReferenceError("observation_projection", nil)
	}
	stages := make([]experiment.StageObservation, len(manifest.AnalysisPlan.Stages))
	activationObserved := observation.Coverage && observation.Events[0].Start.Activation.UseEvidence == agentadapter.UseEvidenceObserved
	for index, declaration := range manifest.AnalysisPlan.Stages {
		value := false
		switch declaration.Stage {
		case experiment.StageCandidateRecall, experiment.StageSelection, experiment.StageLoad,
			experiment.StageInstructionAccess, experiment.StageUsefulAdherence:
			value = activationObserved
		case experiment.StageReferenceAccess, experiment.StageScriptAccess:
			// The fixed in-memory program has neither reference-answer nor
			// script authority, so absence is an observed property.
		case experiment.StageVerifierOutcome:
			value = passed
		default:
			return nil, nil, unsupportedSequentialReference("stage_projection", nil)
		}
		stages[index] = experiment.StageObservation{Stage: declaration.Stage, Presence: experiment.PresenceObserved, Value: &value}
	}
	metrics := make([]experiment.MetricObservation, len(manifest.AnalysisPlan.Metrics))
	for index, declaration := range manifest.AnalysisPlan.Metrics {
		value := uint64(0)
		if passed {
			value = 1
		}
		metrics[index] = experiment.MetricObservation{Metric: declaration.ID, Presence: experiment.PresenceObserved, Value: &value}
	}
	return stages, metrics, nil
}

func (prepared *sequentialReferencePrepared) sealIncompleteTrial(session *DurableAttemptSession, assignment sequentialReferenceAssignment,
	state experiment.LifecycleState, exclusion experiment.ExclusionReason, observationSHA string,
) (sealedSequentialReferenceTrial, error) {
	event, err := sequentialReferenceTerminalEvent(session)
	if err != nil {
		return sealedSequentialReferenceTrial{}, err
	}
	stages := make([]experiment.StageObservation, len(prepared.manifest.AnalysisPlan.Stages))
	for index, declaration := range prepared.manifest.AnalysisPlan.Stages {
		stages[index] = experiment.StageObservation{Stage: declaration.Stage, Presence: experiment.PresenceUnknown}
	}
	metrics := make([]experiment.MetricObservation, len(prepared.manifest.AnalysisPlan.Metrics))
	for index, declaration := range prepared.manifest.AnalysisPlan.Metrics {
		metrics[index] = experiment.MetricObservation{Metric: declaration.ID, Presence: experiment.PresenceUnknown}
	}
	record, err := experiment.SealTrialRecord(prepared.manifest, experiment.TrialRecord{
		TrialID: assignment.TrialID, BlockID: assignment.BlockID, TreatmentID: assignment.TreatmentID,
		AttemptPlanSHA256: session.Plan().PlanSHA256, LifecycleState: state, Eligibility: experiment.EligibilityIneligible,
		Exclusion: exclusion, AgentObservationSHA256: observationSHA, LifecycleEventSHA256: event.EventSHA256,
		Stages: stages, Metrics: metrics,
	})
	return sealedSequentialReferenceTrial{record: record, event: event}, err
}

func sequentialReferenceTerminalEvent(session *DurableAttemptSession) (lifecycle.Event, error) {
	inspection, err := session.store.Inspect(session.Plan().AttemptID)
	if err != nil || !inspection.Complete || len(inspection.Events) == 0 || !inspection.Projection.Terminal {
		return lifecycle.Event{}, sequentialReferenceError("terminal_event", err)
	}
	return inspection.Events[len(inspection.Events)-1], nil
}

func terminalizeSequentialReferenceInterruption(ctx context.Context, session *DurableAttemptSession, runErr error,
	usage lifecycle.Usage,
) (experiment.LifecycleState, experiment.ExclusionReason, error) {
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return experiment.LifecycleTimedOut, experiment.ExclusionLifecycleIncomplete, session.Timeout(true, usage)
	case errors.Is(runErr, context.Canceled), ctx != nil && errors.Is(context.Cause(ctx), context.Canceled):
		return experiment.LifecycleCanceled, experiment.ExclusionLifecycleIncomplete, session.Cancel(true, usage)
	default:
		return experiment.LifecycleUnknown, experiment.ExclusionLifecycleUnknown, session.Unknown(lifecycle.ErrorInternal, usage)
	}
}

func sequentialReferenceAssignments(manifest experiment.Manifest) []sequentialReferenceAssignment {
	result := make([]sequentialReferenceAssignment, 0, len(manifest.Blocks)*len(manifest.Treatments))
	for _, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			result = append(result, sequentialReferenceAssignment{TrialID: assignment.TrialID, BlockID: block.ID, TreatmentID: assignment.TreatmentID})
		}
	}
	return result
}

func sequentialReferenceTreatmentByID(manifest experiment.Manifest, id string) (experiment.Treatment, bool) {
	for _, treatment := range manifest.Treatments {
		if treatment.ID == id {
			return treatment, true
		}
	}
	return experiment.Treatment{}, false
}

func sequentialReferenceGradePassed(receipt grading.Receipt) bool {
	return receipt.Status == grading.ReceiptComplete && len(receipt.Decisions) == 1 &&
		receipt.Decisions[0].Presence == grading.PresenceObserved && receipt.Decisions[0].Passed
}

func sequentialReferenceLifecycleUsage() lifecycle.Usage {
	return lifecycle.Usage{
		EstimatedCostMicroUSD: lifecycle.ObservedMetric(0),
		InputTokens:           lifecycle.ObservedMetric(0),
		OutputTokens:          lifecycle.ObservedMetric(0),
	}
}

func sequentialReferenceLedgerHeader(manifestSHA256 string) (lifecycle.LedgerHeader, error) {
	nonce, err := hex.DecodeString(manifestSHA256)
	if err != nil || len(nonce) != 32 {
		return lifecycle.LedgerHeader{}, sequentialReferenceError("manifest_identity", err)
	}
	return lifecycle.NewHeader(bytes.NewReader(nonce))
}

func (prepared *sequentialReferencePrepared) destroy() {
	if prepared == nil {
		return
	}
	for _, treatment := range prepared.treatments {
		clear(treatment.inputs.Fixture)
		clear(treatment.inputs.Skill)
		clear(treatment.inputs.Definitions)
		treatment.inputs = executionbackend.ReferenceInputs{}
	}
	prepared.treatments = nil
}

func sequentialReferenceError(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrSequentialReference, code)
	}
	return fmt.Errorf("%w: %s: %w", ErrSequentialReference, code, cause)
}

func unsupportedSequentialReference(code string, cause error) error {
	joined := errors.Join(ErrSequentialReference, ErrSequentialReferenceUnsupported)
	if cause == nil {
		return fmt.Errorf("%w: %s", joined, code)
	}
	return fmt.Errorf("%w: %s: %w", joined, code, cause)
}
