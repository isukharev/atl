package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestPrivateActivationStudyPlanExecutesOneBoundFourCellBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	preview, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if preview.Kind != PrivateRunSetKindActivationStudy || preview.CostAssurance != PrivateActivationCostAssuranceDetectionOnly ||
		preview.CostPreventive || len(preview.Surfaces) != 1 || preview.Surfaces[0] != SurfaceCLISkill ||
		!reflect.DeepEqual(preview.OrderedTreatments, ActivationStudyOrder(0)) {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivatePlanTextSafe(t, string(encoded), fixture)
	if _, err := CreatePrivatePlan(context.Background(), fixture.createOptions()); err == nil {
		t.Fatal("second pending activation-study plan was accepted")
	}

	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != 2 || summary.Status != "completed" || summary.Completed != 4 || summary.CostKnown == nil || !*summary.CostKnown ||
		!reflect.DeepEqual(summary.Surfaces, []string{SurfaceCLISkill}) {
		t.Fatalf("summary=%+v", summary)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != PrivateRunSetKindActivationStudy || len(source.Surfaces) != 4 {
		t.Fatalf("source=%+v", source)
	}
	seenCells := map[string]struct{}{}
	for _, surface := range source.Surfaces {
		if surface.CellID == "" || privateActivationTreatmentIndex(surface.SkillActivation) < 0 || surface.Surface != SurfaceCLISkill {
			t.Fatalf("surface=%+v", surface)
		}
		if _, exists := seenCells[surface.CellID]; exists {
			t.Fatalf("duplicate opaque cell id: %s", surface.CellID)
		}
		seenCells[surface.CellID] = struct{}{}
		for _, treatment := range PrivateActivationStudyTreatments() {
			guess := "cell-" + sha256HexBytes([]byte(preview.PlanID + "\x00" + treatment))[:16]
			if surface.CellID == guess {
				t.Fatalf("opaque reviewer cell id was derivable from plan and treatment: %s", surface.CellID)
			}
		}
	}

	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, ActivationStudyOrder(1)) {
		t.Fatalf("next order=%v", next.OrderedTreatments)
	}
}

func TestPrivateActivationStudyCapsCalibrationTimeoutWithoutChangingTreatments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	const treatmentTimeout = 600
	for _, relative := range manifest.RunSets[0].SpecPaths {
		path := filepath.Join(fixture.root, filepath.FromSlash(relative))
		var spec RunSpec
		readPrivatePlanTestJSON(t, path, &spec)
		spec.TimeoutSeconds = treatmentTimeout
		writeJSONTestFile(t, path, spec)
	}

	installPrivateActivationRunStub(t)
	calibrationRun := privatePlanRunCalibration
	calibrationTimeout := 0
	privatePlanRunCalibration = func(ctx context.Context, options CodexCLICalibrationOptions) (CodexCLICalibrationReceipt, error) {
		calibrationTimeout = options.TimeoutSeconds
		return calibrationRun(ctx, options)
	}
	t.Cleanup(func() { privatePlanRunCalibration = calibrationRun })

	preview := fixture.createPlan(t)
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil || summary.Status != "completed" || summary.Completed != 4 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if calibrationTimeout != maxCodexCLICalibrationTimeout {
		t.Fatalf("calibration timeout=%d want=%d", calibrationTimeout, maxCodexCLICalibrationTimeout)
	}
	for _, relative := range manifest.RunSets[0].SpecPaths {
		var spec RunSpec
		readPrivatePlanTestJSON(t, filepath.Join(fixture.root, filepath.FromSlash(relative)), &spec)
		if spec.TimeoutSeconds != treatmentTimeout {
			t.Fatalf("treatment timeout=%d want=%d", spec.TimeoutSeconds, treatmentTimeout)
		}
	}
}

func TestPrivateActivationStudyOrderCannotBeSelectedByExpiryOrAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	first := fixture.createPlan(t)
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	alternate := manifest.RunSets[0]
	alternate.Alias = "alternate-study"
	manifest.RunSets = append(manifest.RunSets, alternate)
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}

	options := fixture.createOptions()
	options.RunSetAlias = alternate.Alias
	if _, err := CreatePrivatePlan(context.Background(), options); err == nil {
		t.Fatal("a second alias bypassed the active study-series gate")
	}
	options.Now = fixture.now.Add(2 * time.Hour)
	options.Consent.ExpiresAt = fixture.now.Add(3 * time.Hour).Format(time.RFC3339)
	afterExpiry, err := CreatePrivatePlan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterExpiry.OrderedTreatments, first.OrderedTreatments) {
		t.Fatalf("expired unexecuted plan advanced order: first=%v next=%v", first.OrderedTreatments, afterExpiry.OrderedTreatments)
	}
}

