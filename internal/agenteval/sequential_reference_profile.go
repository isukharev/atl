package agenteval

import (
	"encoding/json"
	"slices"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
)

// SequentialReferenceExperimentCapabilityContract returns the exact closed
// capability/runtime contract accepted by this provider-free composition.
func SequentialReferenceExperimentCapabilityContract() (ExperimentCapabilityContract, error) {
	adapter, err := agentadapter.ReferenceContract()
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("adapter_contract", err)
	}
	adapterSHA, err := agentadapter.ContractSHA256(adapter)
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("adapter_identity", err)
	}
	backend, err := executionbackend.ReferenceContract()
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("execution_contract", err)
	}
	backendSHA, err := executionbackend.ContractSHA256(backend)
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("execution_identity", err)
	}
	grader, err := grading.BuiltinContract()
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("grader_contract", err)
	}
	graderSHA, err := grading.ContractSHA256(grader)
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("grader_identity", err)
	}
	modelSHA, err := contentMinimizedAttemptDigest("sequential-reference-model", "not_applicable")
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("model_identity", err)
	}
	harnessSHA, err := contentMinimizedAttemptDigest("sequential-reference-harness", "closed-sequential-v1")
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("harness_identity", err)
	}
	budgetsSHA, err := contentMinimizedAttemptDigest("sequential-reference-budgets", "per-treatment-plan-v1")
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("budgets_identity", err)
	}
	authoritySHA, err := contentMinimizedAttemptDigest("sequential-reference-authority", struct {
		Process           bool `json:"process"`
		Provider          bool `json:"provider"`
		ConfiguredBackend bool `json:"configured_backend"`
		Network           bool `json:"network"`
		Credentials       bool `json:"credentials"`
		PrivateWorkspace  bool `json:"private_workspace"`
	}{})
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("authority_identity", err)
	}
	runtime := experiment.RuntimeBinding{
		AgentSHA256: adapter.ExecutableSHA256, ModelSHA256: modelSHA, EnvironmentSHA256: backendSHA,
		AdapterSHA256: adapterSHA, ExecutionBackendSHA256: backendSHA, GraderSHA256: graderSHA,
		HarnessSHA256: harnessSHA, BudgetsSHA256: budgetsSHA, AuthoritySHA256: authoritySHA,
	}
	supported := sequentialReferenceCapabilities()
	capabilities := make([]experiment.Capability, len(experiment.Capabilities()))
	for index, identifier := range experiment.Capabilities() {
		capabilities[index] = experiment.Capability{ID: identifier, Support: experiment.SupportUnsupported}
		if !supported[identifier] {
			continue
		}
		binding, digestErr := contentMinimizedAttemptDigest("sequential-reference-capability", struct {
			ID      experiment.CapabilityID   `json:"id"`
			Runtime experiment.RuntimeBinding `json:"runtime"`
		}{ID: identifier, Runtime: runtime})
		if digestErr != nil {
			return ExperimentCapabilityContract{}, sequentialReferenceError("capability_identity", digestErr)
		}
		capabilities[index].Support = experiment.SupportSupported
		capabilities[index].BindingSHA256 = binding
	}
	contract, err := experiment.SealCapabilityContract(experiment.CapabilityContract{Runtime: runtime, Capabilities: capabilities})
	if err != nil {
		return ExperimentCapabilityContract{}, sequentialReferenceError("capability_contract", err)
	}
	return contract, nil
}

// NewSequentialReferenceGradingPlan creates the one deterministic plan used by
// this composition. Its public evidence is the canonical execution receipt;
// the check observes the closed verdict member rather than raw output bytes.
func NewSequentialReferenceGradingPlan(inputProjectionSHA256 string) (GradingPlan, error) {
	contract, err := grading.BuiltinContract()
	if err != nil {
		return GradingPlan{}, sequentialReferenceError("grader_contract", err)
	}
	contractSHA, err := grading.ContractSHA256(contract)
	if err != nil {
		return GradingPlan{}, sequentialReferenceError("grader_identity", err)
	}
	backend, err := executionbackend.ReferenceContract()
	if err != nil {
		return GradingPlan{}, sequentialReferenceError("execution_contract", err)
	}
	backendSHA, err := executionbackend.ContractSHA256(backend)
	if err != nil {
		return GradingPlan{}, sequentialReferenceError("execution_identity", err)
	}
	plan := grading.Plan{
		Schema: grading.PlanSchema, SchemaVersion: grading.SchemaVersion, ContractVersion: grading.ContractVersion,
		ContractSHA256: contractSHA, Mode: grading.ModeDeterministic, InputProjectionSHA256: inputProjectionSHA256,
		EnvironmentSHA256: backendSHA,
		Checks: []grading.Check{{
			ID: "execution-verdict", Kind: grading.CheckJSONValue, Visibility: grading.VisibilityPublic,
			JSONValue: &grading.JSONValueRule{EvidenceID: "execution-receipt", Pointer: "/verdict", Expected: json.RawMessage(`"succeeded"`)},
		}},
		Script: []grading.ScriptInstruction{},
		Limits: grading.PlanLimits{DeadlineMillis: 15_000, MaxInputBytes: executionbackend.MaxReceiptBytes, MaxOutputBytes: grading.MaxReceiptBytes},
	}
	// Deterministic plans require a nil script, not an empty present array.
	plan.Script = nil
	if _, err := grading.Admit(contract, plan); err != nil {
		return GradingPlan{}, sequentialReferenceError("grading_plan", err)
	}
	return plan, nil
}

