package agenteval

import (
	"errors"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

var ErrPrivateExperimentCompatibility = errors.New("private_experiment_compatibility_invalid")

// PrivateActivationExperimentInput contains only reviewed digest projections.
// The private prompt contracts, paths, source bytes, and provider evidence do
// not enter the neutral manifest.
type PrivateActivationExperimentInput struct {
	Plan               PrivateActivationStudyPlan
	ActivationContract PrivateActivationStudyContract
	CapabilityContract ExperimentCapabilityContract
	AnalysisPlan       ExperimentAnalysisPlan
	Case               ExperimentCaseBinding
	SkillSHA256        string
}

// CompilePrivateActivationExperiment projects readable schema-v1/v2 private
// four-cell studies into the neutral immutable manifest. It is compare-only:
// it does not expose private prompt material, modify the source lifecycle, or
// authorize another attempt.
func CompilePrivateActivationExperiment(input PrivateActivationExperimentInput) (ExperimentManifest, error) {
	if err := input.ActivationContract.Validate(); err != nil || !validSHA256(input.SkillSHA256) {
		return ExperimentManifest{}, ErrPrivateExperimentCompatibility
	}
	profile := experiment.CompatibilityNone
	var planSHA256 string
	var err error
	switch input.Plan.SchemaVersion {
	case legacyPrivateActivationStudyPlanSchemaVersion:
		if err = validateLegacyPrivateActivationStudyPlan(input.Plan); err == nil {
			planSHA256, err = legacyPrivateActivationStudyPlanSHA256(input.Plan)
		}
		profile = experiment.CompatibilityPrivateActivationV1
	case PrivateActivationStudyPlanSchemaVersion:
		if err = input.Plan.Validate(); err == nil {
			planSHA256, err = input.Plan.SHA256()
		}
		profile = experiment.CompatibilityPrivateActivationV2
	default:
		err = ErrPrivateExperimentCompatibility
	}
	if err != nil || len(input.Plan.Cells)%len(PrivateActivationStudyTreatments()) != 0 {
		return ExperimentManifest{}, ErrPrivateExperimentCompatibility
	}
	treatments := make([]experiment.TreatmentRequest, 0, len(PrivateActivationStudyTreatments()))
	selectors := make(map[string]experiment.ArmSelector, len(PrivateActivationStudyTreatments()))
	for index, activation := range PrivateActivationStudyTreatments() {
		contract, ok := input.ActivationContract.Treatment(activation)
		if !ok {
			return ExperimentManifest{}, ErrPrivateExperimentCompatibility
		}
		selector, ok := privateActivationExperimentSelector(activation)
		if !ok {
			return ExperimentManifest{}, ErrPrivateExperimentCompatibility
		}
		role := experiment.RoleCandidate
		if index == 0 {
			role = experiment.RoleReference
		}
		selectors[activation] = selector
		treatments = append(treatments, experiment.TreatmentRequest{
			Arm: selector, Role: role, SkillSHA256: input.SkillSHA256, DistractorSHA256: []string{},
			ControlSHA256: input.Case.CaseSHA256, ControlProvenance: experiment.ControlLegacyProjection,
			ExecutionBindingSHA256: contract.RunSpecSHA256, ExpectedActivation: true,
		})
	}
	legacySequence := make([]experiment.ArmSelector, 0, len(input.Plan.Cells))
	for _, cell := range input.Plan.Cells {
		contract, ok := input.ActivationContract.Treatment(cell.SkillActivation)
		selector, selectorOK := selectors[cell.SkillActivation]
		if !ok || !selectorOK || cell.ContractSHA256 != contract.RunSpecSHA256 {
			return ExperimentManifest{}, ErrPrivateExperimentCompatibility
		}
		legacySequence = append(legacySequence, selector)
	}
	stratumSHA256, err := contentMinimizedAttemptDigest("private-activation-experiment-stratum", struct {
		CommonContractSHA256 string `json:"common_contract_sha256"`
		PlanSHA256           string `json:"plan_sha256"`
	}{input.ActivationContract.CommonContractSHA256, planSHA256})
	if err != nil {
		return ExperimentManifest{}, ErrPrivateExperimentCompatibility
	}
	blocks := uint32(len(input.Plan.Cells) / len(treatments)) // #nosec G115 -- private plan validation caps cells at 400.
	design, err := experiment.SealDesign(experiment.Design{
		CompatibilityProfile:     profile,
		CapabilityContractSHA256: input.CapabilityContract.CapabilityContractSHA256,
		AnalysisPlanSHA256:       input.AnalysisPlan.AnalysisPlanSHA256,
		Case:                     input.Case,
		Treatments:               treatments,
		Strata:                   []experiment.StratumRequest{{BindingSHA256: stratumSHA256, Blocks: blocks}},
		Ordering: experiment.OrderingPolicy{
			Kind: experiment.OrderingLegacyFixed, SeedSHA256: planSHA256, LegacySequence: legacySequence,
		},
		Stopping: experiment.StoppingRule{
			Kind: experiment.StoppingFixedRoster, MaximumBlocks: blocks, SafetyStops: []experiment.SafetyStopCode{},
		},
	})
	if err != nil {
		return ExperimentManifest{}, ErrPrivateExperimentCompatibility
	}
	return experiment.Compile(design, input.CapabilityContract, input.AnalysisPlan)
}

func privateActivationExperimentSelector(activation string) (experiment.ArmSelector, bool) {
	channels := map[string]experiment.ActivationChannel{
		SkillActivationImplicit:  experiment.ChannelImplicit,
		SkillActivationExplicit:  experiment.ChannelExplicitUser,
		SkillActivationDeveloper: experiment.ChannelDeveloper,
		SkillActivationCombined:  experiment.ChannelCombined,
	}
	channel, ok := channels[activation]
	return experiment.ArmSelector{
		Condition: experiment.ConditionCurrent, ActivationChannel: channel,
		SelectionAuthority: experiment.SelectionAgent, Control: experiment.ControlPositive,
	}, ok
}
