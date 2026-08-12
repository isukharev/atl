package agenteval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/isukharev/atl/internal/agenteval/grading"
)

const (
	atlGradingMaxCounter = ^uint64(0)
)

type atlGradingObservation struct {
	final                      []byte
	workspace                  string
	atlInvocations             int
	failedATL                  int
	unexpectedRequests         int
	skillInvocations           int
	skillInvocationsByName     map[string]int
	delegations                int
	guardDenials               int
	httpMethods                map[string]int
	httpMethodsObserved        bool
	cliExitCodes               []int
	capabilityFamilies         []CapabilityFamilyMetric
	capabilityFamiliesObserved bool
	capabilitySequence         []string
	mcpInvocations             []MCPInvocation
	mcpInvocationsObserved     bool
	cliErrorContracts          []CLIErrorContract
}

type atlGradingEvaluation struct {
	checks  map[string]bool
	plan    grading.Plan
	receipt grading.Receipt
}

func newATLGradingPlan(checks []RunCheck, _ string, inputProjectionSHA256 string) (grading.Plan, error) {
	contract, err := grading.BuiltinContract()
	if err != nil {
		return grading.Plan{}, fmt.Errorf("build ATL grader contract: %w", err)
	}
	contractSHA, err := grading.ContractSHA256(contract)
	if err != nil {
		return grading.Plan{}, fmt.Errorf("digest ATL grader contract: %w", err)
	}
	converted := make([]grading.Check, len(checks))
	for index, check := range checks {
		converted[index], err = atlGradingCheck(check, atlGradingEvidenceID(index))
		if err != nil {
			return grading.Plan{}, fmt.Errorf("map ATL check %q: %w", check.Name, err)
		}
	}
	sort.Slice(converted, func(i, j int) bool { return converted[i].ID < converted[j].ID })
	if inputProjectionSHA256 == "" {
		inputProjectionSHA256, err = contentMinimizedAttemptDigest("atl-grading-input", converted)
		if err != nil {
			return grading.Plan{}, err
		}
	}
	environmentSHA256, err := contentMinimizedAttemptDigest("atl-grading-environment", "builtin-atl-grading-adapter-v1")
	if err != nil {
		return grading.Plan{}, err
	}
	plan := grading.Plan{
		Schema: grading.PlanSchema, SchemaVersion: grading.SchemaVersion, ContractVersion: grading.ContractVersion,
		ContractSHA256: contractSHA, Mode: grading.ModeDeterministic, InputProjectionSHA256: inputProjectionSHA256,
		EnvironmentSHA256: environmentSHA256, Checks: converted,
		Limits: grading.PlanLimits{DeadlineMillis: grading.MaxDurationMillis, MaxInputBytes: grading.MaxEvidenceBytes,
			MaxOutputBytes: grading.MaxReceiptBytes},
	}
	if _, err := grading.Admit(contract, plan); err != nil {
		return grading.Plan{}, fmt.Errorf("admit ATL grading plan: %w", err)
	}
	return plan, nil
}

