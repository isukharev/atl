package agenteval

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

func InspectSequentialReferencePublication(destination string) (SequentialReferenceResult, error) {
	_, result, err := inspectSequentialReferencePublication(destination)
	return result, err
}

func inspectSequentialReferencePublication(destination string) (experiment.Manifest, SequentialReferenceResult, error) {
	return inspectSequentialReferencePublicationContext(context.Background(), destination)
}

func inspectSequentialReferencePublicationContext(ctx context.Context, destination string) (experiment.Manifest, SequentialReferenceResult, error) {
	return inspectSequentialReferencePublicationWithAdmissionContext(ctx, destination, nil)
}

func inspectSequentialReferencePublicationWithAdmissionContext(ctx context.Context, destination string,
	admitManifest func(experiment.Manifest) error,
) (experiment.Manifest, SequentialReferenceResult, error) {
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	publication, err := openSequentialReferencePublication(destination)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	defer publication.close()
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if _, err := publication.root.Lstat(sequentialReferenceMarkerName); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("publication_incomplete", err)
	}
	manifestData, err := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceManifestName, experiment.MaxManifestBytes)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("manifest_read", err)
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	manifest, err := experiment.DecodeManifest(bytes.NewReader(manifestData))
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("manifest_decode", err)
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if err := validateSequentialReferenceManifestProfile(manifest); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if admitManifest != nil {
		if err := admitManifest(manifest); err != nil {
			return experiment.Manifest{}, SequentialReferenceResult{}, err
		}
	}
	scheduleData, err := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceSchedulerPlanName, scheduler.MaxPlanBytes)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("scheduler_plan_read", err)
	}
	schedule, err := scheduler.DecodePlan(bytes.NewReader(scheduleData))
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("scheduler_plan_decode", err)
	}
	reportData, err := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceSchedulerReportName, scheduler.MaxReportBytes)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("scheduler_report_read", err)
	}
	scheduleReport, err := scheduler.DecodeReport(bytes.NewReader(reportData), schedule)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("scheduler_report_decode", err)
	}
	recordValidator, err := experiment.NewTrialRecordValidator(manifest)
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("trial_validator", err)
	}
	assignments := sequentialReferenceAssignments(manifest)
	baseBindings, err := ExperimentAttemptBindings(manifest)
	if err != nil || len(baseBindings) != len(assignments) {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("attempt_bindings", err)
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	store, err := OpenAttemptLedgerStoreStrictContext(ctx, filepath.Join(destination, sequentialReferenceLedgerDirectory))
	if err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("attempt_store_open", err)
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	inspections, err := store.InspectAllStrictContext(ctx, len(assignments))
	if contextErr := sequentialReferenceInspectionContextError(ctx); contextErr != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, contextErr
	}
	if err != nil || len(inspections) != len(assignments) {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("attempt_roster", err)
	}
	result := SequentialReferenceResult{ManifestSHA256: manifest.ManifestSHA256, Trials: make([]SequentialReferenceTrialArtifacts, len(assignments)),
		Scheduler: scheduleReport}
	for index, assignment := range assignments {
		if err := sequentialReferenceInspectionContextError(ctx); err != nil {
			return experiment.Manifest{}, SequentialReferenceResult{}, err
		}
		artifacts, readErr := publication.readTrialContext(ctx, manifest, recordValidator, assignment, baseBindings[index], inspections[index])
		if readErr != nil {
			return experiment.Manifest{}, SequentialReferenceResult{}, readErr
		}
		result.Trials[index] = artifacts
	}
	if err := validateSequentialReferencePublishedSchedule(manifest, assignments, inspections, result, schedule); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if err := publication.validateShapeContext(ctx, len(assignments), false); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return experiment.Manifest{}, SequentialReferenceResult{}, err
	}
	if !publication.stable() {
		return experiment.Manifest{}, SequentialReferenceResult{}, sequentialReferenceError("publication_changed", nil)
	}
	return manifest, result, nil
}

func sequentialReferenceInspectionContextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
func errSequentialReferenceArtifactDrift(left, right SequentialReferenceTrialArtifacts) error {
	if reflect.DeepEqual(left, right) {
		return nil
	}
	return errors.New("artifact drift")
}

