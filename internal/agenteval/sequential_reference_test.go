package agenteval

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestSequentialReferenceRunsCanonicalAgentSkillsRosterAndBindsEveryArtifact(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	firstStore := newSequentialReferenceStoreForTest(t, manifest)
	first, err := RunSequentialReference(context.Background(), firstStore, manifest, bundle)
	if err != nil {
		t.Fatalf("run first reference roster: %v", err)
	}
	wantTrials := len(manifest.Blocks) * len(manifest.Treatments)
	if len(first.Trials) != wantTrials || first.ManifestSHA256 != manifest.ManifestSHA256 {
		t.Fatalf("result trials=%d manifest=%s, want %d/%s", len(first.Trials), first.ManifestSHA256, wantTrials, manifest.ManifestSHA256)
	}
	assignments := sequentialReferenceAssignments(manifest)
	for index, artifacts := range first.Trials {
		assignment := assignments[index]
		if artifacts.TrialRecord.TrialID != assignment.TrialID || artifacts.TrialRecord.BlockID != assignment.BlockID ||
			artifacts.TrialRecord.TreatmentID != assignment.TreatmentID || artifacts.AttemptPlan.Ordinal != uint32(index+1) {
			t.Fatalf("trial[%d] order drifted: assignment=%+v artifacts=%+v", index, assignment, artifacts.TrialRecord)
		}
		if artifacts.Observation == nil || artifacts.ExecutionReceipt == nil || artifacts.GradeReceipt == nil {
			t.Fatalf("trial[%d] artifact chain is incomplete", index)
		}
		if err := experiment.ValidateTrialRecord(manifest, artifacts.TrialRecord); err != nil {
			t.Fatalf("trial[%d] record: %v", index, err)
		}
		adapter, err := agentadapterReferenceContractForTest()
		if err != nil {
			t.Fatal(err)
		}
		observationSHA, err := AgentAdapterObservationSHA256(adapter, *artifacts.Observation)
		if err != nil || observationSHA != artifacts.TrialRecord.AgentObservationSHA256 {
			t.Fatalf("trial[%d] observation binding=%s want=%s err=%v", index, observationSHA, artifacts.TrialRecord.AgentObservationSHA256, err)
		}
		gradeSHA, err := GradeReceiptSHA256(artifacts.GradingPlan, *artifacts.GradeReceipt)
		if err != nil || gradeSHA != artifacts.TrialRecord.GradeReceiptSHA256 || gradeSHA != artifacts.LifecycleEvent.Evidence.ReceiptSHA256 {
			t.Fatalf("trial[%d] grade binding=%s record=%s event=%s err=%v", index, gradeSHA,
				artifacts.TrialRecord.GradeReceiptSHA256, artifacts.LifecycleEvent.Evidence.ReceiptSHA256, err)
		}
		if artifacts.AttemptPlan.PlanSHA256 != artifacts.TrialRecord.AttemptPlanSHA256 ||
			artifacts.LifecycleEvent.EventSHA256 != artifacts.TrialRecord.LifecycleEventSHA256 {
			t.Fatalf("trial[%d] lifecycle binding drifted", index)
		}
		for _, stage := range artifacts.TrialRecord.Stages {
			if stage.Presence != experiment.PresenceObserved || stage.Value == nil {
				t.Fatalf("trial[%d] stage %s is not an observed reference projection", index, stage.Stage)
			}
		}
		executionData, err := EncodeExecutionBackendTrialReceipt(artifacts.ExecutionPlan, *artifacts.ExecutionReceipt)
		if err != nil {
			t.Fatalf("trial[%d] execution receipt: %v", index, err)
		}
		contract, err := BuiltinGraderContract()
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := AdmitGradingPlan(contract, artifacts.GradingPlan)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := PrepareGradingEvidence(context.Background(), admitted, grading.EvidenceSet{
			InputProjectionSHA256: artifacts.GradingPlan.InputProjectionSHA256,
			Files: []grading.FileEvidence{{
				ID: "execution-receipt", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o600, Data: executionData,
			}},
			Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{},
		})
		if err != nil {
			t.Fatalf("trial[%d] reprepare evidence: %v", index, err)
		}
		if prepared.SHA256() != artifacts.GradeReceipt.EvidenceSHA256 || !reflect.DeepEqual(prepared.Citations(), artifacts.GradeReceipt.Evidence) {
			prepared.Destroy()
			t.Fatalf("trial[%d] execution receipt is not transitively bound by grading", index)
		}
		prepared.Destroy()
	}

	secondStore := newSequentialReferenceStoreForTest(t, manifest)
	second, err := RunSequentialReference(context.Background(), secondStore, manifest, bundle)
	if err != nil {
		t.Fatalf("run second reference roster: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical declared inputs changed publication-safe identities or projections")
	}
	encoded, err := jsonMarshalForSequentialReferenceTest(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"synthetic public case", "separately authored negative control", "Synthetic public skill", "https://"} {
		if bytes.Contains(encoded, []byte(raw)) {
			t.Fatalf("content or authority escaped result: %q", raw)
		}
	}
}

