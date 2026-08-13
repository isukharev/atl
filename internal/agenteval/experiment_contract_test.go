package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestExperimentFacadeBindsAgentSkillsAndFullLifecycleRoster(t *testing.T) {
	projection, err := ProjectAgentSkillsExperiment(AgentSkillsImportOptions{
		SkillRoot: filepath.Join("interchange", "agentskills", "testdata", "guide-v1", "skill"),
		Format:    "agentskills-guide-v1",
		Baseline:  "no-skill",
	})
	if err != nil {
		t.Fatalf("project Agent Skills experiment: %v", err)
	}
	if len(projection.Cases) != 2 || projection.Cases[0].Case.SourceKind != experiment.SourceAgentSkills ||
		projection.Cases[0].CurrentSkillSHA256 == "" || projection.Cases[0].PreviousSkillSHA256 != "" {
		t.Fatalf("projection = %+v", projection)
	}
	encodedProjection, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"Summarize the rows", "CSV", "hostile.sh"} {
		if bytes.Contains(encodedProjection, []byte(content)) {
			t.Fatalf("content-bearing Agent Skills value escaped projection: %q", content)
		}
	}
	previousProjection, err := ProjectAgentSkillsExperiment(AgentSkillsImportOptions{
		SkillRoot:         filepath.Join("interchange", "agentskills", "testdata", "anthropic-v1", "skill"),
		PreviousSkillRoot: filepath.Join("interchange", "agentskills", "testdata", "anthropic-v1", "previous"),
		Format:            "anthropic-skill-creator-v1",
		Baseline:          "previous-skill",
	})
	if err != nil || len(previousProjection.Cases) == 0 || previousProjection.Cases[0].PreviousSkillSHA256 == "" ||
		previousProjection.Cases[0].PreviousSkillSHA256 == previousProjection.Cases[0].CurrentSkillSHA256 {
		t.Fatalf("previous-skill projection=%+v err=%v", previousProjection, err)
	}

	capability, analysis, design := rootExperimentContracts(t, projection.Cases[0].Case, projection.Cases[0].CurrentSkillSHA256)
	manifest, err := CompileExperiment(design, capability, analysis)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	bindings, err := ExperimentAttemptBindings(manifest)
	if err != nil || len(bindings) != len(manifest.Blocks)*len(manifest.Treatments) {
		t.Fatalf("bindings=%d err=%v", len(bindings), err)
	}
	for index, binding := range bindings {
		if binding.Privacy != lifecycle.PrivacyContentMinimized || binding.Identity.ExperimentSHA256 == manifest.ManifestSHA256 {
			t.Fatalf("binding[%d]=%+v", index, binding)
		}
	}
	store := newAttemptLedgerForTest(t)
	first, err := EnsureExperimentRoster(store, manifest)
	if err != nil {
		t.Fatalf("ensure roster: %v", err)
	}
	again, err := EnsureExperimentRoster(store, manifest)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("roster was not idempotent: err=%v", err)
	}
	changed := manifest
	changed.ManifestSHA256 = strings.Repeat("f", 64)
	if _, err := EnsureExperimentRoster(store, changed); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestExperimentRosterRefusesPreregisteredPolicyMutationAfterCommit(t *testing.T) {
	caseBinding := experiment.CaseBinding{
		SourceKind: experiment.SourceNative, SourceSHA256: rootExperimentDigest("roster-source"),
		CaseSHA256: rootExperimentDigest("roster-case"), TaskSHA256: rootExperimentDigest("roster-task"),
		FixtureSHA256: rootExperimentDigest("roster-fixture"), GradingPlanSHA256: rootExperimentDigest("roster-grading"),
	}
	capability, analysis, design := rootExperimentContracts(t, caseBinding, rootExperimentDigest("roster-skill"))
	original, err := experiment.Compile(design, capability, analysis)
	if err != nil {
		t.Fatal(err)
	}

	compileWith := func(t *testing.T, changedAnalysis experiment.AnalysisPlan, changedDesign experiment.Design) experiment.Manifest {
		t.Helper()
		manifest, compileErr := experiment.Compile(changedDesign, capability, changedAnalysis)
		if compileErr != nil {
			t.Fatalf("compile mutation: %v", compileErr)
		}
		if manifest.ManifestSHA256 == original.ManifestSHA256 {
			t.Fatal("preregistered policy mutation retained manifest identity")
		}
		return manifest
	}

	repeated := analysis
	repeated.AnalysisPlanSHA256 = ""
	repeated.RepeatedAttempts = experiment.RepeatedAttemptPolicy{Kind: experiment.RepeatedAttemptsAll, K: []uint32{1, 2}}
	repeated, err = experiment.SealAnalysisPlan(repeated)
	if err != nil {
		t.Fatal(err)
	}
	repeatedDesign := design
	repeatedDesign.DesignSHA256 = ""
	repeatedDesign.AnalysisPlanSHA256 = repeated.AnalysisPlanSHA256
	repeatedDesign, err = experiment.SealDesign(repeatedDesign)
	if err != nil {
		t.Fatal(err)
	}

	exclusions := analysis
	exclusions.AnalysisPlanSHA256 = ""
	exclusions.AllowedExclusions = append([]experiment.ExclusionReason{}, analysis.AllowedExclusions...)
	exclusions.AllowedExclusions = append(exclusions.AllowedExclusions, experiment.ExclusionCoverageMismatch)
	exclusions, err = experiment.SealAnalysisPlan(exclusions)
	if err != nil {
		t.Fatal(err)
	}
	exclusionsDesign := design
	exclusionsDesign.DesignSHA256 = ""
	exclusionsDesign.AnalysisPlanSHA256 = exclusions.AnalysisPlanSHA256
	exclusionsDesign, err = experiment.SealDesign(exclusionsDesign)
	if err != nil {
		t.Fatal(err)
	}

	ordering := design
	ordering.DesignSHA256 = ""
	ordering.Ordering.SeedSHA256 = rootExperimentDigest("changed-order")
	ordering, err = experiment.SealDesign(ordering)
	if err != nil {
		t.Fatal(err)
	}

	stopping := design
	stopping.DesignSHA256 = ""
	stopping.Strata = []experiment.StratumRequest{{BindingSHA256: design.Strata[0].BindingSHA256, Blocks: 4}}
	stopping.Stopping.MaximumBlocks = 4
	stopping, err = experiment.SealDesign(stopping)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string]experiment.Manifest{
		"repetition": compileWith(t, repeated, repeatedDesign),
		"exclusions": compileWith(t, exclusions, exclusionsDesign),
		"ordering":   compileWith(t, analysis, ordering),
		"stopping":   compileWith(t, analysis, stopping),
	}
	for name, mutated := range mutations {
		t.Run(name, func(t *testing.T) {
			store := newAttemptLedgerForTest(t)
			plans, planErr := EnsureExperimentRoster(store, original)
			if planErr != nil || len(plans) == 0 {
				t.Fatalf("ensure original roster: plans=%d err=%v", len(plans), planErr)
			}
			appendAttemptEventForTest(t, store, plans[0], lifecycle.StateCommitted,
				[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidence(lifecycle.ErrorNone))
			if _, rosterErr := EnsureExperimentRoster(store, mutated); !errors.Is(rosterErr, ErrAttemptLedgerConflict) {
				t.Fatalf("committed roster accepted %s mutation: %v", name, rosterErr)
			}
		})
	}
}

