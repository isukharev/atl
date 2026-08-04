package agenteval

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// evaluatorRuntimeModeContract is a closed review classification for the
// evaluator's mutually exclusive runtime modes. Its prose fields capture the
// decisions that a refactor must review; the focused AST and behavioral tests
// below independently bind the critical executable boundaries.
type evaluatorRuntimeModeContract struct {
	SourceFile             string
	EntryFunction          string
	ImmutableContractOwner string
	RuntimeOnlyBindings    string
	MarkerSideEffect       string
	ProviderBackendContact string
	AuthOwner              string
	CapsuleGranularity     string
	PreProviderRoles       string
	AttemptBoundary        string
	SpawnCardinality       string
	Revalidation           string
	CleanupOwner           string
}

type evaluatorRuntimeModeContractRow struct {
	Name string
	evaluatorRuntimeModeContract
}

func TestEvaluatorRuntimeModeClosedReviewClassification(t *testing.T) {
	// This is an ordered, closed vocabulary. A new runtime mode needs a full
	// boundary row, rather than inheriting an existing execution contract.
	rows := []evaluatorRuntimeModeContractRow{
		{Name: "headless-dry-run", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "runner.go", EntryFunction: "RunHeadless",
			ImmutableContractOwner: "RunHeadless", RuntimeOnlyBindings: "loaded run and preview command", MarkerSideEffect: "private output root marker",
			ProviderBackendContact: "none", AuthOwner: "none", CapsuleGranularity: "none",
			PreProviderRoles: "input validation and preview construction", AttemptBoundary: "none", SpawnCardinality: "zero",
			Revalidation: "run spec and scenario", CleanupOwner: "none",
		}},
		{Name: "headless-synthetic", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "runner.go", EntryFunction: "RunHeadless",
			ImmutableContractOwner: "RunHeadless", RuntimeOnlyBindings: "synthetic fixture and evaluator wrapper", MarkerSideEffect: "private output root marker and synthetic receipt",
			ProviderBackendContact: "provider and synthetic mock backend", AuthOwner: "RunHeadless for Codex; provider process for Claude", CapsuleGranularity: "one provider capsule per repetition",
			PreProviderRoles: "input validation and executable attestation", AttemptBoundary: "immediately before provider spawn", SpawnCardinality: "one provider process per repetition",
			Revalidation: "executable attestation before execution", CleanupOwner: "RunHeadless",
		}},
		{Name: "headless-private-live", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "runner.go", EntryFunction: "RunHeadless",
			ImmutableContractOwner: "RunHeadless", RuntimeOnlyBindings: "private workspace, gateway, and evaluator wrapper", MarkerSideEffect: "private output root marker and run artifacts",
			ProviderBackendContact: "provider and configured private backend", AuthOwner: "RunHeadless or shared ExecutePrivatePlan session", CapsuleGranularity: "one provider capsule per repetition",
			PreProviderRoles: "private input validation and confinement preflight", AttemptBoundary: "immediately before provider spawn", SpawnCardinality: "one provider process per repetition",
			Revalidation: "plugin package before private CLI spawn", CleanupOwner: "RunHeadless",
		}},
		{Name: "provider-calibration", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "calibration.go", EntryFunction: "RunCodexCLICalibration",
			ImmutableContractOwner: "RunCodexCLICalibration", RuntimeOnlyBindings: "snapshotted Codex runtime and command broker", MarkerSideEffect: "calibration evidence and receipt",
			ProviderBackendContact: "one provider invocation and no backend", AuthOwner: "ExecutePrivatePlan", CapsuleGranularity: "one calibration provider capsule",
			PreProviderRoles: "confinement preflight and fixed command policy", AttemptBoundary: "durable calibration provider-attempt event before spawn", SpawnCardinality: "one provider process",
			Revalidation: "plugin package immediately before spawn", CleanupOwner: "RunCodexCLICalibration",
		}},
		{Name: "private-plan-treatment", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "private_plan.go", EntryFunction: "ExecutePrivatePlan",
			ImmutableContractOwner: "ExecutePrivatePlan", RuntimeOnlyBindings: "private plan snapshot and RunHeadless options", MarkerSideEffect: "lifecycle state and treatment receipt",
			ProviderBackendContact: "provider and configured private backend", AuthOwner: "ExecutePrivatePlan", CapsuleGranularity: "one provider capsule per treatment cell",
			PreProviderRoles: "current and snapshot material revalidation", AttemptBoundary: "durable treatment provider-attempt event before spawn", SpawnCardinality: "one provider process per treatment cell",
			Revalidation: "before every treatment cell", CleanupOwner: "ExecutePrivatePlan",
		}},
		{Name: "private-review", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "private_review_runner.go", EntryFunction: "RunPrivateReview",
			ImmutableContractOwner: "RunPrivateReview", RuntimeOnlyBindings: "review packet, isolated provider runtime, and review proxy", MarkerSideEffect: "review execution attempt and receipt",
			ProviderBackendContact: "one reviewer provider invocation and no backend", AuthOwner: "runPrivateReviewProvider", CapsuleGranularity: "one scratch runtime per reviewer slot",
			PreProviderRoles: "prepared packet, reserve, and pristine-template validation", AttemptBoundary: "durable execution-attempt record before provider setup", SpawnCardinality: "one provider process per reviewer slot",
			Revalidation: "review bindings and packet inputs", CleanupOwner: "runPrivateReviewProvider",
		}},
		{Name: "tool-availability-probe", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "tool_availability.go", EntryFunction: "QualifyCodexCLIToolAvailability",
			ImmutableContractOwner: "QualifyCodexCLIToolAvailability", RuntimeOnlyBindings: "isolated probe runtime and loopback Responses endpoint", MarkerSideEffect: "content-free availability report",
			ProviderBackendContact: "one loopback synthetic request and no provider or backend", AuthOwner: "none", CapsuleGranularity: "one probe runtime",
			PreProviderRoles: "inspect and copy reviewed agent", AttemptBoundary: "none", SpawnCardinality: "one probe child",
			Revalidation: "copied reviewed agent", CleanupOwner: "QualifyCodexCLIToolAvailability",
		}},
		{Name: "cli-route-probe", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "cli_route_qualification.go", EntryFunction: "QualifyCLIRoute",
			ImmutableContractOwner: "QualifyCLIRoute", RuntimeOnlyBindings: "isolated probe runtime and nonce-scoped loopback endpoint", MarkerSideEffect: "content-free route report",
			ProviderBackendContact: "one loopback synthetic request and no provider or backend", AuthOwner: "none", CapsuleGranularity: "one probe runtime",
			PreProviderRoles: "inspect and copy reviewed agent", AttemptBoundary: "none", SpawnCardinality: "one probe child terminated after first request",
			Revalidation: "copied reviewed agent", CleanupOwner: "QualifyCLIRoute",
		}},
		{Name: "confinement-probe", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "runner.go", EntryFunction: "runCodexConfinementPreflight",
			ImmutableContractOwner: "runCodexConfinementPreflight", RuntimeOnlyBindings: "reviewed agent sandbox profile and forbidden loopback listener", MarkerSideEffect: "none",
			ProviderBackendContact: "no model, provider, or backend", AuthOwner: "provider runtime when supplied", CapsuleGranularity: "one preflight child",
			PreProviderRoles: "validate confinement before model access", AttemptBoundary: "none", SpawnCardinality: "one sandbox child",
			Revalidation: "provider launch resolution", CleanupOwner: "runCodexConfinementPreflight",
		}},
		{Name: "offline-aggregate", evaluatorRuntimeModeContract: evaluatorRuntimeModeContract{
			SourceFile: "aggregate_root.go", EntryFunction: "AggregateSyntheticOutputRoot",
			ImmutableContractOwner: "AggregateSyntheticOutputRoot", RuntimeOnlyBindings: "marked synthetic output root", MarkerSideEffect: "none",
			ProviderBackendContact: "none", AuthOwner: "none", CapsuleGranularity: "none",
			PreProviderRoles: "stable inventory and contract validation", AttemptBoundary: "none", SpawnCardinality: "zero",
			Revalidation: "stable reread before aggregate", CleanupOwner: "AggregateSyntheticOutputRoot",
		}},
	}
	if !validEvaluatorRuntimeModeRows(rows) {
		t.Fatal("runtime mode contract invalid")
	}
	for _, row := range rows {
		if !evaluatorRuntimeEntryPointExists(row.SourceFile, row.EntryFunction) {
			t.Fatal("runtime mode contract invalid")
		}
	}
	for _, mutate := range []func([]evaluatorRuntimeModeContractRow) []evaluatorRuntimeModeContractRow{
		func(candidate []evaluatorRuntimeModeContractRow) []evaluatorRuntimeModeContractRow {
			return append(candidate, candidate[0])
		},
		func(candidate []evaluatorRuntimeModeContractRow) []evaluatorRuntimeModeContractRow {
			return candidate[:len(candidate)-1]
		},
		func(candidate []evaluatorRuntimeModeContractRow) []evaluatorRuntimeModeContractRow {
			candidate[0].Name = "unknown-runtime-mode"
			return candidate
		},
		func(candidate []evaluatorRuntimeModeContractRow) []evaluatorRuntimeModeContractRow {
			candidate[0].CleanupOwner = ""
			return candidate
		},
	} {
		candidate := append([]evaluatorRuntimeModeContractRow(nil), rows...)
		if validEvaluatorRuntimeModeRows(mutate(candidate)) {
			t.Fatal("runtime mode contract invalid")
		}
	}
}

