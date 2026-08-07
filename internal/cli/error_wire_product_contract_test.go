package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type cliErrorWireExpectation struct {
	remediation string
	exitCodes   []int
}

type cliErrorWireFixture struct {
	SchemaVersion int `json:"schema_version"`
	Contracts     []struct {
		ExitCode    int    `json:"exit_code"`
		Kind        string `json:"kind"`
		Remediation string `json:"remediation"`
	} `json:"contracts"`
	Recovery cliErrorRecoveryWireFixture `json:"recovery"`
}

type cliErrorRecoveryWireFixture struct {
	SchemaVersion    int                  `json:"schema_version"`
	Members          []cliErrorWireMember `json:"members"`
	Actions          []string             `json:"actions"`
	NextCapabilities []string             `json:"next_capabilities"`
	ValidVectors     []json.RawMessage    `json:"valid_vectors"`
	InvalidVectors   []json.RawMessage    `json:"invalid_vectors"`
}

type cliErrorWireMember struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// TestCLIErrorWireProductContract is the lightweight product-side half of the
// evaluator compatibility gate. It proves every stable classification/exit
// triplet is reachable from product errors and independently inventories both
// source classification owners and the exit-code source. It does not import
// the evaluator or rely on its parser vocabulary.
func TestCLIErrorWireProductContract(t *testing.T) {
	expected := map[string]cliErrorWireExpectation{
		"api_error":             {"inspect_backend_error", []int{1}},
		"authentication_failed": {"reauthenticate", []int{3}},
		"check_failed":          {"review_failed_check", []int{8}},
		"configuration_error":   {"complete_configuration", []int{7}},
		"content_policy":        {"request_human_approval", []int{8}},
		"forbidden":             {"request_access", []int{6}},
		"internal_error":        {"report_bug", []int{8}},
		"not_found":             {"verify_identifier_or_access", []int{4}},
		"output_limit_exceeded": {"narrow_or_raise_bound", []int{1, 8}},
		"rate_limited":          {"wait_before_retry", []int{1}},
		"read_only_policy":      {"request_human_approval", []int{8}},
		"transport_error":       {"inspect_network_before_retry", []int{1}},
		"unexpected_error":      {"inspect_error", []int{1}},
		"usage_error":           {"fix_request", []int{2}},
		"version_conflict":      {"refresh_and_reapply", []int{5}},
	}
	assertCLIErrorWireFixture(t, expected)
	examples := []struct {
		name string
		err  error
	}{
		{"api", &httpx.APIError{Status: http.StatusServiceUnavailable}},
		{"authentication", fmt.Errorf("%w: x", domain.ErrAuth)},
		{"check", fmt.Errorf("%w: x", domain.ErrCheckFailed)},
		{"configuration", fmt.Errorf("%w: x", domain.ErrConfig)},
		{"content policy", &contentpolicy.DenialError{Reason: contentpolicy.ReasonExplicitDeny}},
		{"forbidden", fmt.Errorf("%w: x", domain.ErrForbidden)},
		{"internal", &accessPolicyInvariantError{Command: "atl future"}},
		{"not found", fmt.Errorf("%w: x", domain.ErrNotFound)},
		{"output limit generic", fmt.Errorf("%w: x", domain.ErrOutputLimit)},
		{"output limit check", fmt.Errorf("%w: %w", domain.ErrCheckFailed, domain.ErrOutputLimit)},
		{"rate limited", &httpx.APIError{Status: http.StatusTooManyRequests}},
		{"read only", &readOnlyPolicyError{Command: "atl jira push"}},
		{"transport", fmt.Errorf("x: %w", &httpx.TransportError{})},
		{"unexpected", errors.New("x")},
		{"usage", fmt.Errorf("%w: x", domain.ErrUsage)},
		{"version conflict", fmt.Errorf("%w: x", domain.ErrVersionConflict)},
	}
	covered := map[string]map[int]bool{}
	for _, example := range examples {
		t.Run(example.name, func(t *testing.T) {
			kind, remediation := classifyError(example.err)
			known, ok := expected[kind]
			if !ok || known.remediation != remediation {
				t.Fatalf("classification=%q/%q is outside the product contract", kind, remediation)
			}
			exitCode := codeFor(example.err)
			if !slices.Contains(known.exitCodes, exitCode) {
				t.Fatalf("triplet=%d/%q/%q is outside the product contract", exitCode, kind, remediation)
			}
			if covered[kind] == nil {
				covered[kind] = map[int]bool{}
			}
			covered[kind][exitCode] = true
		})
	}
	for kind, contract := range expected {
		for _, exitCode := range contract.exitCodes {
			if !covered[kind][exitCode] {
				t.Fatalf("product contract %d/%q/%q is not reachable", exitCode, kind, contract.remediation)
			}
		}
	}

	sourcePairs := productReturnedStringPairs(t, filepath.Join("..", "diagnostic", "error.go"), "Classify")
	for kind, remediation := range productReturnedStringPairs(t, "root.go", "classifyError") {
		if previous, exists := sourcePairs[kind]; exists && previous != remediation {
			t.Fatalf("source classification %q has two remediations: %q and %q", kind, previous, remediation)
		}
		sourcePairs[kind] = remediation
	}
	if len(sourcePairs) != len(expected) {
		t.Fatalf("source has %d classifications, product contract has %d", len(sourcePairs), len(expected))
	}
	for kind, remediation := range sourcePairs {
		known, exists := expected[kind]
		if !exists || known.remediation != remediation {
			t.Fatalf("source classification %q/%q is outside the product contract", kind, remediation)
		}
	}

	parsedRoot := productParseGoFile(t, "root.go")
	wantConstants := map[string]int{
		"exitOK": 0, "exitGeneric": 1, "exitUsage": 2, "exitAuth": 3, "exitNotFound": 4,
		"exitVersionConfl": 5, "exitForbidden": 6, "exitConfig": 7, "exitCheckFailed": 8,
	}
	if got := productIntegerConstants(parsedRoot, wantConstants); !equalProductStringIntMaps(got, wantConstants) {
		t.Fatalf("CLI exit constants=%v, want %v", got, wantConstants)
	}
	wantCases := []productSwitchReturn{
		{condition: "ErrAuth", result: "exitAuth"},
		{condition: "ErrNotFound", result: "exitNotFound"},
		{condition: "ErrVersionConflict", result: "exitVersionConfl"},
		{condition: "ErrForbidden", result: "exitForbidden"},
		{condition: "ErrConfig", result: "exitConfig"},
		{condition: "ErrCheckFailed", result: "exitCheckFailed"},
		{condition: "ErrUsage", result: "exitUsage"},
		{condition: "default", result: "exitGeneric"},
	}
	if got := productFunctionSwitchReturns(t, parsedRoot, "codeFor"); !slices.Equal(got, wantCases) {
		t.Fatalf("codeFor cases=%v, want %v", got, wantCases)
	}
}

