package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestJiraGuardedExecutionComposesUsageAndPreservesAbsoluteDeadline(t *testing.T) {
	parent, err := domain.NewReadBudget(3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	_, finishParent, err := parent.BeginResponse(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	finishParent(2)
	incomingDeadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(domain.WithReadBudget(t.Context(), parent), incomingDeadline)
	defer cancel()
	execution, err := newJiraGuardedExecution(ctx, parent, 1, 7, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	if domain.ReadBudgetFromContext(execution.ctx) != execution.budget || !domain.SingleAttempt(execution.ctx) || !domain.RedactedHTTPTrace(execution.ctx) {
		t.Fatal("execution context did not carry the exact row budget and request policy")
	}
	if !execution.deadline.Equal(incomingDeadline) {
		t.Fatalf("deadline=%v want=%v", execution.deadline, incomingDeadline)
	}
	if err := execution.budget.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	remaining, finish, err := execution.budget.BeginResponse(t.Context())
	if err != nil || remaining != 7 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	finish(5)
	if execution.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 5}) {
		t.Fatalf("row usage=%+v", execution.Usage())
	}
	if parent.Usage() != (domain.ReadBudgetUsage{Attempts: 2, ResponseBytes: 7}) {
		t.Fatalf("parent usage=%+v", parent.Usage())
	}

	cancel()
	closeout, closeoutCancel := execution.Closeout()
	defer closeoutCancel()
	if closeout.Err() != nil {
		t.Fatalf("caller cancellation suppressed closeout: %v", closeout.Err())
	}
	if domain.ReadBudgetFromContext(closeout) != execution.budget || !domain.SingleAttempt(closeout) || !domain.RedactedHTTPTrace(closeout) {
		t.Fatal("closeout did not preserve the exact row budget and request policy")
	}
	if got, ok := closeout.Deadline(); !ok || !got.Equal(incomingDeadline) {
		t.Fatalf("closeout deadline=%v present=%t", got, ok)
	}
}

func TestPreparedGuardedCoreCallAndSafetyInventory(t *testing.T) {
	cores := map[string]string{
		"guardedLinkPreparedCore":       "GuardedLink",
		"guardedLabelsPreparedCore":     "GuardedLabels",
		"addCommentGuardedPreparedCore": "AddCommentGuarded",
		"setFieldsGuardedPreparedCore":  "SetFieldsGuarded",
	}
	constructors := map[string]map[string]bool{
		"newJiraGuardedExecution": {"GuardedLink": true, "GuardedLabels": true, "AddCommentGuarded": true, "SetFieldsGuarded": true, "qualifyJiraPlanRow": true},
	}
	banned := map[string]bool{
		"NewReadBudget": true, "NewChildReadBudget": true, "WithTimeout": true,
		"WithDeadline": true, "UpdateLabels": true, "AddComment": true,
		"UpdateIssueFields": true, "Link": true, "DeleteLink": true,
	}
	callers := make(map[string][]string, len(cores))
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range preparedGuardedReferenceViolations(parsed, cores, constructors, banned) {
			t.Errorf("%s: %s", path, violation)
		}
		if strings.HasPrefix(filepath.Base(path), "jira_plan_") {
			for _, violation := range jiraPlanSafetyReferenceViolations(parsed, cores) {
				t.Errorf("%s: %s", path, violation)
			}
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			calledSelectors := make(map[*ast.SelectorExpr]bool)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					calledSelectors[selector] = true
				}
				return true
			})
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if selector, ok := node.(*ast.SelectorExpr); ok {
					if _, prepared := cores[selector.Sel.Name]; prepared && !calledSelectors[selector] {
						t.Errorf("prepared core %s captured as a function value in %s", selector.Sel.Name, function.Name.Name)
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch target := call.Fun.(type) {
				case *ast.Ident:
					name = target.Name
				case *ast.SelectorExpr:
					name = target.Sel.Name
				}
				if _, prepared := cores[name]; prepared {
					callers[name] = append(callers[name], function.Name.Name)
				}
				if allowed, constructor := constructors[name]; constructor && !allowed[function.Name.Name] {
					t.Errorf("execution constructor called from %s", function.Name.Name)
				}
				if _, constructor := constructors[name]; constructor {
					callers[name] = append(callers[name], function.Name.Name)
				}
				if _, prepared := cores[function.Name.Name]; prepared && banned[name] {
					t.Errorf("prepared core %s calls banned %s", function.Name.Name, name)
				}
				return true
			})
		}
	}
	for core, wrapper := range cores {
		got := callers[core]
		if len(got) != 2 || got[0] != wrapper || got[1] != "executeJiraPlanRow" {
			t.Errorf("%s callers=%v want [%s executeJiraPlanRow]", core, got, wrapper)
		}
	}
	for constructor, allowed := range constructors {
		got := callers[constructor]
		if len(got) != len(allowed) {
			t.Errorf("%s callers=%v", constructor, got)
		}
		counts := make(map[string]int, len(got))
		for _, caller := range got {
			counts[caller]++
		}
		for caller := range allowed {
			if counts[caller] != 1 {
				t.Errorf("%s caller %s count=%d", constructor, caller, counts[caller])
			}
		}
	}
}

