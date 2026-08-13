package agenteval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

const (
	sequentialReferenceMarkerName           = ".sequential-reference-incomplete"
	sequentialReferenceManifestName         = "manifest.json"
	sequentialReferenceSchedulerPlanName    = "scheduler-plan.json"
	sequentialReferenceSchedulerReportName  = "scheduler-report.json"
	sequentialReferenceLedgerDirectory      = "attempt-ledger"
	sequentialReferenceTrialsDirectory      = "trials"
	sequentialReferenceObservationName      = "agent-observation.json"
	sequentialReferenceExecutionPlanName    = "execution-plan.json"
	sequentialReferenceExecutionReceiptName = "execution-receipt.json"
	sequentialReferenceGradingPlanName      = "grading-plan.json"
	sequentialReferenceGradeReceiptName     = "grade-receipt.json"
	sequentialReferenceTrialRecordName      = "trial-record.json"
)

type sequentialReferencePublication struct {
	destination string
	manifest    experiment.Manifest
	parentPath  string
	base        string
	parentInfo  fs.FileInfo
	createdInfo fs.FileInfo
	parent      *os.Root
	root        *os.Root
	activeLock  *hardenedFileLock
	testHook    func(string) error
}

// RunSequentialReferenceToNewDestination pre-admits all inputs before creating
// an exact new destination. Once its marker is established, a failed or
// interrupted run retains it and any durable ledger state; this entry point
// never resumes or replays that destination.
func RunSequentialReferenceToNewDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle,
) (SequentialReferenceResult, error) {
	return runScheduledReferenceToNewDestination(ctx, destination, manifest, bundle, SequentialReferenceRunOptions{Workers: 1}, true)
}

// RunScheduledReferenceToNewDestination preserves the exact new-publication
// contract while executing independent manifest blocks through the admitted
// local scheduler.
func RunScheduledReferenceToNewDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle, options SequentialReferenceRunOptions,
) (SequentialReferenceResult, error) {
	return runScheduledReferenceToNewDestination(ctx, destination, manifest, bundle, options, false)
}

// ResumeSequentialReferenceAtDestination resumes only the planned complement
// of an incomplete one-worker publication. Every durable non-planned attempt is
// absorbing; a nonterminal crash tail is first closed as unknown and is never
// executed again.
func ResumeSequentialReferenceAtDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle,
) (SequentialReferenceResult, error) {
	return resumeScheduledReferenceAtDestination(ctx, destination, manifest, bundle, SequentialReferenceRunOptions{Workers: 1})
}

// ResumeScheduledReferenceAtDestination applies the same no-replay recovery
// rule while preserving the exact scheduler plan selected for the publication.
func ResumeScheduledReferenceAtDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle, options SequentialReferenceRunOptions,
) (SequentialReferenceResult, error) {
	return resumeScheduledReferenceAtDestination(ctx, destination, manifest, bundle, options)
}

func runScheduledReferenceToNewDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle, options SequentialReferenceRunOptions, commitCurrentOnCancellation bool,
) (SequentialReferenceResult, error) {
	if err := validateSequentialReferenceRunOptions(options); err != nil {
		return SequentialReferenceResult{}, err
	}
	prepared, err := prepareSequentialReference(ctx, manifest, bundle)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer prepared.destroy()
	if runtime.GOOS == "windows" {
		return SequentialReferenceResult{}, unsupportedSequentialReference("attempt_ledger", ErrAttemptLedgerUnsupported)
	}
	manifestData, err := experiment.EncodeManifest(prepared.manifest)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("manifest_encode", err)
	}
	publication, err := createSequentialReferencePublication(destination, prepared.manifest, manifestData, options.Workers)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer publication.close()
	store, err := CreateSequentialReferenceAttemptStore(filepath.Join(destination, sequentialReferenceLedgerDirectory), prepared.manifest)
	if err != nil || !publication.stable() {
		return SequentialReferenceResult{}, unknownSequentialReference("attempt_store_create", err)
	}
	result, runErr := prepared.runScheduled(ctx, store, options, nil, nil, commitCurrentOnCancellation,
		publication.writeSchedulerPlan, publication.stageTrial, publication.writeTrial)
	if runErr != nil {
		return result, unknownSequentialReference("run", runErr)
	}
	if err := publication.finish(prepared.manifest, store, result); err != nil {
		return result, unknownSequentialReference("finish", err)
	}
	return result, nil
}

func resumeScheduledReferenceAtDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle, options SequentialReferenceRunOptions,
) (SequentialReferenceResult, error) {
	if err := validateSequentialReferenceRunOptions(options); err != nil {
		return SequentialReferenceResult{}, err
	}
	prepared, err := prepareSequentialReference(ctx, manifest, bundle)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer prepared.destroy()
	if runtime.GOOS == "windows" {
		return SequentialReferenceResult{}, unsupportedSequentialReference("attempt_ledger", ErrAttemptLedgerUnsupported)
	}
	publication, err := openSequentialReferencePublication(destination)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer publication.close()
	if err := publication.acquireActiveLock(); err != nil {
		if errors.Is(err, ErrAttemptLedgerBusy) {
			return SequentialReferenceResult{}, unknownSequentialReference("resume_active", err)
		}
		return SequentialReferenceResult{}, sequentialReferenceError("resume_marker", err)
	}
	marker, err := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceMarkerName, 128)
	if err != nil || !bytes.Equal(marker, sequentialReferenceMarker(prepared.manifest.ManifestSHA256, options.Workers)) {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_marker", err)
	}
	manifestData, err := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceManifestName, experiment.MaxManifestBytes)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_manifest", err)
	}
	storedManifest, err := experiment.DecodeManifest(bytes.NewReader(manifestData))
	if err != nil || !reflect.DeepEqual(storedManifest, prepared.manifest) {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_manifest_binding", err)
	}
	publication.manifest = prepared.manifest
	assignments := sequentialReferenceAssignments(prepared.manifest)
	if err := publication.validateResumeShapeContext(ctx, len(assignments)); err != nil {
		return SequentialReferenceResult{}, err
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return SequentialReferenceResult{}, err
	}
	store, err := CreateSequentialReferenceAttemptStore(filepath.Join(destination, sequentialReferenceLedgerDirectory), prepared.manifest)
	if err != nil || !publication.stable() {
		return SequentialReferenceResult{}, unknownSequentialReference("resume_attempt_store", err)
	}
	roster, err := prepared.prepareScheduledRosterWithSink(store, options, publication.writeSchedulerPlan)
	if err != nil {
		return SequentialReferenceResult{}, unknownSequentialReference("resume_roster", err)
	}
	inspections, err := store.InspectAllContext(ctx)
	if err != nil || len(inspections) != len(roster.plans) {
		return SequentialReferenceResult{}, unknownSequentialReference("resume_inspection", err)
	}
	reportInfo, reportStatErr := publication.root.Lstat(sequentialReferenceSchedulerReportName)
	reportExists := reportStatErr == nil
	if reportStatErr != nil && !errors.Is(reportStatErr, fs.ErrNotExist) {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_scheduler_report", reportStatErr)
	}
	if reportExists {
		if !reportInfo.Mode().IsRegular() || reportInfo.Mode()&fs.ModeSymlink != 0 || reportInfo.Mode().Perm() != 0o600 {
			return SequentialReferenceResult{}, sequentialReferenceError("resume_scheduler_report", nil)
		}
		data, readErr := readSequentialReferenceFileContext(ctx, publication.root, sequentialReferenceSchedulerReportName, scheduler.MaxReportBytes)
		if readErr != nil {
			return SequentialReferenceResult{}, sequentialReferenceError("resume_scheduler_report", readErr)
		}
		if _, decodeErr := scheduler.DecodeReport(bytes.NewReader(data), roster.schedule); decodeErr != nil {
			return SequentialReferenceResult{}, sequentialReferenceError("resume_scheduler_report", decodeErr)
		}
		for _, inspection := range inspections {
			if !inspection.Complete || !inspection.Projection.Terminal {
				return SequentialReferenceResult{}, sequentialReferenceError("resume_report_before_terminal", nil)
			}
		}
	}
	if !sequentialReferenceStartedAttemptsAreSchedulePrefix(roster, inspections) {
		return SequentialReferenceResult{}, unknownSequentialReference("resume_attempt_order", ErrAttemptLedgerConflict)
	}
	validator, err := experiment.NewTrialRecordValidator(prepared.manifest)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_trial_validator", err)
	}
	if len(roster.baseBindings) != len(roster.assignments) {
		return SequentialReferenceResult{}, sequentialReferenceError("resume_attempt_bindings", nil)
	}
	existing := make([]SequentialReferenceTrialArtifacts, len(roster.plans))
	terminalByOrdinal := make(map[uint32]scheduler.TerminalTask, len(roster.plans))
	for index := range inspections {
		if err := sequentialReferenceInspectionContextError(ctx); err != nil {
			return SequentialReferenceResult{}, unknownSequentialReference("resume_interrupted", err)
		}
		inspection := inspections[index]
		if !inspection.Complete || inspection.Plan.PlanSHA256 != roster.plans[index].PlanSHA256 {
			return SequentialReferenceResult{}, unknownSequentialReference("resume_attempt", ErrAttemptLedgerIncomplete)
		}
		if inspection.Projection.State == lifecycle.StatePlanned {
			if exists, existsErr := publication.trialStageExists(inspection.Plan.Ordinal); existsErr != nil || exists {
				return SequentialReferenceResult{}, unknownSequentialReference("resume_planned_stage", existsErr)
			}
			continue
		}
		if !inspection.Projection.Terminal {
			session := &DurableAttemptSession{store: store, plan: inspection.Plan}
			if err := session.Unknown(lifecycle.ErrorInternal, inspection.Projection.Usage); err != nil {
				return SequentialReferenceResult{}, unknownSequentialReference("resume_absorb", err)
			}
			inspection, err = store.Inspect(inspection.Plan.AttemptID)
			if err != nil || !inspection.Complete || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateUnknown {
				return SequentialReferenceResult{}, unknownSequentialReference("resume_absorb_readback", err)
			}
			inspections[index] = inspection
		}
		artifacts, recoverErr := publication.recoverTerminalTrialContext(ctx, prepared, validator,
			roster.assignments[index], roster.baseBindings[index], inspection)
		if recoverErr != nil {
			return SequentialReferenceResult{}, unknownSequentialReference("resume_trial", recoverErr)
		}
		if err := publication.writeTrial(index, artifacts); err != nil {
			return SequentialReferenceResult{}, unknownSequentialReference("resume_trial_write", err)
		}
		existing[index] = artifacts
		terminalByOrdinal[inspection.Plan.Ordinal] = scheduler.TerminalTask{
			TaskSHA256: inspection.Plan.PlanSHA256, Outcome: sequentialReferenceSchedulerOutcome(artifacts),
		}
	}
	terminal := make([]scheduler.TerminalTask, 0, len(terminalByOrdinal))
	for _, task := range roster.schedule.Tasks {
		if item, ok := terminalByOrdinal[task.Ordinal]; ok {
			terminal = append(terminal, item)
		}
	}
	result, runErr := prepared.runPreparedSchedule(ctx, store, roster, terminal, existing, false,
		publication.stageTrial, publication.writeTrial)
	if runErr != nil {
		return result, unknownSequentialReference("resume_run", runErr)
	}
	if err := publication.finish(prepared.manifest, store, result); err != nil {
		return result, unknownSequentialReference("resume_finish", err)
	}
	return result, nil
}

