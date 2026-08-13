package experiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestCompileIsDeterministicAndCallerOrderIndependent(t *testing.T) {
	capability, analysis, design := testContracts(t, 4)
	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	capabilityInput := cloneCapabilityContract(capability)
	capabilityInput.CapabilityContractSHA256 = ""
	slices.Reverse(capabilityInput.Capabilities)
	capabilityAgain, err := SealCapabilityContract(capabilityInput)
	if err != nil {
		t.Fatalf("reseal capability: %v", err)
	}
	analysisInput := cloneAnalysisPlan(analysis)
	analysisInput.AnalysisPlanSHA256 = ""
	slices.Reverse(analysisInput.Stages)
	slices.Reverse(analysisInput.Metrics)
	slices.Reverse(analysisInput.Comparisons)
	for index := range analysisInput.Comparisons {
		slices.Reverse(analysisInput.Comparisons[index].Stages)
		slices.Reverse(analysisInput.Comparisons[index].Metrics)
	}
	analysisAgain, err := SealAnalysisPlan(analysisInput)
	if err != nil {
		t.Fatalf("reseal analysis: %v", err)
	}
	designInput := cloneDesign(design)
	designInput.DesignSHA256 = ""
	slices.Reverse(designInput.Treatments)
	designInput.CapabilityContractSHA256 = capabilityAgain.CapabilityContractSHA256
	designInput.AnalysisPlanSHA256 = analysisAgain.AnalysisPlanSHA256
	designAgain, err := SealDesign(designInput)
	if err != nil {
		t.Fatalf("reseal design: %v", err)
	}
	manifestAgain, err := Compile(designAgain, capabilityAgain, analysisAgain)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if !reflect.DeepEqual(manifest, manifestAgain) {
		t.Fatal("caller ordering changed the compiled manifest")
	}
}

func TestCompileWilliamsOrdersBalancePositionAndCarryover(t *testing.T) {
	for _, treatmentCount := range []int{2, 3, 4, 5} {
		t.Run(string(rune('0'+treatmentCount)), func(t *testing.T) {
			capability, analysis, design := testContracts(t, treatmentCount)
			manifest, err := Compile(design, capability, analysis)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			position := map[string][]int{}
			carryover := map[string]int{}
			for _, block := range manifest.Blocks {
				for index, assignment := range block.Assignments {
					if position[assignment.TreatmentID] == nil {
						position[assignment.TreatmentID] = make([]int, treatmentCount)
					}
					position[assignment.TreatmentID][index]++
					if index > 0 {
						key := block.Assignments[index-1].TreatmentID + "\x00" + assignment.TreatmentID
						carryover[key]++
					}
				}
			}
			for treatmentID, counts := range position {
				for index := 1; index < len(counts); index++ {
					if counts[index] != counts[0] {
						t.Fatalf("treatment %s position counts are not balanced: %v", treatmentID, counts)
					}
				}
			}
			wantCarryover := 1
			if treatmentCount%2 == 1 {
				wantCarryover = 2
			}
			for _, left := range manifest.Treatments {
				for _, right := range manifest.Treatments {
					if left.ID == right.ID {
						continue
					}
					if got := carryover[left.ID+"\x00"+right.ID]; got != wantCarryover {
						t.Fatalf("carryover %s -> %s = %d, want %d", left.ID, right.ID, got, wantCarryover)
					}
				}
			}
		})
	}
}

func TestCompileBindsContentButNotOrderIntoStableMemberIdentities(t *testing.T) {
	capability, analysis, design := testContracts(t, 4)
	first, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile first: %v", err)
	}
	changed := cloneDesign(design)
	changed.DesignSHA256 = ""
	changed.Ordering.SeedSHA256 = testDigest("different-order")
	changed, err = SealDesign(changed)
	if err != nil {
		t.Fatalf("seal changed order: %v", err)
	}
	second, err := Compile(changed, capability, analysis)
	if err != nil {
		t.Fatalf("compile second: %v", err)
	}
	if first.ManifestSHA256 == second.ManifestSHA256 {
		t.Fatal("ordering seed did not change manifest identity")
	}
	for index := range first.Treatments {
		if first.Treatments[index].ID != second.Treatments[index].ID {
			t.Fatal("ordering seed changed treatment identity")
		}
	}
	changedOrder := false
	for index := range first.Blocks {
		if first.Blocks[index].ID != second.Blocks[index].ID {
			t.Fatal("within-block ordering changed block identity")
		}
		if !reflect.DeepEqual(first.Blocks[index].Assignments, second.Blocks[index].Assignments) {
			changedOrder = true
		}
	}
	if !changedOrder {
		t.Fatal("test seeds unexpectedly produced the same treatment order")
	}

	request := design.Treatments[0]
	firstID, err := treatmentID(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Role = RoleCandidate
	secondID, err := treatmentID(request)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatal("analysis role changed treatment identity")
	}
}