func TestPrivateActivationStudiesProjectToContentMinimizedComparableManifests(t *testing.T) {
	contract, err := BuildPrivateActivationStudyContract(privateActivationStudyTestSpecs())
	if err != nil {
		t.Fatal(err)
	}
	caseBinding := experiment.CaseBinding{
		SourceKind: experiment.SourceNative, SourceSHA256: rootExperimentDigest("private-source"),
		CaseSHA256: rootExperimentDigest("private-case"), TaskSHA256: rootExperimentDigest("private-task"),
		FixtureSHA256: rootExperimentDigest("private-fixture"), GradingPlanSHA256: rootExperimentDigest("private-grading"),
	}
	capability, analysis, _ := rootExperimentContracts(t, caseBinding, rootExperimentDigest("private-skill"))
	analysis.AnalysisPlanSHA256 = ""
	analysis.Comparisons = []experiment.Comparison{{
		Reference: experiment.ArmSelector{Condition: experiment.ConditionCurrent, ActivationChannel: experiment.ChannelImplicit, SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive},
		Candidate: experiment.ArmSelector{Condition: experiment.ConditionCurrent, ActivationChannel: experiment.ChannelExplicitUser, SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive},
		Stages:    rootExperimentFunnelStages(),
		Metrics:   []experiment.MetricID{experiment.MetricDurationMillis, experiment.MetricOutcome},
	}}
	analysis, err = experiment.SealAnalysisPlan(analysis)
	if err != nil {
		t.Fatal(err)
	}
	cells := make([]PrivateActivationStudyCell, 0, 16)
	for block, order := range CanonicalPrivateActivationStudyOrders() {
		for position, cell := range order {
			treatment, ok := contract.Treatment(cell.SkillActivation)
			if !ok {
				t.Fatal("missing treatment")
			}
			cells = append(cells, PrivateActivationStudyCell{
				CellID: "cell-" + string(rune('a'+block*4+position)), SkillActivation: cell.SkillActivation,
				ContractSHA256: treatment.RunSpecSHA256, MaxEstimatedCostMicroUSD: 1,
			})
		}
	}
	current, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "synthetic-study", TotalAuthorizedMicroUSD: 18, ReviewerReserveMicroUSD: 1,
		Calibration:           PrivateActivationCalibrationContract{ContractSHA256: rootExperimentDigest("calibration"), MaxEstimatedCostMicroUSD: 1},
		OrderedBalancedRoster: cells,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := current
	legacy.SchemaVersion = legacyPrivateActivationStudyPlanSchemaVersion
	legacy.Calibration = PrivateActivationCalibrationContract{}
	legacy.Cost.CalibrationAllocatedMicroUSD = 0
	legacy.Cost.TotalAuthorizedMicroUSD = 17

	compile := func(plan PrivateActivationStudyPlan) experiment.Manifest {
		manifest, err := CompilePrivateActivationExperiment(PrivateActivationExperimentInput{
			Plan: plan, ActivationContract: contract, CapabilityContract: capability, AnalysisPlan: analysis,
			Case: caseBinding, SkillSHA256: rootExperimentDigest("private-skill"),
		})
		if err != nil {
			t.Fatalf("compile private compatibility: %v", err)
		}
		return manifest
	}
	currentManifest := compile(current)
	legacyManifest := compile(legacy)
	if currentManifest.Design.CompatibilityProfile != experiment.CompatibilityPrivateActivationV2 ||
		legacyManifest.Design.CompatibilityProfile != experiment.CompatibilityPrivateActivationV1 ||
		len(currentManifest.Treatments) != 4 || !currentManifest.PositionBalanceComplete {
		t.Fatalf("current=%+v legacy=%+v", currentManifest.Design, legacyManifest.Design)
	}
	for index := range currentManifest.Treatments {
		if currentManifest.Treatments[index].ID != legacyManifest.Treatments[index].ID {
			t.Fatal("schema generation changed comparable treatment identity")
		}
	}
	encoded, err := experiment.EncodeManifest(currentManifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range privateActivationStudyTestSpecs() {
		if bytes.Contains(encoded, []byte(spec.PromptFile)) || bytes.Contains(encoded, []byte(spec.ScenarioFile)) {
			t.Fatal("private source path escaped compatibility manifest")
		}
	}
}

func rootExperimentContracts(t *testing.T, caseBinding experiment.CaseBinding, skillSHA256 string) (experiment.CapabilityContract, experiment.AnalysisPlan, experiment.Design) {
	t.Helper()
	capabilities := make([]experiment.Capability, len(rootExperimentCapabilities()))
	for index, identifier := range rootExperimentCapabilities() {
		capabilities[index] = experiment.Capability{ID: identifier, Support: experiment.SupportSupported, BindingSHA256: rootExperimentDigest(string(identifier))}
	}
	capability, err := experiment.SealCapabilityContract(experiment.CapabilityContract{
		Runtime: experiment.RuntimeBinding{
			AgentSHA256: rootExperimentDigest("agent"), ModelSHA256: rootExperimentDigest("model"),
			EnvironmentSHA256: rootExperimentDigest("environment"), AdapterSHA256: rootExperimentDigest("adapter"),
			ExecutionBackendSHA256: rootExperimentDigest("execution-backend"),
			GraderSHA256:           rootExperimentDigest("grader"), HarnessSHA256: rootExperimentDigest("harness"),
			BudgetsSHA256: rootExperimentDigest("budgets"), AuthoritySHA256: rootExperimentDigest("authority"),
		},
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := experiment.ArmSelector{Condition: experiment.ConditionNone, ActivationChannel: experiment.ChannelImplicit, SelectionAuthority: experiment.SelectionNone, Control: experiment.ControlPositive}
	candidate := experiment.ArmSelector{Condition: experiment.ConditionCurrent, ActivationChannel: experiment.ChannelImplicit, SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive}
	analysis, err := experiment.SealAnalysisPlan(experiment.AnalysisPlan{
		ConfidenceBasisPoints: 9500, MinimumInferenceBlocks: 2, BootstrapSamples: 1000,
		BootstrapSeedSHA256: rootExperimentDigest("bootstrap"), Multiplicity: experiment.MultiplicityHolm,
		RepeatedAttempts: experiment.RepeatedAttemptPolicy{Kind: experiment.RepeatedAttemptsNone, K: []uint32{1}},
		Stages:           rootExperimentStageDeclarations(),
		Metrics: []experiment.MetricDeclaration{
			{ID: experiment.MetricOutcome, Kind: experiment.MetricBinary, Role: experiment.MetricPrimary, Direction: experiment.DirectionHigher, Capability: experiment.CapabilityObserveOutcome, FamilySHA256: rootExperimentDigest("outcome")},
			{ID: experiment.MetricDurationMillis, Kind: experiment.MetricCount, Role: experiment.MetricExploratory, Direction: experiment.DirectionLower, Capability: experiment.CapabilityObserveDuration, FamilySHA256: rootExperimentDigest("duration")},
		},
		Comparisons:       []experiment.Comparison{{Reference: reference, Candidate: candidate, Stages: rootExperimentFunnelStages(), Metrics: []experiment.MetricID{experiment.MetricDurationMillis, experiment.MetricOutcome}}},
		AllowedExclusions: []experiment.ExclusionReason{experiment.ExclusionDrift, experiment.ExclusionLifecycleIncomplete, experiment.ExclusionLifecycleUnknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	design, err := experiment.SealDesign(experiment.Design{
		CompatibilityProfile:     experiment.CompatibilityNone,
		CapabilityContractSHA256: capability.CapabilityContractSHA256, AnalysisPlanSHA256: analysis.AnalysisPlanSHA256,
		Case: caseBinding,
		Treatments: []experiment.TreatmentRequest{
			{Arm: reference, Role: experiment.RoleReference, DistractorSHA256: []string{}, ControlSHA256: caseBinding.CaseSHA256, ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: rootExperimentDigest("reference-binding")},
			{Arm: candidate, Role: experiment.RoleCandidate, SkillSHA256: skillSHA256, DistractorSHA256: []string{}, ControlSHA256: caseBinding.CaseSHA256, ControlProvenance: experiment.ControlFromSource, ExecutionBindingSHA256: rootExperimentDigest("candidate-binding"), ExpectedActivation: true},
		},
		Strata:   []experiment.StratumRequest{{BindingSHA256: rootExperimentDigest("stratum"), Blocks: 2}},
		Ordering: experiment.OrderingPolicy{Kind: experiment.OrderingWilliams, SeedSHA256: rootExperimentDigest("order"), LegacySequence: []experiment.ArmSelector{}},
		Stopping: experiment.StoppingRule{Kind: experiment.StoppingFixedRoster, MaximumBlocks: 2, SafetyStops: []experiment.SafetyStopCode{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return capability, analysis, design
}

func rootExperimentFunnelStages() []experiment.FunnelStage {
	return []experiment.FunnelStage{
		experiment.StageCandidateRecall, experiment.StageSelection, experiment.StageLoad,
		experiment.StageInstructionAccess, experiment.StageReferenceAccess, experiment.StageScriptAccess,
		experiment.StageUsefulAdherence, experiment.StageVerifierOutcome,
	}
}

func rootExperimentStageDeclarations() []experiment.StageDeclaration {
	capabilities := []experiment.CapabilityID{
		experiment.CapabilityObserveCandidateRecall, experiment.CapabilityObserveSelection, experiment.CapabilityObserveLoad,
		experiment.CapabilityObserveInstructionAccess, experiment.CapabilityObserveReferenceAccess, experiment.CapabilityObserveScriptAccess,
		experiment.CapabilityObserveUsefulAdherence, experiment.CapabilityObserveVerifierOutcome,
	}
	stages := rootExperimentFunnelStages()
	result := make([]experiment.StageDeclaration, len(stages))
	for index, stage := range stages {
		role := experiment.MetricExploratory
		if stage == experiment.StageVerifierOutcome {
			role = experiment.MetricConfirmatory
		}
		result[index] = experiment.StageDeclaration{
			Stage: stage, Role: role, Capability: capabilities[index], FamilySHA256: rootExperimentDigest("stage-family-" + string(stage)),
		}
	}
	return result
}

func rootExperimentCapabilities() []experiment.CapabilityID {
	return []experiment.CapabilityID{
		experiment.CapabilityChannelAdapterNative, experiment.CapabilityChannelCombined, experiment.CapabilityChannelDeveloper,
		experiment.CapabilityChannelExplicitUser, experiment.CapabilityChannelImplicit,
		experiment.CapabilityConditionAutonomousOracle, experiment.CapabilityConditionCurrent, experiment.CapabilityConditionForcedOracle,
		experiment.CapabilityConditionNone, experiment.CapabilityConditionOracleDistractors, experiment.CapabilityConditionPrevious,
		experiment.CapabilityConditionRetrievedAbsent, experiment.CapabilityConditionRetrievedPresent,
		experiment.CapabilityControlAdversarialDistractor, experiment.CapabilityControlIrrelevant,
		experiment.CapabilityControlNearMissNegative, experiment.CapabilityControlPositive,
		experiment.CapabilityControlStaleVersionMismatch, experiment.CapabilityControlUnsupportedDomain,
		experiment.CapabilityObserveCandidateRecall, experiment.CapabilityObserveDuration, experiment.CapabilityObserveCost,
		experiment.CapabilityObserveInputTokens, experiment.CapabilityObserveInstructionAccess, experiment.CapabilityObserveLoad,
		experiment.CapabilityObserveOutcome, experiment.CapabilityObserveOutputTokens, experiment.CapabilityObserveReferenceAccess,
		experiment.CapabilityObserveScriptAccess, experiment.CapabilityObserveSelection, experiment.CapabilityObserveUsefulAdherence,
		experiment.CapabilityObserveVerifierOutcome,
	}
}

func rootExperimentDigest(label string) string {
	digest, err := contentMinimizedAttemptDigest("experiment-test", label)
	if err != nil {
		panic(err)
	}
	return digest
}