func TestPrivateActivationStudyStopsAfterResourceControlFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	passing := privatePlanRunHeadless
	invocations := 0
	privatePlanRunHeadless = func(ctx context.Context, options RunOptions) (RunOutput, error) {
		invocations++
		output, err := passing(ctx, options)
		if err != nil {
			return output, err
		}
		output.Results[0].Status = "fail"
		output.Results[0].Violations = []Violation{{Code: "budget_exceeded", Subject: "backend_requests", Observed: 2, Limit: 1}}
		resultPath := filepath.Join(options.OutputRoot, output.Results[0].ScenarioID, output.Results[0].Runtime.Provider,
			output.Results[0].Variant, "run-01", "result.json")
		data, marshalErr := json.MarshalIndent(output.Results[0], "", "  ")
		if marshalErr != nil {
			return RunOutput{}, marshalErr
		}
		if writeErr := os.WriteFile(resultPath, append(data, '\n'), 0o600); writeErr != nil {
			return RunOutput{}, writeErr
		}
		return output, nil
	}
	preview := fixture.createPlan(t)
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err == nil || summary.Status != "stopped" || invocations != 1 || summary.Completed != 0 {
		t.Fatalf("summary=%+v invocations=%d err=%v", summary, invocations, err)
	}
	report, doctorErr := DoctorPrivateWorkspace(fixture.root, fixture.repository)
	if doctorErr != nil || !report.Healthy || report.Counts.IncompleteRuns != 1 {
		t.Fatalf("doctor=%+v err=%v", report, doctorErr)
	}
	references, err := InspectPrivatePlanRunReferences(fixture.root, fixture.repository)
	if err != nil || len(references) != 1 {
		t.Fatalf("references=%+v err=%v", references, err)
	}
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	item := plan.Items[0]
	receiptPath, runDirectory := privateActivationItemEvidencePaths(fixture.root, plan, references[0].RunID, item)
	for _, evidencePath := range []string{filepath.Join(runDirectory, "result.json"), filepath.Join(runDirectory, "final.json"), receiptPath} {
		evidenceData, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, append(evidenceData, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
		if report := InspectPrivateWorkspace(fixture.root, fixture.repository); report.Healthy {
			t.Fatal("doctor accepted drifted evidence for a stopped study")
		}
		if err := os.WriteFile(evidencePath, evidenceData, 0o600); err != nil {
			t.Fatal(err)
		}
		if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy {
			t.Fatalf("restored evidence report=%+v err=%v", report, err)
		}
		if err := os.Remove(evidencePath); err != nil {
			t.Fatal(err)
		}
		if report := InspectPrivateWorkspace(fixture.root, fixture.repository); report.Healthy {
			t.Fatal("doctor accepted missing evidence for a stopped study")
		}
		if err := os.WriteFile(evidencePath, evidenceData, 0o600); err != nil {
			t.Fatal(err)
		}
		if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy {
			t.Fatalf("restored deleted evidence report=%+v err=%v", report, err)
		}
	}
	if err := removePrivateTree(fixture.root, filepath.Join(fixture.root, "runs", references[0].RunID)); err != nil {
		t.Fatal(err)
	}
	if report := InspectPrivateWorkspace(fixture.root, fixture.repository); report.Healthy {
		t.Fatal("doctor accepted deletion of a stopped study run tree")
	}
}

func TestPrivateActivationRecoveryDoesNotAdvanceLaunchedCellWithoutProviderEvidence(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	snapshotResidue := filepath.Join(fixture.root, ".ephemeral", "execution-"+runID, "provider-runtime", "capsule")
	if err := os.MkdirAll(snapshotResidue, 0o700); err != nil {
		t.Fatal(err)
	}
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID, Status: "running",
		CompletedSurfaces: []string{}, CompletedCells: []string{}}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation})
	if err != nil || recovered.Status != "stopped" || recovered.Completed != 0 || recovered.EstimatedCostMicroUSD != 50 || !recovered.CostKnown {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, ".ephemeral", "execution-"+runID)); !os.IsNotExist(err) {
		t.Fatalf("recovery left execution/provider residue: %v", err)
	}
	if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy {
		t.Fatalf("recovered workspace report=%+v err=%v", report, err)
	}
	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, ActivationStudyOrder(0)) {
		t.Fatalf("evidence-free launched recovery advanced the order: %v", next.OrderedTreatments)
	}
}

func TestPrivateActivationRecoveryReconcilesDurableExecutionReceipt(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	item, ok := privateActivationPlanItemForCell(plan, cell.CellID)
	if !ok {
		t.Fatal("planned cell missing")
	}
	output := writePrivateActivationExecutionEvidence(t, fixture, plan, item, runID)
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID, Status: "running",
		CompletedSurfaces: []string{}, CompletedCells: []string{}}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation})
	if err != nil || recovered.Status != "stopped" || recovered.EstimatedCostMicroUSD != output.EstimatedCostMicroUSDTotal+50 || !recovered.CostKnown {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if !privateActivationAttemptStarted(stateEventsFromFile(t, statePath)) {
		t.Fatal("reconciled receipt did not become durable provider evidence")
	}
	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, ActivationStudyOrder(1)) {
		t.Fatalf("receipt-backed recovery did not advance exactly once: %v", next.OrderedTreatments)
	}
}

func TestPrivateActivationRecoveryConsumesCommittedAttemptWithoutReceipt(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-cccccccccccccccccccccccccccccccc"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID, Status: "running",
		CompletedSurfaces: []string{}, CompletedCells: []string{}}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation})
	if err != nil || recovered.Status != "stopped" || recovered.Completed != 0 || recovered.EstimatedCostMicroUSD != 50 || recovered.CostKnown {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, ActivationStudyOrder(1)) {
		t.Fatalf("committed provider attempt did not advance exactly once: %v", next.OrderedTreatments)
	}
}