func validEvaluatorRuntimeModeRows(rows []evaluatorRuntimeModeContractRow) bool {
	want := []string{
		"headless-dry-run", "headless-synthetic", "headless-private-live", "provider-calibration", "private-plan-treatment",
		"private-review", "tool-availability-probe", "cli-route-probe", "confinement-probe", "offline-aggregate",
	}
	if len(rows) != len(want) {
		return false
	}
	known := make(map[string]bool, len(want))
	for _, name := range want {
		known[name] = true
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !known[row.Name] || seen[row.Name] || !completeEvaluatorRuntimeModeContract(row.evaluatorRuntimeModeContract) {
			return false
		}
		seen[row.Name] = true
	}
	for _, name := range want {
		if !seen[name] {
			return false
		}
	}
	return true
}

func completeEvaluatorRuntimeModeContract(row evaluatorRuntimeModeContract) bool {
	for _, value := range []string{
		row.SourceFile, row.EntryFunction, row.ImmutableContractOwner, row.RuntimeOnlyBindings, row.MarkerSideEffect, row.ProviderBackendContact, row.AuthOwner,
		row.CapsuleGranularity, row.PreProviderRoles, row.AttemptBoundary, row.SpawnCardinality, row.Revalidation, row.CleanupOwner,
	} {
		if value == "" {
			return false
		}
	}
	return true
}

