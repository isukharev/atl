package compose

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInnerEntrypointsDoNotImportAdapters(t *testing.T) {
	for _, root := range []string{"../cli", "../mcpserver"} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err == nil && strings.Contains(importPath, "/internal/adapter/") {
					t.Errorf("%s imports concrete adapter %s; use internal/compose", path, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
}

func TestHTTPClientConstructionAndImmutablePolicyInventory(t *testing.T) {
	expectedConstructors := map[string]bool{
		"adapter/confluence/confluence.go:NewWithScheduler:httpx.NewWithScheduler":       false,
		"adapter/confluence/confluence.go:NewWithSchedulerTLS:httpx.NewWithSchedulerTLS": false,
		"adapter/jira/jira.go:NewWithScheduler:httpx.NewWithScheduler":                   false,
		"adapter/jira/jira.go:NewWithSchedulerTLS:httpx.NewWithSchedulerTLS":             false,
	}
	expectedPolicyCalls := map[string]map[string]int{
		"adapter/confluence/confluence.go": {"WithRequiredWriteClearance": 1, "WithTrace": 1},
		"adapter/jira/jira.go":             {"WithGenericConflict": 1, "WithRequiredWriteClearance": 1, "WithTrace": 1},
	}
	bannedMutablePolicy := map[string]bool{
		"SetTrace": true, "SetNoVersionGate": true, "RequireWriteClearance": true,
	}
	constructors := map[string]bool{"New": true, "NewWithScheduler": true, "NewWithSchedulerTLS": true}

	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(filepath.ToSlash(path), "../")
		httpxAliases := map[string]bool{}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil || importPath != "github.com/isukharev/atl/internal/httpx" {
				continue
			}
			name := "httpx"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." || name == "_" {
				t.Errorf("%s imports internal/httpx as %q; use a named import so constructor ownership stays visible", relative, name)
				continue
			}
			httpxAliases[name] = true
		}
		directCallFunctions := map[*ast.SelectorExpr]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				directCallFunctions[selector] = true
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if bannedMutablePolicy[function.Name.Name] {
				t.Errorf("%s declares removed mutable HTTP policy %s", relative, function.Name.Name)
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if selector, ok := node.(*ast.SelectorExpr); ok {
					qualifier, qualified := selector.X.(*ast.Ident)
					if qualified && httpxAliases[qualifier.Name] && constructors[selector.Sel.Name] && !directCallFunctions[selector] {
						t.Errorf("%s:%s references raw HTTP constructor httpx.%s outside the exact direct call position", relative, function.Name.Name, selector.Sel.Name)
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if bannedMutablePolicy[selector.Sel.Name] {
					t.Errorf("%s:%s calls removed mutable HTTP policy %s", relative, function.Name.Name, selector.Sel.Name)
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if !ok || !httpxAliases[qualifier.Name] {
					return true
				}
				if function.Name.Name == "transportOptions" {
					if expected := expectedPolicyCalls[relative]; expected != nil {
						expected[selector.Sel.Name]--
					}
				}
				if !constructors[selector.Sel.Name] {
					return true
				}
				key := relative + ":" + function.Name.Name + ":httpx." + selector.Sel.Name
				if _, ok := expectedConstructors[key]; !ok {
					t.Errorf("unreviewed raw HTTP client construction %s", key)
					return true
				}
				expectedConstructors[key] = true
				if call.Ellipsis == token.NoPos || len(call.Args) == 0 {
					t.Errorf("%s does not spread resolved immutable policy options", key)
					return true
				}
				resolved, ok := call.Args[len(call.Args)-1].(*ast.CallExpr)
				if !ok {
					t.Errorf("%s last argument is not transportOptions(resolved)", key)
					return true
				}
				name, ok := resolved.Fun.(*ast.Ident)
				if !ok || name.Name != "transportOptions" {
					t.Errorf("%s last argument is not transportOptions(resolved)", key)
					return true
				}
				if len(resolved.Args) != 1 {
					t.Errorf("%s transportOptions argument count = %d", key, len(resolved.Args))
					return true
				}
				argument, ok := resolved.Args[0].(*ast.Ident)
				if !ok || argument.Name != "resolved" {
					t.Errorf("%s does not pass the resolved adapter options", key)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for constructor, found := range expectedConstructors {
		if !found {
			t.Errorf("missing reviewed raw HTTP client construction %s", constructor)
		}
	}
	for path, expected := range expectedPolicyCalls {
		for policy, remaining := range expected {
			if remaining != 0 {
				t.Errorf("%s immutable policy %s count drifted by %d", path, policy, -remaining)
			}
		}
	}
}