func TestCompileRefusesUnsupportedAmbiguousAndIncompleteDesigns(t *testing.T) {
	capability, analysis, design := testContracts(t, 4)

	unsupportedInput := cloneCapabilityContract(capability)
	unsupportedInput.CapabilityContractSHA256 = ""
	for index := range unsupportedInput.Capabilities {
		if unsupportedInput.Capabilities[index].ID == CapabilityConditionCurrent {
			unsupportedInput.Capabilities[index].Support = SupportUnsupported
			unsupportedInput.Capabilities[index].BindingSHA256 = ""
		}
	}
	unsupported, err := SealCapabilityContract(unsupportedInput)
	if err != nil {
		t.Fatalf("seal unsupported capability: %v", err)
	}
	unsupportedDesign := cloneDesign(design)
	unsupportedDesign.DesignSHA256 = ""
	unsupportedDesign.CapabilityContractSHA256 = unsupported.CapabilityContractSHA256
	unsupportedDesign, err = SealDesign(unsupportedDesign)
	if err != nil {
		t.Fatalf("seal unsupported design: %v", err)
	}
	if _, err := Compile(unsupportedDesign, unsupported, analysis); codeOf(t, err) != ErrorUnsupportedDesign {
		t.Fatalf("unsupported capability code = %v, want %s", err, ErrorUnsupportedDesign)
	}

	duplicate := cloneDesign(design)
	duplicate.DesignSHA256 = ""
	duplicate.Treatments[1].ExecutionBindingSHA256 = duplicate.Treatments[0].ExecutionBindingSHA256
	if _, err := SealDesign(duplicate); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("duplicate execution binding code = %v, want %s", err, ErrorInvalidDesign)
	}

	incomplete := cloneDesign(design)
	incomplete.DesignSHA256 = ""
	incomplete.Strata[0].Blocks--
	incomplete.Stopping.MaximumBlocks--
	if _, err := SealDesign(incomplete); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("incomplete Williams cycle code = %v, want %s", err, ErrorInvalidDesign)
	}

	overlongRepeated := cloneAnalysisPlan(analysis)
	overlongRepeated.AnalysisPlanSHA256 = ""
	overlongRepeated.RepeatedAttempts = RepeatedAttemptPolicy{Kind: RepeatedAttemptsAll, K: []uint32{1, 5}}
	overlongRepeated, err = SealAnalysisPlan(overlongRepeated)
	if err != nil {
		t.Fatalf("seal overlong repeated-attempt policy: %v", err)
	}
	overlongDesign := cloneDesign(design)
	overlongDesign.DesignSHA256 = ""
	overlongDesign.AnalysisPlanSHA256 = overlongRepeated.AnalysisPlanSHA256
	overlongDesign, err = SealDesign(overlongDesign)
	if err != nil {
		t.Fatalf("seal overlong repeated-attempt design: %v", err)
	}
	if _, err := Compile(overlongDesign, capability, overlongRepeated); codeOf(t, err) != ErrorUnsupportedDesign {
		t.Fatalf("overlong repeated-attempt code = %v, want %s", err, ErrorUnsupportedDesign)
	}

	misclassifiedCompatibility := cloneDesign(design)
	misclassifiedCompatibility.DesignSHA256 = ""
	misclassifiedCompatibility.CompatibilityProfile = CompatibilityPrivateActivationV2
	if _, err := SealDesign(misclassifiedCompatibility); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("non-private compatibility design code = %v, want %s", err, ErrorInvalidDesign)
	}

	largeCapability, largeAnalysis, largeDesign := testPairLimitContracts(t)
	if _, err := Compile(largeDesign, largeCapability, largeAnalysis); codeOf(t, err) != ErrorLimitExceeded {
		t.Fatalf("oversized pair roster code = %v, want %s", err, ErrorLimitExceeded)
	}
}

func TestValidateManifestReplaysEveryCrossContractAdmission(t *testing.T) {
	capability, analysis, design := testContracts(t, 2)

	minimumInput := cloneAnalysisPlan(analysis)
	minimumInput.AnalysisPlanSHA256 = ""
	minimumInput.MinimumInferenceBlocks = totalDesignBlocks(design) + 1
	minimum, err := SealAnalysisPlan(minimumInput)
	if err != nil {
		t.Fatalf("seal minimum-block analysis: %v", err)
	}
	minimumDesignInput := cloneDesign(design)
	minimumDesignInput.DesignSHA256 = ""
	minimumDesignInput.AnalysisPlanSHA256 = minimum.AnalysisPlanSHA256
	minimumDesign, err := SealDesign(minimumDesignInput)
	if err != nil {
		t.Fatalf("seal minimum-block design: %v", err)
	}
	if err := ValidateManifest(derivedManifestWithoutAdmission(t, minimumDesign, capability, minimum)); codeOf(t, err) != ErrorInvalidManifest {
		t.Fatalf("minimum-block manifest code = %v, want %s", err, ErrorInvalidManifest)
	}

	repeatedInput := cloneAnalysisPlan(analysis)
	repeatedInput.AnalysisPlanSHA256 = ""
	repeatedInput.RepeatedAttempts = RepeatedAttemptPolicy{Kind: RepeatedAttemptsAll, K: []uint32{1, totalDesignBlocks(design) + 1}}
	repeated, err := SealAnalysisPlan(repeatedInput)
	if err != nil {
		t.Fatalf("seal repeated-attempt analysis: %v", err)
	}
	repeatedDesignInput := cloneDesign(design)
	repeatedDesignInput.DesignSHA256 = ""
	repeatedDesignInput.AnalysisPlanSHA256 = repeated.AnalysisPlanSHA256
	repeatedDesign, err := SealDesign(repeatedDesignInput)
	if err != nil {
		t.Fatalf("seal repeated-attempt design: %v", err)
	}
	if err := ValidateManifest(derivedManifestWithoutAdmission(t, repeatedDesign, capability, repeated)); codeOf(t, err) != ErrorInvalidManifest {
		t.Fatalf("repeated-attempt manifest code = %v, want %s", err, ErrorInvalidManifest)
	}

	largeCapability, largeAnalysis, largeDesign := testPairLimitContracts(t)
	if err := ValidateManifest(derivedManifestWithoutAdmission(t, largeDesign, largeCapability, largeAnalysis)); codeOf(t, err) != ErrorInvalidManifest {
		t.Fatalf("oversized-pair manifest code = %v, want %s", err, ErrorInvalidManifest)
	}
}

func TestNegativeControlsMustBeSeparatelyAuthoredAndAreNeverSynthesized(t *testing.T) {
	capability, analysis, design := testContracts(t, 5)
	designInput := cloneDesign(design)
	designInput.DesignSHA256 = ""
	designInput.Case.SourceKind = SourceAgentSkills
	designInput.Treatments[len(designInput.Treatments)-1].ControlProvenance = ControlFromSource
	if _, err := SealDesign(designInput); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("source-derived negative code = %v, want %s", err, ErrorInvalidDesign)
	}

	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(manifest.Treatments) != len(design.Treatments) {
		t.Fatalf("compiler synthesized treatments: got %d want %d", len(manifest.Treatments), len(design.Treatments))
	}
	for _, treatment := range manifest.Treatments {
		if treatment.Arm.Condition == ConditionForcedOracle && treatment.AutonomousRoutingEligible {
			t.Fatal("forced treatment was marked autonomous")
		}
	}
}

