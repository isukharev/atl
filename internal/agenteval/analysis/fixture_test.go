package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func testManifest(t *testing.T, blocks, minimum, bootstrap uint32, k []uint32) experiment.Manifest {
	return testManifestWithStrata(t, minimum, bootstrap, k, []experiment.StratumRequest{{BindingSHA256: testDigest("stratum"), Blocks: blocks}})
}

func testManifestWithStrata(t *testing.T, minimum, bootstrap uint32, k []uint32, strata []experiment.StratumRequest) experiment.Manifest {
	t.Helper()
	strata = append([]experiment.StratumRequest{}, strata...)
	sort.Slice(strata, func(left, right int) bool { return strata[left].BindingSHA256 < strata[right].BindingSHA256 })
	totalBlocks := uint32(0)
	for _, stratum := range strata {
		totalBlocks += stratum.Blocks
	}
	capabilities := make([]experiment.Capability, 0, len(experiment.Capabilities()))
	for _, capability := range experiment.Capabilities() {
		capabilities = append(capabilities, experiment.Capability{ID: capability, Support: experiment.SupportSupported, BindingSHA256: testDigest("capability-" + string(capability))})
	}
	capability, err := experiment.SealCapabilityContract(experiment.CapabilityContract{
		Runtime: experiment.RuntimeBinding{
			AgentSHA256: testDigest("agent"), ModelSHA256: testDigest("model"), EnvironmentSHA256: testDigest("environment"),
			AdapterSHA256: testDigest("adapter"), ExecutionBackendSHA256: testDigest("execution"), GraderSHA256: testDigest("grader"),
			HarnessSHA256: testDigest("harness"), BudgetsSHA256: testDigest("budgets"), AuthoritySHA256: testDigest("authority"),
		},
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedFamily := testDigest("confirmatory-family")
	stages := make([]experiment.StageDeclaration, 0, len(closedStages))
	for _, stage := range closedStages {
		role, family := experiment.MetricExploratory, testDigest("stage-family-"+string(stage))
		if stage == experiment.StageLoad {
			role, family = experiment.MetricConfirmatory, sharedFamily
		}
		stages = append(stages, experiment.StageDeclaration{Stage: stage, Role: role, Capability: stageCapability(stage), FamilySHA256: family})
	}
	reference := experiment.ArmSelector{Condition: experiment.ConditionNone, ActivationChannel: experiment.ChannelImplicit, SelectionAuthority: experiment.SelectionNone, Control: experiment.ControlPositive}
	candidate := experiment.ArmSelector{Condition: experiment.ConditionCurrent, ActivationChannel: experiment.ChannelImplicit, SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive}
	analysisPlan, err := experiment.SealAnalysisPlan(experiment.AnalysisPlan{
		ConfidenceBasisPoints: 9500, MinimumInferenceBlocks: minimum, BootstrapSamples: bootstrap,
		BootstrapSeedSHA256: testDigest("bootstrap"), Multiplicity: experiment.MultiplicityHolm,
		RepeatedAttempts: experiment.RepeatedAttemptPolicy{Kind: experiment.RepeatedAttemptsAll, K: append([]uint32{}, k...)},
		Stages:           stages,
		Metrics: []experiment.MetricDeclaration{
			{ID: experiment.MetricDurationMillis, Kind: experiment.MetricCount, Role: experiment.MetricExploratory, Direction: experiment.DirectionLower, Capability: experiment.CapabilityObserveDuration, FamilySHA256: testDigest("duration-family")},
			{ID: experiment.MetricOutcome, Kind: experiment.MetricBinary, Role: experiment.MetricPrimary, Direction: experiment.DirectionHigher, Capability: experiment.CapabilityObserveOutcome, FamilySHA256: sharedFamily},
		},
		Comparisons: []experiment.Comparison{{Reference: reference, Candidate: candidate, Stages: []experiment.FunnelStage{experiment.StageLoad}, Metrics: []experiment.MetricID{experiment.MetricDurationMillis, experiment.MetricOutcome}}},
		AllowedExclusions: []experiment.ExclusionReason{
			experiment.ExclusionCoverageMismatch, experiment.ExclusionDrift, experiment.ExclusionDuplicateMember,
			experiment.ExclusionGradeIncomplete, experiment.ExclusionIneligible, experiment.ExclusionLifecycleIncomplete,
			experiment.ExclusionLifecycleUnknown, experiment.ExclusionMissingMember, experiment.ExclusionUnsupportedCapability,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	caseSHA := testDigest("case")
	design, err := experiment.SealDesign(experiment.Design{
		CompatibilityProfile:     experiment.CompatibilityNone,
		CapabilityContractSHA256: capability.CapabilityContractSHA256, AnalysisPlanSHA256: analysisPlan.AnalysisPlanSHA256,
		Case: experiment.CaseBinding{SourceKind: experiment.SourceAgentSkills, SourceSHA256: testDigest("source"), CaseSHA256: caseSHA,
			TaskSHA256: testDigest("task"), FixtureSHA256: testDigest("fixture"), GradingPlanSHA256: testDigest("grading")},
		Treatments: []experiment.TreatmentRequest{
			{Arm: reference, Role: experiment.RoleReference, DistractorSHA256: []string{}, ControlSHA256: caseSHA, ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: testDigest("reference-binding")},
			{Arm: candidate, Role: experiment.RoleCandidate, SkillSHA256: testDigest("skill"), DistractorSHA256: []string{}, ControlSHA256: caseSHA, ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: testDigest("candidate-binding"), ExpectedActivation: true},
		},
		Strata:   strata,
		Ordering: experiment.OrderingPolicy{Kind: experiment.OrderingWilliams, SeedSHA256: testDigest("ordering"), LegacySequence: []experiment.ArmSelector{}},
		Stopping: experiment.StoppingRule{Kind: experiment.StoppingFixedRoster, MaximumBlocks: totalBlocks, SafetyStops: []experiment.SafetyStopCode{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := experiment.Compile(design, capability, analysisPlan)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func testRecords(t *testing.T, manifest experiment.Manifest) []experiment.TrialRecord {
	t.Helper()
	referenceOutcomes := []uint64{0, 1, 0, 1, 0, 0}
	candidateOutcomes := []uint64{0, 0, 1, 1, 1, 1}
	durationDeltas := []int64{10, -5, 0, 20, -10, 5}
	records := make([]experiment.TrialRecord, 0, len(manifest.Blocks)*len(manifest.Treatments))
	roles := map[string]experiment.TreatmentRole{}
	for _, treatment := range manifest.Treatments {
		roles[treatment.ID] = treatment.Role
	}
	for blockIndex, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			role := roles[assignment.TreatmentID]
			outcome := referenceOutcomes[blockIndex%len(referenceOutcomes)]
			duration := int64(100)
			load := false
			if role == experiment.RoleCandidate {
				outcome = candidateOutcomes[blockIndex%len(candidateOutcomes)]
				duration += durationDeltas[blockIndex%len(durationDeltas)]
				load = true
			}
			stages := make([]experiment.StageObservation, 0, len(closedStages))
			for _, stage := range closedStages {
				value := load
				if stage == experiment.StageVerifierOutcome {
					value = outcome == 1
				}
				stages = append(stages, experiment.StageObservation{Stage: stage, Presence: experiment.PresenceObserved, Value: boolPointer(value)})
			}
			metrics := make([]experiment.MetricObservation, 0, len(manifest.AnalysisPlan.Metrics))
			for _, declaration := range manifest.AnalysisPlan.Metrics {
				value := outcome
				if declaration.ID == experiment.MetricDurationMillis {
					value = uint64(duration)
				}
				metrics = append(metrics, experiment.MetricObservation{Metric: declaration.ID, Presence: experiment.PresenceObserved, Value: uint64Pointer(value)})
			}
			state := experiment.LifecycleFailed
			if outcome == 1 {
				state = experiment.LifecycleSucceeded
			}
			record, err := experiment.SealTrialRecord(manifest, experiment.TrialRecord{
				TrialID: assignment.TrialID, BlockID: block.ID, TreatmentID: assignment.TreatmentID,
				AttemptPlanSHA256: testDigest("attempt-" + assignment.TrialID), LifecycleState: state,
				Eligibility: experiment.EligibilitySupported, Exclusion: experiment.ExclusionNone,
				AgentObservationSHA256: testDigest("observation-" + assignment.TrialID), GradeReceiptSHA256: testDigest("grade-" + assignment.TrialID),
				LifecycleEventSHA256: testDigest("lifecycle-" + assignment.TrialID), Stages: stages, Metrics: metrics,
			})
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
	}
	return records
}

func testManifestWithPlan(t *testing.T, manifest experiment.Manifest, mutate func(*experiment.AnalysisPlan)) experiment.Manifest {
	t.Helper()
	plan := manifest.AnalysisPlan
	plan.AnalysisPlanSHA256 = ""
	mutate(&plan)
	sealedPlan, err := experiment.SealAnalysisPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	design := manifest.Design
	design.DesignSHA256 = ""
	design.AnalysisPlanSHA256 = sealedPlan.AnalysisPlanSHA256
	sealedDesign, err := experiment.SealDesign(design)
	if err != nil {
		t.Fatal(err)
	}
	result, err := experiment.Compile(sealedDesign, manifest.CapabilityContract, sealedPlan)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testThreeTreatmentManifest(t *testing.T, comparisons func(reference, current, previous experiment.ArmSelector) []experiment.Comparison) experiment.Manifest {
	t.Helper()
	base := testManifest(t, 6, 2, 100, []uint32{1})
	reference, current := experiment.ArmSelector{}, experiment.ArmSelector{}
	for _, treatment := range base.Design.Treatments {
		switch treatment.Arm.Condition {
		case experiment.ConditionNone:
			reference = treatment.Arm
		case experiment.ConditionCurrent:
			current = treatment.Arm
		}
	}
	previous := experiment.ArmSelector{
		Condition: experiment.ConditionPrevious, ActivationChannel: experiment.ChannelDeveloper,
		SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive,
	}
	plan := base.AnalysisPlan
	plan.AnalysisPlanSHA256 = ""
	plan.Comparisons = comparisons(reference, current, previous)
	sealedPlan, err := experiment.SealAnalysisPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	design := base.Design
	design.DesignSHA256 = ""
	design.AnalysisPlanSHA256 = sealedPlan.AnalysisPlanSHA256
	design.Treatments = append(design.Treatments, experiment.TreatmentRequest{
		Arm: previous, Role: experiment.RoleCandidate, SkillSHA256: testDigest("previous-skill"),
		SkillVersionSHA256: testDigest("previous-version"), DistractorSHA256: []string{},
		ControlSHA256: design.Case.CaseSHA256, ControlProvenance: experiment.ControlFromSource,
		ExecutionBindingSHA256: testDigest("previous-binding"), ExpectedActivation: true,
	})
	sealedDesign, err := experiment.SealDesign(design)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := experiment.Compile(sealedDesign, base.CapabilityContract, sealedPlan)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func testFourTreatmentManifest(t *testing.T, comparisons func(firstReference, secondReference, current, previous experiment.ArmSelector) []experiment.Comparison) experiment.Manifest {
	t.Helper()
	base := testManifest(t, 8, 2, 100, []uint32{1})
	firstReference, current := experiment.ArmSelector{}, experiment.ArmSelector{}
	for _, treatment := range base.Design.Treatments {
		switch treatment.Arm.Condition {
		case experiment.ConditionNone:
			firstReference = treatment.Arm
		case experiment.ConditionCurrent:
			current = treatment.Arm
		}
	}
	secondReference := experiment.ArmSelector{
		Condition: experiment.ConditionNone, ActivationChannel: experiment.ChannelDeveloper,
		SelectionAuthority: experiment.SelectionNone, Control: experiment.ControlPositive,
	}
	previous := experiment.ArmSelector{
		Condition: experiment.ConditionPrevious, ActivationChannel: experiment.ChannelDeveloper,
		SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive,
	}
	plan := base.AnalysisPlan
	plan.AnalysisPlanSHA256 = ""
	plan.Comparisons = comparisons(firstReference, secondReference, current, previous)
	sealedPlan, err := experiment.SealAnalysisPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	design := base.Design
	design.DesignSHA256 = ""
	design.AnalysisPlanSHA256 = sealedPlan.AnalysisPlanSHA256
	design.Treatments = append(design.Treatments,
		experiment.TreatmentRequest{
			Arm: secondReference, Role: experiment.RoleReference, DistractorSHA256: []string{},
			ControlSHA256: design.Case.CaseSHA256, ControlProvenance: experiment.ControlFromSource,
			ExecutionBindingSHA256: testDigest("second-reference-binding"), ExpectedActivation: false,
		},
		experiment.TreatmentRequest{
			Arm: previous, Role: experiment.RoleCandidate, SkillSHA256: testDigest("previous-skill"),
			SkillVersionSHA256: testDigest("previous-version"), DistractorSHA256: []string{},
			ControlSHA256: design.Case.CaseSHA256, ControlProvenance: experiment.ControlFromSource,
			ExecutionBindingSHA256: testDigest("previous-binding"), ExpectedActivation: true,
		},
	)
	sealedDesign, err := experiment.SealDesign(design)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := experiment.Compile(sealedDesign, base.CapabilityContract, sealedPlan)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func treatmentByRole(t *testing.T, manifest experiment.Manifest, role experiment.TreatmentRole) experiment.Treatment {
	t.Helper()
	for _, treatment := range manifest.Treatments {
		if treatment.Role == role {
			return treatment
		}
	}
	t.Fatalf("missing treatment role %s", role)
	return experiment.Treatment{}
}

func stageCapability(stage experiment.FunnelStage) experiment.CapabilityID {
	return map[experiment.FunnelStage]experiment.CapabilityID{
		experiment.StageCandidateRecall:   experiment.CapabilityObserveCandidateRecall,
		experiment.StageSelection:         experiment.CapabilityObserveSelection,
		experiment.StageLoad:              experiment.CapabilityObserveLoad,
		experiment.StageInstructionAccess: experiment.CapabilityObserveInstructionAccess,
		experiment.StageReferenceAccess:   experiment.CapabilityObserveReferenceAccess,
		experiment.StageScriptAccess:      experiment.CapabilityObserveScriptAccess,
		experiment.StageUsefulAdherence:   experiment.CapabilityObserveUsefulAdherence,
		experiment.StageVerifierOutcome:   experiment.CapabilityObserveVerifierOutcome,
	}[stage]
}

func testDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func boolPointer(value bool) *bool       { return &value }
func uint64Pointer(value uint64) *uint64 { return &value }
