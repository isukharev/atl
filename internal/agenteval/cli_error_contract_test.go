package agenteval

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestParseCLIErrorContractAdmitsOnlyTypedFailedCLIErrors(t *testing.T) {
	typed := `{"error":"page not found: private-page-title","code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}`
	typedWithRecovery := `{"error":"page not found: private-page-title","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"adjust_request","retry_safe":false}}`
	policy := `{"error":"blocked by read-only policy","code":8,"kind":"read_only_policy","remediation":"request_human_approval","policy":"read_only","command":"atl jira push"}`
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		want     CLIErrorContract
	}{
		{
			name: "typed failure after unrelated stderr", exitCode: 4,
			stderr: "warning: mirror view not regenerated\n" + typed + "\n",
			want:   CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		},
		{
			name: "typed failure with validated recovery", exitCode: 4, stderr: typedWithRecovery,
			want: CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		},
		{
			name: "read-only policy refusal", exitCode: 8, stderr: policy,
			want: CLIErrorContract{ExitCode: 8, Kind: "read_only_policy", Remediation: "request_human_approval"},
		},
		{name: "successful invocation", exitCode: 0, stderr: typed},
		{name: "empty capture", exitCode: 4, stderr: ""},
		{name: "blank capture", exitCode: 4, stderr: "\n \n\t\n"},
		{name: "text output", exitCode: 4, stderr: "error: page not found: private-page-title\n"},
		{name: "truncated object", exitCode: 4, stderr: typed[:len(typed)-12]},
		{name: "trailing data", exitCode: 4, stderr: typed + " {}"},
		{name: "not an object", exitCode: 4, stderr: "[4]"},
		{name: "code disagrees with audited exit", exitCode: 5, stderr: typed},
		{
			name: "unknown kind", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"backend_said_no","remediation":"verify_identifier_or_access"}`,
		},
		{
			name: "mismatched remediation", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"reauthenticate"}`,
		},
		{name: "missing remediation", exitCode: 4, stderr: `{"error":"x","code":4,"kind":"not_found"}`},
		{name: "missing message", exitCode: 4, stderr: `{"code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}`},
		{
			name: "unknown member", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","hint":"private-page-title"}`,
		},
		{
			name: "unknown recovery member", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"adjust_request","retry_safe":false,"private_hint":7}}`,
		},
		{
			name: "null recovery is present but invalid", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":null}`,
		},
		{
			name: "unknown recovery action", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"retry_private_target","retry_safe":false}}`,
		},
		{
			name: "unsafe exact retry", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"adjust_request","retry_safe":true}}`,
		},
		{
			name: "policy member without its kind", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","policy":"read_only","command":"atl jira push"}`,
		},
		{
			name: "oversized capture", exitCode: 4,
			stderr: strings.Repeat("x\n", maxCLIErrorContractStderrBytes) + typed,
		},
		{
			name: "oversized line", exitCode: 4,
			stderr: `{"error":"` + strings.Repeat("x", maxCLIErrorContractLineBytes) + `","code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, classified := ParseCLIErrorContract(test.exitCode, []byte(test.stderr))
			wantClassified := test.want != CLIErrorContract{}
			if classified != wantClassified || contract != test.want {
				t.Fatalf("contract=%+v classified=%v, want %+v/%v", contract, classified, test.want, wantClassified)
			}
			encoded, err := json.Marshal(contract)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"private-page-title", "atl jira push", "mirror view", "blocked by read-only"} {
				if strings.Contains(string(encoded), marker) {
					t.Fatalf("contract %s retained %q", encoded, marker)
				}
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &members); err != nil {
				t.Fatal(err)
			}
			if len(members) != 3 {
				t.Fatalf("contract %s is not the closed three-member record", encoded)
			}
			for _, member := range []string{"exit_code", "kind", "remediation"} {
				if _, ok := members[member]; !ok {
					t.Fatalf("contract %s is missing %q", encoded, member)
				}
			}
		})
	}
}

func TestValidateCLIErrorContractBoundsExitCodes(t *testing.T) {
	for _, code := range []int{-1, 0, 3, 255, 256} {
		if _, ok := ValidateCLIErrorContract(code, "not_found", "verify_identifier_or_access"); ok {
			t.Fatalf("exit code %d accepted", code)
		}
	}
	if _, ok := ValidateCLIErrorContract(4, "", ""); ok {
		t.Fatal("empty classification accepted")
	}
	for _, contract := range []CLIErrorContract{
		{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		{ExitCode: 1, Kind: "output_limit_exceeded", Remediation: "narrow_or_raise_bound"},
		{ExitCode: 8, Kind: "output_limit_exceeded", Remediation: "narrow_or_raise_bound"},
	} {
		if _, ok := ValidateCLIErrorContract(contract.ExitCode, contract.Kind, contract.Remediation); !ok {
			t.Fatalf("valid contract rejected: %+v", contract)
		}
	}
}

// The benchmark vocabulary is a reviewed copy of the CLI's own classification.
// This proves it stays closed and in step with what the CLI can emit, so a new
// CLI kind cannot silently widen a published contract and an existing one
// cannot drift out of the table.
func TestCLIErrorContractVocabularyMatchesCLIClassification(t *testing.T) {
	examples := []struct {
		err      error
		exitCode int
	}{
		{fmt.Errorf("%w: x", domain.ErrAuth), 3},
		{fmt.Errorf("%w: x", domain.ErrNotFound), 4},
		{fmt.Errorf("%w: x", domain.ErrVersionConflict), 5},
		{fmt.Errorf("%w: x", domain.ErrForbidden), 6},
		{fmt.Errorf("%w: x", domain.ErrConfig), 7},
		{fmt.Errorf("%w: x", domain.ErrOutputLimit), 1},
		{fmt.Errorf("%w: %w", domain.ErrCheckFailed, domain.ErrOutputLimit), 8},
		{fmt.Errorf("%w: x", domain.ErrCheckFailed), 8},
		{fmt.Errorf("%w: x", domain.ErrUsage), 2},
		{fmt.Errorf("x: %w", &httpx.TransportError{}), 1},
		{fmt.Errorf("x: %w", &httpx.APIError{Status: http.StatusTooManyRequests}), 1},
		{fmt.Errorf("x: %w", &httpx.APIError{Status: http.StatusServiceUnavailable}), 1},
		{errors.New("x"), 1},
	}
	covered := map[string]struct{}{}
	for _, example := range examples {
		kind, remediation := diagnostic.Classify(example.err)
		known, exists := cliErrorContractVocabulary[kind]
		if !exists || known.remediation != remediation {
			t.Fatalf("CLI classification %q/%q is not in the benchmark vocabulary", kind, remediation)
		}
		if _, ok := ValidateCLIErrorContract(example.exitCode, kind, remediation); !ok {
			t.Fatalf("reachable CLI contract %d/%q/%q is not in the benchmark vocabulary", example.exitCode, kind, remediation)
		}
		covered[kind] = struct{}{}
	}
	// The two CLI-local policy classifications never reach diagnostic.Classify.
	for kind, remediation := range map[string]string{
		"internal_error": "report_bug", "read_only_policy": "request_human_approval",
	} {
		if cliErrorContractVocabulary[kind].remediation != remediation {
			t.Fatalf("CLI-local classification %q/%q is not in the benchmark vocabulary", kind, remediation)
		}
		if _, ok := ValidateCLIErrorContract(8, kind, remediation); !ok {
			t.Fatalf("reachable CLI-local contract 8/%q/%q is not in the benchmark vocabulary", kind, remediation)
		}
		covered[kind] = struct{}{}
	}
	if len(covered) != len(cliErrorContractVocabulary) {
		t.Fatalf("vocabulary holds %d pairs, %d are reachable from the CLI", len(cliErrorContractVocabulary), len(covered))
	}
}

// Runtime examples above bind every current triplet, while this source-level
// census closes the other drift direction: adding a literal classification to
// diagnostic.Classify or classifyError without adding it to the harness fails
// even when nobody remembered to add a matching example.
func TestCLIErrorContractVocabularyCoversEverySourceClassification(t *testing.T) {
	sourcePairs := returnedStringPairs(t, filepath.Join("..", "diagnostic", "error.go"), "Classify")
	localPairs := returnedStringPairs(t, filepath.Join("..", "cli", "root.go"), "classifyError")
	for kind, remediation := range localPairs {
		if previous, exists := sourcePairs[kind]; exists && previous != remediation {
			t.Fatalf("source classification %q has two remediations: %q and %q", kind, previous, remediation)
		}
		sourcePairs[kind] = remediation
	}
	if len(sourcePairs) != len(cliErrorContractVocabulary) {
		t.Fatalf("source has %d classifications, harness has %d", len(sourcePairs), len(cliErrorContractVocabulary))
	}
	for kind, remediation := range sourcePairs {
		known, exists := cliErrorContractVocabulary[kind]
		if !exists || known.remediation != remediation {
			t.Fatalf("source classification %q/%q is absent or different in the harness", kind, remediation)
		}
	}
}

func returnedStringPairs(t *testing.T, file, function string) map[string]string {
	t.Helper()
	parsed := parseGoFile(t, file)
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
			if len(statement.Results) == 1 && function == "classifyError" && isDiagnosticClassifyDelegation(statement.Results[0]) {
				return true
			}
			if len(statement.Results) != 2 {
				t.Fatalf("%s.%s contains an unexpected %d-result return", file, function, len(statement.Results))
			}
			kind, kindOK := stringLiteral(statement.Results[0])
			remediation, remediationOK := stringLiteral(statement.Results[1])
			if !kindOK || !remediationOK {
				t.Fatalf("%s.%s contains a non-literal two-result return", file, function)
			}
			pairs[kind] = remediation
			return true
		})
		return pairs
	}
	t.Fatalf("function %s not found in %s", function, file)
	return nil
}

func isDiagnosticClassifyDelegation(expression ast.Expr) bool {
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

// The vocabulary's numeric column is reviewed against the CLI's source of
// truth. This exact, ordered census catches a new, reordered, or structurally
// changed codeFor clause and a changed exit constant; the CLI package's runtime
// matrix then joins those values to the harness vocabulary.
func TestCLIErrorContractExitCodeSourceIsPinned(t *testing.T) {
	file := filepath.Join("..", "cli", "root.go")
	parsed := parseGoFile(t, file)
	wantConstants := map[string]int{
		"exitOK": 0, "exitGeneric": 1, "exitUsage": 2, "exitAuth": 3, "exitNotFound": 4,
		"exitVersionConfl": 5, "exitForbidden": 6, "exitConfig": 7, "exitCheckFailed": 8,
	}
	gotConstants := integerConstants(parsed, wantConstants)
	if !equalStringIntMaps(gotConstants, wantConstants) {
		t.Fatalf("CLI exit constants=%v, want %v", gotConstants, wantConstants)
	}
	wantCases := []switchReturn{
		{condition: "ErrAuth", result: "exitAuth"},
		{condition: "ErrNotFound", result: "exitNotFound"},
		{condition: "ErrVersionConflict", result: "exitVersionConfl"},
		{condition: "ErrForbidden", result: "exitForbidden"},
		{condition: "ErrConfig", result: "exitConfig"},
		{condition: "ErrCheckFailed", result: "exitCheckFailed"},
		{condition: "ErrUsage", result: "exitUsage"},
		{condition: "default", result: "exitGeneric"},
	}
	gotCases := functionSwitchReturns(t, parsed, "codeFor")
	if !slices.Equal(gotCases, wantCases) {
		t.Fatalf("codeFor cases=%v, want %v", gotCases, wantCases)
	}
}

func parseGoFile(t *testing.T, file string) *ast.File {
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

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func integerConstants(parsed *ast.File, wanted map[string]int) map[string]int {
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

type switchReturn struct {
	condition string
	result    string
}

func functionSwitchReturns(t *testing.T, parsed *ast.File, function string) []switchReturn {
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
		var returns []switchReturn
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
			returns = append(returns, switchReturn{condition: key, result: identifier.Name})
		}
		return returns
	}
	t.Fatalf("function %s not found", function)
	return nil
}

func equalStringIntMaps(left, right map[string]int) bool {
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

func TestCLIErrorContractsEqualComparesExactOrderedContracts(t *testing.T) {
	checks := []RunCheck{{Name: "error_contracts", Kind: "cli_error_contracts_equal", Expected: json.RawMessage(
		`[{"exit_code":4,"kind":"not_found","remediation":"verify_identifier_or_access"},` +
			`{"exit_code":8,"kind":"check_failed","remediation":"review_failed_check"}]`)}}
	notFound := CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"}
	checkFailed := CLIErrorContract{ExitCode: 8, Kind: "check_failed", Remediation: "review_failed_check"}
	for name, observed := range map[string][]CLIErrorContract{
		"exact":             {notFound, checkFailed},
		"reordered":         {checkFailed, notFound},
		"absent":            nil,
		"partial":           {notFound},
		"extra":             {notFound, checkFailed, checkFailed},
		"wrong kind":        {notFound, {ExitCode: 8, Kind: "forbidden", Remediation: "request_access"}},
		"wrong exit code":   {notFound, {ExitCode: 4, Kind: "check_failed", Remediation: "review_failed_check"}},
		"wrong remediation": {notFound, {ExitCode: 8, Kind: "check_failed", Remediation: "report_bug"}},
	} {
		t.Run(name, func(t *testing.T) {
			results, err := evaluateRunChecksWithCLIErrorContracts(checks, []byte(`{}`), "", 2, 2, 0, 0, nil,
				0, 0, nil, false, []int{4, 8}, nil, false, nil, nil, false, observed)
			if err != nil {
				t.Fatal(err)
			}
			if results["error_contracts"] != (name == "exact") {
				t.Fatalf("%s evaluated to %v", name, results["error_contracts"])
			}
		})
	}
	// The narrower entry points cannot observe contracts at all, so an oracle
	// evaluated through them stays false rather than silently passing.
	results, err := evaluateRunChecks(checks, []byte(`{}`), "", 2, 2, 0, 0, nil, 0, 0, nil, false, []int{4, 8})
	if err != nil || results["error_contracts"] {
		t.Fatalf("legacy evaluation results=%v err=%v", results, err)
	}
}

func TestRunSpecCLIErrorContractCheckRequiresBoundedClosedExpectation(t *testing.T) {
	base := validRunSpec()
	valid := base
	valid.Checks = append(append([]RunCheck(nil), base.Checks...), RunCheck{
		Name: "error_contracts", Kind: "cli_error_contracts_equal",
		Expected: json.RawMessage(`[{"exit_code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}]`),
	})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunSpec){
		"missing expected": func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = nil },
		"null expected":    func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`null`) },
		"empty array":      func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[]`) },
		"not an array":     func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`{"exit_code":4}`) },
		"trailing data":    func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[] []`) },
		"pointer":          func(s *RunSpec) { s.Checks[len(s.Checks)-1].Pointer = "/answer" },
		"minimum":          func(s *RunSpec) { s.Checks[len(s.Checks)-1].Minimum = 1 },
		"maximum":          func(s *RunSpec) { s.Checks[len(s.Checks)-1].Maximum = 1 },
		"mcp transport":    func(s *RunSpec) { s.ToolTransport = "mcp" },
		"successful code": func(s *RunSpec) {
			s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[{"exit_code":0,"kind":"not_found","remediation":"verify_identifier_or_access"}]`)
		},
		"unbounded code": func(s *RunSpec) {
			s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[{"exit_code":256,"kind":"not_found","remediation":"verify_identifier_or_access"}]`)
		},
		"unknown kind": func(s *RunSpec) {
			s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[{"exit_code":4,"kind":"backend_said_no","remediation":"verify_identifier_or_access"}]`)
		},
		"pair mismatch": func(s *RunSpec) {
			s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[{"exit_code":4,"kind":"not_found","remediation":"report_bug"}]`)
		},
		"free-text member": func(s *RunSpec) {
			s.Checks[len(s.Checks)-1].Expected = json.RawMessage(`[{"exit_code":4,"kind":"not_found","remediation":"verify_identifier_or_access","error":"private prose"}]`)
		},
		"unbounded length": func(s *RunSpec) { s.Checks[len(s.Checks)-1].Expected = json.RawMessage(oversizedContractExpectation()) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Checks = append([]RunCheck(nil), valid.Checks...)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid cli_error_contracts_equal check passed")
			}
		})
	}
}