func validateSequentialReferencePublishedSchedule(manifest experiment.Manifest, assignments []sequentialReferenceAssignment,
	inspections []AttemptLedgerInspection, result SequentialReferenceResult, schedule scheduler.Plan,
) error {
	if len(assignments) != len(inspections) || len(assignments) != len(result.Trials) {
		return sequentialReferenceError("scheduler_roster", nil)
	}
	plans := make([]lifecycle.Plan, len(inspections))
	executionPlans := make([]executionbackend.Plan, len(result.Trials))
	for index := range inspections {
		plans[index] = inspections[index].Plan
		executionPlans[index] = result.Trials[index].ExecutionPlan
	}
	want, err := sequentialReferenceSchedulerPlan(manifest, SequentialReferenceRunOptions{Workers: schedule.Limits.Workers},
		plans, assignments, executionPlans)
	if err != nil || !reflect.DeepEqual(want, schedule) || scheduler.ValidateReport(schedule, result.Scheduler) != nil ||
		!sequentialReferenceSchedulerReportMatchesTrials(schedule, result) {
		return sequentialReferenceError("scheduler_projection", err)
	}
	return nil
}

func sequentialReferenceSchedulerReportMatchesTrials(schedule scheduler.Plan, result SequentialReferenceResult) bool {
	if len(result.Trials) != len(schedule.Tasks) {
		return false
	}
	candidate := scheduler.Report{Stop: scheduler.StopNone, Started: uint32(len(schedule.Tasks)), // #nosec G115 -- task count is bounded.
		Completed: uint32(len(schedule.Tasks))} // #nosec G115 -- task count is bounded.
	for _, artifacts := range result.Trials {
		switch sequentialReferenceSchedulerOutcome(artifacts) {
		case scheduler.OutcomeSucceeded:
			candidate.Succeeded++
		case scheduler.OutcomeFailed:
			candidate.Failed++
		case scheduler.OutcomeCanceled:
			candidate.Canceled++
		case scheduler.OutcomeUnknown:
			candidate.Unknown++
		default:
			return false
		}
	}
	want, err := scheduler.SealReport(schedule, candidate)
	return err == nil && reflect.DeepEqual(want, result.Scheduler)
}

func validateSequentialReferenceManifestProfile(manifest experiment.Manifest) error {
	if err := experiment.ValidateManifest(manifest); err != nil || manifest.Design.Case.SourceKind != experiment.SourceAgentSkills ||
		manifest.Design.CompatibilityProfile != experiment.CompatibilityNone || !manifest.PositionBalanceComplete {
		return sequentialReferenceError("manifest_profile", err)
	}
	want, err := SequentialReferenceExperimentCapabilityContract()
	if err != nil || !reflect.DeepEqual(manifest.CapabilityContract, want) ||
		!sequentialReferenceAnalysisSupported(manifest.AnalysisPlan) || !sequentialReferenceTreatmentsSupported(manifest.Design.Treatments) {
		return unsupportedSequentialReference("manifest_profile", err)
	}
	return nil
}

func (publication *sequentialReferencePublication) readTrial(manifest experiment.Manifest, validator *experiment.TrialRecordValidator,
	assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding,
	inspection AttemptLedgerInspection,
) (SequentialReferenceTrialArtifacts, error) {
	return publication.readTrialContext(context.Background(), manifest, validator, assignment, baseBinding, inspection)
}

func (publication *sequentialReferencePublication) readTrialContext(ctx context.Context, manifest experiment.Manifest,
	validator *experiment.TrialRecordValidator, assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding,
	inspection AttemptLedgerInspection,
) (SequentialReferenceTrialArtifacts, error) {
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	if !inspection.Complete || !inspection.Projection.Terminal || len(inspection.Events) == 0 ||
		inspection.Plan.Ordinal == 0 || inspection.Plan.Ordinal > lifecycle.MaxAttempts {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("attempt_incomplete", nil)
	}
	artifacts, exists, err := publication.readTrialStageContext(ctx, validator, inspection)
	if err != nil || !exists || artifacts.TrialRecord.RecordSHA256 == "" {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("trial_files", err)
	}
	if artifacts.TrialRecord.Eligibility == experiment.EligibilitySupported {
		if artifacts.Observation == nil || artifacts.ExecutionReceipt == nil || artifacts.GradeReceipt == nil {
			return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("trial_files", nil)
		}
		executionReceiptData, encodeErr := executionbackend.EncodeReceipt(artifacts.ExecutionPlan, *artifacts.ExecutionReceipt)
		if encodeErr != nil {
			return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("execution_receipt_encode", encodeErr)
		}
		if err := validateSequentialReferenceArtifactChain(ctx, manifest, assignment, baseBinding, inspection, artifacts, executionReceiptData); err != nil {
			return SequentialReferenceTrialArtifacts{}, err
		}
	} else if err := validateSequentialReferenceIncompleteArtifactChain(ctx, manifest, assignment, baseBinding, inspection, artifacts); err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	return artifacts, nil
}

