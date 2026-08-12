package grading

import (
	"context"
	"slices"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
)

func validateScriptShape(checks []Check, script []ScriptInstruction) error {
	if len(script) == 0 || len(script) > MaxScriptInstructions {
		return contractError("script_length")
	}
	checkIDs := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		checkIDs[check.ID] = struct{}{}
	}
	emitted := make([]string, 0, len(checks))
	stack := 0
	for _, instruction := range script {
		if !validScriptInstruction(instruction) {
			return contractError("script_instruction")
		}
		switch instruction.Operation {
		case ScriptFileExists, ScriptFileSHA256Equals, ScriptJSONEquals, ScriptCommandExitEquals, ScriptResourceMaximum,
			ScriptEventCountMinimum:
			stack++
		case ScriptAnd, ScriptOr:
			if stack < 2 {
				return contractError("script_stack_underflow")
			}
			stack--
		case ScriptNot:
			if stack < 1 {
				return contractError("script_stack_underflow")
			}
		case ScriptEmit:
			if stack < 1 {
				return contractError("script_stack_underflow")
			}
			if _, ok := checkIDs[instruction.CheckID]; !ok || len(emitted) > 0 && emitted[len(emitted)-1] >= instruction.CheckID {
				return contractError("script_emit")
			}
			emitted = append(emitted, instruction.CheckID)
			stack--
		}
		if stack > MaxScriptStack {
			return contractError("script_stack_limit")
		}
	}
	if stack != 0 || len(emitted) != len(checks) {
		return contractError("script_closure")
	}
	for index := range checks {
		if emitted[index] != checks[index].ID {
			return contractError("script_coverage")
		}
	}
	return nil
}

func validScriptInstruction(instruction ScriptInstruction) bool {
	empty := func(check, evidence, pointer, digest string, expected []byte, integer *int64, unsigned *uint64) bool {
		return check == "" && evidence == "" && pointer == "" && digest == "" && expected == nil && integer == nil && unsigned == nil
	}
	switch instruction.Operation {
	case ScriptFileExists:
		return instruction.CheckID == "" && validIdentifier(instruction.EvidenceID) && instruction.Pointer == "" &&
			instruction.ExpectedSHA256 == "" && instruction.ExpectedJSON == nil && instruction.Integer == nil && instruction.Unsigned == nil
	case ScriptFileSHA256Equals:
		return instruction.CheckID == "" && validIdentifier(instruction.EvidenceID) && instruction.Pointer == "" &&
			validSHA256(instruction.ExpectedSHA256) && instruction.ExpectedJSON == nil && instruction.Integer == nil && instruction.Unsigned == nil
	case ScriptJSONEquals:
		return instruction.CheckID == "" && validIdentifier(instruction.EvidenceID) && validJSONPointer(instruction.Pointer) &&
			instruction.ExpectedSHA256 == "" && validEmbeddedJSON(instruction.ExpectedJSON) && instruction.Integer == nil && instruction.Unsigned == nil
	case ScriptCommandExitEquals:
		return instruction.CheckID == "" && validIdentifier(instruction.EvidenceID) && instruction.Pointer == "" &&
			instruction.ExpectedSHA256 == "" && instruction.ExpectedJSON == nil && instruction.Integer != nil && instruction.Unsigned == nil
	case ScriptResourceMaximum, ScriptEventCountMinimum:
		return instruction.CheckID == "" && validIdentifier(instruction.EvidenceID) && instruction.Pointer == "" &&
			instruction.ExpectedSHA256 == "" && instruction.ExpectedJSON == nil && instruction.Integer == nil && instruction.Unsigned != nil &&
			*instruction.Unsigned <= maxRuleValue
	case ScriptAnd, ScriptNot, ScriptOr:
		return empty(instruction.CheckID, instruction.EvidenceID, instruction.Pointer, instruction.ExpectedSHA256, instruction.ExpectedJSON,
			instruction.Integer, instruction.Unsigned)
	case ScriptEmit:
		return validIdentifier(instruction.CheckID) && instruction.EvidenceID == "" && instruction.Pointer == "" &&
			instruction.ExpectedSHA256 == "" && instruction.ExpectedJSON == nil && instruction.Integer == nil && instruction.Unsigned == nil
	default:
		return false
	}
}

type scriptValue struct {
	presence  Presence
	value     bool
	citations []Citation
}