// InspectSequentialReferencePublication strictly decodes one complete output
// tree and independently replays every transitive artifact binding. A marker,
// missing member, unknown file, alias, or noncanonical artifact is rejected.

func createSequentialReferencePublication(destination string, manifest experiment.Manifest, manifestData []byte,
	workers uint32,
) (*sequentialReferencePublication, error) {
	if workers == 0 || workers > scheduler.MaxWorkers {
		return nil, sequentialReferenceError("scheduler_options", nil)
	}
	publication, err := prepareSequentialReferencePublicationPath(destination, true)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			publication.close()
		}
	}()
	publication.manifest = manifest
	if err := writeSequentialReferenceFile(publication.root, sequentialReferenceMarkerName,
		sequentialReferenceMarker(manifest.ManifestSHA256, workers)); err != nil {
		return nil, unknownSequentialReference("publication_marker", err)
	}
	if err := publication.acquireActiveLock(); err != nil {
		return nil, unknownSequentialReference("publication_lock", err)
	}
	if err := writeSequentialReferenceFile(publication.root, sequentialReferenceManifestName, manifestData); err != nil {
		return nil, unknownSequentialReference("manifest_write", err)
	}
	if err := publication.root.Mkdir(sequentialReferenceTrialsDirectory, 0o700); err != nil {
		return nil, unknownSequentialReference("trials_create", err)
	}
	if err := syncSequentialReferenceDirectory(publication.root, "."); err != nil || !publication.stable() {
		return nil, unknownSequentialReference("publication_create", err)
	}
	if err := publication.syncParent(); err != nil || !publication.stable() {
		return nil, unknownSequentialReference("publication_parent_sync", err)
	}
	failed = false
	return publication, nil
}