func validateSequentialReferenceIncompleteArtifactChain(ctx context.Context, manifest experiment.Manifest,
	assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding, inspection AttemptLedgerInspection,
	artifacts SequentialReferenceTrialArtifacts,
) error {
	if artifacts.TrialRecord.Eligibility != experiment.EligibilityIneligible || artifacts.TrialRecord.GradeReceiptSHA256 != "" ||
		assignment.TrialID != artifacts.TrialRecord.TrialID || assignment.BlockID != artifacts.TrialRecord.BlockID ||
		assignment.TreatmentID != artifacts.TrialRecord.TreatmentID || artifacts.AttemptPlan.PlanSHA256 != artifacts.TrialRecord.AttemptPlanSHA256 ||
		artifacts.LifecycleEvent.EventSHA256 != artifacts.TrialRecord.LifecycleEventSHA256 ||
		!reflect.DeepEqual(artifacts.LifecycleEvent, inspection.Events[len(inspection.Events)-1]) {
		return sequentialReferenceError("incomplete_artifact_chain", nil)
	}
	wantState := experiment.LifecycleState("")
	wantExclusion := experiment.ExclusionReason("")
	switch artifacts.LifecycleEvent.To {
	case lifecycle.StateFailed:
		wantState, wantExclusion = experiment.LifecycleFailed, experiment.ExclusionLifecycleIncomplete
	case lifecycle.StateCanceled:
		wantState, wantExclusion = experiment.LifecycleCanceled, experiment.ExclusionLifecycleIncomplete
	case lifecycle.StateTimedOut:
		wantState, wantExclusion = experiment.LifecycleTimedOut, experiment.ExclusionLifecycleIncomplete
	case lifecycle.StateUnknown:
		wantState, wantExclusion = experiment.LifecycleUnknown, experiment.ExclusionLifecycleUnknown
	default:
		return sequentialReferenceError("incomplete_lifecycle", nil)
	}
	if artifacts.TrialRecord.LifecycleState != wantState || artifacts.TrialRecord.Exclusion != wantExclusion ||
		!sequentialReferenceUnknownProjections(manifest, artifacts.TrialRecord) {
		return sequentialReferenceError("incomplete_trial_projection", nil)
	}
	treatment, ok := sequentialReferenceTreatmentByID(manifest, assignment.TreatmentID)
	if !ok {
		return sequentialReferenceError("artifact_treatment", nil)
	}
	backend, err := executionbackend.ReferenceContract()
	if err != nil {
		return err
	}
	admittedExecution, err := executionbackend.Admit(backend, artifacts.ExecutionPlan)
	if err != nil || admittedExecution.SHA256() != treatment.ExecutionBindingSHA256 ||
		!sequentialReferenceExecutionPlanSupported(manifest, treatment, artifacts.ExecutionPlan) {
		return sequentialReferenceError("execution_binding", err)
	}
	wantGrading, err := NewSequentialReferenceGradingPlan(artifacts.GradingPlan.InputProjectionSHA256)
	if err != nil || !reflect.DeepEqual(wantGrading, artifacts.GradingPlan) {
		return sequentialReferenceError("grading_profile", err)
	}
	gradingSHA, err := grading.PlanSHA256(artifacts.GradingPlan)
	if err != nil || gradingSHA != manifest.Design.Case.GradingPlanSHA256 {
		return sequentialReferenceError("grading_binding", err)
	}
	wantBinding, err := BindExecutionBackendTrial(baseBinding, artifacts.ExecutionPlan)
	if err == nil {
		wantBinding, err = BindGradingPlan(wantBinding, artifacts.GradingPlan)
	}
	if err != nil || !reflect.DeepEqual(wantBinding, artifacts.AttemptPlan.Binding) {
		return sequentialReferenceError("attempt_binding", err)
	}
	if artifacts.Observation != nil && artifacts.ExecutionReceipt == nil || artifacts.GradeReceipt != nil &&
		(artifacts.Observation == nil || artifacts.ExecutionReceipt == nil) {
		return sequentialReferenceError("trial_stage_order", nil)
	}
	if artifacts.Observation == nil {
		if artifacts.TrialRecord.AgentObservationSHA256 != "" {
			return sequentialReferenceError("observation_binding", nil)
		}
	} else {
		adapter, contractErr := agentadapter.ReferenceContract()
		if contractErr != nil {
			return contractErr
		}
		wantObservation, observationErr := agentadapter.NewReferenceObservation(adapter, artifacts.AttemptPlan.AttemptID,
			treatment.ExpectedActivation)
		observationSHA, digestErr := agentadapter.ObservationSHA256(adapter, *artifacts.Observation)
		if observationErr != nil || digestErr != nil || !reflect.DeepEqual(wantObservation, *artifacts.Observation) ||
			observationSHA != artifacts.TrialRecord.AgentObservationSHA256 {
			return sequentialReferenceError("observation_binding", errors.Join(observationErr, digestErr))
		}
	}
	if artifacts.GradeReceipt != nil {
		executionReceiptData, encodeErr := executionbackend.EncodeReceipt(artifacts.ExecutionPlan, *artifacts.ExecutionReceipt)
		if encodeErr != nil {
			return sequentialReferenceError("execution_receipt_encode", encodeErr)
		}
		grader, contractErr := grading.BuiltinContract()
		if contractErr != nil {
			return contractErr
		}
		admitted, admitErr := grading.Admit(grader, artifacts.GradingPlan)
		if admitErr != nil {
			return sequentialReferenceError("grading_admission", admitErr)
		}
		evidence, evidenceErr := grading.PrepareEvidence(ctx, admitted, grading.EvidenceSet{
			InputProjectionSHA256: artifacts.GradingPlan.InputProjectionSHA256,
			Files: []grading.FileEvidence{{ID: "execution-receipt", Visibility: grading.VisibilityPublic,
				Present: true, Mode: 0o600, Data: executionReceiptData}},
			Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{},
			Counters: []grading.CounterEvidence{},
		})
		if evidenceErr != nil {
			return sequentialReferenceError("grading_evidence", evidenceErr)
		}
		wantReceipt, evaluateErr := grading.EvaluateDeterministic(ctx, admitted, evidence)
		evidence.Destroy()
		if evaluateErr != nil || !reflect.DeepEqual(wantReceipt, *artifacts.GradeReceipt) {
			return sequentialReferenceError("grade_receipt_projection", evaluateErr)
		}
	}
	return nil
}