func evaluatorRuntimeEntryPointExists(name, function string) bool {
	parsed, err := parseEvaluatorRuntimeSource(name)
	if err != nil {
		return false
	}
	return evaluatorRuntimeFunction(parsed, function) != nil
}

func parseEvaluatorRuntimeSource(name string) (*ast.File, error) {
	set := token.NewFileSet()
	return parser.ParseFile(set, name, nil, 0)
}

func evaluatorRuntimeFunction(file *ast.File, name string) *ast.FuncDecl {
	for _, declaration := range file.Decls {
		candidate, ok := declaration.(*ast.FuncDecl)
		if ok && candidate.Name.Name == name {
			return candidate
		}
	}
	return nil
}

type evaluatorRuntimeCallMatcher func(*ast.CallExpr) bool

func identifierCall(name string) evaluatorRuntimeCallMatcher {
	return func(call *ast.CallExpr) bool {
		identifier, ok := call.Fun.(*ast.Ident)
		return ok && identifier.Name == name
	}
}

func identifierCallWithArgumentPaths(name string, argumentPaths ...[]string) evaluatorRuntimeCallMatcher {
	base := identifierCall(name)
	return func(call *ast.CallExpr) bool {
		if !base(call) || len(call.Args) != len(argumentPaths) {
			return false
		}
		for index, want := range argumentPaths {
			if !expressionPath(call.Args[index], want...) {
				return false
			}
		}
		return true
	}
}