func TestCompileCoversClosedTreatmentAndControlMatrix(t *testing.T) {
	capability, analysis, design := testMatrixContracts(t)
	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile matrix: %v", err)
	}
	if len(manifest.Treatments) != 13 || !manifest.PositionBalanceComplete {
		t.Fatalf("matrix treatments=%d balanced=%t", len(manifest.Treatments), manifest.PositionBalanceComplete)
	}

	conditions := map[Condition]bool{}
	controls := map[ControlClass]bool{}
	for _, treatment := range manifest.Treatments {
		conditions[treatment.Arm.Condition] = true
		controls[treatment.Arm.Control] = true
		if treatment.Arm.Condition == ConditionForcedOracle && treatment.AutonomousRoutingEligible {
			t.Fatal("forced oracle was classified as autonomous routing")
		}
		if treatment.Arm.Condition == ConditionAutonomousOracle && !treatment.AutonomousRoutingEligible {
			t.Fatal("autonomous oracle was not eligible for autonomous routing")
		}
		if treatment.AutonomousRoutingEligible && treatment.Arm.ActivationChannel != ChannelImplicit &&
			treatment.Arm.ActivationChannel != ChannelAdapterNative {
			t.Fatalf("directed activation channel was classified as autonomous routing: %+v", treatment)
		}
		if treatment.Arm.Control != ControlPositive {
			if treatment.ControlProvenance != ControlSeparatelyAuthored || treatment.ExpectedActivation {
				t.Fatalf("negative control was not separately authored and activation-negative: %+v", treatment)
			}
		}
	}
	for _, condition := range []Condition{
		ConditionNone, ConditionCurrent, ConditionPrevious, ConditionForcedOracle,
		ConditionAutonomousOracle, ConditionOracleDistractors,
		ConditionRetrievedPresent, ConditionRetrievedAbsent,
	} {
		if !conditions[condition] {
			t.Fatalf("condition %s is absent from matrix", condition)
		}
	}
	for _, control := range []ControlClass{
		ControlPositive, ControlNearMissNegative, ControlIrrelevant,
		ControlUnsupportedDomain, ControlStaleVersionMismatch, ControlAdversarialDistractor,
	} {
		if !controls[control] {
			t.Fatalf("control %s is absent from matrix", control)
		}
	}
}

func TestCompileRefusesAnyUnobservableSelectedCellOrFunnelStage(t *testing.T) {
	capability, analysis, design := testMatrixContracts(t)
	for _, identifier := range []CapabilityID{
		CapabilityConditionRetrievedAbsent,
		CapabilityChannelDeveloper,
		CapabilityControlAdversarialDistractor,
		CapabilityObserveLoad,
	} {
		t.Run(string(identifier), func(t *testing.T) {
			unsupportedInput := cloneCapabilityContract(capability)
			unsupportedInput.CapabilityContractSHA256 = ""
			for index := range unsupportedInput.Capabilities {
				if unsupportedInput.Capabilities[index].ID == identifier {
					unsupportedInput.Capabilities[index].Support = SupportUnsupported
					unsupportedInput.Capabilities[index].BindingSHA256 = ""
				}
			}
			unsupported, err := SealCapabilityContract(unsupportedInput)
			if err != nil {
				t.Fatalf("seal unsupported capability: %v", err)
			}
			unsupportedDesign := cloneDesign(design)
			unsupportedDesign.DesignSHA256 = ""
			unsupportedDesign.CapabilityContractSHA256 = unsupported.CapabilityContractSHA256
			unsupportedDesign, err = SealDesign(unsupportedDesign)
			if err != nil {
				t.Fatalf("seal unsupported design: %v", err)
			}
			if _, err := Compile(unsupportedDesign, unsupported, analysis); codeOf(t, err) != ErrorUnsupportedDesign {
				t.Fatalf("compile code = %v, want %s", err, ErrorUnsupportedDesign)
			}
		})
	}

	ambiguous := cloneDesign(design)
	ambiguous.DesignSHA256 = ""
	for index := range ambiguous.Treatments {
		if ambiguous.Treatments[index].Arm.Condition == ConditionRetrievedPresent {
			ambiguous.Treatments[index].DistractorSHA256 = []string{testDigest("unbound-retrieval-distractor")}
		}
	}
	if _, err := SealDesign(ambiguous); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("ambiguous retrieval+distractor code = %v, want %s", err, ErrorInvalidDesign)
	}

	aliasedDistractor := cloneDesign(design)
	aliasedDistractor.DesignSHA256 = ""
	for index := range aliasedDistractor.Treatments {
		if aliasedDistractor.Treatments[index].Arm.Condition == ConditionOracleDistractors {
			aliasedDistractor.Treatments[index].DistractorSHA256 = []string{aliasedDistractor.Treatments[index].SkillSHA256}
		}
	}
	if _, err := SealDesign(aliasedDistractor); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("oracle-as-distractor code = %v, want %s", err, ErrorInvalidDesign)
	}

	duplicateControl := cloneDesign(design)
	duplicateControl.DesignSHA256 = ""
	var controlSHA256 string
	for index := range duplicateControl.Treatments {
		if duplicateControl.Treatments[index].Arm.Control != ControlPositive {
			if controlSHA256 == "" {
				controlSHA256 = duplicateControl.Treatments[index].ControlSHA256
			} else {
				duplicateControl.Treatments[index].ControlSHA256 = controlSHA256
				break
			}
		}
	}
	if _, err := SealDesign(duplicateControl); codeOf(t, err) != ErrorInvalidDesign {
		t.Fatalf("duplicate negative control code = %v, want %s", err, ErrorInvalidDesign)
	}

	incompleteAnalysis := cloneAnalysisPlan(analysis)
	incompleteAnalysis.AnalysisPlanSHA256 = ""
	incompleteAnalysis.Stages = incompleteAnalysis.Stages[:len(incompleteAnalysis.Stages)-1]
	if _, err := SealAnalysisPlan(incompleteAnalysis); codeOf(t, err) != ErrorInvalidAnalysis {
		t.Fatalf("incomplete funnel declaration code = %v, want %s", err, ErrorInvalidAnalysis)
	}

	missingPrimaryMetric := cloneAnalysisPlan(analysis)
	missingPrimaryMetric.AnalysisPlanSHA256 = ""
	missingPrimaryMetric.Comparisons[0].Metrics = []MetricID{MetricDurationMillis}
	if _, err := SealAnalysisPlan(missingPrimaryMetric); codeOf(t, err) != ErrorInvalidAnalysis {
		t.Fatalf("unselected primary metric code = %v, want %s", err, ErrorInvalidAnalysis)
	}

	missingPrimaryStage := cloneAnalysisPlan(analysis)
	missingPrimaryStage.AnalysisPlanSHA256 = ""
	for index := range missingPrimaryStage.Metrics {
		missingPrimaryStage.Metrics[index].Role = MetricExploratory
	}
	for index := range missingPrimaryStage.Stages {
		if missingPrimaryStage.Stages[index].Stage == StageVerifierOutcome {
			missingPrimaryStage.Stages[index].Role = MetricPrimary
		}
	}
	missingPrimaryStage.Comparisons[0].Stages = append([]FunnelStage{}, closedStages[:len(closedStages)-1]...)
	if _, err := SealAnalysisPlan(missingPrimaryStage); codeOf(t, err) != ErrorInvalidAnalysis {
		t.Fatalf("unselected primary stage code = %v, want %s", err, ErrorInvalidAnalysis)
	}
}