func jiraPlanSafetyReferenceViolations(parsed *ast.File, cores map[string]string) []string {
	allowedCalls := map[string]map[string]bool{
		"NewReadBudget":                 {"RunJiraPlan": true},
		"newJiraGuardedExecution":       {"qualifyJiraPlanRow": true},
		"guardedLinkPreparedCore":       {"executeJiraPlanRow": true},
		"guardedLabelsPreparedCore":     {"executeJiraPlanRow": true},
		"addCommentGuardedPreparedCore": {"executeJiraPlanRow": true},
		"setFieldsGuardedPreparedCore":  {"executeJiraPlanRow": true},
	}
	banned := map[string]bool{
		"Comment": true, "Update": true, "Link": true, "UpdateLabels": true,
		"GuardedLink": true, "GuardedLabels": true, "AddCommentGuarded": true, "SetFieldsGuarded": true,
		"NewChildReadBudget": true, "WithTimeout": true, "WithDeadline": true,
	}
	for core := range cores {
		banned[core] = true
	}
	for name := range allowedCalls {
		banned[name] = true
	}
	var violations []string
	for _, declaration := range parsed.Decls {
		owner := "package scope"
		root := ast.Node(declaration)
		if function, ok := declaration.(*ast.FuncDecl); ok {
			if function.Body == nil {
				continue
			}
			owner = function.Name.Name
			root = function.Body
		}
		direct := map[ast.Node]bool{}
		ast.Inspect(root, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				direct[call.Fun] = true
				if selector, selectorOK := call.Fun.(*ast.SelectorExpr); selectorOK {
					direct[selector.Sel] = true
				}
			}
			return true
		})
		nested := map[ast.Node]bool{}
		ast.Inspect(root, func(node ast.Node) bool {
			literal, ok := node.(*ast.FuncLit)
			if !ok {
				return true
			}
			ast.Inspect(literal, func(inner ast.Node) bool {
				if inner != nil {
					nested[inner] = true
				}
				return true
			})
			return false
		})
		ast.Inspect(root, func(node ast.Node) bool {
			name := ""
			switch value := node.(type) {
			case *ast.SelectorExpr:
				name = value.Sel.Name
			case *ast.Ident:
				name = value.Name
			default:
				return true
			}
			if !banned[name] {
				return true
			}
			if nested[node] || !direct[node] || !allowedCalls[name][owner] {
				violations = append(violations, owner+": unsafe plan reference: "+name)
			}
			return true
		})
	}
	return violations
}