func openSequentialReferencePublication(destination string) (*sequentialReferencePublication, error) {
	return prepareSequentialReferencePublicationPath(destination, false)
}

func prepareSequentialReferencePublicationPath(destination string, create bool) (*sequentialReferencePublication, error) {
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return nil, sequentialReferenceError("destination", nil)
	}
	parentPath, base := filepath.Dir(destination), filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return nil, sequentialReferenceError("destination", nil)
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, sequentialReferenceError("destination_parent", err)
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, sequentialReferenceError("destination_parent", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = parent.Close()
		}
	}()
	if !sequentialReferenceRootStable(parentPath, parentInfo, parent) {
		return nil, sequentialReferenceError("destination_parent_changed", nil)
	}
	created := false
	if create {
		if _, err := parent.Lstat(base); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return nil, sequentialReferenceError("destination_exists", err)
		}
		if err := parent.Mkdir(base, 0o700); err != nil {
			return nil, sequentialReferenceError("destination_create", err)
		}
		created = true
	}
	createdInfo, err := parent.Lstat(base)
	if err != nil || !createdInfo.IsDir() || createdInfo.Mode()&fs.ModeSymlink != 0 || createdInfo.Mode().Perm() != 0o700 {
		return nil, sequentialReferencePublicationPathError("destination_shape", err, created)
	}
	root, err := parent.OpenRoot(base)
	if err != nil {
		return nil, sequentialReferencePublicationPathError("destination_open", err, created)
	}
	publication := &sequentialReferencePublication{destination: destination, parentPath: parentPath, base: base,
		parentInfo: parentInfo, createdInfo: createdInfo, parent: parent, root: root}
	if !publication.stable() {
		_ = root.Close()
		return nil, sequentialReferencePublicationPathError("destination_changed", nil, created)
	}
	failed = false
	return publication, nil
}

func sequentialReferencePublicationPathError(code string, cause error, created bool) error {
	if created {
		return unknownSequentialReference(code, cause)
	}
	return sequentialReferenceError(code, cause)
}

func (publication *sequentialReferencePublication) close() {
	if publication == nil {
		return
	}
	if publication.activeLock != nil {
		_ = publication.activeLock.Unlock()
		publication.activeLock = nil
	}
	if publication.root != nil {
		_ = publication.root.Close()
		publication.root = nil
	}
	if publication.parent != nil {
		_ = publication.parent.Close()
		publication.parent = nil
	}
}

func (publication *sequentialReferencePublication) stable() bool {
	if publication == nil || publication.parent == nil || publication.root == nil ||
		!sequentialReferenceRootStable(publication.parentPath, publication.parentInfo, publication.parent) {
		return false
	}
	ambient, ambientErr := publication.parent.Lstat(publication.base)
	opened, openedErr := publication.root.Stat(".")
	return ambientErr == nil && openedErr == nil && ambient.IsDir() && opened.IsDir() &&
		ambient.Mode()&fs.ModeSymlink == 0 && os.SameFile(publication.createdInfo, ambient) && os.SameFile(publication.createdInfo, opened)
}