func atlGradingCheck(check RunCheck, evidenceID string) (grading.Check, error) {
	result := grading.Check{ID: atlGradingCheckID(check.Name), Visibility: grading.VisibilityHidden}
	budget := func(minimum, maximum uint64) grading.Check {
		result.Kind = grading.CheckBudget
		result.Budget = &grading.BudgetRule{EvidenceID: evidenceID, Minimum: minimum, Maximum: maximum}
		return result
	}
	policy := func(maximum uint64) grading.Check {
		result.Kind = grading.CheckPolicy
		result.Policy = &grading.PolicyRule{EvidenceID: evidenceID, MaximumViolations: maximum}
		return result
	}
	sequence := func(kind grading.CheckKind, expected []string, alternatives [][]string) grading.Check {
		rule := &grading.SequenceRule{EvidenceID: evidenceID, Expected: []string{atlSequenceSHA256(expected)},
			Alternatives: cloneATLSequenceAlternatives(alternatives), MinimumSimilarityBPS: 10_000}
		if alternatives != nil {
			rule.Expected = []string{}
			for index := range rule.Alternatives {
				rule.Alternatives[index] = []string{atlSequenceSHA256(rule.Alternatives[index])}
			}
			sort.Slice(rule.Alternatives, func(i, j int) bool { return slices.Compare(rule.Alternatives[i], rule.Alternatives[j]) < 0 })
		}
		result.Kind = kind
		if kind == grading.CheckToolSequence {
			result.ToolSequence = rule
		} else {
			result.ActionSequence = rule
		}
		return result
	}
	switch check.Kind {
	case "atl_invocations_min", "interface_invocations_min", "delegations_min":
		minimum, err := atlGradingCounter(check.Minimum)
		return budget(minimum, atlGradingMaxCounter), err
	case "skill_invocations_min":
		minimum, err := atlGradingCounter(check.Minimum)
		if err != nil {
			return grading.Check{}, err
		}
		result.Kind = grading.CheckSkillUse
		result.SkillUse = &grading.CountRule{EvidenceID: evidenceID, Minimum: minimum, Maximum: atlGradingMaxCounter}
		return result, nil
	case "atl_invocations_max", "interface_invocations_max":
		maximum, err := atlGradingCounter(check.Maximum)
		return budget(0, maximum), err
	case "atl_all_succeeded", "interface_all_succeeded", "mock_no_unexpected", "guard_no_denials", "delegations_none":
		return policy(0), nil
	case "atl_failures_equals", "interface_failures_equals":
		expected, ok := expectedATLFailureCount(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid expected failures")
		}
		exact, err := atlGradingCounter(expected)
		return budget(exact, exact), err
	case "http_methods_observed":
		return budget(1, 1), nil
	case "http_methods_equal":
		expected, ok := expectedHTTPMethods(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid HTTP method expectation")
		}
		return sequence(grading.CheckActionSequence, atlHTTPMethodSequence(expected), nil), nil
	case "capability_families_equal":
		expected, ok := expectedCapabilityFamilies(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid capability expectation")
		}
		return sequence(grading.CheckToolSequence, atlCapabilityExpectationSequence(expected), nil), nil
	case "capability_sequence_equal":
		expected, ok := expectedCapabilitySequence(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid capability sequence")
		}
		return sequence(grading.CheckToolSequence, expected, nil), nil
	case "cli_exit_codes_equal":
		expected, ok := expectedCLIExitCodes(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid CLI exit-code expectation")
		}
		return sequence(grading.CheckActionSequence, atlIntegerSequence(expected), nil), nil
	case "cli_error_contracts_equal":
		expected, ok := expectedCLIErrorContracts(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid CLI error expectation")
		}
		return sequence(grading.CheckActionSequence, atlCLIErrorSequence(expected), nil), nil
	case "mcp_invocations_equal", "mcp_invocations_multiset_equal":
		expected, ok := expectedMCPInvocations(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid MCP invocation expectation")
		}
		values := atlMCPInvocationSequence(expected)
		if check.Kind == "mcp_invocations_multiset_equal" {
			slices.Sort(values)
		}
		return sequence(grading.CheckToolSequence, values, nil), nil
	case "mcp_route_one_of":
		expected, ok := expectedMCPRouteAlternatives(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid MCP route expectation")
		}
		alternatives := make([][]string, len(expected))
		for index, alternative := range expected {
			alternatives[index] = atlMCPRouteSequence(alternative.HTTPMethods, alternative.Invocations)
		}
		return sequence(grading.CheckActionSequence, nil, alternatives), nil
	case "json_equals":
		expected, err := atlLegacyCanonicalJSON(check.Expected)
		if err != nil {
			return grading.Check{}, err
		}
		expected, err = json.Marshal(sha256HexBytes(expected))
		if err != nil {
			return grading.Check{}, fmt.Errorf("encode content-addressed JSON expectation: %w", err)
		}
		result.Kind = grading.CheckJSONValue
		result.JSONValue = &grading.JSONValueRule{EvidenceID: evidenceID, Pointer: "", Expected: expected}
		return result, nil
	case "json_present":
		result.Kind = grading.CheckFileExists
		result.FileExists = &grading.FileExistsRule{EvidenceID: evidenceID, Expected: true}
		return result, nil
	case "json_array_min_items":
		minimum, err := atlGradingMinimumItems(check.Minimum)
		if err != nil {
			return grading.Check{}, err
		}
		result.Kind = grading.CheckJSONSchema
		result.JSONSchema = &grading.JSONSchemaRule{EvidenceID: evidenceID,
			Fields: []grading.JSONField{{Pointer: "", Type: grading.JSONTypeArray, Required: true,
				MinimumItems: minimum}}}
		return result, nil
	case "json_string_equals_optional_period":
		expected, ok := expectedOptionalPeriodString(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid optional-period expectation")
		}
		return sequence(grading.CheckActionSequence, nil, [][]string{{expected}, {expected + "."}}), nil
	case "json_equals_workspace_json":
		expectation, ok := workspaceJSONExpectationFrom(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid workspace JSON expectation")
		}
		predicateSHA256, err := atlWorkspaceJSONPredicateSHA256(check, expectation)
		if err != nil {
			return grading.Check{}, fmt.Errorf("bind workspace JSON predicate: %w", err)
		}
		expected, err := json.Marshal(predicateSHA256)
		if err != nil {
			return grading.Check{}, fmt.Errorf("encode workspace JSON predicate: %w", err)
		}
		// The referenced workspace file may be created during the attempt, after
		// the grading plan is admitted. Bind its exact path/pointer predicate now,
		// then project the comparison result under that predicate at evaluation.
		result.Kind = grading.CheckJSONValue
		result.JSONValue = &grading.JSONValueRule{EvidenceID: evidenceID, Pointer: "", Expected: expected}
		return result, nil
	case "workspace_file_sha256":
		expectation, ok := workspaceFileSHA256ExpectationFrom(check.Expected)
		if !ok {
			return grading.Check{}, fmt.Errorf("invalid workspace file expectation")
		}
		result.Kind = grading.CheckFileSHA256
		result.FileSHA256 = &grading.FileSHA256Rule{EvidenceID: evidenceID,
			ExpectedSHA256: sha256HexBytes([]byte(expectation.SHA256))}
		return result, nil
	default:
		return grading.Check{}, fmt.Errorf("unsupported ATL check kind %q", check.Kind)
	}
}

