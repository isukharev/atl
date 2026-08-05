package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// processRoleContract is intentionally a test-local oracle. It records the
// process names and lifecycle expected by the evaluator without deriving them
// from an evaluator registry or launching a provider process.
type processRoleContract struct {
	Role                            string
	WorkerBasenames                 []string
	RepresentativeFallbackBasenames []string
	Fallback                        bool
	Handler                         string
	Owner                           string
	UpstreamContact                 string
	SpawnCardinality                string
	Cleanup                         string
}

var agentEvalProcessRoleContracts = []processRoleContract{
	{
		Role:                            "coordinator",
		RepresentativeFallbackBasenames: []string{"agent-eval", "agent-eval.exe"},
		Fallback:                        true,
		Handler:                         "run",
		Owner:                           "agent-eval coordinator",
		UpstreamContact:                 "maintainer invocation",
		SpawnCardinality:                "one process per evaluator command",
		Cleanup:                         "exits after one command; the private run directory owns retained artifacts",
	},
	{
		Role:             "guard",
		WorkerBasenames:  []string{"atl-eval-guard", "atl-eval-guard.exe"},
		Handler:          "runClaudeBashGuard",
		Owner:            "provider hook guard",
		UpstreamContact:  "Claude Code pre-tool hook",
		SpawnCardinality: "zero or more processes per provider run",
		Cleanup:          "exits after one hook decision; the private run directory retains guard records",
	},
	{
		Role:             "confinement-probe",
		WorkerBasenames:  []string{"atl-eval-confinement-probe", "atl-eval-confinement-probe.exe"},
		Handler:          "runCommandBrokerProbe",
		Owner:            "Codex CLI confinement preflight",
		UpstreamContact:  "Codex CLI confinement preflight",
		SpawnCardinality: "exactly one process for each brokered Codex CLI preflight",
		Cleanup:          "exits after one probe; the private run directory retains the probe binary",
	},
	{
		Role:             "accounting-proxy",
		WorkerBasenames:  []string{"atl", "atl.exe"},
		Handler:          "runATLProxy",
		Owner:            "evaluator CLI accounting proxy",
		UpstreamContact:  "provider model shell",
		SpawnCardinality: "zero or more processes per provider run",
		Cleanup:          "exits after one atl invocation; the private run directory retains audit records",
	},
	{
		Role:             "bounded-reader",
		WorkerBasenames:  []string{"cat", "sed", "wc"},
		Handler:          "runSkillReader",
		Owner:            "evaluator bounded reader",
		UpstreamContact:  "provider model shell",
		SpawnCardinality: "zero or more processes per eligible CLI run",
		Cleanup:          "exits after one bounded read; the private run directory retains the reader binary",
	},
	{
		Role:             "reviewed-write-shim",
		WorkerBasenames:  []string{"env"},
		Handler:          "runReviewedWriteEnv",
		Owner:            "evaluator reviewed-write shim",
		UpstreamContact:  "provider model shell",
		SpawnCardinality: "zero or more processes per write-enabled CLI run",
		Cleanup:          "exits after one reviewed-write dispatch; the private run directory retains the shim binary",
	},
}

func TestAgentEvalProcessRoleContract(t *testing.T) {
	if err := validateAgentEvalProcessRoleContracts(agentEvalProcessRoleContracts); err != nil {
		t.Fatal(err)
	}

	dispatch, hasCoordinatorFallback := parseAgentEvalBasenameDispatch(t)
	if !hasCoordinatorFallback {
		t.Fatal("process role contract mismatch")
	}
	for _, contract := range agentEvalProcessRoleContracts {
		for _, basename := range contract.WorkerBasenames {
			if got := dispatch[basename]; got != contract.Handler {
				t.Fatalf("process role contract mismatch")
			}
		}
		for _, basename := range contract.RepresentativeFallbackBasenames {
			if _, explicit := dispatch[basename]; explicit {
				t.Fatal("process role contract mismatch")
			}
		}
	}
	if len(dispatch) != 10 {
		t.Fatal("process role contract mismatch")
	}
}