func assertCLIErrorWireFixture(t *testing.T, expected map[string]cliErrorWireExpectation) {
	t.Helper()
	path := filepath.Join("..", "diagnostic", "testdata", "cli-error-wire.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fixture cliErrorWireFixture
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		t.Fatal("CLI error wire fixture contains trailing JSON")
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("CLI error wire fixture schema=%d, want 1", fixture.SchemaVersion)
	}
	wantContracts := 0
	for _, contract := range expected {
		wantContracts += len(contract.exitCodes)
	}
	if len(fixture.Contracts) != wantContracts {
		t.Fatalf("CLI error wire fixture has %d contracts, product has %d", len(fixture.Contracts), wantContracts)
	}
	previous := ""
	seen := map[string]bool{}
	for _, contract := range fixture.Contracts {
		known, ok := expected[contract.Kind]
		if !ok || known.remediation != contract.Remediation || !slices.Contains(known.exitCodes, contract.ExitCode) {
			t.Fatalf("versioned wire fixture triplet=%d/%q/%q is outside the product contract", contract.ExitCode, contract.Kind, contract.Remediation)
		}
		key := fmt.Sprintf("%s\x00%03d", contract.Kind, contract.ExitCode)
		if key <= previous || seen[key] {
			t.Fatalf("CLI error wire fixture is not strictly sorted and unique at %q", key)
		}
		previous, seen[key] = key, true
	}
	assertProductRecoveryWireFixture(t, fixture.Recovery)
}

