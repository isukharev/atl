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
)

const (
	sequentialReferenceMarkerName           = ".sequential-reference-incomplete"
	sequentialReferenceManifestName         = "manifest.json"
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
	testHook    func(string) error
}

// RunSequentialReferenceToNewDestination pre-admits all inputs before creating
// an exact new destination. Once its marker is established, a failed or
// interrupted run retains it and any durable ledger state; this entry point
// never resumes or replays that destination.
func RunSequentialReferenceToNewDestination(ctx context.Context, destination string, manifest ExperimentManifest,
	bundle SequentialReferenceBundle,
) (SequentialReferenceResult, error) {
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
	publication, err := createSequentialReferencePublication(destination, prepared.manifest, manifestData)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer publication.close()
	store, err := CreateSequentialReferenceAttemptStore(filepath.Join(destination, sequentialReferenceLedgerDirectory), prepared.manifest)
	if err != nil || !publication.stable() {
		return SequentialReferenceResult{}, unknownSequentialReference("attempt_store_create", err)
	}
	result, runErr := prepared.run(ctx, store, publication.writeTrial)
	if runErr != nil {
		return result, unknownSequentialReference("run", runErr)
	}
	if err := publication.finish(prepared.manifest, store, result); err != nil {
		return result, unknownSequentialReference("finish", err)
	}
	return result, nil
}

// InspectSequentialReferencePublication strictly decodes one complete output
// tree and independently replays every transitive artifact binding. A marker,
// missing member, unknown file, alias, or noncanonical artifact is rejected.
func InspectSequentialReferencePublication(destination string) (SequentialReferenceResult, error) {
	publication, err := openSequentialReferencePublication(destination)
	if err != nil {
		return SequentialReferenceResult{}, err
	}
	defer publication.close()
	if _, err := publication.root.Lstat(sequentialReferenceMarkerName); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return SequentialReferenceResult{}, sequentialReferenceError("publication_incomplete", err)
	}
	manifestData, err := readSequentialReferenceFile(publication.root, sequentialReferenceManifestName, experiment.MaxManifestBytes)
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("manifest_read", err)
	}
	manifest, err := experiment.DecodeManifest(bytes.NewReader(manifestData))
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("manifest_decode", err)
	}
	if err := validateSequentialReferenceManifestProfile(manifest); err != nil {
		return SequentialReferenceResult{}, err
	}
	store, err := OpenAttemptLedgerStore(filepath.Join(destination, sequentialReferenceLedgerDirectory))
	if err != nil {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_store_open", err)
	}
	inspections, err := store.InspectAll()
	assignments := sequentialReferenceAssignments(manifest)
	if err != nil || len(inspections) != len(assignments) {
		return SequentialReferenceResult{}, sequentialReferenceError("attempt_roster", err)
	}
	result := SequentialReferenceResult{ManifestSHA256: manifest.ManifestSHA256, Trials: make([]SequentialReferenceTrialArtifacts, len(assignments))}
	for index, assignment := range assignments {
		artifacts, readErr := publication.readTrial(manifest, assignment, inspections[index])
		if readErr != nil {
			return SequentialReferenceResult{}, readErr
		}
		result.Trials[index] = artifacts
	}
	if err := publication.validateShape(len(assignments), false); err != nil {
		return SequentialReferenceResult{}, err
	}
	if !publication.stable() {
		return SequentialReferenceResult{}, sequentialReferenceError("publication_changed", nil)
	}
	return result, nil
}