func sequentialReferenceUnknownProjections(manifest experiment.Manifest, record experiment.TrialRecord) bool {
	if len(record.Stages) != len(manifest.AnalysisPlan.Stages) || len(record.Metrics) != len(manifest.AnalysisPlan.Metrics) {
		return false
	}
	for index, declaration := range manifest.AnalysisPlan.Stages {
		if record.Stages[index].Stage != declaration.Stage || record.Stages[index].Presence != experiment.PresenceUnknown ||
			record.Stages[index].Value != nil {
			return false
		}
	}
	for index, declaration := range manifest.AnalysisPlan.Metrics {
		if record.Metrics[index].Metric != declaration.ID || record.Metrics[index].Presence != experiment.PresenceUnknown ||
			record.Metrics[index].Value != nil {
			return false
		}
	}
	return true
}

func (publication *sequentialReferencePublication) readTrialStageContext(ctx context.Context,
	validator *experiment.TrialRecordValidator, inspection AttemptLedgerInspection,
) (SequentialReferenceTrialArtifacts, bool, error) {
	artifacts := SequentialReferenceTrialArtifacts{AttemptPlan: inspection.Plan}
	if len(inspection.Events) != 0 {
		artifacts.LifecycleEvent = inspection.Events[len(inspection.Events)-1]
	}
	directory := filepath.Join(sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(inspection.Plan.Ordinal))
	info, err := publication.root.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return artifacts, false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("trial_directory", err)
	}
	files, err := readSequentialReferenceDirectoryContext(ctx, publication.root, directory, 7)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("trial_files", err)
	}
	allowed := map[string]bool{
		sequentialReferenceObservationName:      true,
		sequentialReferenceExecutionPlanName:    true,
		sequentialReferenceExecutionReceiptName: true,
		sequentialReferenceGradeReceiptName:     true,
		sequentialReferenceGradingPlanName:      true,
		sequentialReferenceTrialRecordName:      true,
	}
	present := make(map[string]bool, len(files))
	for _, name := range files {
		if !allowed[name] {
			return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("trial_files", nil)
		}
		present[name] = true
	}
	if !present[sequentialReferenceExecutionPlanName] || !present[sequentialReferenceGradingPlanName] {
		return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("trial_stage_base", nil)
	}
	read := func(name string, maximum int64) ([]byte, error) {
		return readSequentialReferenceFileContext(ctx, publication.root, filepath.Join(directory, name), maximum)
	}
	executionPlanData, err := read(sequentialReferenceExecutionPlanName, executionbackend.MaxPlanBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, false, err
	}
	artifacts.ExecutionPlan, err = executionbackend.DecodePlan(bytes.NewReader(executionPlanData))
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("execution_plan_decode", err)
	}
	gradingPlanData, err := read(sequentialReferenceGradingPlanName, grading.MaxPlanBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, false, err
	}
	artifacts.GradingPlan, err = grading.DecodePlan(bytes.NewReader(gradingPlanData))
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("grading_plan_decode", err)
	}
	if present[sequentialReferenceObservationName] {
		adapter, contractErr := agentadapter.ReferenceContract()
		if contractErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, contractErr
		}
		data, readErr := read(sequentialReferenceObservationName, agentadapter.MaxObservationBytes)
		if readErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, readErr
		}
		observation, decodeErr := agentadapter.DecodeObservation(bytes.NewReader(data), adapter)
		if decodeErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("observation_decode", decodeErr)
		}
		artifacts.Observation = &observation
	}
	if present[sequentialReferenceExecutionReceiptName] {
		data, readErr := read(sequentialReferenceExecutionReceiptName, executionbackend.MaxReceiptBytes)
		if readErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, readErr
		}
		receipt, decodeErr := executionbackend.DecodeReceipt(bytes.NewReader(data), artifacts.ExecutionPlan)
		if decodeErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("execution_receipt_decode", decodeErr)
		}
		artifacts.ExecutionReceipt = &receipt
	}
	if present[sequentialReferenceGradeReceiptName] {
		data, readErr := read(sequentialReferenceGradeReceiptName, grading.MaxReceiptBytes)
		if readErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, readErr
		}
		receipt, decodeErr := grading.DecodeReceipt(bytes.NewReader(data), artifacts.GradingPlan)
		if decodeErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("grade_receipt_decode", decodeErr)
		}
		artifacts.GradeReceipt = &receipt
	}
	if present[sequentialReferenceTrialRecordName] {
		data, readErr := read(sequentialReferenceTrialRecordName, experiment.MaxTrialBytes)
		if readErr != nil {
			return SequentialReferenceTrialArtifacts{}, false, readErr
		}
		artifacts.TrialRecord, err = validator.Decode(bytes.NewReader(data))
		if err != nil {
			return SequentialReferenceTrialArtifacts{}, false, sequentialReferenceError("trial_record_decode", err)
		}
	}
	return artifacts, true, nil
}