func selectorCall(receiver, method string) evaluatorRuntimeCallMatcher {
	return func(call *ast.CallExpr) bool {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != method {
			return false
		}
		identifier, ok := selector.X.(*ast.Ident)
		return ok && identifier.Name == receiver
	}
}

func selectorCallWithReceiverPath(receiver []string, method string) evaluatorRuntimeCallMatcher {
	return func(call *ast.CallExpr) bool {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == method && expressionPath(selector.X, receiver...)
	}
}

func selectorCallWithIdentifierArgument(receiver, method, argument string) evaluatorRuntimeCallMatcher {
	base := selectorCall(receiver, method)
	return func(call *ast.CallExpr) bool {
		if !base(call) {
			return false
		}
		if len(call.Args) < 2 {
			return false
		}
		identifier, ok := call.Args[1].(*ast.Ident)
		return ok && identifier.Name == argument
	}
}

func assertRuntimeModeCallOrder(t *testing.T, name, function string, matchers ...evaluatorRuntimeCallMatcher) {
	t.Helper()
	parsed, err := parseEvaluatorRuntimeSource(name)
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, function)
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	next := 0
	ast.Inspect(body.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && next < len(matchers) && matchers[next](call) {
			next++
		}
		return true
	})
	if next != len(matchers) {
		t.Fatal("runtime mode contract invalid")
	}
}

func assertRuntimeModeCallCount(t *testing.T, name, function string, matcher evaluatorRuntimeCallMatcher, want int) {
	t.Helper()
	parsed, err := parseEvaluatorRuntimeSource(name)
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, function)
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	count := 0
	ast.Inspect(body.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && matcher(call) {
			count++
		}
		return true
	})
	if count != want {
		t.Fatal("runtime mode contract invalid")
	}
}

func assertRuntimeModeReceiptPersistenceGuards(t *testing.T) {
	t.Helper()
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, "runner.go", nil, 0)
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, "RunHeadless")
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	verifyGuard, pluginGuard, receiptLoop := -1, -1, -1
	for index, statement := range body.Body.List {
		if guard, ok := statement.(*ast.IfStmt); ok {
			switch {
			case runtimeModeExactCallGuard(set, guard, []string{"err"},
				selectorCallWithArgumentPaths("attestation", "verifyExecutables",
					[]string{"options", "AgentBinary"}, []string{"options", "ATLBinary"}, []string{"options", "WrapperExecutable"}),
				"err != nil"):
				verifyGuard = index
			case runtimeModeExactPluginIdentityGuard(set, guard):
				pluginGuard = index
			}
		}
		if loop, ok := statement.(*ast.RangeStmt); ok && runtimeModeExactReceiptPersistenceLoop(set, loop) {
			receiptLoop = index
		}
	}
	if verifyGuard < 0 || pluginGuard <= verifyGuard || receiptLoop <= pluginGuard {
		t.Fatal("runtime mode contract invalid")
	}
}

func selectorCallWithArgumentPaths(receiver, method string, argumentPaths ...[]string) evaluatorRuntimeCallMatcher {
	base := selectorCall(receiver, method)
	return func(call *ast.CallExpr) bool {
		if !base(call) || len(call.Args) != len(argumentPaths) {
			return false
		}
		for index, want := range argumentPaths {
			if !expressionPath(call.Args[index], want...) {
				return false
			}
		}
		return true
	}
}

func runtimeModeExactCallGuard(
	set *token.FileSet,
	guard *ast.IfStmt,
	lhs []string,
	matcher evaluatorRuntimeCallMatcher,
	condition string,
) bool {
	return guard != nil && guard.Else == nil && runtimeModeExactCallAssignment(guard.Init, lhs, matcher) &&
		runtimeModeNodeSource(set, guard.Cond) == condition && runtimeModeDirectReturn(guard.Body)
}