func TestSequentialReferenceRunsOneAttemptAtATimeInManifestOrder(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	prepared, err := prepareSequentialReference(context.Background(), manifest, bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.destroy()
	store := newSequentialReferenceStoreForTest(t, manifest)
	wantAssignments := sequentialReferenceAssignments(manifest)
	published := 0
	result, err := prepared.run(context.Background(), store, func(index int, artifacts SequentialReferenceTrialArtifacts) error {
		if index != published || artifacts.AttemptPlan.Ordinal != uint32(index+1) {
			return errors.New("publication order drifted")
		}
		inspections, inspectErr := store.InspectAll()
		if inspectErr != nil || len(inspections) != len(wantAssignments) {
			return errors.Join(errors.New("inspect attempt roster"), inspectErr)
		}
		for ordinal, inspection := range inspections {
			if ordinal <= index {
				if !inspection.Projection.Terminal {
					return errors.New("published attempt is not terminal")
				}
				continue
			}
			if inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StatePlanned {
				return errors.New("later attempt entered before current publication")
			}
		}
		published++
		return nil
	})
	if err != nil || published != len(wantAssignments) || len(result.Trials) != len(wantAssignments) {
		t.Fatalf("sequential roster published=%d trials=%d err=%v", published, len(result.Trials), err)
	}
}

func TestSequentialReferencePublicationIsExactNewCanonicalAndStrictlyReadable(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	parent := t.TempDir()
	firstDestination := filepath.Join(parent, "first")
	first, err := RunSequentialReferenceToNewDestination(context.Background(), firstDestination, manifest, bundle)
	if err != nil {
		t.Fatalf("publish first: %v", err)
	}
	inspected, err := InspectSequentialReferencePublication(firstDestination)
	if err != nil || !reflect.DeepEqual(inspected, first) {
		t.Fatalf("inspect first: equal=%t err=%v", reflect.DeepEqual(inspected, first), err)
	}
	if _, err := os.Lstat(filepath.Join(firstDestination, sequentialReferenceMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("completed publication retained marker: %v", err)
	}
	if _, err := RunSequentialReferenceToNewDestination(context.Background(), firstDestination, manifest, bundle); err == nil {
		t.Fatal("existing destination was accepted")
	}
	secondDestination := filepath.Join(parent, "second")
	second, err := RunSequentialReferenceToNewDestination(context.Background(), secondDestination, manifest, bundle)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("second publication changed result: equal=%t err=%v", reflect.DeepEqual(first, second), err)
	}
	missingReceipt := filepath.Join(secondDestination, sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(1), sequentialReferenceGradeReceiptName)
	if err := os.Remove(missingReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSequentialReferencePublication(secondDestination); err == nil {
		t.Fatal("publication with missing grading evidence was accepted")
	}

	assignments := sequentialReferenceAssignments(manifest)
	candidateIndex := -1
	for index, assignment := range assignments {
		treatment, ok := sequentialReferenceTreatmentByID(manifest, assignment.TreatmentID)
		if ok && treatment.ExpectedActivation {
			candidateIndex = index
			break
		}
	}
	if candidateIndex < 0 {
		t.Fatal("fixture has no activated candidate")
	}
	mutatedArtifacts := first.Trials[candidateIndex]
	adapter, err := SequentialReferenceAgentAdapterContract()
	if err != nil {
		t.Fatal(err)
	}
	mutatedObservation, err := agentadapter.NewReferenceObservation(adapter, mutatedArtifacts.AttemptPlan.AttemptID, false)
	if err != nil {
		t.Fatal(err)
	}
	mutatedObservationSHA, err := AgentAdapterObservationSHA256(adapter, mutatedObservation)
	if err != nil {
		t.Fatal(err)
	}
	mutatedRecord := mutatedArtifacts.TrialRecord
	mutatedRecord.RecordSHA256 = ""
	mutatedRecord.AgentObservationSHA256 = mutatedObservationSHA
	mutatedRecord.Stages, mutatedRecord.Metrics, err = sequentialReferenceObservedProjections(manifest, mutatedObservation, true)
	if err != nil {
		t.Fatal(err)
	}
	mutatedRecord, err = experiment.SealTrialRecord(manifest, mutatedRecord)
	if err != nil {
		t.Fatal(err)
	}
	trialDirectory := filepath.Join(firstDestination, sequentialReferenceTrialsDirectory,
		attemptLedgerOrdinalName(mutatedArtifacts.AttemptPlan.Ordinal))
	observationData, err := agentadapter.EncodeObservation(adapter, mutatedObservation)
	if err != nil {
		t.Fatal(err)
	}
	recordData, err := experiment.EncodeTrialRecord(manifest, mutatedRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trialDirectory, sequentialReferenceObservationName), observationData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trialDirectory, sequentialReferenceTrialRecordName), recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSequentialReferencePublication(firstDestination); err == nil {
		t.Fatal("self-consistent observation/profile drift was accepted")
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	canceledDestination := filepath.Join(parent, "canceled")
	if _, err := RunSequentialReferenceToNewDestination(canceledContext, canceledDestination, manifest, bundle); err == nil {
		t.Fatal("canceled preflight was accepted")
	}
	if _, err := os.Lstat(canceledDestination); !os.IsNotExist(err) {
		t.Fatalf("canceled preflight acquired destination authority: %v", err)
	}
}

func TestSequentialReferenceBundleIsClosedCanonicalBoundedAndFutureRejecting(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	data, err := EncodeSequentialReferenceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSequentialReferenceBundle(bytes.NewReader(data))
	if err != nil || !reflect.DeepEqual(decoded, bundle) {
		t.Fatalf("round trip equal=%t err=%v", reflect.DeepEqual(decoded, bundle), err)
	}
	mutations := map[string][]byte{
		"unknown":   bytes.Replace(data, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1),
		"duplicate": bytes.Replace(data, []byte(`{"schema":`), []byte(`{"schema":"agent-eval/sequential-reference-bundle","schema":`), 1),
		"future":    bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"trailing":  append(slices.Clone(data), []byte(`{}`)...),
		"no_lf":     slices.Clone(data[:len(data)-1]),
	}
	for name, mutated := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSequentialReferenceBundle(bytes.NewReader(mutated)); err == nil {
				t.Fatal("mutation was accepted")
			}
		})
	}
	over := bundle
	over.Treatments = slices.Clone(bundle.Treatments)
	over.Treatments[0].Inputs.Definitions = make([]byte, SequentialReferenceBundleMaxBytes)
	if _, err := EncodeSequentialReferenceBundle(over); err == nil {
		t.Fatal("oversized bundle was accepted")
	}
	treatments := slices.Clone(manifest.Design.Treatments)
	var referenceIndex, candidateIndex int
	for index, treatment := range treatments {
		switch treatment.Role {
		case experiment.RoleReference:
			referenceIndex = index
		case experiment.RoleCandidate:
			candidateIndex = index
		}
	}
	treatments[referenceIndex].Role, treatments[candidateIndex].Role = treatments[candidateIndex].Role, treatments[referenceIndex].Role
	if sequentialReferenceTreatmentsSupported(treatments) {
		t.Fatal("role-swapped valid design escaped the exact reference profile")
	}
}