func TestPrivateActivationRecoveryRejectsTamperedReceiptWithoutStateMutation(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-dddddddddddddddddddddddddddddddd"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	item, ok := privateActivationPlanItemForCell(plan, cell.CellID)
	if !ok {
		t.Fatal("planned cell missing")
	}
	writePrivateActivationExecutionEvidence(t, fixture, plan, item, runID)
	receiptPath, _ := privateActivationItemEvidencePaths(fixture.root, plan, runID, item)
	receiptData, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(receiptData, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID, Status: "running",
		CompletedSurfaces: []string{}, CompletedCells: []string{}}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation}); err == nil {
		t.Fatal("tampered recovery receipt was accepted")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed recovery mutated durable study state")
	}
}

func TestPrivateActivationRecoveryRejectsSymlinkSnapshotWithoutTouchingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "must-survive")
	if err := os.WriteFile(marker, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(fixture.root, ".ephemeral", "execution-"+runID)
	if err := os.Symlink(outside, snapshot); err != nil {
		t.Fatal(err)
	}
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID, Status: "running",
		CompletedSurfaces: []string{}, CompletedCells: []string{}}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation}); err == nil {
		t.Fatal("symlinked execution snapshot was accepted")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "outside\n" {
		t.Fatalf("outside target changed: data=%q err=%v", data, err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed symlink recovery mutated durable study state")
	}
}

func TestPrivateActivationStudyPersistsReceiptBeforeAtomicFinalCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	preview := fixture.createPlan(t)
	originalWrite := privatePlanWriteState
	var writes []privatePlanState
	privatePlanWriteState = func(path string, state privatePlanState) error {
		if err := originalWrite(path, state); err != nil {
			return err
		}
		copyState := state
		copyState.CompletedCells = append([]string(nil), state.CompletedCells...)
		copyState.Events = append([]PrivateActivationLifecycleEvent(nil), state.Events...)
		writes = append(writes, copyState)
		return nil
	}
	t.Cleanup(func() { privatePlanWriteState = originalWrite })

	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" || !reflect.DeepEqual(summary.Surfaces, []string{SurfaceCLISkill}) {
		t.Fatalf("summary=%+v", summary)
	}
	receipts := 0
	for _, state := range writes {
		plan, _, loadErr := loadPrivatePlan(fixture.root, preview.PlanID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		lifecycle, lifecycleErr := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
		if lifecycleErr != nil {
			t.Fatal(lifecycleErr)
		}
		lifecycle.Events = append([]PrivateActivationStudyEvent(nil), state.Events...)
		projection, projectionErr := lifecycle.project()
		if projectionErr != nil {
			t.Fatalf("persisted invalid state=%+v err=%v", state, projectionErr)
		}
		if len(state.Events) > 0 && state.Events[len(state.Events)-1].Type == PrivateActivationEventReceipt {
			receipts++
			if state.Status != "running" || projection.activePhase != PrivateActivationEventReceipt {
				t.Fatalf("receipt was not durably isolated before decision: state=%+v projection=%+v", state, projection)
			}
		}
		if projection.status == PrivateActivationStudyCompleted && state.Status != "completed" {
			t.Fatalf("completed chain persisted without completed state: %+v", state)
		}
	}
	if receipts != 4 {
		t.Fatalf("durable receipts=%d writes=%d", receipts, len(writes))
	}
	last := writes[len(writes)-1]
	if last.Status != "completed" || last.CompletedAt == "" || len(last.CompletedCells) != 4 || last.EstimatedCostMicroUSD != 450 {
		t.Fatalf("last state=%+v", last)
	}
}

func TestPrivateActivationStateWriteFailuresReportLastDurableProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	for _, failAt := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("write-%d", failAt), func(t *testing.T) {
			fixture := newPrivateActivationPlanFixture(t)
			installPrivateActivationRunStub(t)
			preview := fixture.createPlan(t)
			originalWrite := privatePlanWriteState
			calls := 0
			privatePlanWriteState = func(path string, state privatePlanState) error {
				calls++
				if calls == failAt {
					return errors.New("injected state write failure")
				}
				return originalWrite(path, state)
			}
			t.Cleanup(func() { privatePlanWriteState = originalWrite })

			summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
			if err == nil {
				t.Fatalf("summary=%+v err=%v", summary, err)
			}
			statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
			data, readErr := os.ReadFile(statePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var state privatePlanState
			if decodePrivateLifecycleJSON(data, &state) != nil {
				t.Fatal("durable state became invalid")
			}
			if summary.Status != state.Status || summary.Completed != len(state.CompletedCells) || summary.EstimatedCostMicroUSD != state.EstimatedCostMicroUSD {
				t.Fatalf("summary=%+v durable=%+v", summary, state)
			}
		})
	}
}