func runtimeModeExactCallAssignment(statement ast.Stmt, lhs []string, matcher evaluatorRuntimeCallMatcher) bool {
	assignment, ok := statement.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != len(lhs) || len(assignment.Rhs) != 1 {
		return false
	}
	for index, name := range lhs {
		if !expressionPath(assignment.Lhs[index], name) {
			return false
		}
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return ok && matcher(call)
}

func runtimeModeDirectReturn(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}
	_, ok := body.List[0].(*ast.ReturnStmt)
	return ok
}

func runtimeModeExactPluginIdentityGuard(set *token.FileSet, guard *ast.IfStmt) bool {
	if guard == nil || guard.Init != nil || guard.Else != nil ||
		runtimeModeNodeSource(set, guard.Cond) != "attestation != nil" || len(guard.Body.List) != 2 {
		return false
	}
	if !runtimeModeExactCallAssignment(guard.Body.List[0], []string{"finalPluginVersion", "finalSkillDigest", "err"},
		identifierCallWithArgumentPaths("pluginIdentity", []string{"options", "PluginRoot"}, []string{"contract", "spec", "Provider"})) {
		return false
	}
	mismatch, ok := guard.Body.List[1].(*ast.IfStmt)
	return ok && mismatch.Init == nil && mismatch.Else == nil &&
		runtimeModeNodeSource(set, mismatch.Cond) ==
			"err != nil || finalPluginVersion != pluginVersion || finalSkillDigest != skillDigest" &&
		runtimeModeDirectReturn(mismatch.Body)
}

func runtimeModeExactReceiptPersistenceLoop(set *token.FileSet, loop *ast.RangeStmt) bool {
	if loop == nil || loop.Tok != token.DEFINE || !expressionPath(loop.Key, "_") ||
		!expressionPath(loop.Value, "receipt") || !expressionPath(loop.X, "receipts") || len(loop.Body.List) != 1 {
		return false
	}
	guard, ok := loop.Body.List[0].(*ast.IfStmt)
	return ok && runtimeModeExactCallGuard(set, guard, []string{"err"},
		identifierCallWithArgumentPaths("writeSyntheticRunReceipt", []string{"outputRoot"}, []string{"receipt"}),
		"err != nil")
}

func runtimeModeNodeSource(set *token.FileSet, node ast.Node) string {
	var buffer bytes.Buffer
	if err := format.Node(&buffer, set, node); err != nil {
		return ""
	}
	return buffer.String()
}

func assertRuntimeModeConditionalAssignment(t *testing.T, name, function, condition, target string, valuePath ...string) {
	t.Helper()
	parsed, err := parseEvaluatorRuntimeSource(name)
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, function)
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	matches := 0
	ast.Inspect(body.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok || !expressionPath(statement.Cond, condition) {
			return true
		}
		for _, nested := range statement.Body.List {
			assignment, ok := nested.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || !expressionPath(assignment.Lhs[0], target) {
				continue
			}
			if expressionPath(assignment.Rhs[0], valuePath...) {
				matches++
			}
		}
		return true
	})
	if matches != 1 {
		t.Fatal("runtime mode contract invalid")
	}
}

func assertHeadlessExecutionErrorPrecedence(t *testing.T) {
	t.Helper()
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, "runner.go", nil, 0)
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, "runHeadlessOnce")
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	want := []string{
		"execution.gatewayCloseErr != nil",
		"execution.brokerCloseErr != nil",
		"execution.externalCloseErr != nil",
		"execution.timedOut",
		"execution.guardAborted",
		"execution.runErr != nil",
		"execution.closeTranscriptErr != nil || execution.closeStderrErr != nil",
	}
	next := 0
	for _, statement := range body.Body.List {
		guard, ok := statement.(*ast.IfStmt)
		if !ok || next >= len(want) || runtimeModeNodeSource(set, guard.Cond) != want[next] {
			continue
		}
		if guard.Init != nil || guard.Else != nil || !runtimeModeDirectReturn(guard.Body) {
			t.Fatal("runtime mode contract invalid")
		}
		next++
	}
	if next != len(want) {
		t.Fatal("runtime mode contract invalid")
	}
}