func sequentialReferenceRootStable(path string, initial fs.FileInfo, root *os.Root) bool {
	ambient, ambientErr := os.Lstat(path)
	opened, openedErr := root.Stat(".")
	return ambientErr == nil && openedErr == nil && ambient.IsDir() && opened.IsDir() && ambient.Mode()&fs.ModeSymlink == 0 &&
		os.SameFile(initial, ambient) && os.SameFile(initial, opened)
}

func (publication *sequentialReferencePublication) writeTrial(index int, artifacts SequentialReferenceTrialArtifacts) error {
	if artifacts.TrialRecord.RecordSHA256 == "" {
		return sequentialReferenceError("trial_incomplete", nil)
	}
	return publication.writeTrialStage(index, artifacts)
}

func (publication *sequentialReferencePublication) stageTrial(index int, artifacts SequentialReferenceTrialArtifacts) error {
	return publication.writeTrialStage(index, artifacts)
}

func (publication *sequentialReferencePublication) writeTrialStage(index int, artifacts SequentialReferenceTrialArtifacts) error {
	if index < 0 || index >= lifecycle.MaxAttempts || !publication.stable() {
		return sequentialReferenceError("trial_order", nil)
	}
	ordinal := uint32(index + 1) // #nosec G115 -- the guard bounds index by lifecycle.MaxAttempts.
	if artifacts.AttemptPlan.Ordinal != ordinal {
		return sequentialReferenceError("trial_order", nil)
	}
	directory := filepath.Join(sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(artifacts.AttemptPlan.Ordinal))
	info, err := publication.root.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		if err := publication.root.Mkdir(directory, 0o700); err != nil {
			return err
		}
		info, err = publication.root.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return sequentialReferenceError("trial_directory", err)
	}
	files, err := encodeSequentialReferenceTrialStage(publication.manifest, artifacts)
	if err != nil {
		return err
	}
	for _, file := range files {
		name := filepath.Join(directory, file.name)
		existingInfo, existingErr := publication.root.Lstat(name)
		if existingErr == nil {
			existing, readErr := readSequentialReferenceFile(publication.root, name, int64(len(file.data)))
			if readErr != nil || !existingInfo.Mode().IsRegular() || existingInfo.Mode().Perm() != 0o600 ||
				existingInfo.Mode()&fs.ModeSymlink != 0 || !bytes.Equal(existing, file.data) {
				return sequentialReferenceError("trial_stage_drift", readErr)
			}
			continue
		}
		if !errors.Is(existingErr, fs.ErrNotExist) {
			return existingErr
		}
		if err := writeSequentialReferenceFile(publication.root, name, file.data); err != nil {
			return err
		}
	}
	existing, err := readSequentialReferenceDirectoryContext(context.Background(), publication.root, directory, 7)
	want := make([]string, len(files))
	for index := range files {
		want[index] = files[index].name
	}
	sort.Strings(want)
	if err != nil || !reflect.DeepEqual(existing, want) {
		return sequentialReferenceError("trial_stage_shape", err)
	}
	if err := syncSequentialReferenceDirectory(publication.root, directory); err != nil {
		return err
	}
	return syncSequentialReferenceDirectory(publication.root, sequentialReferenceTrialsDirectory)
}

func (publication *sequentialReferencePublication) trialStageExists(ordinal uint32) (bool, error) {
	if ordinal == 0 || ordinal > lifecycle.MaxAttempts {
		return false, sequentialReferenceError("trial_order", nil)
	}
	directory := filepath.Join(sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(ordinal))
	info, err := publication.root.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return false, sequentialReferenceError("trial_directory", err)
	}
	return true, nil
}

func (publication *sequentialReferencePublication) writeSchedulerPlan(plan scheduler.Plan) error {
	if !publication.stable() {
		return sequentialReferenceError("scheduler_publication", nil)
	}
	data, err := scheduler.EncodePlan(plan)
	if err != nil {
		return sequentialReferenceError("scheduler_plan_encode", err)
	}
	if err := writeOrValidateSequentialReferenceFile(publication.root, sequentialReferenceSchedulerPlanName, data); err != nil {
		return err
	}
	return syncSequentialReferenceDirectory(publication.root, ".")
}

type sequentialReferenceFile struct {
	name string
	data []byte
}

