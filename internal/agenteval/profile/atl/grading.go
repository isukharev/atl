package atl

import (
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/agenteval/grading"
)

// LegacyGradingDescriptor binds every ATL RunCheck spelling to one neutral
// deterministic grading family. It is a compatibility projection: ATL keeps
// its historical JSON bytes while the generic contract owns grading authority.
type LegacyGradingDescriptor struct {
	Kind   string
	Family grading.CheckKind
}

var legacyGradingCatalog = []LegacyGradingDescriptor{
	{Kind: "atl_all_succeeded", Family: grading.CheckPolicy},
	{Kind: "atl_failures_equals", Family: grading.CheckPolicy},
	{Kind: "atl_invocations_max", Family: grading.CheckBudget},
	{Kind: "atl_invocations_min", Family: grading.CheckBudget},
	{Kind: "capability_families_equal", Family: grading.CheckToolSequence},
	{Kind: "capability_sequence_equal", Family: grading.CheckToolSequence},
	{Kind: "cli_error_contracts_equal", Family: grading.CheckCommandOutput},
	{Kind: "cli_exit_codes_equal", Family: grading.CheckCommandExit},
	{Kind: "delegations_min", Family: grading.CheckBudget},
	{Kind: "delegations_none", Family: grading.CheckPolicy},
	{Kind: "guard_no_denials", Family: grading.CheckPolicy},
	{Kind: "http_methods_equal", Family: grading.CheckActionSequence},
	{Kind: "http_methods_observed", Family: grading.CheckActionSequence},
	{Kind: "interface_all_succeeded", Family: grading.CheckPolicy},
	{Kind: "interface_failures_equals", Family: grading.CheckPolicy},
	{Kind: "interface_invocations_max", Family: grading.CheckBudget},
	{Kind: "interface_invocations_min", Family: grading.CheckBudget},
	{Kind: "json_array_min_items", Family: grading.CheckJSONSchema},
	{Kind: "json_equals", Family: grading.CheckJSONValue},
	{Kind: "json_equals_workspace_json", Family: grading.CheckJSONValue},
	{Kind: "json_present", Family: grading.CheckJSONSchema},
	{Kind: "json_string_equals_optional_period", Family: grading.CheckJSONValue},
	{Kind: "mcp_invocations_equal", Family: grading.CheckToolSequence},
	{Kind: "mcp_invocations_multiset_equal", Family: grading.CheckToolSequence},
	{Kind: "mcp_route_one_of", Family: grading.CheckActionSequence},
	{Kind: "mock_no_unexpected", Family: grading.CheckPolicy},
	{Kind: "skill_invocations_min", Family: grading.CheckSkillUse},
	{Kind: "workspace_file_sha256", Family: grading.CheckFileSHA256},
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
	return legacyGradingCatalog[index].Family, true
}