func sequentialReferenceCapabilities() map[experiment.CapabilityID]bool {
	return map[experiment.CapabilityID]bool{
		experiment.CapabilityChannelImplicit:          true,
		experiment.CapabilityConditionCurrent:         true,
		experiment.CapabilityConditionNone:            true,
		experiment.CapabilityControlNearMissNegative:  true,
		experiment.CapabilityControlPositive:          true,
		experiment.CapabilityObserveCandidateRecall:   true,
		experiment.CapabilityObserveInstructionAccess: true,
		experiment.CapabilityObserveLoad:              true,
		experiment.CapabilityObserveOutcome:           true,
		experiment.CapabilityObserveReferenceAccess:   true,
		experiment.CapabilityObserveScriptAccess:      true,
		experiment.CapabilityObserveSelection:         true,
		experiment.CapabilityObserveUsefulAdherence:   true,
		experiment.CapabilityObserveVerifierOutcome:   true,
	}
}

func sequentialReferenceAnalysisSupported(plan experiment.AnalysisPlan) bool {
	if len(plan.Metrics) != 1 || plan.Metrics[0].ID != experiment.MetricOutcome ||
		plan.RepeatedAttempts.Kind != experiment.RepeatedAttemptsNone || !slices.Equal(plan.RepeatedAttempts.K, []uint32{1}) {
		return false
	}
	for _, required := range []experiment.ExclusionReason{
		experiment.ExclusionGradeIncomplete,
		experiment.ExclusionLifecycleIncomplete,
		experiment.ExclusionLifecycleUnknown,
	} {
		if !slices.Contains(plan.AllowedExclusions, required) {
			return false
		}
	}
	return true
}

func sequentialReferenceTreatmentsSupported(treatments []experiment.TreatmentRequest) bool {
	if len(treatments) != 3 {
		return false
	}
	roles := map[experiment.TreatmentRole]bool{}
	capabilities := sequentialReferenceCapabilities()
	for _, treatment := range treatments {
		if roles[treatment.Role] || !sequentialReferenceTreatmentSupported(treatment) {
			return false
		}
		roles[treatment.Role] = true
		if !capabilities[capabilityForSequentialTreatment(treatment)] {
			return false
		}
	}
	return roles[experiment.RoleReference] && roles[experiment.RoleCandidate] && roles[experiment.RoleControl]
}

func sequentialReferenceTreatmentSupported(treatment experiment.TreatmentRequest) bool {
	if len(treatment.DistractorSHA256) != 0 || treatment.SkillVersionSHA256 != "" || treatment.RetrieverSHA256 != "" {
		return false
	}
	want := experiment.ArmSelector{ActivationChannel: experiment.ChannelImplicit}
	switch treatment.Role {
	case experiment.RoleReference:
		want.Condition, want.SelectionAuthority, want.Control = experiment.ConditionNone, experiment.SelectionNone, experiment.ControlPositive
		return treatment.Arm == want && treatment.ControlProvenance == experiment.ControlFromSource &&
			treatment.SkillSHA256 == "" && !treatment.ExpectedActivation
	case experiment.RoleCandidate:
		want.Condition, want.SelectionAuthority, want.Control = experiment.ConditionCurrent, experiment.SelectionAgent, experiment.ControlPositive
		return treatment.Arm == want && treatment.ControlProvenance == experiment.ControlFromSource &&
			treatment.SkillSHA256 != "" && treatment.ExpectedActivation
	case experiment.RoleControl:
		want.Condition, want.SelectionAuthority, want.Control = experiment.ConditionNone, experiment.SelectionNone, experiment.ControlNearMissNegative
		return treatment.Arm == want && treatment.ControlProvenance == experiment.ControlSeparatelyAuthored &&
			treatment.SkillSHA256 == "" && !treatment.ExpectedActivation
	default:
		return false
	}
}

func capabilityForSequentialTreatment(treatment experiment.TreatmentRequest) experiment.CapabilityID {
	switch treatment.Arm.Condition {
	case experiment.ConditionNone:
		return experiment.CapabilityConditionNone
	case experiment.ConditionCurrent:
		return experiment.CapabilityConditionCurrent
	default:
		return ""
	}
}

func sequentialReferenceExecutionPlanSupported(plan executionbackend.Plan) bool {
	if plan.Network.Mode != executionbackend.NetworkDeny || plan.Credentials.Mode != executionbackend.CredentialsNone {
		return false
	}
	switch plan.Program.Kind {
	case executionbackend.ProgramReferenceCopy:
		return len(plan.Artifacts) == 1 && plan.Artifacts[0].Privacy == executionbackend.PrivacyPublic
	case executionbackend.ProgramWaitForCancel:
		return len(plan.Artifacts) == 0
	default:
		return false
	}
}
