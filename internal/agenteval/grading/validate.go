package grading

import (
	"fmt"
	"slices"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
)

const maxRuleValue = ^uint64(0)

func CheckKinds() []CheckKind { return slices.Clone(closedCheckKinds) }
func Modes() []Mode           { return slices.Clone(closedModes) }

func NewContract(id, version, implementationSHA256, contentSHA256 string, modes []ModePolicy, support map[CheckKind]Support) (Contract, error) {
	for kind := range support {
		if !slices.Contains(closedCheckKinds, kind) {
			return Contract{}, contractError("unknown_capability")
		}
	}
	policies := slices.Clone(modes)
	sort.Slice(policies, func(i, j int) bool { return policies[i].Mode < policies[j].Mode })
	capabilities := make([]Capability, len(closedCheckKinds))
	for index, kind := range closedCheckKinds {
		state, ok := support[kind]
		if !ok {
			state = SupportUnknown
		}
		capabilities[index] = Capability{Kind: kind, Support: state}
	}
	contract := Contract{
		Schema: ContractSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		GraderID: id, GraderVersion: version, ImplementationSHA256: implementationSHA256, ContentSHA256: contentSHA256,
		Modes: policies, Capabilities: capabilities,
		Limits: Limits{MaxChecks: MaxChecks, MaxEvidenceItems: MaxEvidenceItems, MaxEvidenceBytes: MaxEvidenceBytes,
			MaxScriptInstructions: MaxScriptInstructions, MaxCitationsPerCheck: MaxCitationsPerCheck},
	}
	if err := ValidateContract(contract); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func BuiltinContract() (Contract, error) {
	support := make(map[CheckKind]Support, len(closedCheckKinds))
	for _, kind := range closedCheckKinds {
		support[kind] = SupportSupported
	}
	implementation, content := builtinIdentities()
	return NewContract("builtin-grading", "1", implementation, content, builtinModes(), support)
}

func builtinIdentities() (string, string) {
	return hashDomain("builtin-grading-implementation", []byte("closed-v1")),
		hashDomain("builtin-grading-content", []byte("deterministic+uint64-rules+json-cardinality+sequence-alternatives+typed-script+offline-panel/v1"))
}

func builtinModes() []ModePolicy {
	return []ModePolicy{
		{Mode: ModeDeterministic, Support: SupportSupported, ExecutionClass: ExecutionInProcess},
		{Mode: ModeJudgeAssessment, Support: SupportSupported, ExecutionClass: ExecutionOfflineAssessment},
		{Mode: ModeScriptDSL, Support: SupportSupported, ExecutionClass: ExecutionHermeticVerifier},
	}
}

func ReferenceBackendSHA256() (string, error) {
	contract, err := executionbackend.ReferenceContract()
	if err != nil {
		return "", contractError("reference_backend")
	}
	return executionbackend.ContractSHA256(contract)
}

func ValidateContract(contract Contract) error {
	if contract.Schema != ContractSchema || contract.SchemaVersion != SchemaVersion || contract.ContractVersion != ContractVersion ||
		!validIdentifier(contract.GraderID) || !validVersion(contract.GraderVersion) || !validSHA256(contract.ImplementationSHA256) ||
		!validSHA256(contract.ContentSHA256) || len(contract.Modes) != len(closedModes) || len(contract.Capabilities) != len(closedCheckKinds) ||
		!validLimits(contract.Limits) {
		return contractError("contract_shape")
	}
	for index, policy := range contract.Modes {
		if policy.Mode != closedModes[index] || !validModePolicy(policy) {
			return contractError("mode_policy")
		}
	}
	for index, capability := range contract.Capabilities {
		if capability.Kind != closedCheckKinds[index] || !capability.Support.valid() {
			return contractError("capability")
		}
	}
	if contract.GraderID == "builtin-grading" {
		implementation, content := builtinIdentities()
		if contract.GraderVersion != "1" || contract.ImplementationSHA256 != implementation || contract.ContentSHA256 != content ||
			!slices.Equal(contract.Modes, builtinModes()) {
			return contractError("builtin_identity")
		}
		for _, capability := range contract.Capabilities {
			if capability.Support != SupportSupported {
				return contractError("builtin_capability")
			}
		}
	}
	return nil
}

func validLimits(limits Limits) bool {
	return limits.MaxChecks > 0 && limits.MaxChecks <= MaxChecks && limits.MaxEvidenceItems > 0 && limits.MaxEvidenceItems <= MaxEvidenceItems &&
		limits.MaxEvidenceBytes > 0 && limits.MaxEvidenceBytes <= MaxEvidenceBytes && limits.MaxScriptInstructions > 0 &&
		limits.MaxScriptInstructions <= MaxScriptInstructions && limits.MaxCitationsPerCheck > 0 &&
		limits.MaxCitationsPerCheck <= MaxCitationsPerCheck
}

func validModePolicy(policy ModePolicy) bool {
	if !policy.Mode.valid() || !policy.Support.valid() || !policy.ExecutionClass.valid() {
		return false
	}
	if policy.Support != SupportSupported && (policy.Process || policy.Provider || policy.Network || policy.Credentials) {
		return false
	}
	switch policy.ExecutionClass {
	case ExecutionInProcess:
		return !policy.Process && !policy.Provider && !policy.Network && !policy.Credentials
	case ExecutionHermeticVerifier:
		return !policy.Provider && !policy.Network && !policy.Credentials
	case ExecutionOfflineAssessment:
		return !policy.Process && !policy.Provider && !policy.Network && !policy.Credentials
	default:
		return false
	}
}

func Admit(contract Contract, plan Plan) (AdmittedPlan, error) {
	if err := ValidateContract(contract); err != nil {
		return AdmittedPlan{}, err
	}
	contractSHA, err := ContractSHA256(contract)
	if err != nil || plan.ContractSHA256 != contractSHA {
		return AdmittedPlan{}, contractError("plan_contract_binding")
	}
	if err := ValidatePlan(plan); err != nil {
		return AdmittedPlan{}, err
	}
	if !planWithinContractLimits(contract, plan) {
		return AdmittedPlan{}, policyError("plan_limits")
	}
	policy := contract.Modes[slices.Index(closedModes, plan.Mode)]
	if policy.Support != SupportSupported {
		return AdmittedPlan{}, newError(ErrorUnsupported, fmt.Errorf("%w: mode %s", ErrUnsupported, plan.Mode))
	}
	claims := make(map[CheckKind]Support, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		claims[capability.Kind] = capability.Support
	}
	for _, check := range plan.Checks {
		if claims[check.Kind] != SupportSupported {
			return AdmittedPlan{}, newError(ErrorUnsupported, fmt.Errorf("%w: check %s", ErrUnsupported, check.Kind))
		}
	}
	if plan.Mode == ModeScriptDSL {
		backendSHA, err := ReferenceBackendSHA256()
		if err != nil || plan.ExecutionBackendSHA256 != backendSHA {
			return AdmittedPlan{}, policyError("script_backend")
		}
	}
	planSHA, err := PlanSHA256(plan)
	if err != nil {
		return AdmittedPlan{}, err
	}
	return AdmittedPlan{contract: cloneContract(contract), plan: clonePlan(plan), planSHA: planSHA}, nil
}

func planWithinContractLimits(contract Contract, plan Plan) bool {
	if len(plan.Checks) > int(contract.Limits.MaxChecks) || len(plan.Script) > int(contract.Limits.MaxScriptInstructions) {
		return false
	}
	for _, check := range plan.Checks {
		if check.Qualitative != nil && len(check.Qualitative.EvidenceIDs) > int(contract.Limits.MaxCitationsPerCheck) {
			return false
		}
	}
	return true
}

func ValidatePlan(plan Plan) error {
	if plan.Schema != PlanSchema || plan.SchemaVersion != SchemaVersion || plan.ContractVersion != ContractVersion ||
		!validSHA256(plan.ContractSHA256) || !plan.Mode.valid() || !validSHA256(plan.InputProjectionSHA256) ||
		!validSHA256(plan.EnvironmentSHA256) || plan.Checks == nil || len(plan.Checks) == 0 || len(plan.Checks) > MaxChecks ||
		!validPlanLimits(plan.Limits) {
		return contractError("plan_shape")
	}
	for index := range plan.Checks {
		if index > 0 && plan.Checks[index-1].ID >= plan.Checks[index].ID || validateCheck(plan.Checks[index]) != nil {
			return contractError("plan_checks")
		}
	}
	if !planRuleItemsWithinBound(plan.Checks) {
		return contractError("plan_rule_items")
	}
	switch plan.Mode {
	case ModeDeterministic:
		if plan.ExecutionBackendSHA256 != "" || plan.Script != nil || plan.Judge != nil || containsQualitative(plan.Checks) {
			return contractError("deterministic_mode")
		}
	case ModeScriptDSL:
		if !validSHA256(plan.ExecutionBackendSHA256) || plan.Script == nil || plan.Judge != nil || containsQualitative(plan.Checks) ||
			validateScriptShape(plan.Checks, plan.Script) != nil {
			return contractError("script_mode")
		}
	case ModeJudgeAssessment:
		if plan.ExecutionBackendSHA256 != "" || plan.Script != nil || plan.Judge == nil || !onlyQualitative(plan.Checks) ||
			validateJudgePolicy(*plan.Judge) != nil {
			return contractError("judge_mode")
		}
	}
	return nil
}

func planRuleItemsWithinBound(checks []Check) bool {
	remaining := MaxEvidenceItems
	for _, check := range checks {
		count := 1
		switch check.Kind {
		case CheckJSONSchema:
			count += len(check.JSONSchema.Fields)
		case CheckTreeDiff:
			count += len(check.TreeDiff.Expected)
		case CheckToolSequence:
			count += len(check.ToolSequence.Expected)
			for _, alternative := range check.ToolSequence.Alternatives {
				count += len(alternative)
			}
		case CheckActionSequence:
			count += len(check.ActionSequence.Expected)
			for _, alternative := range check.ActionSequence.Alternatives {
				count += len(alternative)
			}
		case CheckQualitative:
			count += len(check.Qualitative.EvidenceIDs)
		}
		if count > remaining {
			return false
		}
		remaining -= count
	}
	return true
}

func validPlanLimits(limits PlanLimits) bool {
	return limits.DeadlineMillis > 0 && limits.DeadlineMillis <= MaxDurationMillis && limits.MaxInputBytes > 0 &&
		limits.MaxInputBytes <= MaxEvidenceBytes && limits.MaxOutputBytes > 0 && limits.MaxOutputBytes <= MaxReceiptBytes
}

func validateCheck(check Check) error {
	if !validIdentifier(check.ID) || !slices.Contains(closedCheckKinds, check.Kind) || !check.Visibility.valid() || check.ruleCount() != 1 {
		return contractError("check_shape")
	}
	if !check.ruleMatchesKind() {
		return contractError("check_rule")
	}
	switch check.Kind {
	case CheckFileExists:
		if !validIdentifier(check.FileExists.EvidenceID) {
			return contractError("file_exists")
		}
	case CheckFileMetadata:
		if !validIdentifier(check.FileMetadata.EvidenceID) || check.FileMetadata.ExpectedSizeBytes > MaxEvidenceBytes ||
			check.FileMetadata.ExpectedMode == 0 || check.FileMetadata.ExpectedMode > 0o777 {
			return contractError("file_metadata")
		}
	case CheckFileSHA256:
		if !validIdentifier(check.FileSHA256.EvidenceID) || !validSHA256(check.FileSHA256.ExpectedSHA256) {
			return contractError("file_sha")
		}
	case CheckJSONValue:
		if !validIdentifier(check.JSONValue.EvidenceID) || !validJSONPointer(check.JSONValue.Pointer) || !validEmbeddedJSON(check.JSONValue.Expected) {
			return contractError("json_value")
		}
	case CheckJSONSchema:
		if !validIdentifier(check.JSONSchema.EvidenceID) || check.JSONSchema.Fields == nil || len(check.JSONSchema.Fields) == 0 ||
			len(check.JSONSchema.Fields) > MaxChecks {
			return contractError("json_schema")
		}
		for index, field := range check.JSONSchema.Fields {
			if !validJSONPointer(field.Pointer) || !field.Type.valid() || field.MinimumItems > MaxEvidenceItems ||
				(field.MinimumItems != 0 && (field.Type != JSONTypeArray || !field.Required)) ||
				index > 0 && check.JSONSchema.Fields[index-1].Pointer >= field.Pointer {
				return contractError("json_schema_field")
			}
		}
	case CheckCommandExit:
		if !validIdentifier(check.CommandExit.EvidenceID) {
			return contractError("command_exit")
		}
	case CheckCommandOutput:
		if !validIdentifier(check.CommandOutput.EvidenceID) || !check.CommandOutput.Stream.valid() ||
			!validSHA256(check.CommandOutput.ExpectedSHA256) {
			return contractError("command_output")
		}
	case CheckTreeDiff:
		if !validIdentifier(check.TreeDiff.EvidenceID) || check.TreeDiff.Expected == nil || len(check.TreeDiff.Expected) > MaxEvidenceItems {
			return contractError("tree_diff")
		}
		for index, change := range check.TreeDiff.Expected {
			if !validRelativePath(change.Path) || !change.Kind.valid() || index > 0 && check.TreeDiff.Expected[index-1].Path >= change.Path ||
				change.Kind == TreeRemoved && change.SHA256 != "" || change.Kind != TreeRemoved && !validSHA256(change.SHA256) {
				return contractError("tree_change")
			}
		}
	case CheckToolSequence:
		if !validSequenceRule(check.ToolSequence) || check.ToolSequence.MinimumSimilarityBPS != 10_000 {
			return contractError("tool_sequence")
		}
	case CheckActionSequence:
		if !validSequenceRule(check.ActionSequence) {
			return contractError("action_sequence")
		}
	case CheckSkillActivation:
		if !validCountRule(check.SkillActivation) {
			return contractError("skill_activation")
		}
	case CheckSkillUse:
		if !validCountRule(check.SkillUse) {
			return contractError("skill_use")
		}
	case CheckBudget:
		if !validIdentifier(check.Budget.EvidenceID) || check.Budget.Minimum > check.Budget.Maximum || check.Budget.Maximum > maxRuleValue {
			return contractError("budget")
		}
	case CheckPolicy:
		if !validIdentifier(check.Policy.EvidenceID) || check.Policy.MaximumViolations > MaxEvidenceItems {
			return contractError("policy")
		}
	case CheckQualitative:
		if !validIdentifier(check.Qualitative.RubricCriterionID) || check.Qualitative.EvidenceIDs == nil ||
			len(check.Qualitative.EvidenceIDs) == 0 || len(check.Qualitative.EvidenceIDs) > MaxCitationsPerCheck {
			return contractError("qualitative")
		}
		for index, id := range check.Qualitative.EvidenceIDs {
			if !validIdentifier(id) || index > 0 && check.Qualitative.EvidenceIDs[index-1] >= id {
				return contractError("qualitative_evidence")
			}
		}
	}
	return nil
}

func (check Check) ruleCount() int {
	count := 0
	for _, present := range []bool{check.FileExists != nil, check.FileMetadata != nil, check.FileSHA256 != nil, check.JSONValue != nil,
		check.JSONSchema != nil, check.CommandExit != nil, check.CommandOutput != nil, check.TreeDiff != nil, check.ToolSequence != nil,
		check.ActionSequence != nil, check.SkillActivation != nil, check.SkillUse != nil, check.Budget != nil, check.Policy != nil,
		check.Qualitative != nil} {
		if present {
			count++
		}
	}
	return count
}

func (check Check) ruleMatchesKind() bool {
	return check.Kind == CheckFileExists && check.FileExists != nil || check.Kind == CheckFileMetadata && check.FileMetadata != nil ||
		check.Kind == CheckFileSHA256 && check.FileSHA256 != nil || check.Kind == CheckJSONValue && check.JSONValue != nil ||
		check.Kind == CheckJSONSchema && check.JSONSchema != nil || check.Kind == CheckCommandExit && check.CommandExit != nil ||
		check.Kind == CheckCommandOutput && check.CommandOutput != nil || check.Kind == CheckTreeDiff && check.TreeDiff != nil ||
		check.Kind == CheckToolSequence && check.ToolSequence != nil || check.Kind == CheckActionSequence && check.ActionSequence != nil ||
		check.Kind == CheckSkillActivation && check.SkillActivation != nil || check.Kind == CheckSkillUse && check.SkillUse != nil ||
		check.Kind == CheckBudget && check.Budget != nil || check.Kind == CheckPolicy && check.Policy != nil ||
		check.Kind == CheckQualitative && check.Qualitative != nil
}

func validSequenceRule(rule *SequenceRule) bool {
	if rule == nil || !validIdentifier(rule.EvidenceID) || rule.Expected == nil || len(rule.Expected) > MaxSequenceItems ||
		rule.MinimumSimilarityBPS < 1 || rule.MinimumSimilarityBPS > 10_000 {
		return false
	}
	for _, value := range rule.Expected {
		if !validText(value, MaxRelativePathBytes) {
			return false
		}
	}
	if rule.Alternatives != nil {
		if len(rule.Alternatives) < 2 || len(rule.Alternatives) > MaxSequenceItems || len(rule.Expected) != 0 ||
			rule.MinimumSimilarityBPS != 10_000 {
			return false
		}
		var total int
		for index, alternative := range rule.Alternatives {
			if alternative == nil || len(alternative) > MaxSequenceItems || index > 0 && slices.Compare(rule.Alternatives[index-1], alternative) >= 0 {
				return false
			}
			total += len(alternative)
			if total > MaxEvidenceItems {
				return false
			}
			for _, value := range alternative {
				if !validText(value, MaxRelativePathBytes) {
					return false
				}
			}
		}
	}
	return true
}

func validCountRule(rule *CountRule) bool {
	return rule != nil && validIdentifier(rule.EvidenceID) && rule.Minimum <= rule.Maximum && rule.Maximum <= maxRuleValue
}

func containsQualitative(checks []Check) bool {
	return slices.ContainsFunc(checks, func(check Check) bool { return check.Kind == CheckQualitative })
}

func onlyQualitative(checks []Check) bool {
	return !slices.ContainsFunc(checks, func(check Check) bool { return check.Kind != CheckQualitative })
}

func validateJudgePolicy(policy JudgePolicy) error {
	if !validSHA256(policy.RubricSHA256) || !validSHA256(policy.PromptContractSHA256) ||
		!validSHA256(policy.BlindAssignmentSHA256) || policy.ToolPolicy != "none" ||
		len(policy.Reviewers) != 3 && len(policy.Reviewers) != 5 {
		return contractError("judge_policy")
	}
	for index, reviewer := range policy.Reviewers {
		if !validReviewer(reviewer) || index > 0 && policy.Reviewers[index-1].ID >= reviewer.ID {
			return contractError("judge_reviewer")
		}
	}
	return nil
}

func validReviewer(reviewer Reviewer) bool {
	if !validIdentifier(reviewer.ID) || !reviewer.Kind.valid() || reviewer.MaxInputTokens > MaxTokens ||
		reviewer.MaxOutputTokens > MaxTokens || reviewer.MaxEstimatedCostMicroUSD > MaxCostMicroUSD {
		return false
	}
	switch reviewer.Kind {
	case ReviewerHuman:
		return reviewer.Model == "" && reviewer.EnvironmentSHA256 == "" && reviewer.MaxInputTokens == 0 && reviewer.MaxOutputTokens == 0 &&
			reviewer.MaxEstimatedCostMicroUSD == 0
	case ReviewerModel:
		return validText(reviewer.Model, MaxReviewerModelBytes) && validSHA256(reviewer.EnvironmentSHA256) && reviewer.MaxInputTokens > 0 &&
			reviewer.MaxOutputTokens > 0 && reviewer.MaxEstimatedCostMicroUSD > 0
	default:
		return false
	}
}
