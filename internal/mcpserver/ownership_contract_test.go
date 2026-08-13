package mcpserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestMCPSourceOwnersStaySeparated(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	directory := filepath.Dir(current)
	want := map[string]string{
		"ConfluenceSearchInput":              "confluence_wire.go",
		"JiraIssueSearchInput":               "jira_wire.go",
		"NewForService":                      "server.go",
		"ServeService":                       "stdio.go",
		"normalizeSDKSchemaValidationErrors": "schema.go",
		"validateConfluenceSectionsResult":   "confluence_validation.go",
		"validateStructureView":              "jira_validation.go",
	}
	got := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Base(path) == filepath.Base(current) ||
			len(entry.Name()) >= len("_test.go") && entry.Name()[len(entry.Name())-len("_test.go"):] == "_test.go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if _, tracked := want[declaration.Name.Name]; tracked {
					got[declaration.Name.Name] = filepath.Base(path)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if ok {
						if _, tracked := want[typeSpec.Name.Name]; tracked {
							got[typeSpec.Name.Name] = filepath.Base(path)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range want {
		if got[name] != file {
			t.Errorf("%s owner=%q want %q", name, got[name], file)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("tracked declarations=%v want %v", got, want)
	}
}

func TestToolDescriptorsCannotRegisterHandlers(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	directory := filepath.Dir(current)
	registrations := []string{}
	descriptorHasServerParameter := false
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Base(path) == filepath.Base(current) ||
			len(entry.Name()) >= len("_test.go") && entry.Name()[len(entry.Name())-len("_test.go"):] == "_test.go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if function.Name.Name == "readOnlyTool" {
				ast.Inspect(function.Type.Params, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if ok && selector.Sel.Name == "Server" {
						descriptorHasServerParameter = true
					}
					return true
				})
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				owner, ownerOK := selector.X.(*ast.Ident)
				if ownerOK && owner.Name == "mcp" && selector.Sel.Name == "AddTool" {
					registrations = append(registrations, filepath.Base(path)+":"+function.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(registrations)
	if descriptorHasServerParameter {
		t.Fatal("readOnlyTool descriptor unexpectedly accepts an MCP server")
	}
	if len(registrations) != 1 || registrations[0] != "schema.go:addReadOnlyTool" {
		t.Fatalf("direct handler registrations=%v want [schema.go:addReadOnlyTool]", registrations)
	}
}