func assertProductRecoveryWireFixture(t *testing.T, fixture cliErrorRecoveryWireFixture) {
	t.Helper()
	if fixture.SchemaVersion != diagnostic.RecoverySchemaVersion {
		t.Fatalf("recovery wire fixture schema=%d, product schema=%d", fixture.SchemaVersion, diagnostic.RecoverySchemaVersion)
	}
	if got := productCLIErrorJSONMembers(reflect.TypeFor[diagnostic.Recovery]()); !slices.Equal(got, fixture.Members) {
		t.Fatalf("product recovery members=%v, versioned wire fixture=%v", got, fixture.Members)
	}
	actions := productTypedStringConstants(t, filepath.Join("..", "diagnostic", "recovery.go"), "RecoveryAction")
	if !slices.Equal(actions, fixture.Actions) {
		t.Fatalf("product recovery actions=%v, versioned wire fixture=%v", actions, fixture.Actions)
	}
	capabilities := productTypedStringConstants(t, filepath.Join("..", "diagnostic", "recovery.go"), "NextCapability")
	if !slices.Equal(capabilities, fixture.NextCapabilities) {
		t.Fatalf("product recovery capabilities=%v, versioned wire fixture=%v", capabilities, fixture.NextCapabilities)
	}
	if len(fixture.ValidVectors) != 27 || len(fixture.InvalidVectors) != 30 {
		t.Fatalf("recovery semantic vectors=%d valid/%d invalid, want 27/30", len(fixture.ValidVectors), len(fixture.InvalidVectors))
	}
	for index, raw := range fixture.ValidVectors {
		if !productValidRecoveryJSON(raw) {
			t.Fatalf("product rejected valid recovery vector %d: %s", index, raw)
		}
	}
	for index, raw := range fixture.InvalidVectors {
		if productValidRecoveryJSON(raw) {
			t.Fatalf("product accepted invalid recovery vector %d: %s", index, raw)
		}
	}
}

func productCLIErrorJSONMembers(typ reflect.Type) []cliErrorWireMember {
	members := make([]cliErrorWireMember, 0, typ.NumField())
	for index := range typ.NumField() {
		tag := typ.Field(index).Tag.Get("json")
		parts := strings.Split(tag, ",")
		members = append(members, cliErrorWireMember{Name: parts[0], Required: !slices.Contains(parts[1:], "omitempty")})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].Name < members[right].Name })
	return members
}

type productRecoveryWire struct {
	SchemaVersion   int                               `json:"schema_version"`
	Action          diagnostic.RecoveryAction         `json:"action"`
	RetrySafe       *bool                             `json:"retry_safe"`
	NextCapability  diagnostic.NextCapability         `json:"next_capability,omitempty"`
	Requested       *int                              `json:"requested,omitempty"`
	Available       *int                              `json:"available,omitempty"`
	Matches         *int                              `json:"matches,omitempty"`
	ExpectedVersion *int                              `json:"expected_version,omitempty"`
	ObservedVersion *int                              `json:"observed_version,omitempty"`
	ExpectedForest  *diagnostic.RecoveryForestVersion `json:"expected_forest,omitempty"`
	ObservedForest  *diagnostic.RecoveryForestVersion `json:"observed_forest,omitempty"`
}

func productValidRecoveryJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire productRecoveryWire
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.RetrySafe == nil {
		return false
	}
	return diagnostic.ValidateRecovery(diagnostic.Recovery{
		SchemaVersion: wire.SchemaVersion, Action: wire.Action, RetrySafe: *wire.RetrySafe,
		NextCapability: wire.NextCapability, Requested: wire.Requested, Available: wire.Available,
		Matches: wire.Matches, ExpectedVersion: wire.ExpectedVersion, ObservedVersion: wire.ObservedVersion,
		ExpectedForest: wire.ExpectedForest, ObservedForest: wire.ObservedForest,
	})
}

func productTypedStringConstants(t *testing.T, file, typeName string) []string {
	t.Helper()
	parsed := productParseGoFile(t, file)
	var values []string
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			valueSpec := specification.(*ast.ValueSpec)
			identifier, ok := valueSpec.Type.(*ast.Ident)
			if !ok || identifier.Name != typeName || len(valueSpec.Values) != len(valueSpec.Names) {
				continue
			}
			for _, expression := range valueSpec.Values {
				value, ok := productStringLiteral(expression)
				if !ok {
					t.Fatalf("%s constant has a non-literal value", typeName)
				}
				values = append(values, value)
			}
		}
	}
	sort.Strings(values)
	return values
}