func TestTrialRecordsKeepFunnelFailuresFalseLoadsAndNegativeLiftIndependent(t *testing.T) {
	capability, analysis, design := testMatrixContracts(t)
	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile matrix: %v", err)
	}
	current := findMatrixTreatment(t, manifest, ConditionCurrent, ControlPositive)
	baseline := sealMatrixTrial(t, manifest, analysis, current, "baseline", "", 1, false)
	for _, failure := range []struct {
		name  string
		stage FunnelStage
	}{
		{"retrieval", StageCandidateRecall},
		{"selection", StageSelection},
		{"activation", StageLoad},
		{"instruction", StageInstructionAccess},
		{"reference", StageReferenceAccess},
		{"script", StageScriptAccess},
		{"use", StageUsefulAdherence},
		{"verifier", StageVerifierOutcome},
	} {
		t.Run(failure.name, func(t *testing.T) {
			record := sealMatrixTrial(t, manifest, analysis, current, failure.name, failure.stage, 1, false)
			if record.RecordSHA256 == baseline.RecordSHA256 || metricValue(t, record, MetricOutcome) != 1 {
				t.Fatal("funnel failure changed or erased the independent outcome")
			}
			for _, stage := range record.Stages {
				want := stage.Stage != failure.stage
				if stage.Value == nil || *stage.Value != want {
					t.Fatalf("stage %s=%v, want %t", stage.Stage, stage.Value, want)
				}
			}
		})
	}

	for _, control := range []struct {
		name      string
		condition Condition
		class     ControlClass
	}{
		{"oracle-absent", ConditionRetrievedAbsent, ControlPositive},
		{"distractor", ConditionOracleDistractors, ControlAdversarialDistractor},
		{"stale-skill", ConditionPrevious, ControlStaleVersionMismatch},
	} {
		t.Run(control.name, func(t *testing.T) {
			treatment := findMatrixTreatment(t, manifest, control.condition, control.class)
			if treatment.ExpectedActivation {
				t.Fatal("negative treatment expects activation")
			}
			record := sealMatrixTrial(t, manifest, analysis, treatment, control.name, "", 1, true)
			if stageValue(t, record, StageLoad) != true || metricValue(t, record, MetricOutcome) != 1 {
				t.Fatal("false load was conflated with unrelated outcome")
			}
		})
	}

	reference := findMatrixTreatment(t, manifest, ConditionNone, ControlPositive)
	referenceRecord := sealMatrixTrial(t, manifest, analysis, reference, "reference", "", 1, false)
	candidateRecord := sealMatrixTrial(t, manifest, analysis, current, "candidate-negative-lift", "", 0, false)
	if metricValue(t, referenceRecord, MetricOutcome) != 1 || metricValue(t, candidateRecord, MetricOutcome) != 0 ||
		referenceRecord.LifecycleState != LifecycleSucceeded || candidateRecord.LifecycleState != LifecycleSucceeded {
		t.Fatal("negative lift could not be represented independently of lifecycle success")
	}
}

func TestTrialRecordKeepsLifecycleOutcomeAndMissingnessSeparate(t *testing.T) {
	capability, analysis, design := testContracts(t, 2)
	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	assignment := manifest.Blocks[0].Assignments[0]
	falseValue := false
	zero := uint64(0)
	record, err := SealTrialRecord(manifest, TrialRecord{
		TrialID:                assignment.TrialID,
		BlockID:                manifest.Blocks[0].ID,
		TreatmentID:            assignment.TreatmentID,
		AttemptPlanSHA256:      testDigest("attempt-plan"),
		LifecycleState:         LifecycleSucceeded,
		Eligibility:            EligibilitySupported,
		Exclusion:              ExclusionNone,
		AgentObservationSHA256: testDigest("observation"),
		GradeReceiptSHA256:     testDigest("grade"),
		LifecycleEventSHA256:   testDigest("lifecycle"),
		Stages:                 testStageObservations(&falseValue),
		Metrics:                testMetricObservations(analysis, &zero),
	})
	if err != nil {
		t.Fatalf("seal record: %v", err)
	}
	if record.LifecycleState != LifecycleSucceeded || metricValue(t, record, MetricOutcome) != 0 {
		t.Fatal("lifecycle success was conflated with task outcome")
	}

	unknown := cloneTrialRecord(record)
	unknown.RecordSHA256 = ""
	for index := range unknown.Metrics {
		if unknown.Metrics[index].Metric == MetricDurationMillis {
			unknown.Metrics[index].Presence = PresenceUnknown
			unknown.Metrics[index].Value = nil
		}
	}
	unknown, err = SealTrialRecord(manifest, unknown)
	if err != nil {
		t.Fatalf("seal unknown record: %v", err)
	}
	if unknown.RecordSHA256 == record.RecordSHA256 {
		t.Fatal("observed zero and unknown metric have the same identity")
	}

	postHoc := cloneTrialRecord(record)
	postHoc.RecordSHA256 = ""
	postHoc.Eligibility = EligibilityIneligible
	postHoc.Exclusion = ExclusionGradeIncomplete
	postHoc.GradeReceiptSHA256 = ""
	if _, err := SealTrialRecord(manifest, postHoc); codeOf(t, err) != ErrorInvalidTrial {
		t.Fatalf("unregistered exclusion code = %v, want %s", err, ErrorInvalidTrial)
	}
}

