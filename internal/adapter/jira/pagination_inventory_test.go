package jira

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestJiraPaginationOwnerInventoryIsClosed(t *testing.T) {
	want := map[string][]string{
		"agile.go:Boards":                         {"agileNext", "requestStartAt"},
		"agile.go:SprintIssues":                   {"agileNext", "requestStartAt"},
		"agile.go:Sprints":                        {"agileNext", "requestStartAt"},
		"agile.go:agileNext":                      {"advance", "matches", "requested", "requested", "requested", "requested"},
		"agile.go:boardIssuePage":                 {"agileNext", "requestStartAt"},
		"create_metadata.go:readCreateFields":     {"advance", "matches", "requestStartAt", "requested", "requested"},
		"create_metadata.go:readCreateIssueTypes": {"advance", "matches", "requestStartAt", "requested", "requested"},
		"jira.go:CompleteChangelog":               {"advance", "matches", "requestStartAt", "requested", "requested", "requested"},
		"jira.go:ListComments":                    {"advance", "matches", "requestStartAt", "requested", "requested", "requested", "requested", "requested", "requested"},
		"jira.go:searchPage":                      {"advance", "matches", "requestStartAt"},
		"worklogs.go:ListIssueWorklogs":           {"advance", "matches", "requestStartAt", "requested", "requested", "requested", "requested", "requested", "requested"},
	}
	trackedSelectors := map[string]bool{"advance": true, "matches": true, "requested": true}
	got := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			key := entry.Name() + ":" + function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr == nil && strings.Contains(value, "startAt=%") {
						got[key] = append(got[key], "requestStartAt")
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch called := call.Fun.(type) {
				case *ast.Ident:
					if called.Name == "agileNext" {
						got[key] = append(got[key], called.Name)
					}
				case *ast.SelectorExpr:
					if trackedSelectors[called.Sel.Name] {
						got[key] = append(got[key], called.Sel.Name)
					}
					if called.Sel.Name == "Set" && len(call.Args) > 0 {
						if literal, literalOK := call.Args[0].(*ast.BasicLit); literalOK {
							value, unquoteErr := strconv.Unquote(literal.Value)
							if unquoteErr == nil && value == "startAt" {
								got[key] = append(got[key], "requestStartAt")
							}
						}
					}
				}
				return true
			})
		}
	}
	for key := range got {
		sort.Strings(got[key])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Jira checked-pagination owners = %#v, want exactly %#v", got, want)
	}
}