func (publication *sequentialReferencePublication) recoverTerminalTrialContext(ctx context.Context,
	prepared *sequentialReferencePrepared, validator *experiment.TrialRecordValidator,
	assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding, inspection AttemptLedgerInspection,
) (SequentialReferenceTrialArtifacts, error) {
	if prepared == nil || !inspection.Complete || !inspection.Projection.Terminal || len(inspection.Events) == 0 {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("resume_terminal", nil)
	}
	artifacts, exists, err := publication.readTrialStageContext(ctx, validator, inspection)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	treatment := prepared.treatments[assignment.TreatmentID]
	if treatment == nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("resume_treatment", nil)
	}
	if !exists {
		artifacts = SequentialReferenceTrialArtifacts{AttemptPlan: inspection.Plan, ExecutionPlan: treatment.plan,
			GradingPlan: prepared.gradingPlan, LifecycleEvent: inspection.Events[len(inspection.Events)-1]}
	}
	observationSHA, gradeSHA, executionReceiptData, err := validateSequentialReferenceTrialStage(ctx, prepared,
		assignment, baseBinding, inspection, artifacts)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	var want sealedSequentialReferenceTrial
	full := artifacts.Observation != nil && artifacts.ExecutionReceipt != nil && artifacts.GradeReceipt != nil
	switch inspection.Projection.State {
	case lifecycle.StateSucceeded:
		if !full {
			return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("resume_success_artifacts", nil)
		}
		passed := artifacts.ExecutionReceipt.Verdict == executionbackend.VerdictSucceeded && sequentialReferenceGradePassed(*artifacts.GradeReceipt)
		want, err = prepared.sealSupportedTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
			observationSHA, gradeSHA, passed, *artifacts.Observation)
	case lifecycle.StateFailed:
		if full {
			passed := artifacts.ExecutionReceipt.Verdict == executionbackend.VerdictSucceeded && sequentialReferenceGradePassed(*artifacts.GradeReceipt)
			want, err = prepared.sealSupportedTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
				observationSHA, gradeSHA, passed, *artifacts.Observation)
		} else {
			want, err = prepared.sealIncompleteTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
				experiment.LifecycleFailed, experiment.ExclusionLifecycleIncomplete, observationSHA)
		}
	case lifecycle.StateCanceled:
		want, err = prepared.sealIncompleteTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
			experiment.LifecycleCanceled, experiment.ExclusionLifecycleIncomplete, observationSHA)
	case lifecycle.StateTimedOut:
		want, err = prepared.sealIncompleteTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
			experiment.LifecycleTimedOut, experiment.ExclusionLifecycleIncomplete, observationSHA)
	case lifecycle.StateUnknown:
		want, err = prepared.sealIncompleteTrialFromEvent(inspection.Plan, assignment, inspection.Events[len(inspection.Events)-1],
			experiment.LifecycleUnknown, experiment.ExclusionLifecycleUnknown, observationSHA)
	default:
		return SequentialReferenceTrialArtifacts{}, unsupportedSequentialReference("resume_terminal_state", nil)
	}
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("resume_trial_record", err)
	}
	if artifacts.TrialRecord.RecordSHA256 == "" {
		artifacts.TrialRecord = want.record
	} else if !reflect.DeepEqual(artifacts.TrialRecord, want.record) {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("resume_trial_record_drift", nil)
	}
	artifacts.LifecycleEvent = want.event
	if full && (inspection.Projection.State == lifecycle.StateSucceeded || inspection.Projection.State == lifecycle.StateFailed) {
		if err := validateSequentialReferenceArtifactChain(ctx, prepared.manifest, assignment, baseBinding, inspection,
			artifacts, executionReceiptData); err != nil {
			return SequentialReferenceTrialArtifacts{}, err
		}
	}
	return artifacts, nil
}