func TestExperimentCodecsAreClosedCanonicalAndFutureRejecting(t *testing.T) {
	capability, analysis, design := testContracts(t, 2)
	manifest, err := Compile(design, capability, analysis)
	if err != nil {
		t.Fatal(err)
	}
	assignment := manifest.Blocks[0].Assignments[0]
	value := true
	one := uint64(1)
	record, err := SealTrialRecord(manifest, TrialRecord{
		TrialID: assignment.TrialID, BlockID: manifest.Blocks[0].ID, TreatmentID: assignment.TreatmentID,
		AttemptPlanSHA256: testDigest("attempt"), LifecycleState: LifecycleSucceeded, Eligibility: EligibilitySupported,
		Exclusion: ExclusionNone, AgentObservationSHA256: testDigest("observation"), GradeReceiptSHA256: testDigest("grade"),
		LifecycleEventSHA256: testDigest("event"), Stages: testStageObservations(&value), Metrics: testMetricObservations(analysis, &one),
	})
	if err != nil {
		t.Fatal(err)
	}

	capabilityBytes, _ := EncodeCapabilityContract(capability)
	designBytes, _ := EncodeDesign(design)
	analysisBytes, _ := EncodeAnalysisPlan(analysis)
	manifestBytes, _ := EncodeManifest(manifest)
	recordBytes, _ := EncodeTrialRecord(manifest, record)
	validator, err := NewTrialRecordValidator(manifest)
	if err != nil {
		t.Fatal(err)
	}
	validatedRecord, err := validator.Decode(bytes.NewReader(recordBytes))
	if err != nil || !reflect.DeepEqual(validatedRecord, record) {
		t.Fatalf("prevalidated trial decode err=%v equal=%t", err, reflect.DeepEqual(validatedRecord, record))
	}
	roundTrips := []struct {
		name string
		data []byte
		read func([]byte) error
	}{
		{"capability", capabilityBytes, func(data []byte) error { _, err := DecodeCapabilityContract(bytes.NewReader(data)); return err }},
		{"design", designBytes, func(data []byte) error { _, err := DecodeDesign(bytes.NewReader(data)); return err }},
		{"analysis", analysisBytes, func(data []byte) error { _, err := DecodeAnalysisPlan(bytes.NewReader(data)); return err }},
		{"manifest", manifestBytes, func(data []byte) error { _, err := DecodeManifest(bytes.NewReader(data)); return err }},
		{"trial", recordBytes, func(data []byte) error { _, err := DecodeTrialRecord(bytes.NewReader(data), manifest); return err }},
	}
	for _, test := range roundTrips {
		t.Run(test.name, func(t *testing.T) {
			if err := test.read(test.data); err != nil {
				t.Fatalf("round trip: %v", err)
			}
			for name, mutation := range map[string][]byte{
				"missing-newline": test.data[:len(test.data)-1],
				"carriage-return": append(append([]byte{}, test.data[:len(test.data)-1]...), '\r', '\n'),
				"unknown":         bytes.Replace(test.data, []byte(`{"schema":`), []byte(`{"unknown":0,"schema":`), 1),
				"duplicate":       bytes.Replace(test.data, []byte(`{"schema":`), []byte(`{"schema":"duplicate","schema":`), 1),
			} {
				t.Run(name, func(t *testing.T) {
					if err := test.read(mutation); err == nil {
						t.Fatal("hostile mutation was accepted")
					}
				})
			}
		})
	}

	future := cloneCapabilityContract(capability)
	future.SchemaVersion = SchemaVersion + 1
	future.CapabilityContractSHA256 = ""
	future.CapabilityContractSHA256, err = digestCapabilityContract(future)
	if err != nil {
		t.Fatal(err)
	}
	futureBytes, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	futureBytes = append(futureBytes, '\n')
	if _, err := DecodeCapabilityContract(bytes.NewReader(futureBytes)); err == nil {
		t.Fatal("future schema version was accepted")
	}
}