func assertRuntimeModeDryRunReturnOrder(t *testing.T) {
	t.Helper()
	parsed, err := parseEvaluatorRuntimeSource("runner.go")
	if err != nil {
		t.Fatal("runtime mode contract invalid")
	}
	body := evaluatorRuntimeFunction(parsed, "RunHeadless")
	if body == nil {
		t.Fatal("runtime mode contract invalid")
	}
	returnPosition := token.NoPos
	for _, statement := range body.Body.List {
		condition, ok := statement.(*ast.IfStmt)
		if !ok || !selectorExpression(condition.Cond, "options", "DryRun") {
			continue
		}
		for _, nested := range condition.Body.List {
			if result, ok := nested.(*ast.ReturnStmt); ok {
				returnPosition = result.Return
			}
		}
	}
	if !returnPosition.IsValid() {
		t.Fatal("runtime mode contract invalid")
	}
	for _, matcher := range []evaluatorRuntimeCallMatcher{
		identifierCall("newCodexAuthSession"), identifierCall("agentRuntimeVersion"), identifierCall("atlRuntimeVersion"), identifierCall("runHeadlessOnce"),
	} {
		position := token.NoPos
		ast.Inspect(body.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if ok && !position.IsValid() && matcher(call) {
				position = call.Pos()
			}
			return true
		})
		if !position.IsValid() || position <= returnPosition {
			t.Fatal("runtime mode contract invalid")
		}
	}
}

func selectorExpression(expression ast.Expr, receiver, field string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == receiver
}

func expressionPath(expression ast.Expr, want ...string) bool {
	if len(want) == 0 {
		return false
	}
	switch typed := expression.(type) {
	case *ast.Ident:
		return len(want) == 1 && typed.Name == want[0]
	case *ast.SelectorExpr:
		return len(want) > 1 && typed.Sel.Name == want[len(want)-1] && expressionPath(typed.X, want[:len(want)-1]...)
	default:
		return false
	}
}

func TestEvaluatorRuntimeModeHeadlessDryRunCreatesOnlyMarker(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "executed")
	writeProbe := func(name string) string {
		path := filepath.Join(t.TempDir(), name)
		content := fmt.Sprintf("#!/bin/sh\nprintf invoked >> %q\nexit 97\n", logPath)
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	outputRoot := filepath.Join(t.TempDir(), "output")
	output, err := RunHeadless(context.Background(), RunOptions{
		SpecPath:          filepath.Join(repository, "benchmarks", "agent-eval", "jira-structure-subtree-export", "run.cli.codex.json"),
		OutputRoot:        outputRoot,
		RepositoryRoot:    repository,
		AgentBinary:       writeProbe("agent"),
		ATLBinary:         writeProbe("atl"),
		PluginRoot:        repository,
		WrapperExecutable: writeProbe("wrapper"),
		DryRun:            true,
	})
	if err != nil || len(output.Results) != 0 || output.Preview.BackendMode != BackendModeSynthetic {
		t.Fatal("runtime mode contract invalid")
	}
	marker, err := os.ReadFile(filepath.Join(outputRoot, privateOutputRootMarker))
	if err != nil || string(marker) != privateOutputRootMarkerContents {
		t.Fatal("runtime mode contract invalid")
	}
	entries := []string{}
	if err := filepath.WalkDir(outputRoot, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != outputRoot {
			relative, err := filepath.Rel(outputRoot, path)
			if err != nil {
				return err
			}
			entries = append(entries, filepath.ToSlash(relative))
		}
		return nil
	}); err != nil || len(entries) != 1 || entries[0] != privateOutputRootMarker {
		t.Fatal("runtime mode contract invalid")
	}
	if data, err := os.ReadFile(logPath); err == nil || len(data) != 0 {
		t.Fatal("runtime mode contract invalid")
	}
	assertRuntimeModeDryRunReturnOrder(t)
}

