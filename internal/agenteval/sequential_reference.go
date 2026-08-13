package agenteval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

var (
	ErrSequentialReference               = errors.New("sequential_reference_invalid")
	ErrSequentialReferenceUnsupported    = errors.New("sequential_reference_unsupported")
	ErrSequentialReferenceOutcomeUnknown = errors.New("sequential_reference_outcome_unknown")
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
	Scheduler      SchedulerReport                     `json:"scheduler"`
}

// SequentialReferenceRunOptions selects only local scheduling capacity. Every
// other authority and resource reservation comes from the immutable plans.
type SequentialReferenceRunOptions struct {
	Workers uint32
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

// CreateSequentialReferenceAttemptStore creates a fresh physical-attempt
// ledger for one clean publication. Logical manifest/trial identities remain
// stable, while the random ledger identity prevents cross-destination replay
// of an AttemptID.
func CreateSequentialReferenceAttemptStore(root string, manifest ExperimentManifest) (*AttemptLedgerStore, error) {
	if err := experiment.ValidateManifest(manifest); err != nil {
		return nil, sequentialReferenceError("manifest", err)
	}
	return CreateAttemptLedgerStore(root, nil)
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
	return prepared.runScheduled(ctx, store, SequentialReferenceRunOptions{Workers: 1}, nil, nil, true, nil, nil, nil)
}

// RunScheduledReference executes the same immutable reference roster with a
// bounded local scheduler. Workers never change manifest or result order.
func RunScheduledReference(ctx context.Context, store *AttemptLedgerStore, manifest ExperimentManifest, bundle SequentialReferenceBundle,
	options SequentialReferenceRunOptions,
) (SequentialReferenceResult, error) {
	prepared, err := prepareSequentialReference(ctx, manifest, bundle)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer prepared.destroy()
	return prepared.runScheduled(ctx, store, options, nil, nil, false, nil, nil, nil)
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
		if admitted.SHA256() != ownedManifest.Treatments[index].ExecutionBindingSHA256 ||
			!sequentialReferenceExecutionPlanSupported(ownedManifest, ownedManifest.Treatments[index], plan) {
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
	return prepared.runScheduled(ctx, store, SequentialReferenceRunOptions{Workers: 1}, nil, nil, true, nil, nil, sink)
}

type sequentialReferenceScheduledRoster struct {
	assignments  []sequentialReferenceAssignment
	baseBindings []lifecycle.Binding
	plans        []lifecycle.Plan
	schedule     scheduler.Plan
}

func (prepared *sequentialReferencePrepared) runScheduled(ctx context.Context, store *AttemptLedgerStore,
	options SequentialReferenceRunOptions, terminal []scheduler.TerminalTask,
	existing []SequentialReferenceTrialArtifacts,
	commitCurrentOnCancellation bool,
	scheduleSink func(scheduler.Plan) error,
	stageSink func(int, SequentialReferenceTrialArtifacts) error,
	sink func(int, SequentialReferenceTrialArtifacts) error,
) (SequentialReferenceResult, error) {
	roster, err := prepared.prepareScheduledRosterWithSink(store, options, scheduleSink)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	return prepared.runPreparedSchedule(ctx, store, roster, terminal, existing, commitCurrentOnCancellation, stageSink, sink)
}

func (prepared *sequentialReferencePrepared) prepareScheduledRoster(store *AttemptLedgerStore,
	options SequentialReferenceRunOptions,
) (sequentialReferenceScheduledRoster, error) {
	return prepared.prepareScheduledRosterWithSink(store, options, nil)
}

func (prepared *sequentialReferencePrepared) prepareScheduledRosterWithSink(store *AttemptLedgerStore,
	options SequentialReferenceRunOptions, scheduleSink func(scheduler.Plan) error,
) (sequentialReferenceScheduledRoster, error) {
	if store == nil {
		return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_store", nil)
	}
	bindings, err := ExperimentAttemptBindings(prepared.manifest)
	if err != nil {
		return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_bindings", err)
	}
	baseBindings := append([]lifecycle.Binding(nil), bindings...)
	assignments := sequentialReferenceAssignments(prepared.manifest)
	if len(bindings) != len(assignments) {
		return sequentialReferenceScheduledRoster{}, sequentialReferenceError("assignment_count", nil)
	}
	for index, assignment := range assignments {
		treatment := prepared.treatments[assignment.TreatmentID]
		if treatment == nil {
			return sequentialReferenceScheduledRoster{}, sequentialReferenceError("assignment_treatment", nil)
		}
		bindings[index], err = BindExecutionBackendTrial(bindings[index], treatment.plan)
		if err == nil {
			bindings[index], err = BindGradingPlan(bindings[index], prepared.gradingPlan)
		}
		if err != nil {
			return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_binding", err)
		}
	}
	plans := make([]lifecycle.Plan, len(bindings))
	for index, binding := range bindings {
		plans[index], err = lifecycle.NewPlan(store.Header(), uint32(index+1), binding) // #nosec G115 -- bindings are bounded by lifecycle.MaxAttempts.
		if err != nil {
			return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_plan", err)
		}
	}
	schedule, err := prepared.schedulerPlan(options, plans, assignments)
	if err != nil {
		return sequentialReferenceScheduledRoster{}, err
	}
	// Persist the width-dependent scheduler plan before materializing its
	// planned ledger roster. A crash can therefore leave neither member, or a
	// plan whose exact roster may be completed, but never an unbound roster
	// that a later resume can reinterpret at a different width.
	if scheduleSink != nil {
		if err := scheduleSink(schedule); err != nil {
			return sequentialReferenceScheduledRoster{}, sequentialReferenceError("scheduler_sink", err)
		}
	}
	written, err := store.EnsureRoster(bindings)
	if err != nil {
		return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_roster", err)
	}
	if !reflect.DeepEqual(written, plans) {
		return sequentialReferenceScheduledRoster{}, sequentialReferenceError("attempt_roster_readback", nil)
	}
	return sequentialReferenceScheduledRoster{assignments: assignments, baseBindings: baseBindings, plans: plans, schedule: schedule}, nil
}

func (prepared *sequentialReferencePrepared) runPreparedSchedule(ctx context.Context, store *AttemptLedgerStore,
	roster sequentialReferenceScheduledRoster, terminal []scheduler.TerminalTask,
	existing []SequentialReferenceTrialArtifacts, commitCurrentOnCancellation bool,
	stageSink func(int, SequentialReferenceTrialArtifacts) error,
	sink func(int, SequentialReferenceTrialArtifacts) error,
) (SequentialReferenceResult, error) {
	if len(roster.assignments) == 0 || len(roster.assignments) != len(roster.plans) || scheduler.ValidatePlan(roster.schedule) != nil ||
		(len(existing) != 0 && len(existing) != len(roster.plans)) {
		return SequentialReferenceResult{}, sequentialReferenceError("scheduler_roster", nil)
	}
	terminalByTask := make(map[string]scheduler.Outcome, len(terminal))
	for _, item := range terminal {
		terminalByTask[item.TaskSHA256] = item.Outcome
	}
	for index, artifacts := range existing {
		_, terminalTask := terminalByTask[roster.plans[index].PlanSHA256]
		if (artifacts.TrialRecord.RecordSHA256 != "") != terminalTask {
			return SequentialReferenceResult{}, sequentialReferenceError("scheduler_snapshot", nil)
		}
	}
	result := SequentialReferenceResult{ManifestSHA256: prepared.manifest.ManifestSHA256,
		Trials: make([]SequentialReferenceTrialArtifacts, 0, len(roster.plans))}
	slots := make([]SequentialReferenceTrialArtifacts, len(roster.plans))
	copy(slots, existing)
	var sinkMutex sync.Mutex
	dispatchContext := ctx
	if commitCurrentOnCancellation && roster.schedule.Limits.Workers == 1 && len(terminal) == 0 {
		dispatchContext = context.WithoutCancel(ctx)
	}
	report, runErr := scheduler.RunRemaining(dispatchContext, roster.schedule, terminal, func(_ context.Context, task scheduler.Task) (scheduler.RunFunc, error) {
		index := int(task.Ordinal - 1)
		if index < 0 || index >= len(roster.plans) || roster.plans[index].PlanSHA256 != task.TaskSHA256 ||
			slots[index].TrialRecord.RecordSHA256 != "" {
			return nil, sequentialReferenceError("scheduler_task", nil)
		}
		session, sessionErr := NewDurableAttemptSession(store, roster.plans[index])
		if sessionErr != nil {
			return nil, sequentialReferenceError("attempt_session", sessionErr)
		}
		if sessionErr = beginRunAttempt(session); sessionErr != nil {
			return nil, sequentialReferenceError("attempt_commit", sessionErr)
		}
		return func(runContext context.Context) (scheduler.Outcome, error) {
			executionContext := runContext
			if commitCurrentOnCancellation && roster.schedule.Limits.Workers == 1 {
				executionContext = ctx
			}
			stage := func(artifacts SequentialReferenceTrialArtifacts) error {
				if stageSink == nil {
					return nil
				}
				sinkMutex.Lock()
				stageErr := stageSink(index, artifacts)
				sinkMutex.Unlock()
				return stageErr
			}
			artifacts, trialErr := prepared.runStartedTrial(executionContext, session, roster.assignments[index], stage)
			slots[index] = artifacts
			if artifacts.TrialRecord.RecordSHA256 != "" && sink != nil {
				sinkMutex.Lock()
				sinkErr := sink(index, artifacts)
				sinkMutex.Unlock()
				if sinkErr != nil {
					trialErr = errors.Join(trialErr, sequentialReferenceError("artifact_sink", sinkErr))
				}
			}
			return sequentialReferenceSchedulerOutcome(artifacts), trialErr
		}, nil
	})
	result.Scheduler = report
	for _, artifacts := range slots {
		if artifacts.TrialRecord.RecordSHA256 != "" {
			result.Trials = append(result.Trials, artifacts)
		}
	}
	if runErr != nil {
		return result, sequentialReferenceError("scheduler", runErr)
	}
	return result, nil
}

type sequentialReferenceAssignment struct {
	TrialID     string
	BlockID     string
	TreatmentID string
	Round       uint32
}

func (prepared *sequentialReferencePrepared) runStartedTrial(ctx context.Context, session *DurableAttemptSession,
	assignment sequentialReferenceAssignment, stage func(SequentialReferenceTrialArtifacts) error,
) (SequentialReferenceTrialArtifacts, error) {
	treatment := prepared.treatments[assignment.TreatmentID]
	artifacts := SequentialReferenceTrialArtifacts{AttemptPlan: session.Plan(), ExecutionPlan: treatment.plan, GradingPlan: prepared.gradingPlan}
	usage := sequentialReferenceLifecycleUsage()
	if err := stage(artifacts); err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("artifact_stage", err))
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
		stageErr := stage(artifacts)
		state, exclusion, lifecycleErr := terminalizeSequentialReferenceInterruption(ctx, session, runErr, usage)
		if lifecycleErr != nil {
			return artifacts, sequentialReferenceError("attempt_terminal", errors.Join(runErr, stageErr, lifecycleErr))
		}
		sealed, recordErr := prepared.sealIncompleteTrial(session, assignment, state, exclusion, "")
		if recordErr != nil {
			return artifacts, sequentialReferenceError("trial_record", errors.Join(runErr, recordErr))
		}
		artifacts.TrialRecord, artifacts.LifecycleEvent = sealed.record, sealed.event
		return artifacts, sequentialReferenceError("execution", errors.Join(runErr, stageErr))
	}
	executionReceiptData, err := executionbackend.EncodeReceipt(treatment.plan, backendResult.Receipt)
	if err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("execution_receipt", err))
	}
	if err := stage(artifacts); err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, "", sequentialReferenceError("artifact_stage", err))
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
	if err := stage(artifacts); err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, observationSHA, sequentialReferenceError("artifact_stage", err))
	}
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
	if err := stage(artifacts); err != nil {
		return prepared.failPostExecution(session, assignment, artifacts, observationSHA, sequentialReferenceError("artifact_stage", err))
	}
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
	return prepared.sealSupportedTrialFromEvent(session.Plan(), assignment, event, observationSHA, gradeSHA, passed, observation)
}