func evaluateATLChecksWithPlan(ctx context.Context, plan grading.Plan, checks []RunCheck, observed atlGradingObservation) (atlGradingEvaluation, error) {
	contract, err := grading.BuiltinContract()
	if err != nil {
		return atlGradingEvaluation{}, err
	}
	admitted, err := grading.Admit(contract, plan)
	if err != nil {
		return atlGradingEvaluation{}, fmt.Errorf("admit bound ATL grading plan: %w", err)
	}
	evidence, err := atlGradingEvidence(checks, plan, observed)
	if err != nil {
		return atlGradingEvaluation{}, err
	}
	prepared, err := grading.PrepareEvidence(ctx, admitted, evidence)
	if err != nil {
		return atlGradingEvaluation{}, fmt.Errorf("prepare ATL grading evidence: %w", err)
	}
	defer prepared.Destroy()
	receipt, err := grading.EvaluateDeterministic(ctx, admitted, prepared)
	if err != nil {
		return atlGradingEvaluation{}, fmt.Errorf("evaluate ATL grading plan: %w", err)
	}
	legacyByCheckID := make(map[string]string, len(checks))
	for _, check := range checks {
		legacyByCheckID[atlGradingCheckID(check.Name)] = check.Name
	}
	results := make(map[string]bool, len(receipt.Decisions))
	for _, decision := range receipt.Decisions {
		legacyName, found := legacyByCheckID[decision.CheckID]
		if !found {
			return atlGradingEvaluation{}, fmt.Errorf("unbound ATL grading decision %q", decision.CheckID)
		}
		results[legacyName] = decision.Presence == grading.PresenceObserved && decision.Passed
	}
	return atlGradingEvaluation{checks: results, plan: plan, receipt: receipt}, nil
}