func TestSequentialReferenceCheckedInFixturesAreProductionAuthentic(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	wantManifest, err := experiment.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantBundle, err := EncodeSequentialReferenceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for name, fixture := range map[string]struct {
		path string
		want []byte
	}{
		"manifest": {path: filepath.Join("cmd", "agent-eval", "testdata", "sequential-reference-manifest-v1.json"), want: wantManifest},
		"bundle":   {path: filepath.Join("testdata", "standalone-readability", "sequential-reference-bundle-v1.json"), want: wantBundle},
	} {
		data, readErr := os.ReadFile(fixture.path)
		if readErr != nil || !bytes.Equal(data, fixture.want) {
			t.Fatalf("%s fixture is not production-authentic: equal=%t err=%v", name, bytes.Equal(data, fixture.want), readErr)
		}
	}
}

func TestSequentialReferenceCapabilityDriftRefusesBeforeDestinationAuthority(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	for _, state := range []experiment.Support{experiment.SupportSupported, experiment.SupportUnknown} {
		t.Run(string(state), func(t *testing.T) {
			capability := manifest.CapabilityContract
			capability.Capabilities = slices.Clone(manifest.CapabilityContract.Capabilities)
			capability.CapabilityContractSHA256 = ""
			for index := range capability.Capabilities {
				if capability.Capabilities[index].ID != experiment.CapabilityConditionPrevious {
					continue
				}
				capability.Capabilities[index].Support = state
				capability.Capabilities[index].BindingSHA256 = ""
				if state == experiment.SupportSupported {
					capability.Capabilities[index].BindingSHA256 = rootExperimentDigest("unreviewed-reference-capability")
				}
			}
			capability, err := experiment.SealCapabilityContract(capability)
			if err != nil {
				t.Fatal(err)
			}
			design := manifest.Design
			design.DesignSHA256 = ""
			design.CapabilityContractSHA256 = capability.CapabilityContractSHA256
			design, err = experiment.SealDesign(design)
			if err != nil {
				t.Fatal(err)
			}
			drifted, err := experiment.Compile(design, capability, manifest.AnalysisPlan)
			if err != nil {
				t.Fatal(err)
			}
			driftedBundle := bundle
			driftedBundle.ManifestSHA256 = drifted.ManifestSHA256
			destination := filepath.Join(t.TempDir(), "must-not-exist")
			if _, err := RunSequentialReferenceToNewDestination(context.Background(), destination, drifted, driftedBundle); err == nil ||
				!errors.Is(err, ErrSequentialReferenceUnsupported) {
				t.Fatalf("capability drift error=%v", err)
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("capability refusal acquired destination authority: %v", err)
			}
		})
	}
}