func EvaluateScript(ctx context.Context, admitted AdmittedPlan, backend executionbackend.Contract, evidence *PreparedEvidence) (Receipt, error) {
	if ctx == nil || admitted.plan.Mode != ModeScriptDSL || executionbackend.ValidateContract(backend) != nil ||
		backend.Assurance != executionbackend.AssuranceHermeticReference {
		return Receipt{}, policyError("script_backend")
	}
	backendSHA, err := executionbackend.ContractSHA256(backend)
	if err != nil || backendSHA != admitted.plan.ExecutionBackendSHA256 {
		return Receipt{}, policyError("script_backend_binding")
	}
	if err := evidence.requireAlive(); err != nil {
		return Receipt{}, err
	}
	stack := make([]scriptValue, 0, MaxScriptStack)
	decisions := make([]Decision, 0, len(admitted.plan.Checks))
	checks := make(map[string]Check, len(admitted.plan.Checks))
	for _, check := range admitted.plan.Checks {
		checks[check.ID] = check
	}
	for _, instruction := range admitted.plan.Script {
		if err := contextError(ctx); err != nil {
			return Receipt{}, err
		}
		switch instruction.Operation {
		case ScriptFileExists:
			stack = append(stack, scriptFileExists(evidence, instruction.EvidenceID))
		case ScriptFileSHA256Equals:
			stack = append(stack, scriptFileSHA(evidence, instruction.EvidenceID, instruction.ExpectedSHA256))
		case ScriptJSONEquals:
			stack = append(stack, scriptJSONEquals(evidence, instruction.EvidenceID, instruction.Pointer, instruction.ExpectedJSON))
		case ScriptCommandExitEquals:
			stack = append(stack, scriptCommandExit(evidence, instruction.EvidenceID, *instruction.Integer))
		case ScriptResourceMaximum:
			stack = append(stack, scriptCounter(evidence, instruction.EvidenceID, *instruction.Unsigned, false))
		case ScriptEventCountMinimum:
			stack = append(stack, scriptCounter(evidence, instruction.EvidenceID, *instruction.Unsigned, true))
		case ScriptAnd, ScriptOr:
			right, left := stack[len(stack)-1], stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, combineScriptValues(left, right, instruction.Operation == ScriptAnd))
		case ScriptNot:
			if stack[len(stack)-1].presence == PresenceObserved {
				stack[len(stack)-1].value = !stack[len(stack)-1].value
			}
		case ScriptEmit:
			value := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			check := checks[instruction.CheckID]
			decision := Decision{CheckID: check.ID, Presence: PresenceUnknown, Authority: AuthorityScript, Citations: []Citation{}}
			if value.presence == PresenceObserved && citationsMatchVisibility(evidence, value.citations, check.Visibility) {
				decision.Presence = PresenceObserved
				decision.Passed = value.value
				decision.Citations = value.citations
			}
			decisions = append(decisions, decision)
		}
	}
	receipt := newReceipt(admitted, evidence, decisions, []ReviewerReceipt{}, notApplicableUsage(), []Disagreement{})
	if err := validateProducedReceipt(admitted, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func scriptFileExists(evidence *PreparedEvidence, id string) scriptValue {
	ref, ok := evidence.byID[id]
	if !ok || ref.kind != EvidenceFile {
		return scriptUnknown()
	}
	return scriptObserved(evidence, id, evidence.set.Files[ref.index].Present)
}

func scriptFileSHA(evidence *PreparedEvidence, id, want string) scriptValue {
	ref, ok := evidence.byID[id]
	if !ok || ref.kind != EvidenceFile || !evidence.set.Files[ref.index].Present {
		return scriptUnknown()
	}
	return scriptObserved(evidence, id, sha256Hex(evidence.set.Files[ref.index].Data) == want)
}

func scriptJSONEquals(evidence *PreparedEvidence, id, pointer string, want []byte) scriptValue {
	ref, ok := evidence.byID[id]
	if !ok || ref.kind != EvidenceFile || !evidence.set.Files[ref.index].Present {
		return scriptUnknown()
	}
	value, valid := decodeEvidenceJSON(evidence.set.Files[ref.index].Data)
	selected, found := resolveJSONPointer(value, pointer)
	return scriptObserved(evidence, id, valid && found && jsonValueEqual(selected, want))
}

func scriptCommandExit(evidence *PreparedEvidence, id string, want int64) scriptValue {
	ref, ok := evidence.byID[id]
	if !ok || ref.kind != EvidenceCommand {
		return scriptUnknown()
	}
	return scriptObserved(evidence, id, evidence.set.Commands[ref.index].ExitCode == want)
}

func scriptCounter(evidence *PreparedEvidence, id string, want uint64, minimum bool) scriptValue {
	ref, ok := evidence.byID[id]
	if !ok || ref.kind != EvidenceCounter {
		return scriptUnknown()
	}
	value := evidence.set.Counters[ref.index].Value
	if minimum {
		return scriptObserved(evidence, id, value >= want)
	}
	return scriptObserved(evidence, id, value <= want)
}

func scriptUnknown() scriptValue {
	return scriptValue{presence: PresenceUnknown, citations: []Citation{}}
}

func scriptObserved(evidence *PreparedEvidence, id string, value bool) scriptValue {
	return scriptValue{presence: PresenceObserved, value: value, citations: []Citation{evidence.citation(id)}}
}

func combineScriptValues(left, right scriptValue, and bool) scriptValue {
	if left.presence != PresenceObserved || right.presence != PresenceObserved {
		return scriptUnknown()
	}
	value := left.value || right.value
	if and {
		value = left.value && right.value
	}
	citations := append(slices.Clone(left.citations), right.citations...)
	sort.Slice(citations, func(i, j int) bool { return citations[i].EvidenceID < citations[j].EvidenceID })
	citations = slices.CompactFunc(citations, func(left, right Citation) bool { return left == right })
	return scriptValue{presence: PresenceObserved, value: value, citations: citations}
}

func citationsMatchVisibility(evidence *PreparedEvidence, citations []Citation, visibility Visibility) bool {
	if len(citations) == 0 || len(citations) > MaxCitationsPerCheck {
		return false
	}
	for _, citation := range citations {
		ref, ok := evidence.byID[citation.EvidenceID]
		if !ok || ref.visibility != visibility || ref.digest != citation.SHA256 {
			return false
		}
	}
	return true
}
