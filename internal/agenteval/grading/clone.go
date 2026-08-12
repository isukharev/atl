package grading

import "slices"

func cloneContract(value Contract) Contract {
	value.Modes = slices.Clone(value.Modes)
	value.Capabilities = slices.Clone(value.Capabilities)
	return value
}

func clonePlan(value Plan) Plan {
	checks := value.Checks
	value.Checks = make([]Check, len(checks))
	for index := range checks {
		value.Checks[index] = cloneCheck(checks[index])
	}
	script := value.Script
	if script != nil {
		value.Script = make([]ScriptInstruction, len(script))
		for index := range script {
			value.Script[index] = script[index]
			value.Script[index].ExpectedJSON = slices.Clone(script[index].ExpectedJSON)
			if script[index].Integer != nil {
				integer := *script[index].Integer
				value.Script[index].Integer = &integer
			}
			if script[index].Unsigned != nil {
				unsigned := *script[index].Unsigned
				value.Script[index].Unsigned = &unsigned
			}
		}
	}
	if value.Judge != nil {
		judge := *value.Judge
		judge.Reviewers = slices.Clone(value.Judge.Reviewers)
		value.Judge = &judge
	}
	return value
}

func cloneCheck(value Check) Check {
	if value.FileExists != nil {
		copy := *value.FileExists
		value.FileExists = &copy
	}
	if value.FileMetadata != nil {
		copy := *value.FileMetadata
		value.FileMetadata = &copy
	}
	if value.FileSHA256 != nil {
		copy := *value.FileSHA256
		value.FileSHA256 = &copy
	}
	if value.JSONValue != nil {
		copy := *value.JSONValue
		copy.Expected = slices.Clone(copy.Expected)
		value.JSONValue = &copy
	}
	if value.JSONSchema != nil {
		copy := *value.JSONSchema
		copy.Fields = slices.Clone(copy.Fields)
		value.JSONSchema = &copy
	}
	if value.CommandExit != nil {
		copy := *value.CommandExit
		value.CommandExit = &copy
	}
	if value.CommandOutput != nil {
		copy := *value.CommandOutput
		value.CommandOutput = &copy
	}
	if value.TreeDiff != nil {
		copy := *value.TreeDiff
		copy.Expected = slices.Clone(copy.Expected)
		value.TreeDiff = &copy
	}
	if value.ToolSequence != nil {
		copy := *value.ToolSequence
		copy.Expected = slices.Clone(copy.Expected)
		copy.Alternatives = cloneSequenceAlternatives(copy.Alternatives)
		value.ToolSequence = &copy
	}
	if value.ActionSequence != nil {
		copy := *value.ActionSequence
		copy.Expected = slices.Clone(copy.Expected)
		copy.Alternatives = cloneSequenceAlternatives(copy.Alternatives)
		value.ActionSequence = &copy
	}
	if value.SkillActivation != nil {
		copy := *value.SkillActivation
		value.SkillActivation = &copy
	}
	if value.SkillUse != nil {
		copy := *value.SkillUse
		value.SkillUse = &copy
	}
	if value.Budget != nil {
		copy := *value.Budget
		value.Budget = &copy
	}
	if value.Policy != nil {
		copy := *value.Policy
		value.Policy = &copy
	}
	if value.Qualitative != nil {
		copy := *value.Qualitative
		copy.EvidenceIDs = slices.Clone(copy.EvidenceIDs)
		value.Qualitative = &copy
	}
	return value
}

func cloneSequenceAlternatives(value [][]string) [][]string {
	if value == nil {
		return nil
	}
	result := make([][]string, len(value))
	for index := range value {
		result[index] = slices.Clone(value[index])
	}
	return result
}

func cloneReview(value Review) Review {
	decisions := value.Decisions
	value.Decisions = make([]ReviewDecision, len(decisions))
	for index, decision := range decisions {
		value.Decisions[index] = decision
		value.Decisions[index].Citations = slices.Clone(decision.Citations)
	}
	return value
}

func cloneReceipt(value Receipt) Receipt {
	value.Evidence = slices.Clone(value.Evidence)
	decisions := value.Decisions
	value.Decisions = make([]Decision, len(decisions))
	for index, decision := range decisions {
		value.Decisions[index] = decision
		value.Decisions[index].Citations = slices.Clone(decision.Citations)
	}
	value.Reviewers = slices.Clone(value.Reviewers)
	value.Disagreements = slices.Clone(value.Disagreements)
	return value
}
