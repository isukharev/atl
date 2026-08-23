package atl

import (
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/agenteval/grading"
)

// LegacyGradingDescriptor classifies every ATL RunCheck spelling by the exact
// neutral check family used by the root compatibility adapter. The root owns
// the product-specific evidence projection; grading owns every pass decision.
type LegacyGradingDescriptor struct {
	Kind           string
	EvidenceFamily grading.CheckKind
}

var legacyGradingCatalog = []LegacyGradingDescriptor{
	{Kind: "atl_all_succeeded", EvidenceFamily: grading.CheckPolicy},
	{Kind: "atl_failures_equals", EvidenceFamily: grading.CheckBudget},
	{Kind: "atl_invocations_max", EvidenceFamily: grading.CheckBudget},
	{Kind: "atl_invocations_min", EvidenceFamily: grading.CheckBudget},
	{Kind: "capability_families_equal", EvidenceFamily: grading.CheckToolSequence},
	{Kind: "capability_sequence_equal", EvidenceFamily: grading.CheckToolSequence},
	{Kind: "cli_error_contracts_equal", EvidenceFamily: grading.CheckActionSequence},
	{Kind: "cli_exit_codes_equal", EvidenceFamily: grading.CheckActionSequence},
	{Kind: "delegations_min", EvidenceFamily: grading.CheckBudget},
	{Kind: "delegations_none", EvidenceFamily: grading.CheckPolicy},
	{Kind: "guard_no_denials", EvidenceFamily: grading.CheckPolicy},
	{Kind: "http_methods_equal", EvidenceFamily: grading.CheckActionSequence},
	{Kind: "http_methods_observed", EvidenceFamily: grading.CheckBudget},
	{Kind: "interface_all_succeeded", EvidenceFamily: grading.CheckPolicy},
	{Kind: "interface_failures_equals", EvidenceFamily: grading.CheckBudget},
	{Kind: "interface_invocations_max", EvidenceFamily: grading.CheckBudget},
	{Kind: "interface_invocations_min", EvidenceFamily: grading.CheckBudget},
	{Kind: "json_array_min_items", EvidenceFamily: grading.CheckJSONSchema},
	{Kind: "json_equals", EvidenceFamily: grading.CheckJSONValue},
	{Kind: "json_equals_proposal_hash_binding", EvidenceFamily: grading.CheckJSONValue},
	{Kind: "json_equals_workspace_json", EvidenceFamily: grading.CheckJSONValue},
	{Kind: "json_present", EvidenceFamily: grading.CheckFileExists},
	{Kind: "json_string_equals_optional_period", EvidenceFamily: grading.CheckActionSequence},
	{Kind: "mcp_invocations_equal", EvidenceFamily: grading.CheckToolSequence},
	{Kind: "mcp_invocations_multiset_equal", EvidenceFamily: grading.CheckToolSequence},
	{Kind: "mcp_route_one_of", EvidenceFamily: grading.CheckActionSequence},
	{Kind: "mock_no_unexpected", EvidenceFamily: grading.CheckPolicy},
	{Kind: "skill_invocations_min", EvidenceFamily: grading.CheckSkillUse},
	{Kind: "workspace_file_sha256", EvidenceFamily: grading.CheckFileSHA256},
}

func LegacyGradingCatalog() []LegacyGradingDescriptor {
	return slices.Clone(legacyGradingCatalog)
}

func LegacyGradingFamily(kind string) (grading.CheckKind, bool) {
	index, found := slices.BinarySearchFunc(legacyGradingCatalog, kind, func(item LegacyGradingDescriptor, target string) int {
		return strings.Compare(item.Kind, target)
	})
	if !found {
		return "", false
	}
	return legacyGradingCatalog[index].EvidenceFamily, true
}