func testContracts(t *testing.T, treatmentCount int) (CapabilityContract, AnalysisPlan, Design) {
	t.Helper()
	capabilityInput := CapabilityContract{
		Runtime: RuntimeBinding{
			AgentSHA256: testDigest("agent"), ModelSHA256: testDigest("model"), EnvironmentSHA256: testDigest("environment"),
			AdapterSHA256: testDigest("adapter"), ExecutionBackendSHA256: testDigest("execution-backend"),
			GraderSHA256: testDigest("grader"), HarnessSHA256: testDigest("harness"),
			BudgetsSHA256: testDigest("budgets"), AuthoritySHA256: testDigest("authority"),
		},
		Capabilities: make([]Capability, len(closedCapabilities)),
	}
	for index, identifier := range closedCapabilities {
		capabilityInput.Capabilities[index] = Capability{ID: identifier, Support: SupportSupported, BindingSHA256: testDigest(string(identifier))}
	}
	capability, err := SealCapabilityContract(capabilityInput)
	if err != nil {
		t.Fatalf("seal capability: %v: %v", err, errors.Unwrap(err))
	}
	caseDigest := testDigest("case")
	treatments := testTreatments(caseDigest, treatmentCount)
	metrics := []MetricDeclaration{
		{ID: MetricOutcome, Kind: MetricBinary, Role: MetricPrimary, Direction: DirectionHigher, Capability: CapabilityObserveOutcome, FamilySHA256: testDigest("outcome-family")},
		{ID: MetricDurationMillis, Kind: MetricCount, Role: MetricExploratory, Direction: DirectionLower, Capability: CapabilityObserveDuration, FamilySHA256: testDigest("duration-family")},
	}
	analysis, err := SealAnalysisPlan(AnalysisPlan{
		ConfidenceBasisPoints: 9500, MinimumInferenceBlocks: 2, BootstrapSamples: 1000,
		BootstrapSeedSHA256: testDigest("bootstrap"), Multiplicity: MultiplicityHolm,
		RepeatedAttempts: RepeatedAttemptPolicy{Kind: RepeatedAttemptsNone, K: []uint32{1}},
		Stages:           testStageDeclarations(),
		Metrics:          metrics,
		Comparisons: []Comparison{{
			Reference: treatments[0].Arm,
			Candidate: treatments[1].Arm,
			Stages:    append([]FunnelStage{}, closedStages...),
			Metrics:   []MetricID{MetricOutcome, MetricDurationMillis},
		}},
		AllowedExclusions: []ExclusionReason{ExclusionLifecycleUnknown, ExclusionLifecycleIncomplete, ExclusionDrift},
	})
	if err != nil {
		t.Fatalf("seal analysis: %v", err)
	}
	cycle := treatmentCount
	if cycle%2 == 1 {
		cycle *= 2
	}
	design, err := SealDesign(Design{
		CompatibilityProfile:     CompatibilityNone,
		CapabilityContractSHA256: capability.CapabilityContractSHA256,
		AnalysisPlanSHA256:       analysis.AnalysisPlanSHA256,
		Case: CaseBinding{
			SourceKind: SourceNative, SourceSHA256: testDigest("source"), CaseSHA256: caseDigest,
			TaskSHA256: testDigest("task"), FixtureSHA256: testDigest("fixture"), GradingPlanSHA256: testDigest("grading"),
		},
		Treatments: treatments,
		Strata:     []StratumRequest{{BindingSHA256: testDigest("stratum"), Blocks: uint32(cycle)}},
		Ordering:   OrderingPolicy{Kind: OrderingWilliams, SeedSHA256: testDigest("order"), LegacySequence: []ArmSelector{}},
		Stopping:   StoppingRule{Kind: StoppingFixedRoster, MaximumBlocks: uint32(cycle), SafetyStops: []SafetyStopCode{}},
	})
	if err != nil {
		t.Fatalf("seal design: %v", err)
	}
	return capability, analysis, design
}

func testStageDeclarations() []StageDeclaration {
	result := make([]StageDeclaration, len(closedStages))
	for index, stage := range closedStages {
		capability, _ := capabilityForStage(stage)
		role := MetricExploratory
		if stage == StageVerifierOutcome {
			role = MetricConfirmatory
		}
		result[index] = StageDeclaration{
			Stage: stage, Role: role, Capability: capability, FamilySHA256: testDigest("stage-family-" + string(stage)),
		}
	}
	return result
}

func testMatrixContracts(t *testing.T) (CapabilityContract, AnalysisPlan, Design) {
	t.Helper()
	capability, analysis, design := testContracts(t, 2)
	designInput := cloneDesign(design)
	designInput.DesignSHA256 = ""
	designInput.Treatments = testTreatmentMatrix(design.Case.CaseSHA256)
	cycle := uint32(len(designInput.Treatments) * 2) // odd Williams design includes each row and its reversal.
	designInput.Strata = []StratumRequest{{BindingSHA256: testDigest("matrix-stratum"), Blocks: cycle}}
	designInput.Stopping.MaximumBlocks = cycle
	design, err := SealDesign(designInput)
	if err != nil {
		t.Fatalf("seal matrix design: %v", err)
	}
	return capability, analysis, design
}

func testPairLimitContracts(t *testing.T) (CapabilityContract, AnalysisPlan, Design) {
	t.Helper()
	capability, _, _ := testContracts(t, 2)
	caseDigest := testDigest("pair-limit-case")
	conditions := []Condition{
		ConditionNone, ConditionCurrent, ConditionPrevious, ConditionForcedOracle,
		ConditionAutonomousOracle, ConditionOracleDistractors,
	}
	channels := []ActivationChannel{ChannelImplicit, ChannelExplicitUser, ChannelDeveloper, ChannelCombined, ChannelAdapterNative}
	treatments := make([]TreatmentRequest, 0, 24)
	for _, condition := range conditions {
		for _, channel := range channels {
			if len(treatments) == 24 {
				break
			}
			index := len(treatments)
			selection := SelectionAgent
			skill := testDigest("pair-limit-skill")
			version := ""
			distractors := []string{}
			switch condition {
			case ConditionNone:
				selection, skill = SelectionNone, ""
			case ConditionPrevious:
				version = testDigest("pair-limit-version")
			case ConditionForcedOracle:
				selection = SelectionHarness
			case ConditionOracleDistractors:
				distractors = []string{testDigest("pair-limit-distractor")}
			}
			role := RoleReference
			if index >= 12 {
				role = RoleCandidate
			}
			treatments = append(treatments, TreatmentRequest{
				Arm:  ArmSelector{Condition: condition, ActivationChannel: channel, SelectionAuthority: selection, Control: ControlPositive},
				Role: role, SkillSHA256: skill, SkillVersionSHA256: version, DistractorSHA256: distractors,
				ControlSHA256: caseDigest, ControlProvenance: ControlFromSource,
				ExecutionBindingSHA256: testDigest("pair-limit-binding-" + string(rune(index+1))),
				ExpectedActivation:     condition != ConditionNone,
			})
		}
	}
	comparisons := make([]Comparison, 0, MaxComparisons)
	for reference := 0; reference < 12 && len(comparisons) < MaxComparisons; reference++ {
		for candidate := 12; candidate < len(treatments) && len(comparisons) < MaxComparisons; candidate++ {
			comparisons = append(comparisons, Comparison{
				Reference: treatments[reference].Arm, Candidate: treatments[candidate].Arm,
				Stages: []FunnelStage{StageLoad}, Metrics: []MetricID{MetricOutcome},
			})
		}
	}
	analysis, err := SealAnalysisPlan(AnalysisPlan{
		ConfidenceBasisPoints: 9500, MinimumInferenceBlocks: 2, BootstrapSamples: 1000,
		BootstrapSeedSHA256: testDigest("pair-limit-bootstrap"), Multiplicity: MultiplicityHolm,
		RepeatedAttempts: RepeatedAttemptPolicy{Kind: RepeatedAttemptsNone, K: []uint32{1}},
		Stages:           testStageDeclarations(),
		Metrics: []MetricDeclaration{{
			ID: MetricOutcome, Kind: MetricBinary, Role: MetricPrimary, Direction: DirectionHigher,
			Capability: CapabilityObserveOutcome, FamilySHA256: testDigest("pair-limit-outcome"),
		}},
		Comparisons: comparisons, AllowedExclusions: []ExclusionReason{ExclusionLifecycleUnknown},
	})
	if err != nil {
		t.Fatalf("seal pair-limit analysis: %v", err)
	}
	design, err := SealDesign(Design{
		CompatibilityProfile: CompatibilityNone, CapabilityContractSHA256: capability.CapabilityContractSHA256,
		AnalysisPlanSHA256: analysis.AnalysisPlanSHA256,
		Case: CaseBinding{SourceKind: SourceNative, SourceSHA256: testDigest("pair-limit-source"), CaseSHA256: caseDigest,
			TaskSHA256: testDigest("pair-limit-task"), FixtureSHA256: testDigest("pair-limit-fixture"), GradingPlanSHA256: testDigest("pair-limit-grading")},
		Treatments: treatments,
		Strata:     []StratumRequest{{BindingSHA256: testDigest("pair-limit-stratum"), Blocks: 168}},
		Ordering:   OrderingPolicy{Kind: OrderingWilliams, SeedSHA256: testDigest("pair-limit-order"), LegacySequence: []ArmSelector{}},
		Stopping:   StoppingRule{Kind: StoppingFixedRoster, MaximumBlocks: 168, SafetyStops: []SafetyStopCode{}},
	})
	if err != nil {
		t.Fatalf("seal pair-limit design: %v", err)
	}
	return capability, analysis, design
}