func (prepared *sequentialReferencePrepared) sealSupportedTrialFromEvent(plan lifecycle.Plan, assignment sequentialReferenceAssignment,
	event lifecycle.Event, observationSHA, gradeSHA string, passed bool, observation agentadapter.Observation,
) (sealedSequentialReferenceTrial, error) {
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
		AttemptPlanSHA256: plan.PlanSHA256, LifecycleState: state, Eligibility: experiment.EligibilitySupported,
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
	return prepared.sealIncompleteTrialFromEvent(session.Plan(), assignment, event, state, exclusion, observationSHA)
}

func (prepared *sequentialReferencePrepared) sealIncompleteTrialFromEvent(plan lifecycle.Plan, assignment sequentialReferenceAssignment,
	event lifecycle.Event, state experiment.LifecycleState, exclusion experiment.ExclusionReason, observationSHA string,
) (sealedSequentialReferenceTrial, error) {
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
		AttemptPlanSHA256: plan.PlanSHA256, LifecycleState: state, Eligibility: experiment.EligibilityIneligible,
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
		for position, assignment := range block.Assignments {
			result = append(result, sequentialReferenceAssignment{TrialID: assignment.TrialID, BlockID: block.ID,
				TreatmentID: assignment.TreatmentID, Round: uint32(position + 1)}) // #nosec G115 -- treatment count is bounded.
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

func unknownSequentialReference(code string, cause error) error {
	joined := errors.Join(ErrSequentialReference, ErrSequentialReferenceOutcomeUnknown)
	if cause == nil {
		return fmt.Errorf("%w: %s", joined, code)
	}
	return fmt.Errorf("%w: %s: %w", joined, code, cause)
}