func productReturnedStringPairs(t *testing.T, file, function string) map[string]string {
	t.Helper()
	parsed := productParseGoFile(t, file)
	pairs := map[string]string{}
	for _, declaration := range parsed.Decls {
		functionDeclaration, ok := declaration.(*ast.FuncDecl)
		if !ok || functionDeclaration.Name.Name != function {
			continue
		}
		ast.Inspect(functionDeclaration.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			if len(statement.Results) == 1 && function == "classifyError" && productIsDiagnosticClassifyDelegation(statement.Results[0]) {
				return true
			}
			if len(statement.Results) != 2 {
				t.Fatalf("%s.%s contains an unexpected %d-result return", file, function, len(statement.Results))
			}
			kind, kindOK := productStringLiteral(statement.Results[0])
			remediation, remediationOK := productStringLiteral(statement.Results[1])
			if !kindOK || !remediationOK {
				t.Fatalf("%s.%s contains a non-literal two-result return", file, function)
			}
			if previous, exists := pairs[kind]; exists {
				t.Fatalf("%s.%s returns duplicate kind %q with remediations %q and %q", file, function, kind, previous, remediation)
			}
			pairs[kind] = remediation
			return true
		})
		return pairs
	}
	t.Fatalf("function %s not found in %s", function, file)
	return nil
}

func productIsDiagnosticClassifyDelegation(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	callee, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || callee.Sel.Name != "Classify" {
		return false
	}
	packageName, ok := callee.X.(*ast.Ident)
	argument, argumentOK := call.Args[0].(*ast.Ident)
	return ok && argumentOK && packageName.Name == "diagnostic" && argument.Name == "err"
}

func productParseGoFile(t *testing.T, file string) *ast.File {
	t.Helper()
	source, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func productStringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func productIntegerConstants(parsed *ast.File, wanted map[string]int) map[string]int {
	values := map[string]int{}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			valueSpec := specification.(*ast.ValueSpec)
			for index, name := range valueSpec.Names {
				if _, keep := wanted[name.Name]; !keep || index >= len(valueSpec.Values) {
					continue
				}
				literal, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					continue
				}
				value, err := strconv.Atoi(literal.Value)
				if err == nil {
					values[name.Name] = value
				}
			}
		}
	}
	return values
}

type productSwitchReturn struct {
	condition string
	result    string
}

func productFunctionSwitchReturns(t *testing.T, parsed *ast.File, function string) []productSwitchReturn {
	t.Helper()
	for _, declaration := range parsed.Decls {
		functionDeclaration, ok := declaration.(*ast.FuncDecl)
		if !ok || functionDeclaration.Name.Name != function {
			continue
		}
		if len(functionDeclaration.Body.List) != 1 {
			t.Fatalf("%s must contain exactly one statement, got %d", function, len(functionDeclaration.Body.List))
		}
		statement, ok := functionDeclaration.Body.List[0].(*ast.SwitchStmt)
		if !ok || statement.Tag != nil {
			t.Fatalf("%s must consist of one expressionless switch", function)
		}
		var returns []productSwitchReturn
		for _, item := range statement.Body.List {
			clause := item.(*ast.CaseClause)
			key := "default"
			switch len(clause.List) {
			case 0:
			case 1:
				call, ok := clause.List[0].(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					t.Fatalf("%s contains a non-errors.Is case", function)
				}
				callee, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || callee.Sel.Name != "Is" {
					t.Fatalf("%s contains a non-errors.Is case", function)
				}
				selector, ok := call.Args[1].(*ast.SelectorExpr)
				if !ok {
					t.Fatalf("%s contains an errors.Is case without a selector sentinel", function)
				}
				key = selector.Sel.Name
			default:
				t.Fatalf("%s contains a multi-expression case", function)
			}
			if len(clause.Body) != 1 {
				t.Fatalf("%s case %s has %d statements", function, key, len(clause.Body))
			}
			returnStatement, ok := clause.Body[0].(*ast.ReturnStmt)
			if !ok || len(returnStatement.Results) != 1 {
				t.Fatalf("%s case %s is not one direct return", function, key)
			}
			identifier, ok := returnStatement.Results[0].(*ast.Ident)
			if !ok {
				t.Fatalf("%s case %s does not return an exit identifier", function, key)
			}
			returns = append(returns, productSwitchReturn{condition: key, result: identifier.Name})
		}
		return returns
	}
	t.Fatalf("function %s not found", function)
	return nil
}

func equalProductStringIntMaps(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