func atlGradingEvidence(checks []RunCheck, plan grading.Plan, observed atlGradingObservation) (grading.EvidenceSet, error) {
	var document any
	if err := json.Unmarshal(observed.final, &document); err != nil {
		return grading.EvidenceSet{}, fmt.Errorf("decode structured final response: %w", err)
	}
	var err error
	evidence := grading.EvidenceSet{InputProjectionSHA256: plan.InputProjectionSHA256,
		Files:    []grading.FileEvidence{},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{},
		Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{}}
	for index, check := range checks {
		evidenceID := atlGradingEvidenceID(index)
		addCounter := func(value int) error {
			if value < 0 || uint64(value) > atlGradingMaxCounter {
				return fmt.Errorf("ATL counter %q is out of range", check.Name)
			}
			evidence.Counters = append(evidence.Counters, grading.CounterEvidence{ID: evidenceID,
				Visibility: grading.VisibilityHidden, Value: uint64(value)})
			return nil
		}
		addSequence := func(values []string) {
			if values == nil {
				values = []string{}
			}
			evidence.Sequences = append(evidence.Sequences, grading.SequenceEvidence{ID: evidenceID,
				Visibility: grading.VisibilityHidden, Values: []string{atlSequenceSHA256(values)}})
		}
		selectLegacyJSON := func() (any, bool) {
			return resolveJSONPointer(document, check.Pointer)
		}
		addSelectedJSONDigest := func() error {
			file := grading.FileEvidence{ID: evidenceID, Visibility: grading.VisibilityHidden}
			selected, found := selectLegacyJSON()
			if found {
				data, marshalErr := json.Marshal(selected)
				if marshalErr != nil {
					return marshalErr
				}
				digestData, marshalErr := json.Marshal(sha256HexBytes(data))
				if marshalErr != nil {
					return marshalErr
				}
				file.Present, file.Mode, file.Data = true, 0o600, digestData
			}
			evidence.Files = append(evidence.Files, file)
			return nil
		}
		switch check.Kind {
		case "atl_invocations_min", "interface_invocations_min", "atl_invocations_max", "interface_invocations_max":
			err = addCounter(observed.atlInvocations)
		case "atl_all_succeeded", "interface_all_succeeded", "atl_failures_equals", "interface_failures_equals":
			err = addCounter(observed.failedATL)
		case "mock_no_unexpected":
			err = addCounter(observed.unexpectedRequests)
		case "delegations_min", "delegations_none":
			err = addCounter(observed.delegations)
		case "guard_no_denials":
			err = addCounter(observed.guardDenials)
		case "skill_invocations_min":
			target, _ := skillInvocationTarget(check.Expected)
			value := observed.skillInvocations
			if target != "" {
				value = observed.skillInvocationsByName[target]
			}
			err = addCounter(value)
		case "http_methods_observed":
			if observed.httpMethodsObserved {
				err = addCounter(1)
			} else {
				err = addCounter(0)
			}
		case "http_methods_equal":
			if observed.httpMethodsObserved {
				addSequence(atlHTTPMethodSequence(observed.httpMethods))
			}
		case "capability_families_equal":
			if observed.capabilityFamiliesObserved {
				normalized, normalizeErr := normalizeCapabilityFamilies(observed.capabilityFamilies)
				if normalizeErr == nil {
					addSequence(atlCapabilityMetricSequence(normalized))
				}
			}
		case "capability_sequence_equal":
			if observed.capabilityFamiliesObserved {
				addSequence(observed.capabilitySequence)
			}
		case "cli_exit_codes_equal":
			addSequence(atlIntegerSequence(observed.cliExitCodes))
		case "cli_error_contracts_equal":
			addSequence(atlCLIErrorSequence(observed.cliErrorContracts))
		case "mcp_invocations_equal":
			if observed.mcpInvocationsObserved {
				addSequence(atlMCPInvocationSequence(observed.mcpInvocations))
			}
		case "mcp_invocations_multiset_equal":
			if observed.mcpInvocationsObserved {
				values := atlMCPInvocationSequence(observed.mcpInvocations)
				slices.Sort(values)
				addSequence(values)
			}
		case "mcp_route_one_of":
			if observed.httpMethodsObserved && observed.mcpInvocationsObserved {
				addSequence(atlMCPRouteSequence(observed.httpMethods, observed.mcpInvocations))
			}
		case "json_string_equals_optional_period":
			actual, found := resolveJSONPointer(document, check.Pointer)
			value, stringValue := actual.(string)
			if found && stringValue {
				addSequence([]string{value})
			} else {
				addSequence([]string{})
			}
		case "json_equals_workspace_json":
			expectation, _ := workspaceJSONExpectationFrom(check.Expected)
			actual, actualFound := selectLegacyJSON()
			expected, expectedFound := readWorkspaceJSONPointer(observed.workspace, expectation)
			predicateSHA256, predicateErr := atlWorkspaceJSONPredicateSHA256(check, expectation)
			if predicateErr != nil {
				return grading.EvidenceSet{}, fmt.Errorf("bind workspace JSON predicate: %w", predicateErr)
			}
			file := grading.FileEvidence{ID: evidenceID, Visibility: grading.VisibilityHidden,
				Present: true, Mode: 0o600, Data: []byte(`"mismatch"`)}
			if actualFound && expectedFound {
				actualData, actualErr := json.Marshal(actual)
				expectedData, expectedErr := json.Marshal(expected)
				if actualErr != nil || expectedErr != nil {
					return grading.EvidenceSet{}, fmt.Errorf("canonicalize workspace JSON comparison")
				}
				if bytes.Equal(actualData, expectedData) {
					file.Data, err = json.Marshal(predicateSHA256)
				}
			}
			evidence.Files = append(evidence.Files, file)
		case "workspace_file_sha256":
			expectation, _ := workspaceFileSHA256ExpectationFrom(check.Expected)
			file := grading.FileEvidence{ID: evidenceID, Visibility: grading.VisibilityHidden}
			target := filepath.Join(observed.workspace, filepath.FromSlash(expectation.Path))
			if info, statErr := hardenedStatWithin(observed.workspace, target); statErr == nil && info.Mode().IsRegular() &&
				info.Size() <= maxWorkspaceArtifactBytes {
				if data, readErr := hardenedReadFileWithinLimit(observed.workspace, target, maxWorkspaceArtifactBytes); readErr == nil {
					file.Present, file.Mode, file.Data = true, 0o600, []byte(sha256HexBytes(data))
				}
			}
			evidence.Files = append(evidence.Files, file)
		case "json_equals":
			err = addSelectedJSONDigest()
		case "json_present":
			_, found := selectLegacyJSON()
			file := grading.FileEvidence{ID: evidenceID, Visibility: grading.VisibilityHidden, Present: found}
			if found {
				file.Mode = 0o600
			}
			evidence.Files = append(evidence.Files, file)
		case "json_array_min_items":
			selected, found := selectLegacyJSON()
			file := grading.FileEvidence{ID: evidenceID, Visibility: grading.VisibilityHidden}
			if found {
				projected := any(nil)
				if values, array := selected.([]any); array {
					length := min(len(values), check.Minimum)
					projected = make([]any, length)
				}
				data, marshalErr := json.Marshal(projected)
				if marshalErr != nil {
					return grading.EvidenceSet{}, marshalErr
				}
				file.Present, file.Mode, file.Data = true, 0o600, data
			}
			evidence.Files = append(evidence.Files, file)
		default:
			return grading.EvidenceSet{}, fmt.Errorf("unsupported ATL check kind %q", check.Kind)
		}
		if err != nil {
			return grading.EvidenceSet{}, err
		}
	}
	sort.Slice(evidence.Files, func(i, j int) bool { return evidence.Files[i].ID < evidence.Files[j].ID })
	sort.Slice(evidence.Sequences, func(i, j int) bool { return evidence.Sequences[i].ID < evidence.Sequences[j].ID })
	sort.Slice(evidence.Counters, func(i, j int) bool { return evidence.Counters[i].ID < evidence.Counters[j].ID })
	return evidence, nil
}

func atlLegacyCanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode legacy JSON expectation: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize legacy JSON expectation: %w", err)
	}
	return canonical, nil
}

func atlGradingEvidenceID(index int) string { return fmt.Sprintf("atl-evidence-%03d", index+1) }

func atlGradingCheckID(name string) string { return "atl-check-" + sha256HexBytes([]byte(name)) }

func atlWorkspaceJSONPredicateSHA256(check RunCheck, expectation workspaceJSONExpectation) (string, error) {
	return contentMinimizedAttemptDigest("atl-workspace-json-predicate", struct {
		FinalPointer     string `json:"final_pointer"`
		WorkspacePath    string `json:"workspace_path"`
		WorkspacePointer string `json:"workspace_pointer"`
	}{check.Pointer, expectation.Path, expectation.Pointer})
}

func atlSequenceSHA256(values []string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("atl-grading-sequence-v1\x00"))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(len(values)))
	_, _ = digest.Write(encoded[:])
	for _, value := range values {
		binary.BigEndian.PutUint64(encoded[:], uint64(len(value)))
		_, _ = digest.Write(encoded[:])
		_, _ = digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func atlGradingCounter(value int) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("ATL grading counter is out of range")
	}
	converted := uint64(value) // #nosec G115 -- the negative guard above makes this widening conversion exact.
	if converted > atlGradingMaxCounter {
		return 0, fmt.Errorf("ATL grading counter is out of range")
	}
	return converted, nil
}