func createSequentialReferencePublication(destination string, manifest experiment.Manifest, manifestData []byte) (*sequentialReferencePublication, error) {
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
	if err := writeSequentialReferenceFile(publication.root, sequentialReferenceMarkerName, []byte(manifest.ManifestSHA256+"\n")); err != nil {
		return nil, unknownSequentialReference("publication_marker", err)
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
	if index < 0 || index >= lifecycle.MaxAttempts || !publication.stable() {
		return sequentialReferenceError("trial_order", nil)
	}
	ordinal := uint32(index + 1) // #nosec G115 -- the guard bounds index by lifecycle.MaxAttempts.
	if artifacts.AttemptPlan.Ordinal != ordinal {
		return sequentialReferenceError("trial_order", nil)
	}
	directory := filepath.Join(sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(artifacts.AttemptPlan.Ordinal))
	if err := publication.root.Mkdir(directory, 0o700); err != nil {
		return err
	}
	files, err := encodeSequentialReferenceTrial(publication.manifest, artifacts)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := writeSequentialReferenceFile(publication.root, filepath.Join(directory, file.name), file.data); err != nil {
			return err
		}
	}
	if err := syncSequentialReferenceDirectory(publication.root, directory); err != nil {
		return err
	}
	return syncSequentialReferenceDirectory(publication.root, sequentialReferenceTrialsDirectory)
}

type sequentialReferenceFile struct {
	name string
	data []byte
}

func encodeSequentialReferenceTrial(manifest experiment.Manifest, artifacts SequentialReferenceTrialArtifacts) ([]sequentialReferenceFile, error) {
	if artifacts.TrialRecord.RecordSHA256 == "" {
		return nil, sequentialReferenceError("trial_incomplete", nil)
	}
	executionPlan, err := executionbackend.EncodePlan(artifacts.ExecutionPlan)
	if err != nil {
		return nil, err
	}
	gradingPlan, err := grading.EncodePlan(artifacts.GradingPlan)
	if err != nil {
		return nil, err
	}
	trialRecord, err := experiment.EncodeTrialRecord(manifest, artifacts.TrialRecord)
	if err != nil {
		return nil, sequentialReferenceError("trial_record_encode", err)
	}
	files := []sequentialReferenceFile{
		{name: sequentialReferenceExecutionPlanName, data: executionPlan},
		{name: sequentialReferenceGradingPlanName, data: gradingPlan},
		{name: sequentialReferenceTrialRecordName, data: trialRecord},
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
	marker, err := readSequentialReferenceFile(publication.root, sequentialReferenceMarkerName, 128)
	if err != nil || !bytes.Equal(marker, []byte(manifest.ManifestSHA256+"\n")) {
		return sequentialReferenceError("publication_marker", err)
	}
	inspections, err := store.InspectAll()
	assignments := sequentialReferenceAssignments(manifest)
	if err != nil || len(inspections) != len(result.Trials) || len(assignments) != len(result.Trials) {
		return sequentialReferenceError("publication_roster", err)
	}
	for index := range result.Trials {
		read, readErr := publication.readTrial(manifest, assignments[index], inspections[index])
		if readErr != nil || !reflect.DeepEqual(read, result.Trials[index]) {
			return sequentialReferenceError("publication_readback", errors.Join(readErr, errSequentialReferenceArtifactDrift(read, result.Trials[index])))
		}
	}
	if err := publication.validateShape(len(result.Trials), true); err != nil || !publication.stable() {
		return sequentialReferenceError("publication_shape", err)
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

func errSequentialReferenceArtifactDrift(left, right SequentialReferenceTrialArtifacts) error {
	if reflect.DeepEqual(left, right) {
		return nil
	}
	return errors.New("artifact drift")
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

func (publication *sequentialReferencePublication) readTrial(manifest experiment.Manifest, assignment sequentialReferenceAssignment,
	inspection AttemptLedgerInspection,
) (SequentialReferenceTrialArtifacts, error) {
	if !inspection.Complete || !inspection.Projection.Terminal || len(inspection.Events) == 0 ||
		inspection.Plan.Ordinal == 0 || inspection.Plan.Ordinal > lifecycle.MaxAttempts {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("attempt_incomplete", nil)
	}
	directory := filepath.Join(sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(inspection.Plan.Ordinal))
	wantFiles := []string{
		sequentialReferenceObservationName,
		sequentialReferenceExecutionPlanName,
		sequentialReferenceExecutionReceiptName,
		sequentialReferenceGradeReceiptName,
		sequentialReferenceGradingPlanName,
		sequentialReferenceTrialRecordName,
	}
	files, err := readSequentialReferenceDirectory(publication.root, directory)
	if err != nil || !reflect.DeepEqual(files, wantFiles) {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("trial_files", err)
	}
	adapter, err := agentadapter.ReferenceContract()
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	observationData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceObservationName), agentadapter.MaxObservationBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	observation, err := agentadapter.DecodeObservation(bytes.NewReader(observationData), adapter)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("observation_decode", err)
	}
	executionPlanData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceExecutionPlanName), executionbackend.MaxPlanBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	executionPlan, err := executionbackend.DecodePlan(bytes.NewReader(executionPlanData))
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("execution_plan_decode", err)
	}
	executionReceiptData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceExecutionReceiptName), executionbackend.MaxReceiptBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	executionReceipt, err := executionbackend.DecodeReceipt(bytes.NewReader(executionReceiptData), executionPlan)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("execution_receipt_decode", err)
	}
	gradingPlanData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceGradingPlanName), grading.MaxPlanBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	gradingPlan, err := grading.DecodePlan(bytes.NewReader(gradingPlanData))
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("grading_plan_decode", err)
	}
	gradeReceiptData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceGradeReceiptName), grading.MaxReceiptBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	gradeReceipt, err := grading.DecodeReceipt(bytes.NewReader(gradeReceiptData), gradingPlan)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("grade_receipt_decode", err)
	}
	trialRecordData, err := readSequentialReferenceFile(publication.root, filepath.Join(directory, sequentialReferenceTrialRecordName), experiment.MaxTrialBytes)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	trialRecord, err := experiment.DecodeTrialRecord(bytes.NewReader(trialRecordData), manifest)
	if err != nil {
		return SequentialReferenceTrialArtifacts{}, sequentialReferenceError("trial_record_decode", err)
	}
	artifacts := SequentialReferenceTrialArtifacts{
		AttemptPlan: inspection.Plan, Observation: &observation, ExecutionPlan: executionPlan,
		ExecutionReceipt: &executionReceipt, GradingPlan: gradingPlan, GradeReceipt: &gradeReceipt,
		LifecycleEvent: inspection.Events[len(inspection.Events)-1], TrialRecord: trialRecord,
	}
	if err := validateSequentialReferenceArtifactChain(manifest, assignment, inspection, artifacts, executionReceiptData); err != nil {
		return SequentialReferenceTrialArtifacts{}, err
	}
	return artifacts, nil
}