func TestEvaluatorRuntimeModeCommitmentAndProbeOrdering(t *testing.T) {
	assertRuntimeModeCallOrder(t, "provider_attempt.go", "executeProviderAttempt",
		identifierCall("commit"), identifierCall("revalidate"), selectorCall("command", "Start"), selectorCall("command", "Wait"))
	assertRuntimeModeConditionalAssignment(t, "runner_provider.go", "executeAndCloseHeadlessProvider", "codexPrivateCLI", "revalidateProvider", "input", "bindings", "providerRuntime", "verifyPluginPackage")
	assertRuntimeModeCallCount(t, "runner_provider.go", "executeAndCloseHeadlessProvider", identifierCallWithArgumentPaths("executeProviderAttempt",
		[]string{"input", "command"}, []string{"input", "bindings", "providerAttemptCommitted"}, []string{"revalidateProvider"}), 1)
	assertRuntimeModeCallCount(t, "calibration.go", "RunCodexCLICalibration", identifierCallWithArgumentPaths("executeProviderAttempt",
		[]string{"command"}, []string{"options", "providerAttemptCommitted"}, []string{"providerRuntime", "verifyPluginPackage"}), 1)
	assertRuntimeModeCallOrder(t, "private_review_runner.go", "RunPrivateReview",
		selectorCallWithIdentifierArgument("safepath", "WriteFileExclusiveWithin", "attemptPath"), identifierCall("loadPrivateReviewInputs"), identifierCall("privateReviewRunProvider"))
	assertRuntimeModeCallOrder(t, "tool_availability.go", "QualifyCodexCLIToolAvailability",
		identifierCall("preparePrivateProbeAgent"), selectorCall("command", "Run"))
	assertRuntimeModeCallOrder(t, "cli_route_qualification.go", "QualifyCLIRoute",
		identifierCall("preparePrivateProbeAgent"), selectorCall("command", "Run"))
	assertRuntimeModeCallOrder(t, "runner_provider.go", "executeAndCloseHeadlessProvider",
		identifierCall("executeProviderAttempt"),
		selectorCallWithReceiverPath([]string{"input", "resources", "commandBroker"}, "Close"),
		selectorCallWithReceiverPath([]string{"input", "resources", "liveGateway"}, "Close"),
		selectorCallWithReceiverPath([]string{"input", "resources", "externalProxy"}, "closeBounded"),
		identifierCall("close"), selectorCall("time", "Since"),
		selectorCallWithReceiverPath([]string{"input", "transcript"}, "Close"),
		selectorCallWithReceiverPath([]string{"input", "stderr"}, "Close"))
	assertRuntimeModeCallOrder(t, "runner_provider.go", "closeDeferred",
		selectorCallWithReceiverPath([]string{"resources", "commandBroker"}, "Close"),
		selectorCallWithReceiverPath([]string{"resources", "liveGateway"}, "Close"),
		selectorCallWithReceiverPath([]string{"resources", "externalProxy"}, "closeBounded"),
		selectorCall("os", "RemoveAll"), selectorCallWithReceiverPath([]string{"resources", "backend"}, "Close"))
	assertHeadlessExecutionErrorPrecedence(t)
}

func TestEvaluatorRuntimeModeReceiptPersistenceRemainsOuterAndRevalidated(t *testing.T) {
	assertRuntimeModeCallCount(t, "runner.go", "RunHeadless", identifierCall("writeSyntheticRunReceipt"), 1)
	assertRuntimeModeCallCount(t, "runner.go", "runHeadlessOnce", identifierCall("writeSyntheticRunReceipt"), 0)
	assertRuntimeModeCallCount(t, "runner_outcome.go", "finalizeHeadlessOutcome", identifierCall("writeSyntheticRunReceipt"), 0)
	assertRuntimeModeReceiptPersistenceGuards(t)
}