func validateSequentialReferenceTrialStage(ctx context.Context, prepared *sequentialReferencePrepared,
	assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding, inspection AttemptLedgerInspection,
	artifacts SequentialReferenceTrialArtifacts,
) (string, string, []byte, error) {
	treatment := prepared.treatments[assignment.TreatmentID]
	if treatment == nil || !reflect.DeepEqual(artifacts.ExecutionPlan, treatment.plan) ||
		!reflect.DeepEqual(artifacts.GradingPlan, prepared.gradingPlan) || !reflect.DeepEqual(artifacts.AttemptPlan, inspection.Plan) {
		return "", "", nil, sequentialReferenceError("trial_stage_binding", nil)
	}
	wantBinding, err := BindExecutionBackendTrial(baseBinding, artifacts.ExecutionPlan)
	if err == nil {
		wantBinding, err = BindGradingPlan(wantBinding, artifacts.GradingPlan)
	}
	if err != nil || !reflect.DeepEqual(wantBinding, artifacts.AttemptPlan.Binding) {
		return "", "", nil, sequentialReferenceError("attempt_binding", err)
	}
	if artifacts.Observation != nil && artifacts.ExecutionReceipt == nil || artifacts.GradeReceipt != nil &&
		(artifacts.Observation == nil || artifacts.ExecutionReceipt == nil) {
		return "", "", nil, sequentialReferenceError("trial_stage_order", nil)
	}
	var executionReceiptData []byte
	if artifacts.ExecutionReceipt != nil {
		executionReceiptData, err = executionbackend.EncodeReceipt(artifacts.ExecutionPlan, *artifacts.ExecutionReceipt)
		if err != nil {
			return "", "", nil, sequentialReferenceError("execution_receipt_encode", err)
		}
	}
	observationSHA := ""
	if artifacts.Observation != nil {
		manifestTreatment, ok := sequentialReferenceTreatmentByID(prepared.manifest, assignment.TreatmentID)
		if !ok {
			return "", "", nil, sequentialReferenceError("artifact_treatment", nil)
		}
		wantObservation, observationErr := agentadapter.NewReferenceObservation(prepared.adapter,
			artifacts.AttemptPlan.AttemptID, manifestTreatment.ExpectedActivation)
		if observationErr != nil || !reflect.DeepEqual(wantObservation, *artifacts.Observation) {
			return "", "", nil, sequentialReferenceError("observation_projection", observationErr)
		}
		observationSHA, err = agentadapter.ObservationSHA256(prepared.adapter, *artifacts.Observation)
		if err != nil {
			return "", "", nil, sequentialReferenceError("observation_binding", err)
		}
	}
	gradeSHA := ""
	if artifacts.GradeReceipt != nil {
		evidence, evidenceErr := grading.PrepareEvidence(ctx, prepared.admittedGrading, grading.EvidenceSet{
			InputProjectionSHA256: prepared.gradingPlan.InputProjectionSHA256,
			Files: []grading.FileEvidence{{ID: "execution-receipt", Visibility: grading.VisibilityPublic,
				Present: true, Mode: 0o600, Data: executionReceiptData}},
			Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{},
			Counters: []grading.CounterEvidence{},
		})
		if evidenceErr != nil {
			return "", "", nil, sequentialReferenceError("grading_evidence", evidenceErr)
		}
		wantGrade, evaluateErr := grading.EvaluateDeterministic(ctx, prepared.admittedGrading, evidence)
		evidence.Destroy()
		if evaluateErr != nil || !reflect.DeepEqual(wantGrade, *artifacts.GradeReceipt) {
			return "", "", nil, sequentialReferenceError("grade_receipt_projection", evaluateErr)
		}
		gradeSHA, err = grading.ReceiptSHA256(prepared.gradingPlan, *artifacts.GradeReceipt)
		if err != nil {
			return "", "", nil, sequentialReferenceError("grade_receipt_binding", err)
		}
	}
	return observationSHA, gradeSHA, executionReceiptData, nil
}