func preparedGuardedReferenceViolations(parsed *ast.File, cores map[string]string, constructors map[string]map[string]bool, banned map[string]bool) []string {
	directCallTargets := make(map[ast.Node]bool)
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		directCallTargets[call.Fun] = true
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			directCallTargets[selector.Sel] = true
		}
		return true
	})

	sensitive := make(map[string]bool, len(cores)+len(constructors)+len(banned))
	for name := range cores {
		sensitive[name] = true
	}
	for name := range constructors {
		sensitive[name] = true
	}
	for name := range banned {
		sensitive[name] = true
	}

	var violations []string
	checkReference := func(owner, name string, node ast.Node, inCore, topLevel bool) {
		if topLevel && sensitive[name] {
			violations = append(violations, owner+": package-level capture of "+name)
		}
		if _, prepared := cores[name]; prepared && !directCallTargets[node] {
			violations = append(violations, owner+": prepared core captured as function value: "+name)
		}
		if _, constructor := constructors[name]; constructor && !directCallTargets[node] {
			violations = append(violations, owner+": execution control captured as function value: "+name)
		}
		if inCore && banned[name] {
			violations = append(violations, owner+": banned prepared-core reference: "+name)
		}
	}
	inspect := func(owner string, root ast.Node, inCore, topLevel bool) {
		ast.Inspect(root, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				checkReference(owner, value.Sel.Name, value, inCore, topLevel)
			case *ast.Ident:
				checkReference(owner, value.Name, value, inCore, topLevel)
			}
			return true
		})
	}
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			inspect("package scope", value, false, true)
		case *ast.FuncDecl:
			if value.Body != nil {
				_, inCore := cores[value.Name.Name]
				inCore = inCore || value.Name.Name == "executeJiraPlanRow" || value.Name.Name == "qualifyJiraPlanRow"
				inspect(value.Name.Name, value.Body, inCore, false)
			}
		}
	}
	return violations
}

func TestPreparedGuardedReferenceInventoryRejectsAliasesAndTopLevelCaptures(t *testing.T) {
	cores := map[string]string{"guardedLinkPreparedCore": "GuardedLink"}
	constructors := map[string]map[string]bool{"newJiraGuardedExecution": {"GuardedLink": true}}
	banned := map[string]bool{
		"NewReadBudget": true, "NewChildReadBudget": true, "WithTimeout": true, "WithDeadline": true,
		"UpdateLabels": true, "AddComment": true, "UpdateIssueFields": true, "Link": true, "DeleteLink": true,
	}
	for _, test := range []struct {
		name, source string
	}{
		{name: "legacy writer alias", source: `package app
func (s *JiraService) guardedLinkPreparedCore() { legacy := s.UpdateLabels; legacy() }`},
		{name: "budget constructor alias", source: `package app
func (s *JiraService) guardedLinkPreparedCore() { fresh := domain.NewReadBudget; _, _ = fresh(1, 1) }`},
		{name: "package constructor capture", source: `package app
var fresh = domain.NewReadBudget`},
		{name: "package core capture", source: `package app
var prepared = (*JiraService).guardedLinkPreparedCore`},
		{name: "execution control alias", source: `package app
func GuardedLink() { control := newJiraGuardedExecution; _ = control }`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if violations := preparedGuardedReferenceViolations(parsed, cores, constructors, banned); len(violations) == 0 {
				t.Fatal("unsafe reference unexpectedly passed inventory")
			}
		})
	}
}

func TestJiraPlanSafetyInventoryRejectsLegacyWrappersAliasesAndNestedControls(t *testing.T) {
	cores := map[string]string{"guardedLinkPreparedCore": "GuardedLink"}
	for _, source := range []string{
		`package app; func executeJiraPlanRow() { legacy := (*JiraService).UpdateLabels; _ = legacy }`,
		`package app; func executeJiraPlanRow(port interface{ Comment() }) { port.Comment() }`,
		`package app; func executeJiraPlanRow(port interface{ Update() }) { update := port.Update; update() }`,
		`package app; func executeJiraPlanRow(s *JiraService) { s.GuardedLink(nil, JiraGuardedLinkOpts{}) }`,
		`package app; var guarded = (*JiraService).GuardedLabels`,
		`package app; func helper() { _ = domain.NewReadBudget }`,
		`package app; func helper() { context.WithDeadline(nil, time.Time{}) }`,
		`package app; func helper(s *JiraService) { s.guardedLinkPreparedCore(nil, nil, JiraGuardedLinkOpts{}, nil) }`,
		`package app; func RunJiraPlan() { nested := func() { _, _ = domain.NewReadBudget(1, 1) }; nested() }`,
		`package app; func executeJiraPlanRow(s *JiraService) { nested := func() { s.guardedLinkPreparedCore(nil, nil, JiraGuardedLinkOpts{}, nil) }; nested() }`,
		`package app; func qualifyJiraPlanRow() { nested := func() { _, _ = newJiraGuardedExecution(nil, nil, 1, 1, 1) }; nested() }`,
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), "jira_plan_fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if violations := jiraPlanSafetyReferenceViolations(parsed, cores); len(violations) == 0 {
			t.Fatalf("unsafe plan reference passed: %s", source)
		}
	}
}