func TestPrivateActivationProviderBoundaryControlsOrderConsumption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	for _, test := range []struct {
		name, wantReason string
		commit           bool
		wantCostKnown    bool
		wantOrder        []string
	}{
		{name: "pre-spawn failure", wantReason: PrivateActivationUnknownInterrupted, wantCostKnown: true, wantOrder: ActivationStudyOrder(0)},
		{name: "post-spawn failure", wantReason: PrivateActivationUnknownProvider, commit: true, wantOrder: ActivationStudyOrder(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateActivationPlanFixture(t)
			installPrivateActivationRunStub(t)
			preview := fixture.createPlan(t)
			original := privatePlanRunHeadless
			privatePlanRunHeadless = func(_ context.Context, options RunOptions) (RunOutput, error) {
				if test.commit && options.providerAttemptCommitted != nil {
					if err := options.providerAttemptCommitted(); err != nil {
						return RunOutput{}, err
					}
				}
				return RunOutput{}, errors.New("synthetic provider failure")
			}
			t.Cleanup(func() { privatePlanRunHeadless = original })

			summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
			if err == nil || summary.Status != "stopped" || summary.Completed != 0 || summary.CostKnown == nil || *summary.CostKnown != test.wantCostKnown {
				t.Fatalf("summary=%+v err=%v", summary, err)
			}
			plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			stateData, err := os.ReadFile(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var state privatePlanState
			if decodePrivateLifecycleJSON(stateData, &state) != nil || validatePrivateActivationPlanState(plan, state) != nil || state.StopReason != test.wantReason {
				t.Fatalf("state=%+v", state)
			}
			next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next.OrderedTreatments, test.wantOrder) {
				t.Fatalf("next order=%v want=%v", next.OrderedTreatments, test.wantOrder)
			}
		})
	}
}

func TestPrivateActivationFailedCommitWriteDoesNotConsumeAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	preview := fixture.createPlan(t)
	originalWrite := privatePlanWriteState
	calls := 0
	privatePlanWriteState = func(path string, state privatePlanState) error {
		calls++
		if calls == 9 {
			return errors.New("injected provider commitment write failure")
		}
		return originalWrite(path, state)
	}
	t.Cleanup(func() { privatePlanWriteState = originalWrite })

	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err == nil || summary.Status != "stopped" || summary.Completed != 0 || summary.EstimatedCostMicroUSD != 50 || summary.CostKnown == nil || !*summary.CostKnown {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	events := stateEventsFromFile(t, statePath)
	if privateActivationAttemptStarted(events) {
		t.Fatalf("failed commitment write became durable provider evidence: %+v", events)
	}
	if len(events) == 0 || events[len(events)-1].Type != PrivateActivationEventUnknown ||
		events[len(events)-1].Reason != PrivateActivationUnknownInterrupted {
		t.Fatalf("events=%+v", events)
	}
	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, ActivationStudyOrder(0)) {
		t.Fatalf("failed pre-spawn commitment consumed the order: %v", next.OrderedTreatments)
	}
}

func TestPrivateActivationUnknownCostSummaryUsesDurableProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	passing := privatePlanRunHeadless
	privatePlanRunHeadless = func(ctx context.Context, options RunOptions) (RunOutput, error) {
		output, err := passing(ctx, options)
		if err != nil {
			return output, err
		}
		delete(output.Results[0].Coverage, "estimated_cost_microusd")
		resultPath := filepath.Join(options.OutputRoot, output.Results[0].ScenarioID, output.Results[0].Runtime.Provider,
			output.Results[0].Variant, "run-01", "result.json")
		data, marshalErr := json.MarshalIndent(output.Results[0], "", "  ")
		if marshalErr != nil {
			return RunOutput{}, marshalErr
		}
		if writeErr := os.WriteFile(resultPath, append(data, '\n'), 0o600); writeErr != nil {
			return RunOutput{}, writeErr
		}
		return output, nil
	}
	preview := fixture.createPlan(t)
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err == nil || summary.Status != "stopped" || summary.EstimatedCostMicroUSD != 50 || summary.CostKnown == nil || *summary.CostKnown {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	stateData, err := os.ReadFile(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state privatePlanState
	if decodePrivateLifecycleJSON(stateData, &state) != nil || state.EstimatedCostMicroUSD != summary.EstimatedCostMicroUSD {
		t.Fatalf("summary=%+v state=%+v", summary, state)
	}
}

func TestPrivateActivationCleanupFailureRemainsRecoverable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	preview := fixture.createPlan(t)
	originalRemove := privatePlanRemoveTree
	cleanupFailure := errors.New("injected cleanup failure")
	failCleanup := true
	privatePlanRemoveTree = func(root, target string) error {
		if failCleanup {
			return cleanupFailure
		}
		return originalRemove(root, target)
	}
	t.Cleanup(func() { privatePlanRemoveTree = originalRemove })

	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if !errors.Is(err, cleanupFailure) || summary.Status != "running" || summary.Completed != 3 || summary.CostKnown == nil || !*summary.CostKnown ||
		!reflect.DeepEqual(summary.Surfaces, []string{SurfaceCLISkill}) {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	stateData, err := os.ReadFile(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state privatePlanState
	if decodePrivateLifecycleJSON(stateData, &state) != nil || validatePrivateActivationPlanState(plan, state) != nil ||
		state.Status != "running" || state.StopReason != "" {
		t.Fatalf("state=%+v", state)
	}
	if report := InspectPrivateWorkspace(fixture.root, fixture.repository); report.Healthy {
		t.Fatalf("doctor accepted credential-bearing cleanup residue: %+v", report)
	}
	failCleanup = false
	recovered, recoverErr := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation})
	if recoverErr != nil || recovered.Status != "stopped" || recovered.Completed != 3 || !recovered.CostKnown {
		t.Fatalf("recovered=%+v err=%v", recovered, recoverErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.root, ".ephemeral", "execution-"+summary.RunID)); !os.IsNotExist(statErr) {
		t.Fatalf("recovery left execution snapshot: %v", statErr)
	}
	if report, doctorErr := DoctorPrivateWorkspace(fixture.root, fixture.repository); doctorErr != nil || !report.Healthy {
		t.Fatalf("recovered doctor=%+v err=%v", report, doctorErr)
	}
}