func encodeSequentialReferenceTrialStage(manifest experiment.Manifest, artifacts SequentialReferenceTrialArtifacts) ([]sequentialReferenceFile, error) {
	executionPlan, err := executionbackend.EncodePlan(artifacts.ExecutionPlan)
	if err != nil {
		return nil, err
	}
	gradingPlan, err := grading.EncodePlan(artifacts.GradingPlan)
	if err != nil {
		return nil, err
	}
	files := []sequentialReferenceFile{
		{name: sequentialReferenceExecutionPlanName, data: executionPlan},
		{name: sequentialReferenceGradingPlanName, data: gradingPlan},
	}
	if artifacts.TrialRecord.RecordSHA256 != "" {
		trialRecord, encodeErr := experiment.EncodeTrialRecord(manifest, artifacts.TrialRecord)
		if encodeErr != nil {
			return nil, sequentialReferenceError("trial_record_encode", encodeErr)
		}
		files = append(files, sequentialReferenceFile{name: sequentialReferenceTrialRecordName, data: trialRecord})
	}
	if artifacts.Observation != nil {
		adapter, contractErr := agentadapter.ReferenceContract()
		if contractErr != nil {
			return nil, contractErr
		}
		observation, encodeErr := agentadapter.EncodeObservation(adapter, *artifacts.Observation)
		if encodeErr != nil {
			return nil, encodeErr
		}
		files = append(files, sequentialReferenceFile{name: sequentialReferenceObservationName, data: observation})
	}
	if artifacts.ExecutionReceipt != nil {
		executionReceipt, encodeErr := executionbackend.EncodeReceipt(artifacts.ExecutionPlan, *artifacts.ExecutionReceipt)
		if encodeErr != nil {
			return nil, encodeErr
		}
		files = append(files, sequentialReferenceFile{name: sequentialReferenceExecutionReceiptName, data: executionReceipt})
	}
	if artifacts.GradeReceipt != nil {
		gradeReceipt, encodeErr := grading.EncodeReceipt(artifacts.GradingPlan, *artifacts.GradeReceipt)
		if encodeErr != nil {
			return nil, encodeErr
		}
		files = append(files, sequentialReferenceFile{name: sequentialReferenceGradeReceiptName, data: gradeReceipt})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func (publication *sequentialReferencePublication) finish(manifest experiment.Manifest, store *AttemptLedgerStore, result SequentialReferenceResult) error {
	if !reflect.DeepEqual(publication.manifest, manifest) || result.ManifestSHA256 != manifest.ManifestSHA256 ||
		len(result.Trials) == 0 || !publication.stable() {
		return sequentialReferenceError("publication_binding", nil)
	}
	assignments := sequentialReferenceAssignments(manifest)
	if len(assignments) != len(result.Trials) {
		return sequentialReferenceError("publication_roster", nil)
	}
	recordValidator, err := experiment.NewTrialRecordValidator(manifest)
	if err != nil {
		return sequentialReferenceError("publication_trial_validator", err)
	}
	baseBindings, err := ExperimentAttemptBindings(manifest)
	if err != nil || len(baseBindings) != len(assignments) {
		return sequentialReferenceError("publication_attempt_bindings", err)
	}
	scheduleData, err := readSequentialReferenceFile(publication.root, sequentialReferenceSchedulerPlanName, scheduler.MaxPlanBytes)
	if err != nil {
		return sequentialReferenceError("scheduler_plan_read", err)
	}
	schedule, err := scheduler.DecodePlan(bytes.NewReader(scheduleData))
	if err != nil || scheduler.ValidateReport(schedule, result.Scheduler) != nil {
		return sequentialReferenceError("scheduler_binding", err)
	}
	reportData, err := scheduler.EncodeReport(schedule, result.Scheduler)
	if err != nil {
		return sequentialReferenceError("scheduler_report_encode", err)
	}
	if err := writeOrValidateSequentialReferenceFile(publication.root, sequentialReferenceSchedulerReportName, reportData); err != nil {
		return sequentialReferenceError("scheduler_report_write", err)
	}
	if err := syncSequentialReferenceDirectory(publication.root, "."); err != nil {
		return sequentialReferenceError("scheduler_report_sync", err)
	}
	if err := publication.validateCompletedReadback(manifest, store, result, schedule,
		recordValidator, assignments, baseBindings, false); err != nil {
		return err
	}
	if err := syncSequentialReferenceDirectory(publication.root, "."); err != nil {
		return sequentialReferenceError("publication_sync", err)
	}
	if err := publication.syncParent(); err != nil {
		return sequentialReferenceError("publication_parent_sync", err)
	}
	if !publication.stable() {
		return sequentialReferenceError("publication_changed", nil)
	}
	if err := publication.validateShape(len(result.Trials), true); err != nil {
		return sequentialReferenceError("publication_shape", err)
	}
	if publication.testHook != nil {
		if err := publication.testHook("before_marker_remove"); err != nil {
			return sequentialReferenceError("publication_complete", err)
		}
	}
	// Repeat strict content readback after the final fault boundary: residue or
	// replaced bytes retain the marker instead of certifying a rejected tree.
	if err := publication.validateCompletedReadback(manifest, store, result, schedule,
		recordValidator, assignments, baseBindings, true); err != nil {
		return err
	}
	// Removing the already-durable incomplete marker is the final
	// process-visible commit. There are deliberately no fallible operations
	// after it: a crash may conservatively retain the marker, but an error can
	// never be returned with a markerless publication.
	if err := publication.root.Remove(sequentialReferenceMarkerName); err != nil {
		return sequentialReferenceError("publication_complete", err)
	}
	return nil
}

func (publication *sequentialReferencePublication) syncParent() error {
	parentDirectory, err := publication.parent.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(parentDirectory.Sync(), parentDirectory.Close())
}

func writeSequentialReferenceFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written := 0
	for written < len(data) {
		count, writeErr := file.Write(data[written:])
		if writeErr != nil || count == 0 {
			_ = file.Close()
			return fmt.Errorf("sequential reference write")
		}
		written += count
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeOrValidateSequentialReferenceFile(root *os.Root, name string, data []byte) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return writeSequentialReferenceFile(root, name, data)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return sequentialReferenceError("artifact_shape", err)
	}
	existing, err := readSequentialReferenceFile(root, name, int64(len(data)))
	if err != nil || !bytes.Equal(existing, data) {
		return sequentialReferenceError("artifact_drift", err)
	}
	return nil
}

func readSequentialReferenceFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	return readSequentialReferenceFileContext(context.Background(), root, name, maximum)
}