func TestAgentEvalProcessRoleContractRejectsClosedSetDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]processRoleContract) []processRoleContract
	}{
		{
			name: "duplicate",
			mutate: func(contracts []processRoleContract) []processRoleContract {
				return append(contracts, contracts[0])
			},
		},
		{
			name: "missing",
			mutate: func(contracts []processRoleContract) []processRoleContract {
				return contracts[1:]
			},
		},
		{
			name: "unknown",
			mutate: func(contracts []processRoleContract) []processRoleContract {
				contracts[0].Role = "unknown"
				return contracts
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			contracts := cloneAgentEvalProcessRoleContracts(agentEvalProcessRoleContracts)
			if err := validateAgentEvalProcessRoleContracts(test.mutate(contracts)); !errors.Is(err, errAgentEvalProcessRoleContract) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

var errAgentEvalProcessRoleContract = errors.New("invalid agent-eval process role contract")

func validateAgentEvalProcessRoleContracts(contracts []processRoleContract) error {
	expected := map[string]struct{}{
		"coordinator": {}, "guard": {}, "confinement-probe": {}, "accounting-proxy": {}, "bounded-reader": {}, "reviewed-write-shim": {},
	}
	seenRoles := make(map[string]struct{}, len(contracts))
	seenBasenames := make(map[string]struct{})
	for _, contract := range contracts {
		if contract.Role == "" || contract.Handler == "" || contract.Owner == "" || contract.UpstreamContact == "" ||
			contract.SpawnCardinality == "" || contract.Cleanup == "" {
			return errAgentEvalProcessRoleContract
		}
		if _, ok := expected[contract.Role]; !ok {
			return errAgentEvalProcessRoleContract
		}
		if _, duplicate := seenRoles[contract.Role]; duplicate {
			return errAgentEvalProcessRoleContract
		}
		seenRoles[contract.Role] = struct{}{}
		if contract.Role == "coordinator" {
			if !contract.Fallback || len(contract.WorkerBasenames) != 0 || len(contract.RepresentativeFallbackBasenames) == 0 {
				return errAgentEvalProcessRoleContract
			}
			for _, basename := range contract.RepresentativeFallbackBasenames {
				if basename == "" {
					return errAgentEvalProcessRoleContract
				}
			}
			continue
		}
		if contract.Fallback || len(contract.RepresentativeFallbackBasenames) != 0 || len(contract.WorkerBasenames) == 0 {
			return errAgentEvalProcessRoleContract
		}
		for _, basename := range contract.WorkerBasenames {
			if basename == "" {
				return errAgentEvalProcessRoleContract
			}
			if _, duplicate := seenBasenames[basename]; duplicate {
				return errAgentEvalProcessRoleContract
			}
			seenBasenames[basename] = struct{}{}
		}
	}
	if len(seenRoles) != len(expected) {
		return errAgentEvalProcessRoleContract
	}
	return nil
}

func cloneAgentEvalProcessRoleContracts(contracts []processRoleContract) []processRoleContract {
	clone := slices.Clone(contracts)
	for index := range clone {
		clone[index].WorkerBasenames = slices.Clone(clone[index].WorkerBasenames)
		clone[index].RepresentativeFallbackBasenames = slices.Clone(clone[index].RepresentativeFallbackBasenames)
	}
	return clone
}

func parseAgentEvalBasenameDispatch(t *testing.T) (map[string]string, bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal("process role contract mismatch")
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "main" {
			continue
		}
		dispatch := make(map[string]string)
		for _, statement := range function.Body.List {
			conditional, ok := statement.(*ast.IfStmt)
			if !ok {
				continue
			}
			basenames := basenameConditions(conditional.Cond)
			handler := exitedHandler(conditional.Body)
			if len(basenames) > 0 && handler != "" {
				for _, basename := range basenames {
					dispatch[basename] = handler
				}
			}
		}
		return dispatch, hasCoordinatorFallback(function.Body)
	}
	t.Fatal("process role contract mismatch")
	return nil, false
}

func basenameConditions(expression ast.Expr) []string {
	switch expression := expression.(type) {
	case *ast.BinaryExpr:
		if expression.Op == token.LOR {
			return append(basenameConditions(expression.X), basenameConditions(expression.Y)...)
		}
		if expression.Op != token.EQL || !isBaseIdentifier(expression.X) {
			return nil
		}
		literal, ok := expression.Y.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return nil
		}
		basename, err := strconv.Unquote(literal.Value)
		if err != nil {
			return nil
		}
		return []string{basename}
	}
	return nil
}

func isBaseIdentifier(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == "base"
}

func exitedHandler(body *ast.BlockStmt) string {
	if len(body.List) != 1 {
		return ""
	}
	expressionStatement, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return ""
	}
	exitCall, ok := expressionStatement.X.(*ast.CallExpr)
	if !ok || calledIdentifier(exitCall.Fun) != "os.Exit" || len(exitCall.Args) != 1 {
		return ""
	}
	handlerCall, ok := exitCall.Args[0].(*ast.CallExpr)
	if !ok {
		return ""
	}
	return calledIdentifier(handlerCall.Fun)
}

func hasCoordinatorFallback(body *ast.BlockStmt) bool {
	lastWorkerDispatch := -1
	fallback := -1
	for index, statement := range body.List {
		conditional, ok := statement.(*ast.IfStmt)
		if !ok {
			continue
		}
		if len(basenameConditions(conditional.Cond)) > 0 && exitedHandler(conditional.Body) != "" {
			lastWorkerDispatch = index
		}
		assignment, ok := conditional.Init.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || !isErrIdentifier(assignment.Lhs[0]) {
			continue
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok || calledIdentifier(call.Fun) != "run" || len(call.Args) != 1 || !isOSArgsTail(call.Args[0]) {
			continue
		}
		fallback = index
	}
	return fallback > lastWorkerDispatch
}

func isErrIdentifier(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == "err"
}

func isOSArgsTail(expression ast.Expr) bool {
	slice, ok := expression.(*ast.SliceExpr)
	if !ok || slice.Low == nil {
		return false
	}
	low, ok := slice.Low.(*ast.BasicLit)
	if !ok || low.Kind != token.INT || low.Value != "1" {
		return false
	}
	selector, ok := slice.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Args" {
		return false
	}
	prefix, ok := selector.X.(*ast.Ident)
	return ok && prefix.Name == "os"
}

func calledIdentifier(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		prefix, ok := expression.X.(*ast.Ident)
		if ok {
			return prefix.Name + "." + expression.Sel.Name
		}
	}
	return ""
}