func TestPrivateActivationInterruptedStateWithoutRunTreeIsDoctorValid(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	state := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: "run-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status: "interrupted", CompletedSurfaces: []string{}, CompletedCells: []string{}}
	if validatePrivateActivationPlanState(plan, state) != nil {
		t.Fatalf("initial interrupted state rejected: %+v", state)
	}
	if err := writePrivatePlanState(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json"), state); err != nil {
		t.Fatal(err)
	}
	report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository)
	if err != nil || !report.Healthy || report.Counts.IncompleteRuns != 1 || report.State != "needs_review" {
		t.Fatalf("doctor=%+v err=%v", report, err)
	}
	if _, err := CreatePrivatePlan(context.Background(), fixture.createOptions()); err == nil {
		t.Fatal("incomplete crash state allowed a new order in the same study series")
	}
	recovered, err := RecoverPrivateActivationStudy(PrivateActivationRecoveryOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivateActivationRecoveryConfirmation})
	if err != nil || recovered.Status != "stopped" || recovered.Completed != 0 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	next, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.OrderedTreatments, preview.OrderedTreatments) {
		t.Fatalf("pre-provider recovery advanced order: first=%v next=%v", preview.OrderedTreatments, next.OrderedTreatments)
	}
}

func TestPrivateActivationStateMetadataMustMatchEventProjection(t *testing.T) {
	fixture := newPrivateActivationPlanFixture(t)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	completePrivateActivationFixtureCalibration(t, fixture, plan, &lifecycle, runID)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(0)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkDefinitive(cell.CellID, PrivateActivationOutcomeSuccess); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(PrivateActivationStopInputDrift); err != nil {
		t.Fatal(err)
	}
	projection, err := lifecycle.project()
	if err != nil {
		t.Fatal(err)
	}
	valid := privatePlanState{SchemaVersion: privatePlanStateSchemaVersion, PlanSHA256: preview.PlanSHA256, RunID: runID,
		Status: "stopped", CompletedSurfaces: []string{}, CompletedCells: append([]string(nil), projection.completedCells...),
		Events: append([]PrivateActivationLifecycleEvent(nil), lifecycle.Events...), StopReason: projection.stopReason,
		EstimatedCostMicroUSD: projection.detectedCostMicroUSD}
	if err := validatePrivateActivationPlanState(plan, valid); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	mutations := map[string]func(*privatePlanState){
		"completed cells": func(state *privatePlanState) { state.CompletedCells = nil },
		"detected cost":   func(state *privatePlanState) { state.EstimatedCostMicroUSD++ },
		"stop reason":     func(state *privatePlanState) { state.StopReason = PrivateActivationStopSnapshotDrift },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.CompletedCells = append([]string(nil), valid.CompletedCells...)
			candidate.Events = append([]PrivateActivationLifecycleEvent(nil), valid.Events...)
			mutate(&candidate)
			if err := validatePrivateActivationPlanState(plan, candidate); err == nil {
				t.Fatalf("tampered state accepted: %+v", candidate)
			}
		})
	}
}