func derivedManifestWithoutAdmission(t *testing.T, design Design, capability CapabilityContract, analysis AnalysisPlan) Manifest {
	t.Helper()
	manifest, err := deriveManifest(design, capability, analysis)
	if err != nil {
		t.Fatalf("derive manifest without admission: %v", err)
	}
	manifest.ManifestSHA256, err = digestManifest(manifest)
	if err != nil {
		t.Fatalf("digest manifest without admission: %v", err)
	}
	return manifest
}

func testTreatmentMatrix(caseDigest string) []TreatmentRequest {
	treatment := func(label string, arm ArmSelector, role TreatmentRole, skill, version, retriever string, distractors []string) TreatmentRequest {
		control := caseDigest
		provenance := ControlFromSource
		if arm.Control != ControlPositive {
			control = testDigest("control-" + label)
			provenance = ControlSeparatelyAuthored
		}
		return TreatmentRequest{
			Arm: arm, Role: role, SkillSHA256: skill, SkillVersionSHA256: version,
			DistractorSHA256: distractors, RetrieverSHA256: retriever,
			ControlSHA256: control, ControlProvenance: provenance,
			ExecutionBindingSHA256: testDigest("binding-" + label),
			ExpectedActivation:     arm.Control == ControlPositive && arm.Condition != ConditionNone && arm.Condition != ConditionRetrievedAbsent,
		}
	}
	current := testDigest("matrix-current-skill")
	previous := testDigest("matrix-previous-skill")
	return []TreatmentRequest{
		treatment("none", ArmSelector{ConditionNone, ChannelImplicit, SelectionNone, ControlPositive}, RoleReference, "", "", "", []string{}),
		treatment("current", ArmSelector{ConditionCurrent, ChannelImplicit, SelectionAgent, ControlPositive}, RoleCandidate, current, "", "", []string{}),
		treatment("previous", ArmSelector{ConditionPrevious, ChannelDeveloper, SelectionAgent, ControlPositive}, RoleCandidate, previous, testDigest("matrix-previous-version"), "", []string{}),
		treatment("forced", ArmSelector{ConditionForcedOracle, ChannelExplicitUser, SelectionHarness, ControlPositive}, RoleCandidate, current, "", "", []string{}),
		treatment("autonomous", ArmSelector{ConditionAutonomousOracle, ChannelAdapterNative, SelectionAgent, ControlPositive}, RoleCandidate, current, "", "", []string{}),
		treatment("oracle-distractors", ArmSelector{ConditionOracleDistractors, ChannelCombined, SelectionAgent, ControlPositive}, RoleCandidate, current, "", "", []string{testDigest("matrix-positive-distractor")}),
		treatment("retrieved-present", ArmSelector{ConditionRetrievedPresent, ChannelDeveloper, SelectionRetriever, ControlPositive}, RoleCandidate, current, "", testDigest("matrix-retriever"), []string{}),
		treatment("retrieved-absent", ArmSelector{ConditionRetrievedAbsent, ChannelCombined, SelectionRetriever, ControlPositive}, RoleCandidate, current, "", testDigest("matrix-retriever"), []string{}),
		treatment("near-miss", ArmSelector{ConditionCurrent, ChannelExplicitUser, SelectionAgent, ControlNearMissNegative}, RoleControl, current, "", "", []string{}),
		treatment("irrelevant", ArmSelector{ConditionCurrent, ChannelDeveloper, SelectionAgent, ControlIrrelevant}, RoleControl, current, "", "", []string{}),
		treatment("unsupported", ArmSelector{ConditionCurrent, ChannelCombined, SelectionAgent, ControlUnsupportedDomain}, RoleControl, current, "", "", []string{}),
		treatment("stale", ArmSelector{ConditionPrevious, ChannelImplicit, SelectionAgent, ControlStaleVersionMismatch}, RoleControl, previous, testDigest("matrix-stale-version"), "", []string{}),
		treatment("adversarial", ArmSelector{ConditionOracleDistractors, ChannelAdapterNative, SelectionAgent, ControlAdversarialDistractor}, RoleControl, current, "", "", []string{testDigest("matrix-adversarial-distractor")}),
	}
}