func TestSequentialReferenceCommittedCancellationIsTerminalIncompleteAndNeverReplayed(t *testing.T) {
	manifest, bundle := sequentialReferenceFixture(t)
	prepared, err := prepareSequentialReference(context.Background(), manifest, bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.destroy()
	manifestData, err := experiment.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "interrupted")
	publication, err := createSequentialReferencePublication(destination, manifest, manifestData)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.close()
	store, err := CreateSequentialReferenceAttemptStore(filepath.Join(destination, sequentialReferenceLedgerDirectory), manifest)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	partial, err := prepared.run(canceled, store, publication.writeTrial)
	if err == nil || len(partial.Trials) != 1 || partial.Trials[0].TrialRecord.LifecycleState != experiment.LifecycleCanceled ||
		partial.Trials[0].TrialRecord.Exclusion != experiment.ExclusionLifecycleIncomplete || partial.Trials[0].GradeReceipt != nil {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	marker, err := os.ReadFile(filepath.Join(destination, sequentialReferenceMarkerName))
	if err != nil || string(marker) != manifest.ManifestSHA256+"\n" {
		t.Fatalf("incomplete marker=%q err=%v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(destination, sequentialReferenceTrialsDirectory, attemptLedgerOrdinalName(1), sequentialReferenceTrialRecordName)); err != nil {
		t.Fatalf("terminal trial record was not retained: %v", err)
	}
	inspections, err := store.InspectAll()
	if err != nil || len(inspections) != len(sequentialReferenceAssignments(manifest)) ||
		inspections[0].Projection.State != lifecycle.StateCanceled || !inspections[0].Projection.Terminal ||
		inspections[1].Projection.State != lifecycle.StatePlanned {
		t.Fatalf("ledger=%+v err=%v", inspections, err)
	}
	if _, err := InspectSequentialReferencePublication(destination); err == nil {
		t.Fatal("incomplete publication was accepted")
	}
	if _, err := RunSequentialReferenceToNewDestination(context.Background(), destination, manifest, bundle); err == nil {
		t.Fatal("ambiguous destination was replayed")
	}
}

func TestSequentialReferencePublishedCancellationIsOutcomeUnknownAndNeverReplayed(t *testing.T) {
	manifest, bundle := sequentialReferenceWaitFixture(t)
	destination := filepath.Join(t.TempDir(), "interrupted-publication")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	markerObserved := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		marker := filepath.Join(destination, sequentialReferenceMarkerName)
		for {
			select {
			case <-ticker.C:
				_, err := os.Lstat(marker)
				if err == nil {
					cancel()
					markerObserved <- nil
					return
				}
				if !os.IsNotExist(err) {
					cancel()
					markerObserved <- err
					return
				}
			case <-timeout.C:
				cancel()
				markerObserved <- errors.New("publication marker was not observed")
				return
			}
		}
	}()
	partial, err := RunSequentialReferenceToNewDestination(ctx, destination, manifest, bundle)
	if observeErr := <-markerObserved; observeErr != nil {
		t.Fatal(observeErr)
	}
	if err == nil || !errors.Is(err, ErrSequentialReferenceOutcomeUnknown) || !errors.Is(err, context.Canceled) || len(partial.Trials) == 0 {
		t.Fatalf("partial trials=%d err=%v", len(partial.Trials), err)
	}
	if _, err := os.Lstat(filepath.Join(destination, sequentialReferenceMarkerName)); err != nil {
		t.Fatalf("unknown publication lost its incomplete marker: %v", err)
	}
	if _, err := InspectSequentialReferencePublication(destination); err == nil {
		t.Fatal("unknown publication was accepted as complete")
	}
	if _, err := RunSequentialReferenceToNewDestination(context.Background(), destination, manifest, bundle); err == nil {
		t.Fatal("unknown publication was replayed")
	}
}

func sequentialReferenceWaitFixture(t *testing.T) (experiment.Manifest, SequentialReferenceBundle) {
	t.Helper()
	manifest, bundle := sequentialReferenceFixture(t)
	oldTreatment := bundle.Treatments[0]
	oldPlanSHA, err := executionbackend.PlanSHA256(oldTreatment.Plan)
	if err != nil {
		t.Fatal(err)
	}
	waitPlan := oldTreatment.Plan
	waitPlan.Program = executionbackend.Program{Kind: executionbackend.ProgramWaitForCancel}
	waitPlan.Artifacts = []executionbackend.ArtifactDeclaration{}
	waitPlan.Verifier = executionbackend.Verifier{Kind: executionbackend.VerifierProfileDecision}
	waitPlan.Resources.MaxArtifacts = 0
	newPlanSHA, err := executionbackend.PlanSHA256(waitPlan)
	if err != nil {
		t.Fatal(err)
	}
	design := manifest.Design
	design.DesignSHA256 = ""
	design.Treatments = slices.Clone(design.Treatments)
	replaced := false
	for index := range design.Treatments {
		if design.Treatments[index].ExecutionBindingSHA256 == oldPlanSHA {
			design.Treatments[index].ExecutionBindingSHA256 = newPlanSHA
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("wait fixture did not find its execution binding")
	}
	design, err = experiment.SealDesign(design)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = experiment.Compile(design, manifest.CapabilityContract, manifest.AnalysisPlan)
	if err != nil {
		t.Fatal(err)
	}
	plans := make(map[string]executionbackend.Plan, len(bundle.Treatments))
	inputs := make(map[string]ExecutionBackendReferenceInputs, len(bundle.Treatments))
	for _, treatment := range bundle.Treatments {
		planSHA, digestErr := executionbackend.PlanSHA256(treatment.Plan)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		plans[planSHA] = treatment.Plan
		inputs[planSHA] = treatment.Inputs
	}
	delete(plans, oldPlanSHA)
	delete(inputs, oldPlanSHA)
	plans[newPlanSHA] = waitPlan
	inputs[newPlanSHA] = oldTreatment.Inputs
	treatments := make([]SequentialReferenceTreatment, len(manifest.Treatments))
	for index, treatment := range manifest.Treatments {
		plan, planOK := plans[treatment.ExecutionBindingSHA256]
		input, inputOK := inputs[treatment.ExecutionBindingSHA256]
		if !planOK || !inputOK {
			t.Fatalf("wait fixture lost treatment binding %s", treatment.ExecutionBindingSHA256)
		}
		treatments[index] = SequentialReferenceTreatment{TreatmentID: treatment.ID, Plan: plan, Inputs: input}
	}
	bundle, err = NewSequentialReferenceBundle(manifest, bundle.GradingPlan, treatments)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, bundle
}

func sequentialReferenceFixture(t *testing.T) (experiment.Manifest, SequentialReferenceBundle) {
	t.Helper()
	definitions := sequentialReferenceArchive(t, map[string]string{"instructions.txt": "copy the selected public fixture\n"})
	fixture := sequentialReferenceArchive(t, map[string]string{
		"case.txt":    "synthetic public case\n",
		"control.txt": "separately authored negative control\n",
	})
	skill := sequentialReferenceArchive(t, map[string]string{"SKILL.md": "---\nname: summarize-rows\ndescription: Synthetic public skill.\n---\n"})
	definitionsSHA := sequentialReferenceArchiveSHA(t, definitions)
	fixtureSHA := sequentialReferenceArchiveSHA(t, fixture)
	skillSHA := sequentialReferenceArchiveSHA(t, skill)
	backend, err := HermeticReferenceExecutionBackendContract()
	if err != nil {
		t.Fatal(err)
	}
	newPlan := func(source executionbackend.MountID, path, expected string) executionbackend.Plan {
		plan, err := NewHermeticReferenceTrialPlan(backend, ExecutionBackendReferencePlanOptions{
			DefinitionsSHA256: definitionsSHA, FixtureSHA256: fixtureSHA, SkillSHA256: skillSHA,
			Resources: executionbackend.ResourcePolicy{DeadlineMillis: 15_000, MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20,
				MaxEntries: 16, MaxArtifacts: 1, MaxOperations: 1},
			Artifacts: []executionbackend.ArtifactDeclaration{{ID: "result", MaxBytes: 1 << 20, Privacy: executionbackend.PrivacyPublic}},
			Program:   executionbackend.Program{Kind: executionbackend.ProgramReferenceCopy, SourceMount: source, SourcePath: path, ArtifactID: "result"},
			Verifier:  executionbackend.Verifier{Kind: executionbackend.VerifierSHA256Equals, ArtifactID: "result", ExpectedSHA256: sequentialReferenceBytesSHA(expected)},
		})
		if err != nil {
			t.Fatalf("reference plan %s: %v", path, err)
		}
		return plan
	}
	referencePlan := newPlan(executionbackend.MountFixture, "case.txt", "synthetic public case\n")
	candidatePlan := newPlan(executionbackend.MountSkill, "SKILL.md", "---\nname: summarize-rows\ndescription: Synthetic public skill.\n---\n")
	controlPlan := newPlan(executionbackend.MountFixture, "control.txt", "separately authored negative control\n")
	referencePlanSHA, _ := executionbackend.PlanSHA256(referencePlan)
	candidatePlanSHA, _ := executionbackend.PlanSHA256(candidatePlan)
	controlPlanSHA, _ := executionbackend.PlanSHA256(controlPlan)
	capability, err := SequentialReferenceExperimentCapabilityContract()
	if err != nil {
		t.Fatal(err)
	}
	taskSHA := rootExperimentDigest("sequential-reference-task")
	gradingPlan, err := NewSequentialReferenceGradingPlan(taskSHA)
	if err != nil {
		t.Fatal(err)
	}
	gradingSHA, err := GradingPlanSHA256(gradingPlan)
	if err != nil {
		t.Fatal(err)
	}
	caseSHA := rootExperimentDigest("sequential-reference-case")
	caseBinding := experiment.CaseBinding{
		SourceKind: experiment.SourceAgentSkills, SourceSHA256: rootExperimentDigest("sequential-reference-agent-skills-source"),
		CaseSHA256: caseSHA, TaskSHA256: taskSHA, FixtureSHA256: fixtureSHA, GradingPlanSHA256: gradingSHA,
	}
	reference := experiment.ArmSelector{Condition: experiment.ConditionNone, ActivationChannel: experiment.ChannelImplicit,
		SelectionAuthority: experiment.SelectionNone, Control: experiment.ControlPositive}
	candidate := experiment.ArmSelector{Condition: experiment.ConditionCurrent, ActivationChannel: experiment.ChannelImplicit,
		SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive}
	control := experiment.ArmSelector{Condition: experiment.ConditionNone, ActivationChannel: experiment.ChannelImplicit,
		SelectionAuthority: experiment.SelectionNone, Control: experiment.ControlNearMissNegative}
	analysis, err := experiment.SealAnalysisPlan(experiment.AnalysisPlan{
		ConfidenceBasisPoints: 9500, MinimumInferenceBlocks: 2, BootstrapSamples: 1000,
		BootstrapSeedSHA256: rootExperimentDigest("sequential-reference-bootstrap"), Multiplicity: experiment.MultiplicityHolm,
		RepeatedAttempts: experiment.RepeatedAttemptPolicy{Kind: experiment.RepeatedAttemptsNone, K: []uint32{1}},
		Stages:           sequentialReferenceStageDeclarations(),
		Metrics: []experiment.MetricDeclaration{{ID: experiment.MetricOutcome, Kind: experiment.MetricBinary, Role: experiment.MetricPrimary,
			Direction: experiment.DirectionHigher, Capability: experiment.CapabilityObserveOutcome, FamilySHA256: rootExperimentDigest("sequential-reference-outcome")}},
		Comparisons: []experiment.Comparison{{Reference: reference, Candidate: candidate,
			Stages: []experiment.FunnelStage{experiment.StageVerifierOutcome}, Metrics: []experiment.MetricID{experiment.MetricOutcome}}},
		AllowedExclusions: []experiment.ExclusionReason{experiment.ExclusionGradeIncomplete, experiment.ExclusionLifecycleIncomplete, experiment.ExclusionLifecycleUnknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	design, err := experiment.SealDesign(experiment.Design{
		CompatibilityProfile: experiment.CompatibilityNone, CapabilityContractSHA256: capability.CapabilityContractSHA256,
		AnalysisPlanSHA256: analysis.AnalysisPlanSHA256, Case: caseBinding,
		Treatments: []experiment.TreatmentRequest{
			{Arm: reference, Role: experiment.RoleReference, DistractorSHA256: []string{}, ControlSHA256: caseSHA,
				ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: referencePlanSHA},
			{Arm: candidate, Role: experiment.RoleCandidate, SkillSHA256: skillSHA, DistractorSHA256: []string{}, ControlSHA256: caseSHA,
				ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: candidatePlanSHA, ExpectedActivation: true},
			{Arm: control, Role: experiment.RoleControl, DistractorSHA256: []string{}, ControlSHA256: rootExperimentDigest("sequential-reference-negative-control"),
				ControlProvenance: experiment.ControlSeparatelyAuthored, ExecutionBindingSHA256: controlPlanSHA},
		},
		Strata:   []experiment.StratumRequest{{BindingSHA256: rootExperimentDigest("sequential-reference-stratum"), Blocks: 6}},
		Ordering: experiment.OrderingPolicy{Kind: experiment.OrderingWilliams, SeedSHA256: rootExperimentDigest("sequential-reference-order"), LegacySequence: []experiment.ArmSelector{}},
		Stopping: experiment.StoppingRule{Kind: experiment.StoppingFixedRoster, MaximumBlocks: 6, SafetyStops: []experiment.SafetyStopCode{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := experiment.Compile(design, capability, analysis)
	if err != nil {
		t.Fatal(err)
	}
	plans := map[string]executionbackend.Plan{referencePlanSHA: referencePlan, candidatePlanSHA: candidatePlan, controlPlanSHA: controlPlan}
	treatments := make([]SequentialReferenceTreatment, len(manifest.Treatments))
	for index, treatment := range manifest.Treatments {
		plan, ok := plans[treatment.ExecutionBindingSHA256]
		if !ok {
			t.Fatalf("compiled treatment has unknown plan %s", treatment.ExecutionBindingSHA256)
		}
		treatments[index] = SequentialReferenceTreatment{
			TreatmentID: treatment.ID, Plan: plan,
			Inputs: ExecutionBackendReferenceInputs{Definitions: definitions, Fixture: fixture, Skill: skill},
		}
	}
	bundle, err := NewSequentialReferenceBundle(manifest, gradingPlan, treatments)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, bundle
}

func sequentialReferenceStageDeclarations() []experiment.StageDeclaration {
	stages := []struct {
		stage      experiment.FunnelStage
		capability experiment.CapabilityID
	}{
		{experiment.StageCandidateRecall, experiment.CapabilityObserveCandidateRecall},
		{experiment.StageSelection, experiment.CapabilityObserveSelection},
		{experiment.StageLoad, experiment.CapabilityObserveLoad},
		{experiment.StageInstructionAccess, experiment.CapabilityObserveInstructionAccess},
		{experiment.StageReferenceAccess, experiment.CapabilityObserveReferenceAccess},
		{experiment.StageScriptAccess, experiment.CapabilityObserveScriptAccess},
		{experiment.StageUsefulAdherence, experiment.CapabilityObserveUsefulAdherence},
		{experiment.StageVerifierOutcome, experiment.CapabilityObserveVerifierOutcome},
	}
	result := make([]experiment.StageDeclaration, len(stages))
	for index, stage := range stages {
		result[index] = experiment.StageDeclaration{Stage: stage.stage, Role: experiment.MetricConfirmatory,
			Capability: stage.capability, FamilySHA256: rootExperimentDigest("sequential-reference-stage-" + string(stage.stage))}
	}
	return result
}

func sequentialReferenceArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, name := range names {
		data := []byte(files[name])
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(data)),
			ModTime: sequentialReferenceTarEpoch(), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func sequentialReferenceArchiveSHA(t *testing.T, data []byte) string {
	t.Helper()
	digest, err := executionbackend.ArchiveSHA256(data, 1<<20, 16)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func sequentialReferenceBytesSHA(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func newSequentialReferenceStoreForTest(t *testing.T, manifest experiment.Manifest) *AttemptLedgerStore {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := CreateSequentialReferenceAttemptStore(filepath.Join(parent, "ledger"), manifest)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func agentadapterReferenceContractForTest() (AgentAdapterContract, error) {
	return SequentialReferenceAgentAdapterContract()
}

func jsonMarshalForSequentialReferenceTest(value any) ([]byte, error) {
	return json.Marshal(value)
}

func sequentialReferenceTarEpoch() time.Time { return time.Unix(0, 0).UTC() }