func atlGradingMinimumItems(value int) (uint32, error) {
	if value < 0 {
		return 0, fmt.Errorf("ATL grading item bound is out of range")
	}
	converted := uint64(value) // #nosec G115 -- the negative guard above makes this widening conversion exact.
	if converted > uint64(grading.MaxEvidenceItems) {
		return 0, fmt.Errorf("ATL grading item bound is out of range")
	}
	return uint32(converted), nil // #nosec G115 -- the bound above makes this narrowing conversion exact.
}

func atlPlanCheck(plan grading.Plan, id string) (grading.Check, bool) {
	id = atlGradingCheckID(id)
	index, found := slices.BinarySearchFunc(plan.Checks, id, func(check grading.Check, target string) int {
		if check.ID < target {
			return -1
		}
		if check.ID > target {
			return 1
		}
		return 0
	})
	if !found {
		return grading.Check{}, false
	}
	return plan.Checks[index], true
}

func atlHTTPMethodSequence(methods map[string]int) []string {
	result := make([]string, 0, len(methods))
	for method, count := range methods {
		result = append(result, method+"="+strconv.Itoa(count))
	}
	slices.Sort(result)
	return result
}

func atlCapabilityExpectationSequence(values []capabilityFamilyExpectation) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = fmt.Sprintf("%s=%d/%d/%d", value.Family, value.Invocations, value.Successes, value.Failures)
	}
	return result
}

func atlCapabilityMetricSequence(values []CapabilityFamilyMetric) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = fmt.Sprintf("%s=%d/%d/%d", value.Family, value.Invocations, value.Successes, value.Failures)
	}
	return result
}

func atlIntegerSequence(values []int) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strconv.Itoa(value)
	}
	return result
}

func atlCLIErrorSequence(values []CLIErrorContract) []string {
	result := make([]string, len(values))
	for index, value := range values {
		data, _ := json.Marshal(value)
		result[index] = sha256HexBytes(data)
	}
	return result
}

func atlMCPInvocationSequence(values []MCPInvocation) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = sha256HexBytes([]byte(mcpInvocationKey(value)))
	}
	return result
}

func atlMCPRouteSequence(methods map[string]int, invocations []MCPInvocation) []string {
	methodValues := atlHTTPMethodSequence(methods)
	result := make([]string, 0, len(methodValues)+len(invocations)+1)
	for _, value := range methodValues {
		result = append(result, "http/"+value)
	}
	result = append(result, "route/mcp")
	for _, value := range atlMCPInvocationSequence(invocations) {
		result = append(result, "mcp/"+value)
	}
	return result
}

func cloneATLSequenceAlternatives(values [][]string) [][]string {
	if values == nil {
		return nil
	}
	result := make([][]string, len(values))
	for index := range values {
		result[index] = slices.Clone(values[index])
	}
	return result
}