func readSequentialReferenceFileContext(ctx context.Context, root *os.Root, name string, maximum int64) ([]byte, error) {
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return nil, err
	}
	ambientBefore, err := root.Lstat(name)
	if err != nil || !ambientBefore.Mode().IsRegular() || ambientBefore.Mode()&fs.ModeSymlink != 0 ||
		ambientBefore.Mode().Perm() != 0o600 || ambientBefore.Size() < 0 || ambientBefore.Size() > maximum {
		return nil, sequentialReferenceError("artifact_shape", err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > maximum ||
		!os.SameFile(ambientBefore, info) {
		return nil, sequentialReferenceError("artifact_shape", err)
	}
	data := []byte{}
	chunk := make([]byte, 64<<10)
	for {
		if err := sequentialReferenceInspectionContextError(ctx); err != nil {
			return nil, err
		}
		count, readErr := file.Read(chunk)
		if count > 0 {
			if int64(len(data))+int64(count) > maximum {
				return nil, sequentialReferenceError("artifact_read", nil)
			}
			data = append(data, chunk[:count]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || count == 0 {
			return nil, sequentialReferenceError("artifact_read", readErr)
		}
	}
	if int64(len(data)) != info.Size() {
		return nil, sequentialReferenceError("artifact_read", nil)
	}
	openedAfter, openedErr := file.Stat()
	ambientAfter, ambientErr := root.Lstat(name)
	if openedErr != nil || ambientErr != nil || !ambientAfter.Mode().IsRegular() || ambientAfter.Mode()&fs.ModeSymlink != 0 ||
		ambientAfter.Mode().Perm() != 0o600 || ambientAfter.Size() != int64(len(data)) ||
		!os.SameFile(info, openedAfter) || !os.SameFile(info, ambientAfter) {
		return nil, sequentialReferenceError("artifact_changed", errors.Join(openedErr, ambientErr))
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return nil, err
	}
	return data, nil
}

func syncSequentialReferenceDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func (publication *sequentialReferencePublication) validateShape(trials int, marker bool) error {
	return publication.validateShapeContext(context.Background(), trials, marker)
}

func (publication *sequentialReferencePublication) validateShapeContext(ctx context.Context, trials int, marker bool) error {
	entries, err := readSequentialReferenceDirectoryContext(ctx, publication.root, ".", 7)
	if err != nil {
		return sequentialReferenceError("publication_shape", err)
	}
	wantTop := []string{sequentialReferenceLedgerDirectory, sequentialReferenceManifestName, sequentialReferenceSchedulerPlanName,
		sequentialReferenceSchedulerReportName, sequentialReferenceTrialsDirectory}
	if marker {
		wantTop = append(wantTop, sequentialReferenceMarkerName)
		sort.Strings(wantTop)
	}
	if !reflect.DeepEqual(entries, wantTop) {
		return sequentialReferenceError("publication_members", nil)
	}
	trialEntries, err := readSequentialReferenceDirectoryContext(ctx, publication.root, sequentialReferenceTrialsDirectory, trials+1)
	if err != nil || len(trialEntries) != trials {
		return sequentialReferenceError("trial_members", err)
	}
	for index, name := range trialEntries {
		if name != attemptLedgerOrdinalName(uint32(index+1)) {
			return sequentialReferenceError("trial_order", nil)
		}
	}
	return nil
}

func (publication *sequentialReferencePublication) validateResumeShapeContext(ctx context.Context, trials int) error {
	if trials <= 0 || trials > lifecycle.MaxAttempts {
		return sequentialReferenceError("resume_roster", nil)
	}
	entries, err := readSequentialReferenceDirectoryContext(ctx, publication.root, ".", 8)
	if err != nil {
		return sequentialReferenceError("resume_shape", err)
	}
	allowed := map[string]bool{
		sequentialReferenceMarkerName:          true,
		sequentialReferenceManifestName:        true,
		sequentialReferenceSchedulerPlanName:   true,
		sequentialReferenceSchedulerReportName: true,
		sequentialReferenceLedgerDirectory:     true,
		sequentialReferenceTrialsDirectory:     true,
	}
	present := make(map[string]bool, len(entries))
	for _, name := range entries {
		if !allowed[name] {
			return sequentialReferenceError("resume_members", nil)
		}
		present[name] = true
	}
	if !present[sequentialReferenceMarkerName] || !present[sequentialReferenceManifestName] ||
		!present[sequentialReferenceTrialsDirectory] || (present[sequentialReferenceSchedulerReportName] && !present[sequentialReferenceSchedulerPlanName]) {
		return sequentialReferenceError("resume_members", nil)
	}
	trialEntries, err := readSequentialReferenceDirectoryContext(ctx, publication.root, sequentialReferenceTrialsDirectory, trials+1)
	if err != nil {
		return sequentialReferenceError("resume_trial_members", err)
	}
	valid := make(map[string]bool, trials)
	for ordinal := 1; ordinal <= trials; ordinal++ {
		valid[attemptLedgerOrdinalName(uint32(ordinal))] = true // #nosec G115 -- trials is bounded by MaxAttempts.
	}
	for _, name := range trialEntries {
		if !valid[name] {
			return sequentialReferenceError("resume_trial_order", nil)
		}
	}
	return nil
}

func readSequentialReferenceDirectoryContext(ctx context.Context, root *os.Root, name string, maximum int) ([]string, error) {
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return nil, err
	}
	if maximum < 1 {
		return nil, sequentialReferenceError("directory_limit", nil)
	}
	directory, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maximum)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) == maximum {
		return nil, sequentialReferenceError("directory_limit", nil)
	}
	if err := sequentialReferenceInspectionContextError(ctx); err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, sequentialReferenceError("symbolic_link", nil)
		}
		names[index] = entry.Name()
	}
	sort.Strings(names)
	return names, nil
}
