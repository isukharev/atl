package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGuardedWriteSingleAttemptPortInventory(t *testing.T) {
	want := map[string]bool{
		"confluence_labels.go:MutateLabelsGuarded:writer:AddContentLabels":   true,
		"confluence_labels.go:MutateLabelsGuarded:writer:RemoveContentLabel": true,
		"jira_watchers.go:MutateWatcherGuarded:writer:AddIssueWatcher":       true,
		"jira_watchers.go:MutateWatcherGuarded:writer:RemoveIssueWatcher":    true,
		"jira_worklogs.go:AddWorklogGuarded:writer:AddIssueWorklog":          true,
		"confluence_title.go:SetTitleGuarded:s.store:UpdatePage":             true,
		"confluence_move.go:MoveGuarded:s.store:MovePage":                    true,

		// These bulk/mirror owners share port methods with the guarded paths,
		// but their lifecycle semantics are deliberately outside this slice.
		"confluence.go:pushOne:s.store:UpdatePage":                false,
		"confluence_plan.go:runConfluencePlan:s.store:UpdatePage": false,
		"jira_sync.go:jiraPushOne:s.tr:SetFields":                 false,
	}
	methods := map[string]bool{
		"AddContentLabels": true, "RemoveContentLabel": true,
		"AddIssueWatcher": true, "RemoveIssueWatcher": true,
		"AddIssueWorklog": true, "SetFields": true,
		"UpdatePage": true, "MovePage": true,
	}

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob production Go files: %v", err)
	}
	got := make(map[string]bool)
	allCalls := 0
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if _, _, ok := guardedWritePortCall(node, methods); ok {
				allCalls++
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, selector, ok := guardedWritePortCall(node, methods)
				if !ok {
					return true
				}
				method := selector.Sel.Name
				receiver := guardedWriteReceiver(selector.X)
				key := filepath.Base(path) + ":" + function.Name.Name + ":" + receiver + ":" + method
				if _, exists := got[key]; exists {
					t.Fatalf("%s: multiple production calls to scoped write port %s", path, key)
				}
				got[key] = len(call.Args) > 0 && isSingleAttemptContext(call.Args[0])
				return true
			})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("production guarded-write port inventory = %#v, want exactly %#v", got, want)
	}
	if allCalls != len(got) {
		t.Fatalf("all production calls to scoped write ports = %d, classified function calls = %d", allCalls, len(got))
	}
}

func guardedWritePortCall(node ast.Node, methods map[string]bool) (*ast.CallExpr, *ast.SelectorExpr, bool) {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !methods[selector.Sel.Name] {
		return nil, nil, false
	}
	return call, selector, true
}

func guardedWriteReceiver(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := guardedWriteReceiver(value.X)
		if prefix == "<unsupported>" {
			return prefix
		}
		return prefix + "." + value.Sel.Name
	default:
		return "<unsupported>"
	}
}

func isSingleAttemptContext(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "WithSingleAttempt" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "domain" {
		return false
	}
	ctx, ok := call.Args[0].(*ast.Ident)
	return ok && ctx.Name == "ctx"
}