func validateSequentialReferenceArtifactChain(ctx context.Context, manifest experiment.Manifest, assignment sequentialReferenceAssignment, baseBinding lifecycle.Binding,
	inspection AttemptLedgerInspection, artifacts SequentialReferenceTrialArtifacts, executionReceiptData []byte,
) error {
	if artifacts.Observation == nil || artifacts.ExecutionReceipt == nil || artifacts.GradeReceipt == nil ||
		assignment.TrialID != artifacts.TrialRecord.TrialID || assignment.BlockID != artifacts.TrialRecord.BlockID ||
		assignment.TreatmentID != artifacts.TrialRecord.TreatmentID || artifacts.AttemptPlan.AttemptID != artifacts.Observation.AttemptID ||
		artifacts.AttemptPlan.PlanSHA256 != artifacts.TrialRecord.AttemptPlanSHA256 ||
		artifacts.LifecycleEvent.EventSHA256 != artifacts.TrialRecord.LifecycleEventSHA256 ||
		!reflect.DeepEqual(artifacts.LifecycleEvent, inspection.Events[len(inspection.Events)-1]) {
		return sequentialReferenceError("artifact_chain", nil)
	}
	treatment, ok := sequentialReferenceTreatmentByID(manifest, assignment.TreatmentID)
	if !ok {
		return sequentialReferenceError("artifact_treatment", nil)
	}
	backend, err := executionbackend.ReferenceContract()
	if err != nil {
		return err
	}
	admittedExecution, err := executionbackend.Admit(backend, artifacts.ExecutionPlan)
	if err != nil || admittedExecution.SHA256() != treatment.ExecutionBindingSHA256 ||
		!sequentialReferenceExecutionPlanSupported(manifest, treatment, artifacts.ExecutionPlan) {
		return sequentialReferenceError("execution_binding", err)
	}
	wantGrading, err := NewSequentialReferenceGradingPlan(artifacts.GradingPlan.InputProjectionSHA256)
	if err != nil || !reflect.DeepEqual(wantGrading, artifacts.GradingPlan) {
		return sequentialReferenceError("grading_profile", err)
	}
	gradingSHA, err := grading.PlanSHA256(artifacts.GradingPlan)
	if err != nil || gradingSHA != manifest.Design.Case.GradingPlanSHA256 {
		return sequentialReferenceError("grading_binding", err)
	}
	wantBinding, err := BindExecutionBackendTrial(baseBinding, artifacts.ExecutionPlan)
	if err == nil {
		wantBinding, err = BindGradingPlan(wantBinding, artifacts.GradingPlan)
	}
	if err != nil || !reflect.DeepEqual(wantBinding, artifacts.AttemptPlan.Binding) {
		return sequentialReferenceError("attempt_binding", err)
	}
	adapter, err := agentadapter.ReferenceContract()
	if err != nil {
		return err
	}
	wantObservation, err := agentadapter.NewReferenceObservation(adapter, artifacts.AttemptPlan.AttemptID, treatment.ExpectedActivation)
	if err != nil || !reflect.DeepEqual(wantObservation, *artifacts.Observation) {
		return sequentialReferenceError("observation_projection", err)
	}
	observationSHA, err := agentadapter.ObservationSHA256(adapter, *artifacts.Observation)
	if err != nil || observationSHA != artifacts.TrialRecord.AgentObservationSHA256 {
		return sequentialReferenceError("observation_binding", err)
	}
	grader, err := grading.BuiltinContract()
	if err != nil {
		return err
	}
	admittedGrading, err := grading.Admit(grader, artifacts.GradingPlan)
	if err != nil {
		return sequentialReferenceError("grading_admission", err)
	}
	preparedEvidence, err := grading.PrepareEvidence(ctx, admittedGrading, grading.EvidenceSet{
		InputProjectionSHA256: artifacts.GradingPlan.InputProjectionSHA256,
		Files: []grading.FileEvidence{{
			ID: "execution-receipt", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o600, Data: executionReceiptData,
		}},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{},
	})
	if err != nil {
		return sequentialReferenceError("grading_evidence", err)
	}
	defer preparedEvidence.Destroy()
	wantGrade, err := grading.EvaluateDeterministic(ctx, admittedGrading, preparedEvidence)
	if err != nil || !reflect.DeepEqual(wantGrade, *artifacts.GradeReceipt) {
		return sequentialReferenceError("grade_receipt_projection", err)
	}
	gradeSHA, err := grading.ReceiptSHA256(artifacts.GradingPlan, *artifacts.GradeReceipt)
	if err != nil || gradeSHA != artifacts.TrialRecord.GradeReceiptSHA256 || gradeSHA != artifacts.LifecycleEvent.Evidence.ReceiptSHA256 {
		return sequentialReferenceError("grade_receipt_binding", err)
	}
	passed := artifacts.ExecutionReceipt.Verdict == executionbackend.VerdictSucceeded && sequentialReferenceGradePassed(*artifacts.GradeReceipt)
	wantState, wantLifecycle := experiment.LifecycleFailed, lifecycle.StateFailed
	if passed {
		wantState, wantLifecycle = experiment.LifecycleSucceeded, lifecycle.StateSucceeded
	}
	wantStages, wantMetrics, err := sequentialReferenceObservedProjections(manifest, *artifacts.Observation, passed)
	if err != nil {
		return err
	}
	if artifacts.TrialRecord.LifecycleState != wantState || artifacts.LifecycleEvent.To != wantLifecycle ||
		artifacts.TrialRecord.Eligibility != experiment.EligibilitySupported || artifacts.TrialRecord.Exclusion != experiment.ExclusionNone ||
		!reflect.DeepEqual(artifacts.TrialRecord.Stages, wantStages) || !reflect.DeepEqual(artifacts.TrialRecord.Metrics, wantMetrics) ||
		!sequentialReferenceObservedOutcome(artifacts.TrialRecord, passed) {
		return sequentialReferenceError("trial_projection", nil)
	}
	return nil
}

func sequentialReferenceObservedOutcome(record experiment.TrialRecord, passed bool) bool {
	if len(record.Metrics) != 1 || record.Metrics[0].Metric != experiment.MetricOutcome ||
		record.Metrics[0].Presence != experiment.PresenceObserved || record.Metrics[0].Value == nil {
		return false
	}
	want := uint64(0)
	if passed {
		want = 1
	}
	return *record.Metrics[0].Value == want
}