func findMatrixTreatment(t *testing.T, manifest Manifest, condition Condition, control ControlClass) Treatment {
	t.Helper()
	for _, treatment := range manifest.Treatments {
		if treatment.Arm.Condition == condition && treatment.Arm.Control == control {
			return treatment
		}
	}
	t.Fatalf("treatment %s/%s not found", condition, control)
	return Treatment{}
}

func sealMatrixTrial(t *testing.T, manifest Manifest, analysis AnalysisPlan, treatment Treatment, label string, failedStage FunnelStage, outcome uint64, falseLoad bool) TrialRecord {
	t.Helper()
	var assignment Assignment
	block := manifest.Blocks[0]
	for _, candidate := range block.Assignments {
		if candidate.TreatmentID == treatment.ID {
			assignment = candidate
			break
		}
	}
	if assignment.TrialID == "" {
		t.Fatalf("assignment for %s not found", treatment.ID)
	}
	stageValue := true
	stages := testStageObservations(&stageValue)
	for index := range stages {
		if stages[index].Stage == failedStage {
			value := false
			stages[index].Value = &value
		}
		if falseLoad && stages[index].Stage == StageLoad {
			value := true
			stages[index].Value = &value
		}
	}
	metricValue := uint64(1)
	metrics := testMetricObservations(analysis, &metricValue)
	for index := range metrics {
		if metrics[index].Metric == MetricOutcome {
			value := outcome
			metrics[index].Value = &value
		}
	}
	record, err := SealTrialRecord(manifest, TrialRecord{
		TrialID: assignment.TrialID, BlockID: block.ID, TreatmentID: treatment.ID,
		AttemptPlanSHA256: testDigest("attempt-" + label), LifecycleState: LifecycleSucceeded,
		Eligibility: EligibilitySupported, Exclusion: ExclusionNone,
		AgentObservationSHA256: testDigest("observation-" + label), GradeReceiptSHA256: testDigest("grade-" + label),
		LifecycleEventSHA256: testDigest("lifecycle-" + label), Stages: stages, Metrics: metrics,
	})
	if err != nil {
		t.Fatalf("seal %s trial: %v", label, err)
	}
	return record
}

func stageValue(t *testing.T, record TrialRecord, identifier FunnelStage) bool {
	t.Helper()
	for _, stage := range record.Stages {
		if stage.Stage == identifier && stage.Value != nil {
			return *stage.Value
		}
	}
	t.Fatalf("stage %s not found", identifier)
	return false
}

func testTreatments(caseDigest string, count int) []TreatmentRequest {
	all := []TreatmentRequest{
		{
			Arm:  ArmSelector{Condition: ConditionNone, ActivationChannel: ChannelImplicit, SelectionAuthority: SelectionNone, Control: ControlPositive},
			Role: RoleReference, DistractorSHA256: []string{}, ControlSHA256: caseDigest, ControlProvenance: ControlFromSource,
			ExecutionBindingSHA256: testDigest("none-binding"), ExpectedActivation: false,
		},
		{
			Arm:  ArmSelector{Condition: ConditionCurrent, ActivationChannel: ChannelImplicit, SelectionAuthority: SelectionAgent, Control: ControlPositive},
			Role: RoleCandidate, SkillSHA256: testDigest("current-skill"), DistractorSHA256: []string{}, ControlSHA256: caseDigest,
			ControlProvenance: ControlFromSource, ExecutionBindingSHA256: testDigest("current-binding"), ExpectedActivation: true,
		},
		{
			Arm:  ArmSelector{Condition: ConditionForcedOracle, ActivationChannel: ChannelExplicitUser, SelectionAuthority: SelectionHarness, Control: ControlPositive},
			Role: RoleCandidate, SkillSHA256: testDigest("forced-skill"), DistractorSHA256: []string{}, ControlSHA256: caseDigest,
			ControlProvenance: ControlFromSource, ExecutionBindingSHA256: testDigest("forced-binding"), ExpectedActivation: true,
		},
		{
			Arm:  ArmSelector{Condition: ConditionAutonomousOracle, ActivationChannel: ChannelAdapterNative, SelectionAuthority: SelectionAgent, Control: ControlPositive},
			Role: RoleCandidate, SkillSHA256: testDigest("autonomous-skill"), DistractorSHA256: []string{}, ControlSHA256: caseDigest,
			ControlProvenance: ControlFromSource, ExecutionBindingSHA256: testDigest("autonomous-binding"), ExpectedActivation: true,
		},
		{
			Arm:  ArmSelector{Condition: ConditionOracleDistractors, ActivationChannel: ChannelCombined, SelectionAuthority: SelectionAgent, Control: ControlAdversarialDistractor},
			Role: RoleControl, SkillSHA256: testDigest("distractor-skill"), DistractorSHA256: []string{testDigest("distractor")},
			ControlSHA256: testDigest("control"), ControlProvenance: ControlSeparatelyAuthored,
			ExecutionBindingSHA256: testDigest("distractor-binding"), ExpectedActivation: false,
		},
	}
	return append([]TreatmentRequest{}, all[:count]...)
}

func testStageObservations(value *bool) []StageObservation {
	result := make([]StageObservation, len(closedStages))
	for index, stage := range closedStages {
		copyValue := *value
		result[index] = StageObservation{Stage: stage, Presence: PresenceObserved, Value: &copyValue}
	}
	return result
}

func testMetricObservations(plan AnalysisPlan, value *uint64) []MetricObservation {
	result := make([]MetricObservation, len(plan.Metrics))
	for index, metric := range plan.Metrics {
		copyValue := *value
		if metric.Kind == MetricBinary && copyValue > 1 {
			copyValue = 1
		}
		result[index] = MetricObservation{Metric: metric.ID, Presence: PresenceObserved, Value: &copyValue}
	}
	return result
}

func metricValue(t *testing.T, record TrialRecord, identifier MetricID) uint64 {
	t.Helper()
	for _, metric := range record.Metrics {
		if metric.Metric == identifier && metric.Value != nil {
			return *metric.Value
		}
	}
	t.Fatalf("metric %s not found", identifier)
	return 0
}

func testDigest(label string) string { return hashParts("test", []byte(label)) }

func codeOf(t *testing.T, err error) ErrorCode {
	t.Helper()
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("error %v has no experiment code", err)
	}
	return code
}