func oversizedContractExpectation() string {
	entry := `{"exit_code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}`
	entries := make([]string, maxContractListEntries+1)
	for index := range entries {
		entries[index] = entry
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// Every kind Validate accepts must have a comparison class, or a private
// comparison set fails on a spec the runner would happily execute. The accepted
// kinds are read from the validator itself so a new kind cannot be added
// without a class.
func TestEveryValidatedRunCheckKindHasComparisonClass(t *testing.T) {
	kinds := runCheckKindsAcceptedBy(t, "runspec.go")
	if len(kinds) < 20 {
		t.Fatalf("only %d run check kinds were discovered", len(kinds))
	}
	for _, kind := range []string{"cli_exit_codes_equal", "cli_error_contracts_equal", "json_equals"} {
		if _, ok := kinds[kind]; !ok {
			t.Fatalf("kind discovery missed %q", kind)
		}
	}
	for kind := range kinds {
		if runCheckClass(kind) == "" {
			t.Fatalf("run check kind %q has no comparison class", kind)
		}
	}
	if runCheckClass("kind_that_does_not_exist") != "" {
		t.Fatal("comparison classification is not closed")
	}
}

// runCheckKindsAcceptedBy collects the case literals of every switch on a run
// check's Kind in the given source file.
func runCheckKindsAcceptedBy(t *testing.T, file string) map[string]struct{} {
	t.Helper()
	source, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]struct{}{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		statement, ok := node.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		selector, ok := statement.Tag.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Kind" {
			return true
		}
		for _, clause := range statement.Body.List {
			for _, expression := range clause.(*ast.CaseClause).List {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				kinds[value] = struct{}{}
			}
		}
		return true
	})
	return kinds
}