func validateSequentialReferenceArtifactChain(manifest experiment.Manifest, assignment sequentialReferenceAssignment,
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
	baseBindings, err := ExperimentAttemptBindings(manifest)
	if err != nil || int(inspection.Plan.Ordinal) > len(baseBindings) {
		return sequentialReferenceError("attempt_binding", err)
	}
	wantBinding, err := BindExecutionBackendTrial(baseBindings[inspection.Plan.Ordinal-1], artifacts.ExecutionPlan)
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
	preparedEvidence, err := grading.PrepareEvidence(context.Background(), admittedGrading, grading.EvidenceSet{
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
	wantGrade, err := grading.EvaluateDeterministic(context.Background(), admittedGrading, preparedEvidence)
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

func readSequentialReferenceFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, sequentialReferenceError("artifact_shape", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, sequentialReferenceError("artifact_read", err)
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
	entries, err := readSequentialReferenceDirectory(publication.root, ".")
	if err != nil {
		return sequentialReferenceError("publication_shape", err)
	}
	wantTop := []string{sequentialReferenceLedgerDirectory, sequentialReferenceManifestName, sequentialReferenceTrialsDirectory}
	if marker {
		wantTop = append(wantTop, sequentialReferenceMarkerName)
		sort.Strings(wantTop)
	}
	if !reflect.DeepEqual(entries, wantTop) {
		return sequentialReferenceError("publication_members", nil)
	}
	trialEntries, err := readSequentialReferenceDirectory(publication.root, sequentialReferenceTrialsDirectory)
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

func readSequentialReferenceDirectory(root *os.Root, name string) ([]string, error) {
	directory, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
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