func TestPrivateActivationStudyReviewPacketsAreBlindAndGloballyPrepared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	installPrivateActivationRunStub(t)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	rawResultDigests := map[string]struct{}{}
	for _, surface := range source.Surfaces {
		data, err := os.ReadFile(filepath.Join(surface.RunDirectory, "result.json"))
		if err != nil {
			t.Fatal(err)
		}
		rawResultDigests[sha256HexBytes(data)] = struct{}{}
	}
	panel := privateReviewTestPanel()
	var packets []PrivateReviewSummary
	packetTreatments := make(map[string]string, 12)
	for _, treatment := range PrivateActivationStudyTreatments() {
		for _, reviewer := range panel.Reviewers {
			packet, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
				PlanID: preview.PlanID, Surface: SurfaceCLISkill, Treatment: treatment, ReviewerID: reviewer.ID})
			if err != nil {
				t.Fatal(err)
			}
			packets = append(packets, packet)
			packetTreatments[packet.Packet] = treatment
			if packet.Treatment != "" {
				t.Fatalf("study review summary exposed treatment: %+v", packet)
			}
			if _, enumerable := rawResultDigests[packet.ResultSHA256]; enumerable {
				t.Fatalf("review summary exposed an enumerable treatment result digest: %+v", packet)
			}
			path := filepath.Join(fixture.root, filepath.FromSlash(packet.Packet))
			if _, err := os.Lstat(filepath.Join(path, "result.json")); !os.IsNotExist(err) {
				t.Fatalf("study review packet exposed treatment-bearing result: %v", err)
			}
			reviewData, err := os.ReadFile(filepath.Join(path, "review.json"))
			if err != nil {
				t.Fatal(err)
			}
			review, err := DecodeReview(bytes.NewReader(reviewData))
			if err != nil || review.ResultSHA256 != packet.ResultSHA256 {
				t.Fatalf("opaque packet binding mismatch: review=%+v err=%v", review, err)
			}
			if _, enumerable := rawResultDigests[review.ResultSHA256]; enumerable {
				t.Fatal("review packet exposed an enumerable treatment result digest")
			}
		}
	}
	for _, packet := range packets {
		completePrivateReviewPacket(t, fixture.root, packet.Packet, true)
	}
	for _, packet := range packets {
		if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceCLISkill, Treatment: packetTreatments[packet.Packet], ReviewerID: packet.ReviewerID}); err != nil {
			t.Fatal(err)
		}
	}
	boundSurface := source.Surfaces[0]
	canonicalRoot, _, err := privateWorkspaceLocations(fixture.root, fixture.repository, false)
	if err != nil {
		t.Fatal(err)
	}
	rawData, err := os.ReadFile(filepath.Join(boundSurface.RunDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	finalData, err := os.ReadFile(filepath.Join(boundSurface.RunDirectory, "final.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := DecodeResult(bytes.NewReader(rawData))
	receiptErr := validatePrivateActivationExecutionReceipt(canonicalRoot, boundSurface, rawData, finalData, raw)
	if err != nil || receiptErr != nil {
		t.Fatalf("valid execution receipt rejected: decode=%v receipt=%v", err, receiptErr)
	}
	driftedRaw := raw
	driftedRaw.Runtime.Reasoning = "different-reasoning"
	driftedRawData, _ := json.MarshalIndent(driftedRaw, "", "  ")
	if validatePrivateActivationExecutionReceipt(canonicalRoot, boundSurface, append(driftedRawData, '\n'), finalData, driftedRaw) == nil {
		t.Fatal("runtime-drifted evidence was not rejected by the lifecycle receipt")
	}
	implicitSpecPath := filepath.Join(fixture.root, "cases", "portfolio", "activation-implicit.json")
	implicitSpecData, err := os.ReadFile(implicitSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	var driftedSpec RunSpec
	if err := json.Unmarshal(implicitSpecData, &driftedSpec); err != nil {
		t.Fatal(err)
	}
	driftedSpec.MaxEstimatedCostMicroUSD++
	writeJSONTestFile(t, implicitSpecPath, driftedSpec)
	if _, err := SetPrivateActivationReference(PrivateActivationReferenceSetOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, PlanID: preview.PlanID, Reference: "drifted-study", Confirm: PrivateActivationReferenceConfirmation}); err == nil {
		t.Fatal("post-run activation spec drift was accepted by reference capture")
	}
	if err := os.WriteFile(implicitSpecPath, implicitSpecData, 0o600); err != nil {
		t.Fatal(err)
	}
	reference, err := SetPrivateActivationReference(PrivateActivationReferenceSetOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, PlanID: preview.PlanID, Reference: "passing-study", Confirm: PrivateActivationReferenceConfirmation})
	if err != nil || !reference.Stored || !reference.Gates.PromotionEligible {
		t.Fatalf("reference=%+v err=%v", reference, err)
	}
	referencePath := filepath.Join(fixture.root, "baselines", "activation-studies", "passing-study.json")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(referencePath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := SetPrivateActivationReference(PrivateActivationReferenceSetOptions{Root: fixture.root,
			RepositoryRoot: fixture.repository, PlanID: preview.PlanID, Reference: "passing-study", Confirm: PrivateActivationReferenceConfirmation}); err == nil {
			t.Fatal("owner-readable reference with loose mode was accepted as idempotent")
		}
		if err := os.Chmod(referencePath, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := CompareStoredPrivateActivationReference(PrivateActivationReferenceCompareOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, Reference: "passing-study"})
	if err != nil || !report.Gates.CausalEligible || len(report.Contrasts) == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	passingData, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	forgedPath := filepath.Join(filepath.Dir(referencePath), "forged-study.json")
	if err := os.WriteFile(forgedPath, passingData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareStoredPrivateActivationReference(PrivateActivationReferenceCompareOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, Reference: "forged-study"}); err == nil {
		t.Fatal("reference bytes were accepted under a different alias")
	}
	if _, err := PromotePrivateActivationReference(PrivateActivationPromotionOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, Reference: "forged-study", Confirm: PrivateActivationPromotionConfirmation}); err == nil {
		t.Fatal("reference bytes were promoted under a different alias")
	}
	if runtime.GOOS != "windows" {
		referenceDirectory := filepath.Dir(referencePath)
		if err := os.Chmod(referenceDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := CompareStoredPrivateActivationReference(PrivateActivationReferenceCompareOptions{Root: fixture.root,
			RepositoryRoot: fixture.repository, Reference: "passing-study"}); err == nil {
			t.Fatal("non-private activation reference directory was accepted")
		}
		if err := os.Chmod(referenceDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	promoted, err := PromotePrivateActivationReference(PrivateActivationPromotionOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, Reference: "passing-study", Confirm: PrivateActivationPromotionConfirmation})
	if err != nil || !promoted.Promoted {
		t.Fatalf("promotion=%+v err=%v", promoted, err)
	}
	current, err := CompareStoredPrivateActivationReference(PrivateActivationReferenceCompareOptions{Root: fixture.root,
		RepositoryRoot: fixture.repository, Reference: "current"})
	if err != nil || !reflect.DeepEqual(current, report) {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func newPrivateActivationPlanFixture(t *testing.T) privatePlanTestFixture {
	t.Helper()
	fixture := newPrivatePlanTestFixture(t, true, false)
	writeTestFile(t, filepath.Join(fixture.liveConfig, "config.json"), `{"jira_url":"http://127.0.0.1:9","confluence_url":"https://unused.example.invalid"}`+"\n", 0o600)
	caseRoot := filepath.Join(fixture.root, "cases", "portfolio")
	writeTestFile(t, filepath.Join(caseRoot, "response.json"), `{"type":"object","properties":{"complete":{"type":"boolean"},"evidence_outcome":{"type":"object","properties":{"state":{"type":"string","enum":["none","unavailable","blocked","failed","partial","succeeded"]}},"required":["state"],"additionalProperties":false}},"required":["complete","evidence_outcome"],"additionalProperties":false}`, 0o600)
	var scenario Scenario
	readPrivatePlanTestJSON(t, filepath.Join(caseRoot, "scenario.json"), &scenario)
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.Budgets.MaxInterfaceInvocations = scenario.Budgets.MaxATLInvocations
	scenario.RequiredMetrics = replacePrivatePlanTestString(scenario.RequiredMetrics, "atl_invocations", "interface_invocations")
	writeJSONTestFile(t, filepath.Join(caseRoot, "scenario.json"), scenario)
	data, err := os.ReadFile(filepath.Join(caseRoot, "run.cli.json"))
	if err != nil {
		t.Fatal(err)
	}
	var base RunSpec
	if err := json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	base.Category = BenchmarkCategoryNeutralCommon
	base.DataCapabilities = []string{"jira.fields"}
	writeTestFile(t, filepath.Join(caseRoot, base.ResponseSchemaFile), `{"type":"object","properties":{"complete":{"type":"boolean"},"evidence_outcome":{"type":"object","properties":{"state":{"type":"string","enum":["none","unavailable","blocked","failed","partial","succeeded"]}},"required":["state"],"additionalProperties":false}},"required":["complete","evidence_outcome"],"additionalProperties":false}`, 0o600)
	base.Checks = append([]RunCheck(nil), base.Checks...)
	for index := range base.Checks {
		switch base.Checks[index].Kind {
		case "atl_all_succeeded":
			base.Checks[index].Kind = "interface_all_succeeded"
		case "atl_invocations_min":
			base.Checks[index].Kind = "interface_invocations_min"
		}
	}
	paths := make([]string, 0, 4)
	for _, treatment := range PrivateActivationStudyTreatments() {
		spec := base
		spec.SkillActivation = treatment
		spec.Variant = "activation-" + treatment
		path := filepath.Join(caseRoot, spec.Variant+".json")
		writeJSONTestFile(t, path, spec)
		if _, _, err := ValidateRunSpecFile(path); err != nil {
			t.Fatalf("activation treatment %s: %v", treatment, err)
		}
		paths = append(paths, filepath.ToSlash(filepath.Join("cases", "portfolio", filepath.Base(path))))
	}
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Execution.MaxEstimatedCostMicroUSD = 50_000_000
	panel := privateReviewTestPanel()
	writeTestFile(t, filepath.Join(caseRoot, "blind-assignment.txt"), "Review the response without inferring the interface treatment.\n", 0o600)
	panel.BlindAssignment = "cases/portfolio/blind-assignment.txt"
	manifest.RunSets = []PrivateWorkspaceRunSet{{Kind: PrivateRunSetKindActivationStudy, Alias: fixture.runSetAlias,
		SpecPaths: paths, QualitativeReviewPanel: &panel, ReviewerReserveMicroUSD: 1_000_000,
		CalibrationMaxEstimatedCostMicroUSD: 500_000}}
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}
	if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy || report.Counts.ActivationStudies != 1 {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
	return fixture
}

func installPrivateActivationRunStub(t *testing.T) {
	t.Helper()
	original := privatePlanRunHeadless
	originalCalibration := privatePlanRunCalibration
	privatePlanRunCalibration = func(_ context.Context, options CodexCLICalibrationOptions) (CodexCLICalibrationReceipt, error) {
		if options.providerAttemptCommitted != nil {
			if err := options.providerAttemptCommitted(); err != nil {
				return CodexCLICalibrationReceipt{}, err
			}
		}
		contract, err := BuildCodexCLICalibrationContract(options.Model, options.Reasoning, options.TimeoutSeconds,
			options.MaxEstimatedCostMicroUSD, options.Pricing)
		if err != nil {
			return CodexCLICalibrationReceipt{}, err
		}
		return CodexCLICalibrationReceipt{
			SchemaVersion: CodexCLICalibrationSchemaVersion, ContractSHA256: contract.SHA256, Passed: true,
			CommandFamily:     "atl_version",
			CommandExecutions: 1, BrokeredInvocations: 1, GuardAdmissions: 1, GuardATLAdmissions: 1,
			StdoutBytes: 8, InputTokens: 30, OutputTokens: 10, EstimatedCostMicroUSD: 50, DurationMillis: 1,
		}, nil
	}
	privatePlanRunHeadless = func(_ context.Context, options RunOptions) (RunOutput, error) {
		loaded, err := loadRunInputs(options)
		if err != nil {
			return RunOutput{}, err
		}
		if options.providerAttemptCommitted != nil {
			if err := options.providerAttemptCommitted(); err != nil {
				return RunOutput{}, err
			}
		}
		result := privateActivationStubResult(t, loaded)
		if err := result.Validate(); err != nil {
			return RunOutput{}, err
		}
		directory := filepath.Join(options.OutputRoot, loaded.scenario.ID, loaded.spec.Provider, loaded.spec.Variant, "run-01")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return RunOutput{}, err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(filepath.Join(directory, "result.json"), append(data, '\n'), 0o600); err != nil {
			return RunOutput{}, err
		}
		if err := os.WriteFile(filepath.Join(directory, "final.json"), []byte("{\"complete\":true,\"evidence_outcome\":{\"state\":\"succeeded\"}}\n"), 0o600); err != nil {
			return RunOutput{}, err
		}
		return RunOutput{Results: []Result{result}, EstimatedCostMicroUSDTotal: 100}, nil
	}
	t.Cleanup(func() {
		privatePlanRunHeadless = original
		privatePlanRunCalibration = originalCalibration
	})
}

func privateActivationStubResult(t *testing.T, loaded loadedRun) Result {
	t.Helper()
	result := privateActivationResult(t, loaded.spec.SkillActivation)
	result.ScenarioID = loaded.scenario.ID
	result.TaskClass = loaded.scenario.TaskClass
	result.Category = loaded.scenario.EffectiveCategory()
	result.Variant = loaded.spec.Variant
	result.Runtime.Model = loaded.spec.Model
	result.Runtime.PromptContractSHA256 = loaded.promptContractSHA256
	result.BackendObservation = BackendObservationHTTP
	result.SafetyAssurance = SafetyAssuranceObservedHTTP
	result.Coverage["backend_requests"] = true
	result.Coverage["duplicate_backend_requests"] = true
	result.Coverage["remote_writes"] = true
	result.HTTPMethods = map[string]int{"GET": 1}
	result.Metrics.BackendRequests = 1
	result.Metrics.RemoteWrites = 0
	result.Metrics.EstimatedCostMicroUSD = 100
	result.Coverage["estimated_cost_microusd"] = true
	for _, check := range loaded.spec.Checks {
		if privateActivationSafetyCheckKind(check.Kind) {
			result.Checks[check.Name] = true
		}
	}
	return result
}

func writePrivateActivationExecutionEvidence(t *testing.T, fixture privatePlanTestFixture, plan privatePlan, item privatePlanItem, runID string) RunOutput {
	t.Helper()
	loaded, err := loadRunInputs(RunOptions{SpecPath: filepath.Join(fixture.root, filepath.FromSlash(item.SpecPath))})
	if err != nil {
		t.Fatal(err)
	}
	result := privateActivationStubResult(t, loaded)
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	receiptPath, runDirectory := privateActivationItemEvidencePaths(fixture.root, plan, runID, item)
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(filepath.Join(runDirectory, "result.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "final.json"), []byte("{\"complete\":true,\"evidence_outcome\":{\"state\":\"succeeded\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := RunOutput{Results: []Result{result}, EstimatedCostMicroUSDTotal: 100}
	if _, err := persistPrivateActivationExecutionReceipt(fixture.root, filepath.Join(fixture.root, "runs", runID), plan, item, output); err != nil {
		t.Fatal(err)
	}
	return output
}

func completePrivateActivationFixtureCalibration(t *testing.T, fixture privatePlanTestFixture, plan privatePlan,
	lifecycle *PrivateActivationStudyLifecycle, runID string,
) {
	t.Helper()
	runRoot := filepath.Join(fixture.root, "runs", runID)
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.ReserveCalibration(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationLaunched(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationProviderAttemptCommitted(); err != nil {
		t.Fatal(err)
	}
	receipt := CodexCLICalibrationReceipt{
		SchemaVersion: CodexCLICalibrationSchemaVersion, ContractSHA256: plan.StudyContract.Calibration.ContractSHA256, Passed: true,
		CommandFamily:     "atl_version",
		CommandExecutions: 1, BrokeredInvocations: 1, GuardAdmissions: 1, GuardATLAdmissions: 1,
		StdoutBytes: 8, InputTokens: 30, OutputTokens: 10, EstimatedCostMicroUSD: 50, DurationMillis: 1,
	}
	loaded, err := loadRunInputs(RunOptions{SpecPath: filepath.Join(fixture.root, filepath.FromSlash(plan.Items[0].SpecPath))})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := BuildCodexCLICalibrationContract(loaded.spec.Model, loaded.spec.Reasoning,
		codexCLICalibrationTimeout(loaded.spec.TimeoutSeconds),
		plan.StudyContract.Calibration.MaxEstimatedCostMicroUSD, loaded.spec.Pricing)
	if err != nil {
		t.Fatal(err)
	}
	receiptSHA256, err := persistPrivateActivationCalibrationReceipt(fixture.root, runRoot, plan, contract, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordCalibrationReceipt(PrivateActivationReceipt{SHA256: receiptSHA256, CostKnown: true,
		DetectedCostMicroUSD: receipt.EstimatedCostMicroUSD, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationSucceeded(); err != nil {
		t.Fatal(err)
	}
}

func stateEventsFromFile(t *testing.T, path string) []PrivateActivationLifecycleEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state privatePlanState
	if decodePrivateLifecycleJSON(data, &state) != nil {
		t.Fatal("invalid state")
	}
	return state.Events
}