func TestProxyRecordCLIErrorContractIsRevalidatedBeforeEvaluation(t *testing.T) {
	classified, ok, err := atlProxyRecord{ExitCode: 4, ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access"}.errorContract()
	if err != nil || !ok || classified != (CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"}) {
		t.Fatalf("contract=%+v ok=%v err=%v", classified, ok, err)
	}
	for name, record := range map[string]atlProxyRecord{
		"successful exit":       {ExitCode: 0, ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access"},
		"unknown kind":          {ExitCode: 4, ErrorKind: "backend_said_no", ErrorRemediation: "verify_identifier_or_access"},
		"mismatched pair":       {ExitCode: 4, ErrorKind: "not_found", ErrorRemediation: "report_bug"},
		"half a classification": {ExitCode: 4, ErrorKind: "not_found"},
		"denied invocation":     {ExitCode: 4, Denied: true, ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := record.errorContract(); err == nil || ok {
				t.Fatalf("tampered record accepted: ok=%v err=%v", ok, err)
			}
		})
	}
	for name, record := range map[string]atlProxyRecord{
		"successful invocation":   {ExitCode: 0},
		"unclassified failure":    {ExitCode: 4},
		"denied without contract": {ExitCode: 2, Denied: true},
	} {
		t.Run(name, func(t *testing.T) {
			contract, ok, err := record.errorContract()
			if err != nil || ok || contract != (CLIErrorContract{}) {
				t.Fatalf("contract=%+v ok=%v err=%v", contract, ok, err)
			}
		})
	}
}

// The publishable baseline keeps only counters and outcomes. The contract lives
// in the owner-private audit for the runner's oracle; it must not travel.
func TestSanitizedPrivateAuditDropsCLIErrorContracts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atl-invocations.jsonl")
	writeTestFile(t, path, strings.Join([]string{
		`{"command_family":"jira.issue.get","error_kind":"not_found","error_remediation":"verify_identifier_or_access","stdout_bytes":0,"stderr_bytes":120,"exit_code":4}`,
		`{"command_family":"jira.fields","stdout_bytes":40,"stderr_bytes":0,"exit_code":0}`,
		"",
	}, "\n"), 0o600)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := sanitizePrivateAudit("atl-invocations.jsonl", path, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"error_kind", "error_remediation", "not_found", "verify_identifier_or_access", "command_family"} {
		if strings.Contains(string(sanitized), marker) {
			t.Fatalf("sanitized audit %s retained %q", sanitized, marker)
		}
	}
	if !strings.Contains(string(sanitized), `"exit_code":4`) {
		t.Fatalf("sanitized audit %s dropped the audited exit code", sanitized)
	}
}
