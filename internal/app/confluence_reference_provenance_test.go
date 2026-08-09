package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reference provenance is fail-open if a caller extracts ID but forgets to
// thread resolution.Context. Keep this oracle exact beside the write-clearance
// inventory so every direct or injected resolver caller requires an explicit
// review.
func TestEveryConfluenceReferenceResolutionThreadsProvenance(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") || entry.Name() == "confluence_reference.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok || len(assignment.Lhs) == 0 || len(assignment.Rhs) == 0 {
					return true
				}
				call, ok := assignment.Rhs[0].(*ast.CallExpr)
				if !ok || !isPageReferenceResolverCall(call) {
					return true
				}
				resolved, ok := assignment.Lhs[0].(*ast.Ident)
				if !ok {
					t.Fatalf("%s:%s assigns page reference resolution to a non-identifier", entry.Name(), function.Name.Name)
				}
				if !threadsResolutionContextAfter(function.Body, assignment.End(), resolved.Name) {
					t.Fatalf("%s:%s does not thread %s.Context(ctx) after page reference resolution", entry.Name(), function.Name.Name, resolved.Name)
				}
				key := entry.Name() + ":" + function.Name.Name
				if callers[key] {
					t.Fatalf("multiple page reference resolver calls in %s require a more precise oracle", key)
				}
				callers[key] = true
				return true
			})
		}
	}
	if len(callers) != 17 {
		t.Fatalf("provenance-threaded ResolvePageReference callers=%d, want exact inventory 17: %v", len(callers), callers)
	}
}

func isPageReferenceResolverCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && (selector.Sel.Name == "ResolvePageReference" || selector.Sel.Name == "resolveReference")
}

func threadsResolutionContextAfter(body *ast.BlockStmt, after token.Pos, resolved string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found || node == nil || node.Pos() <= after {
			return true
		}
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		lhs, lhsOK := assignment.Lhs[0].(*ast.Ident)
		call, callOK := assignment.Rhs[0].(*ast.CallExpr)
		if !lhsOK || lhs.Name != "ctx" || !callOK || len(call.Args) != 1 {
			return true
		}
		selector, selectorOK := call.Fun.(*ast.SelectorExpr)
		receiver, receiverOK := func() (*ast.Ident, bool) {
			if !selectorOK {
				return nil, false
			}
			value, ok := selector.X.(*ast.Ident)
			return value, ok
		}()
		argument, argumentOK := call.Args[0].(*ast.Ident)
		found = selectorOK && selector.Sel.Name == "Context" && receiverOK && receiver.Name == resolved && argumentOK && argument.Name == "ctx"
		return true
	})
	return found
}
