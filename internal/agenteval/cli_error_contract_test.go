package agenteval

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const cliErrorWireFixturePath = "../diagnostic/testdata/cli-error-wire.v1.json"

type cliErrorWireFixture struct {
	SchemaVersion int                         `json:"schema_version"`
	Contracts     []CLIErrorContract          `json:"contracts"`
	Recovery      cliErrorRecoveryWireFixture `json:"recovery"`
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

func TestCLIErrorContractVocabularyMatchesVersionedWireFixture(t *testing.T) {
	data, err := os.ReadFile(cliErrorWireFixturePath)
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
	if got := KnownCLIErrorContracts(); !slices.Equal(got, fixture.Contracts) {
		t.Fatalf("evaluator vocabulary=%v, versioned wire fixture=%v", got, fixture.Contracts)
	}
	assertEvaluatorRecoveryWireFixture(t, fixture.Recovery)
}

func assertEvaluatorRecoveryWireFixture(t *testing.T, fixture cliErrorRecoveryWireFixture) {
	t.Helper()
	if fixture.SchemaVersion != 1 {
		t.Fatalf("recovery wire fixture schema=%d, want 1", fixture.SchemaVersion)
	}
	if got := cliErrorJSONMembers(reflect.TypeFor[cliErrorRecovery]()); !slices.Equal(got, fixture.Members) {
		t.Fatalf("evaluator recovery members=%v, versioned wire fixture=%v", got, fixture.Members)
	}
	actions := cliErrorTypedStringConstants(t, "cli_error_recovery.go", "cliErrorRecoveryAction")
	if !slices.Equal(actions, fixture.Actions) {
		t.Fatalf("evaluator recovery actions=%v, versioned wire fixture=%v", actions, fixture.Actions)
	}
	capabilities := cliErrorTypedStringConstants(t, "cli_error_recovery.go", "cliErrorNextCapability")
	if !slices.Equal(capabilities, fixture.NextCapabilities) {
		t.Fatalf("evaluator recovery capabilities=%v, versioned wire fixture=%v", capabilities, fixture.NextCapabilities)
	}
	if len(fixture.ValidVectors) != 27 || len(fixture.InvalidVectors) != 30 {
		t.Fatalf("recovery semantic vectors=%d valid/%d invalid, want 27/30", len(fixture.ValidVectors), len(fixture.InvalidVectors))
	}
	for index, raw := range fixture.ValidVectors {
		if !validCLIErrorRecoveryJSON(raw) {
			t.Fatalf("evaluator rejected valid recovery vector %d: %s", index, raw)
		}
	}
	for index, raw := range fixture.InvalidVectors {
		if validCLIErrorRecoveryJSON(raw) {
			t.Fatalf("evaluator accepted invalid recovery vector %d: %s", index, raw)
		}
	}
}

func cliErrorJSONMembers(typ reflect.Type) []cliErrorWireMember {
	members := make([]cliErrorWireMember, 0, typ.NumField())
	for index := range typ.NumField() {
		tag := typ.Field(index).Tag.Get("json")
		parts := strings.Split(tag, ",")
		members = append(members, cliErrorWireMember{Name: parts[0], Required: !slices.Contains(parts[1:], "omitempty")})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].Name < members[right].Name })
	return members
}

func cliErrorTypedStringConstants(t *testing.T, file, typeName string) []string {
	t.Helper()
	source, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatal(err)
	}
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
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s constant has a non-literal value", typeName)
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, value)
			}
		}
	}
	sort.Strings(values)
	return values
}

func TestParseCLIErrorContractAdmitsOnlyTypedFailedCLIErrors(t *testing.T) {
	legacyPreRecovery := `{"error":"page not found: private-page-title","code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}`
	releasedV060AndCurrent := `{"error":"page not found: private-page-title","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"adjust_request","retry_safe":false}}`
	policy := `{"error":"blocked by read-only policy","code":8,"kind":"read_only_policy","remediation":"request_human_approval","policy":"read_only","command":"atl jira push"}`
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		want     CLIErrorContract
	}{
		{
			name: "typed failure after unrelated stderr", exitCode: 4,
			stderr: "warning: mirror view not regenerated\n" + releasedV060AndCurrent + "\n",
			want:   CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		},
		{
			name: "pre-recovery backward compatibility", exitCode: 4, stderr: legacyPreRecovery,
			want: CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		},
		{
			name: "v0.6.0 and current typed failure with validated recovery", exitCode: 4, stderr: releasedV060AndCurrent,
			want: CLIErrorContract{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"},
		},
		{
			name: "read-only policy refusal", exitCode: 8, stderr: policy,
			want: CLIErrorContract{ExitCode: 8, Kind: "read_only_policy", Remediation: "request_human_approval"},
		},
		{name: "successful invocation", exitCode: 0, stderr: releasedV060AndCurrent},
		{name: "empty capture", exitCode: 4, stderr: ""},
		{name: "blank capture", exitCode: 4, stderr: "\n \n\t\n"},
		{name: "text output", exitCode: 4, stderr: "error: page not found: private-page-title\n"},
		{name: "truncated object", exitCode: 4, stderr: releasedV060AndCurrent[:len(releasedV060AndCurrent)-12]},
		{name: "trailing data", exitCode: 4, stderr: releasedV060AndCurrent + " {}"},
		{name: "not an object", exitCode: 4, stderr: "[4]"},
		{name: "code disagrees with audited exit", exitCode: 5, stderr: releasedV060AndCurrent},
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
			name: "missing required recovery retry safety", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","recovery":{"schema_version":1,"action":"adjust_request"}}`,
		},
		{
			name: "policy member without its kind", exitCode: 4,
			stderr: `{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access","policy":"read_only","command":"atl jira push"}`,
		},
		{
			name: "oversized capture", exitCode: 4,
			stderr: strings.Repeat("x\n", maxCLIErrorContractStderrBytes) + releasedV060AndCurrent,
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
